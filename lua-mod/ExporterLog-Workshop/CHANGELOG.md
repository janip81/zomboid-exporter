# ExporterLog changelog

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
