-- TWR.Mechanics.Key -- generic controlled-key creation with a caller-
-- supplied, retry-stable keyId.
--
-- Container.lockByKey / Door.lockToKey (both already CONFIRMED live,
-- existing-world-test-matrix.md TEST J and the door-lock round-3 fix)
-- already accept an optional caller-supplied keyId and already know
-- how to spawn+give a matching key as a side effect of locking a
-- specific crate/door. What's missing is a STANDALONE path: handing
-- out a key that isn't tied to locking something in the same call
-- (e.g. seeding a key as loot before its target lock exists yet, or a
-- key given directly to a player). This module is that thin,
-- independently-usable wrapper around the exact same proven
-- `item:setKeyId(keyId)` mechanism -- it does not invent a new lock
-- API, it only decouples "make a key" from "lock a thing".
--
-- keyId is REQUIRED here, unlike lockByKey/lockToKey's convenience
-- default (they generate one via ZombRand if omitted). That default
-- exists for ad-hoc debug use; Action.CreateKey's entire purpose per
-- Jani's priority list is a backend-supplied identity that must
-- survive retries unchanged, so silently generating a new one on
-- retry would defeat the point.
--
-- NOT YET PROVEN LIVE. Debug-menu hook: server/TWR/Debug.lua
-- "controlled_key" mechanic gives a key directly to the triggering
-- admin. Update antagonist/DONE.md / antagonist/tests/ once actually
-- confirmed, not before.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Key = TWR.Mechanics.Key or {}

local Key = TWR.Mechanics.Key

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Builds one key item instance bound to keyId. Does not place/give it.
--
-- payload:
--   itemType     (optional string) -- defaults to Base.Key1.
--   displayName  (optional string).
--   contentId    (optional string) -- modData tag for future DB tracking.
function Key.build(keyId, payload)
    if keyId == nil then return nil, "KEYID_REQUIRED" end
    payload = payload or {}
    local itemType = payload.itemType or "Base.Key1"

    local okItem, item = pcall(function() return instanceItem(itemType) end)
    if not okItem or not item then
        return nil, "INSTANCE_FAILED"
    end

    safeCall(item, "setKeyId", keyId)

    if payload.displayName then
        safeCall(item, "setName", payload.displayName)
        safeCall(item, "setCustomName", true)
    end

    local okData, modData = safeCall(item, "getModData")
    if okData and modData then
        modData.TWR_keyId = keyId
        if payload.contentId then modData.TWR_contentId = payload.contentId end
    end

    return item
end

-- Gives a built key directly into a character's inventory. Uses the
-- confirmed sendAddItemToContainer() MP-sync fix (antagonist/DONE.md,
-- 2026-08-13), same as Container.lua's own key-giving paths.
function Key.giveTo(character, keyId, payload)
    local item, err = Key.build(keyId, payload)
    if not item then return nil, err end

    local okInv, inv = safeCall(character, "getInventory")
    if not okInv or not inv then return nil, "NO_INVENTORY" end

    local okAdd = safeCall(inv, "AddItem", item)
    if okAdd then
        pcall(function() sendAddItemToContainer(inv, item) end)
    end
    return item
end

-- PendingActions-compatible resolver. handlerModule = "Key",
-- actionType = "give_key". pending.params.keyId is required.
-- Ground-spawns at the target square rather than delivering to a
-- specific online player -- see antagonist/TODO.md, "delivering to a
-- specific/possibly-offline recipient" is still an open design
-- question shared with the rest of the pending-job system, not solved
-- here.
function Key.resolvePendingAction(pending)
    local params = pending.params or {}
    if params.keyId == nil then
        return false, "KEYID_REQUIRED", "Key.resolvePendingAction -- pending.params.keyId missing"
    end

    local okSq, square = pcall(function() return getCell():getGridSquare(pending.targetX, pending.targetY, pending.targetZ) end)
    if not okSq or not square then
        return false, "SQUARE_NOT_LOADED", "Key.resolvePendingAction -- target square not loaded"
    end

    local item, err = Key.build(params.keyId, params)
    if not item then
        return false, err or "BUILD_FAILED", "Key.build() failed"
    end

    safeCall(square, "AddWorldInventoryItem", item, 0.5, 0.5, 0)

    return true, {
        mechanic = "Key.resolvePendingAction",
        placed = 1,
        requested = 1,
        artifactType = "key",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "ground",
        targetSummary = "ground-spawned key (keyId=" .. tostring(params.keyId) .. ")",
    }
end
