package sessionv2mirror

import "testing"

// R88-o 补遗：R88-o 那一轮只钉了 safeAlignmentRecords / safeSanitizeRefs，
// 漏掉了白名单里的**第三个键** cut_marker 所走的 safeCutMarkerMeta。
// 它同样是 all-or-nothing（`hook.go:590-592`），而且比前两个多一层
// **跨字段自洽校验**——恰恰是最该钉住却没被钉的部分。
//
// 特别说明：`hook_test.go` 里已有一条端到端用例覆盖 cut_marker 的正常路径，
// 所以它并非「零覆盖」；本测试补的是**校验分支**，此前一条都没有。

func okCutMarker() map[string]interface{} {
	return map[string]interface{}{
		"version":                   1.0,
		"created_at":                123.0,
		"source_msg_count":          10.0,
		"system_msg_count":          1.0,
		"cut_index":                 4.0,
		"strategy":                  "smart_window_llm",
		"bytes_before":              2000000.0,
		"bytes_after":               1500000.0,
		"summary_marker":            "[smm_v1:0123456789abcdef]",
		"pre_sanitize_offset_range": []interface{}{1.0, 5.0},
	}
}

func TestSafeCutMarkerMetaDropsWholeOnInvalid(t *testing.T) {
	// 对照：全合法 ⇒ 6 个必填 + 4 个可选全部保留
	full := safeCutMarkerMeta(okCutMarker())
	if full == nil {
		t.Fatal("valid cut marker was dropped")
	}
	for _, field := range []string{
		"version", "created_at", "source_msg_count", "system_msg_count",
		"cut_index", "strategy", "bytes_before", "bytes_after",
		"summary_marker", "pre_sanitize_offset_range",
	} {
		if _, ok := full[field]; !ok {
			t.Errorf("valid cut marker lost field %q: %#v", field, full)
		}
	}

	// 逐个破坏必填字段 ⇒ 整段丢弃
	required := map[string]func(map[string]interface{}){
		"version 缺失":           func(r map[string]interface{}) { delete(r, "version") },
		"created_at 负数":        func(r map[string]interface{}) { r["created_at"] = -1.0 },
		"source_msg_count 非数字": func(r map[string]interface{}) { r["source_msg_count"] = "10" },
		"system_msg_count 小数":  func(r map[string]interface{}) { r["system_msg_count"] = 1.5 },
		"strategy 未知标签":        func(r map[string]interface{}) { r["strategy"] = "totally_made_up" },
	}
	for name, mutate := range required {
		t.Run(name, func(t *testing.T) {
			bad := okCutMarker()
			mutate(bad)
			if got := safeCutMarkerMeta(bad); got != nil {
				t.Fatalf("必填字段非法应整段丢弃，实际 %#v（语义已变，本测试需同步更新）", got)
			}
		})
	}
}

// TestSafeCutMarkerMetaCrossFieldInvariants 钉住两个**跨字段**自洽校验：
// 它们不是「字段在不在」的问题，而是「字段之间说不说得通」，
// 是整个 cut_marker 校验里最有价值也最容易在重构中被悄悄删掉的两条。
func TestSafeCutMarkerMetaCrossFieldInvariants(t *testing.T) {
	cases := []struct {
		name string
		mut  func(map[string]interface{})
	}{
		{"cut_index 为 0（cut<=0）", func(r map[string]interface{}) { r["cut_index"] = 0.0 }},
		{"system+cut > source（不自洽）", func(r map[string]interface{}) {
			r["source_msg_count"] = 3.0 // 1 + 4 > 3
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := okCutMarker()
			c.mut(bad)
			if got := safeCutMarkerMeta(bad); got != nil {
				t.Fatalf("跨字段不自洽应整段丢弃，实际 %#v（该校验可能被删了，本测试需同步更新）", got)
			}
		})
	}

	// 反向对照：边界值 system+cut == source 应当**放行**。
	// 这条专门防「把 > 写成 >=」的过度收紧——那会让合法数据被静默丢弃。
	t.Run("边界 system+cut == source 放行", func(t *testing.T) {
		edge := okCutMarker()
		edge["source_msg_count"] = 5.0 // 1 + 4 == 5
		got := safeCutMarkerMeta(edge)
		if got == nil {
			t.Fatal("system+cut == source 属合法边界，不应被丢弃")
		}
		if got["cut_index"] != 4.0 {
			t.Fatalf("cut_index = %v, want 4", got["cut_index"])
		}
	})

	// 可选字段非法只丢该字段，不影响其余
	t.Run("可选字段非法只丢该字段", func(t *testing.T) {
		partial := okCutMarker()
		delete(partial, "bytes_before")
		partial["summary_marker"] = map[string]interface{}{"nested": true}
		partial["pre_sanitize_offset_range"] = []interface{}{1.0, 999.0} // pair[1] > source
		got := safeCutMarkerMeta(partial)
		if got == nil {
			t.Fatal("可选字段非法不应整段丢弃")
		}
		if _, ok := got["summary_marker"]; ok {
			t.Errorf("非法 summary_marker 不应被保留: %#v", got)
		}
		if _, ok := got["pre_sanitize_offset_range"]; ok {
			t.Errorf("越界的 pre_sanitize_offset_range 不应被保留: %#v", got)
		}
		if _, ok := got["bytes_after"]; !ok {
			t.Errorf("合法可选字段 bytes_after 丢了: %#v", got)
		}
		for _, field := range []string{"version", "created_at", "source_msg_count", "system_msg_count", "cut_index", "strategy"} {
			if _, ok := got[field]; !ok {
				t.Errorf("必填字段 %q 丢了: %#v", field, got)
			}
		}
	})
}
