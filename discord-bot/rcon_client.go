package main

import (
	"strings"

	"github.com/gorcon/rcon"
)

// rconExecute dials fresh for a single command rather than holding one
// long-lived connection -- commands are infrequent (Discord slash commands,
// not a hot path), and a fresh dial avoids dealing with a connection going
// stale while the bot sits idle for hours between uses.
func rconExecute(host, password, command string) (string, error) {
	conn, err := rcon.Dial(host, password)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.Execute(command)
}

// parsePlayerList parses PZ RCON's "players" command output, e.g.:
//
//	Players connected (2):
//	-Alice
//	-Bob
//
// Best-effort: any line that isn't the header is treated as a username
// (with a leading "-" stripped if present) rather than erroring, since
// this hasn't been verified against every server version's exact format.
func parsePlayerList(out string) []string {
	var players []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Players connected") {
			continue
		}
		players = append(players, strings.TrimPrefix(line, "-"))
	}
	return players
}
