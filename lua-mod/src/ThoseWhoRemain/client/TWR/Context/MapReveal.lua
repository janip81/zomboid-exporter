-- TWR.Context.MapReveal -- client-side half of the map-reveal flow.
-- Receives a targeted "twr_map"/"reveal" command from the server
-- (server/TWR/Mechanics/MapReveal.lua) and replicates vanilla's own
-- real reveal-on-map flow exactly (client/PZAPI/ui/organisms/
-- PrintMedia.lua's actual "reveal on map" button handler, real source):
--
--   WorldMapVisited.getInstance():setKnownInSquares(x1,y1,x2,y2)  -- local, immediate UI feedback
--   sendClientCommand(getPlayer(), "map", "setKnownInSquares", {x1=..,y1=..,x2=..,y2=..})
--
-- The second call routes through vanilla's own real, unmodified
-- server/ClientCommands.lua handler (Commands.map.setKnownInSquares),
-- which is the single authoritative path that mutates
-- WorldMapVisitedServer -- see server/TWR/Mechanics/MapReveal.lua's
-- header for the full research trail this design is based on.
--
-- Args are re-validated here even though they came from our own
-- server -- defense in depth, never trust a network payload blindly.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if not isClient() then return end

TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

local MAX_DIMENSION = 500

local function validateRect(x1, y1, x2, y2)
    x1, y1, x2, y2 = tonumber(x1), tonumber(y1), tonumber(x2), tonumber(y2)
    if not (x1 and y1 and x2 and y2) then return nil, "non-numeric coordinate" end
    if x2 < x1 or y2 < y1 then return nil, "x2<x1 or y2<y1" end
    if (x2 - x1) > MAX_DIMENSION or (y2 - y1) > MAX_DIMENSION then
        return nil, "rectangle exceeds MAX_DIMENSION=" .. MAX_DIMENSION
    end
    return x1, y1, x2, y2
end

-- Shared reveal primitive -- exposed so any client-initiated caller
-- (e.g. TWR.Context.Flyer's own reveal-on-map button, which needs no
-- server round trip since it's the reading player revealing their own
-- map) can reuse the exact same validated, proven mutation this
-- server-pushed path already uses, instead of re-deriving it.
function TWR.Context.revealMapRect(x1, y1, x2, y2)
    local vx1, vy1, vx2, vy2, verr = validateRect(x1, y1, x2, y2)
    if not vx1 then
        print("TWR: Context.MapReveal -- revealMapRect REJECTED: " .. tostring(verr))
        return false, verr
    end

    local okLocal, localErr = pcall(function()
        WorldMapVisited.getInstance():setKnownInSquares(vx1, vy1, vx2, vy2)
    end)
    if not okLocal then
        print("TWR: Context.MapReveal -- local WorldMapVisited:setKnownInSquares FAILED: " .. tostring(localErr))
    end

    local okSend, sendErr = pcall(function()
        sendClientCommand(getPlayer(), "map", "setKnownInSquares", { x1 = vx1, y1 = vy1, x2 = vx2, y2 = vy2 })
    end)
    if not okSend then
        print("TWR: Context.MapReveal -- vanilla sendClientCommand(map,setKnownInSquares) FAILED: " .. tostring(sendErr))
        return false, "sendClientCommand failed"
    end

    print("TWR: Context.MapReveal -- revealed (" .. vx1 .. "," .. vy1 .. ")-(" .. vx2 .. "," .. vy2 .. ") locally + forwarded through vanilla map/setKnownInSquares")
    return true
end

local function onServerCommand(module, command, args)
    if module ~= "twr_map" or command ~= "reveal" then return end
    if not args then
        print("TWR: Context.MapReveal -- twr_map/reveal received with no args, ignoring")
        return
    end
    TWR.Context.revealMapRect(args.x1, args.y1, args.x2, args.y2)
end

local function init()
    TWR.Runtime.registerEventOnce(TWR.Context.callbacks, "onServerCommandMapReveal", Events.OnServerCommand, onServerCommand)
end

-- Self-initialize: immediate attempt handles F11 reload, OnGameStart
-- fallback handles the one-time first-boot ordering race (same pattern
-- this mod's other Context files use).
local ok, err = pcall(init)
if not ok then
    print("TWR: Context.MapReveal init deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(init)
