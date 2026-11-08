package sales

// quote.go —— 报价工作台（2026-11-04 实施规划 W1-T2，决议 A/B/C）。
//
// 状态机（头表 944，转换表 sales_quote_status_log 隐式定义）：
//
//	draft → pending_approval → approved → sent → won | lost
//	                                        └→ (revise) 新版本行，旧版 superseded
//
// 决议 A（版本化）：
//   - draft 内容修改 = **新版本行**（v+1），旧版本回填 superseded_by ——
//     draft 修订与 sent 后修订走同一条「修订即新版」路径，无原地 UPDATE；
//   - pending_approval / approved 期间内容冻结（409）；
//   - Expired 是派生态：读取时 current 版本 valid_until < now() ⇒ expired，
//     从不落库（头状态 CHECK 闭集故意不含 'expired'）；
//   - locked 是正交布尔：pending_approval 置 true，decide（approved/rejected）
//     与 revise 解除。
//
// 决议 B（审批）：三维阈值（毛利下限 / 折扣深度 / 单笔金额）任一触发 ⇒
// 进人审并记 trigger_reasons jsonb（读数+阈值快照，可复算）；三维全不触发
// ⇒ 政策内自动放行（不进人审，直接 approved）——DealHub「只审例外」。
//
// 决议 C（零售价）：行上 rate_in/rate_out 是基础价（积分/1M），
// discount_pct 是正交折扣轴——折扣永不写回基础价；折后价 = ceil(rate×(1-d))
// 只出现在预计算列。FX 单源 settings.CNYToUSDFX。
//
// 毛利预计算（Odoo store=True 范式）：USD 口径，credits → CNY 用
// maas_settings.cents_per_credit（分），CNY → USD 用 FX 快照。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// 状态机取值（与 944 CHECK 闭集逐字对齐；expired 是派生态，故意缺席）。
const (
	StatusDraft           = "draft"
	StatusPendingApproval = "pending_approval"
	StatusApproved        = "approved"
	StatusSent            = "sent"
	StatusWon             = "won"
	StatusLost            = "lost"
	StatusSuperseded      = "superseded"
)

// QuoteItemInput is one line as submitted by the workbench. Monetary inputs
// are float64 from JSON; they are re-emitted into numeric columns by pgx.
type QuoteItemInput struct {
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	EstTokensIn   int64    `json:"est_tokens_in"`
	EstTokensOut  int64    `json:"est_tokens_out"`
	VolumeNote    string   `json:"volume_note"`
	CostInPer1M   *float64 `json:"cost_in_per_1m"`
	CostOutPer1M  *float64 `json:"cost_out_per_1m"`
	CostCurrency  string   `json:"cost_currency"`
	RateIn        *float64 `json:"rate_in"`
	RateOut       *float64 `json:"rate_out"`
	DiscountPct   float64  `json:"discount_pct"`
	Confidence    string   `json:"confidence"`
}

// QuoteInput is the create/revise payload.
type QuoteInput struct {
	DemandCardID *int64           `json:"demand_card_id"`
	CustomerName string           `json:"customer_name"`
	Title        string           `json:"title"`
	Tier         string           `json:"tier"`
	ValidUntil   *time.Time       `json:"valid_until"`
	Items        []QuoteItemInput `json:"items"`
}

// QuoteVersion is one immutable snapshot (read projection of 945).
type QuoteVersion struct {
	ID              int64             `json:"id"`
	QuoteID         int64             `json:"quote_id"`
	VersionNo       int               `json:"version_no"`
	PriceAxes       map[string]any    `json:"price_axes"`
	PriceConfidence string            `json:"price_confidence"`
	Tier            string            `json:"tier"`
	FXRate          float64           `json:"fx_rate"`
	CostTotalUSD    float64           `json:"cost_total_usd"`
	SaleTotalUSD    float64           `json:"sale_total_usd"`
	MarginUSD       float64           `json:"margin_usd"`
	MarginPct       *float64          `json:"margin_pct"`
	ValidUntil      *time.Time        `json:"valid_until"`
	Expired         bool              `json:"expired"` // 派生态：valid_until < now()
	SupersededBy    *int64            `json:"superseded_by,omitempty"`
	CreatedBy       string            `json:"created_by"`
	CreatedAt       time.Time         `json:"created_at"`
	Items           []QuoteItemOutput `json:"items,omitempty"`
}

// QuoteItemOutput is one stored line (946) with its precomputed columns.
type QuoteItemOutput struct {
	ID            int64    `json:"id"`
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	EstTokensIn   int64    `json:"est_tokens_in"`
	EstTokensOut  int64    `json:"est_tokens_out"`
	VolumeNote    string   `json:"volume_note"`
	CostInPer1M   *float64 `json:"cost_in_per_1m"`
	CostOutPer1M  *float64 `json:"cost_out_per_1m"`
	CostCurrency  string   `json:"cost_currency"`
	RateIn        *float64 `json:"rate_in"`
	RateOut       *float64 `json:"rate_out"`
	DiscountPct   float64  `json:"discount_pct"`
	Confidence    string   `json:"confidence"`
	FXRate        float64  `json:"fx_rate"`
	RevenueUSD    float64  `json:"revenue_usd"`
	CostUSD       float64  `json:"cost_usd"`
	MarginUSD     float64  `json:"margin_usd"`
	MarginPct     *float64 `json:"margin_pct"`
}

