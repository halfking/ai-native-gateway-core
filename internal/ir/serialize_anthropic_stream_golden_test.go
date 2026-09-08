package ir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Audit-r2 Track A#2 (2026-09-08): golden/snapshot coverage for the Anthropic
// stream serializer (SerializeAnthropic). Each scenario feeds a realistic
// upstream SSE sequence through the IR parsers and concatenates the
// SerializeAnthropic output, so the snapshots pin:
//   - usage split across message_start (input/cache) and message_delta
//     (output_tokens) — no mutual-exclusion drop of CompletionTokens;
//   - stop_reason (and stop_sequence) surfacing on termination frames;
//   - lossless Anthropic→Anthropic passthrough of native stop reasons.
//
// Regenerate snapshots with:
//
//	UPDATE_ANTHROPIC_STREAM_GOLDEN=1 go test ./internal/ir/ -run TestSerializeAnthropicStreamGolden
const anthropicGoldenDir = "testdata" + string(filepath.Separator) + "anthropic_stream_golden"

// parseAndSerializeAnthropic converts an Anthropic SSE event sequence
// ("event: X\ndata: {...}" pairs) through the IR and back to Anthropic SSE.
func parseAndSerializeAnthropic(t *testing.T, sse string) string {
	t.Helper()
	var out strings.Builder
	for _, event := range strings.Split(sse, "\n\n") {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		eventType, payload := splitAnthropicSSEEvent(t, event)
		chunk, err := ParseAnthropicStreamEvent(eventType, []byte(payload))
		if err != nil {
			t.Fatalf("ParseAnthropicStreamEvent(%s): %v", eventType, err)
		}
		out.WriteString(chunk.SerializeAnthropic("msg_golden", "claude-golden"))
	}
	return out.String()
}

// parseAndSerializeOpenAIToAnthropic converts an OpenAI SSE data-line sequence
// through the IR into Anthropic SSE.
func parseAndSerializeOpenAIToAnthropic(t *testing.T, sse string) string {
	t.Helper()
	var out strings.Builder
	for _, line := range strings.Split(sse, "\n\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		chunk, err := ParseOpenAIStreamChunk(line)
		if err != nil {
			t.Fatalf("ParseOpenAIStreamChunk(%s): %v", line, err)
		}
		out.WriteString(chunk.SerializeAnthropic("msg_golden", "claude-golden"))
	}
	return out.String()
}

func splitAnthropicSSEEvent(t *testing.T, event string) (eventType, payload string) {
	t.Helper()
	for _, line := range strings.Split(event, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload = strings.TrimPrefix(line, "data: ")
		}
	}
	if eventType == "" || payload == "" {
		t.Fatalf("malformed Anthropic SSE event: %q", event)
	}
	return eventType, payload
}

func compareAnthropicStreamGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(anthropicGoldenDir, name+".sse")
	if os.Getenv("UPDATE_ANTHROPIC_STREAM_GOLDEN") == "1" {
		if err := os.MkdirAll(anthropicGoldenDir, 0o755); err != nil {
			t.Fatalf("mkdir testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with UPDATE_ANTHROPIC_STREAM_GOLDEN=1): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", path, string(want), got)
	}
}

