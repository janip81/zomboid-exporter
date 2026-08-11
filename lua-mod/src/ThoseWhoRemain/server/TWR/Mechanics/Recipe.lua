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
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
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
    return ok
end
