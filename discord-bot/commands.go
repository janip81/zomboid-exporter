package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"
)

type commandTier int

const (
	tierPublic commandTier = iota
	tierModerator
	tierAdmin
)

// commandTiers maps each slash command to the minimum role required.
// Adding a new tiered command later is just one entry here plus a case in
// newInteractionHandler's switch -- no changes needed to the
// authorization logic itself.
var commandTiers = map[string]commandTier{
	"online":       tierPublic,
	"serveruptime": tierPublic,
	"save":         tierAdmin,
	"block":        tierAdmin,
	"unblock":      tierAdmin,
}

var slashCommands = []*discordgo.ApplicationCommand{
	{Name: "online", Description: "List players currently online"},
	{Name: "serveruptime", Description: "Show how long the server has been up"},
	{Name: "save", Description: "Save the world (admin only)"},
	{
		Name:        "block",
		Description: "Block a user from using this bot (admin only)",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to block", Required: true},
		},
	},
	{
		Name:        "unblock",
		Description: "Restore a blocked user's access (admin only)",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to unblock", Required: true},
		},
	},
}

type botDeps struct {
	rconHost     string
	rconPassword string
	metricsURL   string
	serverName   string
	db           *pgxpool.Pool
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

// authorize does a single role lookup and applies both the universal
// "blocked" check and the command's tier requirement. If db is nil (no
// --db-host configured) role stays "": blocking silently no-ops (fails
// open -- there's nothing to check against) but admin/moderator tiers
// still correctly deny (fails closed -- "" never matches a required
// role), which is the safe direction for both.
func authorize(ctx context.Context, deps botDeps, userID string, tier commandTier) (bool, string) {
	var role userRole
	if deps.db != nil {
		r, err := getUserRole(ctx, deps.db, userID)
		if err != nil {
			slog.Error("failed to look up user role", "userID", userID, "err", err)
		} else {
			role = r
		}
	}
	if role == roleBlocked {
		return false, "You've been blocked from using this bot."
	}
	switch tier {
	case tierPublic:
		return true, ""
	case tierModerator:
		if role == roleModerator || role == roleAdmin {
			return true, ""
		}
	case tierAdmin:
		if role == roleAdmin {
			return true, ""
		}
	}
	return false, "You don't have permission to run this command."
}

func newInteractionHandler(deps botDeps) func(*discordgo.Session, *discordgo.InteractionCreate) {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		name := i.ApplicationCommandData().Name
		tier, known := commandTiers[name]
		if !known {
			slog.Warn("unknown slash command received", "name", name)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ok, denyMsg := authorize(ctx, deps, interactionUserID(i), tier)
		if !ok {
			respond(s, i, denyMsg, true)
			return
		}

		switch name {
		case "online":
			handleOnline(s, i, deps)
		case "serveruptime":
			handleServerUptime(s, i, deps)
		case "save":
			handleSave(s, i, deps)
		case "block":
			handleBlockUser(s, i, deps, roleBlocked)
		case "unblock":
			handleBlockUser(s, i, deps, "")
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

// handleBlockUser implements both /block (newRole=roleBlocked) and
// /unblock (newRole="", clears the row) -- same shape, opposite direction.
func handleBlockUser(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps, newRole userRole) {
	if deps.db == nil {
		respond(s, i, "Database is not configured.", true)
		return
	}
	opts := i.ApplicationCommandData().Options
	if len(opts) == 0 || opts[0].UserValue(s) == nil {
		respond(s, i, "Missing user option.", true)
		return
	}
	target := opts[0].UserValue(s)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if newRole == roleBlocked {
		if err := setUserRole(ctx, deps.db, target.ID, roleBlocked, interactionUserID(i)); err != nil {
			slog.Error("failed to block user", "target", target.ID, "err", err)
			respond(s, i, "Failed to block user.", true)
			return
		}
		respond(s, i, fmt.Sprintf("🚫 Blocked %s from using this bot.", target.Username), false)
		return
	}

	if err := clearUserRole(ctx, deps.db, target.ID); err != nil {
		slog.Error("failed to unblock user", "target", target.ID, "err", err)
		respond(s, i, "Failed to unblock user.", true)
		return
	}
	respond(s, i, fmt.Sprintf("✅ Restored access for %s.", target.Username), false)
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
