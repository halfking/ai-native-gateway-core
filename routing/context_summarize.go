package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
)

const (
	defaultCompactionMinWindow = 800_000
	defaultCompactionSummaryMaxTokens = 4096
	compactionSummaryPrefix            = "[Gateway compacted conversation summary — prior turns collapsed to fit context]\n"
	// heuristicCompactBytePerToken approximates chars-per-token for the
	// body-size heuristic. Same ratio used by transform.estimateMessageTokens.
	heuristicCompactBytePerToken = 3.5
	// heuristicCompactThresholdFraction: when the estimated prompt tokens
	// exceed this fraction of the candidate's context window, a non-auth
	// 4xx is treated as a probable context-overflow and the compaction
	// fallback is attempted.
	heuristicCompactThresholdFraction = 0.9
)

// shouldHeuristicCompact decides whether to attempt context compaction as a
// fallback when the upstream returned a 4xx that is NOT explicitly classified
// as context_length_exceeded. This catches providers that return non-standard
// error bodies (or even model_not_found) when the prompt is genuinely too
// large — the body-size heuristic is the signal.
//
// Returns true only when ALL of:
//   - status is a 4xx client error (not 5xx, not 2xx)
//   - status is not an auth/billing/rate-limit code (401/402/403/429)
//   - the error kind is not concurrent overload (that needs a different fix)
//   - the error kind is not model_not_found (that's a routing issue, not size)
//   - estimated tokens (len(body)/3.5) exceed 90% of the candidate's context window
//
// When ContextWindow is nil (unknown), we cannot heuristically estimate, so
// this returns false — only the explicit IsContextLength path triggers recovery
// in that case.
func shouldHeuristicCompact(status int, kind errorsx.ErrorKind, bodyLen int, contextWindow *int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case 401, 402, 403, 429:
		return false
	}
	if kind == errorsx.KindConcurrent || kind == errorsx.KindModelNotFound {
		return false
	}
	if contextWindow == nil || *contextWindow <= 0 {
		return false
	}
	estimatedTokens := int(float64(bodyLen) / heuristicCompactBytePerToken)
	return estimatedTokens > int(float64(*contextWindow)*heuristicCompactThresholdFraction)
}

const compactionSystemPrompt = `You compress long agent conversation history for downstream LLM calls.
Preserve: user goals, decisions, file paths, errors, tool outcomes, API results, and open tasks.
Drop: pleasantries, repeated stack traces, duplicate tool dumps.
Write a dense factual summary only — no preamble, no markdown fences.`

var defaultCompactionModels = []string{
	"minimax-text-01",   // same vendor, 1M ctx
	"gemini-2.5-flash",  // cross-vendor, 1M ctx, cost-effective
}

// compactionModelsFromEnv returns ordered compaction model IDs. Lower index = preferred (cost/affinity).
func compactionModelsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_COMPACTION_MODELS"))
	if raw == "" {
		return append([]string(nil), defaultCompactionModels...)
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), defaultCompactionModels...)
	}
	return out
}

func compactionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_COMPACTION_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// pickCompactionCandidates returns every viable compaction candidate in
// env-preference order (cost / affinity). Used by tryLLMContextCompaction to
// build the fallback chain: model1+credA → model1+credB → model2+credA → ...
//
// Lower index = preferred. The caller walks the slice in order, calling
// invokeCompactionSummarize on each, and stops on the first success.
func (e *Executor) pickCompactionCandidates(ctx context.Context, profile string) []provider.Candidate {
	if e.Provider == nil || !e.Provider.Enabled() {
		return nil
	}
	minWindow := defaultCompactionMinWindow
	var out []provider.Candidate
	for _, model := range compactionModelsFromEnv() {
		cands, _, err := e.Provider.GetCandidates(ctx, model, profile)
		if err != nil {
			slog.Debug("compaction: resolve candidates failed", "model", model, "error", err)
			continue
		}
		added := 0
		for i := range cands {
			c := &cands[i]
			if !c.IsAvailable() {
				continue
			}
			if c.ContextWindow != nil && *c.ContextWindow < minWindow {
				continue
			}
			out = append(out, *c)
			added++
		}
		if added == 0 {
			slog.Debug("compaction: no available candidates for model", "model", model)
		}
	}
	return out
}

