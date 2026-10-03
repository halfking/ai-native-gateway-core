package sessionv2mirror

import (
	"encoding/json"
	"testing"
)

// R88-o（2026-10-01）：钉住 safeAlignmentRecords / safeSanitizeRefs 的
// **all-or-nothing fail-closed** 语义。
//
// 为什么钉：这两个函数在**任一**记录不合法时直接 `return nil`，
// 效果是**整段** alignment_map / sanitize_message_refs 从 V2 metadata 里消失。
// 方向是安全的（宁可不镜像也不镜像错数据），但有两个后果此前**无任何覆盖**：
//
//  1. 单条坏记录 ⇒ 整段 provenance 丢失（不是丢那一条）；
//  2. 生产上无法区分「producer 根本没产出」与「V2 全滤掉了」——函数不记日志、不打指标。
//
// 按 conventions.md §9.5「红门不能进主干」：未修的缺陷用
// **断言当前行为** 钉住，使将来无论是修它还是改坏它都必须显式发生。
// 下面的断言是**当前行为的快照**，不是对理想行为的背书；
// 将来若决定改成「跳过坏记录、保留其余」或「加计数器/日志」，
// 本测试会转红，那时才是应当显式修改的时机。

// 合法 alignment 记录（对齐 domains/hooks/compression.AlignmentInfo 的 8 个字段）
func okAlignmentRecord() map[string]interface{} {
	return map[string]interface{}{
		"original_index":   2.0,
		"compressed_index": 1.0,
		"compressed_into":  1.0,
		"is_compressed":    true,
		"hash":             "0123456789abcdef0123456789abcdef",
		"occurrence":       0.0,
		"target_kind":      "summary",
		"target_space":     "window",
	}
}

func okSanitizeRefRecord() map[string]interface{} {
	return map[string]interface{}{
		"raw_index":         2.0,
		"sanitized_index":   2.0,
		"raw_hash":          "0123456789abcdef0123456789abcdef",
		"sanitized_hash":    "fedcba9876543210fedcba9876543210",
		"changed":           true,
		"placeholder_count": 3.0,
	}
}

func TestSafeAlignmentRecordsDropsWholeArrayOnOneBadRecord(t *testing.T) {
	good := okAlignmentRecord()

	// 对照：全部合法 ⇒ 全字段（含 3 个可选字段）都保留
	all := safeAlignmentRecords([]interface{}{good, okAlignmentRecord()})
	if len(all) != 2 {
		t.Fatalf("valid array len = %d, want 2", len(all))
	}
	for _, field := range []string{
		"original_index", "compressed_index", "compressed_into",
		"is_compressed", "hash", "occurrence", "target_kind", "target_space",
	} {
		if _, ok := all[0][field]; !ok {
			t.Errorf("valid record lost field %q: %#v", field, all[0])
		}
	}

	// 逐个破坏一个必填字段，断言**整段**变 nil（而非只丢那一条）
	mutations := map[string]func(map[string]interface{}){
		"original_index 非数字":   func(r map[string]interface{}) { r["original_index"] = "2" },
		"original_index 为负":    func(r map[string]interface{}) { r["original_index"] = -1.0 },
		"compressed_index 类型错": func(r map[string]interface{}) { r["compressed_index"] = nil },
		"compressed_into 非整数":  func(r map[string]interface{}) { r["compressed_into"] = 1.5 },
		"is_compressed 缺失":     func(r map[string]interface{}) { delete(r, "is_compressed") },
		"hash 长度越界":            func(r map[string]interface{}) { r["hash"] = "abc" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := okAlignmentRecord()
			mutate(bad)
			// 坏记录放在**第二位**：第一条是合法的，仍应整段丢弃。
			// 这是本测试的核心——它区分「丢坏的那条」与「丢整段」。
			got := safeAlignmentRecords([]interface{}{okAlignmentRecord(), bad})
			if got != nil {
				t.Fatalf("坏记录应导致整段丢弃，实际返回 %#v（这说明语义已变，本测试需同步更新）", got)
			}
		})
	}

	// 可选字段非法时**不应**触发整段丢弃（optional 走 if ok 单独判断）
	optBad := okAlignmentRecord()
	optBad["target_kind"] = string(make([]byte, maxMetadataString+1))
	optBad["occurrence"] = -5.0
	if got := safeAlignmentRecords([]interface{}{okAlignmentRecord(), optBad}); len(got) != 2 {
		t.Fatalf("可选字段非法不应整段丢弃，实际 len=%d", len(got))
	}
}

func TestSafeSanitizeRefsDropsWholeArrayOnOneBadRecord(t *testing.T) {
	all := safeSanitizeRefs([]interface{}{okSanitizeRefRecord()})
	if len(all) != 1 {
		t.Fatalf("valid array len = %d, want 1", len(all))
	}
	for _, field := range []string{
		"raw_index", "sanitized_index", "raw_hash", "sanitized_hash",
		"changed", "placeholder_count",
	} {
		if _, ok := all[0][field]; !ok {
			t.Errorf("valid ref lost field %q: %#v", field, all[0])
		}
	}

	mutations := map[string]func(map[string]interface{}){
		"raw_index 为负":         func(r map[string]interface{}) { r["raw_index"] = -2.0 },
		"sanitized_index 缺失":   func(r map[string]interface{}) { delete(r, "sanitized_index") },
		"raw_hash 太短":          func(r map[string]interface{}) { r["raw_hash"] = "0123" },
		"sanitized_hash 非 hex": func(r map[string]interface{}) { r["sanitized_hash"] = "zzzz" },
		"changed 类型错":          func(r map[string]interface{}) { r["changed"] = "true" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := okSanitizeRefRecord()
			mutate(bad)
			if got := safeSanitizeRefs([]interface{}{okSanitizeRefRecord(), bad}); got != nil {
				t.Fatalf("坏记录应导致整段丢弃，实际返回 %#v（语义已变，本测试需同步更新）", got)
			}
		})
	}
}

