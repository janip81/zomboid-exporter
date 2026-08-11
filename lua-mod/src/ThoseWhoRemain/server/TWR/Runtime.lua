-- TWR.Runtime -- server-side reload-safety helper, mirrors
-- client/TWR/Runtime.lua exactly (same proven pattern, confirmed live
-- across many ExporterLog trackers) -- an F11 reload re-executes every
-- one of a mod's files top to bottom, so registering a fresh
-- Events.X.Add(handler) on every reload without first removing the
-- previous one stacks duplicate handlers (confirmed real bug in
-- ExporterLog's own history, fixed there, not repeated here).
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Runtime = TWR.Runtime or {}

local Runtime = TWR.Runtime

function Runtime.registerEventOnce(callbackTable, key, event, handler)
    if callbackTable[key] then
        event.Remove(callbackTable[key])
    end
    event.Add(handler)
    callbackTable[key] = handler
end
