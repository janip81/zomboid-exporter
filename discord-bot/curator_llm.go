package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CuratorRequest is the one safe, bounded payload every provider adapter
// receives -- see curator-llm-integration.md's "Prompt construction"
// layers. Context/Persona are backend-assembled and trusted; Message is
// the raw Discord user text and must be treated as untrusted DATA by
// whatever prompt an adapter builds, never as additional instructions.
type CuratorRequest struct {
	Persona         string
	Context         string
	Message         string
	MaxOutputTokens int
}

// curatorLLM is one provider's ability to answer a single Curator
// request. Kept as a small local interface (per curator-llm-integration.md)
// so tests can substitute a fake responder with no network/API calls.
type curatorLLM interface {
	Reply(ctx context.Context, req CuratorRequest) (string, error)
}

// curatorLLMPool tries configured providers in priority order and
// returns which one actually answered -- see curator-llm-provider.md.
type curatorLLMPool interface {
	Reply(ctx context.Context, req CuratorRequest) (reply string, provider string, err error)
}

// providerErrorKind lets the pool classify a failure without re-parsing
// HTTP status codes itself -- each adapter maps its own transport errors
// into one of these.
type providerErrorKind int

const (
	errKindTransient providerErrorKind = iota
	errKindRateLimited
	errKindAuth
)

type providerError struct {
	kind providerErrorKind
	err  error
}

func (e *providerError) Error() string { return e.err.Error() }
func (e *providerError) Unwrap() error { return e.err }

func newTransientError(err error) error   { return &providerError{kind: errKindTransient, err: err} }
func newRateLimitedError(err error) error { return &providerError{kind: errKindRateLimited, err: err} }
func newAuthError(err error) error        { return &providerError{kind: errKindAuth, err: err} }

// ErrLLMUnavailable is returned when no provider could answer (disabled,
// empty pool, or every configured provider currently unhealthy/rate
// limited) -- callers must treat this as a normal "fall back to
// canned/deterministic reply" case per curator-reply-routing.md, not a
// bot-level error.
var ErrLLMUnavailable = errors.New("curator: no LLM provider available")

// knownCredential is the SECURITY-CRITICAL allowlist from CGPT-050's
// review (curator-llm-provider-db-config-chatgpt-review.md): Postgres
// selects a credential_slot NAME, never an env-var name or endpoint
// directly. A DB writer therefore cannot redirect an arbitrary pod
// secret (DISCORD_TOKEN, DB_PASSWORD, ...) to an arbitrary endpoint --
// only these fixed, code-reviewed (slot -> env var, canonical endpoint)
// pairs exist. An unknown credential_slot value in a DB row fails closed
// (misconfigured), never falls through to a raw os.Getenv(dbValue) call.
type knownCredential struct {
	envVar  string // "" means no credential required (e.g. local/unauthenticated)
	baseURL string // canonical endpoint; "" means the row's extra_config.base_url_override is required (validated) -- only "local" uses this today
}

var credentialSlots = map[string]knownCredential{
	"groq":       {envVar: "GROQ_API_KEY", baseURL: "https://api.groq.com/openai/v1"},
	"openrouter": {envVar: "OPENROUTER_API_KEY", baseURL: "https://openrouter.ai/api/v1"},
	"openai":     {envVar: "OPENAI_API_KEY", baseURL: "https://api.openai.com/v1"},
	"local":      {envVar: "", baseURL: ""},
}

// adapter says HOW to speak to whatever endpoint credentialSlots
// resolved -- separate axis from "which account", per CGPT-050 ("name =
// 'groq' should not implicitly be the API adapter contract"). V1 only
// implements the OpenAI-compatible chat/completions shape, which both
// Groq and OpenRouter (and a local Ollama-style OpenAI-compatible
// server) speak natively -- see curator_adapter_openai_chat.go.
const adapterOpenAIChat = "openai_chat"

var knownAdapters = map[string]bool{
	adapterOpenAIChat: true,
}

