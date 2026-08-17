package main

import (
	"strings"
	"testing"
)

// --- CGPT-PERSONA-LIVE-02: canned identity routing ------------------------

func TestIsCuratorIdentityQuestion_PositiveCases(t *testing.T) {
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
	}
	for _, msg := range cases {
		if !isCuratorIdentityQuestion(msg) {
			t.Errorf("expected %q to match as an identity question", msg)
		}
	}
}

func TestIsCuratorIdentityQuestion_NegativeCases(t *testing.T) {
	cases := []string{
		"what are you doing",
		"what are you doing here?",
		"curator, what are you doing right now",
		"are you human",
		"are you watching us",
		"is this an experiment",
		"curator probably knows where the box is",
		"where did you put the curator's notes",
	}
	for _, msg := range cases {
		if isCuratorIdentityQuestion(msg) {
			t.Errorf("expected %q to NOT match as an identity question", msg)
		}
	}
}

func TestMatchCannedResponse_IdentityQuestionUsesAuthoredPool(t *testing.T) {
	reply, ok := matchCannedResponse("who is that curator")
	if !ok {
		t.Fatal("expected the live-test regression phrase to match a canned reply")
	}
	found := false
	for _, want := range curatorIdentityReplies {
		if reply == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reply %q was not drawn from curatorIdentityReplies", reply)
	}
}

func TestMatchCannedResponse_ActivityQuestionFallsThrough(t *testing.T) {
	// "what are you doing" must NOT be swallowed by the identity matcher --
	// it's the activity-question case CGPT-PERSONA-LIVE-03 addresses via
	// LLM few-shot guidance, not a canned reply.
	if _, ok := matchCannedResponse("what are you doing"); ok {
		t.Error("expected \"what are you doing\" to fall through to the LLM path, not match a canned reply")
	}
}

// --- CGPT-PERSONA-LIVE-01/03: prompt contract regression ------------------

func TestAssembleCuratorPersona_ContainsRoleDisclosureGuardrail(t *testing.T) {
	persona := assembleCuratorPersona(curatorTierCommon)
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
	persona := assembleCuratorPersona(curatorTierCommon)
	if !strings.Contains(persona, `what are you doing`) {
		t.Error("assembled persona missing the anti-mission-statement guidance for activity questions (CGPT-PERSONA-LIVE-03)")
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
