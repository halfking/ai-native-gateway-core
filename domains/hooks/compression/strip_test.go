package compression

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestStripToolInfo_NilAndEmpty 验证 nil / 空 body 的安全 fallback。
func TestStripToolInfo_NilAndEmpty(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		protocol string
	}{
		{"nil body", nil, "anthropic"},
		{"empty body", []byte{}, "openai"},
		{"whitespace only", []byte("   "), "anthropic"},
		{"invalid json", []byte("{not-json"), "openai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, result := StripToolInfo(tt.body, tt.protocol)
			// out should equal input on parse failures
			if string(out) != string(tt.body) {
				t.Errorf("expected passthrough on invalid input, got %q", string(out))
			}
			if result.DidStrip {
				t.Error("expected DidStrip=false on invalid input")
			}
		})
	}
}

// TestStripToolInfo_NoToolsToStrip 验证没有 tool 时直接 passthrough。
func TestStripToolInfo_NoToolsToStrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"}
		]
	}`)
	out, result := StripToolInfo(body, "anthropic")
	if !result.DidStrip && string(out) == string(body) {
		// no tool rounds = no change is acceptable
		return
	}
	if result.ToolCallsRemoved != 0 || result.ToolResultsRemoved != 0 {
		t.Errorf("expected no removals, got tool_calls=%d tool_results=%d",
			result.ToolCallsRemoved, result.ToolResultsRemoved)
	}
}

func TestStripThinkingBlocks_PreservesMultimodalBlocks(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},{"type":"input_audio","input_audio":{"data":"Zm9v","format":"wav"}}]}`)
	out := stripThinkingBlocks(raw)
	var message map[string]any
	if err := json.Unmarshal(out, &message); err != nil {
		t.Fatal(err)
	}
	parts := message["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("content parts = %d, want 3", len(parts))
	}
	if parts[0].(map[string]any)["type"] != "text" || parts[1].(map[string]any)["type"] != "image_url" || parts[2].(map[string]any)["type"] != "input_audio" {
		t.Errorf("non-thinking blocks were not preserved: %v", parts)
	}
}

// TestStripToolInfo_OpenAI_CompletedRound 验证 openai 协议下, 完成 tool 轮被剥离,
// 保留最近 keepLastRounds 轮。需要 >keepLastRounds(2) 个 round 才会触发删除。
func TestStripToolInfo_OpenAI_CompletedRound(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": "r1"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c2", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c2", "content": "r2"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c3", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c3", "content": "r3"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c4", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c4", "content": "r4"},
			{"role": "user", "content": "thanks"}
		]
	}`)
	out, result := StripToolInfo(body, "openai")
	outStr := string(out)
	if result.ToolCallsRemoved == 0 && result.ToolResultsRemoved == 0 {
		t.Error("expected at least one tool_call + tool_result to be removed with 4 rounds")
	}
	// 最早的 c1 应被删除（超过 keepLastRounds=2）。
	if strings.Contains(outStr, "c1") {
		t.Errorf("oldest round c1 should be stripped, still present")
	}
	if result.BytesAfter > result.BytesBefore {
		t.Errorf("strip must shrink body: before=%d after=%d",
			result.BytesBefore, result.BytesAfter)
	}
}

// TestStripToolInfo_PreservesLastRounds 验证最近的 tool 轮总是被完整保留
// (assistant.tool_calls 与其 tool_result 成对存在，不产生孤儿)。
//
// 2026-08-06 fix: 旧实现只保留最后一条 tool result，删掉了它的
// assistant.tool_calls 锚点，导致孤儿 tool_call_id 被
// SanitizeToolMessages 二次删除。新实现保留完整 round。
func TestStripToolInfo_PreservesLastRounds(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "q1"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "function": {"name": "f1"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": "r1"},
			{"role": "user", "content": "q2"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c2", "function": {"name": "f2"}}]},
			{"role": "tool", "tool_call_id": "c2", "content": "r2"},
			{"role": "user", "content": "q3"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c3", "function": {"name": "f3"}}]},
			{"role": "tool", "tool_call_id": "c3", "content": "r3"}
		]
	}`)
	out, _ := StripToolInfo(body, "openai")
	outStr := string(out)
	// 保留的末轮必须 anchor + result 配对，不能出现孤儿 tool_call_id。
	if strings.Contains(outStr, `"tool_call_id":"c2"`) && !strings.Contains(outStr, `"id":"c2"`) {
		t.Errorf("orphan: c2 result kept but its assistant.tool_calls anchor stripped:\n%s", outStr)
	}
	if strings.Contains(outStr, `"tool_call_id":"c3"`) && !strings.Contains(outStr, `"id":"c3"`) {
		t.Errorf("orphan: c3 result kept but its assistant.tool_calls anchor stripped:\n%s", outStr)
	}
	// 最早一轮应被删除。
	if strings.Contains(outStr, `"tool_call_id":"c1"`) {
		t.Errorf("oldest round c1 should be stripped, got: %s", outStr)
	}
}

