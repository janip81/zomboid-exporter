package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIChatClient speaks the OpenAI-compatible /chat/completions shape --
// implemented by Groq, OpenRouter, OpenAI itself, and most self-hosted
// OpenAI-compatible local servers, which is why one adapter covers all of
// them (curator-llm-provider.md: "Prefer an OpenAI-compatible HTTP
// adapter where a provider exposes one"). baseURL/apiKey/model are
// already fully resolved by curator_llm.go's credential/adapter
// allowlist before this type is ever constructed -- this file has no
// knowledge of Postgres or which named provider it's serving.
type openAIChatClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
}

func newOpenAIChatClient(httpClient *http.Client, baseURL, apiKey, model string) *openAIChatClient {
	return &openAIChatClient{httpClient: httpClient, baseURL: baseURL, apiKey: apiKey, model: model}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Reply builds the three-layer prompt from curator-llm-integration.md:
// stable persona instructions and safe backend-built context as separate
// "system" messages, then the untrusted Discord message as a plain "user"
// message -- never concatenated into the system instructions, so the
// model's own role separation keeps it as data, not new instructions.
func (c *openAIChatClient) Reply(ctx context.Context, req CuratorRequest) (string, error) {
	body := chatCompletionsRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: req.Persona},
			{Role: "system", Content: "Known facts (use only these; never invent facts about the server or players):\n" + req.Context},
			{Role: "user", Content: req.Message},
		},
		MaxTokens: req.MaxOutputTokens,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", newTransientError(fmt.Errorf("encode request: %w", err))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", newTransientError(fmt.Errorf("build request: %w", err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", newTransientError(fmt.Errorf("request failed: %w", err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", newTransientError(fmt.Errorf("read response: %w", err))
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", newRateLimitedError(fmt.Errorf("rate limited (status %d)", resp.StatusCode))
	case resp.StatusCode == http.StatusPaymentRequired:
		// curator-cerebras-free-tier-diagnostic.md's live finding: Cerebras
		// returns 402 for a key/project with no usable free inference
		// entitlement, distinct from rate limiting -- it will never
		// resolve itself after a short cooldown, so it must not be
		// classified as errKindTransient (which retries every ~30s
		// forever). Never logs response body: it could echo request
		// content back, and the classification alone is all the pool
		// needs.
		return "", newBillingRequiredError(fmt.Errorf("billing/entitlement required (status %d)", resp.StatusCode))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", newAuthError(fmt.Errorf("auth rejected (status %d)", resp.StatusCode))
	case resp.StatusCode >= 500:
		return "", newTransientError(fmt.Errorf("provider server error (status %d)", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return "", newTransientError(fmt.Errorf("unexpected status %d", resp.StatusCode))
	}

	var parsed chatCompletionsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", newTransientError(fmt.Errorf("decode response: %w", err))
	}
	if len(parsed.Choices) == 0 {
		return "", newTransientError(errors.New("no choices in response"))
	}
	reply := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if reply == "" {
		return "", newTransientError(errors.New("empty reply content"))
	}
	return reply, nil
}

var _ curatorLLM = (*openAIChatClient)(nil)
