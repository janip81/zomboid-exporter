package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// statusFile mirrors {dataPath}/Lua/panelbridge/{serverName}/status.json
// written by PanelBridge every Lua tick while the server is running.
type statusFile struct {
	Alive       bool     `json:"alive"`
	PlayerCount int      `json:"playerCount"`
	Players     []string `json:"players"`
	Timestamp   int64    `json:"timestamp"` // ms since epoch
	Stats       struct {
		Processed int `json:"processed"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"stats"`
}

// startupFile mirrors {dataPath}/Lua/panelbridge/{serverName}/startup.json
type startupFile struct {
	StartTime int64 `json:"startTime"` // ms since epoch
}

type collector struct {
	dataPath   string
	serverName string
	staleAge   float64 // seconds

	up          *prometheus.Desc
	playersOn   *prometheus.Desc
	playerInfo  *prometheus.Desc
	startTime   *prometheus.Desc
	cmdProc     *prometheus.Desc
	cmdFailed   *prometheus.Desc
	statusStale *prometheus.Desc
	statusAge   *prometheus.Desc
}

func newCollector(dataPath, serverName string, staleAge float64) *collector {
	sv := []string{"server"}
	svp := []string{"server", "player"}
	return &collector{
		dataPath:   dataPath,
		serverName: serverName,
		staleAge:   staleAge,
		up: prometheus.NewDesc(
			"zomboid_server_up",
			"1 if the PanelBridge status file reports alive=true and is not stale",
			sv, nil),
		playersOn: prometheus.NewDesc(
			"zomboid_players_online",
			"Number of players currently online",
			sv, nil),
		playerInfo: prometheus.NewDesc(
			"zomboid_player_online",
			"1 if the named player is currently online",
			svp, nil),
		startTime: prometheus.NewDesc(
			"zomboid_server_start_time_seconds",
			"Unix timestamp of the last server start (from startup.json)",
			sv, nil),
		cmdProc: prometheus.NewDesc(
			"zomboid_panelbridge_commands_processed_total",
			"Total commands processed by PanelBridge since last start",
			sv, nil),
		cmdFailed: prometheus.NewDesc(
			"zomboid_panelbridge_commands_failed_total",
			"Total commands that failed in PanelBridge since last start",
			sv, nil),
		statusStale: prometheus.NewDesc(
			"zomboid_status_stale",
			"1 if the status file has not been updated within the stale threshold",
			sv, nil),
		statusAge: prometheus.NewDesc(
			"zomboid_status_age_seconds",
			"Age of the PanelBridge status file in seconds",
			sv, nil),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.playersOn
	ch <- c.playerInfo
	ch <- c.startTime
	ch <- c.cmdProc
	ch <- c.cmdFailed
	ch <- c.statusStale
	ch <- c.statusAge
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	sn := c.serverName
	statusPath := filepath.Join(c.dataPath, "Lua", "panelbridge", sn, "status.json")
	startupPath := filepath.Join(c.dataPath, "Lua", "panelbridge", sn, "startup.json")

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		slog.Warn("cannot read status file", "path", statusPath, "err", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0, sn)
		ch <- prometheus.MustNewConstMetric(c.statusStale, prometheus.GaugeValue, 1, sn)
		ch <- prometheus.MustNewConstMetric(c.statusAge, prometheus.GaugeValue, c.staleAge, sn)
		ch <- prometheus.MustNewConstMetric(c.playersOn, prometheus.GaugeValue, 0, sn)
		return
	}

	var st statusFile
	if err := json.Unmarshal(raw, &st); err != nil {
		slog.Warn("cannot parse status file", "err", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0, sn)
		ch <- prometheus.MustNewConstMetric(c.statusStale, prometheus.GaugeValue, 1, sn)
		ch <- prometheus.MustNewConstMetric(c.statusAge, prometheus.GaugeValue, c.staleAge, sn)
		ch <- prometheus.MustNewConstMetric(c.playersOn, prometheus.GaugeValue, 0, sn)
		return
	}

	ageSecs := float64(time.Now().UnixMilli()-st.Timestamp) / 1000.0
	if ageSecs < 0 {
		ageSecs = 0
	}
	ch <- prometheus.MustNewConstMetric(c.statusAge, prometheus.GaugeValue, ageSecs, sn)

	stale := 0.0
	if ageSecs > c.staleAge {
		stale = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.statusStale, prometheus.GaugeValue, stale, sn)

	up := 0.0
	if st.Alive && stale == 0 {
		up = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up, sn)
	ch <- prometheus.MustNewConstMetric(c.playersOn, prometheus.GaugeValue, float64(st.PlayerCount), sn)
	ch <- prometheus.MustNewConstMetric(c.cmdProc, prometheus.CounterValue, float64(st.Stats.Processed), sn)
	ch <- prometheus.MustNewConstMetric(c.cmdFailed, prometheus.CounterValue, float64(st.Stats.Failed), sn)

	for _, p := range st.Players {
		ch <- prometheus.MustNewConstMetric(c.playerInfo, prometheus.GaugeValue, 1, sn, p)
	}

	raw, err = os.ReadFile(startupPath)
	if err == nil {
		var su startupFile
		if json.Unmarshal(raw, &su) == nil && su.StartTime > 0 {
			ch <- prometheus.MustNewConstMetric(c.startTime, prometheus.GaugeValue,
				float64(su.StartTime)/1000.0, sn)
		}
	}
}

// perkLogMetrics are plain Prometheus counters (not part of the
// file-snapshot collector above) driven by the PerkLog.txt tailer, which
// is event-based rather than poll-based.
type perkLogMetrics struct {
	logins   *prometheus.CounterVec
	deaths   *prometheus.CounterVec
	levelUps *prometheus.CounterVec
	newChars *prometheus.CounterVec
}

func newPerkLogMetrics(reg *prometheus.Registry) *perkLogMetrics {
	m := &perkLogMetrics{
		logins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zomboid_logins_total",
			Help: "Total player logins observed in PerkLog.txt",
		}, []string{"server"}),
		deaths: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zomboid_deaths_total",
			Help: "Total player deaths observed in PerkLog.txt",
		}, []string{"server"}),
		levelUps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zomboid_skill_levelups_total",
			Help: "Total skill level-up events observed in PerkLog.txt",
		}, []string{"server", "skill"}),
		newChars: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zomboid_characters_created_total",
			Help: "Total new characters (fresh spawns/respawns) observed in PerkLog.txt",
		}, []string{"server"}),
	}
	reg.MustRegister(m.logins, m.deaths, m.levelUps, m.newChars)
	return m
}