// Quote is the head + current version projection.
type Quote struct {
	ID               int64         `json:"id"`
	DemandCardID     *int64        `json:"demand_card_id,omitempty"`
	CustomerName     string        `json:"customer_name"`
	Title            string        `json:"title"`
	Status           string        `json:"status"`
	CurrentVersionID *int64        `json:"current_version_id,omitempty"`
	Locked           bool          `json:"locked"`
	CreatedBy        string        `json:"created_by"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Version          *QuoteVersion `json:"version,omitempty"`
	// W2-T2 成本漂移（风暴档 §3.4）：仅活单（draft/pending_approval/approved/
	// sent）计算——版本行快照成本 vs 供给视图当前价的行级最大相对漂移（%）。
	// 超过 sales.quote.cost_drift_alert_pct ⇒ drifted=true。提醒不自动撤：
	// 是否重报由销售/审批决定（「成本已漂移，建议重报」是标注，不是状态迁移）。
	CostDriftPct *float64 `json:"cost_drift_pct,omitempty"`
	CostDrifted  bool     `json:"cost_drifted"`
}

// centsPerCredit reads the billing anchor from maas_settings (row may be
// absent on bare fixtures → 0.01 default mirrors the maas seed).
func (s *Service) centsPerCredit(ctx context.Context) float64 {
	var v float64
	err := s.pool.QueryRow(ctx, `SELECT cents_per_credit::float8 FROM public.maas_settings WHERE id = 1`).Scan(&v)
	if err != nil || v <= 0 {
		return 0.01
	}
	return v
}

// precomputeLines folds Decision C math per line: effective rate = ceil(rate×(1-d)),
// revenue in credits → CNY (cents_per_credit) → USD (FX snapshot); cost folded to
// USD (CNY cost ÷ FX; unknown currency ⇒ cost 0 + confidence unknown_currency).
// ★ 单位语义（AF-1 裁决 A-14，2026-11-05 审计 A1）：cents_per_credit 是
// 「**分**/积分（账单币种的分）」—— credits×cents 得**分**，再 ÷100 折元。
// 三个同语义锚点：maas/consumption_detail.go（revenueBillMajor = credits×cents/100）、
// maas/bills.go AmountCentsFromCredits（credits×cents = AmountCents）、
// 01-schema 价目视图（credits×cents/100.0 = price_cny）。
func precomputeLines(items []QuoteItemInput, fx, centsPerCredit float64) ([]QuoteItemOutput, string) {
	out := make([]QuoteItemOutput, 0, len(items))
	versionConfidence := "trusted"
	for _, in := range items {
		o := QuoteItemOutput{
			CanonicalName: in.CanonicalName,
			DisplayName:   in.DisplayName,
			EstTokensIn:   in.EstTokensIn,
			EstTokensOut:  in.EstTokensOut,
			VolumeNote:    in.VolumeNote,
			CostInPer1M:   in.CostInPer1M,
			CostOutPer1M:  in.CostOutPer1M,
			CostCurrency:  normalizeCostCurrency(in.CostCurrency),
			RateIn:        in.RateIn,
			RateOut:       in.RateOut,
			DiscountPct:   clampDiscount(in.DiscountPct),
			FXRate:        fx,
		}
		// 决议 C：折后价只出现在预计算，基础价轴不动。
		effIn := discounted(derefF(in.RateIn), o.DiscountPct)
		effOut := discounted(derefF(in.RateOut), o.DiscountPct)
		revenueCredits := effIn*float64(in.EstTokensIn)/1e6 + effOut*float64(in.EstTokensOut)/1e6
		// A-14：cents_per_credit = 分/积分 ⇒ ×cents 得分、÷100 折元（A1 修复，
		// 此前漏 ÷100 ⇒ 收入虚大 100×，order_amount 维恒触发、毛利下限维永不触发）。
		revenueCNY := revenueCredits * centsPerCredit / 100
		o.RevenueUSD = revenueCNY / fx

		switch o.CostCurrency {
		case "USD":
			o.CostUSD = derefF(in.CostInPer1M)*float64(in.EstTokensIn)/1e6 +
				derefF(in.CostOutPer1M)*float64(in.EstTokensOut)/1e6
		case "CNY":
			o.CostUSD = (derefF(in.CostInPer1M)*float64(in.EstTokensIn)/1e6 +
				derefF(in.CostOutPer1M)*float64(in.EstTokensOut)/1e6) / fx
		default:
			// 币种未知拒绝，不默认 USD（红线）——成本记 0 但置信档降级，
			// 版本整体不得出正式单。
			o.CostUSD = 0
			o.Confidence = "unknown_currency"
		}
		if o.Confidence == "" {
			o.Confidence = in.Confidence
		}
		o.MarginUSD = o.RevenueUSD - o.CostUSD
		o.MarginPct = marginPctOf(o.MarginUSD, o.RevenueUSD)
		out = append(out, o)

		// 版本级置信档 = 行级最差档（门不是分：一行不可信整单降档）。
		if confidenceRank(o.Confidence) > confidenceRank(versionConfidence) {
			versionConfidence = o.Confidence
		}
	}
	return out, versionConfidence
}

func confidenceRank(c string) int {
	switch c {
	case "trusted":
		return 0
	case "suspect_symmetric":
		return 1
	case "empty":
		return 2
	default: // unknown_currency / unknown
		return 3
	}
}

func normalizeCostCurrency(c string) string {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "CNY":
		return "CNY"
	case "USD", "":
		return "USD"
	default:
		return strings.ToUpper(strings.TrimSpace(c))
	}
}

func clampDiscount(d float64) float64 {
	if d < 0 {
		return 0
	}
	if d > 100 {
		return 100
	}
	return d
}

func discounted(base float64, discountPct float64) float64 {
	if base <= 0 {
		return 0
	}
	return math.Ceil(base * (1 - discountPct/100))
}

// marginPctOf bounds the margin ratio to the 945/946 column range
// (numeric(8,4)). A near-zero revenue makes the naive ratio explode
// (revenue $0.02 vs cost $140 ⇒ -721300% ⇒ SQLSTATE 22003 hard-fails the
// version INSERT — caught by this round's own realdb leg, 2026-11-08):
// clamp instead of exploding; the deeply-negative sign (fail-closed red
// line) survives. Revenue <= 0 ⇒ NULL（无收入无毛利，NULL≠0 红线）。
func marginPctOf(margin, revenue float64) *float64 {
	if revenue <= 0 {
		return nil
	}
	p := margin / revenue * 100
	switch {
	case p > 9999.9999:
		p = 9999.9999
	case p < -9999.9999:
		p = -9999.9999
	}
	return &p
}

func derefF(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// ── 服务端派生（2026-11-08 审计 P1/P5/P6 修复）────────────────────────────
//
// 行成本 / 币种 / 置信档一律服务端从供给视图（941/949）派生，客户端声明
// 不再采信：
//   - P6：C3 门从「客户端断言」变成「服务端事实」——视图四档口径是唯一
//     裁决点，客户端声明 trusted + 任意成本无法再绕过 C3 与毛利三维；
//   - P1：前端此前不传 confidence/cost_currency ⇒ 版本置信 '' 违反 945
//     CHECK ⇒ CreateQuote 必 23514（前端全链路不可用）——派生后该链路闭合；
//   - P5：版本 price_axes（五价坐标系快照）从同一解析行装配，不再恒 '{}'。
//
// 解析规则与 ListSellable 同族：视图按名解析（**任意置信档**——草稿可持
// 不可信行，Submit 的 C3 门拦正式单，风暴档 §4-3 判据）；同名多价整组
// withhold 与 suspended 登记排除；同名多行取 credential_id 最小（确定性，
// 非 withheld 组内同价重复行取哪个都一样）。

// fetchSupplyViewRows reads view rows for the given canonical names (any
// confidence tier) plus the annotation status for the withhold/suspended
// exclusions. Ordered canonical → credential_id → provider_model_id so the
// first row per canonical is the deterministic pick.
func (s *Service) fetchSupplyViewRows(ctx context.Context, names []string) ([]SellableRow, map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
SELECT v.credential_id,
       v.provider_model_id,
       v.raw_model_name,
       v.canonical_name,
       v.provider_name,
       v.supplier_currency,
       v.billing_mode,
       v.supplier_in_per_1m::text,
       v.supplier_out_per_1m::text,
       v.baseline_currency,
       v.baseline_in_per_1m::text,
       v.baseline_out_per_1m::text,
       v.currency_comparable,
       v.has_baseline,
       v.market_ref_currency,
       v.market_ref_in_per_1m::text,
       v.market_ref_out_per_1m::text,
       v.market_ref_platform_id,
       v.market_ref_platform_count,
       v.market_position::text,
       v.price_confidence,
       sa.status
  FROM public.v_supplier_price_vs_baseline v
  LEFT JOIN public.sales_supply_annotations sa
         ON sa.credential_id = v.credential_id
        AND sa.provider_model_id = v.provider_model_id
 WHERE lower(v.canonical_name) = ANY($1)
 ORDER BY v.canonical_name, v.credential_id, v.provider_model_id`, names)
	if err != nil {
		return nil, nil, fmt.Errorf("sales: query supply view: %w", err)
	}
	defer rows.Close()
	out := make([]SellableRow, 0, len(names))
	annStatus := map[string]string{} // "credID:pmID" → status
	for rows.Next() {
		var r SellableRow
		var st *string
		if err := rows.Scan(&r.CredentialID, &r.ProviderModelID, &r.RawModelName,
			&r.CanonicalName, &r.ProviderName, &r.SupplierCurrency, &r.BillingMode,
			&r.SupplierInPer1M, &r.SupplierOutPer1M,
			&r.BaselineCurrency, &r.BaselineInPer1M, &r.BaselineOutPer1M,
			&r.CurrencyComparable, &r.HasBaseline,
			&r.MarketRefCurrency, &r.MarketRefInPer1M, &r.MarketRefOutPer1M,
			&r.MarketRefPlatform, &r.MarketRefPlatforms, &r.MarketPosition,
			&r.PriceConfidence, &st); err != nil {
			return nil, nil, fmt.Errorf("sales: scan supply view: %w", err)
		}
		annStatus[fmt.Sprintf("%d:%d", r.CredentialID, r.ProviderModelID)] = deref(st)
		out = append(out, r)
	}
	return out, annStatus, rows.Err()
}

// withholdNames computes the same-name multi-price groups (整组 withhold 红线)
// over the given rows — the exact variant logic of ListSellable, factored out
// so derivation and the sellable list can never drift apart.
func withholdNames(rows []SellableRow) map[string]bool {
	groups := map[string][]SellableRow{}
	order := make([]string, 0, 16)
	for _, r := range rows {
		if r.CanonicalName == "" {
			continue
		}
		if _, seen := groups[r.CanonicalName]; !seen {
			order = append(order, r.CanonicalName)
		}
		groups[r.CanonicalName] = append(groups[r.CanonicalName], r)
	}
	out := map[string]bool{}
	for _, canon := range order {
		g := groups[canon]
		if len(g) < 2 {
			continue
		}
		var seenPrices [][2]float64
		for _, row := range g {
			inF := parseNum(row.SupplierInPer1M)
			outF := parseNum(row.SupplierOutPer1M)
			dup := false
			for _, p := range seenPrices {
				if p[0] == inF && p[1] == outF {
					dup = true
					break
				}
			}
			if !dup {
				seenPrices = append(seenPrices, [2]float64{inF, outF})
			}
		}
		if len(seenPrices) >= 2 {
			out[canon] = true
		}
	}
	return out
}

