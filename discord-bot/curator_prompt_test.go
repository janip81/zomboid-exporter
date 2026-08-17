package main

import (
	"strings"
	"testing"
)

// --- CGPT-PERSONA-LIVE-02 / conversation-routing: intent classification ---

func TestClassifyCuratorIntent_Identity(t *testing.T) {
	cases := []string{
		"who is curator",
		"who is the curator",
		"who is that curator",
		"Who Is That Curator?",
		"what is the curator",
		"who's the curator",
		"who's that curator",
		"what's the curator",
		"who are you",
		"who are you?",
		"what are you",
		"curator, what are you?",
		"curator what are you",
		"tell me who you are",
		"are you human",
		"are you a bot",
		"are you an ai",
		"are you real",
		"where are you",
	}
	for _, msg := range cases {
		if got := classifyCuratorIntent(msg); got != intentIdentity {
			t.Errorf("classifyCuratorIntent(%q) = %v, want %v", msg, got, intentIdentity)
		}
	}
}

func TestClassifyCuratorIntent_ActivityPurposeNotSwallowedByIdentity(t *testing.T) {
	cases := []string{
		"what are you doing",
		"what are you doing here?",
		"curator, what are you doing right now",
		"what are you up to",
		"why are you here",
	}
	for _, msg := range cases {
		if got := classifyCuratorIntent(msg); got != intentActivityPurpose {
			t.Errorf("classifyCuratorIntent(%q) = %v, want %v", msg, got, intentActivityPurpose)
		}
	}
}

func TestClassifyCuratorIntent_InsultProvocation(t *testing.T) {
	cases := []string{
		"Curator, you're an asshole.",
		"you are such an idiot",
		"shut up curator",
	}
	for _, msg := range cases {
		if got := classifyCuratorIntent(msg); got != intentInsultProvocation {
			t.Errorf("classifyCuratorIntent(%q) = %v, want %v", msg, got, intentInsultProvocation)
		}
	}
}

// Regression for the exact bug curator-llm-conversation-routing.md reports:
// an insult-only fallback line must never be reachable from an identity
// question, and vice versa.
func TestClassifyCuratorIntent_InsultNeverClassifiedAsIdentity(t *testing.T) {
	if got := classifyCuratorIntent("curator who are you?"); got != intentIdentity {
		t.Fatalf("classifyCuratorIntent(%q) = %v, want %v", "curator who are you?", got, intentIdentity)
	}
	for _, line := range curatorIntentFallbacks[intentIdentity] {
		if line == "I have been called worse things by better subjects." {
			t.Error("insult-only fallback line leaked into the IDENTITY fallback pool")
		}
	}
	found := false
	for _, line := range curatorIntentFallbacks[intentInsultProvocation] {
		if line == "I have been called worse things by better subjects." {
			found = true
		}
	}
	if !found {
		t.Error("expected the insult line to live in the INSULT_PROVOCATION fallback pool")
	}
}

func TestClassifyCuratorIntent_SelfStats(t *testing.T) {
	cases := []string{
		"am I doing well?",
		"how am I doing",
		"how many kills do I have",
		"what are my stats",
	}
	for _, msg := range cases {
		if got := classifyCuratorIntent(msg); got != intentSelfStats {
			t.Errorf("classifyCuratorIntent(%q) = %v, want %v", msg, got, intentSelfStats)
		}
	}
}

func TestClassifyCuratorIntent_LoreMystery(t *testing.T) {
	cases := []string{
		"what caused the Knox Event?",
		"is this an experiment",
		"is this a test",
		"are you watching us",
	}
	for _, msg := range cases {
		if got := classifyCuratorIntent(msg); got != intentLoreMystery {
			t.Errorf("classifyCuratorIntent(%q) = %v, want %v", msg, got, intentLoreMystery)
		}
	}
}

func TestClassifyCuratorIntent_DefaultsToGeneric(t *testing.T) {
	if got := classifyCuratorIntent("nice base you've got there"); got != intentGenericCurator {
		t.Errorf("classifyCuratorIntent(...) = %v, want %v", got, intentGenericCurator)
	}
}

// --- intent guidance / fallback pool completeness --------------------------

var allCuratorIntents = []curatorIntent{
	intentIdentity, intentActivityPurpose, intentInsultProvocation,
	intentSelfStats, intentOtherPlayer, intentLoreMystery, intentGenericCurator,
}

func TestCuratorIntentGuidanceText_CoversEveryIntent(t *testing.T) {
	for _, intent := range allCuratorIntents {
		guidance := curatorIntentGuidance(intent)
		if !strings.Contains(guidance, "CURRENT CONVERSATION INTENT: "+string(intent)) {
			t.Errorf("guidance for %v missing its own intent header: %q", intent, guidance)
		}
	}
}

func TestCuratorIntentFallbacks_EveryIntentHasNonEmptyPool(t *testing.T) {
	for _, intent := range allCuratorIntents {
		reply, ok := matchIntentFallback(intent)
		if !ok || reply == "" {
			t.Errorf("intent %v has no usable fallback pool", intent)
		}
	}
}

// --- CGPT-PERSONA-LIVE-01/03: prompt contract regression ------------------

func TestAssembleCuratorPersona_ContainsRoleDisclosureGuardrail(t *testing.T) {
	persona := assembleCuratorPersona(curatorTierCommon, curatorIntentGuidance(intentGenericCurator))
	mustContain := []string{
		"NEVER EXPLAIN YOUR ROLE",
		"an unseen observer",
		"a research system",
	}
	for _, want := range mustContain {
		if !strings.Contains(persona, want) {
			t.Errorf("assembled persona missing expected guardrail text %q -- a future refactor may have deleted CGPT-PERSONA-LIVE-01's role-disclosure rule", want)
		}
	}
}

func TestAssembleCuratorPersona_ContainsActivityQuestionGuardrail(t *testing.T) {
	persona := assembleCuratorPersona(curatorTierCommon, curatorIntentGuidance(intentGenericCurator))
	if !strings.Contains(persona, `what are you doing`) {
		t.Error("assembled persona missing the anti-mission-statement guidance for activity questions (CGPT-PERSONA-LIVE-03)")
	}
}

func TestAssembleCuratorPersona_IncludesIntentGuidance(t *testing.T) {
	persona := assembleCuratorPersona(curatorTierCommon, curatorIntentGuidance(intentSelfStats))
	if !strings.Contains(persona, "CURRENT CONVERSATION INTENT: SELF_STATS") {
		t.Error("assembled persona did not include the requested intent's guidance block")
	}
}

// --- CGPT-PERSONA-LIVE-05: example-data isolation --------------------------

func TestCuratorCanonicalExamples_NoRealLookingPlayerNames(t *testing.T) {
	// A real player name here would be indistinguishable, to the model,
	// from an actual Known Facts callback -- style examples must stay
	// generic/fictional (curator-llm-personality-live-test-review.md).
	banned := []string{"Benjamin"}
	for _, name := range banned {
		if strings.Contains(curatorCanonicalExamples, name) {
			t.Errorf("curatorCanonicalExamples still contains real-looking player name %q", name)
		}
	}
}