// TestStripToolInfo_IncompleteRound 验证未完成 tool_call (无匹配 result)
// 必须被保留，不得删除。
//
// 2026-08-06 fix: 旧实现把不完整 round 也删除，丢失了进行中的工具调用。
// 新实现只删 complete round，incomplete 原样保留。
func TestStripToolInfo_IncompleteRound(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "q"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c1", "function": {"name": "f1"}}]}
		]
	}`)
	out, result := StripToolInfo(body, "openai")
	if result.DidStrip {
		t.Errorf("incomplete round must NOT be stripped, DidStrip=true")
	}
	// 解析检查 c1 是否存活（不用字符串包含，避免 JSON 空格差异）。
	var obj struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range obj.Messages {
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				if tm, ok := tc.(map[string]any); ok {
					if tm["id"] == "c1" {
						return // 存活，测试通过
					}
				}
			}
		}
	}
	t.Errorf("incomplete tool_call c1 must survive strip")
}

// TestHasToolCalls / TestIsToolResult 单元测试辅助函数。
func TestHasToolCalls(t *testing.T) {
	if !hasToolCalls(json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"x"}]}`)) {
		t.Error("expected tool_calls present for assistant role")
	}
	if hasToolCalls(json.RawMessage(`{"role":"user","tool_calls":[{"id":"x"}]}`)) {
		t.Error("hasToolCalls requires role=assistant; user role should be false")
	}
	if hasToolCalls(json.RawMessage(`{"role":"assistant","content":"hi"}`)) {
		t.Error("expected no tool_calls when field missing")
	}
	if hasToolCalls(json.RawMessage(`not-json`)) {
		t.Error("invalid json should return false")
	}
}

func TestIsToolResult(t *testing.T) {
	if !isToolResult(json.RawMessage(`{"role":"tool","tool_call_id":"c1"}`)) {
		t.Error("expected tool result")
	}
	if isToolResult(json.RawMessage(`{"role":"user","content":"hi"}`)) {
		t.Error("user msg should not be tool result")
	}
}

// TestDetectToolRounds 验证结构化 round 检测。
//
// 2026-08-06 fix: detectToolRounds 从返回扁平 map[int]bool 改为返回
// []toolRound，每个 round 含 anchor + results + complete 标志。这让
// filterMessages 能原子地保留/删除整轮，不再产生孤儿 tool_call_id。
func TestDetectToolRounds(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"q"}`),
		json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"f"}}]}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"c1","content":"r"}`),
	}
	rounds := detectToolRounds(msgs)
	if len(rounds) != 1 {
		t.Fatalf("expected 1 round, got %d: %+v", len(rounds), rounds)
	}
	if rounds[0].anchor != 1 {
		t.Errorf("anchor should be index 1, got %d", rounds[0].anchor)
	}
	if len(rounds[0].results) != 1 || rounds[0].results[0] != 2 {
		t.Errorf("results should be [2], got %v", rounds[0].results)
	}
	if !rounds[0].complete {
		t.Errorf("round with one call and one matching result must be complete")
	}
}

