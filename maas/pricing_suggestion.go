package maas

// pricing_suggestion.go —— 定价建议引擎（2026-11-17 定价对比完善轮）。
//
// 需求原文（owner 2026-11-17）：
//   「在进行销售定价时，可以使用复制某个 MaaS 的价格或平均价格，或最低价格等
//     策略，快速地更新本地的销售价格，简化价格的操作。也可以将本地模型的
//     价格与采集的 maas 中的模型价格进行对比，根据策略给出建议方案，可以
//     一键执行。并且要有价格变更的历史，用于审计分析。」
//
// 数据坐标（与 maas/market_price_reference.go 的五价坐标系同源）：
//   · 市场侧：market_price_platform_quotes（970，**按平台**的现价明细，
//     原生币 + CNY 折算列）＋ market_price_reference（940，双地板 + 原厂锚）；
//   · 我方侧：model_credit_rates（手工价）→ 全局有效价（maas/rates.go 同源
//     语义：manual>0 优先，否则全局基准）；
//   · 换算：credits ⇄ 钱 走 maas_settings.cents_per_credit（分/credit）
//     ⇒ CNY = credits × cents_per_credit / 100；反算 credits = CNY×100/cents。
//
// ★ 红线（与 940 同步器红线同源，但语义不同，必须分清）：
//   · **自动**同步（scripts/market-price-sync.py）**禁止**写计费面 —— 行情价
//     不得自动回流路由/计费；
//   · 本文件提供的是**运营显式动作**（「预览建议 → 点一键执行」）⇒ 写的是
//     我方**零售价**（model_credit_rates 的 manual 覆盖），不碰供应商实付价、
//     不碰路由权重、也不改任何自动路径；
//   · 每次写入都经 upsertModelRateSource ⇒ **期次账 model_price_versions
//     自动留痕**（source=market_suggestion + note 带依据与批次 id）
//     ⇒ 审计面唯一，不另开第二套历史（975 迁移）。
//
// ★ NULL≠0 沿用：没有可比行情（该模型没有任何平台现价 / 缺 CNY 折算列 /
//   计费币种与汇率口径不成立）⇒ 建议价**不产出**（nil），不是 0，也不猜。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// 定价策略闭集（与 975 迁移的 CHECK 同源）。
const (
	// PricingStrategyCopyPlatform：复制某个平台的现价（用户显式指定平台）。
	PricingStrategyCopyPlatform = "copy_platform"
	// PricingStrategyAverage：多平台平均价（原厂行**不参与** —— 它是基准锚
	// 不是竞品；口径与 salesdecorate 的 Compare 极值统计同源）。
	PricingStrategyAverage = "average"
	// PricingStrategyMin：多平台最低价（同上，原厂行不参与）。
	PricingStrategyMin = "min"
	// PricingStrategyFloorMarkup：以市场地板为成本基准，按加成率算建议售价
	//   （成本 → 售价，salesdecorate supply_card 的毛利口径：
	//   建议售价 = 地板价 × (1 + 加成率)）。
	PricingStrategyFloorMarkup = "floor_markup"
)

// RateSourceMarketSuggestion —— 期次账的来源标签（975 迁移放开的闭集值）。
const RateSourceMarketSuggestion = "market_suggestion"

// PricingSuggestionRequest —— 定价建议的入参（预览与执行共用同一份口径）。
type PricingSuggestionRequest struct {
	Strategy string `json:"strategy"`
	// PlatformID：copy_platform 必填（复制的目标平台）。
	PlatformID string `json:"platform_id"`
	// Dimension：行情维度（standard / promo / batch …），默认 standard。
	Dimension string `json:"dimension"`
	// MarkupPct：floor_markup 的加成率（百分比，如 20 = 加 20%）。
	MarkupPct *float64 `json:"markup_pct"`
	// CanonicalIDs：限定模型范围（空 = 全部有可比行情的模型）。
	CanonicalIDs []int64 `json:"canonical_ids"`
	// MinDeltaPct：只对「与当前零售价差异 ≥ 该百分比」的行给出建议/执行
	//   （默认 0 = 全部）。用于「只动明显偏离的」这种保守打法。
	MinDeltaPct *float64 `json:"min_delta_pct"`
	// Operator：操作者标识（审计用；由 admin 层从登录态取，不信前端传参）。
	Operator string `json:"-"`
	// Confirm：执行时必须为 true（预览不需要）⇒ 防误触。
	Confirm bool `json:"confirm"`
}

