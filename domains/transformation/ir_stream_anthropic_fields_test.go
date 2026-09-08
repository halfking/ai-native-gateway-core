package transformation

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// Audit-r2 Track A#2 (2026-09-08)：IR 流式路径 Anthropic 序列化丢字段回归测试。
//   - 缺陷 1：SerializeAnthropic 的 ChunkTypeUsage 分支 if/else-if 互斥，单帧
//     同时携带 input+output（OpenAI 最终 usage 帧）时 CompletionTokens 被吞。
//   - 缺陷 2：ChunkTypeDelta 分支丢弃 FinishReason/StopReason，终止帧没有
//     message_delta/stop_reason。
//   - 编排层：Anthropic 把 usage 拆两帧，OpenAI 客户端期望最终 usage 帧
//     input/output/cache 齐全，由 streamUsageAccumulator 跨帧合并。

// 带缓存 token 的 Anthropic 上游流（message_start: input+cache，message_delta: output+stop）。
const anthropicSSEWithCache = `event: message_start
data: {"type":"message_start","message":{"id":"msg_c1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":210,"output_tokens":0,"cache_creation_input_tokens":33,"cache_read_input_tokens":44}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

// extractSSEJSONFrames 从 ConvertStream 输出中剥掉 SSE 包装，取出所有 JSON 负载。
// 注意：当前 processStreamLine 对非 Responses 客户端会把序列化结果再包一层
// "data: "（独立缺陷，另行跟踪），因此这里对每行循环剥前缀，保持本测试只
// 聚焦字段级断言、不依赖帧包装形态。
func extractSSEJSONFrames(t *testing.T, body, marker string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, marker) {
			continue
		}
		payload := line
		for strings.HasPrefix(payload, "data: ") {
			payload = strings.TrimPrefix(payload, "data: ")
		}
		if strings.HasPrefix(payload, "event: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("frame JSON invalid (%q): %v\nbody:\n%s", payload, err, body)
		}
		frames = append(frames, m)
	}
	return frames
}

// 缺陷 1 回归：OpenAI 上游单帧合并 usage（prompt+completion 同帧）→ Anthropic
// 客户端必须同时收到 message_start(input) 与 message_delta(output+stop_reason)。
// 修复前：usage 帧走 message_start 分支，completion 与 stop_reason 双双丢失。
func TestIRTransport_OpenAICombinedUsage_AnthropicClientGetsBothFrames(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()

	openaiSSE := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1500,\"completion_tokens\":50,\"total_tokens\":1550,\"prompt_tokens_details\":{\"cached_tokens\":100}}}\n\n" +
		"data: [DONE]\n\n"
	resp := mockSSEUpstreamResponse(t, openaiSSE)

	env := domain.NewEnvelopeBuilder("combined-usage").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "anthropic-messages",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "claude-sonnet-4-20250514",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}
	body := w.Body.String()

	starts := extractSSEJSONFrames(t, body, "message_start")
	deltas := extractSSEJSONFrames(t, body, "message_delta")

	if len(starts) == 0 {
		t.Fatalf("no message_start frame in output:\n%s", body)
	}
	msgUsage, ok := starts[0]["message"].(map[string]any)["usage"].(map[string]any)
	if !ok {
		t.Fatalf("message_start missing message.usage:\n%s", body)
	}
	if got := msgUsage["input_tokens"].(float64); got != 1500 {
		t.Errorf("message_start input_tokens = %v, want 1500", got)
	}
	if got := msgUsage["cache_read_input_tokens"].(float64); got != 100 {
		t.Errorf("message_start cache_read_input_tokens = %v, want 100 (cached_tokens 映射)", got)
	}

	if len(deltas) == 0 {
		t.Fatalf("no message_delta frame in output (completion_tokens/stop_reason 被丢):\n%s", body)
	}
	deltaUsage, _ := deltas[0]["usage"].(map[string]any)
	if got := deltaUsage["output_tokens"].(float64); got != 50 {
		t.Errorf("message_delta output_tokens = %v, want 50 (互斥分支吞掉了 CompletionTokens)", got)
	}
	deltaObj, _ := deltas[0]["delta"].(map[string]any)
	if got := deltaObj["stop_reason"]; got != "end_turn" {
		t.Errorf("message_delta stop_reason = %v, want end_turn (finish_reason=stop 未透出)", got)
	}
}

// 缺陷 2 回归：OpenAI 终止 delta 帧（delta:{} + finish_reason，无 usage）→
// Anthropic 客户端必须收到 message_delta/stop_reason。修复前该分支整帧丢弃。
func TestIRTransport_OpenAITerminalDelta_AnthropicClientGetsStopReason(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()

	openaiSSE := "data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"working\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	resp := mockSSEUpstreamResponse(t, openaiSSE)

	env := domain.NewEnvelopeBuilder("terminal-delta").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "anthropic-messages",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "claude-sonnet-4-20250514",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}
	body := w.Body.String()

	deltas := extractSSEJSONFrames(t, body, "message_delta")
	if len(deltas) == 0 {
		t.Fatalf("no message_delta frame (stop_reason 丢失):\n%s", body)
	}
	deltaObj, _ := deltas[len(deltas)-1]["delta"].(map[string]any)
	if got := deltaObj["stop_reason"]; got != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use (finish_reason=tool_calls 映射)\nbody:\n%s", got, body)
	}
}

// 编排层跨帧累计：Anthropic 上游把 usage 拆两帧 → OpenAI 客户端最终 usage 帧
// 必须 input/output/cache 齐全（修复前最终帧 prompt=0/cache 丢失）。
func TestIRTransport_AnthropicUpstream_OpenAIClient_FinalUsageFrameComplete(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, anthropicSSEWithCache)

	env := domain.NewEnvelopeBuilder("usage-merge").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "gpt-4o",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}
	body := w.Body.String()

	usageFrames := extractSSEJSONFrames(t, body, "\"usage\"")
	if len(usageFrames) < 2 {
		t.Fatalf("want >=2 usage frames (message_start 与 message_delta 各一), got %d:\n%s", len(usageFrames), body)
	}
	final := usageFrames[len(usageFrames)-1]["usage"].(map[string]any)
	if got := final["prompt_tokens"].(float64); got != 210 {
		t.Errorf("final usage prompt_tokens = %v, want 210", got)
	}
	if got := final["completion_tokens"].(float64); got != 7 {
		t.Errorf("final usage completion_tokens = %v, want 7", got)
	}
	if got := final["total_tokens"].(float64); got != 217 {
		t.Errorf("final usage total_tokens = %v, want 217 (上游无 total 时按 input+output 补齐)", got)
	}
	details, _ := final["prompt_tokens_details"].(map[string]any)
	if details == nil || details["cached_tokens"].(float64) != 44 {
		t.Errorf("final usage prompt_tokens_details.cached_tokens missing/!= 44: %v", final)
	}
	// cache_creation 同样应透传到 OpenAI 帧之外可观测（此处校验不丢即可）：
	// OpenAI 协议无 cache_creation 对应键，IR 层保留在累计器里，不影响最终帧。
}

// Anthropic→Anthropic 直通：原生 stop_reason/stop_sequence 逐字保留，
// message_start 与 message_delta 各自的 usage 半场不被互斥覆盖。
func TestIRTransport_AnthropicPassthrough_PreservesStopReasonAndSplitUsage(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()

	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_p1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":15,"output_tokens":0,"cache_read_input_tokens":6}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hey"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"\n\nBREAK"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`
	resp := mockSSEUpstreamResponse(t, sse)

	env := domain.NewEnvelopeBuilder("passthrough").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "anthropic-messages",
			UpstreamProtocol: "anthropic-messages",
			ClientModel:      "claude-sonnet-4-20250514",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}
	body := w.Body.String()

	starts := extractSSEJSONFrames(t, body, "message_start")
	if len(starts) == 0 {
		t.Fatalf("no message_start:\n%s", body)
	}
	msgUsage, _ := starts[0]["message"].(map[string]any)["usage"].(map[string]any)
	if msgUsage["input_tokens"].(float64) != 15 || msgUsage["cache_read_input_tokens"].(float64) != 6 {
		t.Errorf("message_start usage = %v, want input=15 cache_read=6", msgUsage)
	}

	deltas := extractSSEJSONFrames(t, body, "message_delta")
	if len(deltas) == 0 {
		t.Fatalf("no message_delta:\n%s", body)
	}
	deltaObj, _ := deltas[0]["delta"].(map[string]any)
	if deltaObj["stop_reason"] != "stop_sequence" {
		t.Errorf("stop_reason = %v, want native stop_sequence (逐字透传)", deltaObj["stop_reason"])
	}
	if deltaObj["stop_sequence"] != "\n\nBREAK" {
		t.Errorf("stop_sequence = %v, want \\n\\nBREAK", deltaObj["stop_sequence"])
	}
	if got := deltas[0]["usage"].(map[string]any)["output_tokens"].(float64); got != 3 {
		t.Errorf("output_tokens = %v, want 3", got)
	}
}