// tryLLMContextCompaction summarizes oversized history using a large-context
// model, then rebuilds the client wire body with [summary + recent tail].
// Returns the new source body and true when compaction succeeded.
//
// Falls back across the env-configured compaction model list (and across
// credentials within a model) so a single down/saturated credential does
// not abort recovery. The caller sees ctxLenGiveUp only when the entire
// chain has been exhausted.
//
// tryMemoraCompression is the FIRST attempt at context rebuilding,
// BEFORE the LLM-summary fallback. It asks Memora for the task's most
// relevant L1 session facts and rebuilds the body around them as a
// "dynamic_context" user message. This is strictly best-effort: any
// failure (Memora down, 0 results, parse error) returns (nil, false)
// and the caller falls through to the LLM summary path.
func (e *Executor) tryMemoraCompression(
	ctx context.Context,
	params *ExecParams,
	sourceBody []byte,
) ([]byte, bool) {
	if e.Memora == nil || e.Memora.Disabled() || params == nil || len(sourceBody) == 0 {
		return nil, false
	}
	apiKeyID := extractAPIKeyID(params.R, params.ClientID)
	taskID := memora.TaskID(params.R, sourceBody, apiKeyID)
	if taskID == "" {
		return nil, false
	}
	// Round 47 compression v7 T13: tenant-namespaced user_id.
	userID := memora.UserID(params.TenantID, apiKeyID, taskID)
	if userID == "" {
		return nil, false
	}
	// Build a "what is the user trying to do right now?" query from the
	// last user/assistant turns. Use the existing extractor so we get
	// consistent text regardless of wire format.
	query := buildMemoraQuery(sourceBody, params.ClientProtocol)
	if query == "" {
		return nil, false
	}
	snippets, err := e.Memora.Search(ctx, userID, query, 8)
	if err != nil || len(snippets) == 0 {
		return nil, false
	}
	newBody, ok := memora.RebuildBodyWithMemoraSnippets(sourceBody, snippets, 2)
	if !ok {
		return nil, false
	}
	return newBody, true
}

// buildMemoraQuery turns the last few messages of an OpenAI/Anthropic
// conversation into a plain-text query for Memora's /product/search.
// We deliberately re-use the existing extractConversationText helper so
// the wire format handling stays in one place.
func buildMemoraQuery(body []byte, protocol string) string {
	text, err := extractConversationText(body, protocol)
	if err != nil || strings.TrimSpace(text) == "" {
		return ""
	}
	// Last ~1500 chars is enough for a query; longer just slows search.
	if len(text) > 1500 {
		text = text[len(text)-1500:]
	}
	return text
}

// stableHashKey returns a stable non-negative int hash of the given
// string. Used as a fallback api_key_id when Resolution.APIKeyID is
// unavailable. FNV-1a 32-bit; collisions across distinct strings are
// acceptable (they only widen the user_id namespace).
func stableHashKey(s string) int {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return int(h & 0x7fffffff)
}

