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
	"sort"
	"strings"
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

// 内嵌清单必须能解析，且每一条都**可审计**。
//
// # 这条判据 2026-10-06 被改写：原来它断言「清单必须是空的」
//
// 原断言是 `if len(catalog) != 0 { t.Errorf("…unverified price(s)") }`。
// 它的用意是对的 —— 往计费系统里放未经原厂页面核实的数字比没有价格更糟。
//
// 但它把「每条都经过核实」这个**真不变量**换成了「一条都还没有」这个
// **当时的状态**。后果是它只在 SSOT 永远为空时才成立；而填 SSOT 正是这个
// 包存在的目的。⇒ 一旦填入第一条经核实的基准价，这条门就红，而红的理由
// 写着「每个条目都必须先经原厂页面核对」—— 那会把下一个人引向「删掉这些
// 基准价来修好这条门」。
//
// # 现在钉的是什么
//
//  1. 每条都过 `validate`（`loadBaselineCatalog` 内部逐条调用，缺 source_url /
//     币种 / 非法 fetched_at 一律拒收）—— 这才是「可审计」的机械保证。
//  2. 额外显式复核 source_url / fetched_at / currency / vendor 四个字段非空：
//     validate 是库内契约，而字段非空是**人**读这个文件时需要的前提，两边都查。
//  3. 清单**非空**：一条都没有同样是缺陷（会让对账器每轮无事可对）。
//  4. 逐条打印成清单：SSOT 是权威面，「它现在装了什么」必须在测试输出里
//     可核对，而不是要人去读 JSON。
//  5. 反向：`_example` 那条占位（`gpt-4o`、价格全 0）**不得**混进 models
//     ——它能过 validate（0 不是负数），只有显式拦它。
func TestEmbeddedCatalogIsAuditableAndNotEmpty(t *testing.T) {
	catalog, err := LoadEmbeddedBaselineCatalog()
	if err != nil {
		t.Fatalf("load embedded catalog: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("embedded catalog is empty — supplier-vs-baseline deviation is not computable " +
			"for any model, and the reconciliation worker has nothing to reconcile against. " +
			"An empty SSOT was the honest starting point, not a permanent state.")
	}

	names := make([]string, 0, len(catalog))
	for name, p := range catalog {
		names = append(names, name)
		if strings.TrimSpace(p.SourceURL) == "" {
			t.Errorf("%s: empty source_url — a price without provenance is not auditable", name)
		}
		if strings.TrimSpace(p.Currency) == "" {
			t.Errorf("%s: empty currency — an unknown currency cannot be compared against a "+
				"supplier price, and defaulting it to USD would state a fact the source never said",
				name)
		}
		if strings.TrimSpace(p.Vendor) == "" {
			t.Errorf("%s: empty vendor — the reconciler groups by vendor and a blank one makes "+
				"the corroboration look like a cross-vendor agreement", name)
		}
		if _, err := p.FetchedAtTime(); err != nil {
			t.Errorf("%s: fetched_at: %v", name, err)
		}
	}
	sort.Strings(names)
	t.Logf("embedded SSOT carries %d verified baseline price(s):", len(catalog))
	for _, n := range names {
		p := catalog[n]
		t.Logf("  %-26s %v/%v %v cache-read=%v vendor=%s fetched_at=%s",
			n, derefOrZero(p.InputPer1M), derefOrZero(p.OutputPer1M), p.Currency,
			derefOrZero(p.CacheReadPer1M), p.Vendor, p.FetchedAt)
	}

	// 反向：_example 的占位条目不得混进 models。它**能**过 validate
	//（0 不是负数），所以这条断言不是重复劳动。
	if p, ok := catalog["gpt-4o"]; ok {
		t.Errorf("the _example placeholder (gpt-4o, all-zero prices) leaked into models "+
			"as %v/%v — it would pass validate (0 is not negative), so only this check stops it",
			derefOrZero(p.InputPer1M), derefOrZero(p.OutputPer1M))
	}
}

func derefOrZero(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
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
			// ★ 币种**未知**与币种**不同**是两件事，都得挡（2026-10-05 扫面）。
			//
			// 上一条那类判词之所以成立，靠的是 `obs.Currency != "" &&
			// ssot.Currency != "" && obs.Currency != ssot.Currency` —— **两个非空
			// 条件都在守卫里**。观测源抽不出币种（返回 ""）时整条检查被跳过，
			// 于是这条**币种未知**的观测价被拿去和 USD 基准价算百分比：
			// 3.00 → 2.50 是 -17%，判词写成 drift，reason 里一个字都不提币种。
			//
			// 它比「已知币种不一致」更早发生（抽取失败是常态，不是异常），
			// 也更危险：数字看着完全正常，运营会照着它去谈价。
			// 期望的「正确」表现是价格一字不差时也判不可比 —— 判据刻意**不给**
			// 一个有偏差的价格，否则「因为偏差大才不可比」会读成「偏差小就可比」。
			name: "unknown observation currency is not comparable even when the price agrees",
			ssot: &fresh,
			obs: &PriceObservation{
				InputPer1M: f64(2.50), OutputPer1M: f64(10.00), Currency: "",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictNotComparable,
		},
		{
			// 同一缺陷的另一半：偏差**很大**时也不能判 drift。
			// 数字越大越像一个真结论，所以这一条是上面那条的对照 ——
			// 只钉「不可比」不钉「为什么不可比」的话，改成 drift 也能过上面那条。
			name: "unknown observation currency is not drift however large the gap",
			ssot: &fresh,
			obs: &PriceObservation{
				InputPer1M: f64(99.0), OutputPer1M: f64(99.0), Currency: "",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictNotComparable,
		},
		{
			// 基准侧币种未知：同一道闸的对称面。validate 现在挡住它入库，
			// 但 ReconcileBaselinePrice 收的是内存里的清单，绕得过 validate。
			name: "unknown baseline currency is not comparable",
			ssot: func() *BaselinePrice {
				p := fresh
				p.Currency = ""
				return &p
			}(),
			obs: &PriceObservation{
				InputPer1M: f64(2.50), OutputPer1M: f64(10.00), Currency: "USD",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictNotComparable,
		},
		{
			// ★ 排序是承重的一部分：币种闸门**必须**排在 free-to-paid 之后。
			//
			// 0 在任何币种下都是 0，所以「本该免费、供应商却在收钱」这条与币种
			// 无关，判 drift 是对的、也是最可行动的。若把币种闸门前移，这一行
			// 会降级成 not_comparable —— 台账上「本该免费的东西在收钱」变成
			// 「两个数没在同一种货币里」，运营读到的 actionable 信号消失了，
			// 而两条判词看起来都是"不可比"，台账里再也分不出。
			// 没有这一条的话，把闸门前移是**全绿**的改动。
			name: "free to paid outranks the unknown currency gate",
			ssot: func() *BaselinePrice {
				p := fresh
				p.InputPer1M, p.OutputPer1M = f64(0), f64(0)
				return &p
			}(),
			obs: &PriceObservation{
				InputPer1M: f64(5.0), OutputPer1M: f64(25.0), Currency: "",
				Source: "models.dev", ObservedAt: now,
			},
			want: PriceVerdictDrift,
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
