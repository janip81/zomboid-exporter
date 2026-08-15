-- TWR.QuestEngine -- Lua-side poller/executor for the Postgres-driven
-- quest engine's job dispatch (Gate 1 Phase 4). Reads the exporter-
-- published manifest/job files (questdispatch.go), validates them
-- against a small fixed allowlist of known action types, and converts
-- each into a durable TWR.PendingActions request -- the SAME durable,
-- idempotent, restart-safe spatial-job mechanism every other TWR
-- mechanic already uses (Container.scatterAcrossMap, Key.give_key,
-- RecordedMedia.spawn_vhs). This file adds NO new execution engine: it
-- is purely a translator from "Postgres said do X" to
-- "TWR.PendingActions.request(...)", plus the transport-level
-- "accepted" receipt PendingActions.lua itself has no reason to know
-- about (that system predates any DB-driven dispatcher and is used by
-- plenty of ad-hoc debug-triggered jobs that have no Postgres side at
-- all).
--
-- See zomboid-exporter-ideas/antagonist/tests/
-- quest-job-dispatch-transport-chatgpt-response.md for the full
-- delivery-lifecycle contract this implements steps 3-5 of, and
-- questdispatch.go's header for the exact file-layout this reads
-- (twr_dispatch/manifest.txt + twr_dispatch/inbox/job-<id>.txt).
--
-- Deliberately minimal action-type allowlist -- only what the KVLS
-- fixture (antagonist/quest-db/quest-fixtures/
-- dummy-key-vhs-location-sleep.md) needs. Extend ALLOWED_ACTIONS (and
-- the corresponding TWR.Mechanics resolver) only when a real DB-authored
-- step actually needs a new action type -- do not pre-build a full
-- registry speculatively (Jani's explicit Phase 4 scoping guidance,
-- 2026-08-14).
--
-- Fail-closed by design: an unrecognized/malformed job is REJECTED --
-- no PendingAction is created, no mechanic runs, nothing world-visible
-- happens. Rejection is loud (print()) but not otherwise durable --
-- Postgres/the manifest remain the sole source of truth for what's
-- outstanding, and this file never uses file deletion or any local
-- Lua-side bookkeeping as the correctness mechanism (see
-- questdispatch.go's header: only a durable "accepted" receipt through
-- the existing TWR.Emit.jobResult channel counts as safely delivered).
-- All payload decoding goes through TWR.Json's hand-rolled parser --
-- never loadstring()/load() on anything DB-authored.
--
-- NOT YET PROVEN LIVE -- in particular the exact getFileReader()
-- base-path resolution (assumed relative to the save's Lua/ dir,
-- matching questdispatch.go's /data/Lua/twr_dispatch mount) and
-- whether Kahlua's LuaManager.FileReader behaves exactly as assumed
-- here. Run TRANSPORT-A through H (quest-job-dispatch-transport-
-- chatgpt-response.md) before trusting this; update antagonist/DONE.md
-- once confirmed, not before.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. Guarding here too.
if isClient() then return end

require "Map/SGlobalObjectSystem"
require "Map/SGlobalObject"

TWR = TWR or {}
TWR.QuestEngine = TWR.QuestEngine or {}

local QuestEngine = TWR.QuestEngine

-- CONFIRMED live 2026-08-15 (KVLS-5/6 real test, 38 duplicate spawns):
-- TWR.PendingActions.findByJobId is NOT a sufficient dedup source for
-- this file's purposes -- it only reflects the LIVE pending-object
-- list, which PendingActions.lua's own resolvePendingObject erases the
-- instant a job resolves (by design, per that file's own "only ever
-- holds UNFULFILLED actions" ownership boundary). When the target
-- square is already loaded, TWR.PendingActions.request() resolves
-- SYNCHRONOUSLY inside the same call -- so the pending record can be
-- created AND erased within a single poll, leaving nothing for the
-- NEXT poll to find. Sleeping fast-forwards the game clock through
-- many in-game minutes in a few seconds of real time, firing
-- Events.EveryOneMinute (and therefore QuestEngine.pollDispatch) many
-- times in rapid succession while the same job is still sitting
-- DISPATCHED in Postgres/the manifest (Postgres hasn't ingested the
-- "accepted"/"applied" receipt and republished a fresh manifest yet)
-- -- each of those rapid polls independently found "no live pending
-- record" and created a brand new one.
--
-- Fix: a SEPARATE, durable, append-only "have I ever requested a
-- PendingAction for this jobId" ledger, backed by its own
-- SGlobalObjectSystem (same proven mechanism PendingActions.lua uses)
-- but records are NEVER removed -- unlike PendingActions' by-design
-- resolve-and-erase semantics. PZ Lua callbacks run single-threaded/
-- cooperatively (no real concurrency within one poll), so checking
-- then marking this ledger is race-free across the whole rapid-fire
-- sequence: the first poll marks jobId=4 seen, every subsequent poll
-- (even microseconds later) sees it and skips.
--
-- Deliberately global/unbounded -- fine at Gate 1's scale (a handful
-- of jobs per test run). Revisit (e.g. periodic pruning of old
-- terminal jobIds) only if this ever needs to run at real content
-- scale.
TWRQuestJobSeenObject = SGlobalObject:derive("TWRQuestJobSeenObject")

function TWRQuestJobSeenObject:new(luaSystem, globalObject)
    return SGlobalObject.new(self, luaSystem, globalObject)
end

function TWRQuestJobSeenObject:initNew()
    self.jobId = ""
end

function TWRQuestJobSeenObject:stateFromIsoObject(isoObject)
end

function TWRQuestJobSeenObject:stateToIsoObject(isoObject)
end

TWRQuestJobSeenSystem = SGlobalObjectSystem:derive("TWRQuestJobSeenSystem")

function TWRQuestJobSeenSystem:new()
    return SGlobalObjectSystem.new(self, "twr_quest_job_seen")
end

function TWRQuestJobSeenSystem:initSystem()
    SGlobalObjectSystem.initSystem(self)
    self.system:setModDataKeys({})
    self.system:setObjectModDataKeys({ "jobId" })
end

function TWRQuestJobSeenSystem:newLuaObject(globalObject)
    return TWRQuestJobSeenObject:new(self, globalObject)
end

-- Never tied to any real world object, and never scanned by chunk --
-- always iterated in full via getLuaObjectCount()/getObjectByIndex(),
-- same as TWR.PendingActions.findByJobId's own scan pattern. Placed at
-- a fixed dummy coordinate (0,0,0); location is meaningless here.
function TWRQuestJobSeenSystem:isValidIsoObject(isoObject)
    return false
end

SGlobalObjectSystem.RegisterSystemClass(TWRQuestJobSeenSystem)

local function jobAlreadySeen(jobId)
    if not TWRQuestJobSeenSystem.instance then return false end
    local system = TWRQuestJobSeenSystem.instance
    for i = 1, system:getLuaObjectCount() do
        local seen = system.system:getObjectByIndex(i - 1):getModData()
        if seen.jobId == jobId then
            return true
        end
    end
    return false
end

local function markJobSeen(jobId)
    if not TWRQuestJobSeenSystem.instance then return end
    local luaObject = TWRQuestJobSeenSystem.instance:newLuaObjectAt(0, 0, 0)
    luaObject:initNew()
    luaObject.jobId = jobId
end

-- Module.action -> allowed. Both halves matter: handlerModule must
-- resolve to a real TWR.Mechanics.<Module>.resolvePendingAction, and
-- actionType must be one that module's own resolvePendingAction
-- dispatch (Container.lua's own if/elseif chain, etc.) recognizes.
-- This allowlist is Gate 1's fail-closed boundary -- an actionType
-- absent here is REJECTED before ever reaching TWR.PendingActions,
-- regardless of whether the underlying mechanic could technically
-- handle it.
local ALLOWED_ACTIONS = {
    ["Key.give_key"] = true,
    ["Container.spawn_locked_container"] = true,
    ["Container.spawn_item"] = true,
    ["RecordedMedia.spawn_vhs"] = true,
}

local function splitActionType(fullActionType)
    local dotPos = fullActionType:find("%.")
    if not dotPos then return nil, nil end
    return fullActionType:sub(1, dotPos - 1), fullActionType:sub(dotPos + 1)
end

-- Reads a whole file (relative to the save's Lua/ directory, same base
-- questdispatch.go's /data/Lua/twr_dispatch mount resolves to) via
-- getFileReader, line by line, joined back with "\n". pcall-wrapped
-- throughout -- a missing file (normal before the exporter has ever
-- dispatched anything) must not be a Lua error, just a "nothing to do
-- yet" signal.
local function readWholeFile(relativePath)
    local okReader, reader = pcall(getFileReader, relativePath, false)
    if not okReader or not reader then
        return nil, "FILE_NOT_FOUND_OR_UNREADABLE"
    end

    local lines = {}
    local okRead, line = pcall(function() return reader:readLine() end)
    while okRead and line ~= nil do
        table.insert(lines, line)
        okRead, line = pcall(function() return reader:readLine() end)
    end
    pcall(function() reader:close() end)

    if not okRead then
        return nil, "READ_ERROR"
    end
    return table.concat(lines, "\n")
end

-- manifest.txt format (questdispatch.go's qdPublishManifest):
--   revision=<n>
--   job=<id>
--   job=<id>
--   ...
-- Plain-text, deliberately NOT JSON (see that function's own header --
-- revision is a human/log convenience, never a job identity). Only the
-- job= lines matter here; revision is not compared against anything --
-- per CGPT-G1-P3-03's resolution, Lua always just re-parses the whole
-- (tiny) manifest on every poll rather than trying to skip unchanged
-- ones.
local function parseManifestJobIds(text)
    local jobIds = {}
    for line in text:gmatch("[^\r\n]+") do
        local id = line:match("^job=(%d+)$")
        if id then
            table.insert(jobIds, id)
        end
    end
    return jobIds
end

-- Validates and dispatches exactly one job file. jobIdFromManifest is
-- the id as it appeared in manifest.txt (and therefore the exact
-- filename to read) -- kept separate from the jobId decoded out of the
-- file's own JSON body so the two can be cross-checked (defense in
-- depth against a corrupted/mismatched write).
local function processJobFile(jobIdFromManifest)
    local path = "twr_dispatch/inbox/job-" .. jobIdFromManifest .. ".txt"
    local text, readErr = readWholeFile(path)
    if not text then
        print("TWR.QuestEngine: pollDispatch -- could not read " .. path .. ": " .. tostring(readErr) .. " (will retry next poll)")
        return
    end

    local okDecode, envelope = TWR.Json.decode(text)
    if not okDecode then
        print("TWR.QuestEngine: pollDispatch -- REJECTED, malformed JSON in " .. path .. ": " .. tostring(envelope))
        return
    end
    if type(envelope) ~= "table" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED " .. path .. ": top-level JSON is not an object")
        return
    end

    local jobId = envelope.jobId
    if type(jobId) ~= "string" or jobId == "" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED " .. path .. ": missing/invalid jobId")
        return
    end
    if jobId ~= jobIdFromManifest then
        print("TWR.QuestEngine: pollDispatch -- REJECTED " .. path .. ": jobId in file (" .. jobId .. ") does not match manifest/filename id (" .. jobIdFromManifest .. ")")
        return
    end

    local fullActionType = envelope.actionType
    if type(fullActionType) ~= "string" or fullActionType == "" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": missing/invalid actionType")
        return
    end
    if not ALLOWED_ACTIONS[fullActionType] then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": actionType '" .. fullActionType .. "' is not in the known/allowed set -- fail-closed, no PendingAction created")
        return
    end
    local handlerModule, actionType = splitActionType(fullActionType)
    if not handlerModule then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": actionType '" .. fullActionType .. "' has no 'Module.action' separator")
        return
    end

    local params = envelope.actionParams
    if type(params) ~= "table" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": actionParams is not an object")
        return
    end

    -- Every Gate 1 action type is spatial -- x/y/z are DB-authored
    -- (never a Lua constant, per the fixture doc's architecture rule)
    -- and required here. Revisit this blanket requirement only if a
    -- future non-spatial action type is actually added to
    -- ALLOWED_ACTIONS.
    local x, y, z = params.x, params.y, params.z
    if type(x) ~= "number" or type(y) ~= "number" or type(z) ~= "number" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": actionParams.x/y/z missing or not numbers")
        return
    end

    local idempotencyKey = envelope.idempotencyKey
    if idempotencyKey ~= nil and type(idempotencyKey) ~= "string" then
        print("TWR.QuestEngine: pollDispatch -- REJECTED jobId=" .. jobId .. ": idempotencyKey present but not a string")
        return
    end

    -- TRANSPORT-B: the same job being delivered/read twice (e.g. the
    -- exporter republishes the manifest before it's seen our
    -- "accepted" receipt, or -- CONFIRMED live 2026-08-15 -- many
    -- Events.EveryOneMinute polls firing within seconds of real time
    -- during a sleep-induced game-clock fast-forward) must produce
    -- exactly one logical PendingAction, not two (or 38). Checked
    -- against the durable jobAlreadySeen ledger, NOT
    -- TWR.PendingActions.findByJobId -- see this file's header for why
    -- the live-pending-list check alone is insufficient.
    if jobAlreadySeen(jobId) then
        print("TWR.QuestEngine: pollDispatch -- jobId=" .. jobId .. " already processed, skipping duplicate request (re-emitting accepted in case the first receipt was lost)")
    else
        local artifactKey = params.artifactKey or (jobId .. "-artifact")
        local luaObject = TWR.PendingActions.request(jobId, artifactKey, actionType, handlerModule, x, y, z, params)
        if not luaObject then
            print("TWR.QuestEngine: pollDispatch -- jobId=" .. jobId .. ": TWR.PendingActions.request() returned nil (system not initialized yet?), will retry next poll")
            return
        end
        -- Mark seen only AFTER a successful request() -- never before,
        -- so a failed request (system not ready) can still be retried
        -- on the next poll rather than being permanently skipped.
        markJobSeen(jobId)
    end

    -- Only after the PendingAction is durably represented in SGOS
    -- (immediately above -- either just-created, or already existing
    -- from a prior poll) do we emit the transport-level "accepted"
    -- receipt. This is separate from the eventual applied/
    -- retryable_error/final_error/deferred_world outcome
    -- PendingActions.lua's own resolvePendingObject emits later --
    -- see questdispatch.go's header for the full 8-step delivery
    -- lifecycle this is step 4 of.
    local emitOk, emitErr = TWR.Emit.jobResult({
        jobId = jobId,
        idempotencyKey = idempotencyKey,
        actionType = fullActionType,
        mechanic = "QuestEngine.pollDispatch",
        result = "accepted",
    })
    if not emitOk then
        -- Per Emit.lua's own contract: never let this fail silently.
        print("TWR.QuestEngine: pollDispatch -- ERROR: TWR.Emit.jobResult FAILED for jobId=" .. jobId .. " result=accepted: " .. tostring(emitErr))
    end
