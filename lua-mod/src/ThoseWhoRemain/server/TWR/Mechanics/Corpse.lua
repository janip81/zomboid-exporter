-- TWR.Mechanics.Corpse -- spawns a PERMANENT corpse (not a merely
-- fake-dead zombie -- see the CONFIRMED distinction below) with a
-- controlled outfit and specific loot.
--
-- CONFIRMED live 2026-08-11 (AntagonistProbe TEST C, existing-world-
-- test-matrix.md "Permanent corpse with controlled outfit + specific
-- loot" row):
--   - addZombiesInOutfit(x,y,z,count,outfit,femaleChance) is the
--     CONFIRMED 6-arg form (client/Tutorial/Steps.lua's own usage) --
--     an earlier 9-positional-arg attempt threw a Kahlua "No
--     implementation found" RuntimeException; Kahlua's Java bridge does
--     strict per-overload type matching, nil trailing args don't match
--     any registered overload.
--   - zombie:setFakeDead(true) is NOT permanent -- confirmed live, the
--     zombie got back up on its own.
--   - zombie:setHealth(0) DOES permanently kill it -- confirmed live,
--     stayed dead.
--   - zombie:addItemToSpawnAtDeath(item), called BEFORE setHealth(0),
--     is a real working method (UNCONFIRMED by grep beforehand -- zero
--     hits anywhere in the installed tree -- but live-tested and
--     works). Preferred path.
--   - Fallback if addItemToSpawnAtDeath is unavailable: poll isDead(),
--     then look up the real IsoDeadBody via
--     square:getDeadBodys() (CONFIRMED real) and add loot to ITS
--     container instead of the zombie's own inventory (the loot UI
--     does not read the zombie's own getInventory() post-death --
--     confirmed live, a "cannot get ID for container: inventorymale"
--     warning and the item never appeared).
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. Guarding here matters more than in most
-- Mechanics files: this one registers a real EveryOneMinute watcher
-- (via spawnPermanentCorpse), which would be an active, wasteful
-- client-side side effect without this guard.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Corpse = TWR.Mechanics.Corpse or {}

local Corpse = TWR.Mechanics.Corpse

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- Generalizes AntagonistProbe's single pendingLootZombie/pendingLootSquare
-- globals into a list, so several corpse-spawn jobs can be pending loot
-- resolution concurrently.
Corpse.pending = Corpse.pending or {}

local function checkPendingLoot()
    if #Corpse.pending == 0 then return end

    local stillPending = {}
    for _, entry in ipairs(Corpse.pending) do
        local okDead, dead = safeCall(entry.zombie, "isDead")

        if not okDead or not dead then
            table.insert(stillPending, entry)
        elseif entry.handledAtSpawn then
            -- addItemToSpawnAtDeath already succeeded before death --
            -- CONFIRMED (2026-08-11) that also running the
            -- getDeadBodys() fallback here would double-add loot.
        elseif entry.square then
            local okBodies, bodies = safeCall(entry.square, "getDeadBodys")
            if okBodies and bodies then
                for i = 0, bodies:size() - 1 do
                    local body = bodies:get(i)
                    local okC, container = safeCall(body, "getContainer")
                    if okC and container then
                        for _, itemType in ipairs(entry.lootItems) do
                            safeCall(container, "AddItem", itemType)
                        end
                        break
                    end
                end
            end
        end
    end

    Corpse.pending = stillPending
end

-- Spawns a permanently-dead zombie corpse at (x, y, z) wearing outfit,
-- with lootItems (a list of item type strings) findable on the body.
-- femaleChance is 0-1, same as the confirmed vanilla usage.
function Corpse.spawnPermanentCorpse(x, y, z, outfit, femaleChance, lootItems)
    local okList, zombieList = pcall(function()
        return addZombiesInOutfit(x, y, z, 1, outfit, femaleChance or 0)
    end)
    if not okList or not zombieList then return false end

    local okZ0, zombie = pcall(function() return zombieList:get(0) end)
    if not okZ0 or not zombie then return false end

    local handledAtSpawn = true
    for _, itemType in ipairs(lootItems or {}) do
        local okItem, item = pcall(function() return instanceItem(itemType) end)
        local okAdd = okItem and item and safeCall(zombie, "addItemToSpawnAtDeath", item)
        if not okAdd then handledAtSpawn = false end
    end

    local okHealth = safeCall(zombie, "setHealth", 0)
    if not okHealth then return false end

    -- DIAGNOSTIC 2026-08-11: dedicated MP live report was "blood on the
    -- floor, no visible corpse" -- vanilla itself never calls
    -- zombie:setHealth(0) anywhere (grepped: only animal:setHealth(0)
    -- exists, via shared/TimedActions/Animals/ISKillAnimal.lua). No
    -- proven vanilla precedent that this triggers the same
    -- corpse-creation/sync path a real combat kill does. Checking
    -- whether a real IsoDeadBody actually exists server-side
    -- immediately after death, to tell apart "never created" from
    -- "created but not synced to the client".
    local okSqNow, squareNow = safeCall(zombie, "getCurrentSquare")
    if okSqNow and squareNow then
        local okDead, isDead = safeCall(zombie, "isDead")
        local okBodiesNow, bodiesNow = safeCall(squareNow, "getDeadBodys")
        print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- post-death check: isDead=" .. tostring(okDead and isDead) .. ", square getDeadBodys():size()=" .. tostring(okBodiesNow and bodiesNow and bodiesNow:size() or "?"))
    else
        print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- post-death check: getCurrentSquare() failed (okSqNow=" .. tostring(okSqNow) .. ")")
    end

    if not handledAtSpawn then
        local okSq, square = safeCall(zombie, "getCurrentSquare")
        table.insert(Corpse.pending, {
            zombie = zombie,
            square = okSq and square or nil,
            lootItems = lootItems or {},
            handledAtSpawn = false,
        })
    end

    TWR.Runtime.registerEventOnce(Corpse, "lootWatcher", Events.EveryOneMinute, checkPendingLoot)

    return true
end
