package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakePool is a minimal curatorLLMPool double for askCurator-level
// routing tests -- these test WHETHER askCurator calls the pool at all,
// not the pool's own failover behavior (already covered in
// curator_llm_test.go).
type fakePool struct {
	reply      string
	err        error
	calls      int
	lastReq    CuratorRequest
	lastCalled bool
}

func (f *fakePool) Reply(ctx context.Context, req CuratorRequest) (string, string, error) {
	f.calls++
	f.lastReq = req
	f.lastCalled = true
	if f.err != nil {
		return "", "", f.err
	}
	return f.reply, "fake-provider", nil
}

// --- curator-llm-conversation-routing.md acceptance tests ------------------

// "LLM healthy" acceptance case: the canned identity pool must NOT
// intercept an identity question before a healthy LLM sees it.
func TestAskCurator_LLMHealthy_IdentityQuestionReachesLLM(t *testing.T) {
	pool := &fakePool{reply: "An observer. You already knew that much."}
	deps := botDeps{llmPool: pool}

	reply, ok := askCurator(context.Background(), deps, "user-1", nil, "curator who are you?")
	if !ok {
		t.Fatal("expected a reply")
	}
	if !pool.lastCalled {
		t.Fatal("expected the LLM pool to be called for a healthy identity question, not intercepted by a canned reply")
	}
	if reply != pool.reply {
		t.Errorf("got reply %q, want the LLM's reply %q", reply, pool.reply)
	}
}

// Intent guidance for the classified intent must actually reach the LLM
// request's persona.
func TestAskCurator_LLMHealthy_IncludesIntentGuidanceInPersona(t *testing.T) {
	pool := &fakePool{reply: "ok"}
	deps := botDeps{llmPool: pool}

	if _, ok := askCurator(context.Background(), deps, "user-1", nil, "curator who are you?"); !ok {
		t.Fatal("expected a reply")
	}
	wantHeader := "CURRENT CONVERSATION INTENT: " + string(intentIdentity)
	if !strings.Contains(pool.lastReq.Persona, wantHeader) {
		t.Errorf("persona sent to LLM missing %q", wantHeader)
	}
}

// "LLM disabled" acceptance case: no provider call, identity fallback
// returned instead.
func TestAskCurator_LLMDisabled_ReturnsIntentFallback(t *testing.T) {
	deps := botDeps{llmPool: nil}

	reply, ok := askCurator(context.Background(), deps, "user-1", nil, "curator who are you?")
	if !ok || reply == "" {
		t.Fatal("expected a canned identity fallback reply")
	}
	for _, line := range curatorIntentFallbacks[intentInsultProvocation] {
		if reply == line {
			t.Errorf("identity question returned an insult-pool fallback line %q", reply)
		}
	}
}

// "LLM rate-limited" acceptance case: the limiter denies the call, but
// the interaction still gets a deterministic answer rather than failing
// outright.
func TestAskCurator_RateLimited_FallsThroughToIntentFallback(t *testing.T) {
	pool := &fakePool{reply: "should never be seen"}
	limiter := newCuratorLLMLimiter(time.Hour, time.Hour)
	limiter.allow("user-1") // consume the only allowed call up front

	deps := botDeps{llmPool: pool, llmLimiter: limiter}
	reply, ok := askCurator(context.Background(), deps, "user-1", nil, "curator who are you?")
	if !ok || reply == "" {
		t.Fatal("expected a fallback reply when rate-limited")
	}
	if pool.lastCalled {
		t.Error("a rate-limited request must not call the LLM pool")
	}
}

// Insult regression: "curator who are you?" must never resolve to the
// insult-pool's fallback line, even when falling back (LLM disabled).
func TestAskCurator_InsultRegression_IdentityNeverGetsInsultFallback(t *testing.T) {
	deps := botDeps{llmPool: nil}
	for i := 0; i < 20; i++ {
		reply, ok := askCurator(context.Background(), deps, "user-1", nil, "curator who are you?")
		if !ok {
			t.Fatal("expected a reply")
		}
		if reply == "I have been called worse things by better subjects." {
			t.Fatal("identity question fell back to the insult-only line")
		}
	}
}

// Operational-style messages with no special intent still get a reply via
// the generic fallback pool when the LLM is disabled -- no LLM call is
// required to make that decision.
func TestAskCurator_LLMDisabled_GenericMessageStillReplies(t *testing.T) {
	deps := botDeps{llmPool: nil}
	reply, ok := askCurator(context.Background(), deps, "user-1", nil, "nice base you've got there")
	if !ok || reply == "" {
		t.Fatal("expected a generic fallback reply")
	}
}