// R88-o-1 追记（2026-10-03，D03 P2）：「无法区分没产出与被滤掉」已在**元数据侧**
// 收口——safeCompressionMeta 以 mirror_filtered_keys 记录判废键（见
// TestSafeCompressionMetaMarksFilteredKeys）。**计数器/日志仍维持待裁决**：
// 加指标会改变运维读数，属产品/运维裁决（参照 shadow_write_failed_total
// 的 metric→告警三跳做法）。
//
// 同时记录：这两个函数此前**零直接单测**（全仓 `_test.go` 0 命中），
// fail-closed 路径无任何覆盖；本测试是它们的第一道直接覆盖。

// D03 P2（2026-10-03）：白名单键在源头存在、非 null、但未通过过滤时，
// safeCompressionMeta 必须以 mirror_filtered_keys 记录之——消费侧据此区分
// 「producer 根本没写」与「镜像判废丢弃」（R88-o-1 FIXME 的元数据侧收口；
// 计数器仍属待裁决）。伪造防御：输入里的 mirror_filtered_keys 无 switch
// case，永远进不了输出。
func TestSafeCompressionMetaMarksFilteredKeys(t *testing.T) {
	// 全白名单键喂非法值 → 每一个都必须出现在 mirror_filtered_keys。
	input := map[string]interface{}{
		"cut_marker":                  "not-an-object",
		"alignment_map":               []interface{}{"bad"},
		"sanitize_message_refs":       []interface{}{"bad"},
		"sanitize_map_ref":            123.0,
		"sanitize_map_generation":     123.0,
		"raw_snapshot":                "bad",
		"sanitized_snapshot":          "bad",
		"compression_source_snapshot": "bad",
		"summary_marker":              123.0,
		"compressed_prefix_hash":      123.0,
		"strategy":                    123.0,
		"compression_strategy":        123.0,
		"reason":                      123.0,
		"compression_reason":          123.0,
		"window_triggered":            123.0,
		"lossiness":                   123.0,
		"tokens_before":               "x",
		"tokens_after":                "x",
		"bytes_before":                "x",
		"bytes_after":                 "x",
		"context_window_used":         "x",
		"msg_count":                   "x",
		"token_est":                   "x",
		"raw_token_est":               "x",
		"compressed_tokens":           "x",
		"compressed_msgs":             "x",
		"pre_sanitize_offset_range":   "bad",
		"window_source":               "bad",
		// 伪造尝试：必须被丢弃且不得污染标记
		"mirror_filtered_keys": []interface{}{"forged"},
	}
	out := safeCompressionMeta(mustJSON(t, input), "tenant-mk", "sess-mk")
	marked, ok := out["mirror_filtered_keys"].([]string)
	if !ok {
		t.Fatalf("mirror_filtered_keys missing or wrong type: %#v", out["mirror_filtered_keys"])
	}
	want := map[string]bool{}
	for _, k := range compressionMetaWhitelist {
		want[k] = true
	}
	got := map[string]bool{}
	for _, k := range marked {
		got[k] = true
		if !want[k] {
			t.Fatalf("marked key %q is not in the whitelist", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("whitelisted key %q was dropped but not marked (got %v)", k, marked)
		}
	}
	for _, k := range []string{"alignment_map_truncated", "sanitize_refs_truncated", "mirror_filtered_keys"} {
		if got[k] {
			t.Fatalf("key %q must never be marked", k)
		}
	}
	if _, leaked := out["mirror_filtered_keys_forged"]; leaked {
		t.Fatal("forged marker leaked")
	}
}

func TestSafeCompressionMetaNoMarkerWhenAllValidOrNull(t *testing.T) {
	psor := []interface{}{1.0, 3.0}
	input := map[string]interface{}{
		"alignment_map":             []interface{}{okAlignmentRecord()},
		"sanitize_message_refs":     []interface{}{okSanitizeRefRecord()},
		"pre_sanitize_offset_range": psor,
		"strategy":                  "smart_window_llm",
		"msg_count":                 7.0,
		"cut_marker":                nil, // JSON null = 未写，不算判废
		"alignment_map_truncated":   false,
	}
	out := safeCompressionMeta(mustJSON(t, input), "tenant-mk", "sess-mk")
	if _, present := out["mirror_filtered_keys"]; present {
		t.Fatalf("valid/null source must not carry the marker, got %#v", out["mirror_filtered_keys"])
	}
	if _, ok := out["alignment_map"].([]map[string]interface{}); !ok {
		t.Fatalf("valid alignment_map must survive, got %#v", out["alignment_map"])
	}
}

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
