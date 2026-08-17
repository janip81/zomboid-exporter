package main

import (
	"context"
	"errors"
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
