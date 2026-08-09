package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseExporterLogLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *exporterEvent // nil means "must fail to parse"
	}{
		{
			name: "kill, real sample from a live server",
			line: `[07-08-26 16:40:34.717] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","x":9907,"y":7843,"z":0,"zombieKills":718}.`,
			want: &exporterEvent{
				EventType: "kill",
				SteamID:   "76561197965988300",
				Username:  "Edd1e360",
			},
		},
		{
			name: "event with no steamId (e.g. a hookTimedActionOnce line predating the steamId fix)",
			line: `[07-08-26 16:40:34.717] {"type":"read","username":"Edd1e360","item":"Base.Novel"}.`,
			want: &exporterEvent{
				EventType: "read",
				SteamID:   "",
				Username:  "Edd1e360",
			},
		},
		{
			name: "garbage line does not parse",
			line: `this is not an ExporterLog line at all`,
			want: nil,
		},
		{
			name: "valid timestamp but invalid JSON payload",
			line: `[07-08-26 16:40:34.717] not json.`,
			want: nil,
		},
		{
			name: "valid JSON but missing type field",
			line: `[07-08-26 16:40:34.717] {"steamId":"76561197965988300","username":"Edd1e360"}.`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseExporterLogLine(tc.line)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected parse failure, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil (parse failed)", tc.want)
			}
			if got.EventType != tc.want.EventType || got.SteamID != tc.want.SteamID || got.Username != tc.want.Username {
				t.Fatalf("mismatch:\n got:  %+v\n want: %+v", got, tc.want)
			}
			if got.Fields == nil {
				t.Fatal("Fields must be populated with the full decoded payload")
			}
		})
	}
}

func TestParseExporterLogLine_TimestampAndFields(t *testing.T) {
	line := `[07-08-26 16:40:34.717] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","x":9907,"y":7843,"z":0,"zombieKills":718}.`
	got := parseExporterLogLine(line)
	if got == nil {
		t.Fatal("expected successful parse")
	}
	want, err := time.Parse(perkLogTimeLayout, "07-08-26 16:40:34.717")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(want) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp, want)
	}
	if got.Fields["zombieKills"] != float64(718) {
		t.Fatalf("expected zombieKills=718 preserved in Fields, got %v", got.Fields["zombieKills"])
	}
	if got.Fields["x"] != float64(9907) {
		t.Fatalf("expected x=9907 preserved in Fields, got %v", got.Fields["x"])
	}
}

const killLine = `[07-08-26 16:40:34.717] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","x":9907,"y":7843,"z":0,"zombieKills":718}.` + "\n"
const killLine2 = `[07-08-26 16:40:45.583] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","x":9907,"y":7844,"z":0,"zombieKills":719}.` + "\n"

func TestPollExporterOnce_CatchesUpFreshAndSkipsWhenNoNewContent(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-07")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-07_16-33_ExporterLog.txt")
	if err := os.WriteFile(logPath, []byte(killLine+killLine2), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	var events []*exporterEvent
	onEvent := func(ev *exporterEvent) { events = append(events, ev); store.handleExporterEvent(ctx, ev) }

	done := make(map[string]bool)
	pollExporterOnce(ctx, dir, store, done, onEvent)

	if store.exporterEvents != 2 {
		t.Fatalf("expected 2 exporter events, got %d", store.exporterEvents)
	}
	info, _ := os.Stat(logPath)
	if store.offsets[logPath] != info.Size() {
		t.Fatalf("offset should equal file size after full read: got %d want %d", store.offsets[logPath], info.Size())
	}

	// Second poll, no new content -- must be a complete no-op.
	pollExporterOnce(ctx, dir, store, done, onEvent)
	if store.exporterEvents != 2 {
		t.Fatalf("second poll re-processed content: exporterEvents=%d", store.exporterEvents)
	}
}

func TestPollExporterOnce_RestartResumesFromPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-07")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-07_16-33_ExporterLog.txt")
	if err := os.WriteFile(logPath, []byte(killLine+killLine2), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	store.offsets[logPath] = int64(len(killLine))

	var events []*exporterEvent
	onEvent := func(ev *exporterEvent) { events = append(events, ev); store.handleExporterEvent(ctx, ev) }
	pollExporterOnce(ctx, dir, store, make(map[string]bool), onEvent)

	if store.exporterEvents != 1 {
		t.Fatalf("expected only the line past the checkpoint to be processed, got exporterEvents=%d", store.exporterEvents)
	}
}

func TestPollExporterOnce_PartialTrailingLineNotConsumed(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-07")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-07_16-33_ExporterLog.txt")
	partial := killLine + `[07-08-26 16:41:00.000] {"type":"kill","steamId":"76561197965988300","username":"Edd1e360","zombieKills":720` // no closing brace/newline, mid-write
	if err := os.WriteFile(logPath, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	var events []*exporterEvent
	onEvent := func(ev *exporterEvent) { events = append(events, ev); store.handleExporterEvent(ctx, ev) }
	pollExporterOnce(ctx, dir, store, make(map[string]bool), onEvent)

	if len(events) != 1 {
		t.Fatalf("expected only the complete kill line to be processed, got %d events", len(events))
	}
	if store.offsets[logPath] != int64(len(killLine)) {
		t.Fatalf("offset should stop right after the last complete line: got %d want %d", store.offsets[logPath], len(killLine))
	}

	// Now "finish" the write with a newline, and poll again.
	if err := os.WriteFile(logPath, []byte(partial+`}.`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pollExporterOnce(ctx, dir, store, make(map[string]bool), onEvent)
	if len(events) != 2 {
		t.Fatalf("expected the completed line to be picked up on the next poll, got %d events total", len(events))
	}
}
