package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- CGPT-051-G: credential/adapter allowlist -----------------------------

func TestResolveProvider_UnknownAdapterRejected(t *testing.T) {
	p := newCuratorProviderPool(nil, true, false)
	_, reason := p.resolveProvider(llmProviderRow{name: "x", adapter: "totally_made_up", credentialSlot: "local", baseURLOverride: "http://localhost:1234"})
	if reason == "" {
		t.Fatal("expected an unknown-adapter rejection, got none")
	}
}

func TestResolveProvider_UnknownCredentialSlotRejected(t *testing.T) {
	p := newCuratorProviderPool(nil, true, false)
	_, reason := p.resolveProvider(llmProviderRow{name: "x", adapter: adapterOpenAIChat, credentialSlot: "totally_made_up"})
	if reason == "" {
		t.Fatal("expected an unknown-credential_slot rejection, got none")
	}
}

// This is the CGPT-050 regression test: a credential_slot must NEVER
// resolve to an arbitrary os.Getenv() call. Every slot this bot
// recognizes is hardcoded in credentialSlots; there is no code path that
// accepts an env-var NAME from the DB row at all -- this test documents
// that by construction rather than by probing os.Getenv() directly.
func TestCredentialSlots_OnlyFixedAllowlistEntries(t *testing.T) {
	want := map[string]bool{"groq": true, "openrouter": true, "openai": true, "local": true}
	if len(credentialSlots) != len(want) {
		t.Fatalf("credentialSlots has %d entries, expected exactly %d -- if you added a slot, update this test too", len(credentialSlots), len(want))
	}
	for slot := range want {
		if _, ok := credentialSlots[slot]; !ok {
			t.Errorf("expected credential slot %q to exist", slot)
		}
	}
}

func TestResolveProvider_LocalSlotRequiresValidBaseURLOverride(t *testing.T) {
	p := newCuratorProviderPool(nil, true, false)

	cases := []struct {
		name    string
		row     llmProviderRow
		wantErr bool
	}{
		{"missing override", llmProviderRow{name: "l", adapter: adapterOpenAIChat, credentialSlot: "local"}, true},
		{"non-url override", llmProviderRow{name: "l", adapter: adapterOpenAIChat, credentialSlot: "local", baseURLOverride: "not a url"}, true},
		{"ftp scheme rejected", llmProviderRow{name: "l", adapter: adapterOpenAIChat, credentialSlot: "local", baseURLOverride: "ftp://example.com"}, true},
		{"valid http url", llmProviderRow{name: "l", adapter: adapterOpenAIChat, credentialSlot: "local", baseURLOverride: "http://localhost:11434/v1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, reason := p.resolveProvider(tc.row)
			gotErr := reason != ""
			if gotErr != tc.wantErr {
				t.Errorf("reason=%q, wantErr=%v", reason, tc.wantErr)
			}
		})
	}
}

func TestResolveProvider_MissingCredentialEnvVarRejected(t *testing.T) {
	p := newCuratorProviderPool(nil, true, false)
	// GROQ_API_KEY deliberately left unset.
	_, reason := p.resolveProvider(llmProviderRow{name: "g", adapter: adapterOpenAIChat, credentialSlot: "groq", model: "some-model"})
	if reason == "" {
		t.Fatal("expected rejection when GROQ_API_KEY is unset")
	}
}

func TestResolveProvider_SucceedsWithCredentialPresent(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "test-key")
	p := newCuratorProviderPool(nil, true, false)
	rp, reason := p.resolveProvider(llmProviderRow{name: "g", adapter: adapterOpenAIChat, credentialSlot: "groq", model: "some-model", priority: 5})
	if reason != "" {
		t.Fatalf("unexpected rejection: %s", reason)
	}
	if rp.name != "g" || rp.priority != 5 || rp.client == nil {
		t.Errorf("unexpected resolved provider: %+v", rp)
	}
}

// --- CGPT-051-B: code-enforced free-vs-paid policy ------------------------

