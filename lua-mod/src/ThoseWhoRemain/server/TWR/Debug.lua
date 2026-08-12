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
    TWR.Mechanics.Container.finalizeSpawn(crate)
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

-- TEST O -- direct dead-on-spawn corpse, bypassing zombie:setHealth(0)
-- entirely. CONFIRMED live 2026-08-12: the existing spawn-then-kill
-- approach (spawn healthy via the 6-arg addZombiesInOutfit, then call
-- zombie:setHealth(0)) leaves isDead()==true but
-- square:getDeadBodys():size()==0 -- the zombie is logically dead but
-- no real IsoDeadBody/corpse world object was ever created. Matches the
-- earlier spawnBox() pattern: vanilla never actually calls
-- zombie:setHealth(0) anywhere itself (grepped -- only
-- animal:setHealth(0) exists, via shared/TimedActions/Animals/
-- ISKillAnimal.lua), so there's no proven precedent this triggers the
-- same corpse-creation path a real combat kill does.
--
-- CONFIRMED via grep (client/DebugUIs/ISSpawnHordeUI.lua onSpawn(),
-- vanilla's own zombie-spawn debug tool): addZombiesInOutfit has a
-- FULLER 17-positional-arg form exposing a `health` parameter directly
-- at spawn time -- (x, y, z, count, outfit, femaleChance, crawler,
-- isFallOnFront, isFakeDead, knockedDown, isInvulnerable, isSitting,
-- health, isRecordingAnims, heightOffset, isRagdolling, onFire).
-- self.healthSlider:setValues(0, 2, ...) confirms health is a 0-2
-- multiplier (default 1.0), not a flag -- health=0 at spawn should be a
-- genuinely different, vanilla-exposed code path from spawning healthy
-- and killing via Lua afterward. UNTESTED until now.
-- LIVE FINDING round 1: health=0 via the 17-arg form returned a
-- zombieList whose get(0) failed -- meaning the list came back EMPTY
-- (zero zombies actually spawned), not that the call errored. This
-- helper takes health/x-offset as parameters so a control run
-- (health=1, proves the 17-arg form itself works) and a near-zero
-- variant (health=0.01, tests whether exactly 0 is a special-cased
-- "don't spawn" threshold) can both run from one restart cycle.
local function corpseSpawnDeadProbe(p, health, xOffset, label)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local bx, by, bz = math.floor(x) + xOffset, math.floor(y), math.floor(z)
    print("TWR.Debug: " .. label .. " -- spawning at (" .. bx .. "," .. by .. "," .. bz .. "), health=" .. tostring(health))

    local okList, zombieList = pcall(function()
        return addZombiesInOutfit(bx, by, bz, 1, "Police", 0, false, false, false, false, false, false, health, false, 0, false, false)
    end)
    if not okList or not zombieList then
        print("TWR.Debug: " .. label .. " -- addZombiesInOutfit FAILED: " .. tostring(zombieList))
        return
    end

    local okSize, listSize = safeCall(zombieList, "size")
    print("TWR.Debug: " .. label .. " -- zombieList:size()=" .. tostring(okSize and listSize or "?"))

    local okZ0, zombie = pcall(function() return zombieList:get(0) end)
    if not okZ0 or not zombie then
        print("TWR.Debug: " .. label .. " -- zombieList:get(0) FAILED (list is empty -- no zombie was actually placed)")
        return
    end

    local okCell, cell = pcall(function() return getCell() end)
    local okSq, square = pcall(function() return okCell and cell:getGridSquare(bx, by, bz) end)
    local okDead, isDead = safeCall(zombie, "isDead")
    local okHealthNow, healthNow = safeCall(zombie, "getHealth")

    local okBodies, bodies = false, nil
    if okSq and square then
        okBodies, bodies = safeCall(square, "getDeadBodys")
    end
    print("TWR.Debug: " .. label .. " -- result: getHealth()=" .. tostring(okHealthNow and healthNow or "?") .. ", isDead=" .. tostring(okDead and isDead) .. ", square getDeadBodys():size()=" .. tostring(okBodies and bodies and bodies:size() or "?"))
end

-- Control run: health=1 (normal) -- proves the 17-arg form itself
-- spawns a real, live zombie before testing whether health=0
-- specifically is what causes zero zombies to be placed.
local function runCorpseSpawnControlProbe(p)
    corpseSpawnDeadProbe(p, 1, 1, "TEST O-control (health=1)")
end

-- Near-zero variant: tests whether exactly 0 is special-cased as
-- "don't spawn" (round 1 finding) vs. a genuine near-death spawn.
local function runCorpseSpawnDeadProbe(p)
    corpseSpawnDeadProbe(p, 0.01, -1, "TEST O (health=0.01)")
end

-- TEST P -- construct a real IsoDeadBody directly, instead of trying to
-- convert an already-spawned zombie into one after the fact (TEST O's
-- whole approach). CONFIRMED live 2026-08-12: the original spawn-then-
-- setHealth(0) approach (Corpse.spawnPermanentCorpse) leaves
-- getDeadBodys():size()==0 immediately after death -- and per the
-- user's live report, those SAME corpses only became visible after an
-- unrelated server restart+relogin the next day. That strongly implies
-- corpse creation is normally driven by native combat-kill code (or a
-- save/reload boundary), not by anything a Lua-only setHealth(0) call
-- triggers -- impractical for a live quest system that can't restart
-- the server to spawn a corpse.
--
-- CONFIRMED via grep: IsoDeadBody.new(entity, wasCorpseAlready,
-- addToSquareAndWorld) is a real 3-arg constructor, used server-side in
-- shared/TimedActions/Animals/ISKillAnimalInInventory.lua:kill() and
-- server/Traps/STrapGlobalObject.lua:removeAnimalCorpse() -- but BOTH
-- confirmed real usages pass addToSquareAndWorld=false (they convert an
-- animal into a carryable inventory-item corpse, isoDeadBody:getItem(),
-- never placed in the world). There is NO vanilla precedent anywhere in
-- the installed tree for addToSquareAndWorld=true -- this probe is
-- testing genuinely unproven territory, not confirming a known-working
-- vanilla pattern (unlike TEST N's IsoThumpable.new(), which matched
-- ISWoodenContainer:create() almost exactly). The parameter name
-- strongly implies this is the toggle for "actually place a real world
-- IsoDeadBody, not just an inventory item" -- worth testing directly.
local function runCorpseDirectBodyProbe(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local bx, by, bz = math.floor(x), math.floor(y) + 1, math.floor(z)
    print("TWR.Debug: TEST P -- runCorpseDirectBodyProbe -- spawning live zombie at (" .. bx .. "," .. by .. "," .. bz .. ") to convert directly")

    local okList, zombieList = pcall(function()
        return addZombiesInOutfit(bx, by, bz, 1, "Police", 0)
    end)
    if not okList or not zombieList then
        print("TWR.Debug: TEST P -- addZombiesInOutfit FAILED: " .. tostring(zombieList))
        return
    end

    local okZ0, zombie = pcall(function() return zombieList:get(0) end)
    if not okZ0 or not zombie then
        print("TWR.Debug: TEST P -- zombieList:get(0) FAILED")
        return
    end

    local okBody, body = pcall(function() return IsoDeadBody.new(zombie, false, true) end)
    if not okBody or not body then
        print("TWR.Debug: TEST P -- IsoDeadBody.new(zombie, false, true) FAILED: " .. tostring(body))
        return
    end

    print("TWR.Debug: TEST P -- IsoDeadBody.new() call succeeded (no error), checking real state now")

    local okCell, cell = pcall(function() return getCell() end)
    local okSq, square = pcall(function() return okCell and cell:getGridSquare(bx, by, bz) end)
    local okBodies, bodies = false, nil
    if okSq and square then
        okBodies, bodies = safeCall(square, "getDeadBodys")
    end
    local okGetItem, item = safeCall(body, "getItem")

    print("TWR.Debug: TEST P -- result: square getDeadBodys():size()=" .. tostring(okBodies and bodies and bodies:size() or "?") .. ", body:getItem()=" .. tostring(okGetItem and item ~= nil))
end

-- Shared by runDoor and runDoorUnlockPermanent -- finds the door on the
-- player's current square, falling back to its 4 orthogonal neighbors.
-- Prints diagnostics either way so a "no door found" report can be told
-- apart from a "found the wrong door" report.
local function findDoorNearPlayer(p, label)
    local okCell, cell = pcall(function() return getCell() end)
    local okSquare, square = safeCall(p, "getCurrentSquare")
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okCell and cell and okX and okY and okZ) then return nil end

    print("TWR.Debug: " .. label .. " -- player at (" .. tostring(x) .. "," .. tostring(y) .. "," .. tostring(z) .. "), current square (" .. math.floor(x) .. "," .. math.floor(y) .. ")")

    local door = okSquare and square and TWR.Mechanics.Door.findDoorOnSquare(square)
    if not door then
        door = TWR.Mechanics.Door.findNearbyDoor(cell, math.floor(x), math.floor(y), math.floor(z))
    end
    if not door then
        print("TWR.Debug: " .. label .. " -- no door found on current square or its 4 neighbors")
        return nil
    end

    local okDx, dx = safeCall(door, "getX")
    local okDy, dy = safeCall(door, "getY")
    print("TWR.Debug: " .. label .. " -- door found at (" .. tostring(okDx and dx or "?") .. "," .. tostring(okDy and dy or "?") .. ")")

    return door
end

local function runDoor(p)
    local door = findDoorNearPlayer(p, "runDoor")
    if not door then return end

    local keyId = TWR.Mechanics.Door.lockToKey(door, nil, p)
    print("TWR.Debug: runDoor -- locked, keyId=" .. tostring(keyId) .. ", matching key added to your inventory -- test from the EXTERIOR side. This is the 'relock_on_close' policy: closing the door re-locks it, key required every time. Use 'Unlock nearest door permanently' to test the 'permanent' policy instead.")
end

local function runDoorUnlockPermanent(p)
    local door = findDoorNearPlayer(p, "runDoorUnlockPermanent")
    if not door then return end

    TWR.Mechanics.Door.unlockPermanent(door)
    print("TWR.Debug: runDoorUnlockPermanent -- door converted to plain/permanently unlocked. Verify: opens freely with no key, and closing it does NOT re-lock it.")
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
    corpse_spawn_dead_probe = runCorpseSpawnDeadProbe,
    corpse_spawn_control_probe = runCorpseSpawnControlProbe,
    corpse_direct_body_probe = runCorpseDirectBodyProbe,
    door = runDoor,
    door_unlock_permanent = runDoorUnlockPermanent,
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