func (e *Executor) tryLLMContextCompaction(
	ctx context.Context,
	params *ExecParams,
	targetCand provider.Candidate,
	sourceBody []byte,
) ([]byte, bool) {
	if compactionDisabled() {
		return sourceBody, false
	}
	if params == nil || params.R == nil {
		return sourceBody, false
	}

	profile := params.ClientID.Fingerprint.ClientProfile

	conversation, err := extractConversationText(sourceBody, params.ClientProtocol)
	if err != nil || strings.TrimSpace(conversation) == "" {
		slog.Warn("compaction: extract conversation failed", "error", err)
		return sourceBody, false
	}

	// Keep summarize input under ~900k tokens (heuristic) so 1M models fit.
	conversation = trimTextToTokenBudget(conversation, 900_000)

	candidates := e.pickCompactionCandidates(ctx, profile)
	if len(candidates) == 0 {
		slog.Warn("compaction: no candidate",
			"target_model", targetCand.RawModel,
			"configured_models", compactionModelsFromEnv(),
		)
		return sourceBody, false
	}

	// Walk the fallback chain: model1+credA → model1+credB → model2+credA → ...
	// Stop on the first summarize success. Per-attempt errors are logged at
	// WARN with attempt index so the operator can see how far the chain got.
	var (
		summary    string
		usedCand   *provider.Candidate
		triedCount int
	)
	for i := range candidates {
		cand := candidates[i]
		triedCount++
		s, sErr := e.invokeCompactionSummarize(ctx, params, cand, conversation)
		if sErr != nil {
			slog.Warn("compaction: summarize call failed, trying next",
				"attempt", triedCount,
				"of", len(candidates),
				"compact_model", cand.RawModel,
				"compact_credential_id", cand.CredentialID,
				"target_model", targetCand.RawModel,
				"error", sErr,
			)
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			slog.Warn("compaction: empty summary returned, trying next",
				"attempt", triedCount,
				"of", len(candidates),
				"compact_model", cand.RawModel,
				"compact_credential_id", cand.CredentialID,
			)
			continue
		}
		summary = s
		usedCand = &candidates[i]
		break
	}
	if usedCand == nil {
		slog.Warn("compaction: all candidates exhausted",
			"target_model", targetCand.RawModel,
			"tried", triedCount,
			"of", len(candidates),
		)
		return sourceBody, false
	}

	var rebuilt []byte
	switch params.ClientProtocol {
	case "anthropic-messages":
		rebuilt, err = rebuildAnthropicBodyAfterSummary(sourceBody, summary, 2)
	default:
		rebuilt, err = rebuildOpenAIBodyAfterSummary(sourceBody, summary, 2)
	}
	if err != nil {
		slog.Warn("compaction: rebuild body failed", "error", err)
		return sourceBody, false
	}

	slog.Info("compaction: llm summary applied",
		"compact_model", usedCand.RawModel,
		"compact_credential_id", usedCand.CredentialID,
		"attempt", triedCount,
		"of", len(candidates),
		"target_model", targetCand.RawModel,
		"target_credential_id", targetCand.CredentialID,
		"source_bytes_before", len(sourceBody),
		"source_bytes_after", len(rebuilt),
	)
	return rebuilt, true
}

func (e *Executor) invokeCompactionSummarize(ctx context.Context, params *ExecParams, cand provider.Candidate, conversation string) (string, error) {
	userContent := "Summarize the following conversation history:\n\n" + conversation
	timeout := 120 * time.Second
	if e.UpstreamTimeout > 0 {
		timeout = e.UpstreamTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if cand.Protocol == "anthropic-messages" {
		return e.invokeAnthropicSummarize(ctx, params, cand, userContent)
	}
	return e.invokeOpenAISummarize(ctx, params, cand, userContent)
}

func (e *Executor) invokeOpenAISummarize(ctx context.Context, params *ExecParams, cand provider.Candidate, userContent string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model":       cand.RawModel,
		"max_tokens":  defaultCompactionSummaryMaxTokens,
		"temperature": 0.2,
		"stream":      false,
		"messages": []map[string]string{
			{"role": "system", "content": compactionSystemPrompt},
			{"role": "user", "content": userContent},
		},
	})
	if err != nil {
		return "", err
	}

	resp, err := e.doCompactionUpstream(ctx, params, cand, payload, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("openai summarize upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("openai summarize: empty choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func (e *Executor) invokeAnthropicSummarize(ctx context.Context, params *ExecParams, cand provider.Candidate, userContent string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model":      cand.RawModel,
		"max_tokens": defaultCompactionSummaryMaxTokens,
		"system":     compactionSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userContent},
		},
	})
	if err != nil {
		return "", err
	}

	resp, err := e.doCompactionUpstream(ctx, params, cand, payload, true)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic summarize upstream %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" && block.Text != "" {
			b.WriteString(block.Text)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("anthropic summarize: empty content")
	}
	return b.String(), nil
}

