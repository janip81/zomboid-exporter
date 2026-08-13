-- TWR.Mechanics.Container -- generic container primitives: find an
-- existing container on a square, scatter items into several existing
-- containers within a radius, and spawn a brand-new lockable box.
--
-- Every function here is proven against the real installed B42 build
-- (existing-world-test-matrix.md rows: "Existing-container insertion",
-- "Repeated ambient scatter", "Runtime-spawned box/chest with real
-- inventory", "Runtime-spawned box key-locked", "Runtime-spawned box
-- combination-locked") -- this module doesn't invent anything, it just
-- generalizes AntagonistProbe.lua's TEST A2/G/I/J/K/L into reusable
-- functions.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. This file has no load-time side effects
-- (pure function definitions), but guarding anyway for consistency and
-- so a client never pointlessly parses/executes it.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Container = TWR.Mechanics.Container or {}

local Container = TWR.Mechanics.Container

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Finds the first real container on a square, via the same
-- obj:getContainer() scan TEST A2 proved. Returns nil if none.
-- Returns container, obj -- obj (the parent IsoObject) is needed by
-- callers so they can call obj:transmitCompleteItemToClients() after
-- mutating the container -- CONFIRMED real vanilla pattern, grepped
-- server/Map/MapObjects/MOFeedingTrough.lua's own
-- MOFeedingTrough.generateContainer() (fills trough:getContainer() via
-- AddItem in a loop) followed immediately by
-- isoObject:transmitCompleteItemToClients() on the PARENT object, not
-- the container -- same as Container.finalizeSpawn()'s pattern for
-- freshly-spawned crates.
-- Returns container, obj. obj is returned for callers that might need
-- the parent IsoObject, but see the REVERTED note in scatterIntoExisting
-- below -- do NOT call transmitCompleteItemToClients() on it if it's an
-- already-existing (not freshly server-created) object.
function Container.findExistingContainer(square)
    local okObjs, objects = safeCall(square, "getObjects")
    if not okObjs or not objects then return nil, nil end

    for i = 0, objects:size() - 1 do
        local obj = objects:get(i)
        local okC, container = safeCall(obj, "getContainer")
        if okC and container then return container, obj end
    end

    return nil, nil
end

-- Scans a square grid centered on (centerX, centerY, z) out to radius,
-- collects every real existing container found (TEST A2's mechanism,
-- same as findExistingContainer above), then inserts itemType into a
-- SEEDED random subset (up to count, or fewer if not enough candidates
-- exist). Same seed -> same candidates picked -> reproducible, proven
-- live 2026-08-11 (TEST G: identical squares chosen across two runs
-- with the same seed after a reload).
--
-- Uses a hand-rolled LCG, not math.random -- CONFIRMED live that Kahlua
-- (PZ's Lua VM) does not implement Lua's stdlib RNG at all
-- (math.randomseed/math.random crashed immediately; grepped, zero
-- usage anywhere in the installed B42 tree, only ZombRand/ZombRandFloat
-- are ever used).
--
-- Deliberately has NO anti-duplication logic -- re-running the same
-- seed against containers that already received an item will add a
-- second copy. Anti-duplication is a job-system concern (see
-- quest-rules-schema.md open question 4), not this primitive's job.
--
-- Returns the number of items actually placed.
function Container.scatterIntoExisting(cell, centerX, centerY, z, radius, seed, count, itemType)
    local candidates = {}
    for dx = -radius, radius do
        for dy = -radius, radius do
            local okSq, square = pcall(function() return cell:getGridSquare(centerX + dx, centerY + dy, z) end)
            if okSq and square then
                local container, obj = Container.findExistingContainer(square)
                if container then
                    table.insert(candidates, { container = container, obj = obj, x = centerX + dx, y = centerY + dy })
                end
            end
        end
    end

    if #candidates == 0 then return 0 end

    local lcgState = seed
    local function seededRandomIndex(maxValue)
        lcgState = (lcgState * 1103515245 + 12345) % 2147483648
        return (lcgState % maxValue) + 1
    end

    local placed = 0
    local attempts = 0
    local usedIndices = {}
    while placed < count and attempts < #candidates * 4 do
        attempts = attempts + 1
        local idx = seededRandomIndex(#candidates)
        if not usedIndices[idx] then
            usedIndices[idx] = true
            -- FIX 2026-08-13: transmitCompleteItemToClients() (see the
            -- REVERTED history this comment replaces, still true of why
            -- that approach was wrong) was a full-object replace and is
            -- NOT the targeted mechanism this needed. sendAddItemToContainer
            -- (container, item) is that targeted mechanism -- CONFIRMED
            -- real and already load-bearing elsewhere in this exact file:
            -- lockByKey/lockByPadlock above call
            -- `inv:AddItem(item)` followed by
            -- `sendAddItemToContainer(inv, item)` to sync a single
            -- server-added item into an already-existing, already-
            -- client-loaded container (a player's inventory) without
            -- recreating/replacing the container itself. A world
            -- container is the same ItemContainer type under the hood --
            -- applying the identical pattern here targets the single
            -- added item instead of the whole object, so it should not
            -- reproduce the ghost-duplicate bug transmitCompleteItemToClients
            -- caused. AddItem(itemType) (a module string, not an item
            -- instance) returns the newly-created item -- captured here
            -- so sendAddItemToContainer has the exact instance to sync.
            -- CONFIRMED live on dedicated MP 2026-08-13: item appeared in
            -- the existing container with no relog required, and no
            -- ghost/duplicate object -- the failure mode
            -- transmitCompleteItemToClients caused.
            local okAdd, addedItem = safeCall(candidates[idx].container, "AddItem", itemType)
            if okAdd then
                placed = placed + 1
                if addedItem then
                    pcall(function() sendAddItemToContainer(candidates[idx].container, addedItem) end)
                end
                print("TWR.Mechanics.Container: scatterIntoExisting -- placed " .. itemType .. " at (" .. candidates[idx].x .. "," .. candidates[idx].y .. "," .. z .. ")")
            end
        end
    end

    return placed
end

-- Scatters itemType across many widely-separated points on the map (a
-- flyer pinned up in houses/offices all over town, not one small
-- radius) -- e.g. { {x=10182,y=12791,z=0}, {x=8341,y=11750,z=0}, ... }.
-- Each point gets exactly ONE item placed into whatever existing
-- container scatterIntoExisting finds within radius tiles of it (seed
-- reused per-point so repeat runs against the same point are
-- reproducible; different points naturally scan different candidate
-- sets so this is not the "same subset every time" concern
-- scatterIntoExisting's own header warns about for a single center).
--
-- Does NOT scan the whole map up front -- most of those points are far
-- from any player and their squares are not resident server-side yet
-- (CONFIRMED live, see DeferredArea.lua's header). Each point is queued
-- independently via DeferredArea.waitForSquare and only actually placed
-- once a player's proximity causes that square to load -- matches TEST
-- M's proven production pattern (verified anchor -> wait for real chunk
-- load -> scan -> place), just fanned out to many anchors at once
-- instead of one.
--
-- Returns the list of DeferredArea watcher handles (one per point, in
-- the same order as `points`), so a caller can DeferredArea.cancel()
-- any still-pending ones early if needed.
function Container.scatterAcrossMap(points, radius, seed, itemType)
    local handles = {}
    for i, point in ipairs(points) do
        -- Ad-hoc per-point job id -- no real DB-driven job dispatcher
        -- exists yet (see Emit.lua's header / spawn-result-tracking.md),
        -- so this just needs to be locally unique enough to tell two
        -- attempts apart in twr_job_attempts, not a real durable job
        -- identity. ZombRand, not os.time -- CONFIRMED nowhere in this
        -- codebase uses os.* (Kahlua support unconfirmed), ZombRand is
        -- already proven throughout (keyId generation etc).
        local jobId = "debug-mapscatter-" .. ZombRand(1000000000) .. "-" .. i

        handles[i] = TWR.Mechanics.DeferredArea.waitForSquare(point.x, point.y, point.z, function(square)
            local okCell, cell = pcall(function() return getCell() end)
            if not okCell or not cell then return end

            -- FIX 2026-08-13: CONFIRMED live a real POI anchor can miss
            -- at the base radius (4/5 real building anchors hit on the
            -- first try at radius 5, 1 came back placed=0 -- the anchor
            -- coordinate itself was probably on the street rather than
            -- deep enough into the building). Retry the SAME anchor with
            -- a widening radius instead of just giving up -- the square
            -- is already loaded from waitForSquare above, so no need to
            -- defer again, this is an immediate in-place retry.
            local placed = 0
            local usedRadius = radius
            local attempt = 0
            for a = 1, 3 do
                attempt = a
                usedRadius = radius * a
                placed = Container.scatterIntoExisting(cell, point.x, point.y, point.z, usedRadius, seed + i, 1, itemType)
                if placed > 0 then break end
            end
            print("TWR.Mechanics.Container: scatterAcrossMap -- point " .. i .. " (" .. point.x .. "," .. point.y .. "," .. point.z .. ") placed=" .. placed .. " (radius used=" .. usedRadius .. ")")

            -- Success-first logging (spawn-result-tracking.md): ONE
            -- durable outcome per point here, not one per internal
            -- radius-retry probe above (those stay in the print() above
            -- only, for live debugging).
            if placed > 0 then
                pcall(function()
                    TWR.Emit.jobResult({
                        jobId = jobId,
                        attemptNo = attempt,
                        actionType = "scatter_items",
                        mechanic = "Container.scatterAcrossMap",
                        result = "applied",
                        placed = placed,
                        requested = 1,
                        artifactKey = jobId .. "-artifact",
                        x = point.x,
                        y = point.y,
                        z = point.z,
                        targetType = "container",
                    })
                end)
            else
                pcall(function()
                    TWR.Emit.jobResult({
                        jobId = jobId,
                        attemptNo = attempt,
                        actionType = "scatter_items",
                        mechanic = "Container.scatterAcrossMap",
                        result = "final_error",
                        errorCode = "NO_ELIGIBLE_TARGET",
                        errorDetail = "no existing container found within radius " .. usedRadius .. " after " .. attempt .. " attempt(s)",
                        placed = 0,
                        requested = 1,
                        x = point.x,
                        y = point.y,
                        z = point.z,
                    })
                end)
            end
        end)
    end
    return handles
end

-- Spawns a brand-new lootable box (real IsoThumpable + ItemContainer)
-- at the given square, via DIRECT authoritative server creation --
-- NOT ISWoodenContainer:create(). CONFIRMED FAIL on dedicated MP
-- 2026-08-11: ISWoodenContainer:create() calls
-- buildUtil.consumeMaterial(self) (server/BuildingObjects/
-- ISBuildUtil.lua), whose guard is `if not ISItem or not ISItem.player
-- then return {} end`. The old code set bo.player = 0 (a NUMBER, not
-- nil) -- `not 0` is false in Lua (0 is truthy), so the guard passes
-- and the function proceeds to `playerObj:getInventory()` on the bare
-- number 0, throwing "Object tried to call nil". SP is misleadingly
-- forgiving here: SP runs with isServer()==false, so
-- consumeMaterial() either short-circuits entirely (debug/cheat mode)
-- or converts the number via getSpecificPlayer() (a client-only
-- concept) before ever calling :getInventory() -- neither of those
-- branches exist on a true dedicated server, where isServer()==true.
--
-- This implementation instead replicates ISWoodenContainer:create()
-- (server/BuildingObjects/ISWoodenContainer.lua) line-for-line MINUS
-- the one buildUtil.* call that needs a player:
--   KEPT:     IsoThumpable.new(cell, sq, sprite, north, companion) --
--             companion is the same kind of Lua "ISItem" table every
--             ISBuildingObject-derived class passes as IsoThumpable's
--             own 5th ctor arg. The container itself is provisioned
--             via buildUtil.setInfo() calling
--             javaObject:setIsContainer(true) below, not a separate
--             ItemContainer.new() call (that pattern is used elsewhere
--             in vanilla for non-IsoThumpable IsoObjects, e.g.
--             server/Camping/SCampfireGlobalObject.lua's
--             addContainer(), but is NOT how IsoThumpable-based
--             containers like this crate work).
--   KEPT:     buildUtil.setInfo(javaObject, companion) -- CONFIRMED
--             player-independent (read the full function body): pure
--             javaObject:set*(companion.field) calls, no
--             ISItem.player access anywhere. This is what actually
--             makes the object a real lootable container and sets
--             canBeLockByPadlock (needed for lockByCode/lockByKey/
--             lockByPadlock).
--   DROPPED:  buildUtil.consumeMaterial(self) -- the crash cause, and
--             semantically wrong anyway: a DB-driven quest job
--             spawning a quest container has no player materials to
--             consume.
--   KEPT:     setMaxHealth/setHealth/setBreakSound,
--             sq:AddSpecialObject() -- unchanged, player-independent,
--             confirmed straight from the same
--             ISWoodenContainer:create().
--
-- Deliberately does NOT call transmitCompleteItemToClients() -- see
-- Container.finalizeSpawn() below. LIVE FINDING 2026-08-11 (TEST N
-- round 1): transmitting immediately after creation, before the
-- caller fills the container and applies a lock, means the client
-- receives that as the "complete" snapshot -- AddItem()/
-- setLockedByCode() afterward only mutate server-side Java state with
-- no follow-up packet, so the item silently never appears
-- client-side. The caller must fill + lock first, then call
-- Container.finalizeSpawn(crate) exactly once.
--
-- FULLY PROVEN live on the real dedicated MP server 2026-08-11 (TEST
-- N): crate spawns with no crash, container+item sync to a connected
-- client, a non-admin ("user" access level, RCON-confirmed) could
-- NOT see contents or remove the lock without the correct code, and
-- the correct code both removed the lock and granted loot access.
-- Survived a full server restart.
--
-- Returns the created IsoThumpable (the "crate"), or nil on failure.
function Container.spawnBox(x, y, z)
    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then
        print("TWR.Mechanics.Container: spawnBox -- getCell() failed: " .. tostring(cell))
        return nil
    end

    local okSq, square = pcall(function() return cell:getGridSquare(x, y, z) end)
    if not okSq or not square then
        print("TWR.Mechanics.Container: spawnBox -- getGridSquare(" .. x .. "," .. y .. "," .. z .. ") failed/nil: " .. tostring(square))
        return nil
    end

    local sprite = "carpentry_01_19"
    -- Same field set ISWoodenContainer:new() sets on itself (grepped
    -- from ISWoodenContainer.lua) -- everything setInfo() reads that
    -- we don't explicitly set (canPassThrough, canBarricade,
    -- thumpDmg, isDoor, isDoorFrame, crossSpeed, canBePlastered,
    -- hoppable, isThumpable, isFloor) is nil on the real
    -- ISWoodenContainer's table too in normal vanilla usage, and
    -- setInfo() already runs successfully against that every time a
    -- player builds a real crate -- nil is proven safe there.
    local companion = {
        isContainer = true,
        blockAllTheSquare = true,
        name = "Wooden Crate",
        dismantable = true,
        canBeAlwaysPlaced = true,
        canBeLockedByPadlock = true,
        buildLow = true,
        modData = {},
    }

    local okJo, javaObject = pcall(function() return IsoThumpable.new(cell, square, sprite, false, companion) end)
    if not okJo or not javaObject then
        print("TWR.Mechanics.Container: spawnBox -- IsoThumpable.new() failed: " .. tostring(javaObject))
        return nil
    end

    local okInfo, infoErr = pcall(function() buildUtil.setInfo(javaObject, companion) end)
    if not okInfo then
        print("TWR.Mechanics.Container: spawnBox -- buildUtil.setInfo() failed: " .. tostring(infoErr))
        return nil
    end

    safeCall(javaObject, "setMaxHealth", 200)
    local okMaxHealth, maxHealth = safeCall(javaObject, "getMaxHealth")
    if okMaxHealth then
        safeCall(javaObject, "setHealth", maxHealth)
    end
    pcall(function() javaObject:setBreakSound(IsoThumpable.GetBreakFurnitureSound(sprite)) end)

    local okAdd, addErr = pcall(function() square:AddSpecialObject(javaObject) end)
    if not okAdd then
        print("TWR.Mechanics.Container: spawnBox -- square:AddSpecialObject() failed: " .. tostring(addErr))
        return nil
    end

    return javaObject
end

-- Call exactly once, after the caller has finished filling the
-- container (AddItem) and applying any lock (lockByCode/lockByKey/
-- lockByPadlock) -- pushes the ONE full, consistent snapshot to
-- connected clients. See spawnBox()'s header for why this can't
-- happen inside spawnBox() itself.
function Container.finalizeSpawn(crate)
    safeCall(crate, "transmitCompleteItemToClients")
end

-- Locks crate with a key, generic to any IsoThumpable (not
-- door-specific) -- CONFIRMED via vanilla's own
-- shared/TimedActions/ISPadlockAction.lua, same setKeyId mechanism
-- TEST B already proved on doors. If giveKeyToPlayer is provided, a
-- matching Base.KeyPadlock item is added to their inventory (mirrors
-- vanilla's own bookkeeping) -- omit to leave the box permanently
-- key-locked with no key issued.
--
-- keyId is optional (generates one via ZombRand if omitted) -- per
-- CGPT-101 review, a future retryable DB job should generate/snapshot
-- the keyId once backend-side and pass the SAME value on every retry,
-- matching Door.lockToKey's already-correct pattern, rather than this
-- function silently minting a new identity on each call.
function Container.lockByKey(crate, giveKeyToPlayer, keyId)
    keyId = keyId or ZombRand(100000000)
    safeCall(crate, "setKeyId", keyId)
    safeCall(crate, "setLockedByPadlock", true)
    safeCall(crate, "sync")

    if giveKeyToPlayer then
        local okKeyItem, key = pcall(function() return instanceItem("Base.KeyPadlock") end)
        if okKeyItem and key then
            safeCall(key, "setKeyId", keyId)
            local okInv, inv = safeCall(giveKeyToPlayer, "getInventory")
            if okInv and inv then
                safeCall(inv, "AddItem", key)
                pcall(function() sendAddItemToContainer(inv, key) end)
            end
        end
    end

    return keyId
end

-- Same underlying mechanism as lockByKey (setLockedByPadlock +
-- setKeyId) -- CONFIRMED identical live 2026-08-11 (TEST K). Only
-- difference is which item represents the lock in inventory
-- (Base.Padlock, with setNumberOfKey(1) matching vanilla's own
-- ISPadlockAction bookkeeping).
--
-- keyId is optional, same retry-determinism reasoning as lockByKey
-- above (per CGPT-101 review).
function Container.lockByPadlock(crate, giveKeyToPlayer, keyId)
    keyId = keyId or ZombRand(100000000)
    safeCall(crate, "setLockedByPadlock", true)
    safeCall(crate, "setKeyId", keyId)
    safeCall(crate, "sync")

    if giveKeyToPlayer then
        local okPadlock, padlock = pcall(function() return instanceItem("Base.Padlock") end)
        if okPadlock and padlock then
            safeCall(padlock, "setKeyId", keyId)
            safeCall(padlock, "setNumberOfKey", 1)
            local okInv, inv = safeCall(giveKeyToPlayer, "getInventory")
            if okInv and inv then
                safeCall(inv, "AddItem", padlock)
                pcall(function() sendAddItemToContainer(inv, padlock) end)
            end
        end
    end

    return keyId
end

-- Locks crate with a 3-digit code (0-999) -- CONFIRMED FULLY WORKING
-- 2026-08-11 after a 6-round live debugging saga (see
-- existing-world-test-matrix.md). The mechanism is ONLY
-- setLockedByCode(code) + sync() -- crucially NOT combined with
-- setLockedByPadlock, which live testing proved creates a SECOND
-- independent lock layer requiring two separate removals. Matches
-- vanilla's own ISPadlockByCodeAction.lua "apply" branch exactly.
-- Native code-entry UI (ISDigitalCode) handles player-side entry with
-- no custom UI needed.
function Container.lockByCode(crate, code)
    safeCall(crate, "setLockedByCode", code)
    safeCall(crate, "sync")
end
