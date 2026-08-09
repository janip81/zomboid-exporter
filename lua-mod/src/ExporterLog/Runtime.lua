-- ExporterLog.Runtime -- shared runtime/environment primitives used by
-- every tracker module. Nothing gameplay-specific lives here (no
-- kills, no consumption, no movement) -- just the mechanics that let
-- trackers work identically across all three real deployment contexts
-- and survive Lua hot-reloads without duplicating events.
--
-- Three real runtime modes, CONFIRMED LIVE (2026-08-09):
--   dedicated server   : isServer()=true                -> "server"
--   single-player debug: isServer()=false, isClient()=false -> "debug"
--     (PZ's embedded SP server -- isServer() is misleadingly false
--     here, this is our automatic dev/debug environment)
--   multiplayer client : isClient()=true                -> "client"
--     (tracking must stay disabled here -- a client must never
--     produce its own copy of stats the server already tracks)
ExporterLog = ExporterLog or {}
ExporterLog.Runtime = ExporterLog.Runtime or {}

local Runtime = ExporterLog.Runtime

local mode = nil -- computed once, lazily, on first getMode() call

function Runtime.getMode()
    if mode then return mode end
    if isServer() then
        mode = "server"
    elseif isClient() then
        mode = "client"
    else
        mode = "debug"
    end
    return mode
end

function Runtime.isDebug()
    return Runtime.getMode() == "debug"
end

function Runtime.isTrackingEnabled()
    return Runtime.getMode() ~= "client"
end

-- Status/diagnostic console prefix -- matches each mode's existing
-- convention (production dedicated-server logs used "ExporterLog:",
-- the single-player dev copy used "EXPORTERLOG_DEV:"). This is for
-- one-off status/error prints only (module load, hook errors) -- the
-- actual JSON stat events go through Emit.lua, which has its own
-- separate mode-aware output path (writeLog() vs print()).
function Runtime.logPrefix()
    return Runtime.isDebug() and "EXPORTERLOG_DEV" or "ExporterLog"
end

-- Reload-safety primitives. Ownership of the actual state (which
-- callback is currently registered, which original function was
-- captured) belongs to EACH TRACKER MODULE, in its own .callbacks /
-- .originals table (ExporterLog.Kills.callbacks, ExporterLog.
-- Consumption.originals, etc.) -- not a shared registry here. Runtime
-- just provides the reusable, well-tested REMOVE-BEFORE-ADD /
-- RESTORE-BEFORE-WRAP mechanics so that logic isn't copy-pasted into
-- every tracker's init().
--
-- Both helpers below are safe to call repeatedly (every F11 reload
-- calls each tracker's init() again, which calls these again) --
-- neither ever stacks on top of what a PREVIOUS call installed. This
-- is the same fix, generalized, for a real bug confirmed live earlier
-- this session: a single eaten item produced 5 duplicate "eat" events
-- after repeated reloads, because each reload wrapped the last
-- reload's wrapper instead of replacing it.

-- Reload-safe replacement for Events.X.Add(handler). callbackTable is
-- the calling module's own .callbacks table; key is a name local to
-- that module (e.g. "onZombieDead") -- removes whatever handler was
-- stored there last time before adding the new one, so a reload swaps
-- the handler instead of piling another one on top.
function Runtime.registerEventOnce(callbackTable, key, event, handler)
    if callbackTable[key] then
        event.Remove(callbackTable[key])
    end
    event.Add(handler)
    callbackTable[key] = handler
    print(Runtime.logPrefix() .. ": registered " .. key)
end

-- Reload-safe monkey-patch of actionClass.complete. originalsTable is
-- the calling module's own .originals table. extractFields(self)
-- returns the fields to emit, or nil to skip emitting (e.g. a forced/
-- aborted action that shouldn't count). Fails safe: any error in our
-- own wrapper is caught and printed, never breaks the actual game
-- action. eventEmitter is called as eventEmitter(fields) -- callers
-- pass ExporterLog.Emit.event so this module never has to depend on
-- Emit.lua itself.
function Runtime.hookTimedActionOnce(originalsTable, actionClassName, actionClass, eventName, extractFields, eventEmitter)
    if not actionClass then
        print(Runtime.logPrefix() .. ": could not hook " .. actionClassName .. " (class not found/not loaded yet)")
        return
    end

    if not originalsTable[actionClassName] then
        originalsTable[actionClassName] = actionClass.complete
    end
    local original = originalsTable[actionClassName]

    -- Explicit restore before rewrap, so a reload never builds a new
    -- wrapper on top of a previous wrapper.
    actionClass.complete = original

    local wrapper = function(self, ...)
        local result = original(self, ...)

        local ok, err = pcall(function()
            local fields = extractFields(self)
            if fields then
                fields.type = eventName
                fields.username = (self.character and self.character.getUsername) and self.character:getUsername() or "?"
                fields.steamId = ExporterLog.Utils.getPlayerSteamID(self.character)
                eventEmitter(fields)
            end
        end)
        if not ok then
            print(Runtime.logPrefix() .. ": hook error in " .. actionClassName .. ".complete: " .. tostring(err))
        end

        return result
    end

    actionClass.complete = wrapper

    print(Runtime.logPrefix() .. ": installed " .. eventName .. " hook (" .. actionClassName .. ")")
end

-- Generic per-player observer hook: trackers that need to run
-- something on EVERY player, EVERY time ANY tracker iterates them
-- (not just their own ticks) register here. Kills.lua uses this to
-- seed a player's kill baseline opportunistically from whichever
-- tracker's forEachTrackedPlayer call happens to run first --
-- preserving the original single-file design without Runtime needing
-- to know anything about kills specifically.
--
-- Keyed (not a plain array) so re-registering under the SAME key on
-- every init()/reload OVERWRITES instead of accumulating -- a plain
-- table.insert() here was a real reload-safety gap found while
-- reviewing this architecture: unlike registerEventOnce/
-- hookTimedActionOnce above, an unkeyed observer list had no dedup at
-- all, silently growing by one closure on every F11 reload.
Runtime.playerObservers = Runtime.playerObservers or {}

function Runtime.onTrackedPlayer(key, observer)
    Runtime.playerObservers[key] = observer
end

local function visitPlayer(p, callback)
    if not p then return end
    for _, observer in pairs(Runtime.playerObservers) do
        observer(p)
    end
    callback(p)
end

-- The one place any tracker needs to know about server-vs-debug mode.
-- Dedicated server: iterate every connected player via
-- getOnlinePlayers() (confirmed real, multiple vanilla server/*.lua
-- files). Single-player debug: invoke the callback once for the local
-- player only, via getSpecificPlayer(0) (falls back to getPlayer() if
-- that somehow returns nil) -- both confirmed real via genuine
-- server-side vanilla usage. Multiplayer client: no-op, tracking is
-- disabled there entirely.
function Runtime.forEachTrackedPlayer(callback)
    local currentMode = Runtime.getMode()

    if currentMode == "client" then
        return
    elseif currentMode == "debug" then
        local p = getSpecificPlayer(0)
        if not p then p = getPlayer() end
        visitPlayer(p, callback)
    else
        local players = getOnlinePlayers()
        if not players then return end
        for i = 0, players:size() - 1 do
            visitPlayer(players:get(i), callback)
        end
    end
end
