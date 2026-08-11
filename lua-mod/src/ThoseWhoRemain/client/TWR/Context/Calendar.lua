-- TWR.Context.Calendar -- detects a ThoseWhoRemain.Calendar in the
-- player's inventory context menu and adds "Check Calendar". Promoted
-- from CalendarProbe_Dev after CAL-A confirmed this live 2026-08-11:
-- Events.OnFillInventoryObjectContextMenu fires as expected, and the
-- {items={...}} stack-wrapper unwrap logic below matches what B42
-- actually passes.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

local function onFillInventoryObjectContextMenu(playerIndex, context, items)
    if not items then return end
    for _, entry in ipairs(items) do
        local item = entry
        if type(entry) == "table" and entry.items then
            item = entry.items[1]
        end

        local okType, fullType = safeCall(item, "getFullType")
        if okType and fullType == TWR.Constants.CALENDAR_FULLTYPE then
            pcall(function()
                context:addOption("Check Calendar", item, function()
                    TWRCalendarUI.open(item)
                end)
            end)
            return
        end
    end
end

local function init()
    TWR.Runtime.registerEventOnce(TWR.Context.callbacks, "onFillInventoryObjectContextMenu", Events.OnFillInventoryObjectContextMenu, onFillInventoryObjectContextMenu)
end

-- Self-initialize: immediate attempt handles F11 reload, OnGameStart
-- fallback handles the one-time first-boot ordering race (same pattern
-- ExporterLog's own trackers use, and the load-order lesson
-- CalendarProbe's own header documented tonight).
local ok, err = pcall(init)
if not ok then
    print("TWR: Context.Calendar init deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(init)
