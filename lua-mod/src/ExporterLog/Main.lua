-- ExporterLog Main -- status banner only. THIS FILE DOES NOT CONTROL
-- INITIALIZATION ORDER -- see below for why.
--
-- ORIGINAL DESIGN (superseded, 2026-08-09): this file was meant to be
-- the single bootstrap point -- require every module, then explicitly
-- call each tracker's .init() in a controlled order. That broke on
-- the very first live test: CONFIRMED LIVE, every require("ExporterLog/
-- ...") call failed (logged as a WARN, not fatal) across every file in
-- this mod -- require() does not resolve cross-file paths within a
-- mod's own directory, despite matching vanilla's own proven
-- "SubDir/File" pattern for ITS OWN internal files. Because the
-- requires silently no-op'd, Main.lua reached `ExporterLog.Kills.
-- init()` before Trackers/Kills.lua had even been auto-loaded yet by
-- PZ's own scanner (which does independently find and execute every
-- .lua file in a mod, just with no ordering guarantee relative to
-- other files) -- crashing with "attempted index: init of non-table:
-- null".
--
-- CURRENT DESIGN: every tracker module now self-initializes at the
-- bottom of its own file -- an immediate call (handles every F11
-- reload, which re-executes each file top to bottom) plus an
-- Events.OnGameStart fallback (handles the one-time first-boot
-- ordering race, since OnGameStart is confirmed to fire only once,
-- strictly after every mod file has finished loading). Every tracker
-- also does fresh ExporterLog.Runtime/Emit/Utils/Vehicles lookups at
-- actual call time instead of caching them into file-top-level locals
-- -- a cached local captured before its target table existed would
-- stay nil for every closure in that file for the rest of that load
-- pass, including deferred event handlers.
--
-- This file's only remaining job is a load-confirmation banner, and
-- even that's deferred to Events.OnGameStart for the same reason --
-- ExporterLog.Runtime might not exist yet if this file happens to be
-- auto-loaded early.
--
-- Server-only by design (this whole ExporterLog/ tree lives under
-- media/lua/server/, never shared/ or client/): a mod folder's
-- server/ is only loaded by the server process (real dedicated server
-- or single-player's embedded one) per PZwiki's Build 42 mod
-- structure docs -- putting tracking code in shared/ would make it
-- ALSO execute on multiplayer clients, which must never produce their
-- own copy of stats the server already tracks. ExporterLog.Runtime's
-- mode detection is a second, belt-and-suspenders layer on top of
-- this file placement, not a substitute for it.
-- Bumped by hand on every meaningful change -- the only way to tell
-- which code is actually running on a given server without diffing
-- files, since Steam Workshop itself has no version field, only free-
-- text change notes on the item page.
ExporterLog = ExporterLog or {}
ExporterLog.VERSION = "1.1.0"

Events.OnGameStart.Add(function()
    local Runtime = ExporterLog and ExporterLog.Runtime
    if Runtime then
        print(Runtime.logPrefix() .. ": ExporterLog v" .. ExporterLog.VERSION .. " loaded, mode=" .. Runtime.getMode())
    end
end)
