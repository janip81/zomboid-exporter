-- TWR.Mechanics.PendingSpawnTest -- DISPOSABLE live probe for
-- zomboid-exporter-ideas/antagonist/sglobalobjectsystem-persistence-validation.md
-- (Phases 0-6). Proves/disproves whether a MOD-OWNED SGlobalObjectSystem
-- can durably persist a "requested but not yet materialized" TWR job
-- across a dedicated-server restart, then wake and execute exactly
-- once via the real per-chunk OnChunkLoaded callback -- instead of
-- DeferredArea's RAM-only Events.EveryOneMinute polling, which loses
-- everything on restart (see pending-job-durability.md's "Open
-- problem" section).
--
-- Not production code. Delete once the validation result (CONFIRMED/
-- FAILED per field, per that doc's "What Claude should report back"
-- section) is written up.
--
-- Structure mirrors the REAL, currently-shipping vanilla pattern
-- grepped from the installed B42.20.2 tree (server/Traps/STrapSystem.lua
-- + STrapGlobalObject.lua, server/Camping/SCampfireSystem.lua) as
-- closely as possible, rather than guessing at the SGlobalObjectSystem/
-- SGlobalObject API contract:
--   - SGlobalObjectSystem:new(name) -> SGlobalObjects.registerSystem(name),
--     loads gos_<name>.bin if present, system:getModData() already
--     reflects disk state at this point (base class's own comment).
--   - setModDataKeys/setObjectModDataKeys are a REQUIRED explicit
--     whitelist -- only registered field names actually persist
--     (confirmed verbatim in STrapSystem:initSystem()).
--   - newLuaObjectAt(x,y,z) creates the persistent entry; the caller
--     then calls :initNew() explicitly (confirmed in
--     SCampfireSystem:addCampfire()) -- initNew() is NOT called
--     automatically.
--   - OnChunkLoaded(wx, wy) is the real per-system, per-chunk callback
--     (base class's own comment: exists specifically to avoid "using
--     the LoadGridSquare event and checking every location").
--   - SGlobalObjectSystem.RegisterSystemClass(class) is the real vanilla
--     bootstrap helper (wires Events.OnSGlobalObjectSystemInit itself --
--     do NOT also add that event manually, confirmed by reading the
--     helper's own body).
--   - square:AddWorldInventoryItem(itemType, x, y, z) confirmed real
--     with a bare item-type STRING (not just an instanced item) --
--     grepped server/ClientCommands.lua:130's own debug spawn command:
--     `sq:AddWorldInventoryItem("Base.Twigs", 0, 0, 0)`, the exact item
--     this probe also uses.
--
-- No require() for TWR's own cross-file locals -- see TWR.Constants'
-- header for why. `require "Map/SGlobalObjectSystem"` and
-- `require "Map/SGlobalObject"` below are different: they load
-- VANILLA base game files (not another TWR file), which is exactly
-- how STrapSystem.lua/SCampfireSystem.lua load them too -- confirmed
-- real, unrelated to the cross-mod require() limitation.
if isClient() then return end

require "Map/SGlobalObjectSystem"
require "Map/SGlobalObject"

-- Per-object wrapper. This system never attaches to a real IsoObject
-- (isValidIsoObject always false below), so stateFromIsoObject/
-- stateToIsoObject are never actually invoked -- left as harmless
-- no-ops rather than inheriting the base class's `error(...)` stubs,
-- in case some Java-side path calls them unexpectedly during this probe.
TWRPendingSpawnObject = SGlobalObject:derive("TWRPendingSpawnObject")

function TWRPendingSpawnObject:new(luaSystem, globalObject)
    local o = SGlobalObject.new(self, luaSystem, globalObject)
    return o
end

function TWRPendingSpawnObject:initNew()
    self.jobId = ""
    self.artifactKey = ""
    self.state = "WAITING_WORLD"
    self.actionType = "spawn_item"
    self.itemType = "Base.Twigs"
    self.placementMode = "ground"
    self.targetX = 0
    self.targetY = 0
    self.targetZ = 0
end

function TWRPendingSpawnObject:stateFromIsoObject(isoObject)
end

function TWRPendingSpawnObject:stateToIsoObject(isoObject)
end

-- System. Persisted name "twr_pending_spawn_test" -> expect
-- gos_twr_pending_spawn_test.bin per the base class's own comment --
-- Phase 1 of the validation doc asks to confirm the actual filename.
TWRPendingSpawnTest = SGlobalObjectSystem:derive("TWRPendingSpawnTest")

function TWRPendingSpawnTest:new()
    local o = SGlobalObjectSystem.new(self, "twr_pending_spawn_test")
    return o
end

function TWRPendingSpawnTest:initSystem()
    SGlobalObjectSystem.initSystem(self)
    self.system:setModDataKeys({})
    self.system:setObjectModDataKeys({
        "jobId", "artifactKey", "state", "actionType", "itemType",
        "placementMode", "targetX", "targetY", "targetZ",
    })
end

function TWRPendingSpawnTest:newLuaObject(globalObject)
    return TWRPendingSpawnObject:new(self, globalObject)
end

-- This system's GlobalObjects are pure data records, never a real
-- world IsoObject -- so nothing on any square ever "belongs" to it.
function TWRPendingSpawnTest:isValidIsoObject(isoObject)
    return false
end

-- Phase 0/1: create one persistent pending record at (x,y,z). Mirrors
-- SCampfireSystem:addCampfire()'s exact call order: create -> initNew()
-- -> set fields.
function TWRPendingSpawnTest:addPending(jobId, artifactKey, x, y, z, itemType)
    local luaObject = self:newLuaObjectAt(x, y, z)
    luaObject:initNew()
    luaObject.jobId = jobId
    luaObject.artifactKey = artifactKey
    luaObject.itemType = itemType or "Base.Twigs"
    luaObject.targetX = x
    luaObject.targetY = y
    luaObject.targetZ = z
    luaObject.state = "WAITING_WORLD"
    print("TWR PendingSpawnTest: addPending -- jobId=" .. jobId .. " artifactKey=" .. artifactKey .. " target=(" .. x .. "," .. y .. "," .. z .. ") pendingCount=" .. self:getLuaObjectCount())
    return luaObject
end

-- Debug/report helper -- Phase 1/2/4 all need "read back current state
-- without needing new code", this is that.
function TWRPendingSpawnTest:reportStatus()
    local count = self:getLuaObjectCount()
    print("TWR PendingSpawnTest: reportStatus -- pendingCount=" .. count)
    for i = 1, count do
        local luaObject = self.system:getObjectByIndex(i - 1):getModData()
        print("TWR PendingSpawnTest: [" .. i .. "] jobId=" .. tostring(luaObject.jobId)
            .. " artifactKey=" .. tostring(luaObject.artifactKey)
            .. " state=" .. tostring(luaObject.state)
            .. " itemType=" .. tostring(luaObject.itemType)
            .. " target=(" .. tostring(luaObject.targetX) .. "," .. tostring(luaObject.targetY) .. "," .. tostring(luaObject.targetZ) .. ")")
    end
    return count
end

-- Phase 3: the real per-chunk callback -- CONFIRMED this is only
-- invoked for chunks that actually contain this system's GlobalObjects
-- (base class's own comment), not a full-world scan. Executes the
-- pending action exactly once, marks/removes the record so Phase 4
-- (restart after completion) can prove no duplicate.
function TWRPendingSpawnTest:OnChunkLoaded(wx, wy)
    local globalObjects = self.system:getObjectsInChunk(wx, wy)
    print("TWR PendingSpawnTest: OnChunkLoaded -- chunk=(" .. wx .. "," .. wy .. ") objectsInChunk=" .. globalObjects:size())

    for i = 1, globalObjects:size() do
        local globalObject = globalObjects:get(i - 1)
        local luaObject = globalObject:getModData()

        if luaObject.state == "WAITING_WORLD" then
            print("TWR PendingSpawnTest: OnChunkLoaded -- resolving jobId=" .. tostring(luaObject.jobId) .. " at (" .. luaObject.targetX .. "," .. luaObject.targetY .. "," .. luaObject.targetZ .. ")")

            local okSquare, square = pcall(function() return getCell():getGridSquare(luaObject.targetX, luaObject.targetY, luaObject.targetZ) end)
            local placed = false
            if okSquare and square then
                local okAdd, spawnedItem = pcall(function() return square:AddWorldInventoryItem(luaObject.itemType, 0, 0, 0) end)
                placed = okAdd
                if okAdd and spawnedItem then
                    -- Stable markers per pending-job-durability.md's
                    -- "Artifact marker concept" -- proves whether this
                    -- specific B42 API accepts a persistent modData
                    -- marker on a loose world item (untested until now).
                    pcall(function()
                        spawnedItem:getModData().TWRArtifactKey = luaObject.artifactKey
                        spawnedItem:getModData().TWRJobId = luaObject.jobId
                    end)
                end
            end
            print("TWR PendingSpawnTest: OnChunkLoaded -- placed=" .. tostring(placed))

            luaObject.state = "APPLIED"
            self:removeLuaObject(luaObject)
            print("TWR PendingSpawnTest: OnChunkLoaded -- removed pending record, pendingCount now=" .. self:getLuaObjectCount())
        end
    end

    -- Returns the ArrayList to a pool for reuse (base class's own
    -- comment: "There's no harm if you forget to call it").
    self.system:finishedWithList(globalObjects)
end

-- Real vanilla bootstrap helper -- wires Events.OnSGlobalObjectSystemInit
-- (+ OnDestroyIsoThumpable/OnObjectAdded/OnObjectAboutToBeRemoved, all
-- harmless no-ops here since isValidIsoObject always returns false)
-- itself. Do NOT also register OnSGlobalObjectSystemInit manually --
-- confirmed by reading this helper's own body in SGlobalObjectSystem.lua.
SGlobalObjectSystem.RegisterSystemClass(TWRPendingSpawnTest)
