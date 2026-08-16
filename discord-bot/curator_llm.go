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

func (row llmProviderRow) fingerprint() string {
	sum := sha256.Sum256([]byte(row.adapter + "|" + row.credentialSlot + "|" + row.model))
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

	if row.allowPaid && !p.allowPaidGlobal {
		return resolvedProvider{}, "allow_paid=true but LLM_ALLOW_PAID global gate is not enabled"
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

func (p *curatorProviderPool) getHealth(name string) *providerHealth {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	return p.health[name]
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
		if !p.getHealth(prov.name).availableAt(now) {
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