// deriveQuoteItems overwrites each item's cost/currency/confidence from the
// supply view and assembles the five-price-axes snapshot (P1 baseline /
// P2 market / P3 supplier / P4 baseline-copy note / P5 retail). Items are
// mutated in place; input cost fields are ignored (server is the only
// authority). Returns an error for items that cannot be priced honestly.
func (s *Service) deriveQuoteItems(ctx context.Context, items []QuoteItemInput, tier string) (map[string]any, error) {
	names := make([]string, 0, len(items))
	seen := map[string]bool{}
	for i := range items {
		n := strings.ToLower(strings.TrimSpace(items[i].CanonicalName))
		if n == "" {
			return nil, errors.New("sales: item canonical_name is required")
		}
		if items[i].EstTokensIn < 0 || items[i].EstTokensOut < 0 {
			return nil, fmt.Errorf("sales: est_tokens must be non-negative (%s)", n)
		}
		if (items[i].RateIn != nil && *items[i].RateIn < 0) ||
			(items[i].RateOut != nil && *items[i].RateOut < 0) {
			return nil, fmt.Errorf("sales: rate must be non-negative (%s)", n)
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("sales: at least one item is required")
	}
	rows, annStatus, err := s.fetchSupplyViewRows(ctx, names)
	if err != nil {
		return nil, err
	}
	withheld := withholdNames(rows)
	pick := map[string]SellableRow{}
	for _, r := range rows { // ordered ⇒ first per canonical is the pick
		if _, ok := pick[r.CanonicalName]; !ok {
			pick[r.CanonicalName] = r
		}
	}
	axesItems := map[string]any{}
	for i := range items {
		n := strings.ToLower(strings.TrimSpace(items[i].CanonicalName))
		row, ok := pick[n]
		if !ok {
			return nil, fmt.Errorf("sales: %q is not in the supply view — no cost basis (register a priced binding first)", items[i].CanonicalName)
		}
		if withheld[row.CanonicalName] {
			return nil, fmt.Errorf("sales: %q is withheld (same-name multi-price) and cannot be quoted", items[i].CanonicalName)
		}
		if annStatus[fmt.Sprintf("%d:%d", row.CredentialID, row.ProviderModelID)] == "suspended" {
			return nil, fmt.Errorf("sales: %q binding annotation is suspended", items[i].CanonicalName)
		}
		items[i].CanonicalName = row.CanonicalName
		items[i].CostInPer1M = textPtrToF(row.SupplierInPer1M)
		items[i].CostOutPer1M = textPtrToF(row.SupplierOutPer1M)
		items[i].CostCurrency = normalizeCostCurrency(row.SupplierCurrency)
		items[i].Confidence = row.PriceConfidence
		axesItems[row.CanonicalName] = map[string]any{
			"p1_baseline": map[string]any{
				"in":       textPtrToAny(row.BaselineInPer1M),
				"out":      textPtrToAny(row.BaselineOutPer1M),
				"currency": textPtrToAny(row.BaselineCurrency),
				"has":      row.HasBaseline,
			},
			"p2_market": map[string]any{
				"in":       textPtrToAny(row.MarketRefInPer1M),
				"out":      textPtrToAny(row.MarketRefOutPer1M),
				"currency": textPtrToAny(row.MarketRefCurrency),
				"platform": textPtrToAny(row.MarketRefPlatform),
				"count":    row.MarketRefPlatforms, // nil = 无市场信号（NULL≠0）
				"position": textPtrToAny(row.MarketPosition),
			},
			"p3_supplier": map[string]any{
				"in":               textPtrToAny(row.SupplierInPer1M),
				"out":              textPtrToAny(row.SupplierOutPer1M),
				"currency":         normalizeCostCurrency(row.SupplierCurrency),
				"credential_id":    row.CredentialID,
				"provider_model_id": row.ProviderModelID,
				"provider":         row.ProviderName,
			},
			"p5_retail": map[string]any{
				"in":           items[i].RateIn,
				"out":          items[i].RateOut,
				"discount_pct": clampDiscount(items[i].DiscountPct),
			},
		}
	}
	// P4 不是第四个数据源：826 明写 baseline_price_vendor 是「基准价所引用的
	// 厂商」，P4 = P1 的网关侧副本（第 42 增量五价坐标系结论），快照里以
	// 说明位登记而不是重复列。
	return map[string]any{
		"items": axesItems,
		"tier":  normalizeTier(tier),
		"fx":    settings.CNYToUSDFX,
		"note":  "P4 baseline == P1 vendor list copy (826 single source); snapshot at version creation",
	}, nil
}

func textPtrToF(s *string) *float64 {
	if s == nil {
		return nil
	}
	f := parseNum(s)
	return &f
}

// textPtrToAny keeps NULL as absent (nil) and re-emits exact numeric text as
// a number for the jsonb axes snapshot.
func textPtrToAny(s *string) any {
	if s == nil {
		return nil
	}
	return parseNum(s)
}

// QuoteVersionInput carries the materialized lines + totals into the INSERT.
type quoteVersionWrite struct {
	items      []QuoteItemOutput
	confidence string
	axes       map[string]any // 五价坐标系快照（决议 A）；nil 落 '{}'
	costTotal  float64
	saleTotal  float64
	marginUSD  float64
	marginPct  *float64
	validUntil *time.Time
}

// insertQuoteVersion writes the immutable version row + items + status log,
// moving the head pointer inside one transaction. statusNow is the head
// status at materialization time (the audit row records it as both from and
// to — a version materialization is not a state transition; the previous
// '' / '' rows diluted the transition-table semantics, 2026-11-08 audit).
func (s *Service) insertQuoteVersion(ctx context.Context, tx pgx.Tx, quoteID int64, versionNo int, in QuoteInput, w quoteVersionWrite, actor, statusNow string) (int64, error) {
	fx := settings.CNYToUSDFX
	axes := w.axes
	if axes == nil {
		axes = map[string]any{}
	}
	var versionID int64
	var marginPct any
	if w.marginPct != nil {
		marginPct = *w.marginPct
	}
	err := tx.QueryRow(ctx, `
INSERT INTO public.sales_quote_versions
    (quote_id, version_no, price_axes, price_confidence, tier, fx_rate,
     cost_total_usd, sale_total_usd, margin_usd, margin_pct, valid_until, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::timestamptz,$12)
RETURNING id`,
		quoteID, versionNo, axes, w.confidence, normalizeTier(in.Tier), fx,
		w.costTotal, w.saleTotal, w.marginUSD, marginPct,
		validUntilText(w.validUntil), actor,
	).Scan(&versionID)
	if err != nil {
		return 0, fmt.Errorf("sales: insert quote version: %w", err)
	}
	for i := range w.items {
		it := w.items[i]
		if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_items
    (version_id, canonical_name, display_name, est_tokens_in, est_tokens_out,
     volume_note, cost_in_per_1m, cost_out_per_1m, cost_currency,
     rate_in, rate_out, discount_pct, confidence, fx_rate,
     revenue_usd, cost_usd, margin_usd, margin_pct)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			versionID, it.CanonicalName, it.DisplayName, it.EstTokensIn, it.EstTokensOut,
			it.VolumeNote, it.CostInPer1M, it.CostOutPer1M, it.CostCurrency,
			it.RateIn, it.RateOut, it.DiscountPct, it.Confidence, it.FXRate,
			it.RevenueUSD, it.CostUSD, it.MarginUSD, it.MarginPct); err != nil {
			return 0, fmt.Errorf("sales: insert quote item: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE public.sales_quote_versions
   SET superseded_by = $2
 WHERE quote_id = $1 AND superseded_by IS NULL AND id <> $2`,
		quoteID, versionID); err != nil {
		return 0, fmt.Errorf("sales: supersede previous version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE public.sales_quotes
   SET current_version_id = $2, updated_at = now()
 WHERE id = $1`, quoteID, versionID); err != nil {
		return 0, fmt.Errorf("sales: move head pointer: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_status_log (quote_id, actor, from_status, to_status, reason)
VALUES ($1, $2, $3, $4, $5)`, quoteID, actor, statusNow, statusNow,
		fmt.Sprintf("version v%d materialized", versionNo)); err != nil {
		return 0, fmt.Errorf("sales: log version materialization: %w", err)
	}
	return versionID, nil
}

func normalizeTier(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "aggressive", "conservative":
		return strings.ToLower(strings.TrimSpace(t))
	default:
		return "standard"
	}
}

func validUntilText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// CreateQuote starts a quote: head (draft) + version 1 with the submitted
// lines. 不可信成本不出正式单：版本置信档非 trusted 时仍可创建（草稿），
// 但 Submit 会拒绝（C3 门是提交准入，不是创建准入）。行成本/置信档由服务端
// 从供给视图派生（deriveQuoteItems，2026-11-08 审计 P1/P6 修复）——客户端
// 只提供商业轴（量/零售价/折扣）。
func (s *Service) CreateQuote(ctx context.Context, in QuoteInput, actor string) (*Quote, error) {
	if strings.TrimSpace(in.CustomerName) == "" {
		return nil, errors.New("sales: customer_name is required")
	}
	if len(in.Items) == 0 {
		return nil, errors.New("sales: at least one item is required")
	}
	axes, err := s.deriveQuoteItems(ctx, in.Items, in.Tier)
	if err != nil {
		return nil, err
	}
	cents := s.centsPerCredit(ctx)
	lines, confidence := precomputeLines(in.Items, settings.CNYToUSDFX, cents)
	costTotal, saleTotal := 0.0, 0.0
	for i := range lines {
		costTotal += lines[i].CostUSD
		saleTotal += lines[i].RevenueUSD
	}
	marginUSD := saleTotal - costTotal
	marginPct := marginPctOf(marginUSD, saleTotal)
	validUntil := in.ValidUntil
	if validUntil == nil {
		days := settings.GetPlatformInt("sales.quote.default_valid_days", 14)
		t := time.Now().AddDate(0, 0, days)
		validUntil = &t
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("sales: begin tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var quoteID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO public.sales_quotes (demand_card_id, customer_name, title, status, created_by)
VALUES (NULLIF($1,0)::bigint, $2, $3, $4, $5)
RETURNING id`,
		derefI(in.DemandCardID), in.CustomerName, in.Title, StatusDraft, actor).Scan(&quoteID); err != nil {
		return nil, fmt.Errorf("sales: insert quote head: %w", err)
	}
	if _, err := s.insertQuoteVersion(ctx, tx, quoteID, 1, in, quoteVersionWrite{
		items: lines, confidence: confidence, axes: axes,
		costTotal: costTotal, saleTotal: saleTotal,
		marginUSD: marginUSD, marginPct: marginPct, validUntil: validUntil,
	}, actor, StatusDraft); err != nil {
		return nil, err
	}
	// 需求→报价联动（2026-11-08 审计 P3-13：943 'quoted' 此前是死值）：
	// 指定需求卡的建单把卡从 open/claimed 推进 quoted（guarded——已是
	// quoted/won/lost 的卡不动，一张卡可以多版本报价但状态只推进一次）。
	if id := derefI(in.DemandCardID); id > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE public.sales_demand_cards SET status = 'quoted', updated_at = now()
 WHERE id = $1 AND status IN ('open','claimed')`, id); err != nil {
			return nil, fmt.Errorf("sales: mark demand card quoted: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("sales: commit create quote: %w", err)
	}
	return s.GetQuote(ctx, quoteID)
}

func derefI(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}

// ReviseQuote materializes a new version from new content. Allowed only in
// draft（内容反复改 = 每次保存新版本）or sent（发出后修订 = 新版本，决议 A；
// 审批中/待发期间禁止改内容）。sent 单修订后**强制重评三维红线**（A3 修复：
// 触发 ⇒ 回 pending_approval + locked；不触发 ⇒ 维持 sent）——发出后的新版本
// 与首发同判，不给「改价绕审批」留门。
func (s *Service) ReviseQuote(ctx context.Context, quoteID int64, in QuoteInput, actor string) (*Quote, error) {
	head, err := s.GetQuote(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	if head.Status != StatusDraft && head.Status != StatusSent {
		return nil, fmt.Errorf("sales: quote is %s — content is frozen (revise allowed in draft or after sent)", head.Status)
	}
	if len(in.Items) == 0 {
		return nil, errors.New("sales: at least one item is required")
	}
	axes, err := s.deriveQuoteItems(ctx, in.Items, in.Tier)
	if err != nil {
		return nil, err
	}
	cents := s.centsPerCredit(ctx)
	lines, confidence := precomputeLines(in.Items, settings.CNYToUSDFX, cents)
	costTotal, saleTotal := 0.0, 0.0
	for i := range lines {
		costTotal += lines[i].CostUSD
		saleTotal += lines[i].RevenueUSD
	}
	marginUSD := saleTotal - costTotal
	marginPct := marginPctOf(marginUSD, saleTotal)
	validUntil := in.ValidUntil
	if validUntil == nil && head.Version != nil {
		validUntil = head.Version.ValidUntil // 修订继承现行有效期
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("sales: begin tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var nextNo int
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(version_no), 0) + 1 FROM public.sales_quote_versions WHERE quote_id = $1`,
		quoteID).Scan(&nextNo); err != nil {
		return nil, fmt.Errorf("sales: next version no: %w", err)
	}
	newVersionID, err := s.insertQuoteVersion(ctx, tx, quoteID, nextNo, in, quoteVersionWrite{
		items: lines, confidence: confidence, axes: axes,
		costTotal: costTotal, saleTotal: saleTotal,
		marginUSD: marginUSD, marginPct: marginPct, validUntil: validUntil,
	}, actor, head.Status)
	if err != nil {
		return nil, err
	}
	// A3（2026-11-05 审计）：sent 单修订不得旁路审批——新版本重跑 DealHub
	// 三维（与 SubmitQuote 同一判定核 redLineVerdict）；触发 ⇒ 回
	// pending_approval + locked，人审通过才可再次 SendQuote。未触发 ⇒ 维持 sent。
	nextStatus := head.Status
	locked := false
	if head.Status == StatusSent {
		maxDiscount := 0.0
		for i := range lines {
			if lines[i].DiscountPct > maxDiscount {
				maxDiscount = lines[i].DiscountPct
			}
		}
		mp := 0.0
		if marginPct != nil {
			mp = *marginPct
		}
		verdict := redLineVerdict(mp, maxDiscount, saleTotal)
		if !verdict.AutoApprove {
			nextStatus = StatusPendingApproval
			locked = true
			if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_approvals (quote_id, version_id, trigger_reasons, requested_by)
VALUES ($1, $2, $3::jsonb, $4)`,
				quoteID, newVersionID, triggersJSON(verdict.Triggers), actor); err != nil {
				return nil, fmt.Errorf("sales: insert revise re-approval row: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_status_log (quote_id, actor, from_status, to_status, reason)
VALUES ($1, $2, $3, $4, $5)`,
				quoteID, actor, StatusSent, StatusPendingApproval, triggersJSON(verdict.Triggers)); err != nil {
				return nil, fmt.Errorf("sales: log revise re-approval transition: %w", err)
			}
		}
	}
	res, err := tx.Exec(ctx, `
UPDATE public.sales_quotes SET status = $2, locked = $3, updated_at = now() WHERE id = $1`,
		quoteID, nextStatus, locked)
	if err != nil {
		return nil, fmt.Errorf("sales: settle head on revise: %w", err)
	}
	// 并发守卫（2026-11-08 审计 P3-7）：settle 必须命中恰好一行——0 行说明
	// 头状态在 GetQuote 之后被并发迁移，整单回滚而不是静默落地。
	if res.RowsAffected() == 0 {
		return nil, errors.New("sales: quote head moved concurrently — revise aborted")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("sales: commit revise: %w", err)
	}
	return s.GetQuote(ctx, quoteID)
}

// ── 红线判定（决议 B） ──────────────────────────────────────────────────

// RedLineTrigger is one dimension's evidence, recomputable (audit red line).
type RedLineTrigger struct {
	Dimension string  `json:"dimension"` // margin_floor | discount_depth | order_amount
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

// RedLineVerdict is the three-dimension evaluation of one version.
type RedLineVerdict struct {
	Triggers     []RedLineTrigger `json:"triggers"`
	AutoApprove  bool             `json:"auto_approve"`
	MarginPct    float64          `json:"margin_pct"`
	MaxDiscount  float64          `json:"max_discount_pct"`
	SaleTotalUSD float64          `json:"sale_total_usd"`
}

// EvaluateRedLines applies the DealHub three dimensions to a version.
// Thresholds come from settings_kv (registered in settings/spec_sales.go,
// defaults = the plan's待裁决建议值).
func (s *Service) EvaluateRedLines(ctx context.Context, versionID int64) (*RedLineVerdict, error) {
	v, err := s.getQuoteVersion(ctx, versionID, true)
	if err != nil {
		return nil, err
	}
	marginPct := 0.0
	if v.MarginPct != nil {
		marginPct = *v.MarginPct
	}
	maxDiscount := 0.0
	for _, it := range v.Items {
		if it.DiscountPct > maxDiscount {
			maxDiscount = it.DiscountPct
		}
	}
	return redLineVerdict(marginPct, maxDiscount, v.SaleTotalUSD), nil
}

// redLineVerdict folds the DealHub three dimensions onto already-folded
// numbers — the single judgment core shared by the DB path (EvaluateRedLines)
// and the in-tx revise path (ReviseQuote re-review, A3 修复)。
func redLineVerdict(marginPct, maxDiscount, saleTotal float64) *RedLineVerdict {
	marginFloor := float64(settings.GetPlatformInt("sales.quote.margin_floor_pct", 30))
	discountDepth := float64(settings.GetPlatformInt("sales.quote.discount_depth_pct", 10))
	orderAmount := float64(settings.GetPlatformInt("sales.quote.order_amount_usd", 500))

	verdict := &RedLineVerdict{
		MarginPct:    marginPct,
		MaxDiscount:  maxDiscount,
		SaleTotalUSD: saleTotal,
	}
	if marginFloor > 0 && marginPct < marginFloor {
		verdict.Triggers = append(verdict.Triggers, RedLineTrigger{Dimension: "margin_floor", Value: marginPct, Threshold: marginFloor})
	}
	if discountDepth > 0 && maxDiscount > discountDepth {
		verdict.Triggers = append(verdict.Triggers, RedLineTrigger{Dimension: "discount_depth", Value: maxDiscount, Threshold: discountDepth})
	}
	if orderAmount > 0 && saleTotal >= orderAmount {
		verdict.Triggers = append(verdict.Triggers, RedLineTrigger{Dimension: "order_amount", Value: saleTotal, Threshold: orderAmount})
	}
	verdict.AutoApprove = len(verdict.Triggers) == 0
	return verdict
}

// ── 提交 / 审批 / 发送 / 收口 ────────────────────────────────────────────

// SubmitQuote evaluates red lines: no trigger ⇒ auto-approve（决议 B 政策内
// 自动放行，不进人审）；triggered ⇒ pending_approval + locked + 审批行
// （trigger_reasons 快照可复算）。
func (s *Service) SubmitQuote(ctx context.Context, quoteID int64, actor string) (*Quote, error) {
	head, err := s.GetQuote(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	if head.Status != StatusDraft {
		return nil, fmt.Errorf("sales: quote is %s, only draft can be submitted", head.Status)
	}
	if head.Version == nil {
		return nil, errors.New("sales: quote has no current version")
	}
	verdict, err := s.EvaluateRedLines(ctx, head.Version.ID)
	if err != nil {
		return nil, err
	}
	// C3 门：不可信成本不出正式单（风暴档 §4-3 判据）。
	if head.Version.PriceConfidence != "trusted" {
		return nil, fmt.Errorf("sales: price confidence is %q — untrusted cost cannot leave as a formal quote", head.Version.PriceConfidence)
	}

	if verdict.AutoApprove {
		if err := s.transition(ctx, quoteID, actor, StatusDraft, StatusApproved, "auto-approve: no red-line dimension triggered (in-policy)"); err != nil {
			return nil, err
		}
		return s.GetQuote(ctx, quoteID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("sales: begin tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	res, err := tx.Exec(ctx, `
UPDATE public.sales_quotes SET status = $2, locked = true, updated_at = now()
 WHERE id = $1 AND status = $3`, quoteID, StatusPendingApproval, StatusDraft)
	if err != nil {
		return nil, fmt.Errorf("sales: mark pending_approval: %w", err)
	}
	// 并发守卫（2026-11-08 审计 P3-7）：此前不查 RowsAffected ⇒ 双提交可产生
	// 重复审批行 + 重复 draft→pending_approval 审计行（DecideApproval 会一并
	// UPDATE 所有 pending 行）。0 行 = 有人抢先，整单回滚。
	if res.RowsAffected() == 0 {
		return nil, errors.New("sales: quote is no longer draft (concurrent submit)")
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_approvals (quote_id, version_id, trigger_reasons, requested_by)
VALUES ($1, $2, $3::jsonb, $4)`,
		quoteID, head.Version.ID, triggersJSON(verdict.Triggers), actor); err != nil {
		return nil, fmt.Errorf("sales: insert approval row: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_status_log (quote_id, actor, from_status, to_status, reason)
VALUES ($1, $2, $3, $4, $5)`,
		quoteID, actor, StatusDraft, StatusPendingApproval, triggersJSON(verdict.Triggers)); err != nil {
		return nil, fmt.Errorf("sales: log transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("sales: commit submit: %w", err)
	}
	return s.GetQuote(ctx, quoteID)
}

func triggersJSON(ts []RedLineTrigger) string {
	if len(ts) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, t := range ts {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"dimension":%q,"value":%s,"threshold":%s}`,
			t.Dimension, numJSON(t.Value), numJSON(t.Threshold))
	}
	b.WriteByte(']')
	return b.String()
}

// numJSON renders a float without exponent noise for small magnitudes.
func numJSON(f float64) string {
	s := fmt.Sprintf("%.6f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// DecideApproval records the human decision. approved ⇒ unlock, sent-ready;
// rejected ⇒ unlock, back to draft.
func (s *Service) DecideApproval(ctx context.Context, quoteID int64, decision, comment, approver string) (*Quote, error) {
	if decision != "approved" && decision != "rejected" {
		return nil, fmt.Errorf("sales: invalid decision %q (approved|rejected)", decision)
	}
	if approver == "" {
		return nil, errors.New("sales: decision requires an approver identity")
	}
	head, err := s.GetQuote(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	if head.Status != StatusPendingApproval {
		return nil, fmt.Errorf("sales: quote is %s, not pending approval", head.Status)
	}
	next := StatusApproved
	reason := "approved by " + approver
	if decision == "rejected" {
		next = StatusDraft
		reason = "rejected by " + approver
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("sales: begin tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	res, err := tx.Exec(ctx, `
UPDATE public.sales_quote_approvals
   SET decision = $2, approver = $3, comment = $4, decided_at = now(),
       elapsed_seconds = GREATEST(0, EXTRACT(EPOCH FROM (now() - created_at))::int)
 WHERE quote_id = $1 AND decision = 'pending'`, quoteID, decision, approver, comment)
	if err != nil {
		return nil, fmt.Errorf("sales: record approval decision: %w", err)
	}
	if res.RowsAffected() == 0 {
		return nil, errors.New("sales: no pending approval row for this quote")
	}
	if _, err := tx.Exec(ctx, `
UPDATE public.sales_quotes SET status = $2, locked = false, updated_at = now()
 WHERE id = $1 AND status = $3`, quoteID, next, StatusPendingApproval); err != nil {
		return nil, fmt.Errorf("sales: apply decision: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_status_log (quote_id, actor, from_status, to_status, reason)
VALUES ($1, $2, $3, $4, $5)`, quoteID, approver, StatusPendingApproval, next, reason); err != nil {
		return nil, fmt.Errorf("sales: log decision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("sales: commit decision: %w", err)
	}
	return s.GetQuote(ctx, quoteID)
}

// SendQuote transitions approved → sent.
func (s *Service) SendQuote(ctx context.Context, quoteID int64, actor string) (*Quote, error) {
	if err := s.transition(ctx, quoteID, actor, StatusApproved, StatusSent, "sent to customer"); err != nil {
		return nil, err
	}
	return s.GetQuote(ctx, quoteID)
}

// CloseQuote finishes a sent quote as won / lost（丢单原因强制回填，风暴档 §4-5）.
func (s *Service) CloseQuote(ctx context.Context, quoteID int64, status, reason, actor string) (*Quote, error) {
	if status != StatusWon && status != StatusLost {
		return nil, fmt.Errorf("sales: invalid close status %q (won|lost)", status)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("sales: closing requires a reason (won/lost 收口必须写明原因)")
	}
	if err := s.transition(ctx, quoteID, actor, StatusSent, status, reason); err != nil {
		return nil, err
	}
	return s.GetQuote(ctx, quoteID)
}

// transition is the guarded single-step head transition + audit row.
func (s *Service) transition(ctx context.Context, quoteID int64, actor, from, to, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sales: begin tx: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	res, err := tx.Exec(ctx, `
UPDATE public.sales_quotes SET status = $3, locked = false, updated_at = now()
 WHERE id = $1 AND status = $2`, quoteID, from, to)
	if err != nil {
		return fmt.Errorf("sales: transition head: %w", err)
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("sales: quote is not in %q state (no transition)", from)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO public.sales_quote_status_log (quote_id, actor, from_status, to_status, reason)
VALUES ($1, $2, $3, $4, $5)`, quoteID, actor, from, to, reason); err != nil {
		return fmt.Errorf("sales: log transition: %w", err)
	}
	return tx.Commit(ctx)
}

// ── owner 语义（A-11 先例；2026-11-08 审计 P2-3 修复）─────────────────────
//
// 报价单是销售本人的商业数据（客户、价格、毛利）：读与动作收窄到
// created_by 本人；super_admin 全量；legacy admin_key（无 username 服务
// 账号）不收窄（有意，myOwnerScope 同款）。非本人一律 ErrNotFound——
// 不泄露存在性（IDOR 防御口径），而不是 403。

// EnsureQuoteOwner verifies the actor may touch this quote.
func (s *Service) EnsureQuoteOwner(ctx context.Context, quoteID int64, actor string, unrestricted bool) error {
	if unrestricted {
		return nil
	}
	var createdBy string
	err := s.pool.QueryRow(ctx, `
SELECT created_by FROM public.sales_quotes WHERE id = $1`, quoteID).Scan(&createdBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("sales: quote owner check: %w", err)
	}
	if createdBy != actor {
		return ErrNotFound
	}
	return nil
}

// ── 读面 ────────────────────────────────────────────────────────────────

// ListQuotes lists heads with their current version summary. unrestricted
// = false 时只看本人创建的单（owner 语义；super / legacy admin_key 全量）。
func (s *Service) ListQuotes(ctx context.Context, status, actor string, unrestricted bool) ([]Quote, error) {
	q := `
SELECT q.id, q.demand_card_id, q.customer_name, q.title, q.status,
       q.current_version_id, q.locked, q.created_by, q.created_at, q.updated_at,
       v.id, v.version_no, v.price_confidence, v.tier, v.fx_rate::float8,
       v.cost_total_usd::float8, v.sale_total_usd::float8, v.margin_usd::float8,
       v.margin_pct::float8, v.valid_until, v.created_by, v.created_at,
       (v.valid_until IS NOT NULL AND v.valid_until < now())
  FROM public.sales_quotes q
  LEFT JOIN public.sales_quote_versions v ON v.id = q.current_version_id`
	args := []any{}
	conj := " WHERE"
	if status != "" {
		q += conj + ` q.status = $1`
		args = append(args, status)
		conj = " AND"
	}
	if !unrestricted {
		q += conj + ` q.created_by = $` + strconv.Itoa(len(args)+1)
		args = append(args, actor)
	}
	q += ` ORDER BY q.updated_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sales: list quotes: %w", err)
	}
	defer rows.Close()
	out := make([]Quote, 0, 16)
	for rows.Next() {
		var head Quote
		var demandCard *int64
		var versionID *int64
		var versionNo *int
		var vConf, vTier *string
		var fx, cost, sale, margin *float64
		var marginPct *float64
		var validUntil *time.Time
		var vCreatedBy *string
		var vCreatedAt *time.Time
		var expired bool
		if err := rows.Scan(&head.ID, &demandCard, &head.CustomerName, &head.Title,
			&head.Status, &head.CurrentVersionID, &head.Locked, &head.CreatedBy,
			&head.CreatedAt, &head.UpdatedAt,
			&versionID, &versionNo, &vConf, &vTier, &fx, &cost, &sale, &margin,
			&marginPct, &validUntil, &vCreatedBy, &vCreatedAt, &expired); err != nil {
			return nil, fmt.Errorf("sales: scan quote list: %w", err)
		}
		head.DemandCardID = demandCard
		if versionID != nil {
			head.Version = &QuoteVersion{
				ID: *versionID, QuoteID: head.ID,
				VersionNo: derefI2(versionNo), PriceConfidence: derefS(vConf),
				Tier: derefS(vTier), FXRate: derefF(fx),
				CostTotalUSD: derefF(cost), SaleTotalUSD: derefF(sale),
				MarginUSD: derefF(margin), MarginPct: marginPct,
				ValidUntil: validUntil, Expired: expired,
				CreatedBy: derefS(vCreatedBy),
			}
			if vCreatedAt != nil {
				head.Version.CreatedAt = *vCreatedAt
			}
		}
		out = append(out, head)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sales: iterate quotes: %w", err)
	}
	s.attachCostDrift(ctx, out)
	return out, nil
}

// attachCostDrift marks active quotes whose version-snapshot line costs have
// drifted from the current supply-view prices（风暴档 §3.4「成本漂移提醒」，
// W2-T2：提醒不自动撤）。
//
// 口径：与派生同一确定性选行（非 withheld、非 suspended、credential_id 最小）；
// 行级漂移 = max(|Δin/in|, |Δout/out|)（旧价为 0/NULL 而新价出现 ⇒ 100%，
// 反向同理；币种不同的行跳过——不跨币种假装可比，NULL≠0 红线）；单取行级
// 最大值进版本，任一行超阈值 ⇒ drifted=true。won/lost/superseded 不漂
// （已收口的单回看历史快照，漂移提醒没有收件人）。
func (s *Service) attachCostDrift(ctx context.Context, quotes []Quote) {
	active := map[int64]*Quote{} // versionID → quote（漂移按版本快照对价）
	canonSet := map[string]bool{}
	canonNames := make([]string, 0, 8)
	versionIDs := make([]int64, 0, len(quotes))
	for i := range quotes {
		q := &quotes[i]
		switch q.Status {
		case StatusDraft, StatusPendingApproval, StatusApproved, StatusSent:
		default:
			continue
		}
		if q.Version == nil {
			continue
		}
		active[q.Version.ID] = q
		versionIDs = append(versionIDs, q.Version.ID)
	}
	if len(versionIDs) == 0 {
		return
	}
	type snapCost struct {
		in, out  *float64
		currency string
	}
	snaps := map[int64]map[string]snapCost{} // versionID → canonical → snapshot
	itemRows, err := s.pool.Query(ctx, `
SELECT version_id, canonical_name, cost_in_per_1m::float8, cost_out_per_1m::float8, cost_currency
  FROM public.sales_quote_items WHERE version_id = ANY($1)`, versionIDs)
	if err != nil {
		return // 漂移是增强信息，读失败不阻断清单（内部错误照常落日志）
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var vid int64
		var canon string
		var inC, outC *float64
		var cur string
		if itemRows.Scan(&vid, &canon, &inC, &outC, &cur) != nil {
			return
		}
		if snaps[vid] == nil {
			snaps[vid] = map[string]snapCost{}
		}
		snaps[vid][canon] = snapCost{in: inC, out: outC, currency: cur}
		lc := strings.ToLower(canon)
		if !canonSet[lc] {
			canonSet[lc] = true
			canonNames = append(canonNames, lc)
		}
	}
	if itemRows.Err() != nil || len(canonNames) == 0 {
		return
	}
	viewRows, annStatus, err := s.fetchSupplyViewRows(ctx, canonNames)
	if err != nil {
		return
	}
	withheld := withholdNames(viewRows)
	current := map[string]SellableRow{}
	for _, r := range viewRows { // ordered ⇒ first per canonical = same pick as derivation
		if _, ok := current[r.CanonicalName]; !ok {
			current[r.CanonicalName] = r
		}
	}
	driftThreshold := float64(settings.GetPlatformInt("sales.quote.cost_drift_alert_pct", 10))
	for vid, perCanon := range snaps {
		q := active[vid]
		if q == nil {
			continue
		}
		maxDrift := 0.0
		for canon, snap := range perCanon {
			row, ok := current[canon]
			if !ok || withheld[canon] ||
				annStatus[fmt.Sprintf("%d:%d", row.CredentialID, row.ProviderModelID)] == "suspended" {
				continue // 供给行消失/被扣下/停用：无可比价，不假报漂移
			}
			if normalizeCostCurrency(row.SupplierCurrency) != snap.currency {
				continue // 币种变了不可比（拒绝默认换算，红线）
			}
			newIn, newOut := parseNum(row.SupplierInPer1M), parseNum(row.SupplierOutPer1M)
			oldIn, oldOut := derefF(snap.in), derefF(snap.out)
			for _, pair := range [][2]float64{{oldIn, newIn}, {oldOut, newOut}} {
				old, now := pair[0], pair[1]
				var d float64
				switch {
				case old > 0:
					d = math.Abs(now-old) / old * 100
				case old == 0 && now > 0, old > 0 && now == 0:
					d = 100 // 成本从无到有 / 从有到无
				default:
					d = 0
				}
				if d > maxDrift {
					maxDrift = d
				}
			}
		}
		if maxDrift > 0 {
			p := maxDrift
			q.CostDriftPct = &p
			q.CostDrifted = maxDrift >= driftThreshold
		}
	}
}

func derefI2(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func derefS(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GetQuote loads the head + current version (+ items).
func (s *Service) GetQuote(ctx context.Context, quoteID int64) (*Quote, error) {
	var head Quote
	var demandCard *int64
	err := s.pool.QueryRow(ctx, `
SELECT id, demand_card_id, customer_name, title, status, current_version_id,
       locked, created_by, created_at, updated_at
  FROM public.sales_quotes WHERE id = $1`, quoteID).
		Scan(&head.ID, &demandCard, &head.CustomerName, &head.Title, &head.Status,
			&head.CurrentVersionID, &head.Locked, &head.CreatedBy,
			&head.CreatedAt, &head.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sales: get quote: %w", err)
	}
	head.DemandCardID = demandCard
	// （2026-11-08 审计 P3-16：此前的 versionID 死变量与恒假分支已删。）
	if head.CurrentVersionID != nil {
		v, err := s.getQuoteVersion(ctx, *head.CurrentVersionID, true)
		if err == nil {
			head.Version = v
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return &head, nil
}

func (s *Service) getQuoteVersion(ctx context.Context, versionID int64, withItems bool) (*QuoteVersion, error) {
	var v QuoteVersion
	var marginPct *float64
	var axes []byte
	err := s.pool.QueryRow(ctx, `
SELECT id, quote_id, version_no, price_axes, price_confidence, tier, fx_rate::float8,
       cost_total_usd::float8, sale_total_usd::float8, margin_usd::float8,
       margin_pct::float8, valid_until, superseded_by, created_by, created_at,
       (valid_until IS NOT NULL AND valid_until < now())
  FROM public.sales_quote_versions WHERE id = $1`, versionID).
		Scan(&v.ID, &v.QuoteID, &v.VersionNo, &axes, &v.PriceConfidence, &v.Tier,
			&v.FXRate, &v.CostTotalUSD, &v.SaleTotalUSD, &v.MarginUSD, &marginPct,
			&v.ValidUntil, &v.SupersededBy, &v.CreatedBy, &v.CreatedAt, &v.Expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sales: get quote version: %w", err)
	}
	v.MarginPct = marginPct
	v.PriceAxes = map[string]any{}
	_ = jsonUnmarshal(axes, &v.PriceAxes)
	if withItems {
		items, err := s.listQuoteItems(ctx, versionID)
		if err != nil {
			return nil, err
		}
		v.Items = items
	}
	return &v, nil
}

func (s *Service) listQuoteItems(ctx context.Context, versionID int64) ([]QuoteItemOutput, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, canonical_name, display_name, est_tokens_in, est_tokens_out, volume_note,
       cost_in_per_1m::float8, cost_out_per_1m::float8, cost_currency,
       rate_in::float8, rate_out::float8, discount_pct::float8, confidence,
       fx_rate::float8, revenue_usd::float8, cost_usd::float8,
       margin_usd::float8, margin_pct::float8
  FROM public.sales_quote_items WHERE version_id = $1 ORDER BY id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("sales: list quote items: %w", err)
	}
	defer rows.Close()
	out := make([]QuoteItemOutput, 0, 8)
	for rows.Next() {
		var it QuoteItemOutput
		if err := rows.Scan(&it.ID, &it.CanonicalName, &it.DisplayName,
			&it.EstTokensIn, &it.EstTokensOut, &it.VolumeNote,
			&it.CostInPer1M, &it.CostOutPer1M, &it.CostCurrency,
			&it.RateIn, &it.RateOut, &it.DiscountPct, &it.Confidence,
			&it.FXRate, &it.RevenueUSD, &it.CostUSD, &it.MarginUSD, &it.MarginPct); err != nil {
			return nil, fmt.Errorf("sales: scan quote item: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListQuoteVersions returns the full version chain (audit face).
func (s *Service) ListQuoteVersions(ctx context.Context, quoteID int64) ([]QuoteVersion, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, quote_id, version_no, price_confidence, tier, fx_rate::float8,
       cost_total_usd::float8, sale_total_usd::float8, margin_usd::float8,
       margin_pct::float8, valid_until, superseded_by, created_by, created_at,
       (valid_until IS NOT NULL AND valid_until < now())
  FROM public.sales_quote_versions WHERE quote_id = $1 ORDER BY version_no`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("sales: list quote versions: %w", err)
	}
	defer rows.Close()
	out := make([]QuoteVersion, 0, 4)
	for rows.Next() {
		var v QuoteVersion
		var axes = []byte("{}")
		if err := rows.Scan(&v.ID, &v.QuoteID, &v.VersionNo, &v.PriceConfidence,
			&v.Tier, &v.FXRate, &v.CostTotalUSD, &v.SaleTotalUSD, &v.MarginUSD,
			&v.MarginPct, &v.ValidUntil, &v.SupersededBy, &v.CreatedBy,
			&v.CreatedAt, &v.Expired); err != nil {
			return nil, fmt.Errorf("sales: scan quote version: %w", err)
		}
		v.PriceAxes = map[string]any{}
		_ = jsonUnmarshal(axes, &v.PriceAxes)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── 分享（948，Invoice Ninja QuoteInvitation 形态） ─────────────────────

// ShareLink is one share row (read projection; token is never stored).
type ShareLink struct {
	ID           int64      `json:"id"`
	QuoteID      int64      `json:"quote_id"`
	VersionID    int64      `json:"version_id"`
	ContactName  string     `json:"contact_name"`
	ContactEmail string     `json:"contact_email"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	SentAt       *time.Time `json:"sent_at,omitempty"`
	ViewedAt     *time.Time `json:"viewed_at,omitempty"`
	OpenedAt     *time.Time `json:"opened_at,omitempty"`
	ViewCount    int        `json:"view_count"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	// Token 只在创建响应出现一次（明文不落库）。
	Token string `json:"token,omitempty"`
}

// CreateShare mints a 128-bit token for one contact; only the sha256 lands
// in the table. ttl defaults to 14 days.
func (s *Service) CreateShare(ctx context.Context, quoteID int64, contactName, contactEmail string, ttl time.Duration, actor string) (*ShareLink, error) {
	head, err := s.GetQuote(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	if head.Status != StatusSent && head.Status != StatusApproved && head.Status != StatusWon {
		return nil, fmt.Errorf("sales: share requires a sent (or approved) quote, quote is %s", head.Status)
	}
	if head.CurrentVersionID == nil {
		return nil, errors.New("sales: quote has no current version to share")
	}
	if strings.TrimSpace(contactName) == "" {
		return nil, errors.New("sales: contact_name is required")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("sales: mint token: %w", err)
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	if ttl <= 0 {
		ttl = 14 * 24 * time.Hour
	}
	var out ShareLink
	err = s.pool.QueryRow(ctx, `
INSERT INTO public.sales_quote_shares
    (quote_id, version_id, contact_name, contact_email, token_hash,
     expires_at, sent_at, created_by)
VALUES ($1,$2,$3,$4,$5, now() + make_interval(secs => $6), now(), $7)
RETURNING id, quote_id, version_id, contact_name, contact_email, expires_at,
          revoked_at, sent_at, viewed_at, opened_at, view_count, created_by, created_at`,
		quoteID, *head.CurrentVersionID, contactName, contactEmail, tokenHash,
		int64(ttl.Seconds()), actor).
		Scan(&out.ID, &out.QuoteID, &out.VersionID, &out.ContactName,
			&out.ContactEmail, &out.ExpiresAt, &out.RevokedAt, &out.SentAt,
			&out.ViewedAt, &out.OpenedAt, &out.ViewCount, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("sales: create share: %w", err)
	}
	out.Token = token
	return &out, nil
}

// ListShares lists the share rows of one quote (no tokens exist to show).
func (s *Service) ListShares(ctx context.Context, quoteID int64) ([]ShareLink, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, quote_id, version_id, contact_name, contact_email, expires_at,
       revoked_at, sent_at, viewed_at, opened_at, view_count, created_by, created_at
  FROM public.sales_quote_shares WHERE quote_id = $1 ORDER BY created_at DESC`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("sales: list shares: %w", err)
	}
	defer rows.Close()
	out := make([]ShareLink, 0, 4)
	for rows.Next() {
		var l ShareLink
		if err := rows.Scan(&l.ID, &l.QuoteID, &l.VersionID, &l.ContactName,
			&l.ContactEmail, &l.ExpiresAt, &l.RevokedAt, &l.SentAt, &l.ViewedAt,
			&l.OpenedAt, &l.ViewCount, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("sales: scan share: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeShare marks one share revoked (idempotent). Owner semantics apply:
// only the quote creator (or super/unrestricted) may revoke（2026-11-08 审计
// P2-3 修复——此前只认 shareID，任何 admin 角色可撤销他人客户的链接）。
func (s *Service) RevokeShare(ctx context.Context, shareID int64, actor string, unrestricted bool) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE public.sales_quote_shares sh
   SET revoked_at = now()
 WHERE sh.id = $1 AND sh.revoked_at IS NULL
   AND ($3 OR EXISTS (
        SELECT 1 FROM public.sales_quotes q
         WHERE q.id = sh.quote_id AND q.created_by = $2))`,
		shareID, actor, unrestricted)
	if err != nil {
		return fmt.Errorf("sales: revoke share: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PublicQuote is the customer-safe projection: sale prices and volumes only —
// cost, margin, supplier identity and credentials never enter this struct
// (the response-body assertion test pins the JSON fields). The offer totals
// (subtotal/sale_total，W2 客户版页面的合计位) are the customer's own price —
// 2026-11-08 audit: still no cost/margin axis anywhere in the struct.
type PublicQuote struct {
	CustomerName string            `json:"customer_name"`
	Title        string            `json:"title"`
	ValidUntil   *time.Time        `json:"valid_until"`
	Expired      bool              `json:"expired"`
	Tier         string            `json:"tier"`
	SaleTotalUSD float64           `json:"sale_total_usd"`
	Items        []PublicQuoteItem `json:"items"`
}

// PublicQuoteItem exposes the offer side only (Decision C: base rate ×
// discount both shown — the discount is part of the offer).
type PublicQuoteItem struct {
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	EstTokensIn   int64    `json:"est_tokens_in"`
	EstTokensOut  int64    `json:"est_tokens_out"`
	VolumeNote    string   `json:"volume_note"`
	RateIn        *float64 `json:"rate_in"`
	RateOut       *float64 `json:"rate_out"`
	DiscountPct   float64  `json:"discount_pct"`
	SubtotalUSD   float64  `json:"subtotal_usd"`
}

// GetPublicQuote resolves a share token to the customer-safe projection and
// records the access audit (viewed/opened + per-access detail entry).
func (s *Service) GetPublicQuote(ctx context.Context, token, ip, ua string) (*PublicQuote, error) {
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	var quoteID, versionID int64
	var expiresAt time.Time
	var revokedAt *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT quote_id, version_id, expires_at, revoked_at
  FROM public.sales_quote_shares
 WHERE token_hash = $1`, tokenHash).Scan(&quoteID, &versionID, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sales: resolve share: %w", err)
	}
	// 访问结果先算好（expired/revoked 也要记审计）。
	outcome := "ok"
	if revokedAt != nil {
		outcome = "revoked"
	} else if time.Now().After(expiresAt) {
		outcome = "expired"
	}
	// A6（2026-11-05 审计）：detail.access 截断保留**最近 20 条**——匿名可达
	// 的行级写入必须有界（此前无限追加 = 无界增长）。取 ord 最大（最新）的
	// 20 条，再按 ord 升序回排（保持「最新在尾」的既有约定）。
	// 2026-11-08 审计 P3-12：viewed_at/opened_at/view_count 只在有效访问
	// （outcome=ok）时推进——过期/撤销后的探测流量仍逐条落审计，但不再污染
	// 三时点埋点与漏斗口径。
	if _, err := s.pool.Exec(ctx, `
UPDATE public.sales_quote_shares
   SET viewed_at = CASE WHEN $2 = 'ok' THEN COALESCE(viewed_at, now()) ELSE viewed_at END,
       opened_at = CASE WHEN $2 = 'ok' THEN now() ELSE opened_at END,
       view_count = view_count + CASE WHEN $2 = 'ok' THEN 1 ELSE 0 END,
       detail = jsonb_set(
            COALESCE(detail, '{}'::jsonb),
            '{access}',
            (
              SELECT COALESCE(jsonb_agg(entry ORDER BY ord ASC), '[]'::jsonb)
              FROM (
                SELECT e AS entry, ord
                FROM jsonb_array_elements(
                    COALESCE(detail->'access', '[]'::jsonb) || to_jsonb(jsonb_build_array(
                        jsonb_build_object('at', now(), 'ip', $3::text, 'ua', $4::text, 'outcome', $2)))
                ) WITH ORDINALITY AS t(e, ord)
                ORDER BY ord DESC
                LIMIT 20
              ) recent
            )
       )
 WHERE id = (
    SELECT id FROM public.sales_quote_shares WHERE token_hash = $1
 )`, tokenHash, outcome, truncate(ip, 64), truncate(ua, 256)); err != nil {
		return nil, fmt.Errorf("sales: record access audit: %w", err)
	}
	if outcome != "ok" {
		return nil, ErrNotFound
	}
	head, err := s.GetQuote(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	// A2（2026-11-05 审计）：公开面服务**分享钉住的版本**（948.version_id
	// NOT NULL），不是头表当前版本——sent 后修订换版不得让已发出的客户
	// 链接静默变价，且修订版未经人审前本来就不该被公开面暴露。
	v, err := s.getQuoteVersion(ctx, versionID, true)
	if err != nil {
		return nil, err
	}
	items := make([]PublicQuoteItem, 0, len(v.Items))
	for _, it := range v.Items {
		items = append(items, PublicQuoteItem{
			CanonicalName: it.CanonicalName,
			DisplayName:   it.DisplayName,
			EstTokensIn:   it.EstTokensIn,
			EstTokensOut:  it.EstTokensOut,
			VolumeNote:    it.VolumeNote,
			RateIn:        it.RateIn,
			RateOut:       it.RateOut,
			DiscountPct:   it.DiscountPct,
			SubtotalUSD:   it.RevenueUSD, // 报价单预计算列（客户的应付小计，非敏感轴）
		})
	}
	return &PublicQuote{
		CustomerName: head.CustomerName,
		Title:        head.Title,
		ValidUntil:   v.ValidUntil,
		Expired:      v.Expired,
		Tier:         v.Tier,
		SaleTotalUSD: v.SaleTotalUSD,
		Items:        items,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ListApprovals lists the approval chain of one quote.
func (s *Service) ListApprovals(ctx context.Context, quoteID int64) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, quote_id, version_id, trigger_reasons::text, requested_by, decision,
       approver, comment, created_at, decided_at, elapsed_seconds
  FROM public.sales_quote_approvals WHERE quote_id = $1 ORDER BY created_at DESC`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("sales: list approvals: %w", err)
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 4)
	for rows.Next() {
		var id, qid, vid int64
		var triggers, requestedBy, decision, approver, comment string
		var createdAt time.Time
		var decidedAt *time.Time
		var elapsed *int
		if err := rows.Scan(&id, &qid, &vid, &triggers, &requestedBy, &decision,
			&approver, &comment, &createdAt, &decidedAt, &elapsed); err != nil {
			return nil, fmt.Errorf("sales: scan approval: %w", err)
		}
		out = append(out, map[string]any{
			"id": id, "quote_id": qid, "version_id": vid,
			"trigger_reasons": triggers, "requested_by": requestedBy,
			"decision": decision, "approver": approver, "comment": comment,
			"created_at": createdAt, "decided_at": decidedAt,
			"elapsed_seconds": elapsed,
		})
	}
	return out, rows.Err()
}
