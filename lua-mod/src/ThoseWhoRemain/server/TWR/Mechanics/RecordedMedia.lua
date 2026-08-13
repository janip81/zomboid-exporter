-- TWR.Mechanics.RecordedMedia -- generic DB-suppliable "VHS tape"
-- readable/watchable content.
--
-- SOURCE-CONFIRMED against installed B42.20.2, and this is a
-- DELIBERATE ARCHITECTURE DEVIATION from vanilla's own RecMedia system
-- -- see the long comment below for why, this is not an oversight.
--
-- Vanilla's real VHS/CD/radio content system (shared/RecordedMedia/
-- recorded_media.lua, ~10.5k lines; shared/RecordedMedia/ISRecordedMedia.lua)
-- requires:
--   1. Registration via Events.OnInitRecordedMedia (`_rc:register(category,
--      uuid, itemDisplayName, spawning)` + `data:setExtra(...)` +
--      `data:addLine(text,...)` per line) -- this ONLY fires once at
--      server/game boot, there is no live "register a new one right now"
--      API found anywhere in the installed tree.
--   2. A translation-key round-trip (RM_<uuid> keys resolved from
--      shared/Translate/EN/Recorded_Media.json) for every string.
--   3. An item script with `MediaCategory = Home-VHS` (confirmed real
--      item: Base.VHS_Home) bound to one specific registered entry via
--      the native `item:setRecordedMediaData(mediaDataObject)`.
--   4. The vanilla "Watch/Read" context option (client/ISUI/
--      ISInventoryPaneContextMenu.lua:544) only appears when
--      `item:getMediaData()` is non-nil, i.e. only for items already
--      bound to a REGISTERED entry -- and it displays
--      `item:getMediaData():getTranslatedExtra()`, a single static
--      string, not the per-line `lines` table (those appear to be used
--      by the in-world radio/audio playback path, not this reader).
--
-- That whole chain is boot-time/static by construction -- exactly the
-- "new story idea = new Lua file / mod reload" trap Jani explicitly
-- wants avoided. So instead of fighting the native registry, this
-- module bypasses it entirely: it reuses the confirmed-generic
-- `ISMediaInfo.openPanel(playerNum, text)` UI (client/RecordedMedia/
-- ISMediaInfo.lua) directly -- that function is a thin, standalone
-- rich-text popup with NO dependency on the RecMedia registry, no
-- registration step, no translation keys. Content lives in the item's
-- own modData (server-settable at ANY time, not just boot), and a
-- small custom context-menu option (client/TWR/Context/RecordedMedia.lua)
-- opens it. This is the same "runtime modData, native UI, no new
-- custom window" pattern already proven by the Calendar item and the
-- Readable.lua module above.
--
-- Uses Base.VHS_Home by default (confirmed real vanilla item,
-- MediaCategory = Home-VHS, media/scripts/generated/items/normal.txt)
-- purely for its sprite/name -- we never touch its native
-- RecordedMediaData binding.
--
-- NOT YET PROVEN LIVE. Debug-menu hook: server/TWR/Debug.lua
-- "recorded_media" mechanic. In particular UNCONFIRMED: whether
-- ISMediaInfo.openPanel works correctly when called from a context
-- menu option we add ourselves (vs. its normal call site), and whether
-- the close-triggers-discovery wiring (Trigger.RecordedMediaViewed)
-- actually fires reliably. Update antagonist/DONE.md / antagonist/tests/
-- once actually confirmed, not before.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.RecordedMedia = TWR.Mechanics.RecordedMedia or {}

local RecordedMedia = TWR.Mechanics.RecordedMedia

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

local DEFAULT_ITEM_TYPE = "Base.VHS_Home"

