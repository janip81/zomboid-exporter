-- TWR.PendingActions -- durable, game-side "TWR still owes the world
-- this action" receipt for spatial jobs, persisted via a mod-owned
-- SGlobalObjectSystem. This is the production generalization of the
-- disposable PendingSpawnTest.lua probe, now that its result is
-- LIVE-CONFIRMED (see zomboid-exporter-ideas/antagonist/
-- sglobalobjectsystem-persistence-validation.md, PASS-A through PASS-D
-- + Phase 5 CASE 3) and the naming/ownership design is settled (see
-- .../pending-action-world-handoff.md's "Recommended production SGOS
-- naming"). Every spatial TWR mechanic should request through this
-- instead of DeferredArea's RAM-only Events.EveryOneMinute watchers,
-- which lose all pending state on any restart.
--
-- Ownership boundary (pending-action-world-handoff.md): this system
-- only ever holds UNFULFILLED actions. The moment a mutation succeeds,
-- vanilla PZ's own world/chunk persistence owns the physical result --
-- this system's record for that job is removed, it never becomes a
-- second permanent copy of the world.
--
-- Dispatch design: a pending record stores `handlerModule` (a plain
-- string, e.g. "Container") rather than a registered callback. When a
-- chunk loads, OnChunkLoaded looks up
-- TWR.Mechanics[handlerModule].resolvePendingAction(pending) lazily,
-- at call time -- long after every TWR file has loaded. This
-- deliberately avoids the load-order registration problem this
-- codebase has hit before (server/TWR/Debug.lua's own header: alphabetical
-- same-folder load order means "Container.lua" loads before
-- "PendingActions.lua", so a load-time
-- TWR.PendingActions.registerHandler(...) call from Container.lua would
-- fail with TWR.PendingActions still being nil/incomplete) -- same
-- reason this codebase avoids require() for its own cross-file locals.
--
-- Success-first logging is centralized HERE, not in each mechanic's
-- resolver: resolvePendingAction() only returns success/failure
-- info, this file is the single place that calls TWR.Emit.jobResult,
-- so every migrated mechanic gets the same audit behavior for free
-- and can't accidentally double-emit or forget to.
--
-- Confirmed live structure (see PendingSpawnTest.lua / the validation
-- doc for the exact source citations this mirrors): SGlobalObjectSystem:
-- new(name) -> gos_<name>.bin; setObjectModDataKeys is a REQUIRED
-- whitelist; nested Lua tables ARE persistable through it (confirmed:
-- vanilla's own forageServer.lua stores whole zoneData tables this way
-- and they survive restart in real gameplay) -- `params` below is one
-- whitelisted key holding an arbitrary nested table, not a
-- per-actionType flat field list; OnChunkLoaded(wx, wy) is real and
-- targeted (only fires for chunks containing this system's objects).
--
-- No require() for TWR's own cross-file locals -- see TWR.Constants'
-- header for why. require "Map/SGlobalObjectSystem" / "Map/SGlobalObject"
-- load VANILLA base game files, unrelated to that limitation.
if isClient() then return end

require "Map/SGlobalObjectSystem"
require "Map/SGlobalObject"

TWR = TWR or {}
TWR.PendingActions = TWR.PendingActions or {}

-- Per-object wrapper. Pure data record, never a real IsoObject.
TWRPendingActionObject = SGlobalObject:derive("TWRPendingActionObject")

function TWRPendingActionObject:new(luaSystem, globalObject)
    local o = SGlobalObject.new(self, luaSystem, globalObject)
    return o
end

function TWRPendingActionObject:initNew()
    self.jobId = ""
    self.artifactKey = ""
    self.actionType = ""
    self.handlerModule = ""
    self.state = "WAITING_WORLD"
    self.targetX = 0
    self.targetY = 0
    self.targetZ = 0
    self.params = {}
end

function TWRPendingActionObject:stateFromIsoObject(isoObject)
end

function TWRPendingActionObject:stateToIsoObject(isoObject)
end

-- System. Persisted name "twr_pending_actions" -> gos_twr_pending_actions.bin,
-- per pending-action-world-handoff.md's recommended production naming
-- (deliberately not spawn-specific -- this carries any spatial action).
TWRPendingActionsSystem = SGlobalObjectSystem:derive("TWRPendingActionsSystem")

function TWRPendingActionsSystem:new()
    local o = SGlobalObjectSystem.new(self, "twr_pending_actions")
    return o
end

function TWRPendingActionsSystem:initSystem()
    SGlobalObjectSystem.initSystem(self)
    self.system:setModDataKeys({})
    self.system:setObjectModDataKeys({
        "jobId", "artifactKey", "actionType", "handlerModule", "state",
        "targetX", "targetY", "targetZ", "params",
    })
end

function TWRPendingActionsSystem:newLuaObject(globalObject)
    return TWRPendingActionObject:new(self, globalObject)
end

-- This system's GlobalObjects are pure data records, never a real
-- world IsoObject -- so nothing on any square ever "belongs" to it.
function TWRPendingActionsSystem:isValidIsoObject(isoObject)
    return false
end

function TWRPendingActionsSystem:reportStatus()
    local count = self:getLuaObjectCount()
    print("TWR.PendingActions: reportStatus -- pendingCount=" .. count)
    for i = 1, count do
        local pending = self.system:getObjectByIndex(i - 1):getModData()
        print("TWR.PendingActions: [" .. i .. "] jobId=" .. tostring(pending.jobId)
            .. " actionType=" .. tostring(pending.actionType)
            .. " handlerModule=" .. tostring(pending.handlerModule)
            .. " state=" .. tostring(pending.state)
            .. " target=(" .. tostring(pending.targetX) .. "," .. tostring(pending.targetY) .. "," .. tostring(pending.targetZ) .. ")")
    end
    return count
end

-- The real, targeted, per-chunk callback -- CONFIRMED live only fires
-- for chunks that actually contain this system's GlobalObjects, not a
-- full-world scan (see the validation doc).
function TWRPendingActionsSystem:OnChunkLoaded(wx, wy)
    local globalObjects = self.system:getObjectsInChunk(wx, wy)
    for i = 1, globalObjects:size() do
        local globalObject = globalObjects:get(i - 1)
        local pending = globalObject:getModData()

        if pending.state == "WAITING_WORLD" then
            local module = TWR.Mechanics and TWR.Mechanics[pending.handlerModule]
            if not module or not module.resolvePendingAction then
                print("TWR.PendingActions: OnChunkLoaded -- no resolvePendingAction on TWR.Mechanics." .. tostring(pending.handlerModule) .. " for jobId=" .. tostring(pending.jobId) .. ", leaving pending")
            else
                local ok, a, b = module.resolvePendingAction(pending)

                -- Safe completion ordering (pending-action-world-handoff.md):
                -- only remove the pending receipt AFTER the result has
                -- been emitted through the audit channel -- never
                -- resolve-then-blindly-remove with no record.
                local fields
                if ok then
                    local resolved = a
                    fields = {
                        jobId = pending.jobId,
                        attemptNo = 1,
                        actionType = pending.actionType,
                        mechanic = resolved.mechanic,
                        result = "applied",
                        placed = resolved.placed,
                        requested = resolved.requested,
                        artifactKey = pending.artifactKey,
                        artifactType = resolved.artifactType,
                        x = resolved.x,
                        y = resolved.y,
                        z = resolved.z,
                        targetType = resolved.targetType,
                        targetSummary = resolved.targetSummary,
                    }
                else
                    local errorCode, errorDetail = a, b
                    fields = {
                        jobId = pending.jobId,
                        attemptNo = 1,
                        actionType = pending.actionType,
                        mechanic = pending.handlerModule,
                        result = "final_error",
                        errorCode = errorCode,
                        errorDetail = errorDetail,
                        x = pending.targetX,
                        y = pending.targetY,
                        z = pending.targetZ,
                    }
                end

                local emitOk, emitErr = TWR.Emit.jobResult(fields)
                if not emitOk then
                    -- Per Emit.lua's own contract: a world mutation can
                    -- succeed while the audit-log write itself fails --
                    -- that must never be silent.
                    print("TWR.PendingActions: OnChunkLoaded -- ERROR: TWR.Emit.jobResult FAILED for jobId=" .. tostring(pending.jobId) .. " result=" .. tostring(fields.result) .. ": " .. tostring(emitErr))
                end

                -- Per pending-action-world-handoff.md: a failed/uncertain
                -- outcome should not be silently deleted in a full job-
                -- policy sense -- but no real job-policy/retry system
                -- exists yet (no backend dispatcher), and leaving a
                -- permanently-unsatisfiable job in place would just
                -- re-attempt and re-log an error every single time this
                -- chunk reloads. Remove either way for now; revisit once
                -- real retry/backoff policy exists.
                self:removeLuaObject(pending)
            end
        end
    end
    self.system:finishedWithList(globalObjects)
end

SGlobalObjectSystem.RegisterSystemClass(TWRPendingActionsSystem)

-- Queues one pending action. Called by mechanics (e.g.
-- Container.scatterAcrossMap), not the debug menu directly. jobId/
-- artifactKey should be stable identities from the caller (a real job
-- dispatcher once one exists; ad-hoc per-call today, same as before).
-- params is an arbitrary nested table specific to handlerModule's own
-- resolvePendingAction() -- confirmed persistable as a nested table,
-- see this file's header.
function TWR.PendingActions.request(jobId, artifactKey, actionType, handlerModule, x, y, z, params)
    if not TWRPendingActionsSystem.instance then
        print("TWR.PendingActions: request -- TWRPendingActionsSystem.instance is nil, system not initialized yet")
        return nil
    end
    local luaObject = TWRPendingActionsSystem.instance:newLuaObjectAt(x, y, z)
    luaObject:initNew()
    luaObject.jobId = jobId
    luaObject.artifactKey = artifactKey
    luaObject.actionType = actionType
    luaObject.handlerModule = handlerModule
    luaObject.targetX = x
    luaObject.targetY = y
    luaObject.targetZ = z
    luaObject.params = params or {}
    luaObject.state = "WAITING_WORLD"
    print("TWR.PendingActions: request -- jobId=" .. jobId .. " actionType=" .. actionType .. " handlerModule=" .. handlerModule .. " target=(" .. x .. "," .. y .. "," .. z .. ")")
    return luaObject
end

function TWR.PendingActions.reportStatus()
    if not TWRPendingActionsSystem.instance then
        print("TWR.PendingActions: reportStatus -- TWRPendingActionsSystem.instance is nil")
        return 0
    end
    return TWRPendingActionsSystem.instance:reportStatus()
end
