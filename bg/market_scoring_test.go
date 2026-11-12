package bg

// market_scoring_test.go — 评分纯函数的手算对拍（定稿 M2-T3 验收门）。
//
// # 手算对拍（四模型 + 同区零 FX）
//
// 行情输入取自 211 acc-pg17 真库（2026-11-12 05:5x +08 只读实测，
// dimension='standard'，fx_rate=6.687755 全行同值；绑定形态取真库在役
// 形状 —— 211 上这四只模型只有 USD 绑定，CNY 绑定腿是定稿验收用例的
// 构造形态）。期望值全部**手算**（分数式列在断言旁，独立于实现路径）：
//
//	fx = 6.687755（USD→CNY 单源快照）
//
//	claude-opus-4-8  USD 10/25   海外地板 5/25 (n=1)  国内无  原厂无
//	  C1 NULL；C2 主列 = 30/35 = 0.8571（同区原生零 FX）；
//	  vs_overseas 0.8571；vs_domestic NULL；advantage = 100×clamp(1−35/30,0,1) = 0.00
//	claude-fable-5   USD 15/50   海外地板 10/50 (n=1) 国内无  原厂无
//	  C1 NULL；C2 主列 = 60/65 = 0.9231；vs_overseas 0.9231；advantage 0.00
//	glm-5.1          USD 0.1/0.1 原厂 6/24 CNY        国内地板 6/24 (n=5) 海外无
//	  C3 suspect_symmetric（in==out，真库在役形态）；
//	  C1 = 1 − 0.2/(30 CNY ÷ 6.687755) = 1 − 1.337551/30 = 0.9554（跨币 ×÷fx）；
//	  vs_domestic = (30 ÷ 6.687755)/0.2 = 22.4291；vs_overseas NULL；
//	  advantage（同区海外缺 → 综合地板 30 CNY ÷ fx）= 100×(1−0.2/4.4858103) = 95.54
//	minimax-m2.7     USD 0.30/1.20 与 0.1/0.1   国内地板 2.1/8.4 (n=3) 海外无 原厂无
//	  0.30/1.20：vs_domestic = (10.5 ÷ 6.687755)/1.5 = 1.0467；
//	            advantage = 100×(1−1.5/1.5700336) = 4.46
//	  0.1/0.1：C3 suspect_symmetric；vs_domestic = 7.8502；advantage = 87.26
//	（同区零 FX 用例）minimax-m2.7 CNY 2.5/10（构造，定稿验收用例）
//	  C2 主列 = 10.5/12.5 = 0.84（CNY 绑定 vs 国内地板，原生零 FX）；
//	  vs_domestic = 0.84；advantage = 100×clamp(1−12.5/10.5,0,1) = 0.00；
//	  fx_used = NULL —— 全行零折算（fx_used 语义钉死：NULL=同币零折算）
//
// # 口径守卫（变异验证见交付档）
//
//   - NULL≠0：原厂免费（vendor_list 0/0）⇒ C1 NULL 而非 0/-∞；
//   - FX 单源 fail-closed：需要 fx 而快照缺失 ⇒ 该分量 NULL（绝不猜第二把汇率）；
//   - unknown_currency：非 USD/CNY 绑定 ⇒ C1/C2 全 NULL；
//   - thin coverage：platform_count=1 的读数照写（降级参考是消费方/告警的事）；
//   - fx_used：任一分量折算过 ⇒ 记录快照汇率；全行同币 ⇒ NULL。

import (
	"math"
	"testing"
)

func scoreF64(v float64) *float64 { return &v }

func approxEq(a, b *float64, tol float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) <= tol
}

func expectF64(t *testing.T, name string, got, want *float64, tol float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s: want NULL, got %v", name, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s: want %v, got NULL", name, *want)
	}
	if math.Abs(*got-*want) > tol {
		t.Fatalf("%s: want %v, got %v (tol %v)", name, *want, *got, tol)
	}
}

