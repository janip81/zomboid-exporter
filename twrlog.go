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

// twrLine pairs a parsed event (nil if the line was malformed -- see
// spawn-result-tracking.md review Q8) with the file offset immediately
// after it, so pollTWROnce can commit the checkpoint per-line instead
// of only at the end of a whole poll batch.
type twrLine struct {
	ev          *twrEvent
	offsetAfter int64
}

// readNewTWRLines mirrors readNewExporterLines, parsing with
// parseTWRLogLine instead. Unlike ExporterLog's version, a malformed
// line does NOT get silently dropped -- these are control-plane audit
// records (spawn-result-tracking.md's whole point), so corruption must
// be visible. The line is still skipped (ev == nil) rather than
// retried forever -- a permanently malformed line would otherwise
// block every event after it indefinitely.
func readNewTWRLines(path string, fromOffset int64) (lines []twrLine, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(f)
	offset := fromOffset
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		offset += int64(len(line))
		trimmed := strings.TrimRight(line, "\r\n")
		ev := parseTWRLogLine(trimmed)
		if ev == nil {
			slog.Warn("malformed ThoseWhoRemainLog line, skipping", "path", path, "offsetAfterLine", offset)
		}
		lines = append(lines, twrLine{ev: ev, offsetAfter: offset})
	}
	return lines, nil
}

// pollTWROnce mirrors pollExporterOnce, sharing the same processed_files
// checkpoint table (paths never collide across the three log kinds),
// with one deliberate difference (spawn-result-tracking.md review Q4):
// onEvent returns whether the event was durably committed. On the
// first failure, the checkpoint is only advanced up to the last
// successfully committed line -- NOT past the failed one -- and this
// file is left off `done` so the same line is retried on the next
// poll tick rather than being silently skipped on a transient DB
// outage.
func pollTWROnce(ctx context.Context, dataPath string, db eventStore, done map[string]bool, onEvent func(*twrEvent) bool) {
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

		lines, err := readNewTWRLines(path, offset)
		if err != nil {
			slog.Warn("readNewTWRLines failed", "path", path, "err", err)
			continue
		}

		committed := offset
		allCommitted := true
		for _, ln := range lines {
			if ln.ev == nil {
				// Malformed -- already warned in readNewTWRLines. Safe
				// to skip past (nothing to durably commit).
				committed = ln.offsetAfter
				continue
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !onEvent(ln.ev) {
				allCommitted = false
				break
			}
			committed = ln.offsetAfter
		}

		if committed != offset {
			if err := db.setFileOffset(ctx, key, committed); err != nil {
				slog.Warn("setFileOffset failed", "path", path, "err", err)
			}
		}
		if !allCommitted {
			// Leave `done` unset -- retry this file (from the new,
			// still-behind checkpoint) on the next tick.
			continue
		}
		if path != newest && committed >= info.Size() {
			done[key] = true
		}
	}
}

// pollTWRLogsWithHistory is the ThoseWhoRemainLog.txt equivalent of
// pollExporterLogsWithHistory.
func pollTWRLogsWithHistory(ctx context.Context, dataPath string, db eventStore, onEvent func(*twrEvent) bool) {
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
	pollTWRLogsWithHistory(ctx, dataPath, db, func(ev *twrEvent) bool {
		if ev.Type != "twr_job_result" {
			// Not a durability concern -- an unrecognized (e.g. future)
			// event type is fine to skip past, same as an ExporterLog
			// event type this build doesn't know about.
			return true
		}
		// "accepted" (a quest-engine transport-acceptance receipt) is
		// routed to a separate handler -- see eventstore.go's
		// handleTWRJobAccepted comment (CGPT-G1-P3-01) for why it must
		// never reach handleTWRJobResult/twr_job_attempts: that table's
		// unique index would silently collide with the job's eventual
		// real outcome and discard it.
		if twrStringField(ev.Fields, "result") == "accepted" {
			if err := db.handleTWRJobAccepted(ctx, ev); err != nil {
				slog.Warn("handleTWRJobAccepted failed, will retry", "jobId", twrStringField(ev.Fields, "jobId"), "err", err)
				return false
			}
			return true
		}
		if err := db.handleTWRJobResult(ctx, ev); err != nil {
			slog.Warn("handleTWRJobResult failed, will retry", "jobId", twrStringField(ev.Fields, "jobId"), "err", err)
			return false
		}
		return true
	})
}