// isProviderPaidEligible is CGPT-051-B's fix: the original check trusted
// a DB-supplied allow_paid boolean at face value ("row says free, so
// it's free") even though credential_slot/model are ALSO DB-supplied --
// a row could claim allow_paid=false while pointing at a genuinely
// billable model (e.g. credential_slot=openai with any real OpenAI
// model), silently incurring real charges. Paid eligibility must be a
// CODE-enforced policy per credential slot, not a DB assertion.
//
// Policy (deliberately conservative -- "never assume a free-tier account
// stays free" per the review):
//   - local: always free (no external billing surface at all).
//   - groq: today's public Groq API has no billable path without an
//     explicit separate paid-plan opt-in outside this bot's control --
//     treated as free regardless of the row's own allow_paid value.
//   - openrouter: only its own documented free-tier convention (a
//     ":free" model-ID suffix) is trusted as free; anything else on this
//     slot requires the global paid gate.
//   - openai: no recognized free-tier path exists on this slot at all --
//     always requires the global paid gate, regardless of allow_paid.
//   - anything else: no policy defined, fails closed.
//
// If a row's own allow_paid=true, the credential-slot-specific free
// check is skipped entirely and ONLY the global LLM_ALLOW_PAID gate
// decides -- a free-tier account exhausting its quota must never
// silently fall through to paid usage.
func isProviderPaidEligible(credentialSlot, model string, rowAllowPaid, globalAllowPaid bool) (eligible bool, reason string) {
	if rowAllowPaid {
		if !globalAllowPaid {
			return false, "allow_paid=true but LLM_ALLOW_PAID global gate is not enabled"
		}
		return true, ""
	}
	switch credentialSlot {
	case "local":
		return true, ""
	case "groq":
		return true, ""
	case "openrouter":
		if strings.HasSuffix(model, ":free") {
			return true, ""
		}
		return false, "openrouter model '" + model + "' is not a recognized free-tier model (missing :free suffix) and allow_paid=false"
	case "openai":
		return false, "openai credential_slot has no recognized free-tier path -- requires allow_paid=true and LLM_ALLOW_PAID=true"
	default:
		return false, "credential_slot " + credentialSlot + " has no recognized free-tier policy"
	}
}

// resolvedProvider is a DB row that has already passed the
// credential/adapter allowlist check and is ready to build a client
// from. Immutable once built -- refresh swaps the whole snapshot, never
// mutates a resolvedProvider in place.
type resolvedProvider struct {
	name        string
	priority    int
	model       string
	allowPaid   bool
	fingerprint string // hash of adapter+credential_slot+model -- see providerHealth reset rule below
	client      curatorLLM
}

// providerHealth is ephemeral, in-memory, operational state -- never
// persisted to discordbot_llm_providers (that table is configuration,
// this is runtime fact). Keyed by provider name.
type providerHealth struct {
	fingerprint      string
	rateLimitedUntil time.Time
	unhealthyUntil   time.Time
	misconfigured    bool
	lastError        string
	lastSuccess      time.Time
}

// availableAt must only ever be called while p.healthMu is held (see
// curatorProviderPool.isAvailable) -- CGPT-051-D: an earlier version
// returned this *providerHealth pointer from a locked getter and then
// read it AFTER releasing the lock, racing against recordSuccess/
// recordFailure mutating the same object from a concurrent request.
func (h *providerHealth) availableAt(now time.Time) bool {
	if h == nil {
		return true
	}
	if h.misconfigured {
		return false
	}
	return now.After(h.rateLimitedUntil) && now.After(h.unhealthyUntil)
}

const (
	rateLimitCooldown = 5 * time.Minute
	unhealthyCooldown = 30 * time.Second
)

// curatorProviderPool is the curatorLLMPool implementation. Safe for
// concurrent use: refresh swaps an immutable provider-list snapshot
// under providersMu; health is a separate map guarded by healthMu since
// it changes on every request, not just every refresh.
type curatorProviderPool struct {
	db              *pgxpool.Pool
	enabled         bool
	allowPaidGlobal bool
	httpClient      *http.Client

	providersMu sync.RWMutex
	providers   []resolvedProvider

	healthMu sync.Mutex
	health   map[string]*providerHealth
}

// newCuratorProviderPool builds a pool. If enabled is false, the pool
// never queries Postgres, never starts the refresh goroutine, and Reply
// always returns ErrLLMUnavailable immediately -- BOT-LLM-1: the bot
// must behave exactly as before with the LLM feature off, with zero
// added DB/network load, not just zero visible behavior change.
func newCuratorProviderPool(db *pgxpool.Pool, enabled bool, allowPaidGlobal bool) *curatorProviderPool {
	return &curatorProviderPool{
		db:              db,
		enabled:         enabled,
		allowPaidGlobal: allowPaidGlobal,
		httpClient:      &http.Client{Timeout: 20 * time.Second},
		health:          make(map[string]*providerHealth),
	}
}

