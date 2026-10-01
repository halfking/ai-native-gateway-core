package sessionv2mirror

import "testing"

// R88-y 补遗：`safeWindowSource` 是 V2 白名单里
// provider-window source telemetry（`window_source` 计数）所走的过滤器
// （`hook.go:446-455` / `:469-490`），但 R88-o 只为前三个 safe* 补了测试
// （safeAlignmentRecords / safeSanitizeRefs / safeCutMarkerMeta），
// R88-p 补上了第三个 cut_marker，**第四个 window_source 一直没被钉**。
//
// 本测试要钉住的**核心语义**（也是它与前三个兄弟最不一样的地方）：
//
//	safeAlignmentRecords / safeSanitizeRefs / safeCutMarkerMeta
//	    = all-or-nothing（任一条不合法 ⇒ 整段 return nil）
//	safeWindowSource
//	    = **逐条过滤**（某条 label/count 不合法 ⇒ 只跳过那一条，其余照常保留）
//
// 这条差异是**语义**不是实现细节：如果哪天有人为了「和兄弟们保持一致」把它
// 改成 all-or-nothing，一条坏 label 就会让整份来源构成遥测消失——而遥测丢失
// 是静默的（D03 §3 第 4 条要求「指标不串层」，反过来「指标全没了」同样违约）。
// 所以这里必须钉死。
//
// 数据形态取自真库实证（`request_logs` 215 万行中 window_source 出现 43,787 行，
// 实测键只有两个）：
//
//	retained : 43787
//	summary  : 43787
//
// 三个生产常量（`domains/hooks/compression/alignment_reverse.go:140-142`）
// retained / summary / dropped 全为小写、≤8 字符，天然满足
// boundedCompressionWord 的小写+[0-9_-]、≤24 字符约束。

// realWindowSource 复原真库里最常见的来源构成形态。
func realWindowSource() map[string]interface{} {
	return map[string]interface{}{
		"retained": float64(12),
		"summary":  float64(3),
	}
}

func TestSafeWindowSourceKeepsGoodEntriesAlongsideBad(t *testing.T) {
	// 本测试文件的主断言：**逐条过滤，不是 all-or-nothing**。
	// 坏条目放在**中间**而不是末尾，确保实现不是「遇到坏的就整体放弃」。
	input := map[string]interface{}{
		"retained":   float64(12),
		"UPPERCASE":  float64(1), // 大写 label —— 违反小写约束
		"summary":    float64(3),
		"has space":  float64(2), // 空格 —— 不在 [a-z0-9_-] 内
		"dropped":    float64(5),
		"negative":   float64(-1), // 负数 count —— boundedNumber 拒绝
		"fractional": float64(1.5),
		"stringy":    "7",        // 字符串 count —— boundedNumber 要求 float64
		"":           float64(1), // 空 label
	}
	got := safeWindowSource(input)
	if got == nil {
		t.Fatal("含坏条目不应整段丢弃：safeWindowSource 是逐条过滤，" +
			"若这里变 nil 说明被改成了 all-or-nothing（语义已变）")
	}
	for _, want := range []string{"retained", "summary", "dropped"} {
		if _, ok := got[want]; !ok {
			t.Errorf("合法条目 %q 丢了: %#v", want, got)
		}
	}
	// 反向断言：坏条目确实没混进去
	for _, bad := range []string{"UPPERCASE", "has space", "negative", "fractional", "stringy", ""} {
		if _, ok := got[bad]; ok {
			t.Errorf("非法条目 %q 不应被保留: %#v", bad, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("结果应恰为 3 条合法条目，实际 %d 条: %#v", len(got), got)
	}
}

func TestSafeWindowSourceRealShapeRoundTrip(t *testing.T) {
	// 对照组：真库实测形态必须原样通过。
	got := safeWindowSource(realWindowSource())
	if got == nil {
		t.Fatal("真库形态被丢弃")
	}
	if got["retained"] != float64(12) || got["summary"] != float64(3) {
		t.Fatalf("真库形态的值被改动: %#v", got)
	}
}

func TestSafeWindowSourceWholeMapDropCases(t *testing.T) {
	t.Run("非 map 输入", func(t *testing.T) {
		for _, bad := range []interface{}{
			nil, "retained", float64(3), []interface{}{1.0}, true,
		} {
			if got := safeWindowSource(bad); got != nil {
				t.Errorf("非 map 输入 %#v 应丢弃，实际 %#v", bad, got)
			}
		}
	})

	t.Run("空 map", func(t *testing.T) {
		if got := safeWindowSource(map[string]interface{}{}); got != nil {
			t.Errorf("空 map 应丢弃，实际 %#v", got)
		}
	})

	t.Run("全部条目非法 ⇒ nil", func(t *testing.T) {
		// 注意与主断言的区别：这里是**一条都留不下**，所以整段 nil。
		got := safeWindowSource(map[string]interface{}{
			"UPPERCASE": float64(1),
			"neg":       float64(-1),
		})
		if got != nil {
			t.Errorf("全非法应返回 nil，实际 %#v", got)
		}
	})

	t.Run("超过 16 个标签 ⇒ 整段丢弃（不是截断到 16）", func(t *testing.T) {
		// 这条钉的是「超限即整段丢」而非「取前 16 个」——
		// 截断会让调用方误以为拿到的是全量构成。
		over := make(map[string]interface{}, 17)
		for i := 0; i < 17; i++ {
			over[string(rune('a'+i))] = float64(i)
		}
		if got := safeWindowSource(over); got != nil {
			t.Errorf("17 个标签应整段丢弃，实际 %#v", got)
		}
		// 反向对照：恰好 16 个必须放行
		exact := make(map[string]interface{}, 16)
		for i := 0; i < 16; i++ {
			exact[string(rune('a'+i))] = float64(i)
		}
		if got := safeWindowSource(exact); got == nil || len(got) != 16 {
			t.Errorf("恰好 16 个标签属合法边界，应全量保留，实际 %#v", got)
		}
	})
}

func TestSafeWindowSourceLabelAndCountBounds(t *testing.T) {
	t.Run("label 长度边界 24", func(t *testing.T) {
		ok24 := ""
		for i := 0; i < 24; i++ {
			ok24 += "a"
		}
		if got := safeWindowSource(map[string]interface{}{ok24: float64(1)}); got == nil {
			t.Error("24 字符 label 属合法边界，不应被丢弃")
		}
		tooLong := ok24 + "a"
		if got := safeWindowSource(map[string]interface{}{tooLong: float64(1)}); got != nil {
			t.Errorf("25 字符 label 应丢弃，实际 %#v", got)
		}
	})

	t.Run("count 边界", func(t *testing.T) {
		cases := []struct {
			name string
			val  interface{}
			keep bool
		}{
			{"0（合法）", float64(0), true},
			{"2^53（合法上界）", float64(1 << 53), true},
			// 注意不能用 float64(1<<53 + 1)：那是无类型整数常量，转 float64
			// 时会被舍入回 2^53 本身，等于合法上界，测不到「超界」。
			// 必须用真正可表示为 >2^53 且落在 int64 内的整数值。
			{"超过 2^53", float64(1<<53) + 1024, false},
			{"负数", float64(-1), false},
			{"小数", float64(1.5), false},
			{"字符串", "3", false},
			{"nil", nil, false},
			{"bool", true, false},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				got := safeWindowSource(map[string]interface{}{"retained": c.val})
				if c.keep && got == nil {
					t.Errorf("%v 应保留，实际被丢弃", c.val)
				}
				if !c.keep && got != nil {
					t.Errorf("%v 应丢弃，实际 %#v", c.val, got)
				}
			})
		}
	})
}