// runPerkLogPipeline processes PerkLog.txt for the lifetime of ctx,
// updating Prometheus counters for every event and, if db is non-nil,
// persisting richer per-event detail (death locations, skill history,
// character lifecycle) to it.
//
// With db configured: uses pollPerkLogsWithHistory, which checkpoints a
// byte offset per file in the DB itself -- a first-ever run backfills
// every historical PerkLog.txt from the start, and any gap from exporter
// downtime is caught up automatically on restart, all from the same code
// path, with no separate backfill step and no re-processing of content
// already checkpointed.
//
// With db nil: there's nowhere to persist a checkpoint, so it falls back
// to tailPerkLogLive, which only follows the newest file from EOF forward
// (live Prometheus counters only, no history -- the same behavior this
// exporter always had before persistence existed).
func runPerkLogPipeline(ctx context.Context, dataPath, serverName string, metrics *perkLogMetrics, db eventStore) {
	onEvent := func(ev *perkEvent) {
		switch ev.Kind {
		case "login":
			metrics.logins.WithLabelValues(serverName).Inc()
			if db != nil {
				db.handleLogin(ctx, ev)
			}
		case "died":
			metrics.deaths.WithLabelValues(serverName).Inc()
			if db != nil {
				db.handleDied(ctx, ev)
			}
		case "created_player":
			metrics.newChars.WithLabelValues(serverName).Inc()
			if db != nil {
				db.handleCreatedPlayer(ctx, ev)
			}
		case "level_changed":
			metrics.levelUps.WithLabelValues(serverName, ev.SkillName).Inc()
			if db != nil {
				db.handleLevelChanged(ctx, ev)
			}
		case "skills":
			// No dedicated Prometheus counter -- this is a point-in-time
			// snapshot, not a discrete event. Only meaningful with a DB to
			// store the history in.
			if db != nil {
				db.handleSkills(ctx, ev)
			}
		}
	}

	if db != nil {
		pollPerkLogsWithHistory(ctx, dataPath, db, onEvent)
	} else {
		tailPerkLogLive(ctx, dataPath, onEvent)
	}
}