func (e *Executor) doCompactionUpstream(
	ctx context.Context,
	params *ExecParams,
	cand provider.Candidate,
	payload []byte,
	anthropic bool,
) (*http.Response, error) {
	if e.Circuit != nil && !e.Circuit.Allow(cand.ProviderID, cand.CredentialID) {
		return nil, fmt.Errorf("compaction: circuit open for credential %d", cand.CredentialID)
	}

	var releaseLimiter func()
	if e.Limiter != nil && params != nil {
		rel, err := e.Limiter.AcquireAll(ctx, cand.ProviderID, cand.CredentialID, params.ClientID.IdentityHash, params.KeyID, params.KeyConcurrentLimit)
		if err != nil {
			return nil, fmt.Errorf("compaction: limiter: %w", err)
		}
		releaseLimiter = rel
	}
	if releaseLimiter != nil {
		defer releaseLimiter()
	}

	var url string
	if anthropic {
		url = upstreamurl.MessagesURL(cand.BaseURL)
	} else {
		url = upstreamurl.ChatCompletionsURL(cand.BaseURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	e.applyCompactionRequestHeaders(req, params, cand, anthropic)

	resp, err := e.doCompactionHTTP(req)
	if err != nil {
		if e.Circuit != nil {
			e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindNetwork)
		}
		return nil, err
	}
	if e.Circuit != nil {
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.ClassifyErrorWithBody(resp.StatusCode, nil))
		} else if resp.StatusCode < 400 {
			e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
		}
	}
	return resp, nil
}

func (e *Executor) applyCompactionRequestHeaders(req *http.Request, params *ExecParams, cand provider.Candidate, anthropic bool) {
	if anthropic {
		req.Header.Set("x-api-key", cand.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+cand.APIKey)
	}
	if params == nil || params.R == nil {
		return
	}
	req.Header.Set("X-Request-Id", params.R.Header.Get("X-Request-Id"))
	req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
	req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
	req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)
	if params.Transform != nil {
		for _, h := range params.Transform.StripHeaders {
			req.Header.Del(h)
		}
		for k, v := range params.Transform.InjectHeaders {
			req.Header.Set(k, v)
		}
	}
	if e.HeaderProfiles != nil {
		if prof := e.HeaderProfiles.load(params.R.Context(), cand.CatalogCode, cand.Protocol); prof != nil {
			for k, v := range prof.Headers {
				req.Header.Set(k, v)
			}
		}
	}
}

func (e *Executor) doCompactionHTTP(req *http.Request) (*http.Response, error) {
	if e.Upstream != nil {
		resp, uErr := e.Upstream.Do(req)
		if uErr != nil {
			if uErr.Err != nil {
				return nil, uErr.Err
			}
			return nil, fmt.Errorf("upstream: %s", uErr.Message)
		}
		return resp, nil
	}
	return http.DefaultClient.Do(req)
}

func extractConversationText(body []byte, clientProtocol string) (string, error) {
	if clientProtocol == "anthropic-messages" {
		return extractAnthropicConversationText(body)
	}
	return extractOpenAIConversationText(body)
}

func extractOpenAIConversationText(body []byte) (string, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, raw := range req.Messages {
		role, text := messageRoleAndSummary(raw)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	return b.String(), nil
}

func extractAnthropicConversationText(body []byte) (string, error) {
	var req struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}
	var b strings.Builder
	if len(req.System) > 0 {
		if text := rawJSONTextContent(req.System); text != "" {
			fmt.Fprintf(&b, "[system]\n%s\n\n", text)
		}
	}
	for _, raw := range req.Messages {
		role, text := messageRoleAndSummary(raw)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	return b.String(), nil
}

func messageRoleAndSummary(raw json.RawMessage) (role, text string) {
	var probe struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", ""
	}
	var parts []string
	if t := rawJSONTextContent(probe.Content); t != "" {
		parts = append(parts, t)
	}
	if toolText := formatOpenAIToolCalls(probe.ToolCalls); toolText != "" {
		parts = append(parts, toolText)
	}
	if blockText := formatAnthropicToolBlocks(probe.Content); blockText != "" {
		parts = append(parts, blockText)
	}
	if probe.Role == "tool" {
		label := probe.Name
		if label == "" {
			label = probe.ToolCallID
		}
		if label != "" {
			parts = append([]string{fmt.Sprintf("tool_result(%s)", label)}, parts...)
		} else {
			parts = append([]string{"tool_result"}, parts...)
		}
	}
	return probe.Role, strings.TrimSpace(strings.Join(parts, "\n"))
}

