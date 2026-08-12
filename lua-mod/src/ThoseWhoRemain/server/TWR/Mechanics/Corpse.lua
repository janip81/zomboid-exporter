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
-- KNOWN OPEN ISSUE (tracked separately, not a sync problem): the
-- resulting corpse spawns NAKED regardless of the outfit argument --
-- outfit definitions are compiled binary asset data, not present in
-- any Lua/script-accessible file, so this needs a different
-- investigation approach than grep. Do not assume outfit works until
-- this is separately proven.
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
