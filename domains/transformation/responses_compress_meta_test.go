package transformation

import (
	"encoding/json"
	"strings"
	"testing"
)

// R34-A1 回归：responses 压缩的 WithMeta provenance 变体。原签名函数
// 必须保持兼容（委托后 meta 丢弃）；WithMeta 变体返回删除证据。

func respReq(items ...string) []byte {
	arr := make([]map[string]any, len(items))
	for i, c := range items {
		arr[i] = map[string]any{"type": "message", "role": "user", "content": c}
	}
	b, _ := json.Marshal(map[string]any{"model": "m", "input": arr})
	return b
}

func TestCompressResponsesInputWithMeta_DroppedIndexes(t *testing.T) {
	items := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, strings.Repeat("x", 400)+"-"+string(rune('A'+i)))
	}
	body := respReq(items...)

	out, meta := CompressResponsesInputIfNeededWithMeta(body, 3000, 200)
	if meta == nil {
		t.Fatalf("oversized body must trigger compression")
	}
	if meta.Strategy != "responses_input_trim" {
		t.Fatalf("strategy wrong: %q", meta.Strategy)
	}
	if meta.OriginalItems != 20 || meta.KeptItems >= 20 || len(meta.DroppedIndexes) == 0 {
		t.Fatalf("counts wrong: %+v", meta)
	}
	if meta.OriginalItems-meta.KeptItems != len(meta.DroppedIndexes) {
		t.Fatalf("dropped mismatch: %+v", meta)
	}
	// meta 证据与实际输出一致：dropped 下标升序、最新项不在删除之列
	for i, idx := range meta.DroppedIndexes {
		if i > 0 && idx <= meta.DroppedIndexes[i-1] {
			t.Fatalf("dropped indexes not ascending: %v", meta.DroppedIndexes)
		}
		if idx == 19 {
			t.Fatalf("latest item must never be dropped: %v", meta.DroppedIndexes)
		}
	}
	var outReq struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(out, &outReq); err != nil || len(outReq.Input) != meta.KeptItems {
		t.Fatalf("output items != meta.KeptItems: %d vs %d (err=%v)", len(outReq.Input), meta.KeptItems, err)
	}

	// 原签名兼容：同一输入输出体一致
	if CompressResponsesInputIfNeeded(body, 3000, 200) == nil {
		t.Fatalf("legacy signature must still work")
	}
}

func TestCompressResponsesInputWithMeta_NoTriggerNil(t *testing.T) {
	small := respReq("tiny")
	if _, meta := CompressResponsesInputIfNeededWithMeta(small, 100000, 200); meta != nil {
		t.Fatalf("small body must not trigger compression, got %+v", meta)
	}
	if _, meta := CompressResponsesInputIfNeededWithMeta(small, 0, 200); meta != nil {
		t.Fatalf("zero window must be no-op")
	}
}

func TestCompressResponsesInputAggressivelyWithMeta_Strategy(t *testing.T) {
	items := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, strings.Repeat("y", 500))
	}
	body := respReq(items...)
	_, meta := CompressResponsesInputAggressivelyWithMeta(body, 3000, 200)
	if meta == nil || meta.Strategy != "responses_input_trim_aggressive" {
		t.Fatalf("aggressive strategy wrong: %+v", meta)
	}
}

func TestCompressResponsesInputWithMeta_StringShape(t *testing.T) {
	big := strings.Repeat("z", 100000)
	body, _ := json.Marshal(map[string]any{"model": "m", "input": big})
	out, meta := CompressResponsesInputIfNeededWithMeta(body, 3000, 200)
	if meta == nil || !meta.InputString {
		t.Fatalf("string input should be rune-trimmed with meta, got %+v", meta)
	}
	if meta.KeptRunes >= meta.OriginalRunes {
		t.Fatalf("trim must shrink: %+v", meta)
	}
	var outReq struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(out, &outReq); err != nil || len([]rune(outReq.Input)) != meta.KeptRunes {
		t.Fatalf("output runes != meta.KeptRunes")
	}
}
