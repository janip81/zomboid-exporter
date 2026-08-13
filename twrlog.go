package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ThoseWhoRemainLog.txt is written by the ThoseWhoRemain Lua mod's
// TWR.Emit.jobResult(), one file per server start under Logs/logs_*/,
// same [timestamp] envelope as PerkLog.txt/ExporterLog.txt (see
// perklog.go), but deliberately a SEPARATE file/table from
// ExporterLog.txt -- see
// zomboid-exporter-ideas/antagonist/spawn-result-tracking.md: TWR's
// world-mutation job outcomes are control-plane/audit records, not
// gameplay telemetry, and the two mods must stay independently
// installable/parseable.
//
// Real sample line shape (from Emit.jobResult's flat encoder):
//
//	[13-08-26 14:02:11.408] {"type":"twr_job_result","jobId":"debug-mapscatter-482910337-1","attemptNo":1,"actionType":"scatter_items","mechanic":"Container.scatterAcrossMap","result":"applied","placed":1,"requested":1,"artifactKey":"debug-mapscatter-482910337-1-artifact","x":10090,"y":8260,"z":0,"targetType":"container"}.
//
// Only "twr_job_result" is handled today -- ResultType is still pulled
// out generically (like exporterEvent.EventType) so a future TWR event
// type doesn't require touching the parser, only handleTWRJobResult.
type twrEvent struct {
	Timestamp time.Time
	Type      string
	Fields    map[string]any // full decoded JSON payload
}

func parseTWRLogLine(line string) *twrEvent {
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
	evType, _ := fields["type"].(string)
	if evType == "" {
		return nil
	}

	return &twrEvent{Timestamp: ts, Type: evType, Fields: fields}
}

// listTWRLogs mirrors listExporterLogs for *_ThoseWhoRemainLog.txt.
func listTWRLogs(dataPath string) []string {
	flat, err := filepath.Glob(filepath.Join(dataPath, "Logs", "*_ThoseWhoRemainLog.txt"))
	if err != nil {
		return nil
	}
	archived, err := filepath.Glob(filepath.Join(dataPath, "Logs", "logs_*", "*_ThoseWhoRemainLog.txt"))
	if err != nil {
		return nil
	}
	matches := append(flat, archived...)
	sort.Slice(matches, func(i, j int) bool {
		return filepath.Base(matches[i]) < filepath.Base(matches[j])
	})
	return matches
}

// readNewTWRLines mirrors readNewExporterLines, parsing with
// parseTWRLogLine instead.
func readNewTWRLines(path string, fromOffset int64) (events []*twrEvent, newOffset int64, err error) {
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
			break
		}
		newOffset += int64(len(line))
		if ev := parseTWRLogLine(strings.TrimRight(line, "\r\n")); ev != nil {
			events = append(events, ev)
		}
	}
	return events, newOffset, nil
}

// pollTWROnce mirrors pollExporterOnce, sharing the same processed_files
// checkpoint table (paths never collide across the three log kinds).
func pollTWROnce(ctx context.Context, dataPath string, db eventStore, done map[string]bool, onEvent func(*twrEvent)) {
	files := listTWRLogs(dataPath)
	if len(files) == 0 {
		return
	}
	newest := files[len(files)-1]

	for _, path := range files {
		key := filepath.Base(path)
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

		events, newOffset, err := readNewTWRLines(path, offset)
		if err != nil {
			slog.Warn("readNewTWRLines failed", "path", path, "err", err)
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

// pollTWRLogsWithHistory is the ThoseWhoRemainLog.txt equivalent of
// pollExporterLogsWithHistory.
func pollTWRLogsWithHistory(ctx context.Context, dataPath string, db eventStore, onEvent func(*twrEvent)) {
	done := make(map[string]bool)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	pollTWROnce(ctx, dataPath, db, done, onEvent)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollTWROnce(ctx, dataPath, db, done, onEvent)
		}
	}
}

// twrStringField/twrIntField pull optional fields out of a twrEvent's
// generic JSON payload -- encoding/json decodes numbers into float64
// when unmarshaled into map[string]any, hence the float64 conversion.
// Shared by both store implementations' handleTWRJobResult.
func twrStringField(fields map[string]any, key string) string {
	v, _ := fields[key].(string)
	return v
}

func twrIntField(fields map[string]any, key string) (int, bool) {
	v, ok := fields[key].(float64)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// runTWRLogPipeline processes ThoseWhoRemainLog.txt for the lifetime of
// ctx. A no-op when db is nil, same reasoning as
// runExporterLogPipeline: these events are only meaningful with a DB to
// store them in.
func runTWRLogPipeline(ctx context.Context, dataPath string, db eventStore) {
	if db == nil {
		return
	}
	pollTWRLogsWithHistory(ctx, dataPath, db, func(ev *twrEvent) {
		if ev.Type != "twr_job_result" {
			return
		}
		db.handleTWRJobResult(ctx, ev)
	})
}
