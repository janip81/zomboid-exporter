# ExporterLog changelog

## Update 2026-08-10 (3) — v1.5.0

### New tracked stats
- **Environment (indoor/outdoor/vehicle) streaks** — tracks each player's current continuous streak in one of three states: indoors, outdoors, or in a vehicle. Entering a vehicle correctly ends whatever indoor/outdoor streak was running (sitting in a car parked outside doesn't count as an "outdoor" streak) and starts its own trackable vehicle streak. Emits on every state transition (the completed streak's exact duration, marked `final:true`) plus an hourly heartbeat while a streak is ongoing (`final:false`, the current running total) so a long streak's milestone can be caught before it ends. The `final` flag matters for anyone summing these for lifetime totals later: only `final:true` rows should be summed, since heartbeats repeat the running total rather than adding to it. CONFIRMED live in singleplayer debug: indoor→vehicle→outdoor→vehicle→outdoor all produced correct event shapes and durations, including a fresh (not resumed) streak after exiting a vehicle.

### Fixed
- **Debugger-freeze bug in the new Environment tracker itself**: initial version called `p:getVehicle()`/`p:isOutside()` via plain `pcall`, the same class of bug that froze the whole game on `item:isRead()` earlier — PZ's "break on error" debugger setting intercepts before `pcall`'s protection kicks in. Fixed with the same existence-check-before-call `safeCall()` helper `Reading.lua` already established. Confirmed live: after the fix, every tracker (including previously-silent ones downstream in file-load order) registered cleanly again.

## Update 2026-08-10 (2) — v1.4.0

### New tracked stats
- **World stats** — periodic (~hourly) snapshot of in-game date/time, nights survived, and world age, not tied to any player. CONFIRMED live.
- **Sleeping** — total hours slept per session, via `awake→asleep`/`asleep→awake` transition detection. CONFIRMED live: a full sleep produced the correct `hours` value, matching world stats' independently-observed time skip during the same nap.
- **Reading, rewritten for skill books** — the existing `read` event now distinguishes one-shot Literature (unchanged: `amount:1` on completion) from multi-session skill books, which now report `pageStart`/`pageEnd`/`pagesRead`/`totalPages`/`completed` per reading session (interrupted or finished), via both `ISReadABook:stop()` and `:complete()`. CONFIRMED live across both book types, including a cancelled-then-resumed skill book session.
- **Weapon hits and swings** — per-hit damage (`weapon_hit`: weapon, damage, target type) and per-swing/shot outcome (`weapon_swing`: hit boolean, total damage, targets hit, hardest hit on one target, hardest single projectile). Handles multi-target melee cleaves and multi-pellet shotgun blasts correctly by correlating hits to their originating swing via a shared `attackId`, and by target object identity (not a persisted zombie ID) so "43 damage to one zombie" is distinguishable from "43 spread across three." CONFIRMED live extensively: bare hands, metal baseball bat, shotgun (single and multi-target), single-shot rifle, clean misses, and an accidental shot fired into the air all produced correct, sane output. Accuracy% is `weapon_swing` rows with `hit=true` divided by total rows, per weapon — always ≤100% since it's per-swing, not per-pellet.
- **Deaths (zombie-kills-at-death)** — supplements PerkLog's own native death tracking (already covers location/hours-survived/deaths-count with zero Lua involvement) with the one fact it doesn't carry. Implemented, hooked to `Events.OnPlayerDeath` (confirmed to exist and register live), but not yet tested against an actual death this session.

## Update 2026-08-10 — v1.3.0

### New tracked stats
- **Smoking** — cigarettes/cigars smoked, `amount` always 1 (a whole item, not a partial-consumption ratio). CONFIRMED live: B42 routes smoking through the same `ISEatFoodAction` regular eating uses, not a dedicated action — detected via `item:hasTag(ItemTag.SMOKABLE)` inside the existing `eat` hook rather than a separate one, so it works for any current/future item correctly tagged smokable, not just `Base.Cigarettes`/`Base.Cigar`. Cancelling a smoke mid-action correctly produces no event at all, same as cancelling regular eating.

## Update 2026-08-09 (4) — v1.2.0

### New tracked stats
- **Maximum driving speed** — per vehicle type, records a new personal-best top speed as it's set while driving.

## Update 2026-08-09 (3) — v1.1.0

### New tracked stats
- **Medical treatment** — bandaging, disinfecting, stitching, and splinting, whether treating yourself or another player. Records which body part, which item was used, and (when treating someone else) who the patient was.
- **Injuries** — bites, scratches, lacerations, burns, and fractures, per body part.

### Internal
- `ExporterLog.VERSION` now printed at boot (this update is v1.1.0) — Steam Workshop has no version field of its own, so this is the only way to confirm which code a given server is actually running.

## Update 2026-08-09 (2)

### Fixes
- **eat/drink/pill/read/enter_vehicle/exit_vehicle events now include `steamId`** — previously only `username` was sent, which the companion Go exporter's new generic event ingestion (every ExporterLog.txt line, any `type`, straight into the shared DB event log) can't reliably key a player by. No player-visible behavior change.

## Update 2026-08-09

### New tracked stats
- **Driving distance** — per player, now tagged with the specific vehicle type driven (e.g. `Base.CarLightsBulletinSheriff`), so "most driven vehicle" stats are possible alongside total distance.
- **Walking / running / sprinting distance** — new tracker, correctly distinguishes all three movement states, flushed once accumulated distance crosses 100m per state.
- **Drinking, world sources** — now records *which* object you drank from (e.g. "Sink", "Toilet", "Rain Collector Barrel"), not just "drank from the world". Natural sources (lakes, rivers, rain) are labeled "Natural Water Source".
- **Vehicle enter/exit** — now correctly records the actual vehicle type (previously always logged as unknown).

### Fixes
- **Zombie kills**: added cause-of-death attribution — melee, firearm, vehicle, unarmed, or unknown — including weapon type or vehicle type where applicable.
- **Eating / drinking / pills**: events now include the item's real display name (e.g. "Painkillers") alongside its internal ID, not just the internal ID.
- **Reading**: same display-name addition (e.g. full book/magazine titles).
- Fixed a bug where repeated mod reloads during testing could cause duplicate event entries — not something players would have noticed, but good hygiene.

### Internal
- Full rewrite from a single script into organized modules (kills, consumption, movement, vehicles, reading) — no player-visible behavior change, just easier to maintain and extend going forward.
