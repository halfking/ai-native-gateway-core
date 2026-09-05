// Package sessiondigest defines the durable, presentation-neutral turn digest contract.
package sessiondigest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// SchemaVersion identifies the durable digest envelope shape.
	SchemaVersion = 1
	// AlgorithmVersion identifies the deterministic projection algorithm.
	AlgorithmVersion = "deterministic-v1"
	// Source identifies the writer that created the envelope.
	Source = "session_v2_writer"
)

// Digest is the administrator-safe projection returned by turn APIs.
type Digest struct {
	UserInput       string     `json:"user_input"`
	AssistantOutput string     `json:"assistant_output"`
	Metrics         Metrics    `json:"metrics"`
	Events          []Event    `json:"events,omitempty"`
	ToolUsage       *ToolUsage `json:"tool_usage,omitempty"`
}

// Metrics captures token, cost, latency, cache, and compression summaries.
type Metrics struct {
	TokensUsed      int      `json:"tokens_used"`
	Cost            float64  `json:"cost"`
	LatencyMs       int      `json:"latency_ms"`
	CacheHitRate    *float64 `json:"cache_hit_rate,omitempty"`
	CompressionRate *float64 `json:"compression_rate,omitempty"`
}

// Event describes a compact operational event associated with a turn.
type Event struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// ToolUsage summarizes invoked tools without retaining their arguments.
type ToolUsage struct {
	ToolCallCount int      `json:"tool_call_count"`
	ToolsUsed     []string `json:"tools_used"`
}

// Envelope is the administrator-safe JSONB document stored on a turn.
type Envelope struct {
	SchemaVersion    int       `json:"schema_version"`
	AlgorithmVersion string    `json:"algorithm_version"`
	GeneratedAt      time.Time `json:"generated_at"`
	Source           string    `json:"source"`
	Payload          Digest    `json:"payload"`
}

// Build creates a deterministic digest from JSON-compatible message bodies.
func Build(request, response any, meta, governance map[string]any, generatedAt time.Time) *Envelope {
	user := roleText(request, "user", true)
	assistant := roleText(response, "assistant", false)
	if assistant == "" {
		assistant = roleText(request, "assistant", false)
	}

	payload := Digest{
		UserInput:       compact(user),
		AssistantOutput: compact(assistant),
		Metrics:         buildMetrics(meta, governance),
		Events:          buildEvents(meta, governance),
	}
	if names := toolNames(request, response); len(names) != 0 {
		payload.ToolUsage = &ToolUsage{ToolCallCount: len(names), ToolsUsed: unique(names)}
	}
	if payload.UserInput == "" && payload.AssistantOutput == "" && len(payload.Events) == 0 && payload.ToolUsage == nil && !hasMetrics(meta) {
		return nil
	}
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	return &Envelope{SchemaVersion: SchemaVersion, AlgorithmVersion: AlgorithmVersion, GeneratedAt: generatedAt.UTC(), Source: Source, Payload: payload}
}

func Marshal(envelope *Envelope) ([]byte, error) {
	if envelope == nil {
		return nil, nil
	}
	return json.Marshal(envelope)
}

// Unmarshal rejects malformed and unsupported future documents so readers can fall back.
func Unmarshal(raw []byte) (*Envelope, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.AlgorithmVersion != AlgorithmVersion {
		return nil, fmt.Errorf("unsupported digest version")
	}
	return &envelope, nil
}

