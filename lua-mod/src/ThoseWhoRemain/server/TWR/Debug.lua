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

-- Avoids the classic Lua `ok and v or "?"` ternary bug -- collapses a
-- legitimate `false` result into the same fallback as a failed call,
-- same bug class already found and fixed once in
-- client/TWR/Context/Debug.lua's own describe() helper.
local function describe(ok, v)
    if not ok then return "CALL FAILED" end
    return tostring(v)
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

-- Spawns one of each Container.BOX_TYPES in a row (2 tiles apart, north
-- of the caller) for a direct visual side-by-side comparison -- built
-- after container_sprite_probe research confirmed real sprite names
-- for wardrobe/dresser/shelves/an alternate outdoor crate variant (see
-- Container.lua's BOX_TYPES header for the full research trail).
local function runBoxTypeShowcase(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local bx, by, bz = math.floor(x), math.floor(y) - 2, math.floor(z)
    local offset = 0
    for boxType, def in pairs(TWR.Mechanics.Container.BOX_TYPES) do
        local tx = bx + offset
        local crate = TWR.Mechanics.Container.spawnBox(tx, by, bz, boxType)
        if crate then
            TWR.Mechanics.Container.finalizeSpawn(crate)
            print("TWR.Debug: runBoxTypeShowcase -- boxType=" .. boxType .. " (" .. def.name .. ") spawned at (" .. tx .. "," .. by .. "," .. bz .. ")")
        else
            print("TWR.Debug: runBoxTypeShowcase -- boxType=" .. boxType .. " FAILED at (" .. tx .. "," .. by .. "," .. bz .. ")")
        end
        offset = offset + 2
    end
end

-- TEST K re-verify: padlock-lock path specifically (Container.lockByPadlock),
-- not yet re-clicked through with the fixed spawnBox() this session --
-- TEST J's key-lock path was already re-confirmed live via the P4 KVLS
-- fixture (Container.lockByKey), and TEST L's combination-lock path via
-- runContainer above, but nothing here has exercised lockByPadlock
-- specifically since the spawnBox fix.
local function runContainerPadlock(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local bx, by, bz = math.floor(x) + 1, math.floor(y) + 1, math.floor(z)
    print("TWR.Debug: runContainerPadlock -- player at (" .. tostring(x) .. "," .. tostring(y) .. "," .. tostring(z) .. "), target (" .. bx .. "," .. by .. "," .. bz .. ")")
    local crate = TWR.Mechanics.Container.spawnBox(bx, by, bz)
    if not crate then
        print("TWR.Debug: runContainerPadlock -- spawnBox failed")
        return
    end
    local okC, container = safeCall(crate, "getContainer")
    if okC and container then
        safeCall(container, "AddItem", "Base.Twigs")
    end
    local keyId = TWR.Mechanics.Container.lockByPadlock(crate, p, nil)
    TWR.Mechanics.Container.finalizeSpawn(crate)
    print("TWR.Debug: runContainerPadlock -- box spawned, padlock-locked (keyId=" .. tostring(keyId) .. "), matching padlock given to inventory, contains a twig")
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

-- Gate 1 Phase 6 -- minimal admin Inspect/Retry slice (per
-- pending-job-durability.md/twr_admin_recovery.md's own "first
-- implementation priority" scoping). Both are narrower than the
-- original design docs describe: Lua has no read/write path into
-- Postgres at all (getFileReader only reads local files -- confirmed
-- throughout this project, see quest-job-dispatch-transport-
-- chatgpt-response.md), so a real "look up job_id X's twr_jobs/
-- twr_job_attempts/twr_world_artifacts row and requeue it" pair isn't
-- buildable from the debug menu. What IS genuinely useful from here:
--
--   Inspect -- dump QuestEngine's own durable seen-jobs ledger (the
--   one thing Lua knows for certain, added 2026-08-15 alongside the
--   duplicate-spawn fix).
--   Retry -- force an immediate QuestEngine.pollDispatch() instead of
--   waiting for the next EveryOneMinute tick -- the exporter's own
--   qdRedispatchStaleLeases already retries a stuck DISPATCHED job
--   automatically every 60s, so this is a manual "don't make me wait"
--   button, not a new retry mechanism.
--
-- Real per-job_id Postgres inspection/requeue is done directly via
-- psql (see antagonist/quest-db/quest-fixtures/kvls-gate1-*.sql for
-- the established pattern) -- not a Lua debug-menu concern.
local function runQuestJobLedgerStatus(p)
    TWR.QuestEngine.reportLedgerStatus()
end

local function runQuestForcePoll(p)
    print("TWR.QuestEngine: runQuestForcePoll -- forcing an immediate pollDispatch()")
    TWR.QuestEngine.pollDispatch()
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
    if okAdd and item then
        pcall(function() sendAddItemToContainer(inventory, item) end)
        -- ROOT-CAUSED 2026-08-19: the item's script-level `LearnedRecipes`
        -- field does NOT teach anything on read -- SOURCE-CONFIRMED,
        -- shared/TimedActions/ISReadABook.lua:complete() only reads that
        -- list to special-case the Herbalist trait (line ~333); the real
        -- grant path a few lines later is
        -- `self.character:learnRecipe(self.item:getModData().learnedRecipe)`
        -- -- a single-recipe MODDATA field, completely separate from the
        -- script property. Vanilla's complete() already does its own
        -- sendSyncPlayerFields(0x00000007) right after, so no extra sync
        -- call is needed here once modData.learnedRecipe is set.
        local okData, modData = safeCall(item, "getModData")
        if okData and modData then
            modData.learnedRecipe = "AntagonistProbeTestRecipe"
        end
    end
    print("TWR.Debug: runRecipeNote -- gave ThoseWhoRemain.RecipeNote to inventory " .. (okAdd and item and "SUCCEEDED -- right-click it and choose Read" or "FAILED"))
end

-- antagonist/recipes/calendar.md's production chain, section 4 of the
-- Flyer/Calendar/Recipe presentation-layer work: real recipe clue ->
-- read -> MakePaperCalendar known -> appears in vanilla crafting UI.
-- Do NOT use TWR.Mechanics.Recipe.teach() (runRecipe above) for this --
-- the design doc explicitly requires proving the readable-clue path.
local function runCalendarRecipeNote(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    -- ROOT-CAUSED 2026-08-19 (item never found): two separate module
    -- blocks in one .txt file only registered the first -- split
    -- MakePaperCalendar into its own twr_recipes_calendar.txt. THEN
    -- found a second, unrelated bug: a "//" comment immediately inside
    -- a module body (between two item{} blocks) is not safely parsed
    -- ("unknown script object '//'" WARN) and eats the next item
    -- declaration -- removed the in-block comment above this item in
    -- twr_items.txt. Both real bugs, both fixed.
    --
    -- ROOT-CAUSED 2026-08-19 (item found, but reading it taught
    -- nothing): the item's script-level `LearnedRecipes` field does NOT
    -- grant anything on read -- SOURCE-CONFIRMED,
    -- shared/TimedActions/ISReadABook.lua:complete() only reads that
    -- list to special-case the Herbalist trait; the real grant path is
    -- `self.character:learnRecipe(self.item:getModData().learnedRecipe)`,
    -- a single-recipe MODDATA field entirely separate from the script
    -- property. Vanilla's complete() already calls
    -- sendSyncPlayerFields(0x00000007) right after, so no extra sync
    -- call is needed here once modData.learnedRecipe is set.
    local okAdd, item = safeCall(inventory, "AddItem", "ThoseWhoRemain.PaperCalendarNote")
    if okAdd and item then
        pcall(function() sendAddItemToContainer(inventory, item) end)
        local okData, modData = safeCall(item, "getModData")
        if okData and modData then
            modData.learnedRecipe = "MakePaperCalendar"
        end
    end
    print("TWR.Debug: runCalendarRecipeNote -- gave ThoseWhoRemain.PaperCalendarNote to inventory " .. (okAdd and item and "SUCCEEDED -- right-click it and choose Read" or "FAILED (okAdd=" .. tostring(okAdd) .. " item=" .. tostring(item) .. ")"))
end

-- Testing convenience only -- gives the exact MakePaperCalendar
-- ingredients/tools (twr_recipes.txt) directly to inventory so the
-- crafting step can be tested without a materials scavenger hunt.
local function runCalendarCraftMaterials(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local items = { "Base.Notebook", "Base.Scotchtape", "Base.Pen", "Base.Scissors" }
    local results = {}
    for _, itemType in ipairs(items) do
        local okAdd, item = safeCall(inventory, "AddItem", itemType)
        if okAdd and item then
            pcall(function() sendAddItemToContainer(inventory, item) end)
        end
        table.insert(results, itemType .. "=" .. (okAdd and item and "OK" or "FAILED"))
    end
    print("TWR.Debug: runCalendarCraftMaterials -- " .. table.concat(results, ", "))
end

-- Flyer presentation-profile debug test -- gives a dummy DB-shaped
-- flyer directly to inventory (see server/TWR/Mechanics/Flyer.lua and
-- antagonist/tests/vanilla-flyer-source-trace.md). Right-click -> the
-- literal vanilla "Inspect" option should appear (printMedia modData
-- is set), and the reveal-on-map button should center on the real
-- verified-via-PZmap "The McCoy Logging Corp." location (x=10317,
-- y=9290, cross-checked against vanilla's own
-- PrintMediaDefinitions.MiscDetails.mccoyloggingcorp rectangle -- not
-- an invented coordinate).
local function runFlyer(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local item, err = TWR.Mechanics.Flyer.buildItem({
        contentId = "dummy.flyer.001",
        displayName = "Missing Cat",
        text = "Have you seen Whiskers? Last seen near the logging corp. Reward if found.",
        discoveryKey = "dummy_flyer_001",
        locationRef = { x1 = 10260, y1 = 9220, x2 = 10419, y2 = 9479 },
    })
    if not item then
        print("TWR.Debug: runFlyer -- buildItem FAILED: " .. tostring(err))
        return
    end

    local okAdd = safeCall(inventory, "AddItem", item)
    if okAdd then
        pcall(function() sendAddItemToContainer(inventory, item) end)
    end
    print("TWR.Debug: runFlyer -- gave dummy flyer item " .. (okAdd and "SUCCEEDED -- right-click it, should say Inspect" or "FAILED"))
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
-- tape directly to inventory. No direct "watch" shortcut anymore --
-- removed 2026-08-13 per Jani: a real VHS must require a real TV/VCR,
-- like vanilla. This just proves item creation/give works; the actual
-- watch path is not wired to anything yet -- see
-- antagonist/tests/vhs-device-research.md.
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
    print("TWR.Debug: runRecordedMedia -- gave dummy VHS tape " .. (okAdd and "SUCCEEDED (no watch path wired yet -- needs a real TV)" or "FAILED"))
end

-- VHS-LINES-1 decisive probe (antagonist/tests/
-- vhs-live-handoff-chatgpt-response.md). Gives a tape bound to
-- twr.native.lines.test.001 (see shared/TWR/RecordedMediaRegistry.lua
-- -- registered with real addLine() content). Insert+play through the
-- REAL vanilla TV UI ONLY -- no ISMediaInfo, no TWR.Context.watchTape,
-- no TWR overlay of any kind (there isn't one to use anyway, already
-- removed). PASS = "TWR NATIVE LINE ONE"/"TWR NATIVE LINE TWO" appear
-- as native in-world scrolling captions, same presentation as a real
-- vanilla tape.
local function runVHSLinesTest(p)
    local okInv, inventory = safeCall(p, "getInventory")
    if not okInv or not inventory then return end

    local item, err = TWR.Mechanics.RecordedMedia.buildItem({
        contentId = "twr.native.lines.test.001",
        mediaId = "TWR_NATIVE_LINES_TEST_001",
        displayName = "TWR Native Lines Test",
        lines = { "TWR NATIVE LINE ONE", "TWR NATIVE LINE TWO" },
        discoveryKey = "twr_native_lines_test_001",
    })
    if not item then
        print("TWR.Debug: runVHSLinesTest -- buildItem FAILED: " .. tostring(err))
        return
    end

    local okAdd = safeCall(inventory, "AddItem", item)
    if okAdd then
        pcall(function() sendAddItemToContainer(inventory, item) end)
    end
    print("TWR.Debug: runVHSLinesTest -- gave VHS-LINES-1 test tape " .. (okAdd and "SUCCEEDED -- insert into a REAL TV via the normal vanilla UI, turn it on, press Play. Watch for native scrolling captions." or "FAILED"))
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

    -- FIX 2026-08-13, found live: this call was missing entirely.
    -- Per Container.finalizeSpawn()'s own header comment, it must be
    -- called exactly once, after filling (AddItem) and locking are
    -- done, to push the one full consistent snapshot to connected
    -- clients -- without it, the crate can exist and pass the
    -- server-side existence check while never actually being
    -- transmitted to any client, i.e. invisible in-game despite being
    -- completely real server-side.
    TWR.Mechanics.Container.finalizeSpawn(crate)

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
        .. " -- unlock with the key, take the tape (no direct watch path -- needs a real TV, see vhs-device-research.md). KVLS-3 onward (quest-step advance / sleep / final reward) intentionally not wired, needs the real quest dispatcher.")
end

-- Read-only research probe for the "nicer boxes for quests" request
-- (Container.spawnBox currently hardcodes sprite="carpentry_01_19", a
-- plain wooden crate -- ISWoodenContainer/ISSimpleFurniture's :new()
-- accepts any sprite string, so real vanilla furniture sprites (filing
-- cabinets, military crates, footlockers, etc.) are usable the same
-- way, but the exact real sprite tile NAMES aren't discoverable from
-- source alone -- grepped vanilla source only ever passes
-- "carpentry_01_19" through this exact class. Scans nearby squares for
-- any object with a real container and reports object name + sprite
-- name + container:getType() (the same "type" ContainerButtonIcons.lua
-- keys off) so real candidates (cabinet/wardrobe/dresser/
-- filingcabinet/militarycrate/etc.) can be identified from an actual
-- world instance. Does not create, insert, lock, or otherwise touch
-- anything.
local CONTAINER_PROBE_RADIUS = 6

-- Checks a single (square, listAccessorName) pair, incrementing `found`
-- (passed by table so the caller's total survives across both accessor
-- calls) for each object found with a real container.
local function scanSquareObjectList(square, listAccessorName, cx, cy, cz, foundRef)
    local okObjs, objects = safeCall(square, listAccessorName)
    if not (okObjs and objects) then return end
    for i = 0, objects:size() - 1 do
        local obj = objects:get(i)
        local okC, container = safeCall(obj, "getContainer")
        if okC and container then
            foundRef.n = foundRef.n + 1
            local okName, name = safeCall(obj, "getObjectName")
            local okSprite, sprite = safeCall(obj, "getSprite")
            local spriteName = nil
            if okSprite and sprite then
                local okSN, sn = safeCall(sprite, "getName")
                spriteName = describe(okSN, sn)
            end
            local okType, ctype = safeCall(container, "getType")
            local okCap, capacity = safeCall(container, "getCapacity")
            print("TWR.Debug: runContainerSpriteProbe -- [" .. foundRef.n .. "] square=(" .. cx .. "," .. cy .. "," .. cz .. ")"
                .. " via=" .. listAccessorName
                .. " objectName=" .. describe(okName, name)
                .. " sprite=" .. tostring(spriteName)
                .. " containerType=" .. describe(okType, ctype)
                .. " capacity=" .. describe(okCap, capacity))
        end
    end
end

local function runContainerSpriteProbe(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    local okZ, z = safeCall(p, "getZ")
    if not (okX and okY and okZ) then return end

    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then return end

    -- Scans BOTH getObjects() (normal world/map-placed furniture) AND
    -- getSpecialObjects() -- CONFIRMED real, distinct accessor per
    -- Container.spawnBox's own header/TEST N: objects added via
    -- square:AddSpecialObject() (like our own spawned crates) do NOT
    -- show up in getObjects(), only getSpecialObjects(). Missing this
    -- the first time this probe ran was why a just-spawned crate
    -- reported 0 containers found.
    local foundRef = { n = 0 }
    for dx = -CONTAINER_PROBE_RADIUS, CONTAINER_PROBE_RADIUS do
        for dy = -CONTAINER_PROBE_RADIUS, CONTAINER_PROBE_RADIUS do
            local okSq, square = pcall(function() return cell:getGridSquare(math.floor(x) + dx, math.floor(y) + dy, math.floor(z)) end)
            if okSq and square then
                local cx, cy, cz = math.floor(x) + dx, math.floor(y) + dy, math.floor(z)
                scanSquareObjectList(square, "getObjects", cx, cy, cz, foundRef)
                scanSquareObjectList(square, "getSpecialObjects", cx, cy, cz, foundRef)
            end
        end
    end

    print("TWR.Debug: runContainerSpriteProbe -- scan complete, radius=" .. CONTAINER_PROBE_RADIUS .. ", found " .. foundRef.n .. " container(s)")
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

-- MAP-SAFE-2 (worldmap-visited-bytecode-chatgpt-review.md) manual
-- trigger: reveals a 150-tile-radius (300x300) square OFFSET only 250
-- tiles north of the calling admin's own current position, via the
-- rebuilt client-first flow (server/TWR/Mechanics/MapReveal.lua ->
-- client/TWR/Context/MapReveal.lua).
--
-- Tuning history (all CONFIRMED live 2026-08-26): a reveal centered
-- exactly on the caller's own position was indistinguishable from
-- ground already explored just by standing there ("looks like it...
-- dont see any differance"). A 500-tile offset was too far -- CONFIRMED
-- the server->client command still fired and both the local
-- WorldMapVisited mutation and the real vanilla map/setKnownInSquares
-- forward succeeded (client log showed the exact success line), yet
-- nothing was visible even after zooming the map out fully ("nothing
-- does not reveal any more") -- most likely that offset landed off the
-- populated map (ocean/void has no visible known-vs-unknown contrast
-- either way) or simply outside the default map viewport. 250 tiles
-- (single axis, north only, not diagonal) keeps this near the caller's
-- existing explored area -- same neighborhood, much likelier to be real
-- populated terrain -- while still clearing whatever was organically
-- explored just by standing still.
--
-- Also now tells the caller the exact target coordinates via chat
-- (matching runCoords' own pattern) so there's no ambiguity about where
-- to look on the map.
local function runMapReveal(p)
    local okX, x = safeCall(p, "getX")
    local okY, y = safeCall(p, "getY")
    if not (okX and okY) then
        print("TWR.Debug: runMapReveal -- could not read caller position")
        return
    end
    local cx, cy = math.floor(x), math.floor(y) - 250
    local ok, err = TWR.Mechanics.MapReveal.revealAroundPoint(p, cx, cy, 150)
    if not ok then
        print("TWR.Debug: runMapReveal -- FAILED: " .. tostring(err))
        return
    end
    safeCall(p, "Say", "TWR map reveal target: (" .. cx .. "," .. cy .. ") radius 150 -- look 250 tiles north of here")
    print("TWR.Debug: runMapReveal -- requested reveal around (" .. cx .. "," .. cy .. ") radius 150")
end

local MECHANICS = {
    container = runContainer,
    box_type_showcase = runBoxTypeShowcase,
    container_padlock = runContainerPadlock,
    scatter = runScatter,
    map_scatter = runMapScatter,
    pending_actions_status = runPendingActionsStatus,
    quest_job_ledger_status = runQuestJobLedgerStatus,
    quest_force_poll = runQuestForcePoll,
    deferred_area = runDeferredArea,
    corpse = runCorpse,
    door = runDoor,
    door_unlock_permanent = runDoorUnlockPermanent,
    recipe = runRecipe,
    recipe_forget = runRecipeForget,
    recipe_note = runRecipeNote,
    calendar_recipe_note = runCalendarRecipeNote,
    calendar_craft_materials = runCalendarCraftMaterials,
    flyer = runFlyer,
    readable = runReadable,
    recorded_media = runRecordedMedia,
    controlled_key = runControlledKey,
    fixture_kvls = runFixtureKVLS,
    vhs_lines_test = runVHSLinesTest,
    -- map_reveal RE-ENABLED 2026-08-15 -- rebuilt as a vanilla-
    -- equivalent client-first flow after the additive-only bytecode
    -- confirmation + real vanilla call-site trace (see
    -- server/TWR/Mechanics/MapReveal.lua's header for the full
    -- research trail). Still MAP-SAFE-1..6 gated before any further
    -- promotion -- see worldmap-visited-bytecode-chatgpt-review.md.
    map_reveal = runMapReveal,
    container_sprite_probe = runContainerSpriteProbe,
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
