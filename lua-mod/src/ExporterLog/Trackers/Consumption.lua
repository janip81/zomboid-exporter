-- ExporterLog.Consumption -- eating, drinking (item + world source),
-- pills/medicine, and smoking. Grouped together because they're all
-- monkey-patched TimedAction.complete hooks sharing the same
-- reload-safe pattern (ExporterLog.Runtime.hookTimedActionOnce).
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why. Every ExporterLog.Runtime/Emit/Utils
-- reference below is a fresh lookup at actual call time -- including
-- inside the extractX functions, since those run much later (when the
-- player actually eats/drinks/etc), not at this file's load time, but
-- they're defined as closures here so a stale cached local would
-- still break them for this entire load pass if captured too early.
ExporterLog = ExporterLog or {}
ExporterLog.Consumption = ExporterLog.Consumption or {}
ExporterLog.Consumption.originals = ExporterLog.Consumption.originals or {}

local Consumption = ExporterLog.Consumption

-- Real fluid TYPE (e.g. "Water", "Beer", "Vodka"), not just "it's a
-- liquid". Fails safe to nil if the container is missing/empty/errors.
-- Only used within this module (all drink variants live here), so it
-- stays local rather than moving to Utils.lua.
local function getFluidTypeString(fluidContainer)
    if not fluidContainer then return nil end
    local ok, primaryFluid = pcall(function() return fluidContainer:getPrimaryFluid() end)
    if not ok or not primaryFluid then return nil end
    local ok2, typeStr = pcall(function() return primaryFluid:getFluidTypeString() end)
    if ok2 then return typeStr end
    return nil
end

local function extractDrinkFluid(self)
    -- self.fluidContainer confirmed real (item:getFluidContainer(),
    -- stored on the action itself). consumedRatio * capacity = liters,
    -- same "amount" units ISFluidTransferAction/ISFluidEmptyAction use
    -- elsewhere in vanilla (getAmount()).
    local capOk, capacity = pcall(function() return self.fluidContainer and self.fluidContainer:getFluidCapacity() end)
    local liters = (capOk and capacity and self.consumedRatio) and (self.consumedRatio * capacity) or nil
    return {
        source = "item",
        fluid = getFluidTypeString(self.fluidContainer) or "unknown",
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
        liters = liters,
    }
end

-- Confirmed real, separate code path from ISDrinkFluidAction:
-- ISInventoryPaneContextMenu.onDrinkForThirst uses this for a specific
-- inventory-drink flow.
local function extractDrinkFromBottle(self)
    local fc = nil
    if self.item then
        local ok, result = pcall(function() return self.item:getFluidContainer() end)
        if ok then fc = result end
    end
    return {
        source = "item",
        fluid = getFluidTypeString(fc) or "unknown",
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
    }
end

-- Real display name of a WORLD water source object (e.g. "Sink",
-- "Toilet", "Rain Collector Barrel"), reimplementing the exact logic
-- vanilla's own client-only ISWorldObjectContextMenu.lua uses for its
-- drink tooltip (createWaterSourceTooltip / the local
-- getMoveableDisplayName helper, both client/-scoped and unavailable
-- to us server-side, but their underlying calls -- getSprite():
-- getProperties() tile-property lookup, Translator.getMoveableDisplayName()
-- -- are core object/localization APIs, not client-rendering-specific,
-- so the logic is safe to reimplement here). A CustomName tile
-- property (optionally prefixed by GroupName) gives the real object
-- name; no CustomName property at all (a raw lake/river/rain tile,
-- not a placed object) means there's genuinely no name to give --
-- falls back to "Natural Water Source", matching vanilla's own exact
-- fallback text.
local function getWaterSourceName(waterObject)
    if not waterObject then return nil end

    local okInv, isInv = pcall(function() return instanceof(waterObject, "IsoWorldInventoryObject") end)
    if okInv and isInv then
        local okItem, item = pcall(function() return waterObject:getItem() end)
        if okItem and item then
            local okName, name = pcall(function() return item:getName() end)
            if okName and name then return name end
        end
        return nil
    end

    local okSprite, sprite = pcall(function() return waterObject:getSprite() end)
    if not okSprite or not sprite then return "Natural Water Source" end
    local okProps, props = pcall(function() return sprite:getProperties() end)
    if not okProps or not props then return "Natural Water Source" end
    local okHas, hasCustomName = pcall(function() return props:has("CustomName") end)
    if not okHas or not hasCustomName then return "Natural Water Source" end

    local okName, name = pcall(function() return props:get("CustomName") end)
    if not okName or not name then return "Natural Water Source" end

    local okGroup, hasGroup = pcall(function() return props:has("GroupName") end)
    if okGroup and hasGroup then
        local okGroupName, groupName = pcall(function() return props:get("GroupName") end)
        if okGroupName and groupName then
            name = groupName .. " " .. name
        end
    end

    local okTranslate, translated = pcall(function() return Translator.getMoveableDisplayName(name) end)
    if okTranslate and translated then return translated end
    return name
