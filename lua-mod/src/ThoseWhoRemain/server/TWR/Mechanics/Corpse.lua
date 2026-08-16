-- TWR.Mechanics.Corpse -- spawns a PERMANENT corpse (not a merely
-- fake-dead zombie) at a specific (x, y, z), with a controlled outfit
-- and specific loot, visible immediately to already-connected dedicated
-- MP clients.
--
-- CONFIRMED live 2026-08-11 (SP only, AntagonistProbe TEST C):
--   - addZombiesInOutfit(x,y,z,count,outfit,femaleChance) is the
--     CONFIRMED 6-arg form (client/Tutorial/Steps.lua's own usage).
--   - zombie:setFakeDead(true) is NOT permanent -- confirmed live, the
--     zombie got back up on its own.
--
-- CONFIRMED FAIL live 2026-08-12 (dedicated MP): the original SP-proven
-- approach (spawn healthy, then zombie:setHealth(0)) left
-- isDead()==true but square:getDeadBodys():size()==0 -- no real corpse
-- was ever created server-side. Vanilla itself never calls
-- zombie:setHealth(0) anywhere (grepped: only animal:setHealth(0)
-- exists, via shared/TimedActions/Animals/ISKillAnimal.lua) -- there
-- was never a proven precedent this triggered the same
-- corpse-creation path a real combat kill does.
--
-- ROOT CAUSE + FIX, per ChatGPT's decompiled-Java research (B42.20.2)
-- pointing at GameServer.sendCorpse()/packet AddCorpseToMap, verified
-- against the installed Lua tree and CONFIRMED live 2026-08-12 (TEST
-- Q1): a genuinely different code path is required, not a variant of
-- setHealth(0). The real vanilla corpse-publish sequence, grepped from
-- shared/Definitions/animal/ButcheringUtil.lua (used twice there):
--   1. IsoDeadBody.new(entity, wasCorpseAlready) -- CONFIRMED 2-arg
--      form here (a 3-arg addToSquareAndWorld=true form exists and
--      technically creates a real getDeadBodys()-visible object too,
--      but is UNPROVEN by any vanilla usage and did NOT sync live in
--      testing -- do not use it).
--   2. body:setX/setY/setZ -- position it explicitly.
--   3. square:addCorpse(corpse, false) -- CONFIRMED real, and a
--      genuinely DIFFERENT square method than AddSpecialObject (which
--      Container.spawnBox uses for crates) -- AddSpecialObject did NOT
--      sync a corpse live in testing, addCorpse does.
--   4. corpse:invalidateCorpse() -- CONFIRMED real, always paired with
--      addCorpse/sendCorpse in both real usages.
--   5. corpse:setInvalidateNextRender(true) -- CONFIRMED real, same.
--   6. entity:remove() -- CONFIRMED real -- removes the original
--      (still-alive) zombie once its dead body exists.
--   7. sendCorpse(corpse) -- CONFIRMED real, a BARE GLOBAL function
--      (NOT GameServer.sendCorpse -- that prefix appears nowhere in
--      the installed tree), called LAST, server-only in vanilla's own
--      usage. LIVE CONFIRMED 2026-08-12: corpse appeared to an
--      already-connected client IMMEDIATELY after this call, no
--      reconnect needed -- sendBecomeCorpse/:becomeCorpse( were also
--      checked and do NOT exist anywhere, ruling out that half of the
--      original hypothesis.
--
-- Loot: the old addItemToSpawnAtDeath()-before-death approach relied on
-- native death processing we no longer trigger (we build the dead body
-- directly from a still-alive zombie, we never actually kill it via the
-- native path). Since step 1 above gives us a real IsoDeadBody
-- synchronously, loot is added directly to body:getContainer() instead
-- -- simpler than the old deferred isDead()-polling watcher, and no
-- longer needs one at all.
--
-- OUTFIT FIX 2026-08-12 (CONFIRMED, 2 rounds): the original approach
-- (relying solely on addZombiesInOutfit's own outfit parameter) left
-- corpses naked on dedicated MP despite outfit being correctly
-- specified. Round 1 -- setDressInRandomOutfit(false) +
-- dressInNamedOutfit(outfit) + resetModelNextFrame() on the zombie
-- before conversion (matching the real vanilla precedent in
-- client/Tutorial/Steps.lua) -- still spawned naked live. Round 2 added
-- zombie:DoZombieInventory(), the one call round 1 deliberately omitted
-- (worried it would conflict with the controlled lootItems below) --
-- CONFIRMED LIVE: corpse now spawns visually dressed in the correct
-- outfit, with loot still correct and separate. DoZombieInventory()
-- is apparently what actually equips the worn clothing model, not just
-- loot -- dressInNamedOutfit() alone only selects the definition.
--
-- WORN/AGED LOOK FIX 2026-08-26: corpses spawned visually dressed
-- (per the OUTFIT FIX above) but in pristine, brand-new-looking clothes
-- -- not the dirty/bloodied look a real zombie corpse should have.
-- Root cause: dressInNamedOutfit()/DoZombieInventory() only select and
-- equip the outfit definition; they don't apply any wear. Real vanilla
-- corpse-creation call sites (client/Tutorial/Steps.lua's
-- SneakStep/BandageStep, grepped) all call zombie:addBlood(nil, false,
-- true, false) and zombie:addHole(nil) 7-16 times each, right after
-- dressing and BEFORE IsoDeadBody.new() -- this is vanilla's own real
-- mechanism for the aged/bloodied look, applied to the model (which
-- carries through to the worn clothing layer), not a clothing-condition
-- field. Added the same calls below.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug.
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

-- Spawns a permanently-dead zombie corpse at (x, y, z) wearing outfit
-- (see KNOWN OPEN ISSUE above -- outfit is not yet proven to apply),
-- with lootItems (a list of item type strings) added directly to the
-- corpse's real container. femaleChance is 0-1, same as the confirmed
-- vanilla usage. Returns true/false.
function Corpse.spawnPermanentCorpse(x, y, z, outfit, femaleChance, lootItems)
    local okCell, cell = pcall(function() return getCell() end)
    if not okCell or not cell then return false end

    local okSq, square = pcall(function() return cell:getGridSquare(x, y, z) end)
    if not okSq or not square then return false end

    local okList, zombieList = pcall(function()
        return addZombiesInOutfit(x, y, z, 1, outfit, femaleChance or 0)
    end)
    if not okList or not zombieList then return false end

    local okZ0, zombie = pcall(function() return zombieList:get(0) end)
    if not okZ0 or not zombie then return false end

    -- Explicit controlled-outfit dressing + visual refresh -- CONFIRMED
    -- real via grep, client/Tutorial/Steps.lua (used right before its
    -- own IsoDeadBody.new(zombie, false) call, same conversion we do
    -- below): setDressInRandomOutfit(false) + dressInNamedOutfit(name)
    -- is the real controlled (non-random) outfit API. Round 1 (2026-08-12)
    -- of this fix omitted DoZombieInventory(), assuming it was purely
    -- about loot items (worried about conflicting with our own
    -- controlled lootItems below) -- CONFIRMED STILL NAKED live despite
    -- setDressInRandomOutfit/dressInNamedOutfit/resetModelNextFrame all
    -- running with no errors. "Police" itself is NOT gender-restricted
    -- (grepped shared/NPCs/ZombiesZoneDefinition.lua -- unlike
    -- OfficeWorkerSkirt/OfficeWorker in that same table, which DO carry
    -- an explicit gender= field -- so gender mismatch is not the cause
    -- here). Round 2 added DoZombieInventory() back -- it turns out to be
    -- the step that actually EQUIPS the worn clothing model, not just
    -- loot; dressInNamedOutfit() alone only selects the definition.
    -- CONFIRMED LIVE 2026-08-12: corpse now spawns visually dressed
    -- correctly, loot still correct and separate.
    safeCall(zombie, "setDressInRandomOutfit", false)
    safeCall(zombie, "dressInNamedOutfit", outfit)
    safeCall(zombie, "resetModelNextFrame")
    safeCall(zombie, "DoZombieInventory")

    -- Worn/aged look -- real vanilla precedent (client/Tutorial/Steps.lua),
    -- see file header. Applied to the model before IsoDeadBody.new(),
    -- matching every real usage found.
    for _ = 1, 10 do
        safeCall(zombie, "addBlood", nil, false, true, false)
        safeCall(zombie, "addHole", nil)
    end

    -- ROUND 2 of the worn/aged fix, 2026-08-26: CONFIRMED LIVE the
    -- addBlood/addHole calls above alone were NOT enough -- clothes
    -- still looked brand new. addBlood/addHole are a skin/gore effect;
    -- they don't touch a clothing item's own Condition, which is the
    -- separate per-item attribute that actually drives vanilla's own
    -- rip/tear clothing sprite states (confirmed real API:
    -- item:getConditionMax()/item:setCondition(n), e.g.
    -- shared/Items/OnBreak.lua's own newItem:setCondition(ZombRand(
    -- newItem:getConditionMax())+1)). dressInNamedOutfit()/
    -- DoZombieInventory() only select+equip the outfit at full/default
    -- condition -- they apply no wear at all. Explicitly lowering each
    -- worn item's condition to a random low fraction of its max here.
    -- DIAGNOSTIC 2026-08-26: round 2 (setCondition on worn items) was
    -- CONFIRMED LIVE to still look pristine. Before guessing a third
    -- time, print exactly what getWornItems() actually returns and
    -- whether the condition mutation sticks, so the next fix targets
    -- the real cause instead of another blind guess.
    local okWorn, wornItems = safeCall(zombie, "getWornItems")
    print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- getWornItems() ok=" .. tostring(okWorn) .. " wornItems=" .. tostring(wornItems))
    if okWorn and wornItems then
        local okSize, size = safeCall(wornItems, "size")
        print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- wornItems:size() ok=" .. tostring(okSize) .. " size=" .. tostring(size))
        if okSize then
            for i = 0, size - 1 do
                local okItem, item = safeCall(wornItems, "get", i)
                if okItem and item then
                    local okName, name = safeCall(item, "getName")
                    local okCondBefore, condBefore = safeCall(item, "getCondition")
                    local okMax, maxCond = safeCall(item, "getConditionMax")
                    local worn = nil
                    if okMax and maxCond and maxCond > 0 then
                        worn = ZombRand(math.floor(maxCond * 0.4)) + 1
                        safeCall(item, "setCondition", worn)
                    end
                    local okCondAfter, condAfter = safeCall(item, "getCondition")
                    print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- item[" .. i .. "]=" .. tostring(okName and name or "?")
                        .. " conditionBefore=" .. tostring(okCondBefore and condBefore or "?")
                        .. " conditionMax=" .. tostring(okMax and maxCond or "?")
                        .. " setTo=" .. tostring(worn)
                        .. " conditionAfter=" .. tostring(okCondAfter and condAfter or "?"))
                else
                    print("TWR.Mechanics.Corpse: spawnPermanentCorpse -- item[" .. i .. "] getItem FAILED ok=" .. tostring(okItem))
                end
            end
        end
    end

    local okBody, body = pcall(function() return IsoDeadBody.new(zombie, false) end)
    if not okBody or not body then return false end

    safeCall(body, "setX", x + 0.5)
    safeCall(body, "setY", y + 0.5)
    safeCall(body, "setZ", z)

    local okAdd = pcall(function() square:addCorpse(body, false) end)
    if not okAdd then return false end

    safeCall(body, "invalidateCorpse")
    safeCall(body, "setInvalidateNextRender", true)
    safeCall(zombie, "remove")

    local okContainer, container = safeCall(body, "getContainer")
    if okContainer and container then
        for _, itemType in ipairs(lootItems or {}) do
            safeCall(container, "AddItem", itemType)
        end
    end

    pcall(function() sendCorpse(body) end)

    return true
end