// refFixture 返回 211 真库 2026-11-12 实测的行情行（dimension='standard'）。
func refFixture(canonical string) *marketScoringBinding {
	const fx = 6.687755
	b := &marketScoringBinding{SupplierCur: "USD", FXRate: scoreF64(fx)}
	switch canonical {
	case "claude-opus-4-8":
		b.CanonicalName = canonical
		b.SupplierIn, b.SupplierOut = scoreF64(10), scoreF64(25)
		b.OverseasIn, b.OverseasOut, b.OverseasID, b.OverseasCount = scoreF64(5), scoreF64(25), "openrouter", 1
		b.ComprehensiveIn, b.ComprehensiveOut = scoreF64(33.438775), scoreF64(167.193875)
	case "claude-fable-5":
		b.CanonicalName = canonical
		b.SupplierIn, b.SupplierOut = scoreF64(15), scoreF64(50)
		b.OverseasIn, b.OverseasOut, b.OverseasID, b.OverseasCount = scoreF64(10), scoreF64(50), "openrouter", 1
		b.ComprehensiveIn, b.ComprehensiveOut = scoreF64(66.877550), scoreF64(334.387750)
	case "glm-5.1":
		b.CanonicalName = canonical
		b.SupplierIn, b.SupplierOut = scoreF64(0.1), scoreF64(0.1)
		b.HasRef = true
		b.VendorListIn, b.VendorListOut, b.VendorListCur = scoreF64(6), scoreF64(24), "CNY"
		b.DomesticIn, b.DomesticOut, b.DomesticID, b.DomesticCount = scoreF64(6), scoreF64(24), "zhipu", 5
		b.ComprehensiveIn, b.ComprehensiveOut = scoreF64(6), scoreF64(24)
	case "minimax-m2.7":
		b.CanonicalName = canonical
		b.DomesticIn, b.DomesticOut, b.DomesticID, b.DomesticCount = scoreF64(2.1), scoreF64(8.4), "ppio", 3
		b.ComprehensiveIn, b.ComprehensiveOut = scoreF64(2.1), scoreF64(8.4)
	default:
		panic("unknown fixture model " + canonical)
	}
	return b
}

