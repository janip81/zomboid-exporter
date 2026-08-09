-- ExporterLog.Medical -- medical treatment: bandaging, disinfecting,
-- stitching, splinting. Grouped together for the same reason as
-- Consumption.lua -- all monkey-patched TimedAction.complete hooks
-- sharing Runtime.hookTimedActionOnce. Real vanilla TimedActions found
-- via grep of the live server, not guessed:
-- shared/TimedActions/{ISApplyBandage,ISDisinfect,ISStitch,ISSplint}.lua.
--
-- Every one of these actions distinguishes SELF-treatment from
-- treating ANOTHER player (self.character = who's performing the
-- action / the "doctor", self.otherPlayer = who's being treated / the
-- "patient" -- same person for self-treatment). The shared
-- hookTimedActionOnce wrapper already attributes username/steamId to
-- self.character (the doctor) -- extractX below adds patientUsername/
-- patientSteamId/selfTreated separately, so a "Server Doctor"
-- leaderboard (treating OTHERS specifically) can be told apart from
-- ordinary self-care.
--
-- Bandage/Stitch/Splint all have a self.doIt flag distinguishing APPLY
-- (true) from REMOVE (false) -- only apply counts as "treatment
-- given", so extractX returns nil (skip) on removal. Disinfect has no
-- remove variant, always counts.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why.
ExporterLog = ExporterLog or {}
ExporterLog.Medical = ExporterLog.Medical or {}
ExporterLog.Medical.originals = ExporterLog.Medical.originals or {}

local Medical = ExporterLog.Medical

local function bodyPartName(bodyPart)
    if not bodyPart then return "?" end
    local okType, t = pcall(function() return bodyPart:getType() end)
    if not okType or not t then return "?" end
    local okName, name = pcall(function() return BodyPartType.ToString(t) end)
    if okName and name then return name end
    return "?"
end

-- self.otherPlayer is the PATIENT (same object as self.character for
-- self-treatment) -- merged into every extractX's returned fields.
local function patientFields(self)
    local patient = self.otherPlayer
    return {
        patientUsername = (patient and patient.getUsername) and patient:getUsername() or "?",
        patientSteamId = ExporterLog.Utils.getPlayerSteamID(patient),
        selfTreated = (patient == self.character),
    }
end

local function extractBandage(self)
    if not self.doIt then return nil end -- removing a bandage, not applying -- skip
    local fields = {
        bodyPart = bodyPartName(self.bodyPart),
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
    }
    for k, v in pairs(patientFields(self)) do fields[k] = v end
    return fields
end

local function extractDisinfect(self)
    local fields = {
        bodyPart = bodyPartName(self.bodyPart),
        item = self.alcohol and self.alcohol:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.alcohol),
    }
    for k, v in pairs(patientFields(self)) do fields[k] = v end
    return fields
end

local function extractStitch(self)
    if not self.doIt then return nil end
    local fields = {
        bodyPart = bodyPartName(self.bodyPart),
        item = self.item and self.item:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.item),
    }
    for k, v in pairs(patientFields(self)) do fields[k] = v end
    return fields
end

local function extractSplint(self)
    if not self.doIt then return nil end
    local fields = {
        bodyPart = bodyPartName(self.bodyPart),
        item = self.plank and self.plank:getFullType() or "?",
        name = ExporterLog.Utils.getItemDisplayName(self.plank),
    }
    for k, v in pairs(patientFields(self)) do fields[k] = v end
    return fields
end

-- Monkey-patches every hook exactly once per call, reload-safe -- same
-- mechanics as Consumption.lua.
function Medical.init()
    local Runtime = ExporterLog.Runtime
    local emit = ExporterLog.Emit.event

    Runtime.hookTimedActionOnce(Medical.originals, "ISApplyBandage", ISApplyBandage, "bandage", extractBandage, emit)
    Runtime.hookTimedActionOnce(Medical.originals, "ISDisinfect", ISDisinfect, "disinfect", extractDisinfect, emit)
    Runtime.hookTimedActionOnce(Medical.originals, "ISStitch", ISStitch, "stitch", extractStitch, emit)
    Runtime.hookTimedActionOnce(Medical.originals, "ISSplint", ISSplint, "splint", extractSplint, emit)
end

-- Self-initialize: immediate attempt (handles every F11 reload) plus
-- an Events.OnGameStart fallback (handles the one-time first-boot
-- ordering race) -- same pattern as every other tracker.
local ok, err = pcall(Medical.init)
if not ok then
    print("ExporterLog: Medical.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Medical.init)