func main() {
	dataPath := flag.String("data-path", "/data", "Zomboid data directory (contains Lua/panelbridge/ and Logs/)")
	serverName := flag.String("server-name", "", "Server name — must match the directory under Lua/panelbridge/")
	listenAddr := flag.String("web.listen-address", ":9091", "Address on which to expose metrics")
	stale := flag.Duration("stale-threshold", 120*time.Second, "Status file age above which server is considered stale/down")
	dbDSN := flag.String("db-dsn", "", "Optional external Postgres DSN (e.g. postgres://user:pass@host:5432/dbname). Takes priority over --sqlite-path if both are set.")
	sqlitePath := flag.String("sqlite-path", "", "Optional path to a SQLite database file (created if missing) -- zero-external-dependency alternative to --db-dsn. Must point at a writable location (the default --data-path mount is typically read-only). If neither this nor --db-dsn is set, PerkLog events still drive Prometheus counters but are not persisted -- player/death/skill history and leaderboards need one of them.")
	mqttBroker := flag.String("mqtt-broker", "", "Optional MQTT broker URL (e.g. tcp://mosquitto.mqtt.svc.cluster.local:1883) for live ExporterLog event publishing. Publishes each event to zomboid/<server-name>/<event-type>. If unset, MQTT publishing is disabled -- this is purely additive to Postgres/SQLite persistence.")
	mqttUsername := flag.String("mqtt-username", "", "MQTT broker username, if auth is required (password read from MQTT_PASSWORD env var, never a flag)")
	flag.Parse()

	if *serverName == "" {
		slog.Error("--server-name is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := prometheus.NewRegistry()
	reg.MustRegister(newCollector(*dataPath, *serverName, stale.Seconds()))
	perkMetrics := newPerkLogMetrics(reg)

	var db eventStore
	switch {
	case *dbDSN != "":
		pg, err := newPgStore(ctx, *dbDSN, *serverName)
		if err != nil {
			// Deliberately non-fatal: a DB outage shouldn't take down
			// Prometheus scraping, which is the exporter's primary job.
			slog.Error("failed to connect to Postgres -- continuing without persistence", "err", err)
		} else {
			defer pg.Close()
			db = pg
			slog.Info("connected to Postgres, event history + leaderboards enabled")
		}
	case *sqlitePath != "":
		sq, err := newSQLiteStore(ctx, *sqlitePath, *serverName)
		if err != nil {
			slog.Error("failed to open SQLite database -- continuing without persistence", "path", *sqlitePath, "err", err)
		} else {
			defer sq.Close()
			db = sq
			slog.Info("opened SQLite database, event history + leaderboards enabled", "path", *sqlitePath)
		}
	default:
		slog.Info("no --db-dsn or --sqlite-path set, running Prometheus-only (no player/death/skill history)")
	}

	var mqttPub *mqttPublisher
	if *mqttBroker != "" {
		var err error
		mqttPub, err = newMQTTPublisher(*mqttBroker, "zomboid/"+*serverName, *mqttUsername, os.Getenv("MQTT_PASSWORD"))
		if err != nil {
			slog.Error("failed to connect to MQTT broker -- continuing without live event publishing", "broker", *mqttBroker, "err", err)
			mqttPub = nil
		} else {
			defer mqttPub.close()
			slog.Info("connected to MQTT broker, publishing live events", "broker", *mqttBroker, "topicPrefix", "zomboid/"+*serverName)
		}
	}

	go runPerkLogPipeline(ctx, *dataPath, *serverName, perkMetrics, db)
	go runExporterLogPipeline(ctx, *dataPath, db, mqttPub)
	go runTWRLogPipeline(ctx, *dataPath, db)
	go runConnectionsPipeline(ctx, *dataPath, db)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: *listenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("zomboid-exporter started", "listen", *listenAddr, "server", *serverName, "data", *dataPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}