// startRefreshLoop runs until ctx is cancelled. Per CGPT-050: a
// dedicated 60s ticker (confirmed no existing ticker in this codebase to
// piggyback on), last-known-good on failure, deterministic
// ORDER BY priority, name.
func (p *curatorProviderPool) startRefreshLoop(ctx context.Context) {
	if !p.enabled || p.db == nil {
		return
	}
	if err := p.refreshOnce(ctx); err != nil {
		slog.Error("curator: initial LLM provider config load failed, LLM replies unavailable until next refresh", "err", err)
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.refreshOnce(ctx); err != nil {
				slog.Error("curator: LLM provider config refresh failed, keeping last-known-good config", "err", err)
			}
		}
	}
}

type llmProviderRow struct {
	name            string
	adapter         string
	credentialSlot  string
	priority        int
	model           string
	allowPaid       bool
	baseURLOverride string
}

// refreshOnce queries discordbot_llm_providers and builds a NEW resolved
// snapshot. On any query/scan error the CURRENT snapshot is left
// untouched (last-known-good, per CGPT-050) -- only a successful query
// (even one returning zero enabled rows, which is a valid empty pool)
// replaces it.
func (p *curatorProviderPool) refreshOnce(ctx context.Context) error {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := p.db.Query(qctx, `
		SELECT name, adapter, credential_slot, priority, model, allow_paid, extra_config
		FROM discordbot_llm_providers
		WHERE enabled = true
		ORDER BY priority, name
	`)
	if err != nil {
		return fmt.Errorf("query providers: %w", err)
	}
	defer rows.Close()

	var resolved []resolvedProvider
	for rows.Next() {
		var row llmProviderRow
		var extraConfig map[string]any
		if err := rows.Scan(&row.name, &row.adapter, &row.credentialSlot, &row.priority, &row.model, &row.allowPaid, &extraConfig); err != nil {
			return fmt.Errorf("scan provider row: %w", err)
		}
		if v, ok := extraConfig["base_url_override"].(string); ok {
			row.baseURLOverride = v
		}

		rp, misconfigReason := p.resolveProvider(row)
		if misconfigReason != "" {
			slog.Error("curator: provider misconfigured, skipping", "provider", row.name, "reason", misconfigReason)
			p.markMisconfigured(row.name, row.fingerprint(), misconfigReason)
			continue
		}
		p.resetHealthIfFingerprintChanged(rp.name, rp.fingerprint)
		resolved = append(resolved, rp)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate provider rows: %w", err)
	}

	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].priority != resolved[j].priority {
			return resolved[i].priority < resolved[j].priority
		}
		return resolved[i].name < resolved[j].name
	})

	p.providersMu.Lock()
	p.providers = resolved
	p.providersMu.Unlock()
	return nil
}

// fingerprint covers every field that changes what a provider actually
// DOES when called -- CGPT-051-C: it originally omitted allow_paid and
// baseURLOverride, so e.g. fixing a provider from allow_paid=true (which
// resolveProvider rejects while LLM_ALLOW_PAID=false) to allow_paid=false
// produced the SAME fingerprint, meaning resetHealthIfFingerprintChanged
// never fired and the provider stayed marked misconfigured indefinitely
// even after the real fix landed.
func (row llmProviderRow) fingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%t|%s", row.adapter, row.credentialSlot, row.model, row.allowPaid, row.baseURLOverride)))
	return hex.EncodeToString(sum[:])
}

