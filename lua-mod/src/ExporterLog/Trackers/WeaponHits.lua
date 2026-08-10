-- ExporterLog.WeaponHits -- per-hit weapon damage AND swing/accuracy
-- tracking. CONFIRMED live (2026-08-10): Events.OnWeaponHitCharacter
-- fires (attacker, target, weapon, damage) on every hit that actually
-- connects -- verified across bare hands (~0.80), a metal baseball bat
-- (~1.86-2.15, sensibly ~2.3x harder than bare hands), and a shotgun
-- (a mix of small ~0.08-0.11 pellet hits and larger ~1.6-2.15 hits
-- from the same shell, plus one ~6.37 outlier -- consistent with
-- multiple pellets landing on the same target at once). Two kills were
-- each immediately preceded by their killing weapon_hit event, exactly
-- as expected. Events.OnWeaponSwing fires (attacker, weapon) on every
-- attack attempt regardless of whether it connects -- also confirmed
-- live: several swings fired with no matching OnWeaponHitCharacter
-- (clean misses), including one accidental shot fired into the air.
-- Both are real, implemented events now. weapon_swing carries a
-- computed `hit` boolean (see pendingSwing below for how) rather than
-- being a bare attempt-count -- a naive hits/swings ratio isn't real
-- accuracy%, since one swing can produce MORE than one weapon_hit (the
-- cleave/pellet cases above), which would let the ratio exceed 100%.
-- With `hit` on every swing, real accuracy% is just
-- COUNT(hit=true)/COUNT(*) per weapon in Postgres -- caps at 100% like
-- it should, no further Lua work needed for that (same "raw events in,
-- aggregate in SQL" principle as everything else in this mod).
--
-- weapon_swing also carries three precomputed aggregates, since a
-- multi-target shotgun blast makes "hardest hit" genuinely ambiguous
-- without them: `damage` (total across every target this swing hit --
-- the real "hardest SHOT" stat), `targetsHit` + `maxTargetDamage`
-- (distinguishes "43 damage to one zombie" from "43 spread across
-- five" -- very different outcomes damage alone can't tell apart), and
-- `maxProjectileDamage` (the hardest single pellet/hit, for whoever
-- wants that specific number too). All three computed from
-- damageByTarget, keyed by target OBJECT REFERENCE rather than a
-- resolved id -- see onWeaponHitCharacter's comment for why that's
-- simpler and doesn't depend on guessing the right getter name. A
-- later "% of max weapon damage" stat is aggregation-only -- max
-- damage per weapon is a static lookup (e.g. from pzwiki), joined
-- against the weapon/damage fields already captured here.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.WeaponHits = ExporterLog.WeaponHits or {}
ExporterLog.WeaponHits.callbacks = ExporterLog.WeaponHits.callbacks or {}

local WeaponHits = ExporterLog.WeaponHits

-- [username] = { steamId, weapon, hit } -- the most recent swing for
-- that player, held back from emitting until we know whether it
-- connected. A real accuracy% (hits landed / swings attempted, capped
-- at 100%) needs to know THIS specifically, not just independent
-- swing/hit totals -- a raw hits/swings ratio can exceed 100% since a
-- single swing can produce more than one weapon_hit (confirmed live:
-- a bat cleaving two adjacent zombies, a shotgun's multiple pellets),
-- so counting hits isn't the same as counting connected swings.
--
-- Finalized (and emitted) the moment either: (a) a hit fires for that
-- player -- marks hit=true, doesn't finalize yet, since more hits from
-- the SAME swing can still follow (multi-target/multi-pellet), or (b)
-- the player's NEXT swing fires -- by then no further hits can
-- possibly belong to the previous swing, so it's safe to finalize.
-- Every WEAPON_HIT_DIAG capture this session confirmed hits always
-- land strictly between their own swing and the next one, never after
-- -- this is what makes "finalize on next swing" a safe boundary.
--
-- Known gap, accepted: the very LAST swing of a play session (or of a
-- fight, if the player stops attacking) never gets a "next swing" to
-- trigger its finalize, so it's silently dropped -- one swing missing
-- from lifetime stats is an acceptable loss, same tolerance this mod
-- already has elsewhere (e.g. driving_distance's leftover <0.1km
-- chunk when a drive just stops).
local pendingSwing = {}

-- Simple per-session monotonic counter -- only needs to be unique
-- enough to let SQL JOIN a weapon_swing row to the weapon_hit rows
-- that belong to it (see attackId below), not globally unique across
-- restarts. Resets to 0 on every F11 reload/session start, which is
-- fine -- nothing outside this file's own in-memory correlation logic
-- ever depends on these values persisting.
local nextAttackId = 0
local function newAttackId()
    nextAttackId = nextAttackId + 1
    return nextAttackId
end

local function finalizeSwing(username)
    local pending = pendingSwing[username]
    if not pending then return end
    pendingSwing[username] = nil

    -- Derived from damageByTarget (keyed by the target OBJECT itself,
    -- not a resolved id -- see onWeaponHitCharacter's comment for why
    -- that's simpler and more robust than hunting for the right
    -- getOnlineID()/getObjectID() getter). targetsHit distinguishes
    -- "43 damage to one zombie" from "43 damage spread across five" --
    -- very different outcomes that totalDamage alone can't tell apart.
    -- maxTargetDamage is the true "hardest hit on one target" stat,
    -- which MAX(damage) on individual weapon_hit rows would understate
    -- for any weapon that lands multiple hits per target per swing
    -- (shotgun pellets bunching up on the same zombie).
    local targetsHit = 0
    local maxTargetDamage = 0
    for _, dmg in pairs(pending.damageByTarget) do
        targetsHit = targetsHit + 1
        if dmg > maxTargetDamage then maxTargetDamage = dmg end
    end

    ExporterLog.Emit.event({
        type = "weapon_swing",
        username = username,
        steamId = pending.steamId,
        weapon = pending.weapon,
        hit = pending.hit,
        attackId = pending.attackId,
        -- Sum of every weapon_hit that landed during this swing, not
        -- just the last one -- a single shotgun blast confirmed live
        -- to land up to 8 separate pellet hits at once (~43 total
        -- damage from individual hits of ~4.6-6.4 each). MAX(damage)
        -- on individual weapon_hit rows would only ever surface the
        -- hardest single PELLET, understating what really happened --
        -- MAX(damage) on weapon_swing rows instead gives the true
        -- "hardest shot" stat (summed across every target it hit).
        damage = ExporterLog.Utils.round2(pending.totalDamage),
        projectilesHit = pending.hitCount,
        maxProjectileDamage = ExporterLog.Utils.round2(pending.maxProjectileDamage),
        targetsHit = targetsHit,
        maxTargetDamage = ExporterLog.Utils.round2(maxTargetDamage),
    })
end

local function onWeaponSwing(attacker, weapon)
    local ok, err = pcall(function()
        if not attacker then return end
        local username = (attacker.getUsername and attacker:getUsername()) or "?"
        if username == "?" then return end

        -- This player's previous swing (if any) is now definitely over
        -- -- finalize and emit it before starting to buffer the new one.
        finalizeSwing(username)

        local okType, weaponType = pcall(function() return weapon and weapon:getFullType() end)

        pendingSwing[username] = {
            steamId = ExporterLog.Utils.getPlayerSteamID(attacker),
            weapon = (okType and weaponType) or "?",
            hit = false,
            totalDamage = 0,
            maxProjectileDamage = 0,
            hitCount = 0,
            attackId = newAttackId(),
            -- [target object] = summed damage this swing dealt to that
            -- specific target. Keyed by the target reference ITSELF,
            -- not a resolved id -- Lua allows any value (including
            -- userdata) as a table key, and this codebase already uses
            -- exactly this pattern elsewhere (Vehicles.lua's
            -- recentlySmashedWindows[window]). Sidesteps needing to
            -- find the right id-getter method entirely -- as long as
            -- PZ hands back the same object reference for the same
            -- real zombie across multiple pellet-hit callbacks within
            -- one shot (expected -- it's the same underlying Java
            -- object), object identity alone is enough to group them.
            damageByTarget = {},
        }
    end)
    if not ok then print(ExporterLog.Runtime.logPrefix() .. ": onWeaponSwing error: " .. tostring(err)) end
end

-- IsoZombie for every hit confirmed live so far, but the PvP idea
-- raised earlier this session ("who hits who most") means a target
-- could just as plausibly be another IsoPlayer -- checked explicitly
-- rather than assumed, same instanceof() pattern Kills.lua already
-- uses for weapon-type classification.
local function resolveTarget(target)
    if not target then return "unknown", nil end
    local okPlayer, isPlayer = pcall(function() return instanceof(target, "IsoPlayer") end)
    if okPlayer and isPlayer then
        local username = (target.getUsername and target:getUsername()) or "?"
        return "player", username
    end
    local okZombie, isZombie = pcall(function() return instanceof(target, "IsoZombie") end)
    if okZombie and isZombie then
        return "zombie", nil
    end
    return "unknown", nil
end

local function onWeaponHitCharacter(attacker, target, weapon, damage)
    local ok, err = pcall(function()
        if not attacker then return end
        local username = (attacker.getUsername and attacker:getUsername()) or "?"
        if username == "?" then return end

        -- Mark this player's currently-pending swing as connected and
        -- accumulate this hit's damage into it (both the swing total
        -- and this specific target's running total) -- doesn't
        -- finalize/emit the swing yet, since more hits from the SAME
        -- swing can still follow (a shotgun's remaining pellets,
        -- possibly against a different target). Accumulates the RAW
        -- value, not the rounded one used for the per-hit weapon_hit
        -- event below -- summing already-rounded numbers would
        -- compound rounding error across a multi-hit swing; only the
        -- final totals get rounded, in finalizeSwing().
        local pending = pendingSwing[username]
        local attackId = nil
        if pending then
            pending.hit = true
            attackId = pending.attackId
            if type(damage) == "number" then
                pending.totalDamage = pending.totalDamage + damage
                pending.hitCount = pending.hitCount + 1
                if damage > pending.maxProjectileDamage then
                    pending.maxProjectileDamage = damage
                end
                if target then
                    pending.damageByTarget[target] = (pending.damageByTarget[target] or 0) + damage
                end
            end
        end

        local targetType, targetUsername = resolveTarget(target)

        local okType, weaponType = pcall(function() return weapon and weapon:getFullType() end)

        local fields = {
            type = "weapon_hit",
            username = username,
            steamId = ExporterLog.Utils.getPlayerSteamID(attacker),
            weapon = (okType and weaponType) or "?",
            damage = type(damage) == "number" and ExporterLog.Utils.round2(damage) or nil,
            targetType = targetType,
            -- Links this raw per-pellet hit back to the weapon_swing
            -- (and its precomputed targetsHit/maxTargetDamage/etc.) it
            -- belongs to -- not needed to reconstruct those aggregates
            -- (already computed in Lua above), but keeps the raw
            -- per-hit rows correlatable in SQL for anything not
            -- precomputed here.
            attackId = attackId,
        }
        if targetType == "player" and targetUsername then
            fields.targetUsername = targetUsername
        end
        ExporterLog.Emit.event(fields)
    end)
    if not ok then print(ExporterLog.Runtime.logPrefix() .. ": onWeaponHitCharacter error: " .. tostring(err)) end
end

-- Both confirmed real and now fully implemented -- still guarded via
-- existence check rather than assumed, same principle Vehicles.lua
-- uses for Events.OnEnterVehicle/OnExitVehicle: this session's
-- confirmation was per-save, not a permanent guarantee across every PZ
-- version this mod might run on. OnWeaponHitTree (tree-chopping,
-- unrelated to combat) was dropped -- no ask for it, no reason to keep
-- speculative candidate code around once the real ones are settled.
local CANDIDATES = {
    { name = "OnWeaponSwing", handler = onWeaponSwing },
    { name = "OnWeaponHitCharacter", handler = onWeaponHitCharacter },
}

-- Registers exactly once per call, reload-safe. Safe to call multiple
-- times -- never stacks or duplicates.
function WeaponHits.init()
    local Runtime = ExporterLog.Runtime
    for _, candidate in ipairs(CANDIDATES) do
        local event = Events[candidate.name]
        if event then
            Runtime.registerEventOnce(WeaponHits.callbacks, "on" .. candidate.name, event, candidate.handler)
        else
            print(Runtime.logPrefix() .. ": Events." .. candidate.name .. " not found -- skipping (diagnostic candidate only)")
        end
    end
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file --
-- OnGameStart is confirmed to fire only once, after every mod file has
-- finished loading, and never fires again on a later reload, so it
-- can't cause double-init -- WeaponHits.init() is idempotent anyway.
local ok, err = pcall(WeaponHits.init)
if not ok then
    print("ExporterLog: WeaponHits.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(WeaponHits.init)
