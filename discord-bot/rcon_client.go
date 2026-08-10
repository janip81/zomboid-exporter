package main

import "github.com/gorcon/rcon"

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
