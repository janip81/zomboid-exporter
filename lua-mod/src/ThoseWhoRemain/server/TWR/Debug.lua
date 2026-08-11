-- TWR.Debug -- exercises each real TWR.Mechanics.* function against
-- wherever the calling admin/tester currently is, so the ported
-- mechanics can be verified live on a real server without needing the
-- quest-database/job bridge to exist yet.
--
-- Triggered via the client's right-click "TWR Debug" world context
-- menu (client/TWR/Context/Debug.lua) -> sendClientCommand(player,
-- "twr_debug", "run", {mechanic=...}) -> this file's
-- Events.OnClientCommand handler. Same bridge vanilla's own admin
-- tools use -- CONFIRMED real: sendClientCommand(getPlayer(), "map",
-- "setKnownInSquares", args) is exactly how PrintMedia.lua's "reveal
-- on map" button reaches WorldMapVisitedServer server-side (already
-- relied on by TWR.Mechanics.MapReveal), and
-- Events.OnClientCommand.Add(ClientCommands.OnClientCommand) is
-- vanilla's own server-side registration for that same bridge
-- (server/ClientCommands.lua).
--
-- Double-gated: isDebugEnabled() (CONFIRMED real server-side global --
-- server/CraftRecipeCode/CraftRecipe_BuildMenu.lua's own `return
-- isDebugEnabled()`, server/Camping/camping_tent.lua's `getDebug()`
-- equivalent -- reflects the server's actual debug/sandbox state) AND
-- the calling player's access level must be "admin". Both checked
-- SERVER-side, independent of whether the client-side menu happened to
-- be visible -- a client can send any command it wants, so
-- authorization can never live only in whether a menu option was shown.
--
-- This file is scaffolding, not permanent: delete once the real
-- DB-driven job system exists to exercise these functions instead.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
--
-- CONFIRMED live 2026-08-11: files under media/lua/server/ are ALSO
-- loaded and executed by a connecting MP CLIENT, not just the real
-- dedicated server process (mod content syncs wholesale) -- live-
-- reproduced bug where a client's own local copy of this file silently
-- "handled" the debug menu's command end to end (registered its own
-- handler, printed success) without the real authoritative server ever
-- seeing it, so nothing actually happened in the game world. Matches
-- vanilla's own confirmed pattern (grepped: server/TransactionProcessor.lua,
-- server/Vehicles/VehicleCommands.lua, server/Traps/STrapGlobalObject.lua
-- all start with exactly this guard) -- isClient() is real, and this is
-- the standard fix.
if isClient() then return end

TWR = TWR or {}
TWR.Debug = TWR.Debug or {}

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    print("TWR.Debug: " .. methodName .. "() failed: " .. tostring(v))
    return false, nil
end

local function runContainer(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local bx, by, bz = math.floor(x) + 1, math.floor(y), math.floor(z)
    print("TWR.Debug: runContainer -- player at (" .. tostring(x) .. "," .. tostring(y) .. "," .. tostring(z) .. "), target (" .. bx .. "," .. by .. "," .. bz .. ")")
    local crate = TWR.Mechanics.Container.spawnBox(bx, by, bz)
    if not crate then
        print("TWR.Debug: runContainer -- spawnBox failed")
        return
    end
    local okC, container = safeCall(crate, "getContainer")
    if okC and container then
        safeCall(container, "AddItem", "Base.Twigs")
    end
    TWR.Mechanics.Container.lockByCode(crate, 123)
    print("TWR.Debug: runContainer -- box spawned one tile east, combination-locked to 123, contains a twig")
end

local function runScatter(p)
    local okCell, cell = pcall(function() return getCell() end)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okCell and cell and okX and okY and okZ) then return end

    local placed = TWR.Mechanics.Container.scatterIntoExisting(cell, math.floor(x), math.floor(y), math.floor(z), 15, 42, 3, "Base.Twigs")
    print("TWR.Debug: runScatter -- placed " .. placed .. " twigs into existing containers within 15 tiles")
end

local function runDeferredArea(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local tx, ty, tz = math.floor(x) + 300, math.floor(y) - 300, math.floor(z)
    print("TWR.Debug: runDeferredArea -- watching (" .. tx .. "," .. ty .. "," .. tz .. "), walk there to trigger")
    TWR.Mechanics.DeferredArea.waitForSquare(tx, ty, tz, function(square)
        local okCell, cell = pcall(function() return getCell() end)
        if not okCell or not cell then return end
        local placed = TWR.Mechanics.Container.scatterIntoExisting(cell, tx, ty, tz, 20, 42, 1, "Base.Twigs")
        print("TWR.Debug: runDeferredArea -- area loaded, placed " .. placed .. " twig(s)")
    end)
end

local function runCorpse(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local ok = TWR.Mechanics.Corpse.spawnPermanentCorpse(math.floor(x) + 1, math.floor(y), math.floor(z), "Police", 0, {"Base.Twigs"})
    print("TWR.Debug: runCorpse -- spawnPermanentCorpse " .. (ok and "SUCCEEDED, one tile east" or "FAILED"))
end

local function runDoor(p)
    local okCell, cell = pcall(function() return getCell() end)
    local okSquare, square = safeCall(p, "getCurrentSquare")
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okCell and cell and okX and okY and okZ) then return end

    local door = okSquare and square and TWR.Mechanics.Door.findDoorOnSquare(square)
    if not door then
        door = TWR.Mechanics.Door.findNearbyDoor(cell, math.floor(x), math.floor(y), math.floor(z))
    end
    if not door then
        print("TWR.Debug: runDoor -- no door found on current square or its 4 neighbors")
        return
    end

    local keyId = TWR.Mechanics.Door.lockToKey(door, nil, p)
    print("TWR.Debug: runDoor -- locked, keyId=" .. tostring(keyId) .. ", matching key added to your inventory -- test from the EXTERIOR side")
end

local function runRecipe(p)
    local ok = TWR.Mechanics.Recipe.teach(p, "AntagonistProbeTestRecipe")
    print("TWR.Debug: runRecipe -- teach() " .. (ok and "SUCCEEDED -- check the crafting menu" or "FAILED"))
end

local function runMapReveal(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    if not (okX and okY) then return end

    local ok = TWR.Mechanics.MapReveal.revealAroundPoint(p, x, y, 20)
    print("TWR.Debug: runMapReveal -- revealAroundPoint " .. (ok and "SUCCEEDED -- check the map" or "FAILED"))
end

local MECHANICS = {
    container = runContainer,
    scatter = runScatter,
    deferred_area = runDeferredArea,
    corpse = runCorpse,
    door = runDoor,
    recipe = runRecipe,
    map_reveal = runMapReveal,
}

local function onClientCommand(module, command, player, args)
    if module ~= "twr_debug" or command ~= "run" then return end

    -- CONFIRMED live 2026-08-11: dropped the server-side isDebugEnabled()
    -- requirement here. -debug was confirmed actually present in the
    -- server's own launch args (verified directly in /proc), yet
    -- isDebugEnabled() still reported false for every real command
    -- received -- it evidently checks something other than the launch
    -- flag server-side (the CLIENT's own working debugger, from earlier
    -- tonight, never depended on this server having any -debug flag at
    -- all, which was the first sign this assumption was wrong). Access
    -- level "admin" alone is the real authorization boundary here and
    -- is sufficient for a throwaway debug tool -- see the client-side
    -- isDebugEnabled() check in client/TWR/Context/Debug.lua for why
    -- that one still matters (menu visibility/UX only, not security).
    local okLevel, level = safeCall(player, "getAccessLevel")
    if not okLevel or level ~= "admin" then
        print("TWR.Debug: twr_debug command received from non-admin access level (" .. tostring(level) .. "), ignoring")
        return
    end

    local mechanic = args and args.mechanic
    local fn = mechanic and MECHANICS[mechanic]
    if not fn then
        print("TWR.Debug: unknown mechanic '" .. tostring(mechanic) .. "'")
        return
    end

    fn(player)
end

local function init()
    TWR.Runtime.registerEventOnce(TWR.Debug, "clientCommand", Events.OnClientCommand, onClientCommand)
    print("TWR.Debug: OnClientCommand handler registered")
end

-- Self-initialize: immediate attempt handles F11 reload. CONFIRMED live
-- 2026-08-11: the immediate attempt deterministically fails here every
-- single boot -- root cause is alphabetical same-folder load order
-- ("Debug.lua" < "Runtime.lua" within server/TWR/), not a nondeterministic
-- timing race, so TWR.Runtime genuinely does not exist yet at this
-- file's own load time.
--
-- Events.OnGameStart.Add(init) was the fallback here originally (same
-- pattern proven reliable CLIENT-side, e.g. client/TWR/Context/
-- Calendar.lua) -- CONFIRMED BROKEN server-side: ran for 2+ hours on a
-- live pod with the retry never completing (no
-- "OnClientCommand handler registered" success print ever appeared,
-- despite the file having that exact print statement). Events.OnGameStart
-- apparently does not reliably fire on a headless dedicated server the
-- way it does for a connecting client -- worth remembering for any
-- future server-side TWR file with a load-time dependency.
--
-- Fixed with a self-limiting EveryOneMinute retry instead -- CONFIRMED
-- reliable server-side all night (TEST F/H/M all depended on it).
-- Removes itself the moment init() succeeds.
local function retryInit()
    local ok = pcall(init)
    if ok then
        Events.EveryOneMinute.Remove(retryInit)
    end
end

local ok, err = pcall(init)
if not ok then
    print("TWR.Debug: init deferred, retrying every minute (dependency not loaded yet): " .. tostring(err))
    Events.EveryOneMinute.Add(retryInit)
end
