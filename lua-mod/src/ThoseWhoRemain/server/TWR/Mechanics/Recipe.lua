-- TWR.Mechanics.Recipe -- instantly teaches a player a predefined
-- recipe, bypassing B42's "Research" discovery gate.
--
-- CONFIRMED live 2026-08-11 (AntagonistProbe TEST D, round 3):
-- character:learnRecipe(recipeName) is the real grant API -- traced
-- from vanilla's own shared/TimedActions/ISReadABook.lua:complete(),
-- which calls self.character:learnRecipe(self.item:getModData().learnedRecipe).
-- CONFIRMED wrong: getKnownRecipes():add() does NOT make a new-style
-- craftRecipe visible in the crafting menu, despite succeeding silently
-- -- tested twice, genuinely does nothing observable.
--
-- The recipe itself must still exist as a real craftRecipe script
-- definition (module/recipe name), loaded via a full restart (RESTART-
-- SAFE, not F11-reloadable) -- this function only performs the runtime
-- grant, matching the confirmed "a clue instantly teaches this recipe"
-- production need, bypassing whatever discovery gate (e.g. B42's new
-- "Research" mechanic) the recipe would otherwise require.
--
-- FIX 2026-08-12 (round 1): scripts/twr_recipes.txt was first ported
-- using `module ThoseWhoRemain` (matching this mod's own
-- item-definition convention) instead of the exact `module Base` the
-- original AntagonistProbe TEST D used when it was confirmed working
-- live ("craftable... got a plank"). learnRecipe() still succeeded
-- silently either way (no error to signal the mismatch), but nothing
-- appeared in the crafting menu -- switched twr_recipes.txt back to
-- `module Base` to match the only precedent actually proven live.
-- STILL CONFIRMED FAIL after the module fix AND after a full relog --
-- ruling out both a module mismatch and a simple client-cache/sync
-- gap (relogging always fully resyncs everything else this session).
--
-- FIX 2026-08-12 (round 2, real root cause): ChatGPT's own CGPT-021
-- review (probe-code/ROUND2-REVIEW.md) had already flagged this exact
-- gap and pointed at the fix, which was never actually applied here --
-- grepped CONFIRMED real, shared/TimedActions/ISResearchRecipe.lua's
-- own :complete(): after granting recipes (via
-- scriptItem:researchRecipes(character), a different call than our
-- learnRecipe() but same effect on the known-recipes set), it ALWAYS
-- follows with `sendSyncPlayerFields(character, 0x00000001)` (the
-- comment right above it literally says "--PF_Recipes") -- a required
-- explicit sync push, without which the client's known-recipes state
-- silently never updates. learnRecipe() alone was never enough on
-- dedicated MP -- exact same class of bug as spawnBox/corpse/scatter
-- this whole session, just a different subsystem.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. This file has no load-time side effects
-- (pure function definitions), but guarding anyway for consistency and
-- so a client never pointlessly parses/executes it.
if isClient() then return end

TWR = TWR or {}
TWR.Mechanics = TWR.Mechanics or {}
TWR.Mechanics.Recipe = TWR.Mechanics.Recipe or {}

local Recipe = TWR.Mechanics.Recipe

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

function Recipe.teach(player, recipeName)
    local ok = safeCall(player, "learnRecipe", recipeName)
    -- CONFIRMED real, grepped shared/TimedActions/ISResearchRecipe.lua's
    -- own :complete() -- 0x00000001 is PF_Recipes (per that file's own
    -- comment). Required to push the updated known-recipes state to an
    -- already-connected dedicated MP client -- learnRecipe() alone only
    -- updates server-side state.
    pcall(function() sendSyncPlayerFields(player, 0x00000001) end)
    return ok
end
