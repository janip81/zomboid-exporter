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
	"strconv"
	"strings"
	"time"
)

// PerkLog.txt is written natively by the PZ dedicated server -- no mod
// required. One file per server start, under Logs/logs_YYYY-MM-DD/. Each
// player action writes a line like:
//
//	[06-08-26 08:34:59.194] [76561197965988309][Edd1e360][6764,5380,0][Login][Hours Survived: 472].
//
// A skill-dump line immediately follows most events (Login, Died, Created
// Player), with the full skill list sitting where the event keyword
// normally goes:
//
//	[06-08-26 08:34:59.195] [76561197965988309][Edd1e360][6764,5380,0][Cooking=0, Fitness=5, ...][Hours Survived: 472].
//
// Level-ups carry two extra bracket groups (skill name, new level):
//
//	[...][Level Changed][Woodwork][4][Hours Survived: 620].
const perkLogTimeLayout = "02-01-06 15:04:05.000"

var (
	// Peels off the common [timestamp][steamid][username][x,y,z] prefix,
	// leaving everything after the coordinates as the remainder.
	perkLogPrefixRe = regexp.MustCompile(
		`^\[([^\]]+)\]\s*\[([^\]]+)\]\s*\[([^\]]+)\]\s*\[(\d+),(\d+),(\d+)\]\s*(.*)$`)
	// Hours Survived always trails the line; strip it to isolate the
	// event-specific middle section.
	hoursSurvivedRe = regexp.MustCompile(`\[Hours Survived:\s*([\d.]+)\]\.?\s*$`)
	// First bracket group after the coordinates: either a known event
	// keyword, or (for skill-dump lines) the raw "Key=Val, Key=Val, ..."
	// list.
	firstBracketRe = regexp.MustCompile(`^\[([^\]]*)\]\s*(.*)$`)
	// Level Changed carries two more bracket groups: skill name, level.
	levelChangedRe = regexp.MustCompile(`^\[([^\]]*)\]\s*\[(\d+)\]`)
	// A skill-dump line looks like "Cooking=0, Fitness=5, Strength=6, ...".
	skillPairRe = regexp.MustCompile(`^[A-Za-z]+=\d+(,\s*[A-Za-z]+=\d+)*$`)
)

type perkEvent struct {
	Timestamp     time.Time
	SteamID       string
	Username      string
	X, Y, Z       int
	Kind          string         // "login", "died", "created_player", "level_changed", "skills"
	SkillName     string         // only for level_changed
	SkillLevel    int            // only for level_changed
	Skills        map[string]int // only for kind == "skills"
	HoursSurvived float64
}

func parsePerkLogLine(line string) *perkEvent {
	m := perkLogPrefixRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	ts, err := time.Parse(perkLogTimeLayout, m[1])
	if err != nil {
		return nil
	}
	x, _ := strconv.Atoi(m[4])
	y, _ := strconv.Atoi(m[5])
	z, _ := strconv.Atoi(m[6])
	rest := m[7]

	hoursStr := hoursSurvivedRe.FindStringSubmatch(rest)
	if hoursStr == nil {
		return nil
	}
	hours, _ := strconv.ParseFloat(hoursStr[1], 64)
	rest = hoursSurvivedRe.ReplaceAllString(rest, "")

	fb := firstBracketRe.FindStringSubmatch(rest)
	if fb == nil {
		return nil
	}
	first, remainder := fb[1], fb[2]

	base := perkEvent{
		Timestamp:     ts,
		SteamID:       m[2],
		Username:      m[3],
		X:             x,
		Y:             y,
		Z:             z,
		HoursSurvived: hours,
	}

	switch {
	case first == "Login":
		base.Kind = "login"
	case first == "Died":
		base.Kind = "died"
	case strings.HasPrefix(first, "Created Player"):
		base.Kind = "created_player"
	case first == "Level Changed":
		lc := levelChangedRe.FindStringSubmatch(remainder)
		if lc == nil {
			return nil
		}
		lvl, _ := strconv.Atoi(lc[2])
		base.Kind = "level_changed"
		base.SkillName = lc[1]
		base.SkillLevel = lvl
	case skillPairRe.MatchString(first):
		base.Kind = "skills"
		base.Skills = parseSkillDump(first)
	default:
		// Unrecognized event keyword -- ignore rather than guess.
		return nil
	}

	return &base
}

func parseSkillDump(s string) map[string]int {
	skills := make(map[string]int)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		lvl, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			continue
		}
		skills[strings.TrimSpace(k)] = lvl
	}
	return skills
}

