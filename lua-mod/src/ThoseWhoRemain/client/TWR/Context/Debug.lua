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
-- Menu visibility gates on access level "admin" alone -- matches the
-- server-side boundary (server/TWR/Debug.lua dropped its own
-- isDebugEnabled() check on 2026-08-11 for the same reason: it
-- reported false unreliably even with -debug genuinely present in the
-- server's launch args). CONFIRMED live 2026-08-11: dropped the
-- client-side isDebugEnabled() check here too after it silently hid
-- the menu for an admin player following a server restart -- same
-- unreliability as the server-side global, just noticed later. This
-- was only ever a UX nicety, never the real security boundary: the
-- server independently re-checks access level "admin" before doing
-- anything -- a client could send the command regardless of whether
-- this menu was shown.
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
    { key = "corpse_spawn_dead_probe", label = "TEST O: spawn near-zero-health corpse (0.01)" },
    { key = "corpse_spawn_control_probe", label = "TEST O-control: spawn normal-health zombie (1.0)" },
    { key = "corpse_direct_body_probe", label = "TEST P: construct IsoDeadBody directly" },
    { key = "corpse_sendcorpse_probe", label = "TEST Q1: real vanilla sendCorpse() sequence" },
    { key = "door", label = "Lock nearest door" },
    { key = "door_unlock_permanent", label = "Unlock nearest door permanently" },
    { key = "recipe", label = "Teach test recipe" },
    { key = "map_reveal", label = "Reveal map area" },
}

local function onFillWorldObjectContextMenu(player, context, worldobjects, test)
    if test then return end

    local okAccess, level = pcall(function() return getSpecificPlayer(player):getAccessLevel() end)
    if not okAccess or level ~= "admin" then return end

    print("TWR: Context.Debug -- building TWR Debug submenu (admin passed)")

    local debugOption = context:addOption("TWR Debug", nil, nil)
    local debugMenu = ISContextMenu:getNew(context)
    context:addSubMenu(debugOption, debugMenu)

    for _, entry in ipairs(MECHANIC_LABELS) do
        local mechanicKey = entry.key
        local mechanicLabel = entry.label
        debugMenu:addOption(mechanicLabel, nil, function()
            print("TWR: Context.Debug -- clicked '" .. mechanicLabel .. "', sending twr_debug/run mechanic=" .. mechanicKey)
            local okSend, sendErr = pcall(function()
                sendClientCommand(getSpecificPlayer(player), "twr_debug", "run", { mechanic = mechanicKey })
            end)
            if not okSend then
                print("TWR: Context.Debug -- sendClientCommand FAILED: " .. tostring(sendErr))
            end
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
