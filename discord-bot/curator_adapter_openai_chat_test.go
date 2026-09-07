package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// curator-cerebras-free-tier-diagnostic.md's adapter test: HTTP 402 must
// classify as errKindBillingRequired, distinct from both rate-limit (429)
// and generic transient (5xx/other) -- the pool relies on this
// distinction to treat it as persistent, not a timed retry.
func TestOpenAIChatClient_HTTP402_ReturnsBillingRequiredError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"message":"Payment required to access this resource. Visit your billing tab.","type":"payment_required_error"}`))
	}))
	defer srv.Close()

	client := newOpenAIChatClient(srv.Client(), srv.URL, "test-key-should-never-appear-in-error", "some-model")
	_, err := client.Reply(context.Background(), CuratorRequest{Message: "hi"})
	if err == nil {
		t.Fatal("expected an error for a 402 response")
	}

	var pe *providerError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a *providerError, got %T: %v", err, err)
	}
	if pe.kind != errKindBillingRequired {
		t.Errorf("kind = %v, want errKindBillingRequired", pe.kind)
	}
	if pe.kind == errKindRateLimited {
		t.Error("a 402 must never be classified as rate-limited")
	}
	if strings.Contains(err.Error(), "test-key-should-never-appear-in-error") {
		t.Error("provider error must never echo back the API key")
	}
}

func TestOpenAIChatClient_HTTP429_StillClassifiesAsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := newOpenAIChatClient(srv.Client(), srv.URL, "key", "model")
	_, err := client.Reply(context.Background(), CuratorRequest{Message: "hi"})

	var pe *providerError
	if !errors.As(err, &pe) || pe.kind != errKindRateLimited {
		t.Fatalf("got err=%v, want errKindRateLimited (402 handling must not have disturbed this)", err)
	}
}

// Confirmed live (2026-09-07): sending the "Known facts" system message
// with an empty fact list makes some models (Gemini) refuse to answer at
// all instead of doing their actual job -- for the semantic resolver
// call, that job is strict JSON classification, not fact-grounded
// conversation, and it never sets Context. This locks in that the
// adapter omits the message entirely when Context is empty, and still
// includes it (unchanged) when the personality call sets a real one.
func TestOpenAIChatClient_OmitsKnownFactsMessageWhenContextEmpty(t *testing.T) {
	var captured chatCompletionsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("failed to decode captured request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	client := newOpenAIChatClient(srv.Client(), srv.URL, "key", "model")
	if _, err := client.Reply(context.Background(), CuratorRequest{Persona: "you are a classifier", Message: "hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (persona system + user) when Context is empty: %+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[0].Role != "system" || captured.Messages[0].Content != "you are a classifier" {
		t.Errorf("Messages[0] = %+v, want the persona as the only system message", captured.Messages[0])
	}
	if captured.Messages[1].Role != "user" || captured.Messages[1].Content != "hi" {
		t.Errorf("Messages[1] = %+v, want the user message", captured.Messages[1])
	}
}

func TestOpenAIChatClient_IncludesKnownFactsMessageWhenContextSet(t *testing.T) {
	var captured chatCompletionsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("failed to decode captured request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	client := newOpenAIChatClient(srv.Client(), srv.URL, "key", "model")
	if _, err := client.Reply(context.Background(), CuratorRequest{Persona: "you are Curator", Context: "Most kills: Schabo -- 25.", Message: "hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured.Messages) != 3 {
		t.Fatalf("got %d messages, want 3 (persona system + known-facts system + user) when Context is set: %+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[1].Role != "system" || !strings.Contains(captured.Messages[1].Content, "Most kills: Schabo -- 25.") {
		t.Errorf("Messages[1] = %+v, want a system message containing the Context", captured.Messages[1])
	}
}

func TestOpenAIChatClient_HTTP500_StillClassifiesAsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newOpenAIChatClient(srv.Client(), srv.URL, "key", "model")
	_, err := client.Reply(context.Background(), CuratorRequest{Message: "hi"})

	var pe *providerError
	if !errors.As(err, &pe) || pe.kind != errKindTransient {
		t.Fatalf("got err=%v, want errKindTransient", err)
	}
}
