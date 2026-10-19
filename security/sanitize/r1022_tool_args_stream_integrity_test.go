package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 2026-10-13 — r1022：流式还原不得破坏工具参数 JSON 的结构完整性。
//
// 实测（本地网关 :8782，claude-sonnet-5 经 vapeur，/v1/responses 流式）：
// 工具参数 delta 里出现 marker 家族文本（模型自造的 `{sensitive_flag}`，
// 或回显请求侧遮蔽产生的 `{SENSITIVE:...}`）时，restoreStreamStringField
// 的两个毒分支把**整段 delta** 替换成 `[REDACTED]`——被吞掉的常常是
// delta 自带的 JSON 结构字符（`\"`、`}`、`&&`）。客户端把
// function_call_arguments.delta 拼接后 JSON.parse，一旦分片边界落在
// marker 尾部与收尾结构字符之间，拼接结果就是非法 JSON：
// 表现为「返回有 tools 但没有成功执行」（ZCode 报 tools 解析错误，
// 工具不跑，模型下轮重新发起调用，死循环）。
//
// 真实线帧（curl 收全量 SSE 捕获，无害内容触发）：
//
//	deltas: ["{\"command\":", " \"echo \\"", "\"c", "onfig ", "", "",
//	         "[REDACTED]", " && echo ", "don", "e\"}"]
//	→ "sensitive_flag} = 1\"" 整段消失，含 JSON 转义引号
//
// 修复：JSON 参数 lane（.arguments/.partial_json/anthropic.*.input/
// response.function_call_arguments.*）上，marker 之外的字节必须原样
// 透传——遮蔽只作用于 marker 跨度，不吞结构字符。散文 lane 保持历史上的
// 整段遮蔽语义不变。

func r1022Interceptor(t *testing.T) *SanitizeRestoreInterceptor {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	return it
}

// r1022ResponsesArgsFrame 构造一条 /v1/responses 的
// response.function_call_arguments.delta 帧（真实捕获形状）。
func r1022ResponsesArgsFrame(t *testing.T, itemID, delta string) []byte {
	t.Helper()
	payload := map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      itemID,
		"output_index": 0,
		"delta":        delta,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return append(append([]byte("data: "), data...), '\n', '\n')
}

// r1022FeedArgsDeltas 依序喂入分片，返回每帧实际下发的 delta 字段值。
func r1022FeedArgsDeltas(t *testing.T, it *SanitizeRestoreInterceptor, session string, deltas []string) []string {
	t.Helper()
	meta := &response.StreamMeta{
		SessionID: session,
		RequestID: session,
		State:     response.NewStreamState(),
	}
	var emitted []string
	for i, d := range deltas {
		result, err := it.InterceptStreamChunk(context.Background(), r1022ResponsesArgsFrame(t, "toolu_r1022", d), meta)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if result == nil {
			// 未改写 = 原样透传（线上 wire 即如此），计入拼接。
			emitted = append(emitted, d)
			continue
		}
		if result.ShouldBlock {
			t.Fatalf("frame %d: unexpectedly blocked", i)
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(result.ModifiedChunk)), "data: "))), &frame); err != nil {
			t.Fatalf("frame %d output not JSON: %v (%q)", i, err, string(result.ModifiedChunk))
		}
		out, _ := frame["delta"].(string)
		emitted = append(emitted, out)
	}
	return emitted
}

