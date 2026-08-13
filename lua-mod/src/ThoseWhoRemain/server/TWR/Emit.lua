-- TWR.Emit -- writes one structured JSON result line per meaningful
-- job outcome to Logs/logs_*/*_ThoseWhoRemainLog.txt, exactly
-- mirroring ExporterLog.Emit's writeLog("ExporterLog", json) call
-- (CONFIRMED real vanilla API, same one that file already relies on)
-- but under its own logger name -- a SEPARATE file from
-- ExporterLog.txt, per zomboid-exporter-ideas/antagonist/
-- spawn-result-tracking.md's decision: TWR's world-mutation
-- attempts/results are control-plane/audit records, not gameplay
-- telemetry, and the two mods must stay independently
-- readable/parseable by the Go exporter (see twrlog.go).
--
-- Success-first logging (per the same design doc): call
-- Emit.jobResult() exactly ONCE per dispatched job/application
-- attempt with a final outcome (applied / retryable_error /
-- final_error / deferred_world) -- never once per internal candidate
-- scan, radius-widening retry, or other implementation-level probe.
-- Mechanics like Container.scatterAcrossMap still print() every probe
-- for live debugging (that stays local/ephemeral, unchanged) -- this
-- is the separate, deliberately sparser channel that becomes durable.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. Guarding here too.
if isClient() then return end

TWR = TWR or {}
TWR.Emit = TWR.Emit or {}

local Emit = TWR.Emit

-- Identical escaping to ExporterLog.Emit's jsonEscapeString -- kept as
-- its own copy rather than shared, since require() doesn't resolve
-- cross-mod paths and these two mods must stay independently
-- deployable (TWR doesn't depend on ExporterLog being installed).
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
        if v ~= nil then
            local encodedValue
            if type(v) == "number" or type(v) == "boolean" then
                encodedValue = tostring(v)
            else
                encodedValue = '"' .. jsonEscapeString(v) .. '"'
            end
            table.insert(parts, '"' .. jsonEscapeString(k) .. '":' .. encodedValue)
        end
    end
    return "{" .. table.concat(parts, ",") .. "}"
end

-- fields must include at minimum: jobId, actionType, mechanic, result
-- ("applied" | "retryable_error" | "final_error" | "deferred_world").
-- Optional per result type: attemptNo, idempotencyKey, errorCode,
-- errorDetail, placed, requested, artifactKey, x, y, z, targetType,
-- targetSummary -- see spawn-result-tracking.md for the full field
-- list per outcome. Kept flat (no nested "artifacts" array) since
-- every current TWR mechanic places at most one artifact per job --
-- add nesting only if a real multi-artifact-per-job use case shows up.
--
-- Returns ok, err (mirrors pcall) -- per spawn-result-tracking.md
-- review Q7: a world mutation can succeed while THIS call fails
-- (writeLog throws / mod misconfigured), which would otherwise leave
-- an artifact with zero durable audit record and no visible symptom.
-- Callers MUST check this and fall back to a loud print() with
-- jobId/artifactKey/coordinates -- never let this fail silently.
function Emit.jobResult(fields)
    fields = fields or {}
    fields.type = "twr_job_result"
    local ok, err = pcall(function() writeLog("ThoseWhoRemainLog", jsonEncodeFlat(fields)) end)
    return ok, err
end
