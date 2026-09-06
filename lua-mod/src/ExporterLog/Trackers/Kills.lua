-- ExporterLog.Kills -- zombie kill tracking + kill-cause attribution
-- (melee/firearm/vehicle/unarmed/unknown).
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed). Every ExporterLog.Runtime/Emit/Utils/
-- Vehicles reference below is a fresh lookup at actual call time.
ExporterLog = ExporterLog or {}
ExporterLog.Kills = ExporterLog.Kills or {}
ExporterLog.Kills.callbacks = ExporterLog.Kills.callbacks or {}

local Kills = ExporterLog.Kills

-- Kill-count baselines, keyed by username. Seeded lazily and
-- idempotently (guarded by the nil-check) via the Runtime player-
-- observer hook registered in init() below, which fires on EVERY
-- forEachTrackedPlayer call from ANY tracker (not just this one) --
-- covers a player joining mid-session, a fresh game start, AND a
-- mid-session hot-reload uniformly, with no separate reload-specific
-- logic needed. Never seeds to 0 -- always the player's real current
-- kill count, so historical kills are never emitted as "new". A
-- player joining with 500 historical kills gets baseline=500; their
-- next real kill emits kill 501.
local lastKnownKills = {}

function Kills.initializeKillBaseline(player)
    if not player then return end
    local username = player:getUsername()
    if not username then return end
    if lastKnownKills[username] == nil then
        local kills = player:getZombieKills()
        lastKnownKills[username] = kills
        if ExporterLog.Runtime.isDebug() then
            print("EXPORTERLOG_DEV: kill baseline username=" .. tostring(username) .. " kills=" .. tostring(kills))
        end
    end
end

-- ============================================================
-- KILL-CAUSE ATTRIBUTION
--
-- zombie:getAttackedBy() + the attacker's currently-equipped weapon is
-- the classification signal for melee/firearm/vehicle -- proven
-- working live across melee, firearm, and vehicle kills.
-- ============================================================

-- VEHICLE MUST BE CHECKED FIRST -- otherwise an unarmed vehicle kill
-- falls through to the weapon branch and reads "unarmed" instead of
-- "vehicle".
local function resolveKillMethod(zombie)
    local attackedByOk, attacker = pcall(function() return zombie:getAttackedBy() end)

    local vehicle = nil
    if attackedByOk and attacker then
        vehicle = ExporterLog.Vehicles.getVehicleOf(attacker)
    end
    local isDriving = ExporterLog.Vehicles.isDriver(attacker, vehicle)

    -- FALLBACK (2026-09-06): confirmed live -- a full session of
    -- sustained driving (818 driving_distance events, speeds up to
    -- ~50 km/h) produced ZERO "vehicle" kill attributions; every kill
    -- fell through to whatever weapon the driver happened to have
    -- equipped instead. A fresh attacker:getVehicle() lookup is
    -- apparently nil/stale at the exact instant OnZombieDead fires for
    -- a genuine collision kill -- the same class of staleness already
    -- documented and worked around for OnExitVehicle in Vehicles.lua
    -- ("character:getVehicle() is ALREADY nil by the time our handler
    -- runs"). Falls back to Vehicles.lua's own periodic driving-state
    -- cache (updated every EveryOneMinute tick) whenever the fresh
    -- check comes up empty but the attacker was confirmed driving
    -- moments ago -- a narrow false-positive window (a kill by another
    -- means within ~60s of exiting a vehicle would be misattributed to
    -- "vehicle") is an acceptable trade for fixing what was otherwise a
    -- 100% miss rate on real vehicle kills.
    local usedDrivingStateFallback = false
    if not isDriving and attackedByOk and attacker then
        local okName, uname = pcall(function() return attacker:getUsername() end)
        if okName and uname then
            local cachedVehicle = ExporterLog.Vehicles.lastKnownDrivingVehicle(uname)
            if cachedVehicle then
                vehicle = cachedVehicle
                isDriving = true
                usedDrivingStateFallback = true
            end
        end
    end

    local method = "unknown"
    local weaponType = nil
    local vehicleType = nil

    if isDriving then
        method = "vehicle"
        local okType, t = pcall(function() return vehicle:getScriptName() end)
        vehicleType = okType and t or nil
        if usedDrivingStateFallback then
            print(ExporterLog.Runtime.logPrefix() .. ": vehicle kill resolved via driving-state fallback (fresh getVehicle() was nil/stale) vehicle=" .. tostring(vehicleType))
        end
    elseif attackedByOk and attacker then
        local wOk, w = pcall(function() return attacker:getPrimaryHandItem() end)
        if wOk and w then
            local okType, t = pcall(function() return w:getFullType() end)
            local primaryType = okType and t or "?"

            local okInst, isHandWeapon = pcall(function() return instanceof(w, "HandWeapon") end)
            if okInst and isHandWeapon then
                local rOk, ranged = pcall(function() return w:isRanged() end)
                if rOk then
                    method = ranged and "firearm" or "melee"
                    weaponType = primaryType
                end
            end
        else
            method = "unarmed"
        end
    end

    return method, weaponType, vehicleType
end

local function onZombieDead(zombie)
    if not zombie then return end

    -- DIAGNOSTIC (2026-09-06): confirmed live -- a driver's own
    -- p:getZombieKills() counter often does NOT increment for a
    -- vehicle-collision death (one full 8-minute driving session
    -- produced +1 on the counter despite an estimated ~20 zombies run
    -- over), so the loop below never even calls resolveKillMethod() for
    -- those deaths -- we have zero data on whether zombie:getAttackedBy()
    -- would still have correctly resolved the driver anyway. This
    -- unconditionally resolves and prints the method for EVERY dead
    -- zombie, regardless of whether any tracked player's counter moved,
    -- so DebugLog-server.txt's KILLMETHOD_DIAG line count is a true
    -- server-side death tally to compare against an in-game headcount
    -- next carmageddon, and against how many DID show method != nil
    -- (meaning getAttackedBy() found someone even though the vanilla
    -- stat didn't move). Printed unconditionally (not gated behind
    -- isDebug) so it's visible on the live dedicated server. Remove once
    -- resolved.
    local diagMethod, _, diagVehicle = resolveKillMethod(zombie)
    print(ExporterLog.Runtime.logPrefix() .. ": KILLMETHOD_DIAG zombieDied method=" .. tostring(diagMethod) .. " vehicle=" .. tostring(diagVehicle))

    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local prev = lastKnownKills[username]
        local current = p:getZombieKills()

        -- DELTA FIX (2026-09-06): confirmed live -- a vehicle plowing
        -- through several zombies at once can advance p:getZombieKills()
        -- by more than 1 between two consecutive OnZombieDead callbacks
        -- (the engine appears to update the counter for a whole batch of
        -- simultaneous deaths before firing each zombie's Lua event).
        -- The OLD "current > prev" boolean check only ever emitted ONE
        -- kill event per batch: the first callback already advanced
        -- lastKnownKills[username] to the new total, so every subsequent
        -- callback in the same batch saw current == prev and emitted
        -- nothing -- silently losing every kill but the first in a
        -- multi-kill moment, exactly the scenario a car plowing through
        -- a crowd produces and melee/firearm combat (one kill per
        -- attack animation) essentially never does. Emitting one event
        -- per point of delta (not just one event total) recovers every
        -- swallowed kill; killMethod/weapon/vehicle are resolved once
        -- from THIS callback's zombie and reused for the others in the
        -- same delta, since simultaneous deaths in one batch share the
        -- same cause.
        if prev ~= nil and current > prev then
            local killMethod, weaponType, vehicleType = resolveKillMethod(zombie)
            for kc = prev + 1, current do
                local fields = {
                    type = "kill",
                    steamId = ExporterLog.Utils.getPlayerSteamID(p),
                    username = username,
                    x = math.floor(p:getX()),
                    y = math.floor(p:getY()),
                    z = math.floor(p:getZ()),
                    zombieKills = kc,
                    killMethod = killMethod,
                }
                if weaponType then
                    fields.weapon = weaponType
                end
                if vehicleType then
                    fields.vehicle = vehicleType
                end
                ExporterLog.Emit.event(fields)
            end
        end

        lastKnownKills[username] = current
    end)
end

-- One-time debug-mode startup diagnostics. Baseline seeding itself
-- doesn't happen here -- handled centrally by the observer registered
-- in init() -- this is kept purely for the diagnostic print tied to a
-- real game-start event specifically.
local function onGameStartDebugInit()
    if not ExporterLog.Runtime.isDebug() then return end

    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        print("EXPORTERLOG_DEV: startup player=" .. tostring(p:getUsername())
            .. " zombieKills=" .. tostring(p:getZombieKills()))
    end)
