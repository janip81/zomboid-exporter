-- TWR.Mechanics.Flyer -- generic DB-suppliable vanilla-parity flyer
-- content (ambient/lore clue that looks and behaves exactly like an
-- ordinary Junk-category flyer, per antagonist/design/
-- item-presentation-parity.md).
--
-- SOURCE-CONFIRMED against installed B42.20.3 (buildid 24775771), see
-- antagonist/tests/vanilla-flyer-source-trace.md for the full trace --
-- not invented:
--   * real carrier: Base.Flier (DisplayCategory=Junk, ItemType=
--     base:literature, ReadType=photo, Tags=base:fastread;base:picture,
--     no CanBeWrite -- this is NOT the Readable/Note profile).
--   * a bare Base.Flier shows no readable content at all when read --
--     the literal "Inspect" context option and the actual PrintMedia
--     content window both require item:getModData().printMedia to be
--     set (client/ISUI/ISInventoryPaneContextMenu.lua's doPrintMediaMenu
--     vs doLiteratureMenu split; shared/TimedActions/ISReadABook.lua:
--     perform()/displayPrintMedia()).
--   * printMedia shape consumed by displayPrintMedia(): { id, info,
--     title, text } -- id is only a lookup key for the map-reveal
--     button (client/PZAPI/ui/organisms/PrintMedia.lua), title/text are
--     passed through getText() (untranslated keys render as-is per
--     established B42 convention) then shown verbatim.
--
-- Deliberately does NOT set CanBeWrite/setLockedBy (that's
-- TWR.Mechanics.Readable's Note profile) -- printMedia-driven items
-- have no write/edit path in vanilla at all, so this is read-only by
-- construction, matching Recipe.lua/Readable.lua's read-only-by-design
-- pattern without needing a sentinel lock.
--
-- location_ref map-reveal action: resolved server-side to a concrete
-- rectangle and stashed in modData for the client-side hook
-- (client/TWR/Context/Flyer.lua) to register with vanilla's own
-- PrintMediaDefinitions.MiscDetails table and reuse vanilla's own
-- unmodified reveal-button code path -- see that file's header.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Flyer = TWR.Mechanics.Flyer or {}

local Flyer = TWR.Mechanics.Flyer

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

local ITEM_TYPE = "Base.Flier"

-- Mirrors client/TWR/Context/MapReveal.lua's own MAX_DIMENSION bound --
-- validated here too since this is the actual trust boundary
-- (DB-authored payload), matching that file's "defense in depth, never
-- trust ... blindly" stance rather than assuming a single check
-- anywhere in the chain is enough.
local MAX_LOCATION_DIMENSION = 500

local function validateLocationRef(loc)
    if not loc then return nil end
    local x1, y1, x2, y2 = tonumber(loc.x1), tonumber(loc.y1), tonumber(loc.x2), tonumber(loc.y2)
    if not (x1 and y1 and x2 and y2) then return nil, "non-numeric coordinate" end
    if x2 < x1 or y2 < y1 then return nil, "x2<x1 or y2<y1" end
    if (x2 - x1) > MAX_LOCATION_DIMENSION or (y2 - y1) > MAX_LOCATION_DIMENSION then
        return nil, "rectangle exceeds MAX_LOCATION_DIMENSION=" .. MAX_LOCATION_DIMENSION
    end
    return { x1 = x1, y1 = y1, x2 = x2, y2 = y2 }
end

-- Builds one instanceItem() with the DB-suppliable payload applied.
-- Does not place it anywhere -- shared by ground-spawn and (future)
-- container-spawn callers, matching Readable.buildItem's shape.
--
-- payload (matches the Readable/Note payload shape for consistency):
--   contentId     (optional string) -- stored in modData, not used by
--                 this module directly.
--   displayName   (optional string) -- printMedia.title. Defaults to
--                 the vanilla "Flier" name if omitted.
--   text          (optional string) -- printMedia.text, the body shown
--                 in the PrintMedia window. Defaults to "".
--   discoveryKey  (optional string) -- stored in modData, same as
--                 contentId; kept separate per the established payload
--                 shape.
--   locationRef   (optional table {x1,y1,x2,y2}) -- already-resolved
--                 world rectangle for the reveal-on-map button. Callers
--                 are responsible for resolving a location_ref to
--                 coordinates before calling buildItem -- this module
--                 does not do location-ref resolution itself.
function Flyer.buildItem(payload)
    payload = payload or {}
    local okItem, item = pcall(function() return instanceItem(ITEM_TYPE) end)
    if not okItem or not item then
        return nil, "INSTANCE_FAILED"
    end

    local okData, modData = safeCall(item, "getModData")
    if not okData or not modData then
        return nil, "MODDATA_FAILED"
    end

    -- media_id only needs to be unique enough to not collide with
    -- vanilla's own PrintMediaDefinitions.MiscDetails keys or other TWR
    -- flyers -- prefixed and derived from contentId/discoveryKey when
    -- available, falling back to a per-item unique id otherwise.
    local mediaId = "twr_" .. tostring(payload.contentId or payload.discoveryKey or item:getID())

    -- ROOT-CAUSED 2026-08-18: live test confirmed via the Discord read-
    -- feed announcement ("read Flier: Lowry Court") that the item ends
    -- up showing one of VANILLA's own real, pre-authored ambient flyer
    -- entries (PrintMediaDefinitions.MiscDetails.lowrycourt) instead of
    -- this module's dummy content -- something native re-assigns real
    -- flyer content at READ time (not just at instanceItem() time),
    -- clobbering whatever this function writes to modData.printMedia
    -- before the player ever gets to Inspect it. Writing the authored
    -- content into a second, TWR-only-namespaced field
    -- (TWR_flyerContent) that no native code has any reason to touch,
    -- separate from modData.printMedia -- the client-side hook
    -- (client/TWR/Context/Flyer.lua) re-asserts modData.printMedia from
    -- this field immediately before the vanilla read window opens,
    -- guaranteeing our content wins regardless of when/how the native
    -- reassignment happens. modData.printMedia is still set here too --
    -- still required at context-menu-build time (before any read
    -- action starts) for doPrintMediaMenu's tests.isPrintMedia gate to
    -- produce the literal "Inspect" option in the first place.
    modData.printMedia = {
        id = mediaId,
        title = payload.displayName or "Flyer",
        text = payload.text or "",
    }
    modData.TWR_flyerContent = {
        title = payload.displayName or "Flyer",
        text = payload.text or "",
    }

    if payload.contentId then modData.TWR_contentId = payload.contentId end
    if payload.discoveryKey then modData.TWR_discoveryKey = payload.discoveryKey end

    if payload.locationRef then
        local validated, verr = validateLocationRef(payload.locationRef)
        if validated then
            modData.TWR_locationRef = validated
        else
            print("TWR.Mechanics.Flyer: buildItem -- locationRef REJECTED: " .. tostring(verr))
        end
    end

    -- No explicit modData-sync call here, matching Readable.buildItem's
    -- precedent -- the item hasn't been placed anywhere yet at this
    -- point, so there is nothing to sync to; world placement
    -- (AddWorldInventoryItem, below) is what actually transmits the
    -- item (and its modData) to clients. syncItemFields() is only the
    -- confirmed-real call for mutating modData on an item that already
    -- exists in a container/inventory (see TWR.UI.Calendar's addMark()).

    return item
end

-- Spawns a flyer item directly on the ground at (x,y,z). Returns the
-- item, or nil + an error code string.
function Flyer.spawnOnGround(x, y, z, payload)
    local okSq, square = pcall(function() return getCell():getGridSquare(x, y, z) end)
    if not okSq or not square then
        return nil, "SQUARE_NOT_LOADED"
    end

    local item, err = Flyer.buildItem(payload)
    if not item then
        return nil, err
    end

    safeCall(square, "AddWorldInventoryItem", item, 0.5, 0.5, 0)
    return item
end

-- PendingActions-compatible resolver. handlerModule = "Flyer",
-- actionType = "spawn_flyer". pending.params carries the payload
-- described above.
function Flyer.resolvePendingAction(pending)
    local params = pending.params or {}
    local item, err = Flyer.spawnOnGround(pending.targetX, pending.targetY, pending.targetZ, params)
    if not item then
        return false, err or "SPAWN_FAILED", "Flyer.spawnOnGround() failed"
    end

    return true, {
        mechanic = "Flyer.spawnOnGround",
        placed = 1,
        requested = 1,
        artifactType = "flyer",
        x = pending.targetX,
        y = pending.targetY,
        z = pending.targetZ,
        targetType = "ground",
        targetSummary = "ground-spawned flyer (" .. ITEM_TYPE .. ")",
    }
end
