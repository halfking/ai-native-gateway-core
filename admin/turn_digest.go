package admin

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TurnDigest is the human-readable projection of one conversation turn.
// It is deliberately additive: request/response bodies remain available for
// privileged inspection when a reader needs the exact provider payload.
type TurnDigest struct {
	UserInput       string            `json:"user_input"`
	AssistantOutput string            `json:"assistant_output"`
	Metrics         TurnMetrics       `json:"metrics"`
	Events          []TurnEvent       `json:"events,omitempty"`
	ToolUsage       *ToolUsageSummary `json:"tool_usage,omitempty"`
}

type TurnMetrics struct {
	TokensUsed      int      `json:"tokens_used"`
	Cost            float64  `json:"cost"`
	LatencyMs       int      `json:"latency_ms"`
	CacheHitRate    *float64 `json:"cache_hit_rate,omitempty"`
	CompressionRate *float64 `json:"compression_rate,omitempty"`
}

type TurnEvent struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

type ToolUsageSummary struct {
	ToolCallCount int      `json:"tool_call_count"`
	ToolsUsed     []string `json:"tools_used"`
}

type digestContent struct {
	text   string
	images int
	files  int
	tools  []toolRef
}

type toolRef struct{ id, name, signature string }

// buildTurnDigest extracts only human-readable text/content fields. It never
// falls back to serialising an arbitrary object, which keeps JSON formatting,
// provider metadata and tool arguments out of the default presentation.
func buildTurnDigest(requestDelta, responseDelta any, meta, governance map[string]any) *TurnDigest {
	user, userContent := extractUserInputContent(requestDelta)
	assistant := extractAssistantContent(responseDelta)
	if assistant.text == "" {
		assistant = extractAssistantContent(requestDelta)
	}

	d := &TurnDigest{
		UserInput:       addMediaMarkers(user, userContent),
		AssistantOutput: addMediaMarkers(assistant.text, assistant),
		Metrics:         extractMetrics(meta, governance),
		Events:          extractEvents(meta, governance),
	}
	if tools := collectTools(requestDelta, responseDelta); len(tools) > 0 {
		d.ToolUsage = toolSummary(tools)
	}
	if d.UserInput == "" && d.AssistantOutput == "" && len(d.Events) == 0 && d.ToolUsage == nil && !hasMetricData(meta) {
		return nil
	}
	return d
}

func extractUserInputDigest(body any) string {
	text, content := extractUserInputContent(body)
	return addMediaMarkers(text, content)
}

func extractUserInputContent(body any) (string, digestContent) {
	var last digestContent
	for _, msg := range messageValues(body) {
		if strings.EqualFold(stringValue(msg["role"]), "user") {
			last = digestExtractMessageContent(msg)
		}
	}
	return summarizeDigestText(last.text), last
}

func extractAssistantOutputDigest(body any) string {
	content := extractAssistantContent(body)
	return addMediaMarkers(content.text, content)
}

func extractAssistantContent(body any) digestContent {
	var out digestContent
	for _, msg := range messageValues(body) {
		if !strings.EqualFold(stringValue(msg["role"]), "assistant") {
			continue
		}
		part := digestExtractMessageContent(msg)
		out.text = joinDigestText(out.text, part.text)
		out.images += part.images
		out.files += part.files
		out.tools = append(out.tools, part.tools...)
	}
	out.text = summarizeDigestText(out.text)
	return out
}

// messageValues accepts message arrays and common OpenAI response envelopes.
func messageValues(body any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if _, ok := x["role"]; ok {
				out = append(out, x)
				return
			}
			for _, key := range []string{"messages", "choices", "message", "output", "content"} {
				if child, ok := x[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(body)
	return out
}

func digestExtractMessageContent(msg map[string]any) digestContent {
	var out digestContent
	if v, ok := msg["content"]; ok {
		out = extractContentValue(v, out)
	}
	if out.text == "" {
		out.text = stringValue(msg["text"])
	}
	for _, key := range []string{"tool_calls", "tool_use", "tool_uses"} {
		if v, ok := msg[key]; ok {
			out.tools = append(out.tools, extractToolRefs(v)...)
		}
	}
	return out
}

func extractContentValue(value any, out digestContent) digestContent {
	switch x := value.(type) {
	case string:
		out.text = joinDigestText(out.text, x)
	case []any:
		for _, item := range x {
			out = extractContentValue(item, out)
		}
	case map[string]any:
		typ := strings.ToLower(stringValue(x["type"]))
		switch typ {
		case "text", "input_text", "output_text":
			text := stringValue(x["text"])
			if text == "" {
				text = stringValue(x["content"])
			}
			out.text = joinDigestText(out.text, text)
		case "image", "image_url", "input_image", "output_image":
			out.images++
		case "audio", "input_audio", "output_audio", "video", "input_video", "output_video", "document", "file", "input_file", "output_file", "attachment":
			out.files++
		case "tool_use", "tool_call", "function_call":
			out.tools = append(out.tools, extractToolRefs(x)...)
		case "tool_result", "function_call_output":
			// Tool results are operational metadata, not user-facing prose.
		}
		if typ == "" {
			if text := stringValue(x["text"]); text != "" {
				out.text = joinDigestText(out.text, text)
			}
		}
	}
	return out
}

func extractToolRefs(value any) []toolRef {
	var refs []toolRef
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			name := stringValue(x["name"])
			if fn, ok := x["function"].(map[string]any); ok && name == "" {
				name = stringValue(fn["name"])
			}
			id := stringValue(x["id"])
			if id == "" {
				id = stringValue(x["tool_call_id"])
			}
			if name != "" || id != "" {
				b, _ := json.Marshal(map[string]any{"name": name, "id": id, "function": x["function"]})
				refs = append(refs, toolRef{id: id, name: name, signature: string(b)})
				return
			}
			if fn, ok := x["function"]; ok {
				walk(fn)
			}
		}
	}
	walk(value)
	return refs
}

