package sanitize

// 2026-10-11 回归门：三处「占位符未正确还原」的生产缺口。
//
// 1. Anthropic 流式 thinking 增量（原生透传 + chat→anthropic 桥的
//    reasoning_content 合成）不在任何还原通道：已映射占位符原样下发，
//    残缺标记让整帧及其后所有帧被阻断。
// 2. Responses 终态事件（response.completed / output_item.done / part.done）
//    内嵌上游原始全文（占位符未还原），此前整帧被阻断——所有内容 delta
//    已送达后流被截断。
// 3. 探针早退过滤只认花括号：模型剥掉花括号的裸形态（minimax-m3 实测）
//    让 placeholderCandidate 判假，非流式字段级还原被跳过，整条响应被
//    残留探针阻断。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/stretchr/testify/require"
)

func newStreamLaneRestorer(t *testing.T) *SanitizeRestoreInterceptor {
	t.Helper()
	rdb := setupSaniGuardRedis(t)
	require.NoError(t, rdb.HSet(context.Background(), SanitizeRedisKey("r1015-lanes"),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	return it
}

func streamLaneChunk(t *testing.T, it *SanitizeRestoreInterceptor, frame string) *response.ChunkResult {
	t.Helper()
	result, err := it.InterceptStreamChunk(context.Background(), []byte(frame),
		&response.StreamMeta{SessionID: "r1015-lanes", State: response.NewStreamState()})
	require.NoError(t, err)
	return result
}

func TestStreamAnthropicThinkingDeltaRestoresPlaceholder(t *testing.T) {
	it := newStreamLaneRestorer(t)
	frame := `data:{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking {SENSITIVE:phone:1} again"}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "mapped marker in thinking must restore, not block")
	require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	require.Contains(t, string(result.ModifiedChunk), "13800138000")
}

func TestStreamAnthropicThinkingDegenerateMarkerMaskedNotBlocked(t *testing.T) {
	it := newStreamLaneRestorer(t)
	// 2026-10-08 事故同类：模型把标记改写成非法形态回显。thinking lane 现在
	// 必须就地遮蔽放行，而不是把整帧（及其后所有帧）阻断。
	frame := `data:{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"echo {SENSITIVE:secret:X} done"}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedChunk), "SENSITIVE")
	// r1038：遮蔽物带类型——退化跨段仍能读出 "secret"，标记是
	// "[REDACTED:secret]" 而不是裸形态。
	require.Contains(t, string(result.ModifiedChunk), RedactionMarker(TypeSecret))
}

func TestStreamAnthropicThinkingBareMarkerRestored(t *testing.T) {
	it := newStreamLaneRestorer(t)
	frame := `data:{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"use SENSITIVE:phone:1 later"}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedChunk), "SENSITIVE")
	require.Contains(t, string(result.ModifiedChunk), "13800138000")
}

func TestStreamResponsesCompletedRestoresNestedOutput(t *testing.T) {
	it := newStreamLaneRestorer(t)
	// response.completed 内嵌上游原始全文：delta 已逐段还原，但终态事件里的
	// text 仍是占位符。此前该帧过不了残留探针被整体阻断（流在内容送完后
	// 截断）；现在嵌套树按非流式同款通道还原。
	frame := `data:{"type":"response.completed","response":{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"answer {SENSITIVE:phone:1}"}]}]}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "terminal frame with restorable markers must be rewritten, not blocked")
	require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	require.Contains(t, string(result.ModifiedChunk), "13800138000")
}

func TestStreamResponsesOutputItemDoneRestoresFunctionArguments(t *testing.T) {
	it := newStreamLaneRestorer(t)
	frame := `data:{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"send","arguments":"{\"to\":\"{SENSITIVE:phone:1}\"}"}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	var decoded struct {
		Item struct {
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	require.NoError(t, json.Unmarshal([]byte(testSSEPayload(t, result.ModifiedChunk)), &decoded))
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(decoded.Item.Arguments), &args))
	require.Equal(t, "13800138000", args["to"])
}

func TestStreamResponsesReasoningSummaryPartDoneRestores(t *testing.T) {
	it := newStreamLaneRestorer(t)
	frame := `data:{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":"recalled {SENSITIVE:phone:1}"}}` + "\n\n"
	result := streamLaneChunk(t, it, frame)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	require.Contains(t, string(result.ModifiedChunk), "13800138000")
}

func TestNonStreamBareMarkerFieldRestoredNotBlocked(t *testing.T) {
	it := newStreamLaneRestorer(t)
	// 裸形态（无花括号）占位符：字段级 gate（placeholderCandidate）此前
	// 早退跳过还原，残留探针随后阻断整条响应。修复后字段照常还原。
	body := []byte(`{"type":"message","content":[{"type":"text","text":"use SENSITIVE:phone:1 tomorrow"}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "r1015-lanes", ClientProtocol: "anthropic-messages", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "bare-form marker must be restored, not block the response")
	require.NotNil(t, result.ModifiedBody)
	require.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
	require.Contains(t, string(result.ModifiedBody), "13800138000")
}

func TestProbeFiltersAdmitBareMarkerForm(t *testing.T) {
	// 探针早退过滤的单元钉：裸形态必须被识别为候选，否则字段级还原被跳过。
	require.True(t, placeholderCandidate("value SENSITIVE:phone:1 here"))
	require.True(t, placeholderCandidate("value sensitive:phone:1 here"))
	require.False(t, placeholderCandidate("this is a sensitive topic"))
	require.False(t, placeholderCandidate("no markers at all"))
	body := []byte(`{"choices":[{"message":{"content":"call SENSITIVE:phone:1 now"}}]}`)
	require.True(t, hasReservedPlaceholder(context.Background(), body))
}