end

-- Drinking directly from a WORLD water source (sink, tap, river, rain
-- barrel, etc, not an inventory item) goes through neither of the
-- above -- ISWorldObjectContextMenu.onDrink uses this instead.
-- ISTakeWaterAction is ALSO used for filling a container (self.item
-- ~= nil in that case) -- only emit for the item==nil (genuine
-- direct-drink) case, filling a bottle isn't consumption.
local function extractTakeWater(self)
    if self.item ~= nil then return nil end -- filling a container, not drinking -- skip

    local fc = nil
    local ok1, result = pcall(function() return self.waterObject:getFluidContainer() end)
    if ok1 and result then fc = result end
    local fluid = getFluidTypeString(fc)
    if not fluid then
        local ok2, primaryFluid = pcall(function() return self.waterObject:getPrimaryFluid() end)
        if ok2 and primaryFluid then
            local ok3, t = pcall(function() return primaryFluid:getFluidTypeString() end)
            if ok3 then fluid = t end
        end
    end
    fluid = fluid or (self.waterTaintedCL and "TaintedWater") or "Water"

    return {
        source = "world",
        location = getWaterSourceName(self.waterObject) or "?",
        fluid = fluid,
        liters = self.waterUnit,
    }
end

-- CONFIRMED live (2026-08-10): vanilla routes smoking through this
-- SAME TimedAction, not a dedicated one -- ISSmokingAction (this
-- session's earlier guess) doesn't exist/never fired; the actual
-- ISEatFoodAction hook caught a real cigar smoke as
-- {"item":"Base.Cigar",...,"type":"eat",...}. Per the user's
-- confirmed B42 source knowledge: cigarette/cigar items carry
-- ItemTag.SMOKABLE, and the inventory code checks
-- item:hasTag(ItemTag.SMOKABLE) before queuing this exact same
-- ISEatFoodAction -- there's no separate class to hook, and hooking
-- .complete here (rather than the context-menu click) already gets
-- "cancelling the smoke doesn't count" for free, same as it does for
-- real eating.
local function extractEat(self)
    local isSmokable = false
    if self.item then
        local ok, result = pcall(function() return self.item:hasTag(ItemTag.SMOKABLE) end)
        isSmokable = ok and result or false
    end
    return {
        type = isSmokable and "smoke" or nil, -- nil lets hookTimedActionOnce fall back to "eat"
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
        -- Smoking is one whole cigarette/cigar per action, not a
        -- partial-consumption ratio -- self.percentage is the
        -- hunger-satisfying fraction eaten, which isn't a meaningful
        -- concept here (per user: "i dont think we get half").
        amount = isSmokable and 1 or self.percentage,
    }
end

-- CONFIRMED live (2026-08-09): getDisplayName() correctly distinguishes
-- pill types by fullType (Base.Pills -> "Painkillers", Base.PillsVitamins
-- -> "Caffeine Pills").
local function extractPill(self)
    return {
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
    }
end

-- Monkey-patches every hook exactly once per call, reload-safe (each
-- hookTimedActionOnce call restores Consumption.originals[className]
-- before installing exactly one fresh wrapper -- never stacks, even
-- across many F11 reloads).
function Consumption.init()
    local Runtime = ExporterLog.Runtime
    local emit = ExporterLog.Emit.event

    Runtime.hookTimedActionOnce(Consumption.originals, "ISDrinkFluidAction", ISDrinkFluidAction, "drink", extractDrinkFluid, emit)
    Runtime.hookTimedActionOnce(Consumption.originals, "ISDrinkFromBottle", ISDrinkFromBottle, "drink", extractDrinkFromBottle, emit)
    Runtime.hookTimedActionOnce(Consumption.originals, "ISTakeWaterAction", ISTakeWaterAction, "drink", extractTakeWater, emit)
    Runtime.hookTimedActionOnce(Consumption.originals, "ISEatFoodAction", ISEatFoodAction, "eat", extractEat, emit)
    Runtime.hookTimedActionOnce(Consumption.originals, "ISTakePillAction", ISTakePillAction, "pill", extractPill, emit)
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime/Emit might not
-- exist yet at the exact moment PZ's auto-loader happens to run THIS
-- file -- OnGameStart is confirmed to fire only once, after every mod
-- file has finished loading, and never fires again on a later reload,
-- so it can't cause double-init -- Consumption.init() is idempotent
-- anyway.
local ok, err = pcall(Consumption.init)
if not ok then
    print("ExporterLog: Consumption.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Consumption.init)