func formatOpenAIToolCalls(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil || len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range calls {
		name := c.Function.Name
		if name == "" {
			name = c.ID
		}
		fmt.Fprintf(&b, "tool_call(%s): %s\n", name, truncateForLog([]byte(c.Function.Arguments), 512))
	}
	return strings.TrimSpace(b.String())
}

func formatAnthropicToolBlocks(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var parts []struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Text      string          `json:"text"`
		Content   json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "tool_use":
			fmt.Fprintf(&b, "tool_use(%s): %s\n", p.Name, truncateForLog(p.Input, 512))
		case "tool_result":
			result := p.Text
			if result == "" {
				result = rawJSONTextContent(p.Content)
			}
			fmt.Fprintf(&b, "tool_result(%s): %s\n", p.ToolUseID, truncateForLog([]byte(result), 512))
		}
	}
	return strings.TrimSpace(b.String())
}

func messageRoleAndText(raw json.RawMessage) (role, text string) {
	return messageRoleAndSummary(raw)
}

func rawJSONTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// string content
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// array of parts (openai / anthropic)
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			switch p.Type {
			case "text", "input_text", "output_text":
				if p.Text != "" {
					b.WriteString(p.Text)
					b.WriteByte('\n')
				}
			}
		}
		return strings.TrimSpace(b.String())
	}
	return string(raw)
}

func rebuildOpenAIBodyAfterSummary(body []byte, summary string, keepRecentPairs int) ([]byte, error) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, err
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	system, rest := splitSystemMessages(req.Messages)
	tail := tailMessagesToolAware(rest, keepRecentPairs)
	summaryMsg, _ := json.Marshal(map[string]string{
		"role":    "user",
		"content": compactionSummaryPrefix + summary,
	})
	out := make([]json.RawMessage, 0, len(system)+1+len(tail))
	out = append(out, system...)
	out = append(out, summaryMsg)
	out = append(out, tail...)
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	generic["messages"] = raw
	return json.Marshal(generic)
}

func rebuildAnthropicBodyAfterSummary(body []byte, summary string, keepRecentPairs int) ([]byte, error) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, err
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	tail := tailMessagesToolAware(req.Messages, keepRecentPairs)
	summaryMsg, _ := json.Marshal(map[string]string{
		"role":    "user",
		"content": compactionSummaryPrefix + summary,
	})
	out := make([]json.RawMessage, 0, 1+len(tail))
	out = append(out, summaryMsg)
	out = append(out, tail...)
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	generic["messages"] = raw
	return json.Marshal(generic)
}

func splitSystemMessages(messages []json.RawMessage) (system, rest []json.RawMessage) {
	for _, m := range messages {
		if isOpenAISystemMessage(m) {
			system = append(system, m)
		} else {
			rest = append(rest, m)
		}
	}
	return system, rest
}

func isOpenAISystemMessage(raw json.RawMessage) bool {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Role == "system"
}

// tailMessagesToolAware keeps recent tail without splitting tool rounds.
func tailMessagesToolAware(messages []json.RawMessage, keepRecentPairs int) []json.RawMessage {
	if keepRecentPairs <= 0 || len(messages) == 0 {
		return nil
	}
	maxKeep := keepRecentPairs * 2
	if len(messages) <= maxKeep {
		return append([]json.RawMessage(nil), messages...)
	}
	start := len(messages) - maxKeep
	start = extendTailStartForToolIntegrity(messages, start)
	return append([]json.RawMessage(nil), messages[start:]...)
}

func extendTailStartForToolIntegrity(messages []json.RawMessage, start int) int {
	for start > 0 && tailStartNeedsBackwardExtension(messages, start) {
		start--
	}
	return start
}

func tailStartNeedsBackwardExtension(messages []json.RawMessage, start int) bool {
	if start <= 0 || start >= len(messages) {
		return false
	}
	role := messageRoleOnly(messages[start])
	switch role {
	case "tool":
		return true
	case "assistant":
		if messageHasToolCalls(messages[start]) || messageHasAnthropicToolUse(messages[start]) {
			return true
		}
	case "user":
		if messageHasAnthropicToolResult(messages[start]) {
			return true
		}
	}
	return false
}

