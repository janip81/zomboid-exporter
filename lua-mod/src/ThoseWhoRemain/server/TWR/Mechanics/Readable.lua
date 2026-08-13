-- TWR.Mechanics.Readable -- generic DB-suppliable readable content
-- (paper/note/flyer/pamphlet/recipe-instructions/generic document).
--
-- SOURCE-CONFIRMED against installed B42.20.2 (not invented):
-- client/ISUI/ISInventoryPaneContextMenu.lua:565-575 -- any item with
-- CanBeWrite=true gets a "Read Note"/"Write Note" context option via
-- `tests.canBeWrite`. Whether it's editable depends on the player
-- carrying a pen/pencil tag AND `tests.canBeWrite:getLockedBy()` being
-- either unset or equal to the player's own username. Both branches
-- reuse the SAME native item + the SAME ISUIWriteJournal UI
-- (client/ISUI/ISUIWriteJournal.lua) -- there is no separate "read-only
-- flyer" widget to build. Locking the item to a sentinel username that
-- no real player will ever have makes it permanently read-only to
-- everyone, which is exactly the "DB-authored, not player-editable"
-- requirement.
-- media/scripts/generated/items/literature.txt confirms CanBeWrite=true
-- on Base.SheetPaper2, Base.IndexCard, Base.Notepad, Base.Journal,
-- Base.Notebook, Base.GraphPaper (used SheetPaper2 as the generic
-- default -- a blank sheet is the least presumptive "paper/note/flyer"
-- carrier; callers can pass a different itemType).
-- Native item methods (addPage, setName, setCustomName, setLockedBy)
-- confirmed via ISInventoryPaneContextMenu.lua's own onWriteSomethingClick
-- handler, not guessed.
--
-- NOT YET PROVEN LIVE -- this is the first-pass implementation per
-- Jani's priority list; a debug-menu hook exists (server/TWR/Debug.lua
-- "readable" mechanic) so this can be tested end to end. Update
-- antagonist/DONE.md / antagonist/tests/ once actually confirmed live,
-- not before.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Readable = TWR.Mechanics.Readable or {}

local Readable = TWR.Mechanics.Readable

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

local DEFAULT_ITEM_TYPE = "Base.SheetPaper2"

-- Non-player username used to permanently lock DB-authored content
-- read-only. Any real Steam-connected player has a real username, so
-- this can never collide.
local LOCK_SENTINEL = "TWR_SYSTEM"

-- Builds one instanceItem() with the DB-suppliable payload applied.
-- Does not place it anywhere -- shared by ground-spawn and (future)
-- container-spawn callers.
--
-- payload (matches Jani's desired conceptual DB shape):
--   contentId     (optional string) -- stored in modData for future
--                 discovery-key/DB tracking, not used by this module.
--   displayName   (optional string) -- item's in-world/inventory name.
--   text          (optional string) -- page 1 content. Defaults to "".
--   itemType      (optional string) -- defaults to Base.SheetPaper2.
--   discoveryKey  (optional string) -- stored in modData, same as
--                 contentId; kept separate per Jani's payload shape.
function Readable.buildItem(payload)
    payload = payload or {}
    local itemType = payload.itemType or DEFAULT_ITEM_TYPE
    local okItem, item = pcall(function() return instanceItem(itemType) end)
    if not okItem or not item then
        return nil, "INSTANCE_FAILED"
    end

    safeCall(item, "addPage", 1, payload.text or "")

    if payload.displayName then
        safeCall(item, "setName", payload.displayName)
        safeCall(item, "setCustomName", true)
    end

    -- Read-only distribution -- see file header.
    safeCall(item, "setLockedBy", LOCK_SENTINEL)

    local okData, modData = safeCall(item, "getModData")
    if okData and modData then
        if payload.contentId then modData.TWR_contentId = payload.contentId end
        if payload.discoveryKey then modData.TWR_discoveryKey = payload.discoveryKey end
    end

    return item
end

-- Spawns a readable item directly on the ground at (x,y,z). Returns
-- the item, or nil + an error code string.
function Readable.spawnOnGround(x, y, z, payload)
    local okSq, square = pcall(function() return getCell():getGridSquare(x, y, z) end)
    if not okSq or not square then
        return nil, "SQUARE_NOT_LOADED"
    end

    local item, err = Readable.buildItem(payload)
    if not item then
        return nil, err
    end

    safeCall(square, "AddWorldInventoryItem", item, 0.5, 0.5, 0)
    return item
end

-- PendingActions-compatible resolver. handlerModule = "Readable",
-- actionType = "spawn_readable". pending.params carries the payload
-- described above.
function Readable.resolvePendingAction(pending)
    local params = pending.params or {}
    local item, err = Readable.spawnOnGround(pending.targetX, pending.targetY, pending.targetZ, params)
    if not item then
        return false, err or "SPAWN_FAILED", "Readable.spawnOnGround() failed"
    end

    return true, {
        mechanic = "Readable.spawnOnGround",
        placed = 1,
        requested = 1,
        artifactType = "readable",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "ground",
        targetSummary = "ground-spawned readable item (" .. tostring(params.itemType or DEFAULT_ITEM_TYPE) .. ")",
    }
end
