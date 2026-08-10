package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
)

// slashCommands is deliberately small and split by tier: online/serveruptime
// are public (no DefaultMemberPermissions set -- Discord doesn't gate them),
// save is admin-only, enforced in code via botDeps.adminUserIDs rather than
// Discord's own role/permission system (see admin.go for why).
var slashCommands = []*discordgo.ApplicationCommand{
	{Name: "online", Description: "List players currently online"},
	{Name: "serveruptime", Description: "Show how long the server has been up"},
	{Name: "save", Description: "Save the world (admin only)"},
}

type botDeps struct {
	rconHost     string
	rconPassword string
	metricsURL   string
	serverName   string
	adminUserIDs map[string]bool
}

func interactionUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string, ephemeral bool) {
	data := &discordgo.InteractionResponseData{Content: content}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}); err != nil {
		slog.Error("failed to respond to interaction", "err", err)
	}
}

func newInteractionHandler(deps botDeps) func(*discordgo.Session, *discordgo.InteractionCreate) {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		switch i.ApplicationCommandData().Name {
		case "online":
			handleOnline(s, i, deps)
		case "serveruptime":
			handleServerUptime(s, i, deps)
		case "save":
			if !deps.adminUserIDs[interactionUserID(i)] {
				respond(s, i, "You don't have permission to run this command.", true)
				return
			}
			handleSave(s, i, deps)
		default:
			slog.Warn("unknown slash command received", "name", i.ApplicationCommandData().Name)
		}
	}
}

func handleOnline(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	if deps.rconHost == "" {
		respond(s, i, "RCON is not configured.", true)
		return
	}
	out, err := rconExecute(deps.rconHost, deps.rconPassword, "players")
	if err != nil {
		slog.Error("rcon players failed", "err", err)
		respond(s, i, "Failed to reach the server.", true)
		return
	}
	respond(s, i, fmt.Sprintf("```\n%s\n```", out), false)
}

func handleServerUptime(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	if deps.metricsURL == "" {
		respond(s, i, "Uptime metrics are not configured.", true)
		return
	}
	start, err := fetchServerStartTime(deps.metricsURL, deps.serverName)
	if err != nil {
		slog.Error("fetch server start time failed", "err", err)
		respond(s, i, "Failed to fetch server uptime.", true)
		return
	}
	respond(s, i, fmt.Sprintf("⏱️ Server has been up for %s (since %s)",
		formatDuration(time.Since(start)), start.Format("2006-01-02 15:04 MST")), false)
}

func handleSave(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	if deps.rconHost == "" {
		respond(s, i, "RCON is not configured.", true)
		return
	}
	out, err := rconExecute(deps.rconHost, deps.rconPassword, "save")
	if err != nil {
		slog.Error("rcon save failed", "err", err)
		respond(s, i, "Failed to save the world.", true)
		return
	}
	if out == "" {
		out = "World save triggered."
	}
	respond(s, i, fmt.Sprintf("💾 %s", out), false)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
