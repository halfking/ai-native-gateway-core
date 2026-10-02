// 2026-09-28 审计：ClampEffort 把「关闭思考」意图静默反转为 medium。
//
// 证据（修复前）：ClampEffort("disabled", glm-5 档位) == "medium"。
// 客户端要求关闭推理，paramguard 把它"收窄"成中档思考——方向相反且
// 静默发生，paramledger 报告还会显示成一次正常的 clamp。
//
// 本文件钉死"关闭类写法一律解析为最便宜档位、绝不落到中高档"。
package reasonnorm

import "testing"

// 关闭意图的各家写法（跨供应商通用词表）都必须解析到最便宜档位。
func TestClampEffort_DisableIntentResolvesToCheapestNotMedium(t *testing.T) {
	glm5 := []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	// deepseek-v4 家族没有真正的零档，最便宜就是 low。
	deepseekV4 := []string{"low", "high", "max"}

	cases := []struct {
		name      string
		effort    string
		supported []string
		want      string
	}{
		{"disabled on glm", "disabled", glm5, "none"},
		{"off on glm", "off", glm5, "none"},
		{"false on glm", "false", glm5, "none"},
		{"disable on glm", "disable", glm5, "none"},
		{"none already supported", "none", glm5, "none"},
		{"uppercase DISABLED", "DISABLED", glm5, "none"},
		{"padded  disabled  ", "  disabled  ", glm5, "none"},
		{"disabled on deepseek (no zero tier)", "disabled", deepseekV4, "low"},
		{"off on deepseek", "off", deepseekV4, "low"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampEffort(c.effort, c.supported); got != c.want {
				t.Errorf("ClampEffort(%q, %v) = %q, want %q", c.effort, c.supported, got, c.want)
			}
		})
	}
}

// 关闭意图绝不能解析成 medium 或更高的档位——这条是上面那条的
// 不变式表述，防止将来加档位时把最便宜档算错。
func TestClampEffort_DisableIntentNeverRoundsUp(t *testing.T) {
	glm5 := []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	for _, effort := range []string{"disabled", "off", "false", "disable", "no", "0"} {
		got := ClampEffort(effort, glm5)
		if effortIndex(got) > effortIndex("low") {
			t.Errorf("ClampEffort(%q) = %q (tier %d), must not exceed the low tier",
				effort, got, effortIndex(got))
		}
	}
}

// 未知值（不是关闭意图）维持既有行为：就近映射、并列取高。
// 修复不能顺带改变未知值的既有语义，否则会波及 grok/mistral 等既有路径。
func TestClampEffort_UnknownValueKeepsNearestTierBehaviour(t *testing.T) {
	glm5 := []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	// 与修复前逐值对照：未知值仍落到 medium 兜底档（effortIndex 默认 3）。
	for _, effort := range []string{"bogus_value", "x-high", "turbo"} {
		if got := ClampEffort(effort, glm5); got != "medium" {
			t.Errorf("ClampEffort(%q, %v) = %q, want \"medium\" (unchanged nearest-tier fallback)",
				effort, glm5, got)
		}
	}
	// 合法档位原样返回。
	for _, effort := range []string{"low", "high", "max", "none"} {
		if got := ClampEffort(effort, glm5); got != effort {
			t.Errorf("ClampEffort(%q) = %q, want unchanged", effort, got)
		}
	}
}

// 无 effort 词表的模型仍返回 ""（关闭意图也不该凭空造出档位）。
func TestClampEffort_EmptySupportedStillEmptyForDisableIntent(t *testing.T) {
	if got := ClampEffort("disabled", nil); got != "" {
		t.Errorf("ClampEffort(\"disabled\", nil) = %q, want \"\"", got)
	}
}

// cheapestEffort 按规范档位序取最低，不是按 supported 切片顺序。
func TestCheapestEffort_UsesCanonicalTierNotSliceOrder(t *testing.T) {
	// 故意让最高档排在最前：旧的"取 supported[0]"会返回 max。
	if got := cheapestEffort([]string{"max", "high", "low", "none"}); got != "none" {
		t.Errorf("cheapestEffort([max high low none]) = %q, want \"none\"", got)
	}
	if got := cheapestEffort([]string{"low", "high", "max"}); got != "low" {
		t.Errorf("cheapestEffort([low high max]) = %q, want \"low\"", got)
	}
}
