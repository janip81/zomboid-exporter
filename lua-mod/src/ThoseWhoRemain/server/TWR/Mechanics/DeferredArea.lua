-- TWR.Mechanics.DeferredArea -- generic "wait until a target square is
-- genuinely loaded server-side, then run a callback" primitive.
--
-- CONFIRMED live 2026-08-11 (AntagonistProbe TEST H, TEST M): the
-- server does NOT keep the whole generated map's square data resident
-- regardless of player proximity -- a far-away/unvisited square's
-- cell:getGridSquare(x,y,z) genuinely returns nil until a player gets
-- near it, then becomes available. Events.LoadGridsquare has zero
-- confirmed server-side usage anywhere in the installed B42 tree (only
-- one client-only debug usage found), so this polls
-- cell:getGridSquare() on Events.EveryOneMinute instead of depending on
-- that event.
--
-- TEST M additionally proved this against a real pzfind-verified
-- landmark (McCoy Manor, Muldraugh) combined with
-- Container.findExistingContainer below -- the intended production
-- pattern: verified anchor -> waitForSquare -> real building storage
-- found -> loot placed.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.DeferredArea = TWR.Mechanics.DeferredArea or {}

local DeferredArea = TWR.Mechanics.DeferredArea

-- Registers a watcher that polls every in-game minute until
-- cell:getGridSquare(x, y, z) resolves to a real square, then calls
-- onReady(square) exactly once and removes itself. Each call is an
-- independent watcher (not a single named singleton) -- safe to have
-- several jobs waiting on different target squares at once.
--
-- Returns the internal handler, which the caller may pass to
-- DeferredArea.cancel(handler) to stop waiting early (e.g. the quest
-- job that requested it got cancelled/expired before the area loaded).
--
-- NOTE: this does not itself protect against an F11 reload leaving a
-- stale watcher registered against a since-replaced closure -- fine for
-- calls made in response to real game events (the normal case, since
-- reload doesn't re-fire past events), but avoid calling this
-- unconditionally at file-load time the way AntagonistProbe's TEST H/M
-- did for quick manual testing.
function DeferredArea.waitForSquare(x, y, z, onReady)
    local handler
    handler = function()
        local okCell, cell = pcall(function() return getCell() end)
        if not okCell or not cell then return end

        local okSq, square = pcall(function() return cell:getGridSquare(x, y, z) end)
        if not okSq or not square then return end

        Events.EveryOneMinute.Remove(handler)
        onReady(square)
    end

    Events.EveryOneMinute.Add(handler)
    return handler
end

function DeferredArea.cancel(handler)
    if handler then
        Events.EveryOneMinute.Remove(handler)
    end
end