-- Builds one VHS-tape item instance carrying the DB-suppliable payload
-- in modData. Does not place it anywhere.
--
-- payload:
--   contentId     (optional string) -- modData tag for future DB tracking.
--   mediaId       (optional string) -- modData tag, matches Jani's
--                 desired payload shape (distinct from contentId).
--   displayName   (optional string) -- item's in-world/inventory name.
--   lines         (optional array of strings) -- joined with newlines
--                 into the text ISMediaInfo.openPanel displays. This is
--                 OUR OWN simple text-join, not vanilla's colored
--                 `lines` table format (that format is tied to the
--                 registry path we're bypassing).
--   itemType      (optional string) -- defaults to Base.VHS_Home.
--   discoveryKey  (optional string) -- modData tag; used by the
--                 client-side "watched" report so the backend can tell
--                 pickup apart from actual playback/discovery.
function RecordedMedia.buildItem(payload)
    payload = payload or {}
    local itemType = payload.itemType or DEFAULT_ITEM_TYPE
    local okItem, item = pcall(function() return instanceItem(itemType) end)
    if not okItem or not item then
        return nil, "INSTANCE_FAILED"
    end

    if payload.displayName then
        safeCall(item, "setName", payload.displayName)
        safeCall(item, "setCustomName", true)
    end

    local text = table.concat(payload.lines or { "dummy test content" }, "\n")

    local okData, modData = safeCall(item, "getModData")
    if okData and modData then
        modData.TWR_isVHS = true
        modData.TWR_vhsText = text
        if payload.contentId then modData.TWR_contentId = payload.contentId end
        if payload.mediaId then modData.TWR_mediaId = payload.mediaId end
        if payload.discoveryKey then modData.TWR_discoveryKey = payload.discoveryKey end
    end

    return item
end

-- Spawns a VHS tape directly on the ground at (x,y,z).
function RecordedMedia.spawnOnGround(x, y, z, payload)
    local okSq, square = pcall(function() return getCell():getGridSquare(x, y, z) end)
    if not okSq or not square then
        return nil, "SQUARE_NOT_LOADED"
    end

    local item, err = RecordedMedia.buildItem(payload)
    if not item then
        return nil, err
    end

    safeCall(square, "AddWorldInventoryItem", item, 0.5, 0.5, 0)
    return item
end

-- PendingActions-compatible resolver. handlerModule = "RecordedMedia",
-- actionType = "spawn_vhs".
function RecordedMedia.resolvePendingAction(pending)
    local params = pending.params or {}
    local item, err = RecordedMedia.spawnOnGround(pending.targetX, pending.targetY, pending.targetZ, params)
    if not item then
        return false, err or "SPAWN_FAILED", "RecordedMedia.spawnOnGround() failed"
    end

    return true, {
        mechanic = "RecordedMedia.spawnOnGround",
        placed = 1,
        requested = 1,
        artifactType = "recorded_media",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "ground",
        targetSummary = "ground-spawned VHS tape (" .. tostring(params.itemType or DEFAULT_ITEM_TYPE) .. ")",
    }
end

-- Trigger.RecordedMediaViewed ("recorded_media_viewed", renamed from
-- the original MediaPlaybackCompleted per CGPT-016 -- this reports the client
-- closing the ISMediaInfo panel for an inventory item, NOT real
-- vanilla TV/VCR playback: no television, no inserted tape, no
-- electricity, no elapsed duration are involved. That's an honest,
-- useful DB-fed "viewed this clue" signal on its own; it just isn't
-- equivalent to vanilla media playback, and the name must not imply
-- otherwise. Whether TWR VHS clues should ALSO require a real
-- powered-TV interaction is Jani's call, not decided here --
-- see antagonist/tests/priority-mechanics-p1-p4-chatgpt-review.md.
--
-- CGPT-017 fix: the server previously trusted args.contentId/
-- discoveryKey verbatim from the client -- any client could have
-- claimed an arbitrary discovery by hand-crafting the command. Now
-- the client only sends the ITEM'S ID (see
-- client/TWR/Context/RecordedMedia.lua); the server looks that item up
-- in the player's OWN inventory and reads contentId/mediaId/
-- discoveryKey from the SERVER's own view of that item's modData.
-- args.contentId/discoveryKey are still accepted for debug-log
-- comparison only and are never trusted for the emitted signal.
-- FIX 2026-08-13, found live: the first cut of this only checked
-- player:getInventory(), which REJECTED a completely legitimate
-- "Watch Tape" click on a tape still sitting inside a just-opened
-- nearby crate (never picked up) -- the KVLS fixture test itself hit
-- this. Widened to also search containers on nearby squares (radius
-- 2, same proximity-scan pattern as Container.findExistingContainer),
-- not just the player's carried inventory. Still fully
-- server-authoritative: this only ever reads modData off an item the
-- server itself found in the world/inventory near the reporting
-- player, never trusts anything the client claims about identity.
local SEARCH_RADIUS = 2

local function findNearbyItemById(player, itemID)
    local okInv, inv = safeCall(player, "getInventory")
    if okInv and inv then
        local okItem, item = safeCall(inv, "getItemById", itemID)
        if okItem and item then return item end
    end

    local okX, x = safeCall(player, "getX")
    local okY, y = safeCall(player, "getY")
    local okZ, z = safeCall(player, "getZ")
    if not (okX and okY and okZ) then return nil end

    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then return nil end

    for dx = -SEARCH_RADIUS, SEARCH_RADIUS do
        for dy = -SEARCH_RADIUS, SEARCH_RADIUS do
            local okSq, square = pcall(function() return cell:getGridSquare(math.floor(x) + dx, math.floor(y) + dy, math.floor(z)) end)
            if okSq and square then
                local okObjs, objects = safeCall(square, "getObjects")
                if okObjs and objects then
                    for i = 0, objects:size() - 1 do
                        local okC, container = safeCall(objects:get(i), "getContainer")
                        if okC and container then
                            local okItem, item = safeCall(container, "getItemById", itemID)
                            if okItem and item then return item end
                        end
                    end
                end
            end
        end
    end

    return nil
end

local function onMediaPlayed(module, command, player, args)
    if module ~= "twr_media" or command ~= "played" then return end

    local itemID = args and args.itemID
    local okName, username = safeCall(player, "getUsername")

    local item = itemID ~= nil and findNearbyItemById(player, itemID) or nil

    local okData, modData = false, nil
    if item then
        okData, modData = safeCall(item, "getModData")
    end

    if not (okData and modData and modData.TWR_isVHS) then
        print("TWR.Mechanics.RecordedMedia: onMediaPlayed -- REJECTED, no matching TWR VHS item in player's inventory or nearby containers (player="
            .. (okName and username or "?") .. " itemID=" .. tostring(itemID)
            .. " claimed contentId=" .. tostring(args and args.contentId) .. ") -- possible stale close or forged command, not trusted")
        return
    end

    -- Authoritative identity: from the server's own item, not from args.
    local discoveryKey = modData.TWR_discoveryKey
    local contentId = modData.TWR_contentId

    print("TWR.Mechanics.RecordedMedia: onMediaPlayed -- player=" .. (okName and username or "?")
        .. " contentId=" .. tostring(contentId) .. " discoveryKey=" .. tostring(discoveryKey))

    -- TEMPORARY DIAGNOSTIC PLUMBING (CGPT-018): reusing TWR.Emit.jobResult
    -- here conflates job execution / physical artifact / player-discovery
    -- signal into one record with a throwaway random jobId. This is
    -- intentional short-term wiring, not the durable schema -- route
    -- through a real twr_signals/twr_discoveries path once the quest
    -- engine has one (antagonist/quest-db/), and stop emitting into
    -- twr_world_artifacts' artifactKey for this at that point.
    if TWR.Emit and TWR.Emit.jobResult then
        TWR.Emit.jobResult({
            jobId = "media-played-" .. tostring(discoveryKey) .. "-" .. tostring(ZombRand(1000000000)),
            attemptNo = 1,
            actionType = "recorded_media_viewed",
            mechanic = "RecordedMedia.onMediaPlayed",
            result = "applied",
            placed = 1,
            requested = 1,
            artifactKey = discoveryKey,
            artifactType = "recorded_media_discovery",
            targetType = "player",
            targetSummary = "player=" .. tostring(okName and username or "?") .. " contentId=" .. tostring(contentId),
        })
    end
end

-- FIX 2026-08-13: check TWR.Runtime exists first instead of calling
-- into it and relying on pcall() to catch the resulting Java
-- exception -- see server/TWR/Debug.lua's identical fix for the full
-- reasoning (this file has the exact same alphabetical-load-order gap:
-- Mechanics/ sorts before Runtime.lua within server/TWR/).
local function init()
    if not (TWR.Runtime and TWR.Runtime.registerEventOnce) then
        return false
    end
    TWR.Runtime.registerEventOnce(RecordedMedia, "mediaPlayed", Events.OnClientCommand, onMediaPlayed)
    print("TWR.Mechanics.RecordedMedia: OnClientCommand handler registered")
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
    print("TWR.Mechanics.RecordedMedia: init deferred, retrying every minute (TWR.Runtime not loaded yet)")
    Events.EveryOneMinute.Add(retryInit)
end