end

-- One poll pass: read the manifest, process every job it lists.
-- Missing manifest.txt (before the exporter has ever dispatched
-- anything) is normal, not logged as an error.
function QuestEngine.pollDispatch()
    local manifestText = readWholeFile("twr_dispatch/manifest.txt")
    if not manifestText then
        return
    end

    local jobIds = parseManifestJobIds(manifestText)
    for _, jobId in ipairs(jobIds) do
        processJobFile(jobId)
    end
end

-- FIX pattern (same as RecordedMedia.lua/Debug.lua's own): check
-- TWR.Runtime exists first instead of calling into it and relying on
-- pcall() to catch the resulting Java exception. Alphabetically,
-- "Mechanics" < "QuestEngine.lua" < "Runtime.lua" within server/TWR/,
-- so both Mechanics/* (including PendingActions.lua) AND this file
-- load before Runtime.lua -- TWR.Runtime is not guaranteed to exist yet
-- at load time.
local function init()
    if not (TWR.Runtime and TWR.Runtime.registerEventOnce) then
        return false
    end
    TWR.Runtime.registerEventOnce(QuestEngine, "pollDispatch", Events.EveryOneMinute, QuestEngine.pollDispatch)
    print("TWR.QuestEngine: dispatch poller registered (EveryOneMinute)")
    return true
end

-- Self-limiting EveryOneMinute retry -- same pattern as
-- server/TWR/Debug.lua's own retryInit (confirmed reliable there).
-- Removes itself the moment init() succeeds.
local function retryInit()
    if init() then
        Events.EveryOneMinute.Remove(retryInit)
    end
end

if not init() then
    print("TWR.QuestEngine: init deferred, retrying every minute (TWR.Runtime not loaded yet)")
    Events.EveryOneMinute.Add(retryInit)
end
