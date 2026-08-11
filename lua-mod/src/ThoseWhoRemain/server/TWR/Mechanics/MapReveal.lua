-- TWR.Mechanics.MapReveal -- reveals a rectangular area on a player's
-- in-game map, server-authoritative.
--
-- CONFIRMED live 2026-08-11 (AntagonistProbe TEST E, existing-world-
-- test-matrix.md "Map-enabled flyer / map reveal" row). Traced from
-- client/PZAPI/ui/organisms/PrintMedia.lua's real "reveal on map"
-- button handler: client calls WorldMapVisited.getInstance():
-- setKnownInSquares, then sendClientCommand(player, "map",
-- "setKnownInSquares", {x1,y1,x2,y2}) -- server/ClientCommands.lua
-- handles this via Commands.map.setKnownInSquares, calling
-- WorldMapVisitedServer.getInstance():setKnownInSquares(player, x1,
-- y1, x2, y2) directly -- CONFIRMED real and server-authoritative,
-- callable directly without any client-side flyer/UI wrapping.
-- Save/reload persistence CONFIRMED live 2026-08-11 (revealed area
-- still shown after a clean reload).
--
-- DESIGN LIMITATION (found during this research): map SYMBOL/pin
-- placement (client/ISUI/Maps/ISWorldMapSymbols.lua) has NO
-- server-side handler anywhere -- purely a client-local personal
-- annotation tool. This mechanic can reveal an AREA, but cannot
-- auto-place a marker/pin at an exact spot.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. This file has no load-time side effects
-- (pure function definitions), but guarding anyway for consistency and
-- so a client never pointlessly parses/executes it.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.MapReveal = TWR.Mechanics.MapReveal or {}

local MapReveal = TWR.Mechanics.MapReveal

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Reveals the rectangle (x1,y1)-(x2,y2) on player's map.
function MapReveal.revealArea(player, x1, y1, x2, y2)
    local okWMVS, wmvs = pcall(function() return WorldMapVisitedServer.getInstance() end)
    if not okWMVS or not wmvs then return false end

    return safeCall(wmvs, "setKnownInSquares", player, x1, y1, x2, y2)
end

-- Convenience wrapper: reveals a square of the given radius centered
-- on (centerX, centerY).
function MapReveal.revealAroundPoint(player, centerX, centerY, radius)
    return MapReveal.revealArea(player, centerX - radius, centerY - radius, centerX + radius, centerY + radius)
end