// PricingSuggestionRow —— 一条建议。
type PricingSuggestionRow struct {
	CanonicalID   int64  `json:"canonical_id"`
	CanonicalName string `json:"canonical_name"`
	DisplayName   string `json:"display_name"`
	// 当前零售价（credits 与 CNY 两套表达）。
	//
	// ★ 口径（2026-11-17 修正）：**只表示「这个模型自己」的零售价**
	//   （model_credit_rates 有值）；nil = 该模型没有自己的价 ⇒ 前端显示「—」。
	//   **不再回落全局基准** —— 全局基准是所有模型统一兜底，不是这个模型的价，
	//   拿它当现价会算出假偏离（211 实测 aion-2.0：兜底 0.0492 CNY vs 市场
	//   5.34 CNY ⇒ delta 10759%），还会污染「只看明显偏离的」阈值过滤。
	CurrentCreditsIn  *int64   `json:"current_credits_in_per_1m"`
	CurrentCreditsOut *int64   `json:"current_credits_out_per_1m"`
	CurrentCNYIn      *float64 `json:"current_cny_in_per_1m"`
	CurrentCNYOut     *float64 `json:"current_cny_out_per_1m"`
	// CurrentBasis：现价来源（审计/前端口径）：
	//   explicit = 输入输出两轴都有**模型自有价**；
	//   partial  = 只有一轴有自有价；
	//   none     = 两轴都没有自有价（现价栏显示「—」，幅度不产出）。
	CurrentBasis string `json:"current_basis"`
	// 建议价（nil = 无可比行情 ⇒ 不产出；NULL≠0）。
	SuggestedCreditsIn  *int64   `json:"suggested_credits_in_per_1m"`
	SuggestedCreditsOut *int64   `json:"suggested_credits_out_per_1m"`
	SuggestedCNYIn      *float64 `json:"suggested_cny_in_per_1m"`
	SuggestedCNYOut     *float64 `json:"suggested_cny_out_per_1m"`
	// 建议幅度（相对当前零售价；nil = 当前价缺失或建议价缺失）。
	DeltaPctIn  *float64 `json:"delta_pct_in"`
	DeltaPctOut *float64 `json:"delta_pct_out"`
	// 依据（审计面）：策略命中了哪些平台、样本数、行情日期、采用的币种。
	Basis           string `json:"basis"`
	BasisPlatforms  int    `json:"basis_platforms"`
	BasisPlatformID string `json:"basis_platform_id"`
	RunDate         string `json:"run_date"`
	// Applicable：是否可执行（无建议 / 无变化 ⇒ 不可执行）。
	Applicable bool   `json:"applicable"`
	SkipReason string `json:"skip_reason"`
}

// PricingSuggestionResult —— 预览/执行的结果。
type PricingSuggestionResult struct {
	Strategy   string                 `json:"strategy"`
	PlatformID string                 `json:"platform_id"`
	Dimension  string                 `json:"dimension"`
	MarkupPct  *float64               `json:"markup_pct"`
	Items      []PricingSuggestionRow `json:"items"`
	Summary    PricingSuggestionStats `json:"summary"`
	// BatchID：执行时写入的批次留痕 id（预览为 0）。
	BatchID int64 `json:"batch_id"`
	// Warnings：口径层提示（例如「N 个模型没有可比行情」）。
	Warnings []string `json:"warnings"`
}

// PricingSuggestionStats —— 汇总读数。
type PricingSuggestionStats struct {
	Models      int `json:"models"`
	WithMarket  int `json:"with_market"`
	WithoutData int `json:"without_market"`
	Changed     int `json:"changed"`
	Applied     int `json:"applied"`
	Skipped     int `json:"skipped"`
}

// PricingSuggestionBatchRow —— 批次留痕的一行（审计读面）。
type PricingSuggestionBatchRow struct {
	ID         int64          `json:"id"`
	CreatedAt  string         `json:"created_at"`
	Strategy   string         `json:"strategy"`
	PlatformID string         `json:"platform_id"`
	Dimension  string         `json:"dimension"`
	MarkupPct  *float64       `json:"markup_pct"`
	Operator   string         `json:"operator"`
	DryRun     bool           `json:"dry_run"`
	Requested  int            `json:"requested"`
	Applied    int            `json:"applied"`
	Skipped    int            `json:"skipped"`
	Detail     map[string]any `json:"detail"`
}