func TestMarketScoringHandCalcFourModels(t *testing.T) {
	t.Run("claude-opus-4-8 USD 10/25 vs overseas 5/25", func(t *testing.T) {
		b := refFixture("claude-opus-4-8")
		b.PriceConfidence = "trusted"
		r, st := scoreSupplierPosition(b)
		if st.VendorAbsent != 1 || st.FXFoldedRows != 0 || st.FXMissingFolds != 0 {
			t.Fatalf("stats: %+v", st)
		}
		expectF64(t, "C1 discount_vs_vendor", r.DiscountVsVendor, nil, 0)
		if r.VendorRefCur != "" {
			t.Fatalf("vendor_ref_currency: want '', got %q", r.VendorRefCur)
		}
		// C2 主列 = 绑定同区（海外）地板，原生零 FX：30/35 = 0.8571
		expectF64(t, "market_position", r.MarketPosition, scoreF64(0.8571), 0)
		expectF64(t, "market_ref_in", r.MarketRefIn, scoreF64(5), 0)
		expectF64(t, "market_ref_out", r.MarketRefOut, scoreF64(25), 0)
		if r.MarketRefID != "openrouter" || r.MarketRefCount != 1 || r.MarketRefUsed != "overseas" {
			t.Fatalf("ref meta: %+v", r)
		}
		// 双读数：同区原生 0.8571；异区地板缺失 NULL
		expectF64(t, "vs_overseas", r.VsOverseas, scoreF64(0.8571), 0)
		expectF64(t, "vs_domestic", r.VsDomestic, nil, 0)
		// advantage = 100×clamp(1−35/30,0,1) = 0（比地板贵 ⇒ 中性 0）
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(0), 0)
		expectF64(t, "fx_used", r.FXUsed, nil, 0)
	})

	t.Run("claude-fable-5 USD 15/50 vs overseas 10/50", func(t *testing.T) {
		b := refFixture("claude-fable-5")
		b.PriceConfidence = "trusted"
		r, st := scoreSupplierPosition(b)
		if st.VendorAbsent != 1 || st.FXFoldedRows != 0 {
			t.Fatalf("stats: %+v", st)
		}
		// 60/65 = 0.9231（同区原生零 FX）
		expectF64(t, "market_position", r.MarketPosition, scoreF64(0.9231), 0)
		expectF64(t, "vs_overseas", r.VsOverseas, scoreF64(0.9231), 0)
		expectF64(t, "vs_domestic", r.VsDomestic, nil, 0)
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(0), 0)
		expectF64(t, "fx_used", r.FXUsed, nil, 0)
	})

	t.Run("glm-5.1 USD 0.1/0.1 suspect_symmetric vs vendor CNY 6/24", func(t *testing.T) {
		b := refFixture("glm-5.1")
		b.PriceConfidence = "suspect_symmetric"
		r, st := scoreSupplierPosition(b)
		// C1 = 1 − 0.2/(30÷6.687755) = 1 − 1.337551/30 = 0.9554（跨币折算）
		expectF64(t, "C1 discount_vs_vendor", r.DiscountVsVendor, scoreF64(0.9554), 1e-4)
		// vendor_ref_* 落原币事实源（不落折算值）
		expectF64(t, "vendor_ref_in", r.VendorRefIn, scoreF64(6), 0)
		expectF64(t, "vendor_ref_out", r.VendorRefOut, scoreF64(24), 0)
		if r.VendorRefCur != "CNY" {
			t.Fatalf("vendor_ref_currency: want CNY, got %q", r.VendorRefCur)
		}
		// C2 主列：USD 绑定 → 海外地板，缺失 ⇒ NULL + used=''
		if r.MarketPosition != nil || r.MarketRefUsed != "" || r.MarketRefCount != 0 {
			t.Fatalf("main C2: %+v", r)
		}
		// vs_overseas NULL（地板缺失）；vs_domestic = (30÷6.687755)/0.2 = 22.4291
		expectF64(t, "vs_overseas", r.VsOverseas, nil, 0)
		expectF64(t, "vs_domestic", r.VsDomestic, scoreF64(22.4291), 1e-4)
		// advantage：同区海外缺 → 综合地板 30 CNY ÷ fx → 100×(1−0.2/4.4858103) = 95.54
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(95.54), 1e-2)
		expectF64(t, "fx_used", r.FXUsed, scoreF64(6.687755), 0)
		if st.FXFoldedRows != 1 {
			t.Fatalf("stats: %+v", st)
		}
	})

	t.Run("minimax-m2.7 USD 0.30/1.20 vs domestic 2.1/8.4", func(t *testing.T) {
		b := refFixture("minimax-m2.7")
		b.SupplierIn, b.SupplierOut = scoreF64(0.30), scoreF64(1.20)
		b.PriceConfidence = "trusted"
		r, st := scoreSupplierPosition(b)
		if st.VendorAbsent != 1 {
			t.Fatalf("stats: %+v", st)
		}
		// vs_domestic = (10.5 ÷ 6.687755)/1.5 = 1.0467（跨币折算）
		expectF64(t, "vs_domestic", r.VsDomestic, scoreF64(1.0467), 1e-4)
		expectF64(t, "vs_overseas", r.VsOverseas, nil, 0)
		// advantage = 100×(1−1.5/1.5700336) = 4.46（综合地板折算兜底）
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(4.46), 1e-2)
		expectF64(t, "fx_used", r.FXUsed, scoreF64(6.687755), 0)
	})

	t.Run("minimax-m2.7 USD 0.1/0.1 suspect_symmetric", func(t *testing.T) {
		b := refFixture("minimax-m2.7")
		b.SupplierIn, b.SupplierOut = scoreF64(0.1), scoreF64(0.1)
		b.PriceConfidence = "suspect_symmetric"
		r, _ := scoreSupplierPosition(b)
		// vs_domestic = (10.5 ÷ 6.687755)/0.2 = 7.8502
		expectF64(t, "vs_domestic", r.VsDomestic, scoreF64(7.8502), 1e-4)
		// advantage = 100×(1−0.2/1.5700336) = 87.26
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(87.26), 1e-2)
	})

	t.Run("同区零 FX：minimax CNY 2.5/10 vs ppio 国内地板 2.1/8.4", func(t *testing.T) {
		b := refFixture("minimax-m2.7")
		b.SupplierCur = "CNY"
		b.SupplierIn, b.SupplierOut = scoreF64(2.5), scoreF64(10)
		b.PriceConfidence = "trusted"
		r, st := scoreSupplierPosition(b)
		if st.VendorAbsent != 1 || st.FXFoldedRows != 0 || st.FXMissingFolds != 0 {
			t.Fatalf("stats: %+v", st)
		}
		// C2 主列 = 国内地板原生零 FX：10.5/12.5 = 0.84；used='domestic'
		expectF64(t, "market_position", r.MarketPosition, scoreF64(0.84), 0)
		expectF64(t, "market_ref_in", r.MarketRefIn, scoreF64(2.1), 0)
		expectF64(t, "market_ref_out", r.MarketRefOut, scoreF64(8.4), 0)
		if r.MarketRefID != "ppio" || r.MarketRefCount != 3 || r.MarketRefUsed != "domestic" {
			t.Fatalf("ref meta: %+v", r)
		}
		// vs_domestic 原生零 FX = 0.84；vs_overseas NULL（地板缺失）
		expectF64(t, "vs_domestic", r.VsDomestic, scoreF64(0.84), 0)
		expectF64(t, "vs_overseas", r.VsOverseas, nil, 0)
		// advantage = 100×clamp(1−12.5/10.5,0,1) = 0
		expectF64(t, "market_advantage_score", r.AdvantageScore, scoreF64(0), 0)
		// ★ 全行零折算 ⇒ fx_used NULL（定稿验收用例的判据）
		expectF64(t, "fx_used", r.FXUsed, nil, 0)
	})
}

