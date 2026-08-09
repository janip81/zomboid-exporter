-- ExporterLog.Movement -- walking/running/sprinting distance on foot.
-- Deliberately separate from Trackers/Vehicles.lua's driving distance
-- (different sampling mechanism, different state, and this module
-- explicitly excludes vehicle occupants so the two never double-count
-- the same movement).
--
-- CONFIRMED WORKING live (2026-08-09): walk/run/sprint all correctly
-- classified and emitted across a real play session. Real dx/dy/dist
-- values (e.g. ~26 tiles covered over ~7s of sustained running, ~13
-- km/h) confirm getX()/getY() coordinates are already tile==meter
-- scale, same as vanilla's own world-scale conventions -- so km =
-- distanceTiles / 1000 is the correct, real conversion (NOT driving's
-- rawDist/100, which is a different unit from a different API).
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why. Every ExporterLog.Runtime/Emit/Utils
-- reference below is a fresh lookup at actual call time.
ExporterLog = ExporterLog or {}
ExporterLog.Movement = ExporterLog.Movement or {}
ExporterLog.Movement.callbacks = ExporterLog.Movement.callbacks or {}

local Movement = ExporterLog.Movement

-- Raw coordinate distance (getX()/getY()), sampled on Events.OnTick
-- throttled to every 10th tick -- not full DistToProper/square-object
-- overhead every render tick.
local movementState = {}
local movementTickCounter = 0
local MOVEMENT_SAMPLE_TICKS = 10

-- Confirmed live: real per-sample distances during sustained sprint
-- topped out around ~2.9 tiles per 10-tick sample. Generous margin
-- above that, still tight enough to reject an obvious teleport/
-- respawn/debug-move.
local MAX_DISTANCE_PER_SAMPLE = 50

local function resetMovementState(username)
    movementState[username] = nil
end

local function onMovementTick()
    movementTickCounter = movementTickCounter + 1
    if movementTickCounter < MOVEMENT_SAMPLE_TICKS then return end
    movementTickCounter = 0

    local ok, err = pcall(function()
        ExporterLog.Runtime.forEachTrackedPlayer(function(p)
            local username = p:getUsername()

            if p:getVehicle() then
                -- Driver or passenger -- either already counted by
                -- Vehicles.lua's driving tracker, or not a stat we
                -- track. Drop the baseline so exiting the vehicle
                -- doesn't produce a fake-movement spike from the
                -- stale pre-vehicle position (walk -> enter car ->
                -- drive 10km -> exit must NOT log 10km of "walking").
                resetMovementState(username)
                return
            end

            local x = p:getX()
            local y = p:getY()
            local state = movementState[username]

            if not state then
                movementState[username] = {
                    x = x,
                    y = y,
                    pendingWalkKm = 0,
                    pendingRunKm = 0,
                    pendingSprintKm = 0,
                }
                return
            end

            local dx = x - state.x
            local dy = y - state.y
            local distance = math.sqrt(dx * dx + dy * dy)

            state.x = x
            state.y = y

            if distance <= 0 or distance > MAX_DISTANCE_PER_SAMPLE then
                -- Standing still (0) or an implausible jump (teleport/
                -- respawn/debug-move) -- baseline above is already
                -- updated either way, so the next sample measures fresh.
                return
            end

            local distanceKm = distance / 1000

            local sprintOk, sprinting = pcall(function() return p:isSprinting() end)
            local runOk, running = pcall(function() return p:isRunning() end)
            if not sprintOk then
                print(ExporterLog.Runtime.logPrefix() .. ": isSprinting() error: " .. tostring(sprinting))
                sprinting = false
            end
            if not runOk then
                print(ExporterLog.Runtime.logPrefix() .. ": isRunning() error: " .. tostring(running))
                running = false
            end
            -- sprinting checked first: sprint implies running too on the
            -- underlying character state, and sprint is the more
            -- specific classification.

            if sprinting then
                state.pendingSprintKm = state.pendingSprintKm + distanceKm
                if state.pendingSprintKm >= 0.1 then
                    ExporterLog.Emit.event({
                        type = "movement_distance",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        x = math.floor(p:getX()),
                        y = math.floor(p:getY()),
                        z = math.floor(p:getZ()),
                        movement = "sprint",
                        km = ExporterLog.Utils.round2(state.pendingSprintKm),
                    })
                    state.pendingSprintKm = 0
                end
            elseif running then
                state.pendingRunKm = state.pendingRunKm + distanceKm
                if state.pendingRunKm >= 0.1 then
                    ExporterLog.Emit.event({
                        type = "movement_distance",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        x = math.floor(p:getX()),
                        y = math.floor(p:getY()),
                        z = math.floor(p:getZ()),
                        movement = "run",
                        km = ExporterLog.Utils.round2(state.pendingRunKm),
                    })
                    state.pendingRunKm = 0
                end
            else
                state.pendingWalkKm = state.pendingWalkKm + distanceKm
                if state.pendingWalkKm >= 0.1 then
                    ExporterLog.Emit.event({
                        type = "movement_distance",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        x = math.floor(p:getX()),
                        y = math.floor(p:getY()),
                        z = math.floor(p:getZ()),
                        movement = "walk",
                        km = ExporterLog.Utils.round2(state.pendingWalkKm),
                    })
                    state.pendingWalkKm = 0
                end
            end
        end)
    end)
    if not ok then
        print(ExporterLog.Runtime.logPrefix() .. ": onMovementTick error: " .. tostring(err))
    end
end

-- Registers the hook exactly once per call, reload-safe. Safe to call
-- multiple times -- never stacks or duplicates.
function Movement.init()
    ExporterLog.Runtime.registerEventOnce(Movement.callbacks, "onMovementTick", Events.OnTick, onMovementTick)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file
-- -- OnGameStart is confirmed to fire only once, after every mod file
-- has finished loading, and never fires again on a later reload, so
-- it can't cause double-init -- Movement.init() is idempotent anyway.
local ok, err = pcall(Movement.init)
if not ok then
    print("ExporterLog: Movement.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Movement.init)