// ── 行情侧取数 ─────────────────────────────────────────────────────────

// pricingSuggestionMarketRow —— 一条「某模型的行情事实」。
type pricingSuggestionMarketRow struct {
	CanonicalName string
	CanonicalID   *int64
	// 各策略用到的市场数值（CNY 口径）。
	PlatformCNYIn  *float64 // copy_platform：目标平台价
	PlatformCNYOut *float64
	AvgCNYIn       *float64 // average
	AvgCNYOut      *float64
	MinCNYIn       *float64 // min
	MinCNYOut      *float64
	FloorCNYIn     *float64 // floor_markup（940 综合地板，恒 CNY）
	FloorCNYOut    *float64
	SampleCount    int
	RunDate        string
}

// pricingSuggestionMarketSQL —— 取数：每个模型的「代表价」按平台聚合。
//
// ★ 代表价口径：同一 (模型, 平台) 若有 tier=”（常规价）行就取它；否则取该
//
//	平台该模型**最便宜**的一档（阶梯价里常有更低的输入价）。
//	⇒ DISTINCT ON (canonical_name, platform_id) + ORDER BY (tier='') DESC,
//	  cny_out NULLS LAST。
//
// ★ 原厂行（is_vendor）：copy_platform 允许（用户显式点名）；
//
//	average / min **排除**（原厂是基准锚，不是竞品 —— 与 salesdecorate 的
//	Compare 极值统计同口径）。
const pricingSuggestionMarketSQL = `
WITH rep AS (
    SELECT DISTINCT ON (q.canonical_name, q.platform_id)
           q.canonical_name, q.platform_id, q.is_vendor,
           q.cny_in_per_1m, q.cny_out_per_1m, q.run_date
      FROM public.market_price_platform_quotes q
     WHERE q.dimension = $1
       AND (q.cny_in_per_1m IS NOT NULL OR q.cny_out_per_1m IS NOT NULL)
     ORDER BY q.canonical_name, q.platform_id, (q.tier = '') DESC,
              q.cny_out_per_1m NULLS LAST
)
SELECT r.canonical_name,
       r.canonical_id,
       r.platform_id,
       r.is_vendor,
       r.cny_in_per_1m,
       r.cny_out_per_1m,
       r.run_date,
       (SELECT count(DISTINCT rep2.platform_id) FROM rep rep2
         WHERE rep2.canonical_name = r.canonical_name AND NOT rep2.is_vendor)
FROM rep r
LEFT JOIN public.market_price_reference mpr
       ON mpr.dimension = $1 AND lower(mpr.canonical_name) = lower(r.canonical_name)
WHERE ($3 = '' OR r.canonical_name = ANY($4))
`

// pricingSuggestionFloorSQL —— floor_markup 用的综合地板（940，恒 CNY）。
const pricingSuggestionFloorSQL = `
SELECT m.canonical_name, m.canonical_id,
       m.market_min_cny_in_per_1m, m.market_min_cny_out_per_1m,
       m.market_min_platform_count, m.source_run_date::text
  FROM public.market_price_reference m
 WHERE m.dimension = $1
   AND ($2 = '' OR lower(m.canonical_name) = ANY($3))
`

// ── 建议计算 ───────────────────────────────────────────────────────────

// cnyToCredits 把 CNY/1M 价换算成 credits/1M（取整，至少 1 credit）。
// ★ 口径：credits = CNY × 100 / cents_per_credit（cents_per_credit = 分/credit）。
//
//	⇒ 反算与 maas/market_price_reference.go 的「credits × cents/100 = CNY」逐字同源。
//
// ★ 至少 1 credit：0 credit 会被当成「免费」⇒ 与 NULL≠0 红线冲突 ⇒ 夹到 1。
func cnyToCredits(cny *float64, centsPerCredit float64) *int64 {
	if cny == nil || centsPerCredit <= 0 {
		return nil
	}
	v := int64(math.Round(*cny * 100.0 / centsPerCredit))
	if v < 1 {
		v = 1
	}
	return &v
}