func messageValues(value any) []map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	var messages []map[string]any
	var walk func(any)
	walk = func(current any) {
		switch value := current.(type) {
		case []any:
			for _, item := range value {
				walk(item)
			}
		case map[string]any:
			if _, ok := value["role"]; ok {
				messages = append(messages, value)
				return
			}
			for _, key := range []string{"messages", "choices", "message", "output", "content"} {
				if child, ok := value[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(decoded)
	return messages
}

func roleText(body any, role string, last bool) string {
	var parts []string
	for _, message := range messageValues(body) {
		if !strings.EqualFold(stringValue(message["role"]), role) {
			continue
		}
		text := contentText(message["content"])
		if text == "" {
			text = stringValue(message["text"])
		}
		if text == "" {
			continue
		}
		if last {
			parts = []string{text}
		} else {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func contentText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, item := range value {
			if text := contentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		typ := strings.ToLower(stringValue(value["type"]))
		switch typ {
		case "text", "input_text", "output_text":
			if text := stringValue(value["text"]); text != "" {
				return text
			}
			return stringValue(value["content"])
		case "image", "image_url", "input_image", "output_image":
			return "[附图×1]"
		case "file", "document", "attachment", "input_file", "output_file":
			return "[附件×1]"
		}
	}
	return ""
}

func toolNames(bodies ...any) []string {
	var names []string
	for _, body := range bodies {
		for _, message := range messageValues(body) {
			for _, key := range []string{"tool_calls", "tool_use", "tool_uses"} {
				if value, ok := message[key]; ok {
					collectToolNames(value, &names)
				}
			}
		}
	}
	return names
}

func collectToolNames(value any, names *[]string) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			collectToolNames(item, names)
		}
	case map[string]any:
		name := stringValue(value["name"])
		if name == "" {
			if function, ok := value["function"].(map[string]any); ok {
				name = stringValue(function["name"])
			}
		}
		if name != "" {
			*names = append(*names, name)
			return
		}
		for _, key := range []string{"function", "tool_calls", "tool_use", "tool_uses"} {
			if child, ok := value[key]; ok {
				collectToolNames(child, names)
			}
		}
	}
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func buildMetrics(meta, governance map[string]any) Metrics {
	result := Metrics{TokensUsed: number(first(meta, "tokens_used", "total_tokens")), Cost: real(first(meta, "cost_usd", "cost")), LatencyMs: number(meta["latency_ms"])}
	if result.TokensUsed == 0 {
		result.TokensUsed = number(meta["prompt_tokens"]) + number(meta["completion_tokens"])
	}
	if value, ok := meta["cache_hit_rate"]; ok {
		rate := real(value)
		result.CacheHitRate = &rate
	}
	if value, ok := meta["compression_rate"]; ok {
		rate := real(value)
		result.CompressionRate = &rate
	} else if saved := number(first(governance, "compression_tokens_saved")); saved > 0 && result.TokensUsed > 0 {
		rate := float64(saved) / float64(saved+result.TokensUsed)
		result.CompressionRate = &rate
	}
	return result
}

func buildEvents(meta, governance map[string]any) []Event {
	var events []Event
	add := func(typ, category, message string) {
		if strings.TrimSpace(message) != "" {
			events = append(events, Event{Type: typ, Category: category, Message: message})
		}
	}
	if code := number(meta["status_code"]); code >= 400 {
		add("error", "request_failure", fmt.Sprintf("请求失败（HTTP %d）", code))
	}
	if message := stringValue(first(meta, "error_kind", "error", "failure_reason")); message != "" {
		add("error", "request_failure", message)
	}
	if success, ok := meta["success"].(bool); ok && !success && len(events) == 0 {
		add("error", "request_failure", "请求未成功")
	}
	if latency := number(meta["latency_ms"]); latency > 5000 {
		add("warning", "performance", fmt.Sprintf("响应延迟较高：%.1fs", float64(latency)/1000))
	}
	for _, item := range []struct{ key, category string }{{"injection_verdict", "injection"}, {"output_verdict", "output_filter"}} {
		value := stringValue(governance[item.key])
		if value != "" && !strings.EqualFold(value, "pass") && !strings.EqualFold(value, "allow") && !strings.EqualFold(value, "skip") {
			add("warning", item.category, item.key+": "+value)
		}
	}
	if applied, ok := governance["compression_applied"].(bool); ok && applied {
		add("info", "compression", "已应用会话压缩")
	}
	return events
}

func hasMetrics(meta map[string]any) bool {
	for _, key := range []string{"tokens_used", "total_tokens", "prompt_tokens", "completion_tokens", "cost_usd", "cost", "latency_ms", "cache_read_tokens", "compression_tokens_saved"} {
		if _, ok := meta[key]; ok {
			return true
		}
	}
	return false
}

func first(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func stringValue(value any) string { text, _ := value.(string); return strings.TrimSpace(text) }

func number(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	}
	return 0
}

func real(value any) float64 {
	switch value := value.(type) {
	case float32:
		return float64(value)
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	}
	return 0
}

func compact(value string) string {
	const maxRunes = 260
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}
