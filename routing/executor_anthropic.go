package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// AnthropicExecutor is the ProtocolHandler for Anthropic Messages API
// (and compatible endpoints like minimax /anthropic).
//
// CRITICAL INVARIANTS:
//   - MUST use x-api-key (NOT Authorization: Bearer) for auth
//   - MUST NOT inject stream_options (Anthropic doesn't have it)
//   - MUST NOT collapse tool history (Anthropic has native tool_use)
//   - MUST NOT call disguise (Anthropic field shapes differ)
//   - MUST NOT call XMLCoerce (Anthropic has native tool_use blocks)
//
// For passthrough mode (Q4), body bytes are forwarded unchanged.
// For conversion mode (Q3), the relay layer has already converted
// the body to Anthropic shape; this executor just sends it.
type AnthropicExecutor struct {
	Common *CommonExecutor
	// PassthroughStream is wired by the surrounding Executor at call
	// time and points at relay/anthropic_passthrough_stream.go's
	// StreamAnthropicPassthrough. Required for Q4 streaming — the
	// routing package cannot import relay (relay imports routing),
	// so the function is injected as a hook.
	PassthroughStream func(w http.ResponseWriter, resp *http.Response) StreamOutcome
}

const anthropicVersion = "2023-06-01"

func (a *AnthropicExecutor) BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error) {
	base := strings.TrimRight(cand.BaseURL, "/")
	upstreamURL := base + "/v1/messages"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cand.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel string) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if clientModel != "" {
		body = replaceModelInResponseBody(body, clientModel)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return err
}

func (a *AnthropicExecutor) StreamResponse(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if a.PassthroughStream != nil {
		return a.PassthroughStream(w, resp)
	}
	return defaultAnthropicPassthrough(w, resp)
}

func (a *AnthropicExecutor) ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int) {
	return extractAnthropicUsageFromBody(body)
}

func (a *AnthropicExecutor) CheckSoftMismatch(reqModel, respModel string) (bool, string) {
	if reqModel == "" || respModel == "" {
		return false, ""
	}
	if !strings.EqualFold(reqModel, respModel) {
		return true, "anthropic_response_model_differs_from_request"
	}
	return false, ""
}

func extractAnthropicUsageFromBody(body []byte) (*int, *int) {
	var v struct {
		Usage struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	return v.Usage.InputTokens, v.Usage.OutputTokens
}

// defaultAnthropicPassthrough is the Q4 fallback when no PassthroughStream
// hook is wired: forward SSE bytes unchanged via io.Copy. The production
// implementation (with side-channel audit capture) lives in
// relay/anthropic_passthrough_stream.go and is injected via the
// PassthroughStream hook on AnthropicExecutor.
func defaultAnthropicPassthrough(w http.ResponseWriter, resp *http.Response) StreamOutcome {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = io.Copy(w, resp.Body)
	return StreamOutcome{}
}