// TestR1022ToolArgsContinuationKeepsJSONStructure — 两个毒分支各自的续帧
// 形状：续帧携带收尾结构字符（`"}`），修复前整帧变 `[REDACTED]`，拼接
// JSON 非法；修复后 marker 前缀被遮蔽、结构字符存活。
//
//	case "masked"：restoreText 掩蔽了 combined（分支 A 路径）
//	case "plain"：restoreText 对 combined 不动文本（分支 B 路径，前缀后
//	              跟空格使 loose 文法不命中）
func TestR1022ToolArgsContinuationKeepsJSONStructure(t *testing.T) {
	cases := []struct {
		name   string
		frames []string
	}{
		{"masked", []string{`{"command":"echo {sens`, `itive_flag}"}`}},
		// reservedMarkerStarted 要求扣住的前缀已含完整 marker 词
		// （`{sensitive`），此时续帧无论是什么都会被旧代码整帧丢弃。
		{"plain", []string{`{"command":"echo {sensitive`, ` xyz stuff"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := r1022Interceptor(t)
			emitted := r1022FeedArgsDeltas(t, it, "r1022-continuation-"+c.name, c.frames)

			joined := strings.Join(emitted, "")
			if !json.Valid([]byte(joined)) {
				t.Fatalf("accumulated tool args are invalid JSON after restore: %q", joined)
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(joined), &parsed); err != nil {
				t.Fatalf("accumulated tool args do not parse: %v (%q)", err, joined)
			}
			cmd, _ := parsed["command"].(string)
			if !strings.Contains(cmd, "echo ") {
				t.Fatalf("command value lost outside the marker span: %q (joined=%q)", cmd, joined)
			}
			if strings.Contains(strings.ToLower(joined), "sensitive") {
				t.Fatalf("reserved marker word leaked to the client: %q", joined)
			}
		})
	}
}

// TestR1022HeldPrefixActuallyWithheld — 前提在场判据（判据纪律第 3 条）：
// 分片 1 的 marker 前缀必须真的被扣住，否则上面的合法性断言可能是
// 「看不见」型假绿。
func TestR1022HeldPrefixActuallyWithheld(t *testing.T) {
	it := r1022Interceptor(t)
	emitted := r1022FeedArgsDeltas(t, it, "r1022-withheld",
		[]string{`{"command":"echo {sens`, `itive_flag}"}`})
	if len(emitted) == 0 {
		t.Fatal("no frames emitted — cannot assert the hold")
	}
	first := emitted[0]
	if strings.Contains(strings.ToLower(first), "sens") {
		t.Fatalf("marker prefix was NOT withheld on frame 1: %q", first)
	}
	if !strings.HasPrefix(first, `{"command":"echo `) {
		t.Fatalf("frame 1 lost its own bytes before the marker start: %q", first)
	}
}

// TestR1022MaskedCompletionKeepsTail — 毒分支 A（restoreText 掩蔽完成）：
// 续帧在掩蔽 marker 的同时必须保留帧内其余字节。修复前该分支同样输出
// 纯 `[REDACTED]`，把 `x tail\"}` 一并吞掉。
func TestR1022MaskedCompletionKeepsTail(t *testing.T) {
	it := r1022Interceptor(t)
	emitted := r1022FeedArgsDeltas(t, it, "r1022-masked-tail",
		[]string{`{"command":"echo {SENSITIVE:phone:`, `13800138000x tail"}`})
	joined := strings.Join(emitted, "")
	if !json.Valid([]byte(joined)) {
		t.Fatalf("accumulated tool args are invalid JSON after masked completion: %q", joined)
	}
	if !strings.Contains(joined, `x tail`) {
		t.Fatalf("non-marker tail of the delta was swallowed: %q", joined)
	}
	if strings.Contains(strings.ToLower(joined), "sensitive") {
		t.Fatalf("reserved marker word leaked: %q", joined)
	}
}

// TestR1022LiveCapturedShapeStaysValid — 用线上真实捕获的分片序列
// （printf "%s" "{sensitive_value}" && echo ok 一轮）回放：无论分片边界
// 落在哪，拼接结果必须仍是合法 JSON 且命令语义段（&& echo ok）不丢。
func TestR1022LiveCapturedShapeStaysValid(t *testing.T) {
	it := r1022Interceptor(t)
	captured := []string{
		`{"command": "printf \"%s\" `, // 上游分片形状 1
		`\"{sens`,                     // marker 前缀被扣的边界（引号为 JSON 转义形态）
		`itive_value}\" && echo ok"}`,
	}
	emitted := r1022FeedArgsDeltas(t, it, "r1022-live-shape", captured)
	joined := strings.Join(emitted, "")
	if !json.Valid([]byte(joined)) {
		t.Fatalf("live-captured shape produced invalid JSON: %q", joined)
	}
	if !strings.Contains(joined, `echo ok`) {
		t.Fatalf("command tail after the marker span was swallowed: %q", joined)
	}
	if strings.Contains(strings.ToLower(joined), "sensitive") {
		t.Fatalf("reserved marker word leaked: %q", joined)
	}
}

// TestR1022ProseLaneStillRedactsWholeDelta — 控制组：散文 lane 的历史
// 整段遮蔽语义不因本修复改变（marker 前缀 + 垃圾续文 → 该帧整体
// `[REDACTED]`，这正是 r244 修出来的行为）。
func TestR1022ProseLaneStillRedactsWholeDelta(t *testing.T) {
	it := r1022Interceptor(t)
	meta := &response.StreamMeta{
		SessionID: "r1022-prose",
		RequestID: "r1022-prose",
		State:     response.NewStreamState(),
	}
	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:\"}}]}\n\n")
	if _, err := it.InterceptStreamChunk(context.Background(), first, meta); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	garbage := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"13800138000x tail\"}}]}\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), garbage, meta)
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if result == nil {
		t.Fatal("expected the prose delta to be rewritten")
	}
	if !strings.Contains(string(result.ModifiedChunk), unknownRedactedMarker) {
		t.Fatalf("prose lane no longer redacts: %q", string(result.ModifiedChunk))
	}
	if strings.Contains(strings.ToLower(string(result.ModifiedChunk)), "sensitive") {
		t.Fatalf("marker word leaked on prose lane: %q", string(result.ModifiedChunk))
	}
}
