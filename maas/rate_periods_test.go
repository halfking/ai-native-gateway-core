package maas

import (
	"strings"
	"testing"
	"time"
)

// Wave 3 B1 钉桩测试：峰谷倍率取档规则矩阵（Go 侧 ResolveRateMultiplier 与
// 迁移 736 SQL 函数 maas_resolve_rate_multiplier 逐条同规则）、倍率计费
// 精度/取整、估算表达式乘倍率防退化。

var rateTestCfg = RatePeriodConfig{
	Enabled:  true,
	Timezone: "Asia/Shanghai",
	Periods: []RatePeriod{
		{Name: "peak_am", Start: "08:00", End: "12:00", Multiplier: 3.0},
		{Name: "peak_pm", Start: "17:00", End: "21:00", Multiplier: 3.0},
		{Name: "offpeak_night", Start: "23:00", End: "07:00", Multiplier: 0.7}, // 跨午夜
	},
}

func shanghaiAt(t *testing.T, hour, minute int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	return time.Date(2026, 9, 22, hour, minute, 0, 0, loc)
}

func TestResolveRateMultiplier_RuleMatrix(t *testing.T) {
	cases := []struct {
		hour, minute int
		want         float64
		note         string
	}{
		{8, 0, 3.0, "start boundary inclusive ([08:00,12:00))"},
		{9, 30, 3.0, "mid peak_am"},
		{11, 59, 3.0, "last minute of peak_am"},
		{12, 0, 1.0, "end boundary excluded → standard"},
		{14, 0, 1.0, "no period → standard"},
		{17, 0, 3.0, "peak_pm start"},
		{20, 59, 3.0, "peak_pm last minute"},
		{23, 0, 0.7, "cross-midnight offpeak start"},
		{1, 0, 0.7, "cross-midnight offpeak after midnight"},
		{6, 59, 0.7, "cross-midnight offpeak last minute"},
		{7, 0, 1.0, "cross-midnight end excluded"},
	}
	for _, tc := range cases {
		if got := ResolveRateMultiplier(rateTestCfg, shanghaiAt(t, tc.hour, tc.minute)); got != tc.want {
			t.Errorf("%02d:%02d (%s): got %v want %v", tc.hour, tc.minute, tc.note, got, tc.want)
		}
	}
}

func TestResolveRateMultiplier_DisabledAndDegenerate(t *testing.T) {
	if got := ResolveRateMultiplier(RatePeriodConfig{}, shanghaiAt(t, 9, 0)); got != 1.0 {
		t.Fatalf("empty config must resolve 1.0, got %v", got)
	}
	disabled := rateTestCfg
	disabled.Enabled = false
	if got := ResolveRateMultiplier(disabled, shanghaiAt(t, 9, 0)); got != 1.0 {
		t.Fatalf("disabled config must resolve 1.0, got %v", got)
	}
	broken := RatePeriodConfig{Enabled: true, Timezone: "Asia/Shanghai", Periods: []RatePeriod{
		{Name: "bad_time", Start: "25:00", End: "99:99", Multiplier: 3.0},
		{Name: "bad_mult", Start: "09:00", End: "10:00", Multiplier: -2},
	}}
	if got := ResolveRateMultiplier(broken, shanghaiAt(t, 9, 30)); got != 1.0 {
		t.Fatalf("broken periods must skip to 1.0, got %v", got)
	}
	badTZ := rateTestCfg
	badTZ.Timezone = "Mars/Olympus"
	if got := ResolveRateMultiplier(badTZ, shanghaiAt(t, 9, 0)); got != 3.0 {
		t.Fatalf("bad timezone must fall back to Asia/Shanghai, got %v", got)
	}
}

