-- TWR.Context.Debug -- adds a "TWR Debug" world right-click submenu,
-- letting each server-side TWR.Mechanics.* function get triggered live
-- without editing/reloading code. Hooks Events.OnFillWorldObjectContextMenu
-- -- CONFIRMED real (grepped from the installed B42 tree:
-- client/ISUI/ISWorldObjectContextMenu.lua's own
-- triggerEvent("OnFillWorldObjectContextMenu", player, context,
-- worldobjects, test) firing), same event vanilla's own world context
-- menu is built from. context:addSubMenu/addOption are the same
-- confirmed-real API vanilla uses throughout that file.
--
-- Only builds the menu client-side when isDebugEnabled() is true --
-- CONFIRMED a real client-side global too (client/DebugUIs/*.lua's own
-- usage). This is a UX nicety only, NOT the security boundary: the
-- server independently re-checks isDebugEnabled() and access level
-- "admin" before doing anything (server/TWR/Debug.lua) -- a client
-- could send the command regardless of whether this menu was shown.
--
-- This file is scaffolding, not permanent: delete alongside
-- server/TWR/Debug.lua once the real DB-driven job system exists.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
TWR = TWR or {}
TWR.Context = TWR.Context or {}
TWR.Context.callbacks = TWR.Context.callbacks or {}

local MECHANIC_LABELS = {
    { key = "container", label = "Spawn locked container" },
    { key = "scatter", label = "Scatter into existing containers" },
    { key = "deferred_area", label = "Deferred-area test (300 tiles away)" },
    { key = "corpse", label = "Spawn permanent corpse" },
    { key = "door", label = "Lock nearest door" },
    { key = "recipe", label = "Teach test recipe" },
    { key = "map_reveal", label = "Reveal map area" },
}

local function onFillWorldObjectContextMenu(player, context, worldobjects, test)
    if test then return end
    if not isDebugEnabled() then return end

    local okAccess, level = pcall(function() return getSpecificPlayer(player):getAccessLevel() end)
    if not okAccess or level ~= "admin" then return end

    local debugOption = context:addOption("TWR Debug", nil, nil)
    local debugMenu = ISContextMenu:getNew(context)
    context:addSubMenu(debugOption, debugMenu)

    for _, entry in ipairs(MECHANIC_LABELS) do
        debugMenu:addOption(entry.label, nil, function()
            sendClientCommand(getSpecificPlayer(player), "twr_debug", "run", { mechanic = entry.key })
        end)
    end
end

local function init()
    TWR.Runtime.registerEventOnce(TWR.Context.callbacks, "onFillWorldObjectContextMenu", Events.OnFillWorldObjectContextMenu, onFillWorldObjectContextMenu)
end

-- Self-initialize: immediate attempt handles F11 reload, OnGameStart
-- fallback handles the one-time first-boot ordering race -- same
-- pattern client/TWR/Context/Calendar.lua already established.
local ok, err = pcall(init)
if not ok then
    print("TWR: Context.Debug init deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(init)
