-- TWR.Runtime -- shared client-side runtime primitives, starting with
-- just the reload-safety helper every context/UI hook needs. Mirrors
-- ExporterLog.Runtime.registerEventOnce exactly (same proven pattern,
-- confirmed live across many ExporterLog trackers) rather than
-- inventing a second mechanism -- an F11 reload re-executes every one
-- of a mod's files top to bottom, so registering a fresh
-- Events.X.Add(handler) on every reload without first removing the
-- previous one stacks duplicate handlers (confirmed real bug in
-- ExporterLog's own history, fixed there, not repeated here).
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Runtime = TWR.Runtime or {}

local Runtime = TWR.Runtime

-- callbackTable is the calling module's own .callbacks table; key is a
-- name local to that module. Safe to call on every reload -- swaps the
-- handler instead of piling another one on top.
function Runtime.registerEventOnce(callbackTable, key, event, handler)
    if callbackTable[key] then
        event.Remove(callbackTable[key])
    end
    event.Add(handler)
    callbackTable[key] = handler
    print("TWR: registered " .. key)
end
