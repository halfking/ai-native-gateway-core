// bg/pricing_baseline_sync_test.go — 基准价清单校验与价差判词
//
// 这组测试守的是两条纪律：
//
//  1. **没有出处的价格不进账本**。不可审计的价格正是现状那张手工表漂移到
//     没人知道的原因。
//  2. **对账只判词，不改价**。这里测的全是纯函数，没有任何一条路径会去写
//     models_canonical 的价格——写价的只有 SyncBaselinePricesToDB，而它的
//     唯一触发条件是清单里有一条出处完整的新条目。
package bg

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func validPrice() BaselinePrice {
	return BaselinePrice{
		InputPer1M: f64(2.50), OutputPer1M: f64(10.00),
		Currency:  "USD",
		Vendor:    "openai",
		Source:    "vendor_pricing_page",
		SourceURL: "https://example.invalid/pricing",
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func TestBaselinePriceValidate(t *testing.T) {
	if err := validPrice().validate("gpt-x"); err != nil {
		t.Fatalf("a complete entry must validate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*BaselinePrice)
	}{
		{"no input price", func(p *BaselinePrice) { p.InputPer1M = nil }},
		{"no output price", func(p *BaselinePrice) { p.OutputPer1M = nil }},
		{"negative price", func(p *BaselinePrice) { p.InputPer1M = f64(-1) }},
		// 关键：缺出处必须拒绝。
		{"no source url", func(p *BaselinePrice) { p.SourceURL = "" }},
		{"blank source url", func(p *BaselinePrice) { p.SourceURL = "   " }},
		{"no fetched_at", func(p *BaselinePrice) { p.FetchedAt = "" }},
		{"garbage fetched_at", func(p *BaselinePrice) { p.FetchedAt = "yesterday" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPrice()
			tc.mutate(&p)
			if err := p.validate("gpt-x"); err == nil {
				t.Error("entry validated but should not have")
			}
		})
	}
}

// 内嵌清单必须能解析，且**不预置任何价格**。
//
// 这条是刻意的：往计费系统里放未经原厂页面核实的数字，比没有价格更糟——
// 没有价格时成本是显式未知的，错价格会一路流进成本核算。清单为空是这个
// 设计的诚实起点，不是遗漏。
func TestEmbeddedCatalogLoadsAndCarriesNoUnverifiedPrices(t *testing.T) {
	catalog, err := LoadEmbeddedBaselineCatalog()
	if err != nil {
		t.Fatalf("load embedded catalog: %v", err)
	}
	if len(catalog) != 0 {
		names := make([]string, 0, len(catalog))
		for n := range catalog {
			names = append(names, n)
		}
		t.Errorf("embedded catalog carries %d unverified price(s): %v — "+
			"每个条目都必须先经原厂页面核对并填 source_url/fetched_at 才会进来",
			len(catalog), names)
	}
}

func TestReconcileVerdicts(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	fresh := validPrice()
	fresh.FetchedAt = now.Add(-24 * time.Hour).Format(time.RFC3339)
	stale := validPrice()
	stale.FetchedAt = now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)

	cases := []struct {
		name string
		ssot *BaselinePrice
		obs  *PriceObservation
		want string
	}{
		{
			name: "no baseline entry at all",
			ssot: nil,
			obs:  &PriceObservation{InputPer1M: f64(2.5), OutputPer1M: f64(10), Currency: "USD"},
			want: PriceVerdictMissing,
		},
		{
			name: "no observation from the machine-readable source",
			ssot: &fresh,
			obs:  nil,
			want: PriceVerdictMissing,
		},
		{
			name: "prices agree within tolerance",
			ssot: &fresh,
			obs: &PriceObservation{
				InputPer1M: f64(2.52), OutputPer1M: f64(9.95), Currency: "USD",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictMatch,
		},
		{
			name: "vendor raised the price",
			ssot: &fresh,
			obs: &PriceObservation{
				InputPer1M: f64(3.00), OutputPer1M: f64(10.00), Currency: "USD",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictDrift,
		},
		{
			// 币种不同：必须显式判不可比，而不是给一个看着像偏差的数字。
			name: "currency mismatch is not comparable",
			ssot: &fresh,
			obs: &PriceObservation{
				InputPer1M: f64(18.0), OutputPer1M: f64(72.0), Currency: "CNY",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictNotComparable,
		},
		{
			// 关键优先级：出处过期 > 漂移。过期来源算出来的偏差本身就是
			// 过期数据的偏差，先修新鲜度再谈数字。
			name: "stale source outranks drift",
			ssot: &stale,
			obs: &PriceObservation{
				InputPer1M: f64(99.0), OutputPer1M: f64(99.0), Currency: "USD",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictStaleSource,
		},
		{
			// 免费模型（0 元）两侧都是 0：分母为 0 ⇒ 不可比，而不是「0% 偏差」。
			name: "free on both sides is not a match",
			ssot: func() *BaselinePrice {
				p := validPrice()
				p.InputPer1M, p.OutputPer1M = f64(0), f64(0)
				return &p
			}(),
			obs: &PriceObservation{
				InputPer1M: f64(0), OutputPer1M: f64(0), Currency: "USD",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictNotComparable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcileBaselinePrice("gpt-x", tc.ssot, tc.obs, now)
			if got.Verdict != tc.want {
				t.Errorf("verdict = %q want %q (detail=%v)", got.Verdict, tc.want, got.Detail)
			}
		})
	}
}

// 判词为 drift 时必须真的算出偏差，供台账与告警使用；不是 drift 时偏差
// 只能是 nil 或在容限内。
func TestReconcileReportsDriftPercentages(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ssot := validPrice()
	ssot.FetchedAt = now.Add(-time.Hour).Format(time.RFC3339)

	got := ReconcileBaselinePrice("gpt-x", &ssot, &PriceObservation{
		// 2.50 → 3.00 = +20%
		InputPer1M: f64(3.00), OutputPer1M: f64(10.00), Currency: "USD",
	}, now)
	if got.Verdict != PriceVerdictDrift {
		t.Fatalf("verdict = %q want drift", got.Verdict)
	}
	if got.InputDriftPct == nil {
		t.Fatal("input drift not reported")
	}
	if d := *got.InputDriftPct; d < 19.9 || d > 20.1 {
		t.Errorf("input drift = %v want ~20", d)
	}
	if got.OutputDriftPct == nil {
		t.Fatal("output drift not reported even though it matched")
	}
	if d := *got.OutputDriftPct; d != 0 {
		t.Errorf("output drift = %v want 0", d)
	}
}

// 容限边界：恰好等于容限不算漂移，超过才算。
func TestDriftToleranceBoundary(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ssot := validPrice()
	ssot.FetchedAt = now.Add(-time.Hour).Format(time.RFC3339)

	atLimit := ReconcileBaselinePrice("m", &ssot, &PriceObservation{
		InputPer1M: f64(2.50 * 1.02), OutputPer1M: f64(10.00), Currency: "USD",
	}, now)
	if atLimit.Verdict != PriceVerdictMatch {
		t.Errorf("exactly at tolerance: verdict = %q want match", atLimit.Verdict)
	}
	overLimit := ReconcileBaselinePrice("m", &ssot, &PriceObservation{
		InputPer1M: f64(2.50 * 1.021), OutputPer1M: f64(10.00), Currency: "USD",
	}, now)
	if overLimit.Verdict != PriceVerdictDrift {
		t.Errorf("just over tolerance: verdict = %q want drift", overLimit.Verdict)
	}
}

func TestBaselineSyncKillSwitch(t *testing.T) {
	t.Setenv(baselineSyncEnvKillSwitch, "0")
	if baselineSyncEnabled() {
		t.Error("kill switch did not disable baseline sync")
	}
	t.Setenv(baselineSyncEnvKillSwitch, "")
	if !baselineSyncEnabled() {
		t.Error("baseline sync should default to enabled (opt-out)")
	}
}
