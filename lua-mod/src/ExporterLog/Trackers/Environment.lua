-- ExporterLog.Environment -- tracks each player's current continuous
-- indoor/outdoor streak, for milestones like "24h indoors straight" or
-- "72h outdoors straight" (see ideas/milestones.md). Entering a vehicle
-- breaks BOTH streaks -- sitting in a car parked outside shouldn't count
-- toward an "outdoor" streak even though isOutside() would say the
-- underlying tile is outdoors.
--
-- character:isOutside() and character:getVehicle() are both CONFIRMED
-- real -- widely used server-side (server/Farming/SPlantGlobalObject.lua,
-- server/NPCs/SadisticAIDirector/SadisticMusicDirector.lua for
-- isOutside(); getVehicle() used throughout vehicle-related server code),
-- not fresh guesses.
--
-- Emits two distinct event types (indoor_streak / outdoor_streak) rather
-- than one "environment_streak" + a state field -- the bot's milestone
-- matching (discord-bot/milestones.go) already keys purely on event_type,
-- so two event types need zero schema/matching-logic changes, versus a
-- single type needing a new filter column to disambiguate indoor vs.
-- outdoor. Vehicle time never emits its own event -- it's purely a
-- streak-breaker for the other two states.
--
-- Emits on: every state transition (so a completed streak's exact final
-- duration is always captured precisely), plus a throttled ~hourly
-- heartbeat while a streak is still ongoing (WorldStats.lua's own
-- TICKS_PER_EMIT=60 pattern) -- the Lua side has no knowledge of what
-- thresholds the bot's milestone table actually contains, so a long
-- streak needs SOME periodic emission or a milestone crossed mid-streak
-- (e.g. 24h while still indoors after days inside) would never be caught
-- until the streak eventually ends. Hourly is the practical compromise
-- between catching thresholds promptly and not flooding events/MQTT/
-- Postgres with a row every single in-game minute for every player.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.Environment = ExporterLog.Environment or {}
ExporterLog.Environment.callbacks = ExporterLog.Environment.callbacks or {}

local Environment = ExporterLog.Environment

local HEARTBEAT_TICKS = 60 -- ~hourly
local tickCount = HEARTBEAT_TICKS -- fire on the first real tick, not after a full hour

-- [username] = { state = "indoor"|"outdoor"|"vehicle", streakStartWorldAgeHours = number|nil }
local envState = {}

-- existence-check-before-call discipline the isRead() debugger freeze
-- taught (see Reading.lua's identical helper): a plain pcall alone isn't
-- enough, since PZ's Lua debugger's "break on error" pauses the WHOLE
-- GAME the instant a bad method call throws, even one a pcall would
-- otherwise safely catch a moment later. safeCall() checks obj[methodName]
-- is actually a function BEFORE ever calling it -- a plain table lookup,
-- which never errors in Lua even for a nonexistent key.
local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Duration is measured in IN-GAME hours (gameTime:getWorldAgeHours()),
-- same confirmed-real call every other tracker here uses.
local function currentWorldAgeHours()
    local okTime, gameTime = pcall(function() return getGameTime() end)
    if not okTime or not gameTime then return nil end
    local ok, hours = safeCall(gameTime, "getWorldAgeHours")
    if ok then return hours end
    return nil
end

local function classifyState(p)
    local okVehicle, vehicle = safeCall(p, "getVehicle")
    if okVehicle and vehicle then return "vehicle" end
    local okOutside, isOutside = safeCall(p, "isOutside")
    if not okOutside then return nil end
    if isOutside then return "outdoor" else return "indoor" end
end

local function emitStreak(p, username, state, hours)
    ExporterLog.Emit.event({
        type = state .. "_streak",
        steamId = ExporterLog.Utils.getPlayerSteamID(p),
        username = username,
        hours = ExporterLog.Utils.round2(hours),
    })
end

local function onEveryMinuteEnvironment()
    tickCount = tickCount + 1
    local heartbeat = tickCount >= HEARTBEAT_TICKS
    if heartbeat then tickCount = 0 end

    local nowHours = currentWorldAgeHours()

    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local newState = classifyState(p)
        if not newState then return end

        local state = envState[username]
        if not state then
            -- First observation for this player -- establish baseline
            -- without treating it as a transition, same principle as
            -- Kills.lua's kill baseline and Sleeping.lua's sleep baseline.
            envState[username] = {
                state = newState,
                streakStartWorldAgeHours = (newState ~= "vehicle") and nowHours or nil,
            }
            return
        end

        if newState ~= state.state then
            -- Transition: if the PREVIOUS state was a trackable streak
            -- (indoor/outdoor, not vehicle), emit its final exact
            -- duration before switching states.
            if state.state ~= "vehicle" and state.streakStartWorldAgeHours and nowHours then
                emitStreak(p, username, state.state, nowHours - state.streakStartWorldAgeHours)
            end
            state.streakStartWorldAgeHours = (newState ~= "vehicle") and nowHours or nil
            state.state = newState
        elseif heartbeat and newState ~= "vehicle" and state.streakStartWorldAgeHours and nowHours then
            -- Same state, streak still ongoing, hourly heartbeat: emit
            -- the current running duration so a long streak's milestone
            -- can be caught before the streak ever ends.
            emitStreak(p, username, newState, nowHours - state.streakStartWorldAgeHours)
        end
    end)
end

-- Registers exactly once per call, reload-safe. Safe to call multiple
-- times -- never stacks or duplicates.
function Environment.init()
    local Runtime = ExporterLog.Runtime
    Runtime.registerEventOnce(Environment.callbacks, "onEveryMinuteEnvironment", Events.EveryOneMinute, onEveryMinuteEnvironment)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file --
-- OnGameStart is confirmed to fire only once, after every mod file has
-- finished loading, and never fires again on a later reload, so it
-- can't cause double-init -- Environment.init() is idempotent anyway.
local ok, err = pcall(Environment.init)
if not ok then
    print("ExporterLog: Environment.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Environment.init)
