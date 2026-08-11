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
--
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client, not just the real dedicated server -- see
-- server/TWR/Debug.lua's header for the full live-reproduced bug this
-- caused. Guarding here too so a connecting client never defines this
-- server-side copy at all (keeps client/server TWR.Runtime genuinely
-- separate, matches vanilla's own isClient()-guard convention).
if isClient() then return end

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
