-- ExporterLog.Reading -- book reading. Small module, kept separate
-- from Consumption.lua because it's logically independent (not
-- consumption) and will likely grow later (started/completed/pages
-- read is on the backlog).
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why. Every ExporterLog.Runtime/Emit/Utils
-- reference below is a fresh lookup at actual call time.
ExporterLog = ExporterLog or {}
ExporterLog.Reading = ExporterLog.Reading or {}
ExporterLog.Reading.originals = ExporterLog.Reading.originals or {}

local Reading = ExporterLog.Reading

local function extractRead(self)
    -- forceStopped = the read was aborted (obsolete skill book /
    -- illiterate trait) -- shouldn't count as a real read. Returning
    -- nil here skips emitting, per hookTimedActionOnce's contract.
    if self.forceStopped then return nil end
    return {
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
    }
end

-- Monkey-patches the hook exactly once per call, reload-safe. Safe to
-- call multiple times -- never stacks.
function Reading.init()
    ExporterLog.Runtime.hookTimedActionOnce(Reading.originals, "ISReadABook", ISReadABook, "read", extractRead, ExporterLog.Emit.event)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime/Emit might not
-- exist yet at the exact moment PZ's auto-loader happens to run THIS
-- file -- OnGameStart is confirmed to fire only once, after every mod
-- file has finished loading, and never fires again on a later reload,
-- so it can't cause double-init -- Reading.init() is idempotent
-- anyway.
local ok, err = pcall(Reading.init)
if not ok then
    print("ExporterLog: Reading.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Reading.init)
