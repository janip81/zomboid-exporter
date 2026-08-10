package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// eventTypeFromTopic extracts the trailing "<event-type>" segment from an
// MQTT topic of the form "zomboid/<server>/<event-type>" (see mqtt.go in
// the exporter repo root -- this is the publisher side).
func eventTypeFromTopic(topic string) string {
	if i := strings.LastIndex(topic, "/"); i != -1 {
		return topic[i+1:]
	}
	return topic
}

// formatEvent renders a Discord message for an ExporterLog event. Only
// event types whose exact field shape has been confirmed against real
// live-server payloads get a bespoke format; anything else falls back to
// a generic dump rather than guessing field names/types.
func formatEvent(eventType string, f map[string]any) string {
	username, _ := f["username"].(string)

	switch eventType {
	case "kill":
		kills, _ := f["zombieKills"].(float64)
		return fmt.Sprintf("🧟 **%s** killed a zombie (%.0f total)", username, kills)
	case "death":
		kills, _ := f["zombieKills"].(float64)
		return fmt.Sprintf("💀 **%s** died (%.0f zombie kills)", username, kills)
	case "eat":
		name, _ := f["name"].(string)
		return fmt.Sprintf("🍴 **%s** ate %s", username, name)
	case "drink":
		name, _ := f["name"].(string)
		return fmt.Sprintf("🥤 **%s** drank %s", username, name)
	case "smoke":
		name, _ := f["name"].(string)
		return fmt.Sprintf("🚬 **%s** smoked %s", username, name)
	case "read":
		name, _ := f["name"].(string)
		if completed, _ := f["completed"].(bool); completed {
			return fmt.Sprintf("📖 **%s** finished reading %s", username, name)
		}
		return fmt.Sprintf("📖 **%s** read %s", username, name)
	case "sleep":
		hours, _ := f["hours"].(float64)
		return fmt.Sprintf("💤 **%s** slept %.1f hours", username, hours)
	case "weapon_swing":
		weapon, _ := f["weapon"].(string)
		if hit, _ := f["hit"].(bool); !hit {
			return fmt.Sprintf("🗡️ **%s** swung %s and missed", username, weapon)
		}
		damage, _ := f["damage"].(float64)
		targetsHit, _ := f["targetsHit"].(float64)
		return fmt.Sprintf("🗡️ **%s** hit %.0f target(s) with %s for %.0f damage", username, targetsHit, weapon, damage)
	case "weapon_hit":
		weapon, _ := f["weapon"].(string)
		damage, _ := f["damage"].(float64)
		targetType, _ := f["targetType"].(string)
		if targetUsername, ok := f["targetUsername"].(string); ok && targetUsername != "" {
			return fmt.Sprintf("⚔️ **%s** hit **%s** with %s for %.0f damage", username, targetUsername, weapon, damage)
		}
		return fmt.Sprintf("⚔️ **%s** hit a %s with %s for %.0f damage", username, targetType, weapon, damage)
	default:
		payload, _ := json.Marshal(f)
		return fmt.Sprintf("**%s** [%s]: %s", username, eventType, string(payload))
	}
}
