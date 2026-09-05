-- ExporterLog.Washing -- washing yourself (hygiene) and washing a
-- specific clothing item at a sink/water source. Same monkey-patched
-- TimedAction.complete pattern as Consumption.lua -- see that file's
-- header comment for why there's no require() and no cached
-- cross-file locals here.
ExporterLog = ExporterLog or {}
ExporterLog.Washing = ExporterLog.Washing or {}
ExporterLog.Washing.originals = ExporterLog.Washing.originals or {}

local Washing = ExporterLog.Washing

-- Duplicated from Consumption.lua on purpose (see that file's header
-- comment on why cross-file locals aren't shared here). Real display
-- name of a WORLD water source object (e.g. "Sink", "Bathtub", "Rain
-- Collector Barrel"); falls back to "Natural Water Source" for a raw
-- lake/river tile with no CustomName property, matching vanilla's own
-- ISWorldObjectContextMenu tooltip text exactly.
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

-- ISWashYourself: washing blood/dirt off your own body at a sink.
-- self.sink and self.useSoap both confirmed set at construction (see
-- ISWashYourself:new) and untouched by :complete(), so both are still
-- readable here even though our wrapper runs after the original
-- completes. No per-body-part blood/dirt breakdown persists on self
-- (ISWashYourself:complete only uses a local waterUsed var) -- v1 just
-- tracks that a wash happened, where, and whether soap was used.
local function extractWashSelf(self)
    return {
        location = getWaterSourceName(self.sink) or "?",
        usedSoap = self.useSoap == true,
    }
end

-- ISWashClothing: washing a specific item. self.item, self.sink,
-- self.bloodAmount, self.dirtAmount, self.noSoap are all set at
-- construction (see ISWashClothing:new) and never reassigned by
-- :complete(), so they're all still valid here.
local function extractWashClothing(self)
    return {
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
        location = getWaterSourceName(self.sink) or "?",
        bloodAmount = self.bloodAmount,
        dirtAmount = self.dirtAmount,
        usedSoap = self.noSoap ~= true,
    }
end

-- Monkey-patches every hook exactly once per call, reload-safe --
-- same guarantee as Consumption.init(), see that function's comment.
function Washing.init()
    local Runtime = ExporterLog.Runtime
    local emit = ExporterLog.Emit.event

    Runtime.hookTimedActionOnce(Washing.originals, "ISWashYourself", ISWashYourself, "wash_self", extractWashSelf, emit)
    Runtime.hookTimedActionOnce(Washing.originals, "ISWashClothing", ISWashClothing, "wash_clothing", extractWashClothing, emit)
end

-- Self-initialize: immediate attempt for F11 reloads, OnGameStart
-- fallback for the first-boot load-order race. Same rationale as
-- Consumption.lua's tail -- see that file's comment for the full
-- explanation of why this exact two-path pattern is reload-safe.
local ok, err = pcall(Washing.init)
if not ok then
    print("ExporterLog: Washing.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Washing.init)
