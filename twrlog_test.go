package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseTWRLogLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *twrEvent // nil means "must fail to parse"
	}{
		{
			name: "successful scatter, real shape from Emit.jobResult",
			line: `[13-08-26 14:02:11.408] {"type":"twr_job_result","jobId":"debug-mapscatter-482910337-1","attemptNo":1,"actionType":"scatter_items","mechanic":"Container.scatterAcrossMap","result":"applied","placed":1,"requested":1,"artifactKey":"debug-mapscatter-482910337-1-artifact","x":10090,"y":8260,"z":0,"targetType":"container"}.`,
			want: &twrEvent{Type: "twr_job_result"},
		},
		{
			name: "final error, no eligible target",
			line: `[13-08-26 14:05:00.000] {"type":"twr_job_result","jobId":"debug-mapscatter-1-4","attemptNo":3,"actionType":"scatter_items","mechanic":"Container.scatterAcrossMap","result":"final_error","errorCode":"NO_ELIGIBLE_TARGET","errorDetail":"no existing container found within radius 15 after 3 attempt(s)","placed":0,"requested":1,"x":14099,"y":2810,"z":0}.`,
			want: &twrEvent{Type: "twr_job_result"},
		},
		{
			name: "garbage line does not parse",
			line: `this is not a TWR log line at all`,
			want: nil,
		},
		{
			name: "valid timestamp but invalid JSON payload",
			line: `[13-08-26 14:02:11.408] not json.`,
			want: nil,
		},
		{
			name: "valid JSON but missing type field",
			line: `[13-08-26 14:02:11.408] {"jobId":"debug-1"}.`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTWRLogLine(tc.line)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected parse failure, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil (parse failed)", tc.want)
			}
			if got.Type != tc.want.Type {
				t.Fatalf("mismatch: got type=%q want type=%q", got.Type, tc.want.Type)
			}
			if got.Fields == nil {
				t.Fatal("Fields must be populated with the full decoded payload")
			}
		})
	}
}

func TestParseTWRLogLine_TimestampAndFields(t *testing.T) {
	line := `[13-08-26 14:02:11.408] {"type":"twr_job_result","jobId":"debug-mapscatter-1-1","result":"applied","placed":1,"x":10090,"y":8260,"z":0}.`
	got := parseTWRLogLine(line)
	if got == nil {
		t.Fatal("expected successful parse")
	}
	want, err := time.Parse(perkLogTimeLayout, "13-08-26 14:02:11.408")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(want) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp, want)
	}
	if got.Fields["jobId"] != "debug-mapscatter-1-1" {
		t.Fatalf("expected jobId preserved in Fields, got %v", got.Fields["jobId"])
	}
	if got.Fields["placed"] != float64(1) {
		t.Fatalf("expected placed=1 preserved in Fields, got %v", got.Fields["placed"])
	}
}

func TestTwrStringFieldAndTwrIntField(t *testing.T) {
	fields := map[string]any{"jobId": "abc", "placed": float64(3), "notANumber": "x"}
	if got := twrStringField(fields, "jobId"); got != "abc" {
		t.Fatalf("twrStringField: got %q want %q", got, "abc")
	}
	if got := twrStringField(fields, "missing"); got != "" {
		t.Fatalf("twrStringField for missing key: got %q want empty", got)
	}
	if got, ok := twrIntField(fields, "placed"); !ok || got != 3 {
		t.Fatalf("twrIntField: got %d ok=%v want 3,true", got, ok)
	}
	if _, ok := twrIntField(fields, "notANumber"); ok {
		t.Fatal("twrIntField should fail on a non-numeric field")
	}
	if _, ok := twrIntField(fields, "missing"); ok {
		t.Fatal("twrIntField should fail on a missing field")
	}
}

const twrAppliedLine = `[13-08-26 14:02:11.408] {"type":"twr_job_result","jobId":"debug-1-1","result":"applied","placed":1,"requested":1,"artifactKey":"debug-1-1-artifact","x":10090,"y":8260,"z":0,"targetType":"container","mechanic":"Container.scatterAcrossMap"}.` + "\n"
const twrErrorLine = `[13-08-26 14:02:20.000] {"type":"twr_job_result","jobId":"debug-1-2","result":"final_error","errorCode":"NO_ELIGIBLE_TARGET","x":14099,"y":2810,"z":0,"mechanic":"Container.scatterAcrossMap"}.` + "\n"

func TestPollTWROnce_CatchesUpFreshAndSkipsWhenNoNewContent(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-13")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-13_14-00_ThoseWhoRemainLog.txt")
	if err := os.WriteFile(logPath, []byte(twrAppliedLine+twrErrorLine), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	onEvent := func(ev *twrEvent) { store.handleTWRJobResult(ctx, ev) }

	done := make(map[string]bool)
	pollTWROnce(ctx, dir, store, done, onEvent)

	if store.twrJobResults != 2 {
		t.Fatalf("expected 2 twr job results, got %d", store.twrJobResults)
	}
	info, _ := os.Stat(logPath)
	if store.offsets[filepath.Base(logPath)] != info.Size() {
		t.Fatalf("offset should equal file size after full read: got %d want %d", store.offsets[filepath.Base(logPath)], info.Size())
	}

	// Second poll, no new content -- must be a complete no-op.
	pollTWROnce(ctx, dir, store, done, onEvent)
	if store.twrJobResults != 2 {
		t.Fatalf("second poll re-processed content: twrJobResults=%d", store.twrJobResults)
	}
}

func TestPollTWROnce_RestartResumesFromPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-13")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-13_14-00_ThoseWhoRemainLog.txt")
	if err := os.WriteFile(logPath, []byte(twrAppliedLine+twrErrorLine), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	store.offsets[filepath.Base(logPath)] = int64(len(twrAppliedLine))

	onEvent := func(ev *twrEvent) { store.handleTWRJobResult(ctx, ev) }
	pollTWROnce(ctx, dir, store, make(map[string]bool), onEvent)

	if store.twrJobResults != 1 {
		t.Fatalf("expected only the line past the checkpoint to be processed, got twrJobResults=%d", store.twrJobResults)
	}
}

func TestRunTWRLogPipeline_IgnoresUnknownEventTypes(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-13")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-13_14-00_ThoseWhoRemainLog.txt")
	otherTypeLine := `[13-08-26 14:03:00.000] {"type":"twr_future_event","foo":"bar"}.` + "\n"
	if err := os.WriteFile(logPath, []byte(twrAppliedLine+otherTypeLine), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	onEvent := func(ev *twrEvent) {
		if ev.Type != "twr_job_result" {
			return
		}
		store.handleTWRJobResult(ctx, ev)
	}
	pollTWROnce(ctx, dir, store, make(map[string]bool), onEvent)

	if store.twrJobResults != 1 {
		t.Fatalf("expected only the twr_job_result line to be dispatched, got twrJobResults=%d", store.twrJobResults)
	}
}
