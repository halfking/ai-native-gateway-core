package admin

// market_compare.go — 定稿 M3-T2：/api/admin/market/compare（GET，superAdmin）。
//
// 价格对比分析页（/operations/price-compare）的读面：
//	· C1/C2/C3 评分行 = supplier_market_position（评分 job 每日 upsert 的活表，
//	  computed_at 只留最后一次）；
//	· 双地板并列 = 按 canonical_name 对拍 market_price_reference（959 视图，
//	  dimension='standard'；overseas=USD 原生 / domestic=CNY 原生，平台数 <3
//	  ⇒ 参考值非硬判据 —— 告警 market_ref_thin_coverage 的口径）；
//	· 覆盖率摘要 = price_confidence 四档计数（≥3 平台硬指标读数由前端按
//	  market_ref_platform_count 呈现）。
//
// ★ 红线锚点：本端点只读对账 —— 不写任何表、不喂路由/计费
// （MarketAdvantage 默认权重 0，路由第五维未接线 —— M3-T3 另立裁决）。
//
// ★ NULL≠0：所有价格/比值列按指针透传（无行情行 ⇒ NULL ⇒ 前端显示「无行情」，
// 不得落 0）。表缺席（959/评分 job 未上）⇒ 200 + available:false（空态要给
// 「为什么空 + 下一步」，不是 500 噪声）。

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// nullF 把 NullFloat64 转 *float64（NULL ⇒ nil，永不落 0 —— NULL≠0 红线）。
// ★ 扫描用 sql.Null* 而非 **float64：pgxmock 的简化 Scan 只认 Scanner 接口
// （真 pgx 两种都行）⇒ Null* 是两边都成立的形状。
func nullF(n sql.NullFloat64) *float64 {
	if n.Valid {
		v := n.Float64
		return &v
	}
	return nil
}

// marketCompareRow 评分行 + 对拍地板（原生币种，无隐式折算）。
type marketCompareRow struct {
	CredentialID  int64    `json:"credential_id"`
	ModelID       int64    `json:"provider_model_id"`
	CanonicalName string   `json:"canonical_name"`
	Currency      string   `json:"currency"`
	SupplierIn    *float64 `json:"supplier_in_per_1m"`
	SupplierOut   *float64 `json:"supplier_out_per_1m"`
	// C3 四档：trusted / suspect_symmetric / empty / unknown_currency
	PriceConfidence string `json:"price_confidence"`
	// C1 采购折扣率（vendor_ref_* 为原厂锚原生价；fx_used = USD→CNY 快照，仅跨币非 NULL）
	DiscountVsVendor *float64 `json:"discount_vs_vendor"`
	VendorRefIn      *float64 `json:"vendor_ref_in_per_1m"`
	VendorRefOut     *float64 `json:"vendor_ref_out_per_1m"`
	VendorRefCur     string   `json:"vendor_ref_currency"`
	FXUsed           *float64 `json:"fx_used"`
	// C2 主列：绑定同区地板（原生零 FX）+ 区域名
	MarketPosition  *float64 `json:"market_position"`
	MarketRefIn     *float64 `json:"market_ref_in_per_1m"`
	MarketRefOut    *float64 `json:"market_ref_out_per_1m"`
	MarketRefID     string   `json:"market_ref_platform_id"`
	MarketRefCount  int      `json:"market_ref_platform_count"`
	MarketRefUsed   string   `json:"market_ref_used"`
	MarketAdvantage *float64 `json:"market_advantage_score"`
	VsOverseas      *float64 `json:"market_position_vs_overseas"`
	VsDomestic      *float64 `json:"market_position_vs_domestic"`
	ComputedAt      string   `json:"computed_at"`
	// 双地板对拍（mpr，各按原生币种；平台数 <3 ⇒ 参考值）
	OverseasIn       *float64 `json:"overseas_min_in_per_1m"`
	OverseasOut      *float64 `json:"overseas_min_out_per_1m"`
	OverseasID       string   `json:"overseas_min_platform_id"`
	OverseasCount    int      `json:"overseas_min_platform_count"`
	DomesticIn       *float64 `json:"domestic_min_in_per_1m"`
	DomesticOut      *float64 `json:"domestic_min_out_per_1m"`
	DomesticID       string   `json:"domestic_min_platform_id"`
	DomesticCount    int      `json:"domestic_min_platform_count"`
	VendorListCur    string   `json:"vendor_list_currency"`
	VendorListIn     *float64 `json:"vendor_list_native_in_per_1m"`
	VendorListOut    *float64 `json:"vendor_list_native_out_per_1m"`
	VendorListCNYIn  *float64 `json:"vendor_list_cny_in_per_1m"`
	VendorListCNYOut *float64 `json:"vendor_list_cny_out_per_1m"`
	MPRFXRate        *float64 `json:"mpr_fx_rate"`
}

