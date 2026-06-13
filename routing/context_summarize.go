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
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
)

const (
	defaultCompactionMinWindow = 800_000
	defaultCompactionSummaryMaxTokens = 4096
	compactionSummaryPrefix            = "[Gateway compacted conversation summary — prior turns collapsed to fit context]\n"
)

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
		rel, err := e.Limiter.AcquireAll(ctx, cand.ProviderID, cand.CredentialID, params.ClientID.IdentityHash)
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
	llmAttempted        bool
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
	if targetCand.ContextWindow == nil || params == nil || sourceBody == nil || st == nil {
		return ctxLenGiveUp
	}
	mechanicalFn := func(b []byte) []byte {
		if params.ClientProtocol == "anthropic-messages" {
			return transform.CompressAnthropicMessagesIfNeeded(b, *targetCand.ContextWindow)
		}
		return transform.CompressMessagesIfNeeded(b, *targetCand.ContextWindow)
	}

	if !st.mechanicalAttempted {
		st.mechanicalAttempted = true
		trimmed := mechanicalFn(*sourceBody)
		if len(trimmed) < len(*sourceBody) {
			*sourceBody = trimmed
			slog.Info("context_length 4xx → mechanical trim retry",
				"credential_id", targetCand.CredentialID,
				"model", targetCand.RawModel,
				"context_window", *targetCand.ContextWindow,
				"status", status,
				"source_bytes", len(*sourceBody),
			)
			return ctxLenRetry
		}
	}

	if !st.llmAttempted {
		st.llmAttempted = true
		newBody, ok := e.tryLLMContextCompaction(ctx, params, targetCand, *sourceBody)
		if ok {
			*sourceBody = newBody
			slog.Info("context_length 4xx → llm summary retry",
				"credential_id", targetCand.CredentialID,
				"model", targetCand.RawModel,
				"context_window", *targetCand.ContextWindow,
				"status", status,
				"source_bytes", len(*sourceBody),
			)
			return ctxLenRetry
		}
	}

	return ctxLenGiveUp
}

// classifyContextLengthFromStatus helps tests; re-export pattern from errorsx.
func classifyContextLengthFromStatus(status int, body []byte) bool {
	return errorsx.IsContextLength(errorsx.ClassifyErrorWithBody(status, body))
}
