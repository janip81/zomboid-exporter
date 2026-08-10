package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// fetchServerStartTime scrapes the exporter's own Prometheus endpoint for
// zomboid_server_start_time_seconds{server="<serverName>"} rather than
// standing up a second, independent uptime source -- the exporter already
// computes this from startup.json (see main.go in the exporter repo root).
func fetchServerStartTime(metricsURL, serverName string) (time.Time, error) {
	resp, err := http.Get(metricsURL)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}

	want := fmt.Sprintf(`zomboid_server_start_time_seconds{server="%s"}`, serverName)
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, want) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sec, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(int64(sec), 0), nil
	}
	return time.Time{}, fmt.Errorf("metric %s not found at %s", want, metricsURL)
}