// handleMarketCompare GET /api/admin/market/compare?q=&confidence=&limit=
func (h *Handler) handleMarketCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := h.marketPlatformsQuerier()
	if q == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	// 表缺席（959 未应用 / 评分 job 未跑过 ⇒ smp 空但存在；表本身缺席才是
	// available:false）⇒ 先探 to_regclass，缺席按「为什么空」语义返回。
	var present bool
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := q.QueryRow(ctx,
		`SELECT to_regclass('public.supplier_market_position') IS NOT NULL`).Scan(&present); err != nil {
		writeInternalErr(w, "market compare probe", err)
		return
	}
	if !present {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "supplier_market_position not present (migration 959 / scoring job not applied)",
			"items":     []marketCompareRow{},
			"summary":   map[string]int64{},
		})
		return
	}

	query := r.URL.Query()
	confidence := query.Get("confidence")
	qtext := query.Get("q")
	limit, _ := strconv.Atoi(query.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	rows, err := q.Query(ctx, `
SELECT s.credential_id, s.provider_model_id, s.canonical_name, s.currency,
       s.supplier_in_per_1m, s.supplier_out_per_1m, s.price_confidence,
       s.discount_vs_vendor, s.vendor_ref_in_per_1m, s.vendor_ref_out_per_1m,
       s.vendor_ref_currency, s.fx_used,
       s.market_position, s.market_ref_in_per_1m, s.market_ref_out_per_1m,
       s.market_ref_platform_id, s.market_ref_platform_count, s.market_ref_used,
       s.market_advantage_score, s.market_position_vs_overseas,
       s.market_position_vs_domestic, s.computed_at,
       mpr.overseas_min_in_per_1m, mpr.overseas_min_out_per_1m,
       mpr.overseas_min_platform_id, mpr.overseas_min_platform_count,
       mpr.domestic_min_in_per_1m, mpr.domestic_min_out_per_1m,
       mpr.domestic_min_platform_id, mpr.domestic_min_platform_count,
       COALESCE(mpr.vendor_list_currency, ''),
       mpr.vendor_list_in_per_1m, mpr.vendor_list_out_per_1m,
       mpr.vendor_list_cny_in_per_1m, mpr.vendor_list_cny_out_per_1m,
       mpr.fx_rate
  FROM public.supplier_market_position s
LEFT JOIN LATERAL (
    SELECT m.canonical_id, m.vendor_list_currency,
           m.vendor_list_in_per_1m, m.vendor_list_out_per_1m,
           m.vendor_list_cny_in_per_1m, m.vendor_list_cny_out_per_1m,
           m.fx_rate,
           m.overseas_min_in_per_1m, m.overseas_min_out_per_1m,
           m.overseas_min_platform_id, m.overseas_min_platform_count,
           m.domestic_min_in_per_1m, m.domestic_min_out_per_1m,
           m.domestic_min_platform_id, m.domestic_min_platform_count
      FROM public.market_price_reference m
     WHERE m.dimension = 'standard'
       AND lower(m.canonical_name) = lower(s.canonical_name)
     ORDER BY (m.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true
 WHERE ($1 = '' OR s.canonical_name ILIKE '%' || $1 || '%')
   AND ($2 = '' OR s.price_confidence = $2)
 ORDER BY s.price_confidence, s.canonical_name, s.credential_id
 LIMIT $3`, qtext, confidence, limit)
	if err != nil {
		writeInternalErr(w, "market compare query", err)
		return
	}
	defer rows.Close()
	items := make([]marketCompareRow, 0, limit)
	for rows.Next() {
		var it marketCompareRow
		var computedAt time.Time
		var supplierIn, supplierOut, discountVsVendor sql.NullFloat64
		var vendorRefIn, vendorRefOut, fxUsed sql.NullFloat64
		var marketPosition, marketRefIn, marketRefOut sql.NullFloat64
		var advantage, vsOverseas, vsDomestic sql.NullFloat64
		var ovrIn, ovrOut, domIn, domOut sql.NullFloat64
		var vlIn, vlOut, vlCNYIn, vlCNYOut, mprFX sql.NullFloat64
		if err := rows.Scan(&it.CredentialID, &it.ModelID, &it.CanonicalName, &it.Currency,
			&supplierIn, &supplierOut, &it.PriceConfidence,
			&discountVsVendor, &vendorRefIn, &vendorRefOut,
			&it.VendorRefCur, &fxUsed,
			&marketPosition, &marketRefIn, &marketRefOut,
			&it.MarketRefID, &it.MarketRefCount, &it.MarketRefUsed,
			&advantage, &vsOverseas, &vsDomestic, &computedAt,
			&ovrIn, &ovrOut, &it.OverseasID, &it.OverseasCount,
			&domIn, &domOut, &it.DomesticID, &it.DomesticCount,
			&it.VendorListCur,
			&vlIn, &vlOut, &vlCNYIn, &vlCNYOut, &mprFX); err != nil {
			writeInternalErr(w, "market compare scan", err)
			return
		}
		it.SupplierIn = nullF(supplierIn)
		it.SupplierOut = nullF(supplierOut)
		it.DiscountVsVendor = nullF(discountVsVendor)
		it.VendorRefIn = nullF(vendorRefIn)
		it.VendorRefOut = nullF(vendorRefOut)
		it.FXUsed = nullF(fxUsed)
		it.MarketPosition = nullF(marketPosition)
		it.MarketRefIn = nullF(marketRefIn)
		it.MarketRefOut = nullF(marketRefOut)
		it.MarketAdvantage = nullF(advantage)
		it.VsOverseas = nullF(vsOverseas)
		it.VsDomestic = nullF(vsDomestic)
		it.OverseasIn = nullF(ovrIn)
		it.OverseasOut = nullF(ovrOut)
		it.DomesticIn = nullF(domIn)
		it.DomesticOut = nullF(domOut)
		it.VendorListIn = nullF(vlIn)
		it.VendorListOut = nullF(vlOut)
		it.VendorListCNYIn = nullF(vlCNYIn)
		it.VendorListCNYOut = nullF(vlCNYOut)
		it.MPRFXRate = nullF(mprFX)
		it.ComputedAt = computedAt.UTC().Format(time.RFC3339)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "market compare rows", err)
		return
	}

	summary := map[string]int64{}
	sumRows, err := q.Query(ctx, `
SELECT price_confidence, count(*)
  FROM public.supplier_market_position
 GROUP BY price_confidence`)
	if err != nil {
		// 降级可见性（2026-11-13 12h 修订审计轮补：此前静默吞成空 map，前端把
		// 「API 失败」显示成「0 覆盖」——误导排查方向）。主件（评分行）已到手，
		// summary 失败不构成 5xx 的理由，降级 + WARN 留痕。
		slog.Warn("admin/market/compare: summary query failed (degrading to empty summary)", "error", err)
	} else {
		defer sumRows.Close()
		for sumRows.Next() {
			var k string
			var n int64
			if err := sumRows.Scan(&k, &n); err == nil {
				summary[k] = n
			}
		}
		// 终态检查（rowsguard 钉测抓住的缺口，2026-11-13 48h 审计轮补；
		// 12h 修订审计轮合并两方：取并行侧 fail-closed 形态——截断的 summary
		// 会被当成完整分档下发（「少了一档」与「真的没有那一档」不可区分），
		// 缺口直接 影响前端分档摘要的完备性 ⇒ available:false；另补 WARN 留痕）。
		if serr := sumRows.Err(); serr != nil {
			slog.Warn("admin/market/compare: summary rows iteration failed (fail-closed available:false)", "error", serr)
			writeJSON(w, http.StatusOK, map[string]any{
				"available": false,
				"reason":    "summary rows iteration failed",
				"error":     serr.Error(),
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"items":     items,
		"summary":   summary,
	})
}