func messageHasToolCalls(raw json.RawMessage) bool {
	var probe struct {
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return len(probe.ToolCalls) > 0 && string(probe.ToolCalls) != "null"
}

func messageHasAnthropicToolUse(raw json.RawMessage) bool {
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.Content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_use" {
			return true
		}
	}
	return false
}

func messageRoleOnly(raw json.RawMessage) string {
	var probe struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Role
}

func messageHasAnthropicToolResult(raw json.RawMessage) bool {
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(probe.Content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_result" {
			return true
		}
	}
	return false
}

func tailMessages(messages []json.RawMessage, keepRecentPairs int) []json.RawMessage {
	return tailMessagesToolAware(messages, keepRecentPairs)
}

func trimTextToTokenBudget(text string, tokenBudget int) string {
	// Same heuristic as transform/ctx_compress.go (chars/3.5).
	maxChars := int(float64(tokenBudget) * 3.5)
	if len(text) <= maxChars {
		return text
	}
	// Keep head + tail so summarize model sees early intent and latest state.
	head := maxChars * 40 / 100
	tail := maxChars - head - len("\n...[truncated for compaction input]...\n")
	if tail < 0 {
		tail = 0
	}
	if tail > len(text) {
		tail = len(text)
	}
	if head > len(text) {
		head = len(text)
	}
	return text[:head] + "\n...[truncated for compaction input]...\n" + text[len(text)-tail:]
}

func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

type contextLengthRecoveryState struct {
	mechanicalAttempted bool
	memoraAttempted     bool
	llmAttempted        bool
	// Round 47 compression v7 T-NEW-1: track the strategy that actually
	// produced the rebuild so we can report it on the request_logs row.
	// Only the last successful tier wins (lower tiers short-circuit on
	// success), matching the v7 §3.4 "mechanical → memora → llm" cascade.
	lastStrategy string
	lastReason   string
	lastMeta     []byte // JSON-serialised compression_meta for request_logs
}

type ctxLenRecoveryAction int

const (
	ctxLenGiveUp ctxLenRecoveryAction = iota
	ctxLenRetry
)