func TestIsProviderPaidEligible(t *testing.T) {
	cases := []struct {
		name           string
		credentialSlot string
		model          string
		rowAllowPaid   bool
		globalAllow    bool
		wantEligible   bool
	}{
		{"local always free", "local", "anything", false, false, true},
		{"groq treated as free tier", "groq", "llama-3.1", false, false, true},
		{"openrouter free-suffix model allowed", "openrouter", "meta-llama/llama-3.1-8b-instruct:free", false, false, true},
		{"openrouter official free router allowed (CGPT-052-A)", "openrouter", "openrouter/free", false, false, true},
		{"openrouter non-free model rejected without paid gate", "openrouter", "gpt-4", false, false, false},
		{"openai always rejected without paid gate, even if row claims free", "openai", "gpt-4o-mini", false, false, false},
		{"row allow_paid=true but global gate off -> rejected", "groq", "whatever", true, false, false},
		{"row allow_paid=true and global gate on -> allowed even for openai", "openai", "gpt-4o", true, true, true},
		{"unknown slot fails closed", "made_up", "model", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eligible, reason := isProviderPaidEligible(tc.credentialSlot, tc.model, tc.rowAllowPaid, tc.globalAllow)
			if eligible != tc.wantEligible {
				t.Errorf("eligible=%v reason=%q, want eligible=%v", eligible, reason, tc.wantEligible)
			}
			if !eligible && reason == "" {
				t.Error("rejection must always carry a reason")
			}
		})
	}
}

// The concrete scenario from CGPT-051-B's report: a row can't claim
// allow_paid=false while pointing at a real OpenAI model to sneak past
// the gate.
func TestIsProviderPaidEligible_OpenAIRequiresGlobalGateRegardlessOfRowClaim(t *testing.T) {
	eligible, _ := isProviderPaidEligible("openai", "gpt-4o", false, false)
	if eligible {
		t.Fatal("openai must never be eligible without the global LLM_ALLOW_PAID gate, regardless of the row's own allow_paid value")
	}
}

// --- CGPT-051-C: fingerprint completeness ---------------------------------

func TestFingerprint_ChangesWithAllowPaid(t *testing.T) {
	base := llmProviderRow{name: "x", adapter: adapterOpenAIChat, credentialSlot: "groq", model: "m"}
	withPaid := base
	withPaid.allowPaid = true
	if base.fingerprint() == withPaid.fingerprint() {
		t.Error("fingerprint must change when allow_paid changes -- otherwise fixing a paid-gated provider never clears its stale misconfigured health state")
	}
}

func TestFingerprint_ChangesWithBaseURLOverride(t *testing.T) {
	base := llmProviderRow{name: "x", adapter: adapterOpenAIChat, credentialSlot: "local", baseURLOverride: "http://a:1"}
	changed := base
	changed.baseURLOverride = "http://b:2"
	if base.fingerprint() == changed.fingerprint() {
		t.Error("fingerprint must change when base_url_override changes")
	}
}

func TestFingerprint_StableForIdenticalRow(t *testing.T) {
	a := llmProviderRow{name: "x", adapter: adapterOpenAIChat, credentialSlot: "groq", model: "m", allowPaid: true}
	b := llmProviderRow{name: "x", adapter: adapterOpenAIChat, credentialSlot: "groq", model: "m", allowPaid: true}
	if a.fingerprint() != b.fingerprint() {
		t.Error("identical rows must produce identical fingerprints")
	}
}

// --- pool-level failover / first-success-stop -----------------------------

type fakeCuratorLLM struct {
	reply string
	err   error
	calls int
	mu    sync.Mutex
}

func (f *fakeCuratorLLM) Reply(ctx context.Context, req CuratorRequest) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func (f *fakeCuratorLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestPool_TriesPriorityOrderAndStopsAtFirstSuccess(t *testing.T) {
	first := &fakeCuratorLLM{err: newRateLimitedError(errors.New("429"))}
	second := &fakeCuratorLLM{reply: "second answered"}
	third := &fakeCuratorLLM{reply: "third answered"}

	pool := newCuratorProviderPool(nil, true, false)
	pool.providers = []resolvedProvider{
		{name: "first", priority: 1, client: first},
		{name: "second", priority: 2, client: second},
		{name: "third", priority: 3, client: third},
	}

	reply, provider, err := pool.Reply(context.Background(), CuratorRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "second" || reply != "second answered" {
		t.Fatalf("got provider=%q reply=%q, want second", provider, reply)
	}
	if third.callCount() != 0 {
		t.Error("third provider should never be called once second succeeded -- no fan-out")
	}
}

func TestPool_AllProvidersUnavailableReturnsErrLLMUnavailable(t *testing.T) {
	failing := &fakeCuratorLLM{err: newTransientError(errors.New("boom"))}
	pool := newCuratorProviderPool(nil, true, false)
	pool.providers = []resolvedProvider{{name: "only", priority: 1, client: failing}}

	_, _, err := pool.Reply(context.Background(), CuratorRequest{})
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("got err=%v, want ErrLLMUnavailable", err)
	}
}

func TestPool_DisabledPoolNeverCallsProvider(t *testing.T) {
	client := &fakeCuratorLLM{reply: "should never be seen"}
	pool := newCuratorProviderPool(nil, false, false) // enabled=false
	pool.providers = []resolvedProvider{{name: "x", priority: 1, client: client}}

	_, _, err := pool.Reply(context.Background(), CuratorRequest{})
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("got err=%v, want ErrLLMUnavailable", err)
	}
	if client.callCount() != 0 {
		t.Error("a disabled pool must never call a provider")
	}
}

