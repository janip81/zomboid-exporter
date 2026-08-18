-- TWR.Mechanics.MapReveal -- reveals a rectangular area on a player's
-- in-game map.
--
-- REBUILT 2026-08-15 as a vanilla-equivalent CLIENT-FIRST flow, after a
-- full research+bytecode investigation closed out the 2026-08-12
-- incident (antagonist/tests/worldmap-visited-server-research.md ->
-- worldmap-visited-server-chatgpt-response.md ->
-- worldmap-visited-server-bytecode-findings.md ->
-- worldmap-visited-bytecode-chatgpt-review.md, in that order, all in
-- zomboid-exporter-ideas). Summary of what was actually confirmed by
-- decompiling the real B42.20.2 projectzomboid.jar running on
-- zomboid-test:
--
--   WorldMapVisitedServer.setKnownInSquares(player,x1,y1,x2,y2)
--     -> WorldMapVisited.setKnownInSquares(x1,y1,x2,y2,byte[])  [static]
--     -> setFlags(...)  -- pure `oldFlags | BIT_KNOWN` per grid unit,
--        write-back only if changed. NO destructive/clearing code path
--        exists in this method. CONFIRMED at the bytecode level, not
--        just inferred from API naming.
--   Clearing is architecturally separate (clearKnownInSquares/
--   clearFlags/forget()) and MapReveal never called any of those.
--   WorldMapVisitedServer.setKnownInSquares also sends NO packet to
--   anyone -- it only mutates the server's own per-user dictionary
--   entry. The only place WorldMapVisitedServer ever pushes a player's
--   full visited-map state to a client is loadUser() (fired once, on
--   that player's connect), via
--   PlayerVisitedPacket.HandleSendPacket(byte[], connection) -- there is
--   no periodic re-push to an already-connected client.
--
-- Conclusion: the old server-only call here could not have destroyed
-- the player's map data, and -- since it never pushes anything to an
-- already-connected client either -- it could not have caused an
-- immediate visible change (good or bad) to that player's live map
-- view. The 2026-08-12 "entire map disappeared" symptom, timed right
-- after this call, was very likely coincidental with an unrelated
-- client-side cache/sync issue (matches the independently QA-confirmed
-- historical B42.18 MP visited-map bug, and the "deleting the local
-- client cache folder fixed it on rejoin" observation from the original
-- incident report).
--
-- That said, the OLD implementation was still architecturally wrong:
-- calling WorldMapVisitedServer directly from server Lua is NOT what
-- vanilla itself does for a player-facing reveal. Traced the REAL
-- vanilla call site (client/PZAPI/ui/organisms/PrintMedia.lua's actual
-- "reveal on map" button handler, not ISReadABook.lua -- that file's
-- seeming "dual call" is PZ's shared-script model executing the SAME
-- file independently client-side and server-side, not a network round
-- trip, and is not a real precedent for this):
--
--   CLIENT (PrintMedia.lua, real source):
--     WorldMapVisited.getInstance():setKnownInSquares(x1,y1,x2,y2)  -- immediate local UI feedback
--     if isClient() then
--         sendClientCommand(getPlayer(), "map", "setKnownInSquares", {x1=..,y1=..,x2=..,y2=..})
--     end
--   SERVER (server/ClientCommands.lua, real source, vanilla-owned, unmodified):
--     Commands.map.setKnownInSquares = function(player, args)
--         WorldMapVisitedServer.getInstance():setKnownInSquares(player, args.x1, args.y1, args.x2, args.y2)
--     end
--
-- So vanilla's own server-side handler for this command is the EXACT
-- same one-line call this mechanic always made -- the only thing
-- missing was the client half. This file now drives that client half
-- instead of calling WorldMapVisitedServer directly: it sends a
-- TARGETED TWR-owned server->client command (sendServerCommand, real
-- vanilla API -- confirmed real call sites: server/BuildRecipeCode/
-- buildRecipeCode.lua's `sendServerCommand('erosion', ...)`,
-- server/BuildingObjects/ISBuildUtil.lua's
-- `sendServerCommand(playerObj, 'ui', 'dirtyUI', {})`) to the specific
-- connected player, and client/TWR/Context/MapReveal.lua's
-- Events.OnServerCommand handler performs the exact two-step vanilla
-- client flow above -- including routing the second step through
-- vanilla's own real, unmodified, already-proven ClientCommands.lua
-- handler, rather than TWR calling WorldMapVisitedServer itself. This
-- keeps WorldMapVisitedServer mutation on a single authoritative path
-- (vanilla's own command handler) instead of two independent ones that
-- could drift.
--
-- OFFLINE PLAYERS: not supported yet, by design (per the ChatGPT
-- review's explicit Q4 recommendation) -- sendServerCommand requires a
-- live connection, and there is no existing "wait until this specific
-- player is next online" trigger primitive in TWR.PendingActions/
-- QuestEngine (those are chunk/position-anchored, not player-presence-
-- anchored). revealArea()/revealAroundPoint() below simply no-op (return
-- false) for an unresolvable/offline player rather than silently
-- mutating WorldMapVisitedServer for someone who can't see the result
-- yet. Revisit only if/when a real "deliver on next connect" mechanism
-- exists.
--
-- STILL NOT PROMOTED TO PRODUCTION. Test only on zomboid-test, and only
-- after running MAP-SAFE-1..6 (worldmap-visited-bytecode-chatgpt-
-- review.md's test list) live.
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

-- Fail-closed validation rules per the ChatGPT review's explicit
-- recommendation ("Do not let DB-authored params become an
-- unrestricted arbitrary-map reveal"). MAX_DIMENSION bounds each side
-- of the rectangle independently (not total area), matching how the
-- real vanilla call sites (PrintMedia.lua) always pass small,
-- deliberately-authored rectangles.
local MAX_DIMENSION = 500

local function validateRect(x1, y1, x2, y2)
    x1, y1, x2, y2 = tonumber(x1), tonumber(y1), tonumber(x2), tonumber(y2)
    if not (x1 and y1 and x2 and y2) then
        return nil, "non-numeric coordinate"
    end
    if x2 < x1 or y2 < y1 then
        return nil, "x2<x1 or y2<y1"
    end
    if (x2 - x1) > MAX_DIMENSION or (y2 - y1) > MAX_DIMENSION then
        return nil, "rectangle exceeds MAX_DIMENSION=" .. MAX_DIMENSION
    end
    return x1, y1, x2, y2
end

-- Sends a targeted client command asking player's own client to reveal
-- (x1,y1)-(x2,y2) via the real vanilla client-first flow. Returns
-- true/false, err.
function MapReveal.revealArea(player, x1, y1, x2, y2)
    if not player then
        return false, "no player"
    end
    local vx1, vy1, vx2, vy2, verr = validateRect(x1, y1, x2, y2)
    if not vx1 then
        print("TWR.Mechanics.MapReveal: revealArea REJECTED: " .. tostring(verr))
        return false, verr
    end

    local okSend, sendErr = pcall(function()
        sendServerCommand(player, "twr_map", "reveal", { x1 = vx1, y1 = vy1, x2 = vx2, y2 = vy2 })
    end)
    if not okSend then
        print("TWR.Mechanics.MapReveal: revealArea -- sendServerCommand FAILED: " .. tostring(sendErr))
        return false, "sendServerCommand failed"
    end
    print("TWR.Mechanics.MapReveal: revealArea -- sent twr_map/reveal (" .. vx1 .. "," .. vy1 .. ")-(" .. vx2 .. "," .. vy2 .. ")")
    return true
end

-- Convenience wrapper: reveals a square of the given radius centered
-- on (centerX, centerY).
function MapReveal.revealAroundPoint(player, centerX, centerY, radius)
    return MapReveal.revealArea(player, centerX - radius, centerY - radius, centerX + radius, centerY + radius)
end