func TestMarketScoringGuards(t *testing.T) {
	t.Run("NULL≠0：原厂免费（0/0）⇒ C1 NULL", func(t *testing.T) {
		b := refFixture("claude-opus-4-8")
		b.HasRef = true
		b.VendorListIn, b.VendorListOut, b.VendorListCur = scoreF64(0), scoreF64(0), "USD"
		r, st := scoreSupplierPosition(b)
		if r.DiscountVsVendor != nil {
			t.Fatalf("C1 must be NULL for free vendor, got %v", *r.DiscountVsVendor)
		}
		if st.VendorAbsent != 1 {
			t.Fatalf("stats: %+v", st)
		}
	})

	t.Run("FX 单源 fail-closed：fx 缺失 ⇒ 折算分量 NULL（不猜第二把汇率）", func(t *testing.T) {
		b := refFixture("glm-5.1") // USD 绑定 + CNY 原厂 + CNY 地板 + CNY 综合地板
		b.PriceConfidence = "trusted"
		b.FXRate = nil
		r, st := scoreSupplierPosition(b)
		if r.DiscountVsVendor != nil {
			t.Fatalf("C1 must fail-closed NULL without fx, got %v", *r.DiscountVsVendor)
		}
		if r.VsDomestic != nil {
			t.Fatalf("vs_domestic must fail-closed NULL without fx, got %v", *r.VsDomestic)
		}
		if r.AdvantageScore != nil {
			t.Fatalf("advantage must fail-closed NULL without fx, got %v", *r.AdvantageScore)
		}
		expectF64(t, "fx_used", r.FXUsed, nil, 0)
		// C1 + vs_domestic + advantage 三处 fail-closed 计数
		if st.FXMissingFolds != 3 {
			t.Fatalf("FXMissingFolds: want 3, got %d", st.FXMissingFolds)
		}
	})

	t.Run("unknown_currency：非 USD/CNY 绑定 ⇒ C1/C2 全 NULL", func(t *testing.T) {
		b := refFixture("minimax-m2.7")
		b.SupplierCur = "EUR"
		b.SupplierIn, b.SupplierOut = scoreF64(1), scoreF64(2)
		b.PriceConfidence = "unknown_currency"
		r, st := scoreSupplierPosition(b)
		if r.DiscountVsVendor != nil || r.MarketPosition != nil ||
			r.VsOverseas != nil || r.VsDomestic != nil || r.AdvantageScore != nil {
			t.Fatalf("unknown currency must NULL all ratios: %+v", r)
		}
		if r.MarketRefUsed != "" {
			t.Fatalf("market_ref_used: want '', got %q", r.MarketRefUsed)
		}
		if st.UnknownCurrency != 1 {
			t.Fatalf("stats: %+v", st)
		}
	})

	t.Run("thin coverage：count=1 的读数照写（降级参考不吞读数）", func(t *testing.T) {
		b := refFixture("claude-opus-4-8") // overseas n=1
		b.PriceConfidence = "trusted"
		r, _ := scoreSupplierPosition(b)
		if r.MarketPosition == nil || r.MarketRefCount != 1 {
			t.Fatalf("thin-coverage readout must be written with count, got %+v", r)
		}
	})

	t.Run("fx_used 语义：同币零折算 ⇒ NULL；折算过 ⇒ 快照汇率", func(t *testing.T) {
		// 同币零折算（海外地板原生 USD vs USD 绑定）
		b := refFixture("claude-opus-4-8")
		b.PriceConfidence = "trusted"
		r, _ := scoreSupplierPosition(b)
		if r.FXUsed != nil {
			t.Fatalf("zero-fold row must have fx_used NULL, got %v", *r.FXUsed)
		}
		// 折算行（vs_domestic 跨币）
		b2 := refFixture("minimax-m2.7")
		b2.SupplierIn, b2.SupplierOut = scoreF64(0.1), scoreF64(0.2)
		b2.PriceConfidence = "trusted"
		r2, _ := scoreSupplierPosition(b2)
		if r2.FXUsed == nil || *r2.FXUsed != 6.687755 {
			t.Fatalf("folded row must record snapshot fx, got %+v", r2.FXUsed)
		}
	})

	t.Run("roundTo 对齐 PG numeric round（半数远离零）", func(t *testing.T) {
		cases := []struct {
			in   float64
			n    int
			want float64
		}{
			{0.85714285, 4, 0.8571},
			{0.92307692, 4, 0.9231},
			{22.4290513, 4, 22.4291},
			{-0.16666667, 4, -0.1667},
			{95.541497, 2, 95.54},
			{4.460643, 2, 4.46},
		}
		for _, c := range cases {
			if got := roundTo(c.in, c.n); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("roundTo(%v,%d) = %v, want %v", c.in, c.n, got, c.want)
			}
		}
	})
}

func TestParseMarketScoringAt(t *testing.T) {
	h, m, err := ParseMarketScoringAt("03:10")
	if err != nil || h != 3 || m != 10 {
		t.Fatalf("03:10 => %d:%d %v", h, m, err)
	}
	if _, _, err := ParseMarketScoringAt("24:00"); err == nil {
		t.Fatal("24:00 must fail")
	}
	if _, _, err := ParseMarketScoringAt("3"); err == nil {
		t.Fatal("missing minute must fail")
	}
}