// TestDetectToolRounds_ParallelCallsAreOneRound 验证并行 tool_calls
// (一个 assistant 消息含多个 call id) 被计为一个 round，不是多个。
func TestDetectToolRounds_ParallelCallsAreOneRound(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"},{"id":"c"}]}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"a","content":"ra"}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"b","content":"rb"}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"c","content":"rc"}`),
	}
	rounds := detectToolRounds(msgs)
	if len(rounds) != 1 {
		t.Fatalf("parallel calls must be one round, got %d", len(rounds))
	}
	if !rounds[0].complete {
		t.Errorf("all three results present, round must be complete")
	}
	if len(rounds[0].results) != 3 {
		t.Errorf("expected 3 results, got %d", len(rounds[0].results))
	}
}

// TestDetectToolRounds_IncompleteNotComplete 验证缺 result 的 round
// complete=false，不会被 filterMessages 删除。
func TestDetectToolRounds_IncompleteNotComplete(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"a","content":"ra"}`),
	}
	rounds := detectToolRounds(msgs)
	if len(rounds) != 1 {
		t.Fatalf("expected 1 round, got %d", len(rounds))
	}
	if rounds[0].complete {
		t.Errorf("round missing result for 'b' must NOT be complete")
	}
}

// TestExtractToolCallIDs 验证 call id 提取。
func TestExtractToolCallIDs(t *testing.T) {
	raw := json.RawMessage(`{"tool_calls":[{"id":"c1"},{"id":"c2"}]}`)
	ids := extractToolCallIDs(raw)
	if len(ids) != 2 || ids[0] != "c1" || ids[1] != "c2" {
		t.Errorf("expected [c1, c2], got %v", ids)
	}
}

// TestStripToolInfo_NeverOrphansToolResults 是 2026-08-06 P0 回归测试。
// 生产实测：43 条消息 / 20 个合法 tool pair 经 strip 后变成 4 条 / 1 个孤儿
// tool result。原因是 filterMessages 只保留最后一条 tool result，删掉了
// 它的 assistant.tool_calls 锚点，孤儿被 SanitizeToolMessages 二次删除。
// 本测试断言：strip 后任何存活的 tool result 都必须有匹配的 anchor。
func TestStripToolInfo_NeverOrphansToolResults(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "start"},
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "read", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": id, "content": "result " + id},
		)
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "final"})
	body, _ := json.Marshal(map[string]any{"model": "m", "messages": msgs})

	out, res := StripToolInfo(body, "openai")
	var obj struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range obj.Messages {
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				if tm, ok := tc.(map[string]any); ok {
					if id, _ := tm["id"].(string); id != "" {
						ids[id] = true
					}
				}
			}
		}
	}
	orphan := 0
	for _, m := range obj.Messages {
		if r, _ := m["role"].(string); r == "tool" {
			id, _ := m["tool_call_id"].(string)
			if !ids[id] {
				orphan++
			}
		}
	}
	if orphan > 0 {
		t.Errorf("strip produced %d orphaned tool results (DidStrip=%v, MessagesRemoved=%d)", orphan, res.DidStrip, res.MessagesRemoved)
	}
}

// TestStripToolInfo_FailOpenOnCorruption 验证完整性 guard：如果 strip 会
// 产生孤儿，StripToolInfo 必须放弃 strip，原样返回 body。
func TestStripToolInfo_FailOpenOnCorruption(t *testing.T) {
	// 构造一个 strip 后会产生孤子的场景：只保留 result，删掉所有 anchor。
	// 但 detectToolRounds 只删 complete round，所以这里用一个 complete round
	// 后跟一个 orphan tool result (无 anchor) 来测试 guard。
	// 实际上 filterMessages 不会制造孤儿（原子删除），所以这个测试主要
	// 验证 toolChainIntact 本身能检测孤儿。
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"tool","tool_call_id":"orphan","content":"no anchor"}`),
	}
	if toolChainIntact(msgs) {
		t.Error("toolChainIntact must detect orphan tool result without anchor")
	}
	// 合法链必须通过。
	msgsOK := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"x"}]}`),
		json.RawMessage(`{"role":"tool","tool_call_id":"x","content":"r"}`),
	}
	if !toolChainIntact(msgsOK) {
		t.Error("toolChainIntact must pass for a valid tool chain")
	}
}