end

-- Registers everything exactly once per call, reload-safe. Safe to
-- call multiple times -- never stacks or duplicates.
function Kills.init()
    local Runtime = ExporterLog.Runtime

    -- Keyed observer registration: re-registering under the same key
    -- "kills" on every init()/reload OVERWRITES the previous closure
    -- instead of accumulating a new one alongside it.
    Runtime.onTrackedPlayer("kills", function(p)
        Kills.initializeKillBaseline(p)
    end)

    Runtime.registerEventOnce(Kills.callbacks, "onZombieDead", Events.OnZombieDead, onZombieDead)
    Runtime.registerEventOnce(Kills.callbacks, "onGameStart", Events.OnGameStart, onGameStartDebugInit)

    -- Closes a real race condition in the lazy, observer-based seeding
    -- above: if the very first OnZombieDead call after a reload
    -- happens before any OTHER tracker tick has run (e.g. before the
    -- next EveryOneMinute), the baseline would seed from
    -- getZombieKills() called INSIDE that same OnZombieDead call --
    -- meaning the count already reflects the just-happened kill, still
    -- losing it. In single-player debug mode, the local player object
    -- already exists the instant this runs (unlike a dedicated server,
    -- where players join over time), so seed immediately here --
    -- guaranteed to run before any event can possibly fire. Routed
    -- through forEachTrackedPlayer (with a no-op callback) purely to
    -- run the observer just registered above -- no separate
    -- player-fetch helper needed.
    if Runtime.isDebug() then
        Runtime.forEachTrackedPlayer(function() end)
    end
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime/Vehicles might
-- not exist yet at the exact moment PZ's auto-loader happens to run
-- THIS file -- OnGameStart is confirmed to fire only once, after
-- every mod file has finished loading, and never fires again on a
-- later reload, so it can't cause double-init -- Kills.init() is
-- idempotent anyway.
local ok, err = pcall(Kills.init)
if not ok then
    print("ExporterLog: Kills.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Kills.init)
