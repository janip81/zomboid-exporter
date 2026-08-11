-- TWR.Mechanics.Door -- locks an EXISTING map door to a controlled key,
-- generic to any real door on the loaded map (not a spawned object).
--
-- CONFIRMED live 2026-08-11 (AntagonistProbe TEST B, round 3 -- full
-- mystery solved via ChatGPT's decompiled B42.20.2 IsoDoor research):
-- IsoDoor:canBeOpenFromInside(character) runs BEFORE any key check and
-- returns "open" whenever the player is indoors on EITHER side of the
-- door, completely independent of lock state -- every earlier "test
-- from both sides" attempt was actually still triggering this native
-- shortcut. The real key check (once past that shortcut) treats EITHER
-- door:isLockedByKey() OR door:getModData().CustomLock==true as a
-- keyed lock, then checks
-- character:getInventory():haveThisKeyId(keyId). CustomLock is
-- per-door modData (safe) -- NOT the shared sprite `forceLocked`
-- property, which would leak onto every other door using the same
-- sprite (deliberately not used).
--
-- Also requires door:syncIsoObject(false, 0, nil, nil) immediately
-- after setLockedByKey -- confirmed from vanilla's own
-- shared/TimedActions/ISLockDoor.lua:complete(), without which the
-- lock state isn't recognized by the real interaction check.
--
-- IMPORTANT CALLER-SIDE CAVEAT (proven live): this only actually gates
-- access when tested from the EXTERIOR side of the door. Indoor-side
-- interaction hits the native open-from-inside shortcut regardless of
-- any of these flags -- that's vanilla behavior, not a bug in this
-- mechanic.
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
TWR.Mechanics.Door = TWR.Mechanics.Door or {}

local Door = TWR.Mechanics.Door

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Finds the first IsoDoor object on a square, or nil.
function Door.findDoorOnSquare(square)
    if not square then return nil end
    local okObjs, objects = safeCall(square, "getObjects")
    if not okObjs or not objects then return nil end

    for i = 0, objects:size() - 1 do
        local obj = objects:get(i)
        local okIsDoor, isDoor = pcall(function() return instanceof(obj, "IsoDoor") end)
        if okIsDoor and isDoor then return obj end
    end

    return nil
end

-- Scans a square plus its 4 orthogonal neighbors -- standing precisely
-- on a door's own single tile proved too finicky to hit reliably
-- across several live attempts.
function Door.findNearbyDoor(cell, x, y, z)
    local okSq, square = pcall(function() return cell:getGridSquare(x, y, z) end)
    if okSq and square then
        local door = Door.findDoorOnSquare(square)
        if door then return door end
    end

    local offsets = {{1,0}, {-1,0}, {0,1}, {0,-1}}
    for _, off in ipairs(offsets) do
        local okNeighbor, neighborSquare = pcall(function()
            return cell:getGridSquare(x + off[1], y + off[2], z)
        end)
        if okNeighbor and neighborSquare then
            local door = Door.findDoorOnSquare(neighborSquare)
            if door then return door end
        end
    end

    return nil
end

-- Locks an existing door to keyId (generates one via ZombRand if not
-- given). If giveKeyToPlayer is provided, a matching Base.Key1 item is
-- added to their inventory.
function Door.lockToKey(door, keyId, giveKeyToPlayer)
    keyId = keyId or ZombRand(100000000)

    safeCall(door, "setKeyId", keyId)
    safeCall(door, "setLocked", true)
    safeCall(door, "setLockedByKey", true)
    safeCall(door, "syncIsoObject", false, 0, nil, nil)
    safeCall(door, "setIsLocked", true)

    local okModData, modData = safeCall(door, "getModData")
    if okModData and modData then
        pcall(function() modData.CustomLock = true end)
    end

    if giveKeyToPlayer then
        local okKeyItem, key = pcall(function() return instanceItem("Base.Key1") end)
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

-- Converts an already lockToKey()'d door into a plain, permanently
-- unlocked door -- no key required ever again, closing it does NOT
-- re-lock it. CONFIRMED live 2026-08-11: a lockToKey()'d door's
-- modData.CustomLock is what makes ISLockDoor:isValid()
-- (shared/TimedActions/ISLockDoor.lua) unconditionally reject the
-- normal Lock/Unlock action for anyone without the matching key --
-- exactly the "always relock, key required forever" behavior. This
-- function is the inverse: drops setLockedByKey/setLocked/setIsLocked
-- back to false and clears modData.CustomLock, so the door goes back
-- to being an ordinary vanilla door.
--
-- This is a primitive only -- it does NOT decide WHEN to call itself.
-- That decision (permanent vs. relock vs. future consume_key) belongs
-- to the quest/job action layer, per its own unlock_policy field --
-- see antagonist/quest-engine-extensibility.md. Door.lua stays generic:
-- lockToKey() for the initial lock (== "relock" policy behavior
-- as-is), unlockPermanent() for converting a solved door to
-- permanently open (== "permanent" policy, the documented default).
function Door.unlockPermanent(door)
    safeCall(door, "setLockedByKey", false)
    safeCall(door, "setLocked", false)
    safeCall(door, "setIsLocked", false)

    local okModData, modData = safeCall(door, "getModData")
    if okModData and modData then
        pcall(function() modData.CustomLock = nil end)
    end

    safeCall(door, "syncIsoObject", false, 0, nil, nil)
end