// applyMarkup 成本 → 建议售价：售价 = 成本 × (1 + 加成率/100)。
// ★ 加成率 ≤ -100 ⇒ 负价 ⇒ 不产出（不猜、不夹）。
func applyMarkup(cost *float64, markupPct float64) *float64 {
	if cost == nil {
		return nil
	}
	if markupPct <= -100 {
		return nil
	}
	v := *cost * (1 + markupPct/100.0)
	if v <= 0 {
		return nil
	}
	return &v
}

// pricingSuggestionStrategyPick 从行情事实里挑出该策略要用的价格。
func pricingSuggestionStrategyPick(row pricingSuggestionMarketRow, req PricingSuggestionRequest) (cnyIn, cnyOut *float64, basis string, platforms int) {
	switch req.Strategy {
	case PricingStrategyCopyPlatform:
		return row.PlatformCNYIn, row.PlatformCNYOut,
			"copy_platform:" + req.PlatformID, 1
	case PricingStrategyAverage:
		return row.AvgCNYIn, row.AvgCNYOut, "average:non-vendor", row.SampleCount
	case PricingStrategyMin:
		return row.MinCNYIn, row.MinCNYOut, "min:non-vendor", row.SampleCount
	case PricingStrategyFloorMarkup:
		markup := 0.0
		if req.MarkupPct != nil {
			markup = *req.MarkupPct
		}
		return applyMarkup(row.FloorCNYIn, markup), applyMarkup(row.FloorCNYOut, markup),
			fmt.Sprintf("floor_markup:%+.2f%%", markup), row.SampleCount
	}
	return nil, nil, "", 0
}

