package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// connections.txt is written natively by the dedicated server -- no mod
// required, same envelope as PerkLog.txt (see perklog.go), same
// flat-then-archived location behavior (see listPerkLogs' comment).
// Every line is a flat "key=\"value\"" record of one network event; the
// two that matter here are a player's actual session boundaries (not
// the handshake/queue noise in between), real samples from a live
// server:
//
//	[09-08-26 16:22:23.354] event="fully-connected" message="" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="UDPRakNet".
//	[09-08-26 16:25:38.912] event="disconnect" message="receive-disconnect" guid="945755943418191891" ip="10.244.8.29" steam-id="76561197965988309" role="user" username="Edd1e360" connection-type="Disconnected".
//
// Earlier handshake lines (new-incoming-connection, login,
// client-connect, login-queue-*, player-connect) carry steam-id="0"/
// username="null" or are simply pre-authentication noise -- ignored.
// ip is deliberately never stored -- these events land in the same
// generic events table a future public stats site may read from.
var connectionsKVRe = regexp.MustCompile(`(\w[\w-]*)="([^"]*)"`)

type sessionEvent struct {
	Timestamp time.Time
	Kind      string // "session_start" or "session_end"
	SteamID   string
	Username  string
}

func parseConnectionsLine(line string) *sessionEvent {
	i := strings.IndexByte(line, ']')
	if i < 0 || len(line) < 2 || line[0] != '[' {
		return nil
	}
	ts, err := time.Parse(perkLogTimeLayout, line[1:i])
	if err != nil {
		return nil
	}

	fields := make(map[string]string)
	for _, m := range connectionsKVRe.FindAllStringSubmatch(line[i+1:], -1) {
		fields[m[1]] = m[2]
	}

	steamID := fields["steam-id"]
	username := fields["username"]
	if steamID == "" || steamID == "0" || username == "" || username == "null" {
		return nil
	}

	var kind string
	switch {
	case fields["event"] == "fully-connected":
		kind = "session_start"
	case fields["event"] == "disconnect" && fields["message"] == "receive-disconnect":
		kind = "session_end"
	default:
		return nil
	}

	return &sessionEvent{
		Timestamp: ts,
		Kind:      kind,
		SteamID:   steamID,
		Username:  username,
	}
}

// listConnectionsLogs mirrors listPerkLogs -- see its comment for why
// both the flat (live session) and archived (Logs/logs_YYYY-MM-DD/)
// locations must be globbed.
func listConnectionsLogs(dataPath string) []string {
	flat, err := filepath.Glob(filepath.Join(dataPath, "Logs", "*_connections.txt"))
	if err != nil {
		return nil
	}
	archived, err := filepath.Glob(filepath.Join(dataPath, "Logs", "logs_*", "*_connections.txt"))
	if err != nil {
		return nil
	}
	matches := append(flat, archived...)
	sort.Slice(matches, func(i, j int) bool {
		return filepath.Base(matches[i]) < filepath.Base(matches[j])
	})
	return matches
}

func readNewConnectionsLines(path string, fromOffset int64) (events []*sessionEvent, newOffset int64, err error) {
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
		if ev := parseConnectionsLine(strings.TrimRight(line, "\r\n")); ev != nil {
			events = append(events, ev)
		}
	}
	return events, newOffset, nil
}

// pollConnectionsOnce mirrors pollOnce in perklog.go -- shares the same
// processed_files checkpoint table, basename-keyed (paths never collide
// across the three log kinds).
func pollConnectionsOnce(ctx context.Context, dataPath string, db eventStore, done map[string]bool, onEvent func(*sessionEvent)) {
	files := listConnectionsLogs(dataPath)
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

		events, newOffset, err := readNewConnectionsLines(path, offset)
		if err != nil {
			slog.Warn("readNewConnectionsLines failed", "path", path, "err", err)
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

// pollConnectionsWithHistory is the connections.txt equivalent of
// pollPerkLogsWithHistory -- session_start/session_end are only
// meaningful with a DB to store them in, so callers must only invoke
// this when db != nil.
func pollConnectionsWithHistory(ctx context.Context, dataPath string, db eventStore, onEvent func(*sessionEvent)) {
	done := make(map[string]bool)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	pollConnectionsOnce(ctx, dataPath, db, done, onEvent)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollConnectionsOnce(ctx, dataPath, db, done, onEvent)
		}
	}
}

// runConnectionsPipeline processes connections.txt for the lifetime of
// ctx. A no-op when db is nil -- see pollConnectionsWithHistory.
func runConnectionsPipeline(ctx context.Context, dataPath string, db eventStore) {
	if db == nil {
		return
	}
	pollConnectionsWithHistory(ctx, dataPath, db, func(ev *sessionEvent) {
		db.handleSessionEvent(ctx, ev)
	})
}
