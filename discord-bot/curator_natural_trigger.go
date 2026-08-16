package main

import (
	"context"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// curatorWordPattern matches the standalone, case-insensitive word
// "curator" (and its simple possessive "curator's") -- word boundaries
// mean "curatorial" does NOT match, per
// curator-natural-trigger-and-identity.md's baseline rule and
// BOT-LLM-11 acceptance test.
var curatorWordPattern = regexp.MustCompile(`(?i)\bcurator'?s?\b`)

// curatorNaturalTriggerConfig holds the runtime knobs from
// curator-natural-trigger-and-identity.md's "Possible V1 knobs" list.
type curatorNaturalTriggerConfig struct {
	Enabled            bool
	ChannelID          string // empty = any channel the bot can see
	AmbientReplyChance float64
	UserCooldown       time.Duration
	GlobalCooldown     time.Duration
}

// curatorNaturalTrigger holds MUTABLE reply-pacing state (cooldowns) --
// deliberately separate from curatorProviderPool's health state, which
// is about provider availability, not conversational pacing.
type curatorNaturalTrigger struct {
	cfg  curatorNaturalTriggerConfig
	deps botDeps

	mu              sync.Mutex
	lastGlobalReply time.Time
	lastUserReply   map[string]time.Time
}

func newCuratorNaturalTrigger(cfg curatorNaturalTriggerConfig, deps botDeps) *curatorNaturalTrigger {
	if cfg.AmbientReplyChance < 0 {
		cfg.AmbientReplyChance = 0
	} else if cfg.AmbientReplyChance > 1 {
		cfg.AmbientReplyChance = 1
	}
	return &curatorNaturalTrigger{cfg: cfg, deps: deps, lastUserReply: make(map[string]time.Time)}
}

// handleMessageCreate is registered as a discordgo MessageCreate
// handler. Requires the Discord Message Content privileged intent to be
// enabled (both in the Developer Portal AND passed to
// discordgo.Session.Identify.Intents) -- see main.go's wiring and
// curator-natural-trigger-and-identity.md's "Discord requirement"
// section. Without it, Content is empty for ordinary guild messages and
// this handler silently never triggers.
func (t *curatorNaturalTrigger) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if !t.cfg.Enabled {
		return
	}
	// Ignore bots/webhooks (including this bot's own messages -- never
	// recursively trigger on the Curator's own replies, BOT-LLM-12),
	// wrong channel (if an allowlist is configured), and empty content.
	if m.Author == nil || m.Author.Bot || m.WebhookID != "" {
		return
	}
	if t.cfg.ChannelID != "" && m.ChannelID != t.cfg.ChannelID {
		return
	}
	if strings.TrimSpace(m.Content) == "" {
		return
	}
	if !curatorWordPattern.MatchString(m.Content) {
		return
	}

	roleCtx, roleCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer roleCancel()
	if t.deps.db != nil {
		if role, err := getUserRole(roleCtx, t.deps.db, m.Author.ID); err == nil && role == roleBlocked {
			return
		}
	}

	direct := isDirectCuratorAddress(m.Content)
	if !direct && !t.rollAmbientReply() {
		return
	}
	if !t.consumeCooldown(m.Author.ID) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), curatorLLMTimeout)
	defer cancel()

	var names []string
	if m.Member != nil {
		names = append(names, m.Member.Nick)
	}
	names = append(names, m.Author.GlobalName, m.Author.Username)

	reply, ok := askCurator(ctx, t.deps, m.Author.ID, names, m.Content)
	switch {
	case ok:
		t.send(s, m.ChannelID, reply)
	case direct:
		// An explicit address to the Curator always gets SOME reply,
		// per curator-reply-routing.md.
		t.send(s, m.ChannelID, randomLine(curatorGenericFallbackLines))
	default:
		// Ambient mention with nothing available (no canned match, no
		// LLM) -- intentional silence rather than an error, per
		// curator-natural-trigger-and-identity.md's "avoid answering
		// every mention."
		slog.Info("curator: ambient mention had no available reply, staying silent")
	}
}

// isDirectCuratorAddress is the STRONG-tier check from
// curator-natural-trigger-and-identity.md: message starts with
// "curator", or is phrased as a question mentioning "curator" anywhere.
// Anything else eligible (standalone "curator" elsewhere in an ordinary
// statement) falls to the ambient/probabilistic path instead.
//
// Checking for "?" ANYWHERE in the message (not just as the final
// character) deliberately catches trailing chatter like "...lol" or an
// emoji after the question mark.
func isDirectCuratorAddress(content string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(content))
	if strings.HasPrefix(trimmed, "curator") {
		return true
	}
	return strings.Contains(trimmed, "curator") && strings.Contains(trimmed, "?")
}

func (t *curatorNaturalTrigger) rollAmbientReply() bool {
	return rand.Float64() < t.cfg.AmbientReplyChance
}

// consumeCooldown enforces BOTH the per-user and bot-wide cooldowns
// atomically -- a caller that passes must not be double-counted by a
// second concurrent message before its own reply is sent.
func (t *curatorNaturalTrigger) consumeCooldown(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if now.Sub(t.lastGlobalReply) < t.cfg.GlobalCooldown {
		return false
	}
	if last, ok := t.lastUserReply[userID]; ok && now.Sub(last) < t.cfg.UserCooldown {
		return false
	}
	t.lastGlobalReply = now
	t.lastUserReply[userID] = now
	return true
}

func (t *curatorNaturalTrigger) send(s *discordgo.Session, channelID, content string) {
	if _, err := s.ChannelMessageSend(channelID, content); err != nil {
		slog.Error("curator: failed to send natural-trigger reply", "channelID", channelID, "err", err)
	}
}
