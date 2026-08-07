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

// findLatestPerkLog returns the newest PerkLog.txt across all
// Logs/logs_YYYY-MM-DD/ folders under dataPath, or "" if none exist yet.
func findLatestPerkLog(dataPath string) string {
	matches, err := filepath.Glob(filepath.Join(dataPath, "Logs", "logs_*", "*_PerkLog.txt"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches) // filenames are YYYY-MM-DD_HH-MM-prefixed, so lexical sort == chronological
	return matches[len(matches)-1]
}

// tailPerkLog follows the newest PerkLog.txt, switching files when the
// server restarts and a newer one appears (PZ writes one file per server
// start, not one continuously-appended file). Parsed events are sent to
// out. Blocks until ctx is cancelled.
func tailPerkLog(ctx context.Context, dataPath string, out chan<- *perkEvent) {
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
		// Start at EOF: we only care about events from this point forward,
		// not replaying a whole session's history on every exporter restart.
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

		latest := findLatestPerkLog(dataPath)
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
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}
