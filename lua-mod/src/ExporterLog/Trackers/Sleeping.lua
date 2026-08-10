-- ExporterLog.Sleeping -- total hours slept per player. Sleep isn't a
-- TimedAction in vanilla (ISSleepDialog.lua calls
-- self.player:setAsleep(true) directly, a native setter) and
-- Events.OnSleepingTick's only confirmed usages are client-only
-- (ISSleepingUI.lua), so per ROADMAP.md's research this uses the same
-- diff-polling design already proven for kills/driving: poll
-- player:isAsleep() (a real, widely-used boolean getter, confirmed via
-- server-side media/lua/server/ClientCommands.lua) on
-- Events.EveryOneMinute and react to true<->false transitions.
--
-- CONFIRMED live (2026-08-10): a full sleep cycle correctly produced
-- false->true then true->false transitions and emitted
-- {"type":"sleep","hours":4}, matching the ~4 in-game hours world_stats
-- independently showed passing during the same sleep (22:00 -> 02:00).
-- EveryOneMinute fires extremely fast during sleep's time-acceleration
-- (one real-world sleep produced hundreds of ticks in a few seconds),
-- so the once-per-tick diagnostic this file used to print here is
-- gone now that it's served its purpose -- it was real console spam
-- for something now proven correct.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.Sleeping = ExporterLog.Sleeping or {}
ExporterLog.Sleeping.callbacks = ExporterLog.Sleeping.callbacks or {}

local Sleeping = ExporterLog.Sleeping

-- [username] = { asleep = bool, startWorldAgeHours = number|nil }
local sleepState = {}

-- Duration is measured in IN-GAME hours (gameTime:getWorldAgeHours(),
-- the same confirmed-real call WorldStats.lua uses), not real wall-
-- clock time -- "hours slept" is meaningless in real time given PZ's
-- variable time acceleration.
local function currentWorldAgeHours()
    local ok, gameTime = pcall(function() return getGameTime() end)
    if not ok or not gameTime then return nil end
    local ok2, hours = pcall(function() return gameTime:getWorldAgeHours() end)
    if ok2 then return hours end
    return nil
end

local function onEveryMinuteSleeping()
    local nowHours = currentWorldAgeHours()

    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local okAsleep, isAsleep = pcall(function() return p:isAsleep() end)
        if not okAsleep then return end

        local state = sleepState[username]
        if not state then
            -- First observation for this player (fresh join, or first
            -- tick after a reload) -- establish baseline without
            -- treating it as a transition, same principle as Kills.lua's
            -- kill baseline (never emit off a state we didn't actually
            -- see start).
            sleepState[username] = {
                asleep = isAsleep,
                startWorldAgeHours = isAsleep and nowHours or nil,
            }
            return
        end

        if isAsleep and not state.asleep then
            -- Awake -> asleep: record when this sleep session started.
            state.startWorldAgeHours = nowHours
        elseif not isAsleep and state.asleep then
            -- Asleep -> awake: emit the completed session's duration.
            local hours = nil
            if state.startWorldAgeHours and nowHours then
                hours = nowHours - state.startWorldAgeHours
            end
            ExporterLog.Emit.event({
                type = "sleep",
                steamId = ExporterLog.Utils.getPlayerSteamID(p),
                username = username,
                hours = hours and ExporterLog.Utils.round2(hours) or nil,
            })
            state.startWorldAgeHours = nil
        end

        state.asleep = isAsleep
    end)
end

-- Registers exactly once per call, reload-safe. Safe to call multiple
-- times -- never stacks or duplicates.
function Sleeping.init()
    local Runtime = ExporterLog.Runtime
    Runtime.registerEventOnce(Sleeping.callbacks, "onEveryMinuteSleeping", Events.EveryOneMinute, onEveryMinuteSleeping)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file --
-- OnGameStart is confirmed to fire only once, after every mod file has
-- finished loading, and never fires again on a later reload, so it
-- can't cause double-init -- Sleeping.init() is idempotent anyway.
local ok, err = pcall(Sleeping.init)
if not ok then
    print("ExporterLog: Sleeping.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Sleeping.init)
