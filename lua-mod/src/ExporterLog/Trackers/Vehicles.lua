-- ExporterLog.Vehicles -- driving distance, vehicle enter/exit, and a
-- small reusable vehicle-classification API that Trackers/Kills.lua
-- uses for vehicle-kill detection instead of duplicating the same
-- getVehicle()/isDriver() logic.
--
-- No require() here -- confirmed live (2026-08-09): require() does
-- not resolve cross-file mod-local paths (every require("ExporterLog/
-- ...") failed with a WARN in the actual game log, despite matching
-- vanilla's own proven "SubDir/File" require pattern for its OWN
-- files). Also deliberately does NOT cache ExporterLog.Runtime/Emit/
-- Utils into file-top-level locals -- if any of those tables don't
-- exist yet at the moment THIS file happens to execute (PZ's mod
-- auto-loader gives no ordering guarantee across a mod's own files),
-- a captured-once-at-load-time local would stay nil for every closure
-- in this file for the rest of this load pass, including event
-- handlers that only fire much later. Every reference below is a
-- fresh ExporterLog.X.Y lookup at actual call time instead, which is
-- always safe: by the time any of these functions actually run
-- (either a real game event, or Vehicles.init() itself, called both
-- immediately and via the Events.OnGameStart fallback at the bottom
-- of this file), every other module has finished loading.
ExporterLog = ExporterLog or {}
ExporterLog.Vehicles = ExporterLog.Vehicles or {}
ExporterLog.Vehicles.callbacks = ExporterLog.Vehicles.callbacks or {}

local Vehicles = ExporterLog.Vehicles

-- ============================================================
-- Small reusable vehicle-classification API (used by Kills.lua).
-- Pure/stateless -- available immediately once this file has loaded,
-- independent of whether Vehicles.init() has run yet.
-- ============================================================

-- Fails safe to nil -- never breaks a caller just because the
-- character isn't in a vehicle or the call errors.
function Vehicles.getVehicleOf(character)
    if not character then return nil end
    local ok, vehicle = pcall(function() return character:getVehicle() end)
    if ok then return vehicle end
    return nil
end

-- vehicle:isDriver(character) confirmed real and server-side
-- (server/Vehicles/VehicleCommands.lua: "vehicle:isDriver(player)"),
-- a dedicated method for exactly this check -- preferred over
-- comparing vehicle:getDriver() == character directly since it's the
-- officially-provided API for the question "is this character the
-- driver", not just an equality-compare on a possibly-different-
-- timing getDriver() snapshot.
function Vehicles.isDriver(character, vehicle)
    if not vehicle or not character then return false end
    local ok, result = pcall(function() return vehicle:isDriver(character) end)
    return ok and result or false
end

-- ============================================================
-- Driving distance
-- ============================================================

-- Driver-only (not passengers). Above MAX_RAW_DIST_PER_TICK in a
-- single one-minute tick, the delta is treated as a teleport/admin-
-- move/network correction and discarded (position tracking still
-- resets to the new square so the next tick isn't corrupted by the
-- jump).
local drivingState = {}
local MAX_RAW_DIST_PER_TICK = 500

local function resetDrivingState(username)
    drivingState[username] = nil
end

local function onEveryMinuteDriving()
    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local vehicle = p:getVehicle()

        if not vehicle or vehicle:getDriver() ~= p then
            resetDrivingState(username)
        else
            local currentSquare = vehicle:getSquare()
            local state = drivingState[username]

            if not state or state.vehicle ~= vehicle or not currentSquare or not state.previousSquare then
                drivingState[username] = {
                    vehicle = vehicle,
                    previousSquare = currentSquare,
                    pendingKm = 0,
                    maxSpeedKmh = 0,
                }
            elseif currentSquare then
                local rawDist = state.previousSquare:DistToProper(currentSquare)

                if rawDist <= MAX_RAW_DIST_PER_TICK then
                    state.pendingKm = state.pendingKm + (rawDist / 100)
                end

                state.previousSquare = currentSquare

                -- Max speed: only emit on a new record for THIS driving
                -- session (state.maxSpeedKmh resets to 0 whenever the
                -- vehicle changes, same as pendingKm above) -- an event-
                -- volume optimization, not the actual leaderboard. The
                -- real "fastest ever" / "fastest per vehicle type"
                -- numbers come from MAX()/MAX()-GROUP-BY over every
                -- max_speed event in Postgres, same architecture as
                -- driving_distance -- the vehicle field on every event
                -- is what makes the per-vehicle-type breakdown free.
                local okSpeed, speedKmh = pcall(function() return vehicle:getCurrentSpeedKmHour() end)
                if not okSpeed then
                    -- If this ever prints, it explains a "stuck" max
                    -- speed directly instead of silently no-opping
                    -- forever.
                    print(ExporterLog.Runtime.logPrefix() .. ": getCurrentSpeedKmHour() failed: " .. tostring(speedKmh))
                end
                if okSpeed and speedKmh and speedKmh > state.maxSpeedKmh then
                    state.maxSpeedKmh = speedKmh
                    local okType, scriptName = pcall(function() return vehicle:getScriptName() end)
                    ExporterLog.Emit.event({
                        type = "max_speed",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        kmh = ExporterLog.Utils.round2(speedKmh),
                        vehicle = (okType and scriptName) or "?",
                    })
                end

                if state.pendingKm >= 0.1 then
                    -- Per-car-TYPE stats (not per physical instance):
                    -- same getScriptName() pattern already proven for
                    -- vehicle kills and enter/exit. The accumulator
                    -- itself already resets whenever state.vehicle
                    -- changes (switching cars), so pendingKm at flush
                    -- time always reflects distance driven in THIS
                    -- vehicle since the switch -- tagging it with the
                    -- current vehicle's type is accurate, no separate
                    -- per-vehicle bucketing needed.
                    local okType, scriptName = pcall(function() return vehicle:getScriptName() end)
                    ExporterLog.Emit.event({
                        type = "driving_distance",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        x = math.floor(p:getX()),
                        y = math.floor(p:getY()),
                        z = math.floor(p:getZ()),
                        km = ExporterLog.Utils.round2(state.pendingKm),
                        vehicle = (okType and scriptName) or "?",
                    })
                    state.pendingKm = 0
                end
            end
        end
    end)
end

-- ============================================================
-- Vehicle enter/exit
--
-- CORRECTED (2026-08-09): confirmed live -- both events always
-- emitted vehicle="?". Root cause found via genuine vanilla usage
-- (client/Vehicles/TimedActions/ISEnterVehicle.lua,
-- ISExitVehicle.lua): both events fire as triggerEvent("On[Enter|Exit]
-- Vehicle", self.character) -- a SINGLE argument (the character), not
-- (player, vehicle) as originally assumed. There never was a second
-- argument to read.
--
-- Getting the vehicle back requires two different approaches per
-- event, because of WHEN each fires relative to the actual seat
-- change:
--   enter: vehicle:enter(seat, character) runs in :start(), well
--          before triggerEvent() in :perform() -- character:getVehicle()
--          is already correct by the time our handler runs.
--   exit:  vehicle:exit(character) runs in :perform() BEFORE
--          triggerEvent() on the very next line -- character:getVehicle()
--          is ALREADY nil by the time our handler runs, since they've
--          already left. Solved by caching the script name at enter
--          time, keyed by username, and consuming (+ clearing) it on
--          exit instead of re-querying a vehicle reference that no
--          longer exists on the character.
-- ============================================================

local lastEnteredVehicle = {}

local function onEnterVehicle(character)
    local ok, err = pcall(function()
        local username = character and character.getUsername and character:getUsername() or "?"
        local vehicle = Vehicles.getVehicleOf(character)
        local scriptName = nil
        if vehicle then
            local okType, t = pcall(function() return vehicle:getScriptName() end)
            scriptName = okType and t or nil
        end
        if username ~= "?" then
            lastEnteredVehicle[username] = scriptName
        end
        ExporterLog.Emit.event({
            type = "enter_vehicle",
            username = username,
            steamId = ExporterLog.Utils.getPlayerSteamID(character),
            vehicle = scriptName or "?",
        })
    end)
    if not ok then print(ExporterLog.Runtime.logPrefix() .. ": onEnterVehicle error: " .. tostring(err)) end
end

local function onExitVehicle(character)
    local ok, err = pcall(function()
        local username = character and character.getUsername and character:getUsername() or "?"
        local scriptName = lastEnteredVehicle[username]
        lastEnteredVehicle[username] = nil
        ExporterLog.Emit.event({
            type = "exit_vehicle",
            username = username,
            steamId = ExporterLog.Utils.getPlayerSteamID(character),
            vehicle = scriptName or "?",
        })
    end)
    if not ok then print(ExporterLog.Runtime.logPrefix() .. ": onExitVehicle error: " .. tostring(err)) end
end

-- Registers every hook exactly once per call, reload-safe (removes
-- its own previous registration first via Runtime.registerEventOnce).
-- Safe to call multiple times -- each call is idempotent, never
-- stacks.
function Vehicles.init()
    local Runtime = ExporterLog.Runtime

    Runtime.registerEventOnce(Vehicles.callbacks, "onEveryMinuteDriving", Events.EveryOneMinute, onEveryMinuteDriving)

    if Events.OnEnterVehicle then
        Runtime.registerEventOnce(Vehicles.callbacks, "onEnterVehicle", Events.OnEnterVehicle, onEnterVehicle)
    end

    if Events.OnExitVehicle then
        Runtime.registerEventOnce(Vehicles.callbacks, "onExitVehicle", Events.OnExitVehicle, onExitVehicle)
    end
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file
-- -- OnGameStart is confirmed to fire only once, after every mod file
-- has finished loading, and never fires again on a later reload, so
-- it can't cause double-init -- Vehicles.init() is idempotent anyway.
local ok, err = pcall(Vehicles.init)
if not ok then
    print("ExporterLog: Vehicles.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Vehicles.init)
