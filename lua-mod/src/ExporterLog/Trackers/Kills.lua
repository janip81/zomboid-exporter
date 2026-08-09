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

    local method = "unknown"
    local weaponType = nil
    local vehicleType = nil

    if isDriving then
        method = "vehicle"
        local okType, t = pcall(function() return vehicle:getScriptName() end)
        vehicleType = okType and t or nil
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

    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local prev = lastKnownKills[username]
        local current = p:getZombieKills()

        if prev ~= nil and current > prev then
            local killMethod, weaponType, vehicleType = resolveKillMethod(zombie)
            local fields = {
                type = "kill",
                steamId = ExporterLog.Utils.getPlayerSteamID(p),
                username = username,
                x = math.floor(p:getX()),
                y = math.floor(p:getY()),
                z = math.floor(p:getZ()),
                zombieKills = current,
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
