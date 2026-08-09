-- ExporterLog.Utils -- genuinely cross-tracker helpers only. Anything
-- used by exactly one tracker module stays local to that module
-- instead (e.g. getFluidTypeString is only used inside
-- Trackers/Consumption.lua, so it lives there, not here).
ExporterLog = ExporterLog or {}
ExporterLog.Utils = ExporterLog.Utils or {}

local Utils = ExporterLog.Utils

-- Fails safe to "" on any error/missing value -- never blocks an event
-- from being emitted just because SteamID is unavailable. Used by
-- Kills.lua and Vehicles.lua (driving_distance).
function Utils.getPlayerSteamID(player)
    local ok, id = pcall(function() return player:getSteamID() end)
    if not ok or not id then return "" end
    -- getSteamID() returns a Lua number; PZ's Lua numbers are doubles,
    -- which only exactly represent integers up to 2^53 -- a real
    -- SteamID64 (~7.6e16) is past that range, so tostring() falls back
    -- to unusable scientific notation. %.0f reconstructs the full
    -- decimal digit string instead.
    return string.format("%.0f", id)
end

-- Used by Vehicles.lua (driving_distance) and Movement.lua
-- (movement_distance) for their km-rounding.
function Utils.round2(n)
    return math.floor(n * 100 + 0.5) / 100
end

-- Human-readable display name (e.g. "Painkillers"), separate from the
-- stable internal item id (getFullType(), e.g. "Base.Pills") used for
-- DB grouping. Fails safe to nil -- Emit.event's jsonEncodeFlat simply
-- omits a nil field, so a missing display name never breaks event
-- emission or drops the item id. Used by Consumption.lua and
-- Reading.lua.
function Utils.getItemDisplayName(item)
    if not item then return nil end
    local ok, name = pcall(function() return item:getDisplayName() end)
    if ok and name then return name end
    return nil
end
