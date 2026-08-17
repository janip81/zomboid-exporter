package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
)

// curatorLLMTimeout bounds a single LLM round trip (the pool's own
// failover loop reuses this same ctx across providers, so it's a
// budget for the whole attempt chain, not per-provider) -- see
// curator-llm-provider.md's cost-control rule ("request timeout").
const curatorLLMTimeout = 20 * time.Second

// curatorMaxOutputTokens keeps replies Discord-sized, per
// curator-llm-integration.md's "Prefer concise Discord-sized replies."
const curatorMaxOutputTokens = 300

// curatorGenericFallbackLines is used whenever no provider could answer
// -- disabled, empty pool, or every configured provider currently
// unavailable. Per curator-reply-routing.md: an explicit question (the
// /curator command, or a direct natural-chat address) must always get an
// authored in-character response, never a raw error or silence. Ambient
// mentions may separately choose intentional silence instead -- see
// curator_natural_trigger.go.
var curatorGenericFallbackLines = []string{
	"I have nothing further to observe on that subject at present.",
	"That falls outside what I am currently able to discuss.",
	"The archive is, for the moment, unavailable on that particular point.",
}

// candidateIdentityNames returns nickname/display-name/account-username
// in resolveCuratorIdentity's expected priority order, per
// curator-natural-trigger-and-identity.md.
func candidateIdentityNames(i *discordgo.InteractionCreate) []string {
	var names []string
	if i.Member != nil {
		names = append(names, i.Member.Nick)
		if i.Member.User != nil {
			names = append(names, i.Member.User.GlobalName, i.Member.User.Username)
		}
	} else if i.User != nil {
		names = append(names, i.User.GlobalName, i.User.Username)
	}
	return names
}

// buildCuratorContext resolves the speaker's identity and renders the
// bounded, safe fact set a Curator reply may use -- shared by the
// /curator command and the natural chat trigger so both produce
// consistent, identically-authorized context.
func buildCuratorContext(ctx context.Context, deps botDeps, discordUserID string, candidateNames []string) string {
	if deps.db == nil {
		return "No player records are currently accessible."
	}
	identity, err := resolveCuratorIdentity(ctx, deps.db, discordUserID, candidateNames)
	if err != nil {
		slog.Error("curator: identity resolution failed", "err", err)
	}
	var stats curatorPlayerStats
	if identity.Resolved {
		stats, err = fetchCuratorPlayerStats(ctx, deps.db, identity.SteamID)
		if err != nil {
			slog.Error("curator: fetch player stats failed", "err", err)
		} else {
			// AUTO-LINK-9: server-side only, distinguishes an identity
			// failure (logged in resolveCuratorIdentity) from a
			// stats-query problem when a player reports Curator "not
			// seeing" their real numbers.
			slog.Info("curator: context stats", "steamID", identity.SteamID, "zombieKills", stats.ZombieKills, "deaths", stats.Deaths)
		}
	}
	return renderCuratorContext(identity, stats)
}

// askCurator implements curator-llm-conversation-routing.md's LLM-first
// routing: deterministic code decides WHETHER Curator responds (the
// caller), WHAT the message is about (classifyCuratorIntent), and what
// Curator actually knows (buildCuratorContext) -- but a healthy LLM writes
// the actual conversational sentence, rather than a canned line being
// chosen first. Canned, intent-specific fallback lines are used only when
// the LLM is disabled, unavailable, or rate-limited, so Curator still has
// a voice with zero provider dependency. Returns ok=false only if there's
// truly nothing to say (defensive -- every intent has a non-empty
// fallback pool); callers decide what "no reply" means for their surface.
func askCurator(ctx context.Context, deps botDeps, discordUserID string, candidateNames []string, message string) (reply string, ok bool) {
	intent := classifyCuratorIntent(message)

	if deps.llmPool != nil {
		// CGPT-051-A: the shared rate limiter gates the LLM call itself --
		// the single choke point shared by BOTH /curator and natural chat.
		// A denial here falls through to the intent fallback pool below
		// rather than failing the whole interaction (conversation-routing's
		// "Rate-limit implication": a rate-limited user still gets a
		// deterministic Curator answer, just without consuming an API call).
		if deps.llmLimiter == nil || deps.llmLimiter.allow(discordUserID) {
			contextText := buildCuratorContext(ctx, deps, discordUserID, candidateNames)
			tier := selectCuratorResponseTier()
			llmReply, provider, err := deps.llmPool.Reply(ctx, CuratorRequest{
				Persona:         assembleCuratorPersona(tier, curatorIntentGuidance(intent)),
				Context:         contextText,
				Message:         message,
				MaxOutputTokens: curatorMaxOutputTokens,
			})
			if err == nil {
				slog.Info("curator: LLM reply generated", "provider", provider, "tier", tier, "intent", intent)
				return llmReply, true
			}
			if err != ErrLLMUnavailable {
				slog.Error("curator: LLM pool returned unexpected error", "err", err)
			}
		}
	}

	return matchIntentFallback(intent)
}

func handleCurator(s *discordgo.Session, i *discordgo.InteractionCreate, deps botDeps) {
	opts := i.ApplicationCommandData().Options
	if len(opts) == 0 || opts[0].StringValue() == "" {
		respond(s, i, "You must actually ask something.", true)
		return
	}
	question := opts[0].StringValue()

	ctx, cancel := context.WithTimeout(context.Background(), curatorLLMTimeout)
	defer cancel()

	reply, ok := askCurator(ctx, deps, interactionUserID(i), candidateIdentityNames(i), question)
	if !ok {
		reply = randomLine(curatorGenericFallbackLines)
	}
	respond(s, i, reply, false)
}
