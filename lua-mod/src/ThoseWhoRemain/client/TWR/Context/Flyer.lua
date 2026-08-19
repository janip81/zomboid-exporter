-- TWR.Context.Flyer -- client-side hook wrapping
-- shared/TimedActions/ISReadABook.lua's displayPrintMedia() (the single
-- call site that opens the vanilla read window), for two purposes:
--
-- 1. CONTENT RE-ASSERTION -- ROOT-CAUSED 2026-08-18. Live test showed a
--    TWR dummy flyer displaying real vanilla ambient flyer content
--    ("Lowry Court", a real PrintMediaDefinitions.MiscDetails entry)
--    instead of the authored dummy text, confirmed via the Discord
--    read-feed announcement. Something native re-assigns real flyer
--    content at READ time (not just at instanceItem() time), clobbering
--    whatever server/TWR/Mechanics/Flyer.lua wrote to modData.printMedia
--    before the window opens. Fix: buildItem() also stashes the
--    authored title/text into TWR_flyerContent, a field name no native
--    code has any reason to touch; this hook force-writes
--    modData.printMedia.title/.text from TWR_flyerContent immediately
--    before calling the real, unmodified displayPrintMedia(), so our
--    content always wins regardless of when/how the native
--    reassignment happens.
--
-- 2. LOCATION-REVEAL WIRING -- lets a TWR flyer's optional location_ref
--    use vanilla's own, completely unmodified reveal-on-map button
--    (client/PZAPI/ui/organisms/PrintMedia.lua) instead of building any
--    custom UI. SOURCE-CONFIRMED (antagonist/tests/
--    vanilla-flyer-source-trace.md): PrintMedia's init() only wires up
--    a reveal button when PrintMediaDefinitions.MiscDetails[self.media_id]
--    exists (real vanilla table, shared/PrintMedia/
--    PrintMediaDefinitions.lua, normally only populated with vanilla's
--    own hardcoded lore locations) -- there is no Events.* hook that
--    fires before that window is constructed, so this same
--    displayPrintMedia() wrap is also the only place to populate that
--    entry from the flyer item's own (server-validated) TWR_locationRef
--    modData. Vanilla's own button code then runs completely untouched:
--    same WorldMapVisited.getInstance():setKnownInSquares() +
--    sendClientCommand call TWR.Mechanics.MapReveal already uses, same
--    map-centering/zoom behavior, same UI.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
if not isClient() then return end

TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

local MAX_DIMENSION = 500

-- Re-validated here too, even though server/TWR/Mechanics/Flyer.lua
-- already validated before ever writing TWR_locationRef -- matches
-- Context/MapReveal.lua's own "defense in depth, never trust a network
-- payload blindly" stance for anything that arrived via item
-- replication rather than being authored in this file.
local function validateRect(loc)
    if not loc then return nil end
    local x1, y1, x2, y2 = tonumber(loc.x1), tonumber(loc.y1), tonumber(loc.x2), tonumber(loc.y2)
    if not (x1 and y1 and x2 and y2) then return nil end
    if x2 < x1 or y2 < y1 then return nil end
    if (x2 - x1) > MAX_DIMENSION or (y2 - y1) > MAX_DIMENSION then return nil end
    return x1, y1, x2, y2
end

-- Overwrites modData.printMedia.title/.text from TWR_flyerContent if
-- present, so whatever native content-reassignment happens between
-- buildItem() and this read is discarded in favor of our authored
-- content. Idempotent -- safe to call on every read.
local function reassertFlyerContent(modData)
    local flyerContent = modData.TWR_flyerContent
    if not flyerContent then return end
    modData.printMedia = modData.printMedia or {}
    modData.printMedia.id = modData.printMedia.id or ("twr_fallback_" .. tostring(ZombRand and ZombRand(1000000) or 0))
    modData.printMedia.title = flyerContent.title
    modData.printMedia.text = flyerContent.text
end

local function registerFlyerLocationIfNeeded(modData)
    local printMedia = modData.printMedia
    local locationRef = modData.TWR_locationRef
    if not printMedia or not printMedia.id or not locationRef then return end
    if PrintMediaDefinitions.MiscDetails[printMedia.id] then return end -- already registered (idempotent, matches re-reads)

    local x1, y1, x2, y2 = validateRect(locationRef)
    if not x1 then
        print("TWR: Context.Flyer -- TWR_locationRef failed client-side re-validation, no reveal button for " .. tostring(printMedia.id))
        return
    end

    PrintMediaDefinitions.MiscDetails[printMedia.id] = {
        location1 = { { x1 = x1, y1 = y1, x2 = x2, y2 = y2 } },
    }
end

local function onDisplayPrintMedia(item)
    if not item or not item.hasModData or not item:hasModData() then return end
    local modData = item:getModData()
    reassertFlyerContent(modData)
    registerFlyerLocationIfNeeded(modData)
end

local function init()
    if TWR.Context.flyerHookInstalled then return end -- guards against double-wrap on an F11 reload

    local originalDisplayPrintMedia = ISReadABook.displayPrintMedia
    ISReadABook.displayPrintMedia = function(self)
        local ok, err = pcall(onDisplayPrintMedia, self.item)
        if not ok then
            print("TWR: Context.Flyer -- onDisplayPrintMedia failed: " .. tostring(err))
        end
        return originalDisplayPrintMedia(self)
    end

    TWR.Context.flyerHookInstalled = true
end

-- Self-initialize: immediate attempt handles F11 reload, OnGameStart
-- fallback handles the one-time first-boot ordering race (same pattern
-- this mod's other Context files use).
local ok, err = pcall(init)
if not ok then
    print("TWR: Context.Flyer init deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(init)
