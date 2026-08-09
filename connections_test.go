package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseConnectionsLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *sessionEvent // nil means "must fail to parse"
	}{
		{
			name: "fully-connected, real sample from a live server",
			line: `[09-08-26 16:22:23.354] event="fully-connected" message="" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="UDPRakNet".`,
			want: &sessionEvent{Kind: "session_start", SteamID: "76561197965988309", Username: "Edd1e360"},
		},
		{
			name: "disconnect/receive-disconnect, real sample from a live server",
			line: `[09-08-26 16:25:38.912] event="disconnect" message="receive-disconnect" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="Disconnected".`,
			want: &sessionEvent{Kind: "session_end", SteamID: "76561197965988309", Username: "Edd1e360"},
		},
		{
			name: "early handshake noise (steam-id=0, username=null) is ignored",
			line: `[09-08-26 16:21:34.990] event="RakNet" message="new-incoming-connection" guid="945755943418191891" ip="null" steam-id="0" role="" username="null" connection-type="Disconnected".`,
			want: nil,
		},
		{
			name: "client-connect (intermediate handshake step, not a real session boundary) is ignored",
			line: `[09-08-26 16:21:36.605] event="receive-packet" message="client-connect" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="UDPRakNet".`,
			want: nil,
		},
		{
			name: "disconnection-notification (missing steam-id/username entirely) is ignored",
			line: `[09-08-26 16:25:38.892] event="RakNet" message="disconnection-notification" guid="945755943418191891".`,
			want: nil,
		},
		{
			name: "garbage line does not parse",
			line: `this is not a connections.txt line at all`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConnectionsLine(tc.line)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected parse failure, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil (parse failed)", tc.want)
			}
			if got.Kind != tc.want.Kind || got.SteamID != tc.want.SteamID || got.Username != tc.want.Username {
				t.Fatalf("mismatch:\n got:  %+v\n want: %+v", got, tc.want)
			}
		})
	}
}

func TestParseConnectionsLine_Timestamp(t *testing.T) {
	line := `[09-08-26 16:22:23.354] event="fully-connected" message="" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="UDPRakNet".`
	got := parseConnectionsLine(line)
	if got == nil {
		t.Fatal("expected successful parse")
	}
	want, err := time.Parse(perkLogTimeLayout, "09-08-26 16:22:23.354")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(want) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp, want)
	}
}

const fullyConnectedLine = `[09-08-26 16:22:23.354] event="fully-connected" message="" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="UDPRakNet".` + "\n"
const disconnectLine = `[09-08-26 16:25:38.912] event="disconnect" message="receive-disconnect" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="Disconnected".` + "\n"

func TestPollConnectionsOnce_CatchesUpFreshAndSkipsWhenNoNewContent(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "Logs", "logs_2026-08-09")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "2026-08-09_16-19_connections.txt")
	if err := os.WriteFile(logPath, []byte(fullyConnectedLine+disconnectLine), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := newFakeStore()
	var events []*sessionEvent
	onEvent := func(ev *sessionEvent) { events = append(events, ev); store.handleSessionEvent(ctx, ev) }

	done := make(map[string]bool)
	pollConnectionsOnce(ctx, dir, store, done, onEvent)

	if store.sessionEvents != 2 {
		t.Fatalf("expected 2 session events (start+end), got %d", store.sessionEvents)
	}
	info, _ := os.Stat(logPath)
	if store.offsets[filepath.Base(logPath)] != info.Size() {
		t.Fatalf("offset should equal file size after full read: got %d want %d", store.offsets[filepath.Base(logPath)], info.Size())
	}

	pollConnectionsOnce(ctx, dir, store, done, onEvent)
	if store.sessionEvents != 2 {
		t.Fatalf("second poll re-processed content: sessionEvents=%d", store.sessionEvents)
	}
}

func TestListConnectionsLogs_FindsBothFlatAndArchivedFiles(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "Logs")
	archivedDir := filepath.Join(logsDir, "logs_2026-08-06")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	flatPath := filepath.Join(logsDir, "2026-08-06_10-00_connections.txt")
	archivedPath := filepath.Join(archivedDir, "2026-08-06_08-00_connections.txt")
	if err := os.WriteFile(flatPath, []byte(fullyConnectedLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivedPath, []byte(fullyConnectedLine), 0o644); err != nil {
		t.Fatal(err)
	}

	files := listConnectionsLogs(dir)
	if len(files) != 2 {
		t.Fatalf("expected both the flat (live) and archived file to be found, got %d: %v", len(files), files)
	}
	if filepath.Base(files[0]) != "2026-08-06_08-00_connections.txt" || filepath.Base(files[1]) != "2026-08-06_10-00_connections.txt" {
		t.Fatalf("expected chronological order by basename regardless of location, got %v", files)
	}
}
