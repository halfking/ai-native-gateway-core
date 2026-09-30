package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// R74 P0 回归：Anthropic 滑动窗口重建后不得留下悬空的 tool_use。
//
// 旧实现 TrimAnthropicTail 删除 tool_result-only 的 user 消息，理由是
// 「避免 tool_use_id 孤儿」——方向恰好相反：删掉 result 而保留声明它的
// assistant.tool_use，正好制造 Anthropic 必拒的孤儿。压缩把一个原本合法的
// 请求变成了必然 400 的请求。
//
// 判别力：把 TrimAnthropicTail 退回旧实现，本测试必须红。
func TestR74_AnthropicRebuildLeavesNoOrphanToolUse(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"first user intent"},
		{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"r"}]},
		{"role":"assistant","content":"done"},
		{"role":"user","content":"second user turn"},
		{"role":"assistant","content":"second answer"}
	]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	out, ok := RebuildAnthropicAfterSummary(body, "summary", ret, 3)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	declared := map[string]bool{}
	resolved := map[string]bool{}
	var probe struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	for _, m := range probe.Messages {
		if !strings.HasPrefix(strings.TrimSpace(string(m.Content)), "[") {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			TUID string `json:"tool_use_id"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				declared[b.ID] = true
			case "tool_result":
				resolved[b.TUID] = true
			}
		}
	}
	for id := range declared {
		if !resolved[id] {
			t.Errorf("ORPHAN tool_use %q：assistant 锚点保留但 tool_result 被删，上游 Anthropic 必 400", id)
		}
	}
	for id := range resolved {
		if !declared[id] {
			t.Errorf("ORPHAN tool_result %q：tool_use 锚点被删，Anthropic 同样拒绝", id)
		}
	}
	if len(declared) == 0 {
		t.Fatal("前置条件不成立：重建结果里应至少有一个 tool_use")
	}
}

// R74 P0 回归（另一侧）：cut 落在 result 之后时，留下的是孤儿 tool_result。
// 既有测试 TestRebuildAnthropicAfterSummary_DropsOrphanToolResult 覆盖
// keepRecentPairs=1 的这一侧；本例把两侧钉在同一处修法上。
func TestR74_AnthropicRebuildDropsOrphanToolResult(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"hi"}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"f","input":{}}]}`),
	}
	// tool_use 的 result 已被切掉 —— 锚点必须一并丢弃
	cleaned, dropped := TrimAnthropicTail(messages)
	if dropped != 1 {
		t.Errorf("dropped=%d, 期望 1（无 result 的 tool_use 锚点必须丢弃）", dropped)
	}
	if len(cleaned) != 1 || messageRole(cleaned[0]) != "user" {
		t.Errorf("cleaned=%s, 期望只剩开头的 user 消息", cleaned)
	}
}
