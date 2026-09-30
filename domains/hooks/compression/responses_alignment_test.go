package compression

import (
	"encoding/json"
	"testing"
)

// R34-A1 回归：responses 专属压缩链的 AlignmentMap provenance。
// messages 车道本就带 AlignmentMap；responses 专属链（input 数组裁剪）
// 此前无任何"哪些 input item 去了哪里"的审计证据。

func responsesBody(t *testing.T, items ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"model": "m", "input": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestExtractResponsesInputMessages_ArrayAndString(t *testing.T) {
	body := responsesBody(t,
		map[string]any{"type": "message", "role": "user", "content": "hello"},
		map[string]any{"type": "message", "role": "assistant", "content": "hi"},
	)
	msgs, err := extractResponsesInputMessages(body)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("array shape: %v msgs=%d", err, len(msgs))
	}

	// string input → 单条 user 包装消息
	strBody := []byte(`{"model":"m","input":"just a plain string"}`)
	msgs, err = extractResponsesInputMessages(strBody)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("string shape: %v msgs=%d", err, len(msgs))
	}
	var msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msgs[0], &msg); err != nil || msg.Role != "user" || msg.Content != "just a plain string" {
		t.Fatalf("string wrapper wrong: %s / %v", msgs[0], err)
	}

	// 无 input 字段 → 明确错误（fail-open 由调用方处理）
	if _, err := extractResponsesInputMessages([]byte(`{"model":"m"}`)); err == nil {
		t.Fatalf("missing input must error")
	}
}

func TestBuildResponsesAlignmentMap_RetainAndDrop(t *testing.T) {
	m0 := map[string]any{"type": "message", "role": "user", "content": "turn-0 oldest"}
	m1 := map[string]any{"type": "message", "role": "assistant", "content": "reply-1"}
	m2 := map[string]any{"type": "message", "role": "user", "content": "turn-2 latest"}
	before := responsesBody(t, m0, m1, m2)
	// 模拟专属链删除最老 item（index 0）
	after := responsesBody(t, m1, m2)

	am := BuildResponsesAlignmentMap(before, after)
	if len(am) != 3 {
		t.Fatalf("want 3 entries (one per original item), got %d", len(am))
	}
	if am[0].OriginalIndex != 0 || !am[0].IsCompressed || am[0].CompressedIndex != -1 ||
		am[0].TargetKind != TargetKindDropped {
		t.Fatalf("dropped entry wrong: %+v", am[0])
	}
	if am[1].TargetKind != TargetKindRetained || am[1].CompressedIndex != 0 ||
		am[1].TargetSpace != TargetSpaceResponsesInput {
		t.Fatalf("retained entry wrong: %+v", am[1])
	}
	if am[2].CompressedIndex != 1 {
		t.Fatalf("latest item should map to after-index 1: %+v", am[2])
	}
	// Occurrence 语义：唯一内容 → 全 0
	for i, a := range am {
		if a.Occurrence != 0 || a.Hash == "" {
			t.Fatalf("entry %d occurrence/hash wrong: %+v", i, a)
		}
	}

	// 相同内容的重复 item：hash+occurrence 区分，且保留匹配不串位
	dup := map[string]any{"type": "message", "role": "user", "content": "same"}
	before = responsesBody(t, dup, dup, m2)
	after = responsesBody(t, dup, m2)
	am = BuildResponsesAlignmentMap(before, after)
	if len(am) != 3 || am[0].Occurrence != 0 || am[1].Occurrence != 1 {
		t.Fatalf("occurrence tracking wrong: %+v", am)
	}
	if am[0].TargetKind != TargetKindRetained || am[1].TargetKind != TargetKindDropped {
		t.Fatalf("duplicate retention should match first occurrence: %+v", am)
	}
}

func TestBuildResponsesAlignmentMap_FailOpen(t *testing.T) {
	// before 非 responses 形态 → nil
	if am := BuildResponsesAlignmentMap([]byte(`{"messages":[]}`), []byte(`{"input":[]}`)); am != nil {
		t.Fatalf("non-responses before should yield nil, got %+v", am)
	}
	// 空数组 → nil
	if am := BuildResponsesAlignmentMap([]byte(`{"input":[]}`), []byte(`{"input":[]}`)); am != nil {
		t.Fatalf("empty input should yield nil, got %+v", am)
	}
}

func TestBuildAlignmentMapForProtocol_MessagesUnchanged(t *testing.T) {
	// 参数化重构回归：messages 车道行为不变（retained → TargetSpaceMessages）
	before := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`)
	after := []byte(`{"messages":[{"role":"assistant","content":"b"}]}`)
	am := buildAlignmentMapForProtocol(before, after, -1, "openai")
	if len(am) != 2 || am[0].TargetKind != TargetKindDropped ||
		am[1].TargetSpace != TargetSpaceMessages || am[1].CompressedIndex != 0 {
		t.Fatalf("messages lane regression: %+v", am)
	}
}
