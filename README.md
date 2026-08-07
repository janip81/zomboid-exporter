# zomboid-exporter

A Prometheus exporter + optional stats database for a Project Zomboid
dedicated server. Runs as a sidecar container next to the game server,
reading files it already writes to disk — no RCON polling, no game mods
required beyond the [zomboid-control-panel](https://github.com/fpsacha/zomboid-control-panel)'s
PanelBridge (for live player/uptime status).

## What it does

Two independent data sources, feeding two independent outputs:

1. **PanelBridge status files** (`Lua/panelbridge/<server>/status.json` /
   `startup.json`) — polled on every Prometheus scrape. Gives you live
   player count, per-player online status, server up/down, and command
   success/failure counts.
2. **`PerkLog.txt`** — a log file the PZ dedicated server writes natively
   (no mod needed). Tailed continuously. Gives you logins, deaths,
   character creation, and skill level-ups, each with full coordinates,
   hours-survived, and (for logins/deaths) a complete skill snapshot.

Both feed **Prometheus metrics** always. The `PerkLog.txt` events *can
also* be persisted to a database — either an external **Postgres**
instance or an embedded **SQLite** file — for anything that needs real
history rather than a point-in-time gauge: per-player death records,
character lifecycle across respawns, and leaderboards. The database is
entirely optional; the exporter is fully useful for dashboards/alerting
with neither configured.

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

## Persistence: pick one, or neither

| Flag | Backend | When to use |
|---|---|---|
| *(none)* | — | Just want Prometheus metrics/dashboards. No history, no leaderboards. |
| `--sqlite-path=/path/to/file.db` | Embedded SQLite | Default recommendation. Zero external dependency — just a file on a writable volume. |
| `--db-dsn=postgres://...` | External Postgres | You already run Postgres and want to query the same data from elsewhere, or need more concurrent read access than SQLite comfortably gives you (SQLite here is single-writer). Takes priority over `--sqlite-path` if both are set. |

Both backends implement the same schema (semantically — see
`schema_postgres.sql` / `schema_sqlite.sql`, they differ only in SQL
dialect): `players`, `characters`, `skill_snapshots`, `events`. Applied
automatically on startup, safe to run every boot.

**Note on the data mount**: the exporter typically mounts the same
volume the game server uses, read-only (see the example below). If you
use `--sqlite-path`, point it at a *separate, writable* volume/mount —
not the read-only game-data one.

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

## How PerkLog.txt parsing works

The PZ dedicated server writes lines like:

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

`PerkLog.txt` rotates to a new file every server start (not daily), so
the tailer re-scans for the newest file under `Logs/logs_*/` and
switches to it automatically when the server restarts — it does not
replay a session's full history on exporter restart, only events from
that point forward.