func TestParseRatePeriodConfig_StringifiedForm(t *testing.T) {
	// The admin TypeString channel persists the value as a JSON string;
	// both shapes must decode.
	obj := []byte(`{"enabled":true,"timezone":"UTC","periods":[{"name":"p","start":"00:00","end":"24:00","multiplier":2}]}`)
	direct := ParseRatePeriodConfig(obj)
	if !direct.Enabled || len(direct.Periods) != 1 {
		t.Fatalf("object form unparsable: %+v", direct)
	}
	wrapped := []byte(`"{\"enabled\":true,\"timezone\":\"UTC\",\"periods\":[{\"name\":\"p\",\"start\":\"00:00\",\"end\":\"24:00\",\"multiplier\":2}]}"`)
	viaString := ParseRatePeriodConfig(wrapped)
	if !viaString.Enabled || len(viaString.Periods) != 1 {
		t.Fatalf("stringified form unparsable: %+v", viaString)
	}
	broken := ParseRatePeriodConfig([]byte("{not json"))
	if broken.Enabled {
		t.Fatal("broken JSON must decode to disabled config")
	}
	// Default timezone fills in.
	if got := ParseRatePeriodConfig([]byte(`{"enabled":true}`)).Timezone; got != "Asia/Shanghai" {
		t.Fatalf("default timezone = %q", got)
	}
}

func TestCalcCreditsMultimodalWithMultiplier(t *testing.T) {
	// In = 1 credit per 1M tokens, so numer-in-credits equals the raw
	// token count divided by 1M.
	rates := ModelRateValues{In: 1}

	oneMillion := TokenUsage{PromptTokens: 1_000_000}
	if got := CalcCreditsMultimodalWithMultiplier(oneMillion, rates, 1.0); got != 1 {
		t.Fatalf("1.0x: got %d want 1", got)
	}
	if got := CalcCreditsMultimodalWithMultiplier(oneMillion, rates, 3.0); got != 3 {
		t.Fatalf("3x: got %d want 3", got)
	}
	// 0.7x with a 3-credit base: 3 × 0.7 = 2.1 → ceil 3 (rounding happens
	// once, AFTER the multiplier; a per-rate round would give 2).
	threeMillion := TokenUsage{PromptTokens: 3_000_000}
	if got := CalcCreditsMultimodalWithMultiplier(threeMillion, rates, 0.7); got != 3 {
		t.Fatalf("0.7x: got %d want 3", got)
	}
	// 1_428_572 tokens × 0.7 / 1e6 = 1.0000004 → ceil 2.
	justUnder := TokenUsage{PromptTokens: 1_428_572}
	if got := CalcCreditsMultimodalWithMultiplier(justUnder, rates, 0.7); got != 2 {
		t.Fatalf("round-after-multiplier: got %d want 2", got)
	}
	// A billable request must never discount to zero.
	if got := CalcCreditsMultimodalWithMultiplier(oneMillion, rates, 0.01); got != 1 {
		t.Fatalf("floor-before-ceil: got %d want 1", got)
	}
	// Degenerate multipliers clamp to 1.0.
	for _, m := range []float64{0, -1} {
		if got := CalcCreditsMultimodalWithMultiplier(oneMillion, rates, m); got != 1 {
			t.Fatalf("multiplier %v must clamp to 1.0: got %d want 1", m, got)
		}
	}
	// Empty usage still 0.
	if got := CalcCreditsMultimodalWithMultiplier(TokenUsage{}, rates, 3.0); got != 0 {
		t.Fatalf("empty usage must be 0, got %d", got)
	}
	// Legacy path equivalence at 1.0.
	multi := TokenUsage{PromptTokens: 800_000, CompletionTokens: 400_000, CacheReadTokens: 100_000}
	if CalcCreditsMultimodal(multi, rates) != CalcCreditsMultimodalWithMultiplier(multi, rates, 1.0) {
		t.Fatal("1.0x path must stay byte-identical to the legacy function")
	}
}

func TestRequestLogCreditsSQL_FoldsRateMultiplier(t *testing.T) {
	sql := RequestLogCreditsSQL("rl", true)
	if !strings.Contains(sql, "COALESCE(rl.credits_rate_multiplier, 1.0)") {
		t.Fatal("estimate expression must fold COALESCE(credits_rate_multiplier, 1.0)")
	}
	if !strings.Contains(sql, "credits_charged") {
		t.Fatal("estimate must still prefer persisted credits_charged")
	}
}
