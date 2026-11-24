package sessionsummary

// 2026-11-24 2041 审计轮回归钉测：
// ① stripThinkingTags 误剥回退（JSON 字符串值内字面标签）
// ② describeResponseShape 脱敏形态（不落原文、rune 安全）

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSONEscapesWhenValueContainsLiteralTag(t *testing.T) {
	// 值内含 </output> ⇒ 旧实现 LastIndex 截断 ⇒ 残缺 JSON；新实现回退原文提取
	in := `{"summary": "a</output>b", "title": "t"}`
	got := extractJSON(in)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("回退路径未生效，提取结果残缺: %q err=%v", got, err)
	}
	if v["title"] != "t" {
		t.Fatalf("字段丢失: %v", v)
	}

	// 值内含 <answer> ⇒ 旧实现 FirstIndex 截尾；新实现回退
	in2 := `{"summary": "user asked <answer> what", "title": "t"}`
	got2 := extractJSON(in2)
	var v2 map[string]interface{}
	if err := json.Unmarshal([]byte(got2), &v2); err != nil {
		t.Fatalf("case2 回退失败: %q err=%v", got2, err)
	}
}

func TestExtractJSONStillStripsThinkingPrefix(t *testing.T) {
	// 正常 <think> 包裹场景不受回退影响
	for _, in := range []string{
		`<think>reasoning here</think>{"a":1}`,
		`<think>{"not json"`, // 未闭合
	} {
		got := extractJSON(in)
		if !strings.HasPrefix(got, "{") {
			t.Fatalf("think 剥离回退: in=%q got=%q", in, got)
		}
	}
}

func TestDescribeResponseShapeRedacts(t *testing.T) {
	secret := `Here is your private conversation about Alice <html><body>民</body></html>`
	shape := describeResponseShape(secret)
	if strings.Contains(shape, "Alice") || strings.Contains(shape, "conversation") || strings.Contains(shape, "民") {
		t.Fatalf("形态摘要泄漏原文: %q", shape)
	}
	if !strings.Contains(shape, "html=true") || !strings.Contains(shape, "runes=") {
		t.Fatalf("形态摘要缺关键特征: %q", shape)
	}
	// rune 安全：多字节计数正确（"民"占 1 rune ⇒ 73 rune；字节口径会得 75）
	if !strings.Contains(shape, "runes=73") {
		t.Fatalf("rune 计数错(字节口径会得 75): %q", shape)
	}
}
