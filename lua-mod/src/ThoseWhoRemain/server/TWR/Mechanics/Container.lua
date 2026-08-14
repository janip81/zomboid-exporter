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
    local placements = {}
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
                -- FIX 2026-08-13 (spawn-result-tracking.md review Q1):
                -- record the ACTUAL resolved container square, not just
                -- the search center -- a caller building an audit
                -- record (Container.scatterAcrossMap) needs the real
                -- placement location, not the authoring anchor.
                table.insert(placements, { x = candidates[idx].x, y = candidates[idx].y, z = z, targetType = "container" })
                print("TWR.Mechanics.Container: scatterIntoExisting -- placed " .. itemType .. " at (" .. candidates[idx].x .. "," .. candidates[idx].y .. "," .. z .. ")")
            end
        end
    end

    return placed, placements
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
-- FIX 2026-08-13: promoted off DeferredArea.waitForSquare onto
-- TWR.PendingActions -- DeferredArea's watchers are RAM-only and lose
-- every pending point on any server restart with zero trace anywhere
-- (see zomboid-exporter-ideas/antagonist/pending-job-durability.md's
-- "Open problem"). TWR.PendingActions persists each point via a
-- mod-owned SGlobalObjectSystem instead, LIVE-CONFIRMED to survive a
-- real dedicated-server restart and reactivate via the same real,
-- targeted OnChunkLoaded callback DeferredArea was working around not
-- having (see sglobalobjectsystem-persistence-validation.md).
--
-- Does NOT scan the whole map up front -- most of those points are far
-- from any player and their squares are not resident server-side yet.
-- Each point is queued independently and only actually placed once a
-- player's proximity causes that square to load.
--
-- Success-first logging (spawn-result-tracking.md) now happens
-- centrally in PendingActions.lua's OnChunkLoaded, which calls
-- resolvePendingAction() below and emits exactly one durable outcome
-- per point -- not here, and not once per internal radius-retry probe.
--
-- Returns the list of pending-action records (one per point, in the
-- same order as `points`).
function Container.scatterAcrossMap(points, radius, seed, itemType)
    local pending = {}
    for i, point in ipairs(points) do
        -- Ad-hoc per-point job id -- no real DB-driven job dispatcher
        -- exists yet (see Emit.lua's header / spawn-result-tracking.md),
        -- so this just needs to be locally unique enough to tell two
        -- attempts apart in twr_job_attempts, not a real durable job
        -- identity. ZombRand, not os.time -- CONFIRMED nowhere in this
        -- codebase uses os.* (Kahlua support unconfirmed), ZombRand is
        -- already proven throughout (keyId generation etc).
        local jobId = "debug-mapscatter-" .. ZombRand(1000000000) .. "-" .. i
        pending[i] = TWR.PendingActions.request(jobId, jobId .. "-artifact", "scatter_items", "Container", point.x, point.y, point.z, {
            itemType = itemType,
            radius = radius,
            seed = seed + i,
        })
    end
    return pending
end

-- Entry point TWR.PendingActions.OnChunkLoaded actually calls (looked
-- up as TWR.Mechanics.Container.resolvePendingAction). Dispatches on
-- pending.actionType to the concrete resolver -- Container handles more
-- than one actionType (scatter_items, spawn_container), so this module
-- owns its own internal dispatch rather than PendingActions.lua needing
-- to know about every action type every module supports.
function Container.resolvePendingAction(pending)
    if pending.actionType == "scatter_items" then
        return Container.resolveScatterItems(pending)
    elseif pending.actionType == "spawn_container" then
        return Container.resolveSpawnContainer(pending)
    elseif pending.actionType == "spawn_locked_container" then
        return Container.resolveSpawnLockedContainer(pending)
    elseif pending.actionType == "spawn_item" then
        return Container.resolveSpawnItem(pending)
    end
    return false, "UNKNOWN_ACTION_TYPE", "Container.resolvePendingAction: no resolver for actionType=" .. tostring(pending.actionType)
end

-- Resolver for actionType="scatter_items". Contract (see
-- PendingActions.lua's header): return true, resolved (resolved =
-- {mechanic, placed, requested, artifactType, x, y, z, targetType,
-- targetSummary}) on success, or false, errorCode, errorDetail on
-- failure. Must NOT call TWR.Emit.jobResult itself -- PendingActions.lua
-- is the single place that does, so every migrated mechanic's audit
-- behavior stays centralized and consistent.
function Container.resolveScatterItems(pending)
    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then
        return false, "WORLD_UNAVAILABLE", "getCell() failed"
    end

    local params = pending.params or {}
    local itemType = params.itemType or "Base.Twigs"
    local baseRadius = params.radius or 5
    local seed = params.seed or 1

    -- FIX 2026-08-13: CONFIRMED live a real POI anchor can miss at the
    -- base radius (4/5 real building anchors hit on the first try at
    -- radius 5, 1 came back placed=0 -- the anchor coordinate itself
    -- was probably on the street rather than deep enough into the
    -- building). Retry the SAME anchor with a widening radius instead
    -- of just giving up -- the square is already loaded (this resolver
    -- only runs from OnChunkLoaded), so no need to defer again, this is
    -- an immediate in-place retry.
    --
    -- These radius passes are internal probes within ONE application
    -- attempt, not separate durable attempts -- attemptNo stays 1 in
    -- the emitted job result regardless of how many radii were tried
    -- (real attempt numbering belongs to the future DB job dispatcher,
    -- which doesn't exist yet). usedRadius is recorded in
    -- targetSummary instead, for debugging.
    local placed = 0
    local placements = nil
    local usedRadius = baseRadius
    local radiusAttempts = 0
    for a = 1, 3 do
        radiusAttempts = a
        usedRadius = baseRadius * a
        placed, placements = Container.scatterIntoExisting(cell, pending.targetX, pending.targetY, pending.targetZ, usedRadius, seed, 1, itemType)
        if placed > 0 then break end
    end
    print("TWR.Mechanics.Container: resolveScatterItems -- jobId=" .. tostring(pending.jobId) .. " (" .. pending.targetX .. "," .. pending.targetY .. "," .. pending.targetZ .. ") placed=" .. placed .. " (radius used=" .. usedRadius .. ", radius passes=" .. radiusAttempts .. ")")

    if placed > 0 then
        -- Record the ACTUAL resolved container square (placements[1]),
        -- not the authoring anchor (pending.targetX/Y/Z) --
        -- scatterIntoExisting returns this.
        local resolved = placements[1]
        return true, {
            mechanic = "Container.scatterAcrossMap",
            placed = placed,
            requested = 1,
            -- artifactType describes the placed THING (the item),
            -- targetType/targetSummary describe what it was placed
            -- INTO -- keep these distinct, do not conflate.
            artifactType = itemType,
            x = resolved.x,
            y = resolved.y,
            z = resolved.z,
            targetType = resolved.targetType,
            targetSummary = "existing world container (anchor " .. pending.targetX .. "," .. pending.targetY .. "," .. pending.targetZ .. ", resolved radius " .. usedRadius .. ")",
        }
    end

    return false, "NO_ELIGIBLE_TARGET", "no existing container found within radius " .. usedRadius .. " after " .. radiusAttempts .. " radius pass(es)"
end

-- Resolver for actionType="spawn_container" -- second mechanic
-- migrated onto TWR.PendingActions (2026-08-13), promoting the "Deferred-
-- area test" debug entry off DeferredArea.waitForSquare for the same
-- reason scatterAcrossMap was: a fresh-crate spawn queued at a remote,
-- not-yet-loaded square is exactly the kind of pending state that used
-- to vanish with zero trace on any server restart. Same contract as
-- resolveScatterItems above -- see PendingActions.lua's header.
--
-- params: { itemType (optional, default Base.Twigs), lockCode (optional
-- int -- if present, combination-locks the crate via lockByCode, same
-- as runContainer's own convention; if omitted the crate is left
-- unlocked, matching the original runDeferredArea debug test exactly).
function Container.resolveSpawnContainer(pending)
    local params = pending.params or {}
    local itemType = params.itemType or "Base.Twigs"

    local crate = Container.spawnBox(pending.targetX, pending.targetY, pending.targetZ)
    if not crate then
        return false, "SPAWN_FAILED", "Container.spawnBox() failed"
    end

    local placed = 0
    if itemType then
        local okC, container = safeCall(crate, "getContainer")
        if okC and container then
            local okAdd = safeCall(container, "AddItem", itemType)
            if okAdd then placed = 1 end
        end
    end

    local lockSummary = "unlocked"
    if params.lockCode then
        Container.lockByCode(crate, params.lockCode)
        lockSummary = "combination-locked (code " .. tostring(params.lockCode) .. ")"
    end

    Container.finalizeSpawn(crate)
    print("TWR.Mechanics.Container: resolveSpawnContainer -- jobId=" .. tostring(pending.jobId) .. " (" .. pending.targetX .. "," .. pending.targetY .. "," .. pending.targetZ .. ") placed=" .. placed .. " (" .. lockSummary .. ")")

    return true, {
        mechanic = "Container.spawnBox",
        placed = placed,
        requested = 1,
        artifactType = "container",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "world_object",
        targetSummary = "freshly spawned crate, " .. lockSummary,
    }
end

-- Resolver for actionType="spawn_locked_container" -- Gate 1 Phase 4
-- (quest-engine-driven dispatch). Generalizes resolveSpawnContainer
-- above with a caller-supplied, retry-stable keyId (matching Key.lua's
-- "backend snapshots the identity once" convention -- see its header)
-- and a list of contents to pre-fill, rather than one hardcoded
-- itemType/lockCode. Built for the KVLS fixture's Step 02 ("place
-- matching locked test container") but intentionally generic -- no
-- fixture-specific identity appears here, only the params contract.
--
-- params:
--   keyId    (required) -- same identity Key.lua's give_key action
--            hands to the player; setKeyId on the crate must match.
--   contents (optional array). Each entry:
--            {kind="item", itemType="Base.X", quantity=1 (optional)}
--            {kind="recorded_media", payload={...}} -- see
--            RecordedMedia.buildItem's own payload contract.
--            Unknown/missing "kind" is skipped (logged), not fatal --
--            one bad content entry must not abort the whole container.
--
-- Never gives the key to a player from here -- this resolver has no
-- specific player to hand it to (it's a spatial job, not a player-
-- targeted one). The matching key is a SEPARATE action
-- (Key.give_key), authored as its own DB step action.
--
-- NOT YET PROVEN LIVE. Built directly on top of Container.spawnBox/
-- lockByKey/finalizeSpawn (all CONFIRMED live) and
-- RecordedMedia.buildItem (also CONFIRMED live), so the pieces are
-- proven individually, but this exact composition (fill THEN lock THEN
-- finalize, for a DB-driven job rather than a debug command) has not
-- been exercised on the real dedicated server yet. Update
-- antagonist/DONE.md / antagonist/tests/ once actually confirmed
-- (TRANSPORT-A / KVLS-1..3), not before.
function Container.resolveSpawnLockedContainer(pending)
    local params = pending.params or {}
    if params.keyId == nil then
        return false, "KEYID_REQUIRED", "Container.resolveSpawnLockedContainer -- pending.params.keyId missing"
    end

    local crate = Container.spawnBox(pending.targetX, pending.targetY, pending.targetZ)
    if not crate then
        return false, "SPAWN_FAILED", "Container.spawnBox() failed"
    end

    local okC, container = safeCall(crate, "getContainer")
    local placed = 0
    local requested = 0
    if okC and container then
        for _, entry in ipairs(params.contents or {}) do
            requested = requested + 1
            if entry.kind == "item" then
                local okAdd = safeCall(container, "AddItem", entry.itemType)
                if okAdd then placed = placed + 1 end
            elseif entry.kind == "recorded_media" then
                if TWR.Mechanics.RecordedMedia and TWR.Mechanics.RecordedMedia.buildItem then
                    local item, buildErr = TWR.Mechanics.RecordedMedia.buildItem(entry.payload)
                    if item then
                        local okAdd = safeCall(container, "AddItem", item)
                        if okAdd then
                            placed = placed + 1
                            pcall(function() sendAddItemToContainer(container, item) end)
                        end
                    else
                        print("TWR.Mechanics.Container: resolveSpawnLockedContainer -- recorded_media entry failed: " .. tostring(buildErr))
                    end
                else
                    print("TWR.Mechanics.Container: resolveSpawnLockedContainer -- TWR.Mechanics.RecordedMedia not loaded, skipping recorded_media entry")
                end
            else
                print("TWR.Mechanics.Container: resolveSpawnLockedContainer -- unknown contents entry kind=" .. tostring(entry.kind) .. ", skipping")
            end
        end
    end

    Container.lockByKey(crate, nil, params.keyId)
    Container.finalizeSpawn(crate)
    print("TWR.Mechanics.Container: resolveSpawnLockedContainer -- jobId=" .. tostring(pending.jobId) .. " (" .. pending.targetX .. "," .. pending.targetY .. "," .. pending.targetZ .. ") placed=" .. placed .. "/" .. requested .. " keyId=" .. tostring(params.keyId))

    return true, {
        mechanic = "Container.resolveSpawnLockedContainer",
        placed = placed,
        requested = requested,
        artifactType = "locked_container",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "world_object",
        targetSummary = "freshly spawned crate, key-locked (keyId=" .. tostring(params.keyId) .. ")",
    }
end

-- Resolver for actionType="spawn_item" -- simplest possible generic
-- ground-spawn, no container/lock involved. Built for the KVLS
-- fixture's Step 06 (final harmless reward), mirrors Key.lua's own
-- ground-spawn resolver exactly (same AddWorldInventoryItem pattern).
function Container.resolveSpawnItem(pending)
    local params = pending.params or {}
    local itemType = params.itemType or "Base.Twigs"

    local okSq, square = pcall(function() return getCell():getGridSquare(pending.targetX, pending.targetY, pending.targetZ) end)
    if not okSq or not square then
        return false, "SQUARE_NOT_LOADED", "Container.resolveSpawnItem -- target square not loaded"
    end

    local okAdd, item = safeCall(square, "AddWorldInventoryItem", itemType, 0.5, 0.5, 0)
    if not okAdd then
        return false, "SPAWN_FAILED", "Container.resolveSpawnItem -- AddWorldInventoryItem failed"
    end

    return true, {
        mechanic = "Container.resolveSpawnItem",
        placed = 1,
        requested = 1,
        artifactType = itemType,
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "ground",
        targetSummary = "ground-spawned item (" .. tostring(itemType) .. ")",
    }
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
    -- FIX 2026-08-13, found live via a fixture-test crate that visibly
    -- never appeared despite spawnBox() logging success: the previous
    -- comment here ("nil is proven safe there") was WRONG. Real
    -- ISWoodenContainer:new() calls `o:init()` FIRST (inherited from
    -- ISBuildingObject:derive) -- that base init() is what actually
    -- populates canPassThrough/canBarricade/thumpDmg/isDoor/
    -- isDoorFrame/crossSpeed/canBePlastered/hoppable/isThumpable/
    -- isFloor with real primitive defaults, BEFORE ISWoodenContainer:new()
    -- overrides only the handful of fields it cares about. Our
    -- hand-rolled table skipped that base init() entirely, so those
    -- fields were genuinely nil -- and buildUtil.setInfo()
    -- (server/BuildingObjects/ISBuildUtil.lua:362) unconditionally
    -- calls javaObject:setThumpDmg/setIsDoor/setCrossSpeed/etc. on all
    -- of them, which throws a NullPointerException converting Lua nil
    -- to a Java primitive (int/boolean/float) -- silently, from Lua's
    -- perspective: Kahlua logs each exception but does not raise a
    -- catchable Lua error, so setInfo() "succeeds", AddSpecialObject()
    -- "succeeds", spawnBox() returns a non-nil javaObject, and the
    -- crate is nonetheless never actually placed
    -- ("ERROR: IsoThumpable not found on square" fires later). Values
    -- below are copied exactly from ISBuildingObject:init()
    -- (media/lua/server/BuildingObjects/ISBuildingObject.lua:458-473).
    local companion = {
        isContainer = true,
        blockAllTheSquare = true,
        name = "Wooden Crate",
        dismantable = true,
        canBeAlwaysPlaced = true,
        canBeLockedByPadlock = true,
        buildLow = true,
        modData = {},
        canPassThrough = false,
        canBarricade = false,
        thumpDmg = 8,
        isDoor = false,
        isDoorFrame = false,
        crossSpeed = 1.0,
        canBePlastered = false,
        hoppable = false,
        isThumpable = true,
        isFloor = false,
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

    -- Defense in depth against the exact failure mode found live
    -- 2026-08-13: Java-level exceptions inside buildUtil.setInfo() are
    -- logged but NOT raised as catchable Lua errors (Kahlua swallows
    -- them per-call), so a partially-broken javaObject can sail through
    -- every pcall above as "success" while never actually being
    -- registered on the square (engine logs "IsoThumpable not found on
    -- square" separately, invisible to this function unless checked
    -- explicitly). Confirm the object genuinely landed before trusting it.
    local okVerify, isPresent = pcall(function() return square:getSpecialObjects():contains(javaObject) end)
    if not okVerify or not isPresent then
        print("TWR.Mechanics.Container: spawnBox -- object not actually present on square after AddSpecialObject (silent setInfo/engine failure) -- treating as failed")
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
