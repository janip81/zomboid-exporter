-- TWR.RecordedMediaRegistry -- registers ONE generic "carrier" VHS
-- MediaData entry at boot, so TWR-authored tape items can be bound to
-- a real, native `RecordedMediaData` and accepted by vanilla's actual
-- TV/VCR insert UI (RWMMedia.lua's own `verifyItem` gate requires
-- `item:isRecordedMedia() and item:getMediaType()==deviceData:getMediaType()`
-- -- CONFIRMED live 2026-08-13 that simply using a `Base.VHS_Home` item
-- type is NOT enough on its own; the item must actually be bound to a
-- registered MediaData via `item:setRecordedMediaData(...)`).
--
-- This is the "carrier registration" approach proposed in
-- antagonist/tests/vhs-device-research.md (Q4/Q5) -- register ONE
-- fixed, generic placeholder MediaData at boot (satisfies the native
-- type-check gate), bind EVERY TWR tape to the SAME carrier object,
-- and keep each tape's real DB-fed identity/content in the physical
-- item's own modData (TWR_contentId/TWR_vhsText/etc, already how
-- server/TWR/Mechanics/RecordedMedia.lua works) -- NOT in the shared
-- carrier's static title/lines. The carrier's own text is a generic
-- placeholder never meant to be read by a player; the real content
-- must be displayed by a separate TWR-owned UI hook once we can detect
-- "this specific TWR item is the one currently in this TV and
-- playing" (not yet built -- see file header note below).
--
-- Must run on BOTH client and server (Events.OnInitRecordedMedia is a
-- shared-side event, same as vanilla's own shared/RecordedMedia/
-- ISRecordedMedia.lua -- every connected client needs the identical
-- registered catalog, not just the server).
--
-- UNCONFIRMED LIVE, first pass: exact `register()` parameter meanings
-- for `spawning` (0-2, guessed 0 = "never spawns naturally in vanilla
-- loot") and whether a literal (non translation-key) string for
-- title/itemDisplayName renders as-is or as raw "unknown text" --
-- vanilla's own ISRecordedMedia.lua always passes RM_<uuid>
-- translation keys, we deliberately don't have a translation file for
-- this, so this is the first real test of that fallback behavior.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.RecordedMediaRegistry = TWR.RecordedMediaRegistry or {}

-- Fixed, unique-enough id -- vanilla's own entries use UUIDs but
-- nothing found in source requires that exact format, just uniqueness
-- within the registry.
TWR.RecordedMediaRegistry.CARRIER_ID = "TWR_CARRIER_VHS_001"

local function initRegistry(_rc)
    local okReg, data = pcall(function()
        return _rc:register("Home-VHS", TWR.RecordedMediaRegistry.CARRIER_ID, "TWR Tape", 0)
    end)
    if not okReg or not data then
        print("TWR.RecordedMediaRegistry: register() FAILED: " .. tostring(data))
        return
    end

    pcall(function() data:setTitle("TWR Recorded Media") end)
    pcall(function() data:setSubtitle("(content supplied by ThoseWhoRemain)") end)

    TWR.RecordedMediaRegistry.carrierMediaData = data
    print("TWR.RecordedMediaRegistry: carrier VHS MediaData registered, id=" .. TWR.RecordedMediaRegistry.CARRIER_ID)
end

Events.OnInitRecordedMedia.Add(initRegistry)
