package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// 2026-10-20 12h 审计轮修正的门禁。
//
// ★ 缺陷本体：流式路径原来用
//
//	name, hasName := fn["name"].(string)
//	…
//	} else if hasName && seenIDs["named:"+slot] == 0 { emptyCount++ }
//
// 即**只认「显式空串」**（hasName 为真）⇒ 一个带 id、但 function 里**没有
// name 键**的 tool_call —— 正是本特性要抓的缺陷 —— 在流式路径下**不再被标记**。
//
// 而**非流式**路径（analyzeChatBody）用的是结构体字段
// `tc.Function.Name == ""`（JSON 里缺键即零值）⇒ 仍然标记。
// ⇒ 同一上游缺陷，流式与非流式的判定结果相反。
//
// ★ 判别依据：tool_call 的**首帧**带 id，续帧不带 id
// ⇒ 「无 name 键 + 有 id」= 首帧就缺 name ⇒ 缺陷。
//
// 变异：把 tool_call_quality.go 里的
// `(hasName || id != "")` 改回 `hasName` ⇒ 本用例会失败。
func TestStreamQualityDetectsToolCallWithoutNameKey(t *testing.T) {
	// 有 id、没有 name 键 ⇒ 缺陷形态。
	frame := qualityToolFrame(0, 0, "call1", "", "{}")
	if strings.Contains(frame, `"name"`) {
		t.Fatalf("夹具不对：它不该带 name 键 —— %s", frame)
	}
	for _, mode := range []string{QualityModeFix, QualityModeDetectOnly} {
		t.Run(mode, func(t *testing.T) {
			_, flags, _ := ProcessStreamLine(frame, mode, nil, nil)
			if !strings.Contains(strings.Join(flags, ","), QualityFlagEmptyToolName) {
				t.Fatalf("「有 id 但没有 name 键」的 tool_call 必须被标记为空名，"+
					"实际 flags=%v（流式与非流式口径必须一致）", flags)
			}
		})
	}
}

// 对照臂 ①：**续帧**（无 id、无 name）**不是**缺陷 —— 续帧本来就没有 name。
// 没有这条对照，门可能被「只要没有 name 就标记」这种过度标记的改法骗过
// （那会把每一个正常的分片参数帧都算成空名）。
func TestStreamQualityContinuationWithoutNameIsNotFlagged(t *testing.T) {
	// 先有一帧带 name 的 tool_call（建立槽位认知），再送续帧。
	first := qualityToolFrame(0, 0, "call1", "bash", "")
	_, flags, state := ProcessStreamLine(first, QualityModeFix, nil, nil)
	next := qualityToolFrame(0, 0, "", "", `{"command":"git status"}`)
	_, flags, _ = ProcessStreamLine(next, QualityModeFix, flags, state)
	if len(flags) != 0 {
		t.Fatalf("合法续帧不应被标记：flags=%v", flags)
	}
}

// 对照臂 ②：**同一个**槽位只应计一次空名（续帧不重复计）。
func TestStreamQualityCountsMissingNameOncePerSlot(t *testing.T) {
	// 首帧有 id 无 name ⇒ 缺陷；续帧同样带 id（有的上游会重复带）无 name。
	first := qualityToolFrame(0, 0, "call1", "", "{}")
	second := qualityToolFrame(0, 0, "call1", "", `{"x":1}`)
	_, flags, state := ProcessStreamLine(first, QualityModeFix, nil, nil)
	before := countFlag(flags, QualityFlagEmptyToolName)
	_, flags, _ = ProcessStreamLine(second, QualityModeFix, flags, state)
	after := countFlag(flags, QualityFlagEmptyToolName)
	if before != 1 {
		t.Fatalf("首帧应恰好计一次空名，实际 %d", before)
	}
	if after != before {
		t.Fatalf("同一槽位不应重复计空名：%d → %d", before, after)
	}
}

// 对照臂 ③：**有 name** 的 tool_call 不得被标记（防止判据过宽）。
func TestStreamQualityNamedToolCallNotFlagged(t *testing.T) {
	frame := qualityToolFrame(0, 0, "call1", "bash", "{}")
	_, flags, _ := ProcessStreamLine(frame, QualityModeFix, nil, nil)
	if countFlag(flags, QualityFlagEmptyToolName) != 0 {
		t.Fatalf("有 name 的 tool_call 不得被标记为空名：flags=%v", flags)
	}
}

// countFlag 数一个 flag 在列表里出现几次（flags 是可重复的计数列表）。
func countFlag(flags []string, want string) int {
	n := 0
	for _, f := range flags {
		if f == want {
			n++
		}
	}
	return n
}

// 确认夹具真的产出了「有 id、无 name 键」的 JSON（防止夹具本身写错导致恒绿门）。
func TestQualityToolFrameFixtureOmitsNameKey(t *testing.T) {
	frame := qualityToolFrame(0, 0, "call1", "", "{}")
	body := strings.TrimPrefix(strings.TrimSpace(frame), "data: ")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("夹具不是合法 JSON：%v", err)
	}
	choices, _ := decoded["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("夹具结构不对：%s", frame)
	}
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	calls, _ := delta["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("夹具结构不对：%s", frame)
	}
	call, _ := calls[0].(map[string]any)
	if _, ok := call["id"]; !ok {
		t.Fatalf("夹具应带 id：%s", frame)
	}
	fn, _ := call["function"].(map[string]any)
	if _, ok := fn["name"]; ok {
		t.Fatalf("夹具不应带 name 键：%s", frame)
	}
}