func TestPool_RateLimitedProviderSkippedUntilCooldownExpires(t *testing.T) {
	limited := &fakeCuratorLLM{}
	pool := newCuratorProviderPool(nil, true, false)
	pool.providers = []resolvedProvider{{name: "p", priority: 1, client: limited}}
	pool.recordFailure("p", newRateLimitedError(errors.New("429")))

	if pool.isAvailable("p", time.Now()) {
		t.Error("provider should be unavailable immediately after a rate-limit failure")
	}
	if pool.isAvailable("p", time.Now().Add(rateLimitCooldown+time.Second)) == false {
		t.Error("provider should become available again after the cooldown elapses")
	}
}

// --- CGPT-051-D: concurrent health access under -race ---------------------

func TestPool_ConcurrentHealthAccess(t *testing.T) {
	pool := newCuratorProviderPool(nil, true, false)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			pool.recordSuccess("p")
		}()
		go func() {
			defer wg.Done()
			pool.recordFailure("p", newTransientError(errors.New("x")))
		}()
		go func() {
			defer wg.Done()
			pool.isAvailable("p", time.Now())
		}()
	}
	wg.Wait()
}

// --- natural-chat trigger classification ----------------------------------

func TestCuratorWordPattern(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"who is the curator?", true},
		{"Curator, are you there", true},
		{"the curator's plan", true},
		{"curatorial museum exhibit", false},
		{"I work as a curator", true},
		{"nothing relevant here", false},
	}
	for _, tc := range cases {
		if got := curatorWordPattern.MatchString(tc.text); got != tc.want {
			t.Errorf("curatorWordPattern.MatchString(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestIsDirectCuratorAddress(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"curator, what happened?", true},
		{"Curator do you see me", true},
		{"what is curator doing?", true},
		{"what is Curator doing? lol", true},
		{"I think the curator hates us", false},
		{"the curator has been quiet today", false},
	}
	for _, tc := range cases {
		if got := isDirectCuratorAddress(tc.text); got != tc.want {
			t.Errorf("isDirectCuratorAddress(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestNewCuratorNaturalTrigger_ClampsAmbientChance(t *testing.T) {
	tr := newCuratorNaturalTrigger(curatorNaturalTriggerConfig{AmbientReplyChance: 5}, botDeps{})
	if tr.cfg.AmbientReplyChance != 1 {
		t.Errorf("got %v, want clamped to 1", tr.cfg.AmbientReplyChance)
	}
	tr2 := newCuratorNaturalTrigger(curatorNaturalTriggerConfig{AmbientReplyChance: -5}, botDeps{})
	if tr2.cfg.AmbientReplyChance != 0 {
		t.Errorf("got %v, want clamped to 0", tr2.cfg.AmbientReplyChance)
	}
}

// --- canned response routing -----------------------------------------------

func TestMatchCannedResponse(t *testing.T) {
	reply, ok := matchCannedResponse("who is the curator?")
	if !ok || reply == "" {
		t.Fatal("expected a canned match for a known topic")
	}
	_, ok = matchCannedResponse("what's the weather like on Mars")
	if ok {
		t.Fatal("expected no canned match for an unrelated question")
	}
}

// --- CGPT-051-A: shared LLM rate limiter ----------------------------------

func TestCuratorLLMLimiter_PerUserAndGlobalCooldown(t *testing.T) {
	l := newCuratorLLMLimiter(time.Hour, time.Millisecond)
	if !l.allow("user-a") {
		t.Fatal("first call for a fresh limiter should be allowed")
	}
	if l.allow("user-a") {
		t.Fatal("second immediate call for the same user should be blocked by the (1 hour) user cooldown")
	}
	if l.allow("user-b") {
		t.Fatal("a different user should still be blocked by the (not yet elapsed) global cooldown")
	}
	time.Sleep(2 * time.Millisecond)
	if !l.allow("user-b") {
		t.Fatal("a different user should be allowed once the global cooldown has elapsed")
	}
}

func TestCuratorLLMLimiter_ConcurrentAccess(t *testing.T) {
	l := newCuratorLLMLimiter(time.Millisecond, time.Nanosecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.allow(fmt.Sprintf("user-%d", n))
		}(i)
	}
	wg.Wait()
}
