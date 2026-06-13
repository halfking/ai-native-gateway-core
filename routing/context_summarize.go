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

func (e *Executor) pickCompactionCandidate(ctx context.Context, profile string) (*provider.Candidate, error) {
	if e.Provider == nil || !e.Provider.Enabled() {
		return nil, fmt.Errorf("provider client unavailable")
	}
	minWindow := defaultCompactionMinWindow
	for _, model := range compactionModelsFromEnv() {
		cands, _, err := e.Provider.GetCandidates(ctx, model, profile)
		if err != nil {
			slog.Debug("compaction: resolve candidates failed", "model", model, "error", err)
			continue
		}
		for i := range cands {
			c := &cands[i]
			if !c.IsAvailable() {
				continue
			}
			if c.ContextWindow != nil && *c.ContextWindow < minWindow {
				continue
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("no compaction model available")
}

// tryLLMContextCompaction summarizes oversized history using a large-context
// model, then rebuilds the client wire body with [summary + recent tail].
// Returns the new source body and true when compaction succeeded.
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

	compactCand, err := e.pickCompactionCandidate(ctx, profile)
	if err != nil {
		slog.Warn("compaction: no candidate", "target_model", targetCand.RawModel, "error", err)
		return sourceBody, false
	}

	conversation, err := extractConversationText(sourceBody, params.ClientProtocol)
	if err != nil || strings.TrimSpace(conversation) == "" {
		slog.Warn("compaction: extract conversation failed", "error", err)
		return sourceBody, false
	}

	// Keep summarize input under ~900k tokens (heuristic) so 1M models fit.
	conversation = trimTextToTokenBudget(conversation, 900_000)

	summary, err := e.invokeCompactionSummarize(ctx, *compactCand, conversation)
	if err != nil {
		slog.Warn("compaction: summarize call failed",
			"compact_model", compactCand.RawModel,
			"target_model", targetCand.RawModel,
			"error", err,
		)
		return sourceBody, false
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
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
		"compact_model", compactCand.RawModel,
		"compact_credential_id", compactCand.CredentialID,
		"target_model", targetCand.RawModel,
		"target_credential_id", targetCand.CredentialID,
		"source_bytes_before", len(sourceBody),
		"source_bytes_after", len(rebuilt),
	)
	return rebuilt, true
}

func (e *Executor) invokeCompactionSummarize(ctx context.Context, cand provider.Candidate, conversation string) (string, error) {
	userContent := "Summarize the following conversation history:\n\n" + conversation
	timeout := 120 * time.Second
	if e.UpstreamTimeout > 0 {
		timeout = e.UpstreamTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if cand.Protocol == "anthropic-messages" {
		return e.invokeAnthropicSummarize(ctx, cand, userContent)
	}
	return e.invokeOpenAISummarize(ctx, cand, userContent)
}

func (e *Executor) invokeOpenAISummarize(ctx context.Context, cand provider.Candidate, userContent string) (string, error) {
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

	url := upstreamurl.ChatCompletionsURL(cand.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cand.APIKey)

	resp, err := e.doCompactionHTTP(req)
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

func (e *Executor) invokeAnthropicSummarize(ctx context.Context, cand provider.Candidate, userContent string) (string, error) {
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

	url := upstreamurl.MessagesURL(cand.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cand.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := e.doCompactionHTTP(req)
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
		role, text := messageRoleAndText(raw)
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
		role, text := messageRoleAndText(raw)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	return b.String(), nil
}

func messageRoleAndText(raw json.RawMessage) (role, text string) {
	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", ""
	}
	return probe.Role, rawJSONTextContent(probe.Content)
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
	tail := tailMessages(rest, keepRecentPairs)
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
	tail := tailMessages(req.Messages, keepRecentPairs)
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

// tailMessages keeps the last keepRecentPairs user/assistant pairs (up to 2*keepRecentPairs msgs).
func tailMessages(messages []json.RawMessage, keepRecentPairs int) []json.RawMessage {
	if keepRecentPairs <= 0 || len(messages) == 0 {
		return nil
	}
	maxKeep := keepRecentPairs * 2
	if len(messages) <= maxKeep {
		return append([]json.RawMessage(nil), messages...)
	}
	return append([]json.RawMessage(nil), messages[len(messages)-maxKeep:]...)
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

// applyMechanicalThenLLMCompaction runs trim then optional LLM summarize on sourceBody.
func (e *Executor) applyMechanicalThenLLMCompaction(
	ctx context.Context,
	params *ExecParams,
	targetCand provider.Candidate,
	sourceBody []byte,
	mechanicalTrim func([]byte) []byte,
) ([]byte, bool) {
	trimmed := mechanicalTrim(sourceBody)
	if len(trimmed) < len(sourceBody) {
		return trimmed, true
	}
	newBody, ok := e.tryLLMContextCompaction(ctx, params, targetCand, sourceBody)
	if ok {
		return newBody, true
	}
	return sourceBody, false
}

// classifyContextLengthFromStatus helps tests; re-export pattern from errorsx.
func classifyContextLengthFromStatus(status int, body []byte) bool {
	return errorsx.IsContextLength(errorsx.ClassifyErrorWithBody(status, body))
}