func (e *Executor) handleContextLengthRecovery(
	ctx context.Context,
	params *ExecParams,
	targetCand provider.Candidate,
	sourceBody *[]byte,
	st *contextLengthRecoveryState,
	status int,
) ctxLenRecoveryAction {
	if params == nil || sourceBody == nil || st == nil {
		return ctxLenGiveUp
	}

	// Round 47 compression v7 T-NEW-1: capture bytes_before for compression_meta.
	// Set on the state struct so the successful tier can attach it to the
	// request_logs row.
	st.lastMeta = nil
	st.lastReason = "mode_2_on_4xx"
	st.lastStrategy = "noop"

	// Phase 1: mechanical trim. Only possible when we know the target
	// model's context window (needed to compute the soft limit). When
	// ContextWindow is nil, skip straight to the LLM-summary fallback —
	// summarization does not depend on the target window size.
	if targetCand.ContextWindow != nil && !st.mechanicalAttempted {
		st.mechanicalAttempted = true
		mechanicalFn := func(b []byte) []byte {
			if params.ClientProtocol == "anthropic-messages" {
				return transform.CompressAnthropicMessagesIfNeeded(b, *targetCand.ContextWindow)
			}
			return transform.CompressMessagesIfNeeded(b, *targetCand.ContextWindow)
		}
		trimmed := mechanicalFn(*sourceBody)
		if len(trimmed) < len(*sourceBody) {
			before := len(*sourceBody)
			*sourceBody = trimmed
			slog.Info("context_length 4xx → mechanical trim retry",
				"credential_id", targetCand.CredentialID,
				"model", targetCand.RawModel,
				"context_window", *targetCand.ContextWindow,
				"status", status,
				"source_bytes", before,
				"after_bytes", len(*sourceBody),
			)
			st.lastStrategy = "mechanical_trim"
			st.lastMeta = buildCompressionMeta(targetCand.ContextWindow, before, len(*sourceBody))
			return ctxLenRetry
		}
	}
	// Mark mechanical as attempted even when skipped (nil window) so the
	// state machine is consistent and we never revisit this phase.
	st.mechanicalAttempted = true

	// Memora L1 retrieval: ask Memora for the task's most relevant
	// session facts and rebuild the body around them. Best-effort —
	// any failure (timeout/empty/error) falls through to the LLM
	// summary path below.
	if !st.memoraAttempted && e.Memora != nil && !e.Memora.Disabled() {
		st.memoraAttempted = true
		if newBody, ok := e.tryMemoraCompression(ctx, params, *sourceBody); ok {
			before := len(*sourceBody)
			*sourceBody = newBody
			slog.Info("context_length 4xx → memora L1 rebuild retry",
				"credential_id", targetCand.CredentialID,
				"model", targetCand.RawModel,
				"status", status,
				"source_bytes", before,
				"after_bytes", len(*sourceBody),
			)
			st.lastStrategy = "memora_l1_inject"
			st.lastMeta = buildCompressionMeta(targetCand.ContextWindow, before, len(*sourceBody))
			return ctxLenRetry
		}
	}

	if !st.llmAttempted {
		st.llmAttempted = true
		newBody, ok := e.tryLLMContextCompaction(ctx, params, targetCand, *sourceBody)
		if ok {
			before := len(*sourceBody)
			*sourceBody = newBody
			slog.Info("context_length 4xx → llm summary retry",
				"credential_id", targetCand.CredentialID,
				"model", targetCand.RawModel,
				"context_window", cwLogVal(targetCand.ContextWindow),
				"status", status,
				"source_bytes", before,
				"after_bytes", len(*sourceBody),
			)
			st.lastStrategy = "llm_summary"
			st.lastMeta = buildCompressionMeta(targetCand.ContextWindow, before, len(*sourceBody))
			return ctxLenRetry
		}
	}

	return ctxLenGiveUp
}

// buildCompressionMeta serialises the v7 §3.2 compression_meta JSONB
// payload that relay/handler.go will write into request_logs.
//
// We keep this helper in routing/ (not compressor/) because the recovery
// flow runs inside routing/context_summarize.go which the T-NEW-2 refactor
// will eventually relocate. Until then, this small helper avoids pulling
// the compressor package into routing just for JSON marshalling.
func buildCompressionMeta(contextWindow *int, bytesBefore, bytesAfter int) []byte {
	tokensBefore := bytesBefore * 10 / 35 // chars/3.5 → ×10/35 ≈ tokens
	tokensAfter := bytesAfter * 10 / 35
	meta := map[string]any{
		"tokens_before": tokensBefore,
		"tokens_after":  tokensAfter,
		"bytes_before":  bytesBefore,
		"bytes_after":   bytesAfter,
		"reason_detail": "4xx context_length recovery",
	}
	if contextWindow != nil {
		meta["context_window_used"] = *contextWindow
	}
	out, _ := json.Marshal(meta)
	return out
}

// cwLogVal returns a log-safe representation of the context window pointer.
// Returns -1 when nil so logs clearly distinguish "unknown" from "0".
func cwLogVal(cw *int) int {
	if cw == nil {
		return -1
	}
	return *cw
}

// classifyContextLengthFromStatus helps tests; re-export pattern from errorsx.
func classifyContextLengthFromStatus(status int, body []byte) bool {
	return errorsx.IsContextLength(errorsx.ClassifyErrorWithBody(status, body))
}

// extractAPIKeyID derives a stable positive integer from the inbound
// API key. We don't have direct access to the api_keys.id (it's only
// resolved in auth/verifier.go), so we hash the Bearer token. The
// resulting user_id is namespaced under "k:" and never collides with
// real human users in Memora, so collisions across distinct keys
// only widen the namespace — they don't violate isolation.
func extractAPIKeyID(r *http.Request, ci identity.ClientIdentity) int {
	if r != nil {
		if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
			return stableHashKey(auth[7:])
		}
	}
	if ci.IdentityHash != "" {
		return stableHashKey(ci.IdentityHash)
	}
	return 0
}

