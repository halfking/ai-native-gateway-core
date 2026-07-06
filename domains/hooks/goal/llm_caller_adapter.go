package goal

// llm_caller_adapter.go — adapts an OpenAI-compatible HTTP endpoint to the
// goal.LLMCaller interface.
//
// goal's completion detector and audit hook call LLMCaller.CallLLM(ctx, model,
// []messages) to run an LLM judgement (e.g. "is the task done?", "audit this
// code"). The autoroute package already ships HTTPLlmCaller, but its Call
// method only takes a single classification prompt — it ignores the messages
// shape the goal prompts need. Rather than fork the HTTP plumbing, this file
// reuses autoroute.HTTPLlmCallerConfig (same endpoint/key/model wiring as the
// rest of the gateway) and sends a full chat-completions request with the
// caller-provided messages.
//
// Wiring (cmd/gateway/main.go):
//
//	caller := goal.NewChatLLMCaller(autoroute.HTTPLlmCallerConfig{...})
//	goal.NewModeHook(cfg, store, caller)
//
// The returned text is whatever choices[0].message.content holds, which the
// detector/audit hook then parse as JSON. When the endpoint is unset the
// caller is a no-op that returns an error — callers already fall back to the
// keyword/heuristic detection strategies on error.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// chatLLMCaller implements LLMCaller against an OpenAI-compatible
// /chat/completions endpoint, reusing autoroute.HTTPLlmCallerConfig so the
// goal feature shares endpoint/key/model wiring with auto-route.
type chatLLMCaller struct {
	cfg autoroute.HTTPLlmCallerConfig
}

// NewChatLLMCaller builds an LLMCaller from an autoroute HTTPLlmCallerConfig.
// ApplyDefaults fills zero-value fields (timeout/model/max-tokens/client) with
// the same defaults autoroute uses, so the same env-driven config object can
// be passed to both features.
func NewChatLLMCaller(cfg autoroute.HTTPLlmCallerConfig) LLMCaller {
	ApplyHTTPLlmCallerDefaults(&cfg)
	return &chatLLMCaller{cfg: cfg}
}

// ApplyHTTPLlmCallerDefaults fills zero-value fields of an autoroute
// HTTPLlmCallerConfig with goal-appropriate defaults. Goal prompts return JSON
// (audit results, completion verdicts) so the default MaxTokens is larger than
// autoroute's classification-oriented 16. Exposed so main.go can build one
// config object and reuse it for both autoroute and goal.
func ApplyHTTPLlmCallerDefaults(cfg *autoroute.HTTPLlmCallerConfig) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.MaxTokens == 0 {
		// Goal judgement prompts ask for structured JSON; 16 tokens is too
		// small. 1024 comfortably fits an audit result with several issues.
		cfg.MaxTokens = 1024
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
}

// CallLLM implements LLMCaller. It POSTs the provided messages to the
// configured /chat/completions endpoint and returns choices[0].message.content.
//
// The model argument overrides cfg.Model when non-empty, so the audit hook can
// target a specific audit model (e.g. "auto" routed to a code-audit model)
// while sharing a single caller instance.
func (c *chatLLMCaller) CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error) {
	if c.cfg.Endpoint == "" {
		return "", errors.New("goal: chat LLM caller endpoint not configured")
	}

	chosen := c.cfg.Model
	if strings.TrimSpace(model) != "" {
		chosen = model
	}

	reqBody := map[string]any{
		"model":       chosen,
		"messages":    messages,
		"max_tokens":  c.cfg.MaxTokens,
		"temperature": 0.0, // deterministic judgement
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("goal: marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.Endpoint, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("goal: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	httpReq = httpReq.WithContext(callCtx)

	resp, err := c.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		if callCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", fmt.Errorf("goal: HTTP call: %w", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("goal: HTTP %d: %s", resp.StatusCode, string(excerpt))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("goal: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("goal: empty choices in response")
	}
	return parsed.Choices[0].Message.Content, nil
}

// noopLLMCaller is a safe no-op LLMCaller used when no endpoint is configured.
// All detection strategies that depend on an LLM (completion_detector's
// checkWithLLM, audit_hook's performAudit) gracefully degrade when this
// returns an error — they fall back to the keyword/heuristic path or skip.
type noopLLMCaller struct{}

func (noopLLMCaller) CallLLM(_ context.Context, _ string, _ []map[string]string) (string, error) {
	return "", errors.New("goal: LLM caller not configured")
}

// NoopLLMCaller returns the package-level no-op LLMCaller. Use it as a
// placeholder when goal is wired but no audit/classification endpoint is set,
// so the feature flag can be enabled without a hard runtime dependency.
func NoopLLMCaller() LLMCaller { return noopLLMCaller{} }