// SuggestPricing —— 预览定价建议（不写任何表）。
func (s *Service) SuggestPricing(ctx context.Context, req PricingSuggestionRequest) (PricingSuggestionResult, error) {
	out := PricingSuggestionResult{}
	if !s.Enabled() {
		return out, errors.New("maas service disabled")
	}
	if err := validatePricingStrategy(req); err != nil {
		return out, err
	}
	dim := req.Dimension
	if dim == "" {
		dim = "standard"
	}
	out.Strategy = req.Strategy
	out.PlatformID = req.PlatformID
	out.Dimension = dim
	out.MarkupPct = req.MarkupPct

	settings, err := s.GetSettings(ctx)
	if err != nil {
		return out, err
	}
	// ★ 换算口径：cents_per_credit 缺失 ⇒ 无法把 CNY 价折成 credits ⇒
	//   不产出任何建议（NULL≠0，不猜）。
	if settings.CentsPerCredit <= 0 {
		out.Warnings = append(out.Warnings,
			"maas_settings.cents_per_credit 未设置 ⇒ 无法把行情价换算成本地 credits，本次不产出建议")
		return out, nil
	}
	// ★ 计费币种：**只有 CNY 口径的行情价能直接用**。
	//   非 CNY 计费（USD 等）时，本轮**不产出**建议 —— 这与仓内既有口径一致
	//   （maas/market_price_reference.go：「零售溢价 = 我方零售 CNY / 市场最低 CNY
	//   （仅 currency_display=CNY 时成立）」⇒ 币种口径不成立时**不出数**，
	//   不是折算成 0，也不是拿第二把汇率硬折）。
	//   ★ 若将来要支持非 CNY 计费：唯一允许的汇率来源是 **940 快照 fx_rate**
	//     （FX 单源红线），缺失则同样不产出 ⇒ 不得引入第二把汇率。
	//   ★ 211 生产现值 = CNY（maas_settings.currency_display）⇒ 该分支休眠。
	fxRate := 1.0
	if settings.CurrencyDisplay != "CNY" {
		fxRate = 0
	}

	// ★ 2026-11-18 12h 审计轮根修（P2）：作用域查询的错误必须上抛，不能当
	//   「作用域为空」继续走（空作用域会让预览返回 200 + 0 个模型）。
	names, err := s.pricingSuggestionScope(ctx, req, dim)
	if err != nil {
		return out, err
	}

	// 行情事实
	market, err := s.loadPricingSuggestionMarket(ctx, dim, req, names)
	if err != nil {
		return out, err
	}
	// 我方现价
	current, err := s.loadPricingSuggestionCurrent(ctx, names)
	if err != nil {
		return out, err
	}

	items := make([]PricingSuggestionRow, 0, len(names))
	for _, name := range names {
		it := PricingSuggestionRow{CanonicalName: name}
		if m, ok := market[name]; ok {
			if m.CanonicalID != nil {
				it.CanonicalID = *m.CanonicalID
			}
			it.BasisPlatforms = m.SampleCount
			it.RunDate = m.RunDate
			cnyIn, cnyOut, basis, platforms := pricingSuggestionStrategyPick(m, req)
			it.Basis = basis
			it.BasisPlatformID = req.PlatformID
			if platforms > 0 {
				it.BasisPlatforms = platforms
			}
			if fxRate == 0 {
				// 需要 fx：没有快照汇率 ⇒ 不产出（不猜第二把汇率）。
				it.SkipReason = "缺快照汇率（USD 计费口径）⇒ 不产出建议"
			} else {
				if fxRate != 1 {
					if cnyIn != nil {
						v := *cnyIn / fxRate
						cnyIn = &v
					}
					if cnyOut != nil {
						v := *cnyOut / fxRate
						cnyOut = &v
					}
				}
				it.SuggestedCNYIn = cnyIn
				it.SuggestedCNYOut = cnyOut
				it.SuggestedCreditsIn = cnyToCredits(cnyIn, settings.CentsPerCredit)
				it.SuggestedCreditsOut = cnyToCredits(cnyOut, settings.CentsPerCredit)
			}
		} else {
			it.SkipReason = "没有可比行情（该模型在采集面无现价）"
		}
		// 当前零售价（credits + CNY）
		//
		// ★ 2026-11-17：**只认「这个模型自己」的价**（model_credit_rates 有值）；
		//   没有就显示「—」，**不用全局基准冒充**。
		//   理由（211 线上实测）：全局基准兜底（600×0.82=492 credits=0.0492 CNY）
		//   与市场价（aion-2.0 = 5.34 CNY）相比会算出 10759% 的**假偏离**，
		//   还会污染「只看明显偏离的」阈值过滤 ⇒ 现价与幅度都必须按真实价算。
		curInExplicit, curOutExplicit := false, false
		if c, ok := current[name]; ok {
			curInExplicit, curOutExplicit = c.InExplicit, c.OutExplicit
			pricingSuggestionApplyCurrent(&it, c, settings.CentsPerCredit, settings.CurrencyDisplay)
		}
		// 幅度
		it.DeltaPctIn = pricingDeltaPct(it.CurrentCNYIn, it.SuggestedCNYIn)
		it.DeltaPctOut = pricingDeltaPct(it.CurrentCNYOut, it.SuggestedCNYOut)
		// 可执行性
		applicable, reason := pricingSuggestionDecision(
			it.SuggestedCreditsIn, it.SuggestedCreditsOut,
			curInExplicit || curOutExplicit,
			it.DeltaPctIn, it.DeltaPctOut, req.MinDeltaPct)
		it.Applicable = applicable
		if !applicable && it.SkipReason == "" {
			it.SkipReason = reason
		}
		items = append(items, it)
	}
	out.Items = items
	out.Summary.Models = len(items)
	for _, it := range items {
		if it.SuggestedCreditsIn != nil || it.SuggestedCreditsOut != nil {
			out.Summary.WithMarket++
		} else {
			out.Summary.WithoutData++
		}
		if it.Applicable {
			out.Summary.Changed++
		}
	}
	if out.Summary.WithoutData > 0 {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("%d 个模型没有可比行情 ⇒ 不产出建议（不是 0 价）", out.Summary.WithoutData))
	}
	return out, nil
}

// pricingSuggestionApplyCurrent —— 把「我方现价」写进建议行（纯函数）。
//
// ★ 这是本轮修掉的洞的**落点**：全局基准兜底**不得**进现价栏。
//
//	⇒ 只有该模型**自己**有价（c.InExplicit / c.OutExplicit）才写；
//	  没有 ⇒ 四个现价字段全留 nil ⇒ 前端显示「—」、幅度不产出。
//	⇒ c.In 里即使带着全局基准值（pickCreditRateFlagged 的兜底结果），
//	  在 InExplicit=false 时也**不写** ⇒ 假偏离（如 10759%）无法复活。
//
// ★ 同时产出 CurrentBasis（现价来源）：explicit / partial / none，
//
//	供审计与前端区分「有价」「只有一轴」「没价」。
func pricingSuggestionApplyCurrent(it *PricingSuggestionRow, c pricingSuggestionCurrentPrices, centsPerCredit float64, currencyDisplay string) {
	if c.InExplicit && c.In != nil {
		it.CurrentCreditsIn = c.In
		if currencyDisplay == "CNY" {
			v := float64(*c.In) * centsPerCredit / 100.0
			it.CurrentCNYIn = &v
		}
	}
	if c.OutExplicit && c.Out != nil {
		it.CurrentCreditsOut = c.Out
		if currencyDisplay == "CNY" {
			v := float64(*c.Out) * centsPerCredit / 100.0
			it.CurrentCNYOut = &v
		}
	}
	switch {
	case c.InExplicit && c.OutExplicit:
		it.CurrentBasis = "explicit"
	case c.InExplicit || c.OutExplicit:
		it.CurrentBasis = "partial"
	default:
		it.CurrentBasis = "none"
	}
}