func collectTools(bodies ...any) []toolRef {
	var all []toolRef
	for _, body := range bodies {
		for _, msg := range messageValues(body) {
			all = append(all, digestExtractMessageContent(msg).tools...)
		}
	}
	seen := map[string]bool{}
	out := make([]toolRef, 0, len(all))
	for i, tool := range all {
		key := tool.id
		if key == "" {
			key = tool.signature + "#" + strconv.Itoa(i)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tool)
	}
	return out
}

func toolSummary(tools []toolRef) *ToolUsageSummary {
	names := make([]string, 0, len(tools))
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.name != "" && !seen[tool.name] {
			seen[tool.name] = true
			names = append(names, tool.name)
		}
	}
	return &ToolUsageSummary{ToolCallCount: len(tools), ToolsUsed: names}
}

func extractToolUsage(body any) *ToolUsageSummary {
	return toolSummaryOrNil(collectTools(body))
}
func toolSummaryOrNil(tools []toolRef) *ToolUsageSummary {
	if len(tools) == 0 {
		return nil
	}
	return toolSummary(tools)
}

func extractMetrics(meta, governance map[string]any) TurnMetrics {
	cost := floatValue(firstValue(meta, "cost_usd", "cost"))
	m := TurnMetrics{
		TokensUsed: intValue(firstValue(meta, "tokens_used", "total_tokens")),
		Cost:       cost,
		LatencyMs:  intValue(meta["latency_ms"]),
	}
	// Only fall back to prompt+completion when callers haven't already supplied
	// tokens_used/total_tokens. The list endpoint always injects prompt_tokens
	// + completion_tokens, so this branch is skipped there to avoid double
	// counting.
	if m.TokensUsed == 0 {
		m.TokensUsed = intValue(meta["prompt_tokens"]) + intValue(meta["completion_tokens"])
	}
	if v, ok := meta["cache_hit_rate"]; ok {
		m.CacheHitRate = clampPtr(floatValue(v))
	} else if hasKey(meta, "cache_read_tokens") {
		// Cache hit rate = cache_read / (cache_read + uncached_prompt).
		// We approximate uncached_prompt with prompt_tokens - cache_read when
		// cache_write_tokens isn't available; otherwise (cache_read /
		// (cache_read + cache_write)) using the more conservative denominator.
		cacheRead := intValue(meta["cache_read_tokens"])
		cacheWrite := intValue(meta["cache_write_tokens"])
		prompt := intValue(meta["prompt_tokens"])
		var denom int
		if cacheWrite > 0 {
			denom = cacheRead + cacheWrite
		} else if prompt > cacheRead {
			denom = prompt
		} else {
			denom = cacheRead
		}
		if denom > 0 {
			m.CacheHitRate = clampPtr(float64(cacheRead) / float64(denom))
		}
	}
	if v, ok := meta["compression_rate"]; ok {
		m.CompressionRate = clampPtr(floatValue(v))
	} else if saved := intValue(firstValue(governance, "compression_tokens_saved")); saved > 0 && m.TokensUsed > 0 {
		m.CompressionRate = clampPtr(float64(saved) / float64(saved+m.TokensUsed))
	}
	return m
}

