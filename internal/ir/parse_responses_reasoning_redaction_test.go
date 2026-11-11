// parse_responses_reasoning_redaction_test.go —— reasoning 信封脱敏的钉测（★ 2026-11-11 审计 P2-I）
//
// ★ 缺陷：★ `parseResponsesInputItem` 在丢弃 reasoning item 时，把**整个 item** 当 anomaly
// ★ ★ 的 metadata 上报 ⇒ ★★ `summary`（★ 模型的思考文本）与 `encrypted_content`
// ★ ★（★ 不透明加密推理块）**逐字**进 anomaly 事件 ⇒ ★★ 随 slog.Warn("ir.anomaly")
// ★ ★ 进**可全文检索**的日志面、也随 format-anomaly 采集**落库**
// ★ ★⇒ ★★ 思考内容外泄面 ⇒ ★★ 且对「这是不是一个 reasoning item」这个诊断**零增益**。
//
// ★ 修法：★ 只上报描述性字段（★ type / id / has_summary / has_encrypted_content）。
//
// ★ 变异臂：★ 把 `redactReasoningItem(item)` 换回 `item` ⇒ ★★ 用例红。

package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseResponsesReasoningItemMetadataRedacted 钉住 P2-I：
//
// ★ 断言的是**上报出去的 metadata**（★ 而不是助记函数本身）⇒ ★★ 守的是「外泄面真的被堵住」。
func TestParseResponsesReasoningItemMetadataRedacted(t *testing.T) {
	const secretSummary = "SECRET-THINKING-TEXT-MUST-NOT-REACH-LOGS"
	const secretEnc = "SECRET-ENCRYPTED-REASONING-BLOB"

	var got []AnomalyEvent
	prev := SetAnomalyReporter(func(ev AnomalyEvent) { got = append(got, ev) })
	defer SetAnomalyReporter(prev)

	item := map[string]any{
		"type": "reasoning",
		"id":   "rs_unit_1",
		"summary": []any{
			map[string]any{"type": "summary_text", "text": secretSummary},
		},
		"encrypted_content": secretEnc,
	}

	if _, _, err := parseResponsesInputItem(item); err != nil {
		t.Fatalf("parseResponsesInputItem must drop reasoning without error, got %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 anomaly event for the dropped reasoning item, got %d", len(got))
	}

	blob, err := json.Marshal(got[0].Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	blobStr := string(blob)

	// ★ 外泄面必须堵死
	if strings.Contains(blobStr, secretSummary) {
		t.Fatalf("reasoning summary text leaked into anomaly metadata: %s", blobStr)
	}
	if strings.Contains(blobStr, secretEnc) {
		t.Fatalf("encrypted_content leaked into anomaly metadata: %s", blobStr)
	}

	// ★ 诊断信息不能丢（★ 脱敏不是「什么都不报」）
	md := got[0].Metadata
	if md["type"] != "reasoning" {
		t.Fatalf("metadata.type = %v, want reasoning", md["type"])
	}
	if md["id"] != "rs_unit_1" {
		t.Fatalf("metadata.id = %v, want rs_unit_1", md["id"])
	}
	if md["has_summary"] != true {
		t.Fatalf("metadata.has_summary = %v, want true (★ 存在性是诊断信息的一部分)", md["has_summary"])
	}
	if md["has_encrypted_content"] != true {
		t.Fatalf("metadata.has_encrypted_content = %v, want true", md["has_encrypted_content"])
	}
	// ★ 「有无」代替「是什么」⇒ ★ summary / encrypted_content 这两个**键**都不该出现
	for _, k := range []string{"summary", "encrypted_content"} {
		if _, present := md[k]; present {
			t.Fatalf("metadata must not carry key %q (★ 只保留存在性标志)", k)
		}
	}
}
