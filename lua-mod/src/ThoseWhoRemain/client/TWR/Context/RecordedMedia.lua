-- TWR.Context.RecordedMedia -- displays DB-fed VHS content via the
-- native ISMediaInfo popup (client/RecordedMedia/ISMediaInfo.lua --
-- confirmed a standalone generic rich-text window, no RecMedia
-- registry dependency).
--
-- REMOVED 2026-08-13 per Jani's explicit direction: the direct
-- right-click "Watch Tape" inventory shortcut. A real VHS tape must
-- require a real TV/VCR to watch, exactly like vanilla -- right-
-- clicking a tape in your own inventory and reading it like a book is
-- not acceptable, even as an interim mechanic. TWR.Context.watchTape()
-- below is kept as the reusable display+report helper (still correct
-- and still needed) -- it is simply not wired to any player-facing
-- trigger right now. The real trigger will be whatever hook fires once
-- a real IsoTelevision accepts and plays a TWR-flagged tape -- see
-- antagonist/tests/vhs-device-research.md for the researched device
-- chain (IsoTelevision + DeviceData + ISDeviceMediaAction) this needs
-- to hook into, not yet built.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

-- Kept for reuse once a real TV-driven trigger calls it. NOT currently
-- called from anywhere -- see file header.
function TWR.Context.watchTape(playerNum, item)
    local okData, modData = pcall(function() return item:getModData() end)
    if not okData or not modData or not modData.TWR_vhsText then return end

    local panel = ISMediaInfo.openPanel(playerNum, modData.TWR_vhsText)
    if not panel then return end

    -- ISMediaInfo is a CLASS-LEVEL SINGLETON (confirmed via its own
    -- source: openPanel() reuses ISMediaInfo.instance and just swaps
    -- the text if one is already open, rather than making a new
    -- window). That means if a player opens a second TWR tape without
    -- closing the first, the SAME panel instance gets reused -- so the
    -- reported contentId/discoveryKey must be refreshed on every call,
    -- not just captured once when destroy() is first wrapped, or a
    -- second tape's close would incorrectly report the first tape's
    -- identity. Store current identity ON the panel and read it back
    -- inside destroy(); only wrap destroy() itself once.
    panel._twrCurrentModData = modData
    panel._twrCurrentPlayerNum = playerNum
    local okID, itemID = pcall(function() return item:getID() end)
    panel._twrCurrentItemID = okID and itemID or nil

    if not panel._twrWrapped then
        panel._twrWrapped = true
        local originalDestroy = panel.destroy
        panel.destroy = function(self)
            local reportData = self._twrCurrentModData
            local reportPlayerNum = self._twrCurrentPlayerNum
            local reportItemID = self._twrCurrentItemID
            -- itemID is the load-bearing field -- the server looks this
            -- item up in the player's OWN inventory and reads identity
            -- from ITS OWN view of the modData (CGPT-017 fix). The
            -- contentId/mediaId/discoveryKey sent here are for debug-log
            -- comparison only; the server does not trust them.
            if reportData and reportItemID ~= nil then
                local okSend, sendErr = pcall(function()
                    sendClientCommand(getSpecificPlayer(reportPlayerNum), "twr_media", "played", {
                        itemID = reportItemID,
                        contentId = reportData.TWR_contentId,
                        mediaId = reportData.TWR_mediaId,
                        discoveryKey = reportData.TWR_discoveryKey,
                    })
                end)
                if not okSend then
                    print("TWR: Context.RecordedMedia -- sendClientCommand FAILED: " .. tostring(sendErr))
                end
            end
            originalDestroy(self)
        end
    end
end

-- REMOVED 2026-08-13: the OnFillInventoryObjectContextMenu hook that
-- added "Watch Tape" as a direct right-click option -- see file header.
-- No context-menu registration, no init() needed right now; this file
-- currently only defines TWR.Context.watchTape() for later reuse.
