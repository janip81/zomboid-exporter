package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExporterLog.txt is written by the ExporterLog Lua mod via writeLog(),
// one file per server start, under Logs/logs_YYYY-MM-DD/ -- the same
// [timestamp] envelope PZ's native PerkLog.txt uses (see perklog.go),
// but the payload itself is a single flat JSON object the mod controls
// entirely, e.g. (real sample from a live server):
//
//	[07-08-26 16:40:34.717] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","x":9907,"y":7843,"z":0,"zombieKills":718}.
//
// Deliberately generic on this side: "type"/"steamId"/"username" are
// pulled out because they drive player/character linkage, but every
// field (including those three again) is kept in Fields and stored
// as-is in events.details. Adding a new tracked stat in the Lua mod
// never requires a Go or schema change.

type exporterEvent struct {
	Timestamp time.Time
	EventType string
	SteamID   string // may be "" if the emitting Lua code never resolved one
	Username  string
	Fields    map[string]any // full decoded JSON payload
}

func parseExporterLogLine(line string) *exporterEvent {
	i := strings.IndexByte(line, ']')
	if i < 0 || len(line) < 2 || line[0] != '[' {
		return nil
	}
	ts, err := time.Parse(perkLogTimeLayout, line[1:i])
	if err != nil {
		return nil
	}
	rest := strings.TrimSuffix(strings.TrimSpace(line[i+1:]), ".")

	var fields map[string]any
	if err := json.Unmarshal([]byte(rest), &fields); err != nil {
		return nil
	}
	eventType, _ := fields["type"].(string)
	if eventType == "" {
		return nil
	}
	steamID, _ := fields["steamId"].(string)
	username, _ := fields["username"].(string)

	return &exporterEvent{
		Timestamp: ts,
		EventType: eventType,
		SteamID:   steamID,
		Username:  username,
		Fields:    fields,
	}
}

// canonicalizeExporterFields returns fields with "steamId" replaced by
// canonicalSteamID, per steamid64-canonicalization-and-lua-precision.md's
// "Canonicalize the event JSON too": a raw Lua-derived steamId stored
// verbatim in events.details is easy to mistake for authoritative
// identity later (dashboards, Curator, debugging). The original
// Lua-derived value is kept under "_luaSteamId" only when it actually
// differs, as a diagnostic breadcrumb -- never as identity. Returns
// fields unchanged (no clone, no allocation) when there's nothing to
// canonicalize, which is the common case once the steamIDByUsername
// cache is warm.
func canonicalizeExporterFields(fields map[string]any, canonicalSteamID string) map[string]any {
	luaVal, ok := fields["steamId"].(string)
	if !ok || luaVal == canonicalSteamID {
		return fields
	}
	out := maps.Clone(fields)
	out["steamId"] = canonicalSteamID
	out["_luaSteamId"] = luaVal
	return out
}

// listExporterLogs returns every ExporterLog.txt under dataPath, oldest
// first -- same dual-location handling as listPerkLogs (see its comment):
// the live session's file sits flat in Logs/, only moving under
// Logs/logs_YYYY-MM-DD/ once archived by the next restart.
func listExporterLogs(dataPath string) []string {
	flat, err := filepath.Glob(filepath.Join(dataPath, "Logs", "*_ExporterLog.txt"))
	if err != nil {
		return nil
	}
	archived, err := filepath.Glob(filepath.Join(dataPath, "Logs", "logs_*", "*_ExporterLog.txt"))
	if err != nil {
		return nil
	}
	matches := append(flat, archived...)
	sort.Slice(matches, func(i, j int) bool {
		return filepath.Base(matches[i]) < filepath.Base(matches[j])
	})
	return matches
}

// readNewExporterLines mirrors readNewLines in perklog.go, parsing with
// parseExporterLogLine instead.
func readNewExporterLines(path string, fromOffset int64) (events []*exporterEvent, newOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()

	if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
		return nil, fromOffset, err
	}

	reader := bufio.NewReader(f)
	newOffset = fromOffset
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// Partial line (or clean EOF) -- stop here, don't advance past it.
			break
		}
		newOffset += int64(len(line))
		if ev := parseExporterLogLine(strings.TrimRight(line, "\r\n")); ev != nil {
			events = append(events, ev)
		}
	}
	return events, newOffset, nil
}

// pollExporterOnce mirrors pollOnce in perklog.go for ExporterLog.txt
// files, sharing the same processed_files checkpoint table (paths never
// collide between the two log kinds).
func pollExporterOnce(ctx context.Context, dataPath string, db eventStore, done map[string]bool, onEvent func(*exporterEvent)) {
	files := listExporterLogs(dataPath)
	if len(files) == 0 {
		return
	}
	newest := files[len(files)-1]

	for _, path := range files {
		key := filepath.Base(path) // see listPerkLogs' comment on why
		if done[key] {
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		offset, err := db.getFileOffset(ctx, key)
		if err != nil {
			slog.Warn("getFileOffset failed", "path", path, "err", err)
			continue
		}

		if info.Size() <= offset {
			if path != newest {
				done[key] = true
			}
			continue
		}

		events, newOffset, err := readNewExporterLines(path, offset)
		if err != nil {
			slog.Warn("readNewExporterLines failed", "path", path, "err", err)
			continue
		}
		for _, ev := range events {
			select {
			case <-ctx.Done():
				return
			default:
			}
			onEvent(ev)
		}
		if err := db.setFileOffset(ctx, key, newOffset); err != nil {
			slog.Warn("setFileOffset failed", "path", path, "err", err)
		}
		if path != newest && newOffset >= info.Size() {
			done[key] = true
		}
	}
}

// pollExporterLogsWithHistory is the ExporterLog.txt equivalent of
// pollPerkLogsWithHistory -- there's no no-persistence fallback here
// (unlike PerkLog, ExporterLog events drive no Prometheus counters of
// their own; they're only meaningful with a DB to store them in), so
// callers must only invoke this when db != nil.
func pollExporterLogsWithHistory(ctx context.Context, dataPath string, db eventStore, onEvent func(*exporterEvent)) {
	done := make(map[string]bool)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	pollExporterOnce(ctx, dataPath, db, done, onEvent)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollExporterOnce(ctx, dataPath, db, done, onEvent)
		}
	}
}

// runExporterLogPipeline processes ExporterLog.txt for the lifetime of
// ctx. A no-op when db is nil -- see pollExporterLogsWithHistory. pub may
// be nil (MQTT publishing disabled); publishing is best-effort and never
// gates or delays the Postgres/SQLite write.
func runExporterLogPipeline(ctx context.Context, dataPath string, db eventStore, pub *mqttPublisher) {
	if db == nil {
		return
	}
	pollExporterLogsWithHistory(ctx, dataPath, db, func(ev *exporterEvent) {
		db.handleExporterEvent(ctx, ev)
		pub.publish(ev)
	})
}
