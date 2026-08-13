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

    print("TWR.Debug: runScatter -- center (" .. math.floor(x) .. "," .. math.floor(y) .. "," .. math.floor(z) .. "), radius 15")
    local placed = TWR.Mechanics.Container.scatterIntoExisting(cell, math.floor(x), math.floor(y), math.floor(z), 15, 42, 3, "Base.Twigs")
    print("TWR.Debug: runScatter -- placed " .. placed .. " twigs into existing containers within 15 tiles")
end

-- TEST: scatterAcrossMap against RANDOMLY SELECTED real houses/offices
-- -- a small-scale stand-in for a "flyer scattered all over town"
-- narrative device. FIX 2026-08-13 (round 2): the first version picked
-- purely random (x,y) across the whole map's bounding box -- CONFIRMED
-- live this mostly lands in fields/woods with nothing nearby at all
-- (most of the map isn't a building), which is honest but useless for
-- testing "does it land in containers" like actually wanted. This pool
-- is every House/Office-tagged POI pulled from
-- exporter-ideas/tools/pzmap/data/live_poi.json's real "Categories"
-- field (62 entries, spans Louisville/Muldraugh/Riverside/Rosewood/West
-- Point/etc) -- the RANDOM part is which N of these real building
-- anchors get picked each run (ZombRand, no replacement), not the
-- underlying coordinate itself. z=0 for all (ground floor) --
-- untested against upper floors.
local MAP_SCATTER_POINT_COUNT = 5
local MAP_SCATTER_POOL = {
    { x = 1264, y = 7381, z = 0, label = "House, Brandenburg" },
    { x = 11493, y = 8914, z = 0, label = "House, Dixie" },
    { x = 7090, y = 8373, z = 0, label = "House, Doe Valley" },
    { x = 4243, y = 7226, z = 0, label = "House, Doe Valley" },
    { x = 4127, y = 9418, z = 0, label = "House, Doe Valley" },
    { x = 12037, y = 2593, z = 0, label = "House, Louisville" },
    { x = 14150, y = 2628, z = 0, label = "House, Louisville" },
    { x = 12477, y = 1770, z = 0, label = "Communications, Louisville" },
    { x = 12316, y = 3535, z = 0, label = "Office, Louisville" },
    { x = 12414, y = 2837, z = 0, label = "Office, Louisville" },
    { x = 12320, y = 3412, z = 0, label = "House, Louisville" },
    { x = 12628, y = 3668, z = 0, label = "Office, Louisville" },
    { x = 12662, y = 1400, z = 0, label = "Office, Louisville" },
    { x = 13534, y = 2852, z = 0, label = "House, Louisville" },
    { x = 13423, y = 1732, z = 0, label = "House, Louisville" },
    { x = 13580, y = 1701, z = 0, label = "Medical, Louisville" },
    { x = 12084, y = 1616, z = 0, label = "Factory, Louisville" },
    { x = 12207, y = 1799, z = 0, label = "House, Louisville" },
    { x = 12667, y = 2191, z = 0, label = "Office, Louisville" },
    { x = 12618, y = 3555, z = 0, label = "Office, Louisville" },
    { x = 13561, y = 1581, z = 0, label = "Communications, Louisville" },
    { x = 14099, y = 2810, z = 0, label = "House, Louisville" },
    { x = 12340, y = 2057, z = 0, label = "Office, Louisville" },
    { x = 12245, y = 3542, z = 0, label = "Office, Louisville" },
    { x = 12551, y = 3779, z = 0, label = "Office, Louisville" },
    { x = 12733, y = 3969, z = 0, label = "House, Louisville" },
    { x = 12645, y = 1918, z = 0, label = "Office, Louisville" },
    { x = 13423, y = 3738, z = 0, label = "House, Louisville" },
    { x = 14824, y = 3716, z = 0, label = "House, Louisville" },
    { x = 12421, y = 1423, z = 0, label = "Office, Louisville" },
    { x = 13597, y = 1699, z = 0, label = "Office, Louisville" },
    { x = 12714, y = 1621, z = 0, label = "Spiffo, Louisville" },
    { x = 12670, y = 2137, z = 0, label = "Office, Louisville" },
    { x = 12561, y = 3617, z = 0, label = "Office, Louisville" },
    { x = 12552, y = 3703, z = 0, label = "Office, Louisville" },
    { x = 10072, y = 12783, z = 0, label = "Office, March Ridge" },
    { x = 10078, y = 12625, z = 0, label = "House, March Ridge" },
    { x = 10182, y = 12791, z = 0, label = "Office, March Ridge" },
    { x = 10278, y = 8749, z = 0, label = "Communications, March Ridge" },
    { x = 10747, y = 9412, z = 0, label = "House, Muldraugh" },
    { x = 11063, y = 10638, z = 0, label = "Remote, Muldraugh" },
    { x = 10687, y = 9563, z = 0, label = "House, Muldraugh" },
    { x = 10856, y = 10138, z = 0, label = "House, Muldraugh" },
    { x = 10090, y = 8260, z = 0, label = "House, Muldraugh" },
    { x = 9344, y = 10295, z = 0, label = "Remote, Muldraugh" },
    { x = 9641, y = 10152, z = 0, label = "Office, Muldraugh" },
    { x = 4832, y = 6280, z = 0, label = "Communications, Riverside" },
    { x = 6782, y = 5446, z = 0, label = "House, Riverside" },
    { x = 8001, y = 11440, z = 0, label = "Office, Rosewood" },
    { x = 7920, y = 11512, z = 0, label = "House, Rosewood" },
    { x = 8341, y = 11750, z = 0, label = "House, Rosewood" },
    { x = 8833, y = 11609, z = 0, label = "House, Rosewood" },
    { x = 13887, y = 4040, z = 0, label = "House, Valley Station" },
    { x = 14070, y = 5203, z = 0, label = "House, Valley Station" },
    { x = 13101, y = 5303, z = 0, label = "Remote, Valley Station" },
    { x = 14396, y = 4568, z = 0, label = "House, West Point" },
    { x = 10095, y = 6654, z = 0, label = "House, West Point" },
    { x = 11985, y = 6943, z = 0, label = "Office, West Point" },
    { x = 6470, y = 5311, z = 0, label = "Office, Riverside" },
    { x = 6538, y = 5187, z = 0, label = "Office, Riverside" },
    { x = 6289, y = 5331, z = 0, label = "Office, Riverside" },
    { x = 6484, y = 6170, z = 0, label = "Remote, Doe Valley Lake" },
}

-- Fisher-Yates partial shuffle (ZombRand, no math.random -- see
-- scatterIntoExisting's own header for why) -- picks `count` distinct
-- entries from `pool` without replacement, without mutating pool
-- itself.
local function pickRandomDistinct(pool, count)
    local working = {}
    for i, v in ipairs(pool) do working[i] = v end

    local picked = {}
    local n = #working
    for i = 1, math.min(count, n) do
        local idx = ZombRand(n - i + 1) + 1
        picked[i] = working[idx]
        working[idx] = working[n - i + 1]
    end
    return picked
end

local function runMapScatter(p)
    local points = pickRandomDistinct(MAP_SCATTER_POOL, MAP_SCATTER_POINT_COUNT)

    print("TWR.Debug: runMapScatter -- queuing " .. #points .. " RANDOMLY-CHOSEN real houses/offices, radius 5, will place as each area loads:")
    for i, point in ipairs(points) do
        print("TWR.Debug: runMapScatter -- point " .. i .. ": (" .. point.x .. "," .. point.y .. "," .. point.z .. ") " .. tostring(point.label))
    end
    TWR.Mechanics.Container.scatterAcrossMap(points, 5, 42, "Base.Twigs")
end

-- Reports current TWR.PendingActions state -- the production,
-- action-type-generic successor to the disposable PendingSpawnTest.lua
-- probe (now removed; its validation result is written up in
-- zomboid-exporter-ideas/antagonist/sglobalobjectsystem-persistence-validation.md).
-- No create-trigger here anymore -- pending actions are now requested
-- by mechanics themselves (e.g. Container.scatterAcrossMap), not
-- directly from the debug menu.
local function runPendingActionsStatus(p)
    TWR.PendingActions.reportStatus()
end

local function runDeferredArea(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    -- FIX 2026-08-13: promoted off DeferredArea.waitForSquare onto
    -- TWR.PendingActions -- same reason scatterAcrossMap was: a queued
    -- crate spawn at a not-yet-loaded square used to vanish with zero
    -- trace on any server restart. Now persists via
    -- Container.resolveSpawnContainer -- survives a restart before the
    -- admin gets there, same as the map-scatter test.
    --
    -- Offset: +80 tiles east only (single-axis, moderate) -- history:
    -- was a fixed +/-300 diagonal offset originally, admin hit open
    -- ocean once, shrunk to keep the watched point closer to wherever
    -- the player already is (more likely inhabited/dry land).
    local tx, ty, tz = math.floor(x) + 80, math.floor(y), math.floor(z)
    local jobId = "debug-deferredarea-" .. ZombRand(1000000000)
    print("TWR.Debug: runDeferredArea -- queuing persistent spawn_container at (" .. tx .. "," .. ty .. "," .. tz .. "), walk there to trigger (survives restart)")
    TWR.PendingActions.request(jobId, jobId .. "-artifact", "spawn_container", "Container", tx, ty, tz, {
        itemType = "Base.Twigs",
    })
end

local function runCorpse(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local ok = TWR.Mechanics.Corpse.spawnPermanentCorpse(math.floor(x) + 1, math.floor(y), math.floor(z), "Police", 0, {"Base.Twigs"})
    print("TWR.Debug: runCorpse -- spawnPermanentCorpse " .. (ok and "SUCCEEDED, one tile east" or "FAILED"))
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

local function runRecipeForget(p)
    local ok = TWR.Mechanics.Recipe.forget(p, "AntagonistProbeTestRecipe")
    print("TWR.Debug: runRecipeForget -- forget() " .. (ok and "SUCCEEDED -- recipe should be gone from the crafting menu" or "FAILED"))
end

-- PRODUCTION-PATH alternative to runRecipe() above: instead of an
-- instant admin grant, gives a real lootable ThoseWhoRemain.RecipeNote
-- item (scripts/twr_items.txt, LearnedRecipes=AntagonistProbeTestRecipe)
-- directly to inventory for quick testing. Read it normally (right-click
-- -> Read) to learn the recipe via vanilla's own ISReadABook.lua
-- :complete() -> character:ReadLiterature(item) path -- CONFIRMED real,
-- the same native mechanism magazines/skill books use, no manual
-- learnRecipe()/sendSyncPlayerFields needed on our end at all. For real
-- production use, spawn this item into a container/corpse instead of
-- directly to inventory (a one-line swap, see Container.spawnBox or
-- Corpse.spawnPermanentCorpse's lootItems for the pattern).
local function runRecipeNote(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local okAdd, item = safeCall(inventory, "AddItem", "ThoseWhoRemain.RecipeNote")
    print("TWR.Debug: runRecipeNote -- gave ThoseWhoRemain.RecipeNote to inventory " .. (okAdd and "SUCCEEDED -- right-click it and choose Read" or "FAILED"))
end

local function runMapReveal(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    if not (okX and okY) then return end

    local radius = 60
    local ok = TWR.Mechanics.MapReveal.revealAroundPoint(p, x, y, radius)
    print("TWR.Debug: runMapReveal -- revealAroundPoint center (" .. math.floor(x) .. "," .. math.floor(y) .. "), radius " .. radius .. " " .. (ok and "SUCCEEDED -- open the map (M) and look there" or "FAILED"))
end

-- P1 (readable content) debug test -- gives a dummy DB-shaped readable
-- item directly to inventory. Uses the just-confirmed
-- sendAddItemToContainer() MP-sync fix (antagonist/DONE.md,
-- 2026-08-13) rather than the old transmitCompleteItemToClients()
-- dead end.
local function runReadable(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local item, err = TWR.Mechanics.Readable.buildItem({
        contentId = "dummy.paper.001",
        displayName = "Folded Paper",
        text = "dummy test content",
        discoveryKey = "dummy_readable_001",
    })
    if not item then
        print("TWR.Debug: runReadable -- buildItem FAILED: " .. tostring(err))
        return
    end

    local okAdd = safeCall(inventory, "AddItem", item)
    if okAdd then
        pcall(function() sendAddItemToContainer(inventory, item) end)
    end
    print("TWR.Debug: runReadable -- gave dummy readable item " .. (okAdd and "SUCCEEDED -- right-click it and choose Read Note" or "FAILED"))
end

-- P2 (VHS/RecordedMedia) debug test -- gives a dummy DB-shaped VHS
-- tape directly to inventory. Right-click it -> "Watch Tape" (custom
-- TWR option, NOT vanilla's native Read/Watch -- see
-- Mechanics/RecordedMedia.lua header for why).
local function runRecordedMedia(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local item, err = TWR.Mechanics.RecordedMedia.buildItem({
        contentId = "dummy.vhs.001",
        mediaId = "TWR_DummyTape01",
        displayName = "Home Video",
        lines = { "Dummy line one.", "Dummy line two." },
        discoveryKey = "dummy_vhs_001",
    })
    if not item then
        print("TWR.Debug: runRecordedMedia -- buildItem FAILED: " .. tostring(err))
        return
    end

    local okAdd = safeCall(inventory, "AddItem", item)
    if okAdd then
        pcall(function() sendAddItemToContainer(inventory, item) end)
    end
    print("TWR.Debug: runRecordedMedia -- gave dummy VHS tape " .. (okAdd and "SUCCEEDED -- right-click it and choose Watch Tape" or "FAILED"))
end

-- P3 (controlled key) debug test -- gives a key with a fixed,
-- hardcoded-for-the-test keyId directly to the triggering admin.
-- Running this twice in a row should give two keys that are
-- INTERCHANGEABLE (same keyId), proving retry-stability -- unlike
-- Container.lockByKey/Door.lockToKey's own debug commands, which
-- generate a fresh random keyId every time.
local DUMMY_TEST_KEY_ID = 424242

local function runControlledKey(p)
    local item, err = TWR.Mechanics.Key.giveTo(p, DUMMY_TEST_KEY_ID, {
        displayName = "Dummy Test Key",
        contentId = "dummy.key.001",
    })
    print("TWR.Debug: runControlledKey -- " .. (item and ("gave key with keyId=" .. DUMMY_TEST_KEY_ID .. " SUCCEEDED (run again -- should give an interchangeable duplicate, not a new keyId)") or ("FAILED: " .. tostring(err))))
end

-- P4 -- dummy key->VHS->location->sleep integration fixture, per
-- antagonist/quest-db/quest-fixtures/dummy-key-vhs-location-sleep.md.
-- Covers KVLS-1 (one stable keyId reused for key+lock) and KVLS-2
-- (DB-shaped VHS creation) concretely, chaining P1-P3 above through
-- ALREADY-PROVEN Container.spawnBox/lockByKey (existing-world-test-
-- matrix.md TEST I/J). KVLS-3 onward (authoritative playback ->
-- quest-step advancement -> sleep consumes the stable location ->
-- exactly-one final reward, with restart/MP redelivery safety) is
-- deliberately NOT built here -- the fixture doc's own status line
-- says VHS creation "must be source/API checked and live-proven
-- before this fixture can become fully TEST_READY", and its "Required
-- engine vocabulary" section depends on the real quest dispatcher /
-- twr_jobs reconciliation design that doesn't exist yet. Faking that
-- with one-off state-tracking Lua here would violate the fixture
-- doc's own "do not patch the fixture with one-off Lua" rule. P2's
-- onMediaPlayed already fires (with discoveryKey="fixture_kvls_vhs_alpha")
-- the moment the matching tape is watched -- wiring that into a real
-- step advance is TODO once an engine exists to advance.
local FIXTURE_TEST_KEY_ID = 990001

local function runFixtureKVLS(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local tx, ty, tz = math.floor(x) + 3, math.floor(y), math.floor(z)

    -- Step 02: locked container holding Test VHS Alpha, key_id =
    -- test_key_id, unlock_policy=permanent (CustomLock -- matches
    -- quest-engine-extensibility.md's adopted pattern).
    local crate = TWR.Mechanics.Container.spawnBox(tx, ty, tz)
    if not crate then
        print("TWR.Debug: runFixtureKVLS -- Container.spawnBox FAILED")
        return
    end

    local vhs, vhsErr = TWR.Mechanics.RecordedMedia.buildItem({
        contentId = "fixture.media.vhs.alpha",
        mediaId = "TWR_TEST_VHS_ALPHA",
        displayName = "Test VHS Alpha",
        lines = {
            "TEST RECORDING",
            "Proceed to the designated test location.",
            "Sleep there to continue.",
            "END TEST RECORDING",
        },
        discoveryKey = "fixture_kvls_vhs_alpha",
    })
    if vhs then
        local okC, container = safeCall(crate, "getContainer")
        if okC and container then
            safeCall(container, "AddItem", vhs)
        end
    else
        print("TWR.Debug: runFixtureKVLS -- RecordedMedia.buildItem FAILED: " .. tostring(vhsErr))
    end

    TWR.Mechanics.Container.lockByKey(crate, nil, FIXTURE_TEST_KEY_ID)

    -- Step 01: place the matching test key. Given directly to the
    -- triggering admin for this manual debug run -- there's no real
    -- player-targeting/delivery system yet for a world-placed key
    -- (same open gap Key.resolvePendingAction's own comment flags).
    local key, keyErr = TWR.Mechanics.Key.giveTo(p, FIXTURE_TEST_KEY_ID, {
        displayName = "Test Key Alpha",
        contentId = "fixture.key.alpha",
    })

    print("TWR.Debug: runFixtureKVLS -- locked container+VHS at (" .. tx .. "," .. ty .. "," .. tz .. "), key_id=" .. FIXTURE_TEST_KEY_ID
        .. " " .. (key and "key given SUCCEEDED" or ("key FAILED: " .. tostring(keyErr)))
        .. " -- unlock with the key, take the tape, Watch Tape. KVLS-3 onward (quest-step advance / sleep / final reward) intentionally not wired, needs the real quest dispatcher.")
end

local function runCoords(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local text = "TWR coords: (" .. math.floor(x) .. "," .. math.floor(y) .. "," .. math.floor(z) .. ")"
    print("TWR.Debug: runCoords -- " .. text)
    safeCall(p, "Say", text)
end

local MECHANICS = {
    container = runContainer,
    scatter = runScatter,
    map_scatter = runMapScatter,
    pending_actions_status = runPendingActionsStatus,
    deferred_area = runDeferredArea,
    corpse = runCorpse,
    door = runDoor,
    door_unlock_permanent = runDoorUnlockPermanent,
    recipe = runRecipe,
    recipe_forget = runRecipeForget,
    recipe_note = runRecipeNote,
    readable = runReadable,
    recorded_media = runRecordedMedia,
    controlled_key = runControlledKey,
    fixture_kvls = runFixtureKVLS,
    -- map_reveal DISABLED SERVER-SIDE 2026-08-12 -- removing only the
    -- client-side button (client/TWR/Context/Debug.lua) was NOT enough:
    -- a client on a stale/not-yet-updated Workshop version still had
    -- the old button and could still trigger this. Confirmed live --
    -- user's map broke a SECOND time via exactly this path. Do not
    -- re-add until the suspected data-loss root cause (see URGENT/OPEN
    -- note in existing-world-test-matrix.md) is resolved.
    coords = runCoords,
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

-- Returns true/false instead of throwing. FIX 2026-08-13: previously
-- called TWR.Runtime.registerEventOnce directly and relied on pcall()
-- to catch the resulting "attempted index ... of non-table: null" --
-- correct, but noisy: every single boot logged a full ERROR-level Java
-- exception + stack trace dump (KahluaThread.flushErrorMessage) for
-- something entirely expected and self-healing (see the load-order
-- note below). Checking TWR.Runtime exists FIRST means the guaranteed-
-- to-fail-once-then-self-heal case never calls a method on nil, so it
-- no longer throws. A real error inside registerEventOnce itself would
-- still propagate normally (not silently swallowed like before).
local function init()
    if not (TWR.Runtime and TWR.Runtime.registerEventOnce) then
        return false
    end
    TWR.Runtime.registerEventOnce(TWR.Debug, "clientCommand", Events.OnClientCommand, onClientCommand)
    print("TWR.Debug: OnClientCommand handler registered")
    return true
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
    if init() then
        Events.EveryOneMinute.Remove(retryInit)
    end
end

if not init() then
    print("TWR.Debug: init deferred, retrying every minute (TWR.Runtime not loaded yet)")
    Events.EveryOneMinute.Add(retryInit)
end
