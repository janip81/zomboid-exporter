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
    { key = "deferred_area", label = "Deferred-area test (80 tiles east)" },
    { key = "corpse", label = "Spawn permanent corpse" },
    { key = "door", label = "Lock nearest door" },
    { key = "door_unlock_permanent", label = "Unlock nearest door permanently" },
    { key = "recipe", label = "Teach test recipe" },
    { key = "recipe_note", label = "Give lootable recipe note (read it)" },
    -- map_reveal DISABLED 2026-08-12 -- suspected of wiping previously-
    -- explored map data on a real dedicated MP client (confirmed prior
    -- exploration disappeared after a legitimate, small-radius reveal
    -- call). Root cause not understood yet -- see the URGENT/OPEN note
    -- in existing-world-test-matrix.md. Do not re-enable until resolved.
    { key = "coords", label = "Show my coordinates" },
}

-- DIAGNOSTIC 2026-08-12 -- ChatGPT-requested client-side recipe check
-- (existing-world-test-matrix.md "OPEN 2026-08-12: recipe genuinely
-- invisible" section). Every server-side check has already confirmed
-- correct; this runs the SAME checks but from the client's own local
-- ScriptManager/player state, since server state proves nothing about
-- what the client actually loaded/received. Purely local -- no
-- sendClientCommand, no server round-trip.
local RECIPE_NAME = "AntagonistProbeTestRecipe"

local function runClientRecipeDiagnostic()
    print("TWR: Context.Debug -- CLIENT recipe diagnostic for '" .. RECIPE_NAME .. "'")

    local okSM, scriptManager = pcall(function() return ScriptManager.instance end)
    if not okSM or not scriptManager then
        print("TWR: Context.Debug -- CLIENT ScriptManager.instance not accessible")
        return
    end

    local okRecipe, craftRecipe = pcall(function() return scriptManager:getCraftRecipe(RECIPE_NAME) end)
    if not okRecipe or not craftRecipe then
        print("TWR: Context.Debug -- CLIENT recipe == nil -- client mod/script loading problem (classification A)")
        return
    end

    local okName, name = pcall(function() return craftRecipe:getName() end)
    local okCat, category = pcall(function() return craftRecipe:getCategory() end)
    local okNTBL, needToBeLearn = pcall(function() return craftRecipe:getNeedToBeLearn() end)
    print("TWR: Context.Debug -- CLIENT recipe exists: name=" .. tostring(okName and name or "?") .. " category=" .. tostring(okCat and category or "?") .. " needToBeLearn=" .. tostring(okNTBL and needToBeLearn or "?"))

    local okPlayer, player = pcall(function() return getPlayer() end)
    if not okPlayer or not player then
        print("TWR: Context.Debug -- CLIENT getPlayer() failed")
        return
    end

    local okKR, knownRecipes = pcall(function() return player:getKnownRecipes() end)
    local okContains, contains = false, nil
    if okKR and knownRecipes then
        okContains, contains = pcall(function() return knownRecipes:contains(RECIPE_NAME) end)
    end
    local okIsKnown, isKnown = pcall(function() return player:isRecipeKnown(craftRecipe, true) end)
    print("TWR: Context.Debug -- CLIENT getKnownRecipes():contains=" .. tostring(okContains and contains or "?") .. " CLIENT isRecipeKnown(recipe,true)=" .. tostring(okIsKnown and isKnown or "?"))

    if okIsKnown and isKnown then
        print("TWR: Context.Debug -- CLIENT isRecipeKnown==true -- if crafting menu still doesn't show it after a full close+reopen, this is classification C (client list-filter problem), otherwise D (stale window cache)")
    else
        print("TWR: Context.Debug -- CLIENT isRecipeKnown==false -- classification B (server->client recipe-knowledge sync problem)")
    end
end

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

    -- Local-only diagnostic (no server round-trip) -- see
    -- runClientRecipeDiagnostic() header comment above.
    debugMenu:addOption("Client recipe diagnostic (local only)", nil, function()
        runClientRecipeDiagnostic()
    end)
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