func extractEvents(meta, governance map[string]any) []TurnEvent {
	var events []TurnEvent
	add := func(typ, cat, msg string) {
		if strings.TrimSpace(msg) != "" {
			events = append(events, TurnEvent{Type: typ, Category: cat, Message: msg})
		}
	}
	if code := intValue(meta["status_code"]); code >= 400 {
		add("error", "request_failure", fmt.Sprintf("请求失败（HTTP %d）", code))
	}
	if err := stringValue(firstValue(meta, "error_kind", "error", "failure_reason")); err != "" {
		add("error", "request_failure", err)
	}
	if ok, exists := boolValue(meta["success"]); exists && !ok && len(events) == 0 {
		add("error", "request_failure", "请求未成功")
	}
	if latency := intValue(meta["latency_ms"]); latency > 5000 {
		add("warning", "performance", fmt.Sprintf("响应延迟较高：%.1fs", float64(latency)/1000))
	}
	for _, spec := range []struct{ key, category string }{{"injection_verdict", "injection"}, {"output_verdict", "output_filter"}} {
		if v := stringValue(governance[spec.key]); v != "" && !strings.EqualFold(v, "pass") && !strings.EqualFold(v, "allow") && !strings.EqualFold(v, "skip") {
			add(verdictType(v), spec.category, spec.key+": "+v)
		}
	}
	if applied, ok := boolValue(governance["compression_applied"]); ok && applied {
		add("info", "compression", "已应用会话压缩")
	}
	return events
}

func verdictType(v string) string {
	lower := strings.ToLower(v)
	if strings.Contains(lower, "block") || strings.Contains(lower, "deny") || strings.Contains(lower, "fail") {
		return "error"
	}
	return "warning"
}
func hasMetricData(m map[string]any) bool {
	for _, k := range []string{"tokens_used", "total_tokens", "prompt_tokens", "completion_tokens", "cost_usd", "cost", "latency_ms", "cache_read_tokens", "compression_tokens_saved"} {
		if hasKey(m, k) {
			return true
		}
	}
	return false
}
func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }
func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}
func stringValue(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}
func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		n, _ := x.Float64()
		return n
	}
	return 0
}
func boolValue(v any) (bool, bool) { b, ok := v.(bool); return b, ok }
func clampPtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return &v
}
func joinDigestText(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}
func addMediaMarkers(text string, c digestContent) string {
	text = strings.TrimSpace(text)
	if c.images > 0 {
		text = joinDigestText(text, fmt.Sprintf("[附图×%d]", c.images))
	}
	if c.files > 0 {
		text = joinDigestText(text, fmt.Sprintf("[附件×%d]", c.files))
	}
	return text
}

// summarizeDigestText deliberately uses a deterministic rule rather than an
// external LLM: at >100 bytes keep the opening sentence and sentences with
// explicit conclusion/action markers; if there are no sentence boundaries,
// keep both the beginning and the end so the result remains representative.
//
// All truncation caps are rune-based: CJK-heavy turns routinely exceed the
// byte caps with 3-byte UTF-8 runes, and a byte cut would either produce an
// invalid UTF-8 string (rejected by JSON encoders downstream) or, after the
// safePrefix repair loop, a result materially shorter than the cap. The
// persisted digest (sessiondigest.compact) uses the same 260-rune ceiling, so
// the fallback projection and the persisted envelope agree on length.
func summarizeDigestText(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len([]byte(s)) <= 100 {
		return s
	}
	parts := splitDigestSentences(s)
	selected := make([]string, 0, 3)
	if len(parts) > 0 {
		selected = append(selected, parts[0])
	}
	for _, p := range parts[1:] {
		if containsKeyPoint(p) {
			selected = append(selected, p)
		}
	}
	if len(selected) == 1 && len(parts) > 1 {
		selected = append(selected, parts[1])
	}
	if len(selected) > 0 {
		result := strings.Join(selected, " … ")
		if utf8.RuneCountInString(result) <= digestMaxRunes {
			return result
		}
		return truncateRunes(result, digestMaxRunes-digestEllipsisRunes) + "…"
	}
	return truncateRunes(s, digestHeadRunes) + " … " + truncateTailRunes(s, digestTailRunes)
}

const (
	digestMaxRunes       = 260 // overall ceiling, matches sessiondigest.compact
	digestEllipsisRunes  = 1
	digestHeadRunes      = 110
	digestTailRunes      = 90
	digestNoSentenceHead = 240 // head budget when only the prefix fits
)

// truncateRunes keeps the first max runes of s. Unlike the previous
// byte-then-repair loop (safePrefix), it can never emit invalid UTF-8 and
// never silently under-delivers on multi-byte content.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// truncateTailRunes keeps the last max runes of s (rune-safe mirror of
// truncateRunes for the tail-preserving branch).
func truncateTailRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[len(runes)-max:])
}
func splitDigestSentences(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if strings.ContainsRune("。！？!?；;\n", r) {
			end := i + utf8.RuneLen(r)
			if p := strings.TrimSpace(s[start:end]); p != "" {
				out = append(out, p)
			}
			start = end
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		out = append(out, p)
	}
	return out
}
func containsKeyPoint(s string) bool {
	lower := strings.ToLower(s)
	for _, k := range []string{"关键", "结论", "结果", "需要", "必须", "important", "result", "therefore", "error", "失败", "成功"} {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}
// safePrefix / safeSuffix were removed when summarizeDigestText switched to
// rune-based truncation (truncateRunes / truncateTailRunes). The byte-then-
// repair approach could emit invalid UTF-8 or under-deliver on CJK content.

func deduplicate(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
