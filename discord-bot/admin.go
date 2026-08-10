package main

import (
	"encoding/json"
	"os"
)

// loadAdminUserIDs reads a JSON array of Discord user ID strings from a
// ConfigMap-mounted file. This is the bot's own authorization list for
// admin-tier commands (save, broadcast, kick, etc.) -- deliberately
// independent of Discord server roles/permissions, since who administers
// the Discord server and who should have game-admin rights aren't
// necessarily the same people. A missing file just means no admins are
// configured yet, not an error -- admin commands stay locked down.
func loadAdminUserIDs(path string) (map[string]bool, error) {
	set := make(map[string]bool)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}
