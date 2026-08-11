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
-- location/bedType ADDED 2026-08-11, LIVE CONFIRMED same day (see
-- antagonist/sleep-drop.md) -- a real tent sleep correctly resolved
-- location="tent" bedType="badBed" at the start transition, then
-- correctly emitted {"type":"sleep","hours":11,"location":"tent",
-- "bedType":"badBed"} at wake, which AntagonistProbe's TEST F
-- successfully consumed to spawn a dummy item. Added so the antagonist sleep-drop
-- primitive (zomboid-exporter-ideas, antagonist/sleep-drop.md) can
-- eventually distinguish tent sleep from an ordinary bed by consuming
-- this same event, instead of a separate mod re-polling isAsleep().
-- Grep-confirmed against the live dedicated server pod: there is no
-- player:getBed()/getBedType() (sleep-drop.md's original assumption
-- was wrong, zero hits anywhere) -- the real mechanism, taken directly
-- from vanilla's own client/ISUI/ISWorldObjectContextMenu.lua
-- getBedQuality()/getBedObject(), is a WORLD OBJECT lookup: scan the
-- player's square (and its 4 neighbors, same pattern AntagonistProbe's
-- door-finding already used) for an object whose
-- obj:getSprite():getProperties():has(IsoFlagType.bed) is true. If
-- found, obj:getProperties():get("BedType") is the real bed-quality
-- string (e.g. "averageBed"), and a tent/shelter is identified via
-- obj:getProperties():get("CustomName") == "Tent" or "Shelter" -- NOT
-- a sprite-name list, this is vanilla's own actual tent check. No bed
-- object found -> "floor". player:getVehicle() present -> "vehicle",
-- checked first same as vanilla. Deliberately does NOT emit x/y/z --
-- per Jani, that's not a stat worth tracking, and this tracker stays
-- position-agnostic; the antagonist mod resolves its own campsite
-- position separately if/when it acts on this event.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.Sleeping = ExporterLog.Sleeping or {}
ExporterLog.Sleeping.callbacks = ExporterLog.Sleeping.callbacks or {}

local Sleeping = ExporterLog.Sleeping

-- [username] = { asleep = bool, startWorldAgeHours = number|nil, location = string|nil, bedType = string|nil }
local sleepState = {}

-- See header comment -- mirrors vanilla's own getBedObject() scan
-- (client/ISUI/ISWorldObjectContextMenu.lua), independently
-- reimplemented since that function is client-scoped and this tracker
-- is server-only. Returns the first object found with the vanilla
-- IsoFlagType.bed sprite property, on the player's own square or one
-- of its 4 orthogonal neighbors.
local function findBedObject(square)
    if not square then return nil end
    local okObjs, objects = pcall(function() return square:getObjects() end)
    if not okObjs or not objects then return nil end
    for i = 0, objects:size() - 1 do
        local obj = objects:get(i)
        local okHas, has = pcall(function()
            return obj:getSprite() ~= nil and obj:getProperties():has(IsoFlagType.bed)
        end)
        if okHas and has then return obj end
    end
    return nil
end

local function findNearbyBedObject(p)
    local okSquare, square = pcall(function() return p:getSquare() end)
    if not okSquare or not square then return nil end

    local bed = findBedObject(square)
    if bed then return bed end

    -- CONFIRMED real (grepped server/BuildingObjects/ISDestroyCursor.lua,
    -- ISBarbedWire.lua, and already proven live in AntagonistProbe's
    -- door-finding neighbor scan): square:getAdjacentSquare(IsoDirections.X).
    local directions = { IsoDirections.N, IsoDirections.S, IsoDirections.E, IsoDirections.W }
    for _, dir in ipairs(directions) do
        local okNeighbor, neighbor = pcall(function() return square:getAdjacentSquare(dir) end)
        if okNeighbor and neighbor then
            bed = findBedObject(neighbor)
            if bed then return bed end
        end
    end
    return nil
end

-- Returns { location = "vehicle"|"tent"|"shelter"|"bed"|"floor", bedType = string|nil }
local function resolveSleepLocation(p)
    local okVeh, vehicle = pcall(function() return p:getVehicle() end)
    if okVeh and vehicle then
        return { location = "vehicle" }
    end

    local bed = findNearbyBedObject(p)
    if not bed then
        return { location = "floor" }
    end

    local okProps, props = pcall(function() return bed:getProperties() end)
    local bedType = "averageBed"
    if okProps and props then
        local okType, typeVal = pcall(function() return props:get("BedType") end)
        if okType and typeVal then bedType = typeVal end

        local okCustom, isCustom = pcall(function() return props:has("CustomName") end)
        if okCustom and isCustom then
            local okName, name = pcall(function() return props:get("CustomName") end)
            if okName and name == "Tent" then return { location = "tent", bedType = bedType } end
            if okName and name == "Shelter" then return { location = "shelter", bedType = bedType } end
        end
    end

    return { location = "bed", bedType = bedType }
end

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
            -- Awake -> asleep: record when this sleep session started,
            -- and resolve bed/tent/floor/vehicle NOW -- the player is
            -- still standing at the sleep spot at this exact tick,
            -- before setAsleep() actually finishes and they can't be
            -- relied on to still be at the same square by the time
            -- they wake (time-accelerated sleep can end anywhere the
            -- server put them, e.g. after being moved/teleported).
            state.startWorldAgeHours = nowHours
            local okLoc, loc = pcall(resolveSleepLocation, p)
            state.location = okLoc and loc and loc.location or nil
            state.bedType = okLoc and loc and loc.bedType or nil
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
                location = state.location,
                bedType = state.bedType,
            })
            state.startWorldAgeHours = nil
            state.location = nil
            state.bedType = nil
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