// TestSerializeAnthropicStreamGolden runs the snapshot scenarios. Each case
// also carries targeted assertions so a regenerated-but-wrong snapshot cannot
// silently pass (the golden file alone would bless any output).
func TestSerializeAnthropicStreamGolden(t *testing.T) {
	cases := []struct {
		name       string
		upstream   string // "anthropic" | "openai"
		sse        string
		assertions func(t *testing.T, got string)
	}{
		{
			// 场景 1：纯文本流（Anthropic→Anthropic 直通）。
			name:     "text_stream",
			upstream: "anthropic",
			sse: `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","model":"claude-sonnet-4","role":"assistant","usage":{"input_tokens":12,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "text_stream",
					"event: message_start", `"input_tokens":12`,
					"event: content_block_delta", `"text":"Hel"`, `"text":"lo"`,
					"event: message_delta", `"stop_reason":"end_turn"`, `"output_tokens":4`,
					"event: message_stop")
			},
		},
		{
			// 场景 2：tool_use 流（content_block_start + input_json_delta）。
			name:     "tool_use_stream",
			upstream: "anthropic",
			sse: `event: message_start
data: {"type":"message_start","message":{"id":"msg_02","model":"claude-sonnet-4","role":"assistant","usage":{"input_tokens":57,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"loc"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ation\":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":88}}

event: message_stop
data: {"type":"message_stop"}
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "tool_use_stream",
					"event: content_block_start", `"name":"get_weather"`, `"id":"toolu_01"`,
					"event: content_block_delta", `"partial_json":"{\"loc"`,
					`"stop_reason":"tool_use"`, `"output_tokens":88`)
			},
		},
		{
			// 场景 3：带 usage 的正常终止——input/output/cache 三类字段齐全。
			name:     "usage_normal_termination",
			upstream: "anthropic",
			sse: `event: message_start
data: {"type":"message_start","message":{"id":"msg_03","model":"claude-sonnet-4","role":"assistant","usage":{"input_tokens":120,"output_tokens":0,"cache_creation_input_tokens":45,"cache_read_input_tokens":30}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Answer"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":256}}

event: message_stop
data: {"type":"message_stop"}
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "usage_normal_termination",
					`"input_tokens":120`,
					`"cache_creation_input_tokens":45`,
					`"cache_read_input_tokens":30`,
					`"stop_reason":"max_tokens"`,
					`"output_tokens":256`)
			},
		},
		{
			// 场景 4：OpenAI 上游单帧合并 usage（prompt+completion 同帧）——
			// 审计缺陷 #1 的直接回归：修复前走 message_start 分支，completion
			// 被互斥分支吞掉；修复后 message_start 与 message_delta 同发。
			// 审计缺陷 #2 的回归：finish_reason=stop 必须透出 stop_reason。
			name:     "openai_combined_usage_termination",
			upstream: "openai",
			sse: `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1500,"completion_tokens":50,"total_tokens":1550,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "openai_combined_usage_termination",
					"event: message_start", `"input_tokens":1500`,
					`"cache_read_input_tokens":100`,
					"event: message_delta", `"stop_reason":"end_turn"`, `"output_tokens":50`,
					"event: message_stop")
			},
		},
		{
			// 场景 5：stop_reason 多形态终止——OpenAI 归一化回映射
			// （tool_calls→tool_use、length→max_tokens）与 Anthropic 原生
			// stop_sequence 透传（含 stop_sequence 值本身）。
			name:     "stop_reason_terminations",
			upstream: "openai",
			sse: `data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"calling"}}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-3","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "stop_reason_terminations(openai)",
					`"stop_reason":"tool_use"`,
					`"stop_reason":"max_tokens"`)
			},
		},
		{
			// 场景 5b：Anthropic 原生 stop_sequence 直通（output_tokens=0 的
			// message_delta 也必须透出，修复前整帧被丢）。
			name:     "stop_reason_terminations_native_stop_sequence",
			upstream: "anthropic",
			sse: `event: message_start
data: {"type":"message_start","message":{"id":"msg_04","model":"claude-sonnet-4","role":"assistant","usage":{"input_tokens":9,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"\n\nNEXT"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}
`,
			assertions: func(t *testing.T, got string) {
				assertContainsAll(t, got, "stop_reason_terminations(native)",
					`"stop_reason":"stop_sequence"`,
					`"stop_sequence":"\n\nNEXT"`,
					`"output_tokens":7`)
			},
		},
		{
			// 场景 6：多帧 usage 累计——OpenAI 上游中段先发一帧仅 prompt 的
			// usage，末段再发完整 usage；input 不被覆盖，最终 output_tokens
			// 落在 message_delta，cache 保留在 message_start。
			name:     "usage_accumulation_multi_frame",
			upstream: "openai",
			sse: `data: {"id":"chatcmpl-4","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"a"}}]}

data: {"id":"chatcmpl-4","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{}}],"usage":{"prompt_tokens":100,"completion_tokens":0,"total_tokens":100}}

data: {"id":"chatcmpl-4","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"b"}}]}

data: {"id":"chatcmpl-4","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}

data: [DONE]
`,
			assertions: func(t *testing.T, got string) {
				// 中段帧：input 先行落地（不被后续帧覆盖丢失）
				assertContainsAll(t, got, "usage_accumulation_multi_frame",
					`"input_tokens":100`,
					// 末段帧：input 与 output 双帧齐全，output 不再丢失
					`"stop_reason":"end_turn"`, `"output_tokens":20`,
					"event: message_stop")
				if strings.Count(got, "event: message_start") != 2 {
					t.Errorf("usage_accumulation_multi_frame: want 2 message_start frames (one per usage frame), got %d in:\n%s",
						strings.Count(got, "event: message_start"), got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			switch tc.upstream {
			case "anthropic":
				got = parseAndSerializeAnthropic(t, tc.sse)
			case "openai":
				got = parseAndSerializeOpenAIToAnthropic(t, tc.sse)
			default:
				t.Fatalf("unknown upstream %q", tc.upstream)
			}
			tc.assertions(t, got)
			compareAnthropicStreamGolden(t, tc.name, got)
		})
	}
}

func assertContainsAll(t *testing.T, got, label string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("%s: output missing %q\ngot:\n%s", label, want, got)
		}
	}
}
