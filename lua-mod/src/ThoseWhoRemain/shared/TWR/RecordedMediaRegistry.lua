-- TWR.RecordedMediaRegistry -- registers ONE MediaData entry PER
-- DISTINCT AUTHORED CONTENT at boot, so TWR tape items can be bound to
-- a real, native `RecordedMediaData` (required for vanilla's TV insert
-- UI to accept them -- CONFIRMED live 2026-08-13, RWMMedia.lua's own
-- `verifyItem` gate rejects a plain Base.VHS_Home with no binding) AND
-- so vanilla's own native line/caption playback can show DB-fed text
-- with zero TWR-owned UI.
--
-- ARCHITECTURE, per CGPT-020 (antagonist/tests/
-- vhs-live-handoff-chatgpt-response.md) after a live dead end trying
-- to read the physical inserted item back out of a device (confirmed
-- live: no getMediaItem/getInsertedItem/getItem/getCurrentItem/getTape,
-- and deviceData:getParent():getContainer() is nil -- this is normal
-- vanilla design, not a missing API): DeviceData reduces an inserted
-- tape to `mediaType + mediaIndex`; `deviceData:getMediaData()` IS the
-- authoritative "what's playing" identity while inserted. That only
-- works if identity granularity is ONE MediaData PER DISTINCT CONTENT
-- (not one shared carrier for everything -- rejected, cannot carry
-- different caption lines for different recordings; not one per
-- physical item either -- unnecessary and wasteful). Multiple physical
-- copies of the same authored content simply bind to the same
-- MediaData, same as several copies of a real commercial tape.
--
-- Must run on BOTH client and server (Events.OnInitRecordedMedia is a
-- shared-side event, same as vanilla's own shared/RecordedMedia/
-- ISRecordedMedia.lua -- every connected client needs the identical
-- registered catalog, not just the server).
--
-- This is boot-time-fixed content (a hardcoded Lua table below), same
-- limitation as vanilla's own registry -- new content still needs a
-- mod update + restart to register, no live/hot registration attempted
-- yet (see VHS-SYNC-3 in the chatgpt-response doc -- deliberately not a
-- production requirement, a normal maintenance restart is acceptable).
-- The real DB-driven bridge (Postgres content -> this table, generated
-- at build time) is future work, not built here -- this table is
-- TEST/DUMMY content only, matching every other P1-P4 debug fixture.
--
-- UNCONFIRMED LIVE: whether literal (non translation-key) addLine()
-- text renders as-is or as raw/missing-translation garbage -- vanilla's
-- own registrations always use RM_<uuid> translation keys, we
-- deliberately don't have a translation file, so DUMMY_CONTENT below's
-- VHS-LINES-1 entry is the first real test of that.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.RecordedMediaRegistry = TWR.RecordedMediaRegistry or {}

-- contentId -> { title, subtitle, lines={...} }. contentId doubles as
-- the registry id (prefixed) -- keep it stable across restarts once
-- real content exists; do not regenerate ids per boot.
local DUMMY_CONTENT = {
    ["dummy.vhs.001"] = {
        title = "Home Video",
        lines = { "Dummy line one.", "Dummy line two." },
    },
    ["fixture.media.vhs.alpha"] = {
        title = "Test VHS Alpha",
        lines = {
            "TEST RECORDING",
            "Proceed to the designated test location.",
            "Sleep there to continue.",
            "END TEST RECORDING",
        },
    },
    -- VHS-LINES-1 decisive probe (antagonist/tests/
    -- vhs-live-handoff-chatgpt-response.md): insert+play through ONLY
    -- the real vanilla TV UI, no ISMediaInfo/TWR.Context.watchTape/any
    -- TWR overlay involved -- PASS means these two lines appear as
    -- native in-world scrolling captions, exactly like a real vanilla
    -- tape. If this passes, the native Tier-3 content mechanism is
    -- solved and the whole per-item modData display approach can be
    -- dropped in favor of this registry alone.
    ["twr.native.lines.test.001"] = {
        title = "TWR Native Lines Test",
        lines = { "TWR NATIVE LINE ONE", "TWR NATIVE LINE TWO" },
    },
}

TWR.RecordedMediaRegistry.registry = TWR.RecordedMediaRegistry.registry or {}

local function initRegistry(_rc)
    for contentId, content in pairs(DUMMY_CONTENT) do
        local registryId = "TWR_" .. contentId
        local okReg, data = pcall(function()
            return _rc:register("Home-VHS", registryId, content.title, 0)
        end)
        if okReg and data then
            pcall(function() data:setTitle(content.title) end)
            for _, line in ipairs(content.lines) do
                pcall(function() data:addLine(line, 1, 1, 1, nil) end)
            end
            TWR.RecordedMediaRegistry.registry[contentId] = data
            print("TWR.RecordedMediaRegistry: registered contentId=" .. contentId .. " registryId=" .. registryId .. " lines=" .. #content.lines)
        else
            print("TWR.RecordedMediaRegistry: register() FAILED for contentId=" .. contentId .. ": " .. tostring(data))
        end
    end
end

Events.OnInitRecordedMedia.Add(initRegistry)