// streamUsageAccumulator 单测：零值不覆盖正值、cache/多模态指针字段保留、
// total 缺失时按 input+output 补齐。
func TestStreamUsageAccumulator_MergeSemantics(t *testing.T) {
	var acc streamUsageAccumulator

	if acc.Snapshot() != nil {
		t.Fatal("empty accumulator snapshot should be nil")
	}

	cacheRead, cacheWrite, reasoning := 44, 33, 5
	acc.Observe(&ir.StreamUsage{
		PromptTokens:     210,
		CacheReadTokens:  &cacheRead,
		CacheWriteTokens: &cacheWrite,
	})

	// Anthropic message_delta 半场：仅 output，input/cache 不应被零值抹掉。
	acc.Observe(&ir.StreamUsage{CompletionTokens: 7})

	snap := acc.Snapshot()
	if snap.PromptTokens != 210 || snap.CompletionTokens != 7 {
		t.Errorf("merged prompt/completion = %d/%d, want 210/7", snap.PromptTokens, snap.CompletionTokens)
	}
	if snap.TotalTokens != 217 {
		t.Errorf("merged total = %d, want 217 (自动补齐)", snap.TotalTokens)
	}
	if snap.CacheReadTokens == nil || *snap.CacheReadTokens != 44 {
		t.Errorf("merged cache_read = %v, want 44", snap.CacheReadTokens)
	}
	if snap.CacheWriteTokens == nil || *snap.CacheWriteTokens != 33 {
		t.Errorf("merged cache_write = %v, want 33", snap.CacheWriteTokens)
	}
	if snap.ReasoningTokens != nil {
		t.Errorf("merged reasoning should stay unset, got %v", snap.ReasoningTokens)
	}

	// 上游显式给出 total 时以最后非零为准。
	acc.Observe(&ir.StreamUsage{PromptTokens: 210, CompletionTokens: 7, TotalTokens: 999})
	if got := acc.Snapshot().TotalTokens; got != 999 {
		t.Errorf("total after explicit frame = %d, want 999", got)
	}

	// 零值帧不回退任何已累计字段。
	acc.Observe(&ir.StreamUsage{})
	snap = acc.Snapshot()
	if snap.PromptTokens != 210 || snap.CompletionTokens != 7 || snap.TotalTokens != 999 {
		t.Errorf("zero frame clobbered accumulator: %+v", snap)
	}

	// reasoning tokens 透传。
	acc.Observe(&ir.StreamUsage{ReasoningTokens: &reasoning})
	if got := acc.Snapshot().ReasoningTokens; got == nil || *got != 5 {
		t.Errorf("merged reasoning = %v, want 5", got)
	}
}
