-- TWR.Context.Flyer -- client-side hook that lets a TWR flyer's
-- optional location_ref use vanilla's own, completely unmodified
-- reveal-on-map button (client/PZAPI/ui/organisms/PrintMedia.lua),
-- instead of building any custom UI.
--
-- SOURCE-CONFIRMED (antagonist/tests/vanilla-flyer-source-trace.md):
-- PrintMedia's init() only wires up a reveal button when
-- PrintMediaDefinitions.MiscDetails[self.media_id] exists (real vanilla
-- table, shared/PrintMedia/PrintMediaDefinitions.lua, normally only
-- populated with vanilla's own hardcoded lore locations). There is no
-- Events.* hook that fires before that window is constructed, so the
-- only way to register a DB-authored TWR flyer's location there is to
-- wrap shared/TimedActions/ISReadABook.lua's own displayPrintMedia() --
-- the single call site that opens the window -- and populate the entry
-- from the flyer item's own (server-validated, see
-- server/TWR/Mechanics/Flyer.lua) TWR_locationRef modData right before
-- calling through to the real, unmodified original function. Vanilla's
-- own button code then runs completely untouched: same
-- WorldMapVisited.getInstance():setKnownInSquares() + sendClientCommand
-- call TWR.Mechanics.MapReveal already uses, same map-centering/zoom
-- behavior, same UI.
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

local function registerFlyerLocationIfNeeded(item)
    if not item or not item.hasModData or not item:hasModData() then return end
    local modData = item:getModData()
    local printMedia = modData.printMedia
    local locationRef = modData.TWR_locationRef
    -- DIAGNOSTIC 2026-08-18: live test reported wrong/random content on
    -- read -- logging exactly what the CLIENT sees in this item's
    -- modData at read-time, to tell apart a replication/sync issue
    -- from a server-side or getText()-rendering one. Remove once
    -- root-caused.
    print("TWR: Context.Flyer -- displayPrintMedia hook fired -- printMedia="
        .. tostring(printMedia and ("id=" .. tostring(printMedia.id) .. " title=" .. tostring(printMedia.title) .. " text=" .. tostring(printMedia.text)) or "nil")
        .. " locationRef=" .. tostring(locationRef and (locationRef.x1 .. "," .. locationRef.y1 .. "-" .. locationRef.x2 .. "," .. locationRef.y2) or "nil"))
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

local function init()
    if TWR.Context.flyerHookInstalled then return end -- guards against double-wrap on an F11 reload

    local originalDisplayPrintMedia = ISReadABook.displayPrintMedia
    ISReadABook.displayPrintMedia = function(self)
        local ok, err = pcall(registerFlyerLocationIfNeeded, self.item)
        if not ok then
            print("TWR: Context.Flyer -- registerFlyerLocationIfNeeded failed: " .. tostring(err))
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
