-- ExporterLog.Emit -- the single place every tracker sends a finished
-- event through. Payload shape is identical across modes (trackers
-- never know or care which mode they're in); only the OUTPUT
-- mechanism differs per ExporterLog.Runtime.getMode():
--   server : writeLog("ExporterLog", json) -- writes the real stat
--            file the external Go exporter parses
--            (Logs/logs_*/*_ExporterLog.txt)
--   debug  : print("EXPORTERLOG_DEV: " .. json) -- writeLog()'s
--            per-logger-name file output doesn't produce visible
--            files in single-player (confirmed live) -- console only
--   client : no-op -- multiplayer clients must never produce their
--            own copy of stats the server already tracks
--
-- No require() here -- CONFIRMED LIVE (2026-08-09): require() does not
-- resolve cross-file paths within a mod's own directory the way
-- vanilla's internal require() does for its own files (every
-- require("ExporterLog/...") call failed with a WARN in the actual
-- game log, across every file in this mod, despite matching vanilla's
-- own proven "SubDir/File" pattern). ExporterLog.Runtime.getMode() is
-- only called from inside Emit.event() at actual event-emit time
-- (well after full mod boot), not at this file's own load time, so
-- it's safe regardless of which file PZ's auto-loader happens to
-- execute first.
ExporterLog = ExporterLog or {}
ExporterLog.Emit = ExporterLog.Emit or {}

local Emit = ExporterLog.Emit

local function jsonEscapeString(s)
    s = tostring(s)
    s = s:gsub('\\', '\\\\')
    s = s:gsub('"', '\\"')
    s = s:gsub('\n', '\\n')
    s = s:gsub('\r', '\\r')
    s = s:gsub('\t', '\\t')
    return s
end

local function jsonEncodeFlat(fields)
    local parts = {}
    for k, v in pairs(fields) do
        local encodedValue
        if type(v) == "number" or type(v) == "boolean" then
            encodedValue = tostring(v)
        else
            encodedValue = '"' .. jsonEscapeString(v) .. '"'
        end
        table.insert(parts, '"' .. jsonEscapeString(k) .. '":' .. encodedValue)
    end
    return "{" .. table.concat(parts, ",") .. "}"
end

function Emit.event(fields)
    local mode = ExporterLog.Runtime.getMode()
    if mode == "client" then return end

    local json = jsonEncodeFlat(fields)
    if mode == "server" then
        writeLog("ExporterLog", json)
    else
        print("EXPORTERLOG_DEV: " .. json)
    end
end