// listPerkLogs returns every PerkLog.txt under dataPath, oldest first.
// PZ writes the currently-running session's files flat in Logs/ and only
// moves them into Logs/logs_YYYY-MM-DD/ once the *next* server start
// happens -- confirmed live (2026-08-09): a session's PerkLog.txt/
// ExporterLog.txt sits at Logs/<name>.txt for its entire run and only
// appears under Logs/logs_*/ after a subsequent restart archives it. Both
// locations must be globbed, or the exporter can never see the live
// session's data -- only ever catches up on the *previous* one, a
// restart late. Sorted by basename (not full path) since filenames are
// YYYY-MM-DD_HH-MM-prefixed and thus chronological regardless of which
// of the two locations currently holds them.
func listPerkLogs(dataPath string) []string {
	flat, err := filepath.Glob(filepath.Join(dataPath, "Logs", "*_PerkLog.txt"))
	if err != nil {
		return nil
	}
	archived, err := filepath.Glob(filepath.Join(dataPath, "Logs", "logs_*", "*_PerkLog.txt"))
	if err != nil {
		return nil
	}
	matches := append(flat, archived...)
	sort.Slice(matches, func(i, j int) bool {
		return filepath.Base(matches[i]) < filepath.Base(matches[j])
	})
	return matches
}

// readNewLines opens path, seeks to fromOffset, and reads up to the last
// complete line (a trailing partial line, if the writer is mid-write, is
// left unread -- it'll be picked up whole on the next poll once its
// newline lands). Returns the parsed events and the offset to checkpoint
// at (the byte position right after the last complete line consumed).
func readNewLines(path string, fromOffset int64) (events []*perkEvent, newOffset int64, err error) {
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
		if ev := parsePerkLogLine(strings.TrimRight(line, "\r\n")); ev != nil {
			events = append(events, ev)
		}
	}
	return events, newOffset, nil
}

// pollOnce checks every known PerkLog file for content past its last
// checkpoint and processes it. done tracks files already fully caught up
// (and not the current newest, so they can never grow again) -- skipped
// entirely on subsequent calls rather than even stat()'d again.
func pollOnce(ctx context.Context, dataPath string, db eventStore, done map[string]bool, onEvent func(*perkEvent)) {
	files := listPerkLogs(dataPath)
	if len(files) == 0 {
		return
	}
	newest := files[len(files)-1]

	for _, path := range files {
		// Checkpoints are keyed by basename, not full path: a session's
		// file lives at Logs/<name>.txt while running and moves to
		// Logs/logs_YYYY-MM-DD/<name>.txt once archived on the next
		// restart -- basename is the only thing stable across that move.
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
				done[key] = true // fully caught up and will never grow again
			}
			continue
		}

		events, newOffset, err := readNewLines(path, offset)
		if err != nil {
			slog.Warn("readNewLines failed", "path", path, "err", err)
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

// pollPerkLogsWithHistory is the persistence-enabled path: every file
// (historical and current alike) is checked against its DB-stored
// checkpoint on every poll, so a first-ever run backfills all existing
// files from offset 0, and any exporter downtime is caught up on restart
// -- both for free, from the same code path, with no separate backfill
// step and no re-processing of content already checkpointed.
func pollPerkLogsWithHistory(ctx context.Context, dataPath string, db eventStore, onEvent func(*perkEvent)) {
	done := make(map[string]bool)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	pollOnce(ctx, dataPath, db, done, onEvent) // catch up immediately on startup, don't wait for the first tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce(ctx, dataPath, db, done, onEvent)
		}
	}
}

// tailPerkLogLive is the no-persistence fallback (db == nil): nowhere to
// checkpoint an offset, so it only ever follows the newest file from EOF
// forward, same as before -- live Prometheus counters, no history.
func tailPerkLogLive(ctx context.Context, dataPath string, onEvent func(*perkEvent)) {
	pollInterval := 3 * time.Second
	var (
		currentPath string
		file        *os.File
		reader      *bufio.Reader
	)
	defer func() {
		if file != nil {
			file.Close()
		}
	}()

	openCurrent := func() {
		if file != nil {
			file.Close()
			file = nil
			reader = nil
		}
		f, err := os.Open(currentPath)
		if err != nil {
			slog.Warn("cannot open PerkLog file", "path", currentPath, "err", err)
			return
		}
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			f.Close()
			return
		}
		file = f
		reader = bufio.NewReader(f)
		slog.Info("tailing PerkLog file", "path", currentPath)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		files := listPerkLogs(dataPath)
		var latest string
		if len(files) > 0 {
			latest = files[len(files)-1]
		}
		if latest != "" && latest != currentPath {
			currentPath = latest
			openCurrent()
		}

		if reader == nil {
			time.Sleep(pollInterval)
			continue
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				slog.Warn("error reading PerkLog file", "err", err)
			}
			time.Sleep(pollInterval)
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if ev := parsePerkLogLine(line); ev != nil {
			onEvent(ev)
		}
	}
}