// enqueueMemoraWrite persists the request conversation (and, when
// available, the non-stream assistant response) to Memora via the
// async sink. Best-effort fire-and-forget: if the sink is nil, the
// queue is full, or Memora is disabled, the call is a silent no-op.
//
// This is the write side of the Memora oracle: each successful
// request accumulates facts in L1 session memory so that a later
// context-overflow on the SAME (api_key_id, task_id) can retrieve
// them via tryMemoraCompression and rebuild a smaller body.
//
// We persist the request body's conversation turns (which contain
// the full history visible to the model) rather than individual
// messages, because Memora's /product/add re-derives facts from
// the conversation context. The optional respBody (non-stream only)
// is appended as the assistant turn so Memora sees the model's
// latest output too.
func (e *Executor) enqueueMemoraWrite(params *ExecParams, sourceBody, respBody []byte) {
	if e == nil || e.MemoraSink == nil || params == nil || len(sourceBody) == 0 {
		return
	}
	apiKeyID := extractAPIKeyID(params.R, params.ClientID)
	taskID := memora.TaskID(params.R, sourceBody, apiKeyID)
	if taskID == "" {
		return
	}
	// Round 47 compression v7 T13: tenant-namespaced user_id.
	userID := memora.UserID(params.TenantID, apiKeyID, taskID)
	if userID == "" {
		return
	}
	msgs := extractMemoraMessages(sourceBody, params.ClientProtocol, respBody)
	if len(msgs) == 0 {
		return
	}
	e.MemoraSink.Enqueue(memora.WriteOp{
		UserID:   userID,
		Messages: msgs,
		// Round 47 compression v7 T14: align with Memora MCP ingest_session
		// source enum so Memora can apply per-source TTL / dedup policy.
		Source: "gateway",
		Info: map[string]any{
			"task_id":      taskID,
			"api_key_id":   apiKeyID,
			"tenant_id":    params.TenantID,
			"client_model": params.ClientModel,
		},
	})
}

// extractMemoraMessages turns a wire body (+ optional response body)
// into memora.Message pairs for /product/add. We keep the extraction
// minimal: only role + plain-text content, no tool_call args — Memora
// cares about the semantic conversation, not the tool plumbing.
func extractMemoraMessages(body []byte, protocol string, respBody []byte) []memora.Message {
	text, err := extractConversationText(body, protocol)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil
	}
	// Split the extracted "[role]\n...\n\n" blocks back into messages.
	var out []memora.Message
	for _, block := range splitConversationBlocks(text) {
		if block.role == "" || block.text == "" {
			continue
		}
		out = append(out, memora.Message{Role: block.role, Content: block.text})
	}
	// Append the assistant response when we have it (non-stream path).
	if len(respBody) > 0 {
		if respText := extractAssistantReplyText(respBody, protocol); respText != "" {
			out = append(out, memora.Message{Role: "assistant", Content: respText})
		}
	}
	return out
}

type convBlock struct {
	role string
	text string
}

// splitConversationBlocks reverses the "[role]\ntext\n\n" format
// produced by extractOpenAIConversationText /
// extractAnthropicConversationText.
func splitConversationBlocks(text string) []convBlock {
	var out []convBlock
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		// A header line looks like "[role]".
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			role := line[1 : len(line)-1]
			i++
			var textLines []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "[") && strings.HasSuffix(lines[i], "]") {
					break
				}
				textLines = append(textLines, lines[i])
				i++
			}
			text := strings.TrimSpace(strings.Join(textLines, "\n"))
			if role != "" && text != "" {
				out = append(out, convBlock{role: role, text: text})
			}
		} else {
			i++
		}
	}
	return out
}

// extractAssistantReplyText pulls the assistant's text from a
// non-stream response body. Supports both OpenAI chat.completion
// and Anthropic Messages shapes.
func extractAssistantReplyText(body []byte, protocol string) string {
	if protocol == "anthropic-messages" {
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return ""
		}
		var b strings.Builder
		for _, block := range resp.Content {
			if block.Type == "text" && block.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(block.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	// OpenAI chat.completion
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content)
}
