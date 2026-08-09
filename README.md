# zomboid-exporter

A Prometheus exporter + optional stats database for a Project Zomboid
dedicated server. Runs as a sidecar container next to the game server,
reading files it already writes to disk — no RCON polling. Kill/movement/
consumption/medical stats need a small companion Lua mod (included, see
below); everything else needs no mod at all.

## What it does

Four independent data sources:

1. **PanelBridge status files** (`Lua/panelbridge/<server>/status.json` /
   `startup.json`) — polled on every Prometheus scrape. Gives you live
   player count, per-player online status, server up/down, and command
   success/failure counts. Requires the
   [zomboid-control-panel](https://github.com/fpsacha/zomboid-control-panel)'s
   PanelBridge component.
2. **`PerkLog.txt`** — written natively by the PZ dedicated server, no mod
   needed. Tailed continuously. Gives you logins, deaths, character
   creation, and skill level-ups, each with full coordinates,
   hours-survived, and (for logins/deaths) a complete skill snapshot.
3. **`connections.txt`** — also written natively, no mod needed. Tailed
   continuously. Gives you exact session start/end timestamps per player
   (real login/logout, not just PerkLog's login-only view), sourced from
   the dedicated server's own connect/disconnect handshake logging.
4. **`ExporterLog.txt`** — written by the companion `lua-mod/` (see
   below), one flat JSON object per line. Fully generic on this side:
   whatever `type` the mod emits becomes the stored `event_type`, and the
   full payload lands in `details` verbatim — a new tracked stat needs a
   Lua-only change, never a change here.

PanelBridge status feeds **Prometheus metrics** directly. The other three
are all optional to persist, but persistence is what makes them useful —
without a database, PerkLog/connections/ExporterLog events still drive a
handful of Prometheus counters but nothing else. With one configured
(external **Postgres** or embedded **SQLite**), every event from all
three sources lands in one shared history: per-player death records,
character lifecycle across respawns, real session length, and whatever
the Lua mod currently tracks (kills, driving, walking/running/sprinting,
eating/drinking/pills, reading, medical treatment, injuries, vehicle
enter/exit — see [`lua-mod/ExporterLog-Workshop/CHANGELOG.md`](lua-mod/ExporterLog-Workshop/CHANGELOG.md)
for the exact current list and history).

## Prometheus metrics

| Metric | Type | Labels | Source |
|---|---|---|---|
| `zomboid_server_up` | gauge | `server` | PanelBridge status |
| `zomboid_players_online` | gauge | `server` | PanelBridge status |
| `zomboid_player_online` | gauge | `server`, `player` | PanelBridge status |
| `zomboid_server_start_time_seconds` | gauge | `server` | PanelBridge startup |
| `zomboid_panelbridge_commands_processed_total` | counter | `server` | PanelBridge status |
| `zomboid_panelbridge_commands_failed_total` | counter | `server` | PanelBridge status |
| `zomboid_status_stale` | gauge | `server` | derived |
| `zomboid_status_age_seconds` | gauge | `server` | derived |
| `zomboid_logins_total` | counter | `server` | PerkLog.txt |
| `zomboid_deaths_total` | counter | `server` | PerkLog.txt |
| `zomboid_skill_levelups_total` | counter | `server`, `skill` | PerkLog.txt |
| `zomboid_characters_created_total` | counter | `server` | PerkLog.txt |

There's deliberately no per-stat Prometheus metric for anything sourced
from `ExporterLog.txt` or `connections.txt` — those are open-ended
(new event types appear whenever the Lua mod adds one) and go straight
to the database instead. Build leaderboards/dashboards by querying
Postgres/SQLite directly rather than adding a new gauge per stat.

## Persistence: pick one, or neither

| Flag | Backend | When to use |
|---|---|---|
| *(none)* | — | Just want Prometheus metrics/dashboards. No history, no leaderboards. |
| `--sqlite-path=/path/to/file.db` | Embedded SQLite | Default recommendation. Zero external dependency — just a file on a writable volume. |
| `--db-dsn=postgres://...` | External Postgres | You already run Postgres and want to query the same data from elsewhere, or need more concurrent read access than SQLite comfortably gives you (SQLite here is single-writer). Takes priority over `--sqlite-path` if both are set. |

Both backends implement the same schema (semantically — see
`schema_postgres.sql` / `schema_sqlite.sql`, they differ only in SQL
dialect): `players`, `characters`, `skill_snapshots`, `events`,
`processed_files` (internal checkpoint bookkeeping). Applied
automatically on startup, safe to run every boot — including in-place
migrations for databases that predate a given column (e.g. `server`, add
in place with no manual step).

Every row is labeled with a `server` column (the `--server-name` this
instance was started with), so multiple exporter instances can safely
share one database.

**Note on the data mount**: the exporter typically mounts the same
volume the game server uses, read-only (see the example below). If you
use `--sqlite-path`, point it at a *separate, writable* volume/mount —
not the read-only game-data one.

## The companion Lua mod (`lua-mod/`)

`lua-mod/src/ExporterLog/` is a server-side-only Project Zomboid Build 42
mod (no client-side code — zero effect on players who don't have it)
that writes `ExporterLog.txt` in the same `Logs/logs_*/` folder PZ
already uses for `PerkLog.txt`. It's the only piece of this project that
needs installing separately from the exporter binary/image — distribute
it via Steam Workshop (unlisted or public) or a local `mods/` folder like
any other PZ mod.

- `lua-mod/src/ExporterLog/` — the only hand-edited source. Never edit
  the built copies below directly; they get overwritten.
- `lua-mod/build-dev.py` / `lua-mod/build-workshop.py` — regenerate the
  built copies from `src/` after any edit.
- `lua-mod/ExporterLog_Dev/` — disposable single-player debug copy
  (`print()`-based output visible in the game console), for iterating on
  new trackers without touching a real server.
- `lua-mod/ExporterLog/` — plain local-`mods/`-folder copy.
- `lua-mod/ExporterLog-Workshop/` — Steam Workshop upload staging folder
  (`Tools -> Workshop Item Manager -> Upload to Steam` from the game
  client uploads from here). `CHANGELOG.md`/`workshop.txt` here are
  hand-maintained, not generated.

Steam Workshop has no version field of its own — `ExporterLog.VERSION`
is printed at boot (`grep`-able in server logs) so you can confirm which
code a given server is actually running.

### A note on player identity (`steamId`)

`player:getSteamID()` in Lua returns a double, which only exactly
represents integers up to 2^53 — a real SteamID64 (~7.6×10¹⁶) is past
that range, so any Lua-mod-reported `steamId` is unreliable (a genuine
Kahlua/PZ engine limitation, not fixable from Lua). `PerkLog.txt` and
`connections.txt` are both written natively by the Java engine and never
pass through that conversion, so their `steam_id` is always exact. The
exporter resolves the canonical `steam_id` for each username from those
two trustworthy sources and uses it for every `ExporterLog.txt` event
too, rather than trusting the Lua-reported value directly.

## Flags

```
--data-path              Zomboid data directory (contains Lua/panelbridge/ and Logs/). Default: /data
--server-name             Server name — must match the directory under Lua/panelbridge/. Required.
--web.listen-address      Address to expose metrics on. Default: :9091
--stale-threshold         Status file age above which the server is considered stale/down. Default: 120s
--db-dsn                  Optional external Postgres DSN.
--sqlite-path             Optional path to a SQLite database file (created if missing).
```

## Running it

### Docker Compose (alongside a PZ server + panel)

```yaml
services:
  zomboid-exporter:
    image: ghcr.io/janip81/zomboid-exporter:latest
    command:
      - --server-name=my-server
      - --data-path=/data
      - --sqlite-path=/exporter-data/stats.db
    volumes:
      - zomboid-data:/data:ro
      - exporter-data:/exporter-data
    ports:
      - "9091:9091"

volumes:
  zomboid-data:
    external: true # same volume your PZ server/panel already use
  exporter-data:
```

### Kubernetes (sidecar container)

```yaml
- name: exporter
  image: ghcr.io/janip81/zomboid-exporter:latest
  args:
    - --server-name=my-server
    - --data-path=/data
    - --sqlite-path=/exporter-data/stats.db
  ports:
    - name: metrics
      containerPort: 9091
  volumeMounts:
    - name: data          # same PVC the game server writes to
      mountPath: /data
      readOnly: true
    - name: exporter-data  # separate, writable
      mountPath: /exporter-data
  readinessProbe:
    httpGet: { path: /healthz, port: metrics }
  livenessProbe:
    httpGet: { path: /healthz, port: metrics }
```

A ready-to-use Grafana dashboard is in [`dashboards/zomboid.json`](dashboards/zomboid.json).

## Building from source

```
go build -o zomboid-exporter .
```

Requires Go 1.25+. `CGO_ENABLED=0` works (and is what the Docker image
uses) — both the Postgres driver (`pgx`) and the SQLite driver
(`modernc.org/sqlite`) are pure Go, no C toolchain needed.

## How the native log files get read

`PerkLog.txt`/`connections.txt`/`ExporterLog.txt` all share the same
underlying behavior worth knowing about: PZ writes the *currently
running* session's file flat in `Logs/`, and only moves it into
`Logs/logs_YYYY-MM-DD/` once the *next* server start archives it. The
exporter globs both locations on every poll, so it sees live data
immediately rather than only catching up on the previous session's data
after a restart.

Each file's read progress is checkpointed by filename in the database
(`processed_files` table) — not by full path, since a file's path
changes when it gets archived. On the very first run, every historical
file is read from the start (a full backfill, no manual step required).
On every subsequent run (including after downtime), each file is
compared against its last checkpoint and only genuinely new content is
read; a file that's already fully caught up costs one cheap `stat()` per
poll, not a re-read. No event is ever lost to exporter downtime or
duplicated by a restart, whether that's a five-second pod restart or the
exporter being off for days.

Without a database configured, there's nowhere to persist a checkpoint,
so PerkLog falls back to simply following the newest file from EOF
forward — live Prometheus counters only, no history.

## Example: `PerkLog.txt` format

```
[06-08-26 08:34:59.194] [76561197965988309][Edd1e360][6764,5380,0][Login][Hours Survived: 472].
[06-08-26 08:34:59.195] [76561197965988309][Edd1e360][6764,5380,0][Cooking=0, Fitness=5, ...][Hours Survived: 472].
```

Every login/death/character-creation is immediately followed by a
skill-dump line carrying the character's full current skill levels —
the exporter correlates that dump with whichever character is currently
"active" (alive) for that Steam ID. Level-ups carry two extra bracket
groups (`[Level Changed][SkillName][NewLevel]`). Unrecognized event
keywords are ignored rather than guessed at, so a future PZ update
adding a new event type fails safe (silently skipped) instead of
mis-parsing.
