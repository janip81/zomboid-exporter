-- ExporterLog.WorldStats -- periodic world-level snapshot (in-game
-- date/time, days survived, world age). Not tied to any single
-- player -- Emit.event() is already fully generic (just JSON-encodes
-- whatever fields table it's given, no player-attachment logic of its
-- own to work around), so a "world_stats" event simply omits
-- username/steamId/x/y/z entirely. ROADMAP.md's note that Emit.event()
-- "needs a small tweak" for this was checked and found stale --
-- corrected here rather than carried forward.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.WorldStats = ExporterLog.WorldStats or {}
ExporterLog.WorldStats.callbacks = ExporterLog.WorldStats.callbacks or {}

local WorldStats = ExporterLog.WorldStats

-- getGameTime() and every method read below are CONFIRMED real --
-- already in active live use by our own installed PanelBridge.lua
-- (server/PanelBridge.lua) per ROADMAP.md's research, not a fresh
-- guess.
--
-- Throttled onto the ALREADY-PROVEN-REAL Events.EveryOneMinute
-- (confirmed firing correctly throughout Vehicles.lua/Kills.lua this
-- whole session) rather than the unverified Events.EveryTenMinutes/
-- EveryHours ROADMAP.md floated as options -- neither of those has
-- ever actually been confirmed to exist in this codebase, and this
-- session already spent a real round-trip on one wrong blind guess
-- (ISSmokingAction) to know that's not free. Self-throttling via a
-- tick counter costs nothing and removes that risk entirely.
local TICKS_PER_EMIT = 60 -- ~hourly, since EveryOneMinute fires roughly once per in-game minute
-- Starts already at threshold so the FIRST tick after a reload emits
-- immediately (a baseline, testable via the usual F11-reload-and-check
-- workflow instead of waiting up to an hour of game time) -- every
-- subsequent emit then falls back to the real hourly cadence.
local tickCount = TICKS_PER_EMIT

local function safePcall(fn)
    local ok, v = pcall(fn)
    if ok then return v end
    return nil
end

local function onEveryMinuteWorldStats()
    tickCount = tickCount + 1
    if tickCount < TICKS_PER_EMIT then return end
    tickCount = 0

    local okTime, gameTime = pcall(function() return getGameTime() end)
    if not okTime or not gameTime then return end

    ExporterLog.Emit.event({
        type = "world_stats",
        day = safePcall(function() return gameTime:getDay() end),
        month = safePcall(function() return gameTime:getMonth() end),
        year = safePcall(function() return gameTime:getYear() end),
        hour = safePcall(function() return gameTime:getHour() end),
        minute = safePcall(function() return gameTime:getMinutes() end),
        timeOfDay = safePcall(function() return gameTime:getTimeOfDay() end),
        nightsSurvived = safePcall(function() return gameTime:getNightsSurvived() end),
        worldAgeHours = safePcall(function() return gameTime:getWorldAgeHours() end),
    })
end

-- Registers exactly once per call, reload-safe. Safe to call multiple
-- times -- never stacks or duplicates.
function WorldStats.init()
    local Runtime = ExporterLog.Runtime
    Runtime.registerEventOnce(WorldStats.callbacks, "onEveryMinuteWorldStats", Events.EveryOneMinute, onEveryMinuteWorldStats)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file --
-- OnGameStart is confirmed to fire only once, after every mod file has
-- finished loading, and never fires again on a later reload, so it
-- can't cause double-init -- WorldStats.init() is idempotent anyway.
local ok, err = pcall(WorldStats.init)
if not ok then
    print("ExporterLog: WorldStats.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(WorldStats.init)
