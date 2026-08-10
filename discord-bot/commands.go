package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
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
	"help":         tierPublic,
	"save":         tierAdmin,
	"block":        tierAdmin,
	"unblock":      tierAdmin,
}

var slashCommands = []*discordgo.ApplicationCommand{
	{Name: "online", Description: "List players currently online"},
	{Name: "serveruptime", Description: "Show how long the server has been up"},
	{Name: "help", Description: "DM you the list of available commands"},
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

// randomLine picks one flavor line from a pool -- see
// ideas/curator-style-slashcommands.md, which specifies rotating pools
// rather than a single fixed line so the persona doesn't feel like a
// canned bot.
func randomLine(pool []string) string {
	return pool[rand.Intn(len(pool))]
}

var onlineFlavorLines = []string{
	"The experiment continues.",
	"I am observing.",
	"Activity remains within acceptable parameters.",
	"Several subjects continue to resist statistical inevitability.",
	"The dead remain numerous. The living, less so.",
	"Interesting. More of you survived than expected.",
	"Continue. I require additional data.",
	"No intervention is currently necessary.",
	"You are all being recorded.",
	"Survival remains temporary.",
}

var onlineEmptyFlavorLines = []string{
	"The facility is quiet. No surviving subjects are currently active.",
	"No active subjects detected. Even the dead appear bored.",
	"The experiment continues unattended.",
	"Zero subjects online. An unusually peaceful result.",
	"No survivors detected. I will continue monitoring.",
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
		return false, "Your access to the archive has been revoked. Further inquiries will not be entertained."
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
	return false, "You do not possess the required clearance for this command."
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
		case "help":
			handleHelp(s, i, deps)
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
		respond(s, i, "The observation network is unavailable. RCON has not been configured.", true)
		return
	}
	out, err := rconExecute(deps.rconHost, deps.rconPassword, "players")
	if err != nil {
		slog.Error("rcon players failed", "err", err)
		respond(s, i, "I cannot establish contact with the facility. How inconvenient.", true)
		return
	}

	players := parsePlayerList(out)
	var b strings.Builder
	b.WriteString("CURATOR // ACTIVE SUBJECTS\n")
	switch len(players) {
	case 0:
		b.WriteString("The facility is quiet.\nNo surviving subjects are currently active.\n\n")
		b.WriteString(randomLine(onlineEmptyFlavorLines))
	case 1:
		b.WriteString("One subject remains active.\n")
		b.WriteString(players[0])
		b.WriteString("\n\nI am observing.")
	default:
		fmt.Fprintf(&b, "%d subjects remain active:\n", len(players))
		for _, p := range players {
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(randomLine(onlineFlavorLines))
	}
	respond(s, i, b.String(), false)
}

const helpText = "**Available commands**\n" +
	"`/online` — list players currently online\n" +
	"`/serveruptime` — how long the server has been up\n" +
	"`/help` — this message, sent as a DM\n\n" +
	"**Admin only**\n" +
	"`/save` — save the world\n" +
	"`/block user:<name>` — block a user from using this bot\n" +
	"`/unblock user:<name>` — restore a blocked user's access\n"

// handleHelp sends the command list as a DM rather than posting it in the
// channel -- keeps the channel free of command-reference clutter.
func handleHelp(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	const dmFailedMsg = "I attempted to send you the archive privately. Your communications settings prevented delivery."
	channel, err := s.UserChannelCreate(interactionUserID(i))
	if err != nil {
		slog.Error("failed to open DM channel", "userID", interactionUserID(i), "err", err)
		respond(s, i, dmFailedMsg, true)
		return
	}
	if _, err := s.ChannelMessageSend(channel.ID, helpText); err != nil {
		slog.Error("failed to send help DM", "userID", interactionUserID(i), "err", err)
		respond(s, i, dmFailedMsg, true)
		return
	}
	respond(s, i, "The archive has been delivered privately. Try not to lose it.", true)
}

func handleServerUptime(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	if deps.metricsURL == "" {
		respond(s, i, "I have no record of when this observation cycle began. Uptime metrics are not configured.", true)
		return
	}
	start, err := fetchServerStartTime(deps.metricsURL, deps.serverName)
	if err != nil {
		slog.Error("fetch server start time failed", "err", err)
		respond(s, i, "The facility refuses to disclose how long it has been awake.", true)
		return
	}
	respond(s, i, fmt.Sprintf("⏱️ Observation cycle: **%s**. The facility has been operational since **%s**.",
		formatDuration(time.Since(start)), start.Format("2006-01-02 15:04 MST")), false)
}

func handleSave(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	if deps.rconHost == "" {
		respond(s, i, "I cannot preserve the experiment. RCON has not been configured.", true)
		return
	}
	out, err := rconExecute(deps.rconHost, deps.rconPassword, "save")
	if err != nil {
		slog.Error("rcon save failed", "err", err)
		respond(s, i, "The preservation attempt failed. That is... concerning.", true)
		return
	}
	if out == "" {
		respond(s, i, "💾 The current state of the experiment has been preserved.", false)
		return
	}
	respond(s, i, fmt.Sprintf("💾 Experiment state preserved. %s", out), false)
}

// handleBlockUser implements both /block (newRole=roleBlocked) and
// /unblock (newRole="", clears the row) -- same shape, opposite direction.
func handleBlockUser(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps, newRole userRole) {
	if deps.db == nil {
		respond(s, i, "The subject registry is unavailable. Database access has not been configured.", true)
		return
	}
	opts := i.ApplicationCommandData().Options
	if len(opts) == 0 || opts[0].UserValue(s) == nil {
		if newRole == roleBlocked {
			respond(s, i, "You must specify which subject is to be restricted.", true)
		} else {
			respond(s, i, "You must specify which subject is to be reinstated.", true)
		}
		return
	}
	target := opts[0].UserValue(s)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if newRole == roleBlocked {
		if err := setUserRole(ctx, deps.db, target.ID, roleBlocked, interactionUserID(i)); err != nil {
			slog.Error("failed to block user", "target", target.ID, "err", err)
			respond(s, i, fmt.Sprintf("I was unable to restrict **%s**. They remain... unsupervised.", target.Username), true)
			return
		}
		respond(s, i, fmt.Sprintf("🚫 **%s** has been removed from authorized observation channels.", target.Username), false)
		return
	}

	if err := clearUserRole(ctx, deps.db, target.ID); err != nil {
		slog.Error("failed to unblock user", "target", target.ID, "err", err)
		respond(s, i, fmt.Sprintf("I was unable to restore **%s** to the experiment.", target.Username), true)
		return
	}
	respond(s, i, fmt.Sprintf("✅ **%s** has been returned to active observation.", target.Username), false)
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