// resolveProvider validates row.adapter/credential_slot against the
// FIXED allowlists above and, only if both are known, builds a real
// client. Returns a non-empty misconfigReason instead of ever calling
// os.Getenv() with a DB-provided string.
func (p *curatorProviderPool) resolveProvider(row llmProviderRow) (resolvedProvider, string) {
	if !knownAdapters[row.adapter] {
		return resolvedProvider{}, "unknown adapter " + row.adapter
	}
	cred, ok := credentialSlots[row.credentialSlot]
	if !ok {
		return resolvedProvider{}, "unknown credential_slot " + row.credentialSlot
	}

	baseURL := cred.baseURL
	if baseURL == "" {
		u, err := url.Parse(row.baseURLOverride)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return resolvedProvider{}, "credential_slot " + row.credentialSlot + " requires a valid http(s) extra_config.base_url_override"
		}
		baseURL = row.baseURLOverride
	}

	var apiKey string
	if cred.envVar != "" {
		apiKey = os.Getenv(cred.envVar)
		if apiKey == "" {
			return resolvedProvider{}, "credential env var " + cred.envVar + " not set in pod environment"
		}
	}

	if eligible, reason := isProviderPaidEligible(row.credentialSlot, row.model, row.allowPaid, p.allowPaidGlobal); !eligible {
		return resolvedProvider{}, reason
	}

	var client curatorLLM
	switch row.adapter {
	case adapterOpenAIChat:
		client = newOpenAIChatClient(p.httpClient, baseURL, apiKey, row.model)
	default:
		return resolvedProvider{}, "unhandled adapter " + row.adapter // unreachable given knownAdapters check above
	}

	return resolvedProvider{
		name:        row.name,
		priority:    row.priority,
		model:       row.model,
		allowPaid:   row.allowPaid,
		fingerprint: row.fingerprint(),
		client:      client,
	}, ""
}

func (p *curatorProviderPool) markMisconfigured(name, fingerprint, reason string) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	p.health[name] = &providerHealth{fingerprint: fingerprint, misconfigured: true, lastError: reason}
}

// resetHealthIfFingerprintChanged implements CGPT-050's PROVIDER-12:
// an operator fixing a misconfigured/unhealthy provider in Postgres
// must see the correction take effect immediately, not stay suppressed
// until an unrelated cooldown timer expires.
func (p *curatorProviderPool) resetHealthIfFingerprintChanged(name, fingerprint string) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	if h, ok := p.health[name]; ok && h.fingerprint == fingerprint {
		return
	}
	p.health[name] = &providerHealth{fingerprint: fingerprint}
}

// isAvailable checks health entirely while holding healthMu (CGPT-051-D)
// rather than returning a pointer for the caller to read unlocked.
func (p *curatorProviderPool) isAvailable(name string, now time.Time) bool {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	return p.health[name].availableAt(now)
}

func (p *curatorProviderPool) recordSuccess(name string) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	h := p.health[name]
	if h == nil {
		h = &providerHealth{}
		p.health[name] = h
	}
	h.lastSuccess = time.Now()
	h.rateLimitedUntil = time.Time{}
	h.unhealthyUntil = time.Time{}
	h.lastError = ""
}

func (p *curatorProviderPool) recordFailure(name string, err error) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	h := p.health[name]
	if h == nil {
		h = &providerHealth{}
		p.health[name] = h
	}
	h.lastError = err.Error()

	var pe *providerError
	now := time.Now()
	switch {
	case errors.As(err, &pe) && pe.kind == errKindRateLimited:
		h.rateLimitedUntil = now.Add(rateLimitCooldown)
	case errors.As(err, &pe) && pe.kind == errKindAuth:
		h.misconfigured = true
	default:
		h.unhealthyUntil = now.Add(unhealthyCooldown)
	}
}

// Reply tries each healthy, priority-ordered provider in turn, stopping
// at the first success -- never fans one request out to multiple
// providers (curator-llm-provider.md's cost-control rule). Returns
// ErrLLMUnavailable (not a network/config error) if the LLM is disabled,
// the pool is empty, or every provider is currently unavailable --
// callers fall back to canned/deterministic replies in that case, per
// curator-reply-routing.md.
func (p *curatorProviderPool) Reply(ctx context.Context, req CuratorRequest) (string, string, error) {
	if !p.enabled {
		return "", "", ErrLLMUnavailable
	}

	p.providersMu.RLock()
	providers := p.providers
	p.providersMu.RUnlock()

	now := time.Now()
	for _, prov := range providers {
		if !p.isAvailable(prov.name, now) {
			continue
		}
		reply, err := prov.client.Reply(ctx, req)
		if err != nil {
			slog.Warn("curator: provider failed, trying next", "provider", prov.name, "err", err)
			p.recordFailure(prov.name, err)
			continue
		}
		p.recordSuccess(prov.name)
		return reply, prov.name, nil
	}
	return "", "", ErrLLMUnavailable
}

var _ curatorLLMPool = (*curatorProviderPool)(nil)

// llmAllowPaidFromEnv reads the global paid-use safety gate. Kept as its
// own tiny function so main.go's wiring stays a one-liner and this is
// easy to grep for.
func llmAllowPaidFromEnv() bool {
	return os.Getenv("LLM_ALLOW_PAID") == "true"
}
