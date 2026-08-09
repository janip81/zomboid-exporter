-- ExporterLog.Injuries -- scratches, lacerations (cut), bites, burns,
-- fractures. Unlike medical treatment (a clean TimedAction hook),
-- there's no vanilla event for "a player got injured" -- confirmed via
-- grep of the live server: none of Events.OnHitZombie/
-- OnPlayerAttackFinished/etc fire at the moment of injury, and the
-- native setBitten/setScratched/etc setters are called from
-- window-smashing and debug/client code, never from actual
-- zombie-attack combat resolution in Lua. Same situation kills
-- originally faced -- solved the same way, diff-polling instead of an
-- event hook (player:getZombieKills() then, per-body-part *Time()
-- getters now).
--
-- player:getBodyDamage():getBodyParts() confirmed server-usable (real
-- usage in server/PanelBridge.lua). Each BodyPartDamage exposes
-- getBiteTime()/getScratchTime()/getCutTime()/getBurnTime()/
-- getFractureTime() -- 0 when uninjured, >0 once inflicted. Polled on
-- Events.EveryOneMinute (same cadence as driving distance) rather than
-- every tick -- injuries don't need finer granularity, and this keeps
-- the per-player/per-bodypart/per-type state scan cheap.
--
-- NOT YET LIVE-TESTED (2026-08-09) -- built for single-player dev
-- testing first, per explicit instruction, before touching the
-- production/Workshop copies. In particular the *Time() getters'
-- actual behavior (does the value only ever increase while injured, or
-- can it fluctuate in ways that would cause a false 0->positive
-- "new injury" trigger?) is inferred from the getter names and
-- ISApplyBandage/ISStitch's analogous SetBandaged/setStitchTime
-- write-side usage, not confirmed by a real live wound yet.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why.
ExporterLog = ExporterLog or {}
ExporterLog.Injuries = ExporterLog.Injuries or {}
ExporterLog.Injuries.callbacks = ExporterLog.Injuries.callbacks or {}

local Injuries = ExporterLog.Injuries

-- lastState[username][bodyPartIndex][injuryType] = last-seen *Time()
-- value, so a 0 -> >0 transition (never-before -> just inflicted) is
-- what counts as a NEW injury, not just "currently injured" (which
-- would double-count the same wound every single poll tick until it
-- heals).
local lastState = {}

local function bodyPartName(bodyPart)
    if not bodyPart then return "?" end
    local okType, t = pcall(function() return bodyPart:getType() end)
    if not okType or not t then return "?" end
    local okName, name = pcall(function() return BodyPartType.ToString(t) end)
    if okName and name then return name end
    return "?"
end

-- value may be false (pcall failure) or nil -- normalized to 0 either
-- way. Fires emit(key) exactly once per 0->positive transition.
local function checkInjury(partState, key, value, emit)
    value = value or 0
    local previous = partState[key] or 0
    if value > 0 and previous <= 0 then
        emit(key)
    end
    partState[key] = value
end

local function onEveryMinuteInjuries()
    ExporterLog.Runtime.forEachTrackedPlayer(function(p)
        local username = p:getUsername()
        local okBD, bodyDamage = pcall(function() return p:getBodyDamage() end)
        if not okBD or not bodyDamage then return end
        local okParts, bodyParts = pcall(function() return bodyDamage:getBodyParts() end)
        if not okParts or not bodyParts then return end

        lastState[username] = lastState[username] or {}
        local playerState = lastState[username]

        for i = 0, bodyParts:size() - 1 do
            local part = bodyParts:get(i)
            local okIdx, idx = pcall(function() return part:getIndex() end)
            if okIdx then
                playerState[idx] = playerState[idx] or {}
                local partState = playerState[idx]
                local name = bodyPartName(part)

                local function emit(injuryType)
                    ExporterLog.Emit.event({
                        type = "injury",
                        steamId = ExporterLog.Utils.getPlayerSteamID(p),
                        username = username,
                        injury = injuryType,
                        bodyPart = name,
                    })
                end

                local okBite, biteTime = pcall(function() return part:getBiteTime() end)
                checkInjury(partState, "bite", okBite and biteTime, emit)

                local okScratch, scratchTime = pcall(function() return part:getScratchTime() end)
                checkInjury(partState, "scratch", okScratch and scratchTime, emit)

                local okCut, cutTime = pcall(function() return part:getCutTime() end)
                checkInjury(partState, "laceration", okCut and cutTime, emit)

                local okBurn, burnTime = pcall(function() return part:getBurnTime() end)
                checkInjury(partState, "burn", okBurn and burnTime, emit)

                local okFracture, fractureTime = pcall(function() return part:getFractureTime() end)
                checkInjury(partState, "fracture", okFracture and fractureTime, emit)
            end
        end
    end)
end

function Injuries.init()
    ExporterLog.Runtime.registerEventOnce(Injuries.callbacks, "onEveryMinuteInjuries", Events.EveryOneMinute, onEveryMinuteInjuries)
end

-- Self-initialize: immediate attempt (handles every F11 reload) plus
-- an Events.OnGameStart fallback (handles the one-time first-boot
-- ordering race) -- same pattern as every other tracker.
local ok, err = pcall(Injuries.init)
if not ok then
    print("ExporterLog: Injuries.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Injuries.init)
