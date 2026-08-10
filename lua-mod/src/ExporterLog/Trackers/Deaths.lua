-- ExporterLog.Deaths -- zombie-kills-at-death ONLY. Everything else
-- ROADMAP.md originally wanted for "Deaths + survival time" (total
-- deaths, survival time at death, location) is ALREADY fully captured
-- natively by PerkLog.txt's own "died" event -- CONFIRMED by reading
-- perklog.go/main.go directly: parsePerkLogLine already extracts
-- SteamID/Username/X/Y/Z/HoursSurvived for Kind=="died", and
-- runPerkLogPipeline already calls db.handleDied(ctx, ev) for every
-- one, no Lua mod involved at all. Duplicating any of that here would
-- be redundant, error-prone (PerkLog is the trustworthy native source
-- for exact SteamID -- see README's steamId note), and pure Lua-mod
-- effort for zero new data. This file exists purely to attach the one
-- genuinely missing fact: zombie kill count at the moment of death.
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why (confirmed live: require() doesn't resolve
-- mod-local paths, and PZ's own file auto-load order across a mod's
-- files isn't guaranteed).
ExporterLog = ExporterLog or {}
ExporterLog.Deaths = ExporterLog.Deaths or {}
ExporterLog.Deaths.callbacks = ExporterLog.Deaths.callbacks or {}

local Deaths = ExporterLog.Deaths

-- UNVERIFIED (2026-08-10) -- dedicated server offline, so this event
-- name couldn't be grepped against real vanilla source like every
-- other CONFIRMED hook in this mod was. "Events.OnPlayerDeath" is a
-- best-effort guess (common across PZ mods generally), guarded below
-- the same way Vehicles.lua guards Events.OnEnterVehicle/OnExitVehicle
-- -- if the name's wrong, Deaths.init() just prints a clear diagnostic
-- and no-ops instead of erroring, so it's safe to test live via the
-- usual F11-reload-and-die workflow.
local function onPlayerDeath(character)
    local ok, err = pcall(function()
        if not character then return end
        local username = (character.getUsername and character:getUsername()) or "?"
        local okKills, kills = pcall(function() return character:getZombieKills() end)
        ExporterLog.Emit.event({
            type = "death",
            steamId = ExporterLog.Utils.getPlayerSteamID(character),
            username = username,
            zombieKills = okKills and kills or nil,
        })
    end)
    if not ok then print(ExporterLog.Runtime.logPrefix() .. ": onPlayerDeath error: " .. tostring(err)) end
end

-- Registers exactly once per call, reload-safe. Safe to call multiple
-- times -- never stacks or duplicates.
function Deaths.init()
    local Runtime = ExporterLog.Runtime
    if Events.OnPlayerDeath then
        Runtime.registerEventOnce(Deaths.callbacks, "onPlayerDeath", Events.OnPlayerDeath, onPlayerDeath)
    else
        print(Runtime.logPrefix() .. ": Events.OnPlayerDeath not found -- zombieKills-at-death unavailable, needs a different event name (check when server's back online)")
    end
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime might not exist
-- yet at the exact moment PZ's auto-loader happens to run THIS file --
-- OnGameStart is confirmed to fire only once, after every mod file has
-- finished loading, and never fires again on a later reload, so it
-- can't cause double-init -- Deaths.init() is idempotent anyway.
local ok, err = pcall(Deaths.init)
if not ok then
    print("ExporterLog: Deaths.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Deaths.init)
