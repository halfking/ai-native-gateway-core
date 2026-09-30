package executors

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// R34-A1 回归：provenance 组装 helper。压缩未触发时 no-op；触发时产出
// v7 §3.2 兼容 JSON（strategy/trim_phase/bytes_* + dropped indexes +
// AlignmentMap）。

func TestRecordResponsesInputTrimMeta_NilMetaNoop(t *testing.T) {
	dst := []byte("keep-me")
	recordResponsesInputTrimMeta(&dst, []byte("a"), []byte("b"), nil)
	if string(dst) != "keep-me" {
		t.Fatalf("nil meta must be no-op, got %s", dst)
	}
}

func TestRecordResponsesInputTrimMeta_PayloadShape(t *testing.T) {
	items := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, strings.Repeat("x", 400))
	}
	arr := make([]map[string]any, len(items))
	for i, c := range items {
		arr[i] = map[string]any{"type": "message", "role": "user", "content": c}
	}
	before, _ := json.Marshal(map[string]any{"model": "m", "input": arr})
	after, meta := transformation.CompressResponsesInputIfNeededWithMeta(before, 3000, 200)
	if meta == nil {
		t.Fatalf("expected compression to fire")
	}

	var dst []byte
	recordResponsesInputTrimMeta(&dst, before, after, meta)
	if len(dst) == 0 {
		t.Fatalf("meta non-nil must produce payload")
	}
	var payload map[string]any
	if err := json.Unmarshal(dst, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["strategy"] != "responses_input_trim" || payload["trim_phase"] != "pre_request" {
		t.Fatalf("strategy/phase wrong: %v", payload)
	}
	if _, ok := payload["dropped_input_indexes"]; !ok {
		t.Fatalf("dropped_input_indexes missing: %v", payload)
	}
	am, ok := payload["alignment_map"].([]any)
	if !ok || len(am) != meta.OriginalItems {
		t.Fatalf("alignment_map must have one entry per original item: %v", payload["alignment_map"])
	}

	// string 形态：无 alignment_map（退化映射省略），有 rune 计数
	strBody, _ := json.Marshal(map[string]any{"model": "m", "input": strings.Repeat("z", 100000)})
	_, smeta := transformation.CompressResponsesInputIfNeededWithMeta(strBody, 3000, 200)
	var dst2 []byte
	recordResponsesInputTrimMeta(&dst2, strBody, strBody, smeta)
	var payload2 map[string]any
	_ = json.Unmarshal(dst2, &payload2)
	if _, ok := payload2["alignment_map"]; ok {
		t.Fatalf("string shape must omit alignment_map")
	}
	if payload2["input_string"] != true {
		t.Fatalf("input_string flag missing: %v", payload2)
	}
}
