-- TWR.Context.RecordedMedia -- adds a "Watch Tape" inventory context
-- option for any item carrying modData.TWR_isVHS (see
-- server/TWR/Mechanics/RecordedMedia.lua's header for why this
-- bypasses vanilla's native RecMedia registry entirely).
--
-- Hooks Events.OnFillInventoryObjectContextMenu -- CONFIRMED real
-- (grepped: client/ISUI/ISInventoryPaneContextMenu.lua:935
-- `triggerEvent("OnFillInventoryObjectContextMenu", player, context, items)`).
--
-- Opens the native ISMediaInfo popup (client/RecordedMedia/ISMediaInfo.lua
-- -- confirmed a standalone generic rich-text window, no RecMedia
-- dependency) directly with the item's own modData text. On close,
-- reports back to the server via sendClientCommand so
-- Trigger.MediaPlaybackCompleted (server-side, RecordedMedia.lua's
-- onMediaPlayed) can distinguish "picked up" from "actually watched."
--
-- UNCONFIRMED LIVE: whether wrapping ISMediaInfo.destroy per-instance
-- like this reliably fires for every close path (OK button vs window
-- X vs Esc) -- see antagonist/tests/ once tested.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

local function onWatchTape(playerNum, item)
    local okData, modData = pcall(function() return item:getModData() end)
    if not okData or not modData or not modData.TWR_vhsText then return end

    local panel = ISMediaInfo.openPanel(playerNum, modData.TWR_vhsText)

    -- Report "playback completed" the moment the player closes the
    -- panel, not when they open it -- wrap this specific panel
    -- instance's destroy() once, restore immediately after so we don't
    -- affect any other future ISMediaInfo use.
    if panel and not panel._twrWrapped then
        panel._twrWrapped = true
        local originalDestroy = panel.destroy
        panel.destroy = function(self)
            local okSend, sendErr = pcall(function()
                sendClientCommand(getSpecificPlayer(playerNum), "twr_media", "played", {
                    contentId = modData.TWR_contentId,
                    mediaId = modData.TWR_mediaId,
                    discoveryKey = modData.TWR_discoveryKey,
                })
            end)
            if not okSend then
                print("TWR: Context.RecordedMedia -- sendClientCommand FAILED: " .. tostring(sendErr))
            end
            originalDestroy(self)
        end
    end
end

local function onFillInventoryObjectContextMenu(player, context, items)
    for _, item in ipairs(items) do
        -- items entries can be plain InventoryItem or {items={...}} stacks
        -- (same shape vanilla's own handlers defend against elsewhere in
        -- ISInventoryPaneContextMenu.lua) -- normalize to a single item.
        local realItem = item
        if type(item) == "table" and item.items then
            realItem = item.items[1]
        end
        if realItem then
            local okData, modData = pcall(function() return realItem:getModData() end)
            if okData and modData and modData.TWR_isVHS then
                context:addOption("Watch Tape", nil, function()
                    onWatchTape(player, realItem)
                end)
            end
        end
    end
end

local function init()
    TWR.Runtime.registerEventOnce(TWR.Context.callbacks, "onFillInventoryObjectContextMenuRecordedMedia", Events.OnFillInventoryObjectContextMenu, onFillInventoryObjectContextMenu)
end

local ok, err = pcall(init)
if not ok then
    print("TWR: Context.RecordedMedia init deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(init)
