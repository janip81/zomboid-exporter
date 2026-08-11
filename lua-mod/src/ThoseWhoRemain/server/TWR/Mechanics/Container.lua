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
function Container.findExistingContainer(square)
    local okObjs, objects = safeCall(square, "getObjects")
    if not okObjs or not objects then return nil end

    for i = 0, objects:size() - 1 do
        local obj = objects:get(i)
        local okC, container = safeCall(obj, "getContainer")
        if okC and container then return container end
    end

    return nil
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
                local container = Container.findExistingContainer(square)
                if container then
                    table.insert(candidates, { container = container, x = centerX + dx, y = centerY + dy })
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
            local okAdd = safeCall(candidates[idx].container, "AddItem", itemType)
            if okAdd then placed = placed + 1 end
        end
    end

    return placed
end

-- Spawns a brand-new lootable box (real IsoThumpable + ItemContainer)
-- at the given square. CONFIRMED real via grep against vanilla's OWN
-- test suite (client/Tests/TimedActionsTests.lua
-- Tests.destroy_crate/dismantle_crate): ISWoodenContainer:new(sprite,
-- sprite):create(x,y,z,north,sprite) with sprite="carpentry_01_19".
-- Retrieved via square:getSpecialObjects():get(...) -- a genuinely
-- different accessor than getObjects(), confirmed only from that same
-- vanilla test code.
--
-- bo.player defaults to 0 (not tied to any specific connecting
-- player's client player-num) -- CONFIRMED SAFE live 2026-08-11 (TEST
-- I): bo:create() internally calls buildUtil.consumeMaterial(), which
-- was flagged as an unverified risk (the confirmed vanilla usage always
-- sets a real client-side PLAYER_NUM first) but live-tested with NO
-- materials consumed from any player's inventory regardless.
--
-- Returns the created IsoThumpable (the "crate"), or nil on failure.
function Container.spawnBox(x, y, z)
    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then return nil end

    local okSq, square = pcall(function() return cell:getGridSquare(x, y, z) end)
    if not okSq or not square then return nil end

    local okBo, bo = pcall(function() return ISWoodenContainer:new("carpentry_01_19", "carpentry_01_19") end)
    if not okBo or not bo then return nil end
    bo.player = 0

    local okCreate = pcall(function() bo:create(x, y, z, bo.north, bo.sprite) end)
    if not okCreate then return nil end

    local okSpecial, specialObjects = safeCall(square, "getSpecialObjects")
    if not okSpecial or not specialObjects or specialObjects:size() == 0 then return nil end

    return specialObjects:get(specialObjects:size() - 1)
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