// pricingSuggestionDecision —— 决定一行是否可执行，并给出跳过原因。
//
// ★ 纯函数（不碰 DB、不碰全局状态）⇒ 可被行为门直接喂数字验证。
// ★ 关键语义（2026-11-17 修正）：
//
//	hasOwnCurrent = false（该模型**没有自己的**零售价）
//	  · 阈值 > 0（保守模式）：**不参与** —— 没有偏离可比（现价栏是全局基准，
//	    拿它比市场价会算出假偏离并污染阈值过滤）；
//	  · 阈值 = 0：⇒ **可执行** —— 这正是「给还没定价的模型首次定价」的主场景。
//
//	hasOwnCurrent = true ⇒ 按幅度与阈值判（幅度缺失 ⇒ 无从比较 ⇒ 跳过）。
func pricingSuggestionDecision(
	sugIn, sugOut *int64,
	hasOwnCurrent bool,
	deltaIn, deltaOut *float64,
	minDeltaPct *float64,
) (applicable bool, skipReason string) {
	// 建议价都没产出 ⇒ 没什么可执行的。
	if sugIn == nil && sugOut == nil {
		return false, "建议价不可产出（缺价或口径不成立）"
	}
	threshold := 0.0
	if minDeltaPct != nil {
		threshold = *minDeltaPct
	}
	if !hasOwnCurrent {
		if threshold > 0 {
			return false, "当前无该模型自己的零售价 ⇒ 无偏离可比，保守阈值模式下不参与"
		}
		return true, ""
	}
	// 有自有价，但一个幅度都算不出来 ⇒ 无从比较。
	if deltaIn == nil && deltaOut == nil {
		return false, "无法计算与当前零售价的差异（现价币种口径不成立）"
	}
	if !pricingDeltaMeaningful(deltaIn, deltaOut, minDeltaPct) {
		return false, "与当前零售价差异在阈值内（或无变化）"
	}
	return true, ""
}

// pricingDeltaPct 建议价相对当前价的幅度（%）；任一缺失 ⇒ nil。
func pricingDeltaPct(current, suggested *float64) *float64 {
	if current == nil || suggested == nil || *current == 0 {
		return nil
	}
	v := (*suggested - *current) / *current * 100
	return &v
}

// pricingDeltaMeaningful 幅度是否达到阈值（minDeltaPct，nil 视作 0）。
func pricingDeltaMeaningful(in, out *float64, minDeltaPct *float64) bool {
	threshold := 0.0
	if minDeltaPct != nil {
		threshold = *minDeltaPct
	}
	for _, d := range []*float64{in, out} {
		if d == nil {
			continue
		}
		if math.Abs(*d) >= threshold {
			return true
		}
	}
	return false
}

// validatePricingStrategy 策略入参校验（闭集 + 参数完备性）。
func validatePricingStrategy(req PricingSuggestionRequest) error {
	switch req.Strategy {
	case PricingStrategyCopyPlatform:
		if strings.TrimSpace(req.PlatformID) == "" {
			return errors.New("copy_platform 策略必须指定 platform_id")
		}
	case PricingStrategyAverage, PricingStrategyMin:
	case PricingStrategyFloorMarkup:
		if req.MarkupPct == nil {
			return errors.New("floor_markup 策略必须指定 markup_pct")
		}
		if *req.MarkupPct <= -100 {
			return errors.New("markup_pct 必须大于 -100（否则建议价为负）")
		}
	default:
		return errors.New("未知策略：" + req.Strategy)
	}
	return nil
}
