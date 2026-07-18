package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

func main() {
	dataPath   := flag.String("data-path", "/data", "Zomboid data directory (contains Lua/panelbridge/)")
	serverName := flag.String("server-name", "", "Server name — must match the directory under Lua/panelbridge/")
	listenAddr := flag.String("web.listen-address", ":9091", "Address on which to expose metrics")
	stale      := flag.Duration("stale-threshold", 120*time.Second, "Status file age above which server is considered stale/down")
	flag.Parse()

	if *serverName == "" {
		slog.Error("--server-name is required")
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(newCollector(*dataPath, *serverName, stale.Seconds()))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	slog.Info("zomboid-exporter started", "listen", *listenAddr, "server", *serverName, "data", *dataPath)
	if err := http.ListenAndServe(*listenAddr, mux); err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}
