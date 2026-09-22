package ir

import (
	"encoding/json"
	"testing"
)

// Wave 1 A2 回归钉桩：tool_use.input 必须始终是 JSON 对象。
// 旧实现把非法 JSON 以原始字符串直塞 input，Anthropic 系上游 400，
// 且被误归类为节点故障触发切换/冷却（2026-09-22 设计差距审计 A2）。

func anthropicToolUseInputTestReq(args string) *InternalRequest {
	tc := ToolCall{ID: "call_1", Type: "function"}
	tc.Function.Name = "get_weather"
	tc.Function.Arguments = args
	return &InternalRequest{
		Model:          "claude-sonnet-4",
		MaxTokens:      64,
		SourceProtocol: ProtocolOpenAIChat,
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{tc}},
		},
	}
}

func firstAnthropicToolUseInput(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal serialized body: %v", err)
	}
	for _, msg := range parsed.Messages {
		for _, block := range msg.Content {
			if block["type"] == "tool_use" {
				input, ok := block["input"].(map[string]any)
				if !ok {
					t.Fatalf("tool_use.input is not a JSON object: %v (%s)", block["input"], string(body))
				}
				return input
			}
		}
	}
	t.Fatalf("no tool_use block in serialized body: %s", string(body))
	return nil
}

func TestSerializeAnthropic_InvalidJSONArgsWrappedAsRaw(t *testing.T) {
	ResetAnomalyReporter()
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	req := anthropicToolUseInputTestReq(`not-valid-json{{{`)
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	input := firstAnthropicToolUseInput(t, body)
	raw, ok := input["raw"].(string)
	if !ok {
		t.Fatalf("input = %v, want {\"raw\": ...} wrap", input)
	}
	if raw != `not-valid-json{{{` {
		t.Errorf("raw = %q, want original arguments preserved", raw)
	}
	if len(input) != 1 {
		t.Errorf("input has extra keys: %v", input)
	}
}

func TestSerializeAnthropic_NonObjectJSONArgsWrapped(t *testing.T) {
	// 合法 JSON 但不是对象（数组/标量）同样会撞 Anthropic 的 object 约束。
	ResetAnomalyReporter()
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	for _, args := range []string{`[1,2,3]`, `"just a string"`, `42`, `null`} {
		body, err := SerializeAnthropic(anthropicToolUseInputTestReq(args))
		if err != nil {
			t.Fatalf("SerializeAnthropic(%s): %v", args, err)
		}
		input := firstAnthropicToolUseInput(t, body)
		if input["raw"] != args {
			t.Errorf("args=%s: input = %v, want raw wrap", args, input)
		}
	}
}

func TestSerializeAnthropic_ValidObjectArgsPassThrough(t *testing.T) {
	ResetAnomalyReporter()
	var events []AnomalyEvent
	prev := SetAnomalyReporter(func(ev AnomalyEvent) { events = append(events, ev) })
	defer SetAnomalyReporter(prev)

	body, err := SerializeAnthropic(anthropicToolUseInputTestReq(`{"city":"sf","unit":"c"}`))
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	input := firstAnthropicToolUseInput(t, body)
	if input["city"] != "sf" || input["unit"] != "c" {
		t.Errorf("input = %v, want exact passthrough", input)
	}
	if len(events) != 0 {
		t.Errorf("unexpected anomalies for valid object args: %v", events)
	}
}

func TestSerializeAnthropic_EmptyArgsEmitEmptyObject(t *testing.T) {
	ResetAnomalyReporter()
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	body, err := SerializeAnthropic(anthropicToolUseInputTestReq(""))
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	input := firstAnthropicToolUseInput(t, body)
	if len(input) != 0 {
		t.Errorf("input = %v, want empty object for empty arguments", input)
	}
}

func TestSerializeAnthropic_InvalidJSONArgsRecordFormatAnomaly(t *testing.T) {
	ResetAnomalyReporter()
	var events []AnomalyEvent
	prev := SetAnomalyReporter(func(ev AnomalyEvent) { events = append(events, ev) })
	defer SetAnomalyReporter(prev)

	if _, err := SerializeAnthropic(anthropicToolUseInputTestReq(`oops-not-json`)); err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("anomalies = %d, want 1 (dedup per request): %v", len(events), events)
	}
	ev := events[0]
	if ev.FieldPath != "messages[*].tool_use.input" {
		t.Errorf("field_path = %q", ev.FieldPath)
	}
	if ev.TargetProtocol != ProtocolAnthropicMessages {
		t.Errorf("target_protocol = %q", ev.TargetProtocol)
	}
	if ev.Metadata["anomaly"] != "format" {
		t.Errorf("metadata.anomaly = %v, want format", ev.Metadata["anomaly"])
	}
	if !ev.RawValueTruncated {
		t.Errorf("raw_value_truncated = false, want true")
	}
}
