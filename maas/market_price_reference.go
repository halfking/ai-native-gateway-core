package maas

// market_price_reference.go —— 940 行情面的【只读】网关侧消费（2026-11-10）。
//
// 数据来源：scripts/market-price-sync.py（宿主侧单向同步，salesdecorate
// price_observation → market_price_reference）。本文件只做同库读与对比
// 计算，绝不写任何定价/路由表 —— 行情价禁止回流路由（2026-11-03 方案档
// 红线 §4.4；940 迁移头注）。
//
// 五价坐标系（方案档 §4）：P1 原厂锚（vendor_list_*）与 P2 市场最低
// （market_min_cny_*）由同步器落库；P3 我方实付（credential_model_bindings
// 的 min unit_price）与 P5 我方零售（model_credit_rates manual → 全局有效价，
// 语义与 maas/rates.go effectiveModelRates 逐字同源）在本文件实时 JOIN；
// 比值在 Go 侧计算，币种不匹配不默认折算（NULL≠0、币种未知拒绝 两红线沿用）。

import (
	"context"
	"encoding/json"
	"errors"
	"math"
)

// MarketPriceOverviewRow is one model's market-reference vs our-price view.
type MarketPriceOverviewRow struct {
	CanonicalName string `json:"canonical_name"`
	CanonicalID   *int64 `json:"canonical_id"`
	DisplayName   string `json:"display_name"`
	// P2 市场最低（CNY，同步器落库；platform_count <3 时为参考值）
	MarketMinInCNY   *float64 `json:"market_min_in_cny_per_1m"`
	MarketMinOutCNY  *float64 `json:"market_min_out_cny_per_1m"`
	MarketPlatforms  int      `json:"market_min_platform_count"`
	MarketPlatformID string   `json:"market_min_platform_id"`
	// P1 原厂锚（CNY；NULL=无原厂价，≠0=原厂确认免费 —— NULL≠0 红线）
	VendorListInCNY  *float64 `json:"vendor_list_in_cny_per_1m"`
	VendorListOutCNY *float64 `json:"vendor_list_out_cny_per_1m"`
	VendorVendor     string   `json:"vendor_vendor"`
	// P3 我方实付（绑定 min 单价；币种如实携带，不默认折算）
	SupplierIn  *float64 `json:"supplier_in_per_1m"`
	SupplierOut *float64 `json:"supplier_out_per_1m"`
	SupplierCur string   `json:"supplier_currency"`
	// P5 我方零售（语义与 maas/rates.go 同源：manual>0 优先，否则全局有效价）
	RetailCreditsIn  *int64   `json:"retail_credits_in_per_1m"`
	RetailCreditsOut *int64   `json:"retail_credits_out_per_1m"`
	RetailCNYIn      *float64 `json:"retail_cny_in_per_1m"`
	RetailCNYOut     *float64 `json:"retail_cny_out_per_1m"`
	CentsPerCredit   float64  `json:"cents_per_credit"`
	CurrencyDisplay  string   `json:"currency_display"`
	// 派生比值（Go 侧计算；NULL = 币种/数据不足以成立，不默认）：
	//   C2 市场位势 = 市场最低 CNY / 我方实付 CNY（>1 = 我们买得比市场便宜）；
	//     仅当实付币种为 CNY 时成立（USD 实付不默认折算）。
	MarketPosition *float64 `json:"market_position"`
	//   零售溢价 = 我方零售 CNY / 市场最低 CNY（仅 currency_display=CNY 时成立）。
	RetailVsMarket *float64 `json:"retail_vs_market"`
	// 行情元数据
	SourceRunDate string `json:"source_run_date"`
	Resolved      bool   `json:"resolved"`
}

// MarketPriceSyncRun is one sync-ledger entry (940 market_price_reference_sync).
type MarketPriceSyncRun struct {
	ID              int64          `json:"id"`
	RunDate         string         `json:"run_date"`
	SourcePlatforms int            `json:"source_platforms"`
	SourceRows      int            `json:"source_rows"`
	LandedRows      int            `json:"landed_rows"`
	RejectedRows    int            `json:"rejected_rows"`
	Conflicts       map[string]any `json:"conflicts"`
	Detail          map[string]any `json:"detail"`
	CreatedAt       string         `json:"created_at"`
}

const marketPriceOverviewSQL = `
	SELECT m.canonical_name, m.canonical_id, COALESCE(mc.display_name, ''),
	       m.market_min_cny_in_per_1m, m.market_min_cny_out_per_1m,
	       m.market_min_platform_count, m.market_min_platform_id,
	       m.vendor_list_in_per_1m, m.vendor_list_out_per_1m, m.vendor_vendor,
	       s.s_in, s.s_out, s.s_cur,
	       r.credits_per_1m_in, r.credits_per_1m_out,
	       m.source_run_date::text
	FROM market_price_reference m
	LEFT JOIN models_canonical mc ON mc.id = m.canonical_id
	LEFT JOIN LATERAL (
	    SELECT min(b.unit_price_in_per_1m)  AS s_in,
	           min(b.unit_price_out_per_1m) AS s_out,
	           min(b.currency)              AS s_cur
	    FROM credential_model_bindings b
	    JOIN provider_models pm ON pm.id = b.provider_model_id
	    WHERE pm.canonical_id = m.canonical_id
	      AND COALESCE(b.unit_price_in_per_1m, 0) > 0
	      AND COALESCE(b.unit_price_out_per_1m, 0) > 0
	) s ON m.canonical_id IS NOT NULL
	LEFT JOIN model_credit_rates r ON r.canonical_id = m.canonical_id
	WHERE ($1 = '' OR m.canonical_name ILIKE '%' || $1 || '%'
	       OR COALESCE(mc.display_name, '') ILIKE '%' || $1 || '%')
	  AND ($2 = FALSE OR m.canonical_id IS NOT NULL)
	ORDER BY m.canonical_id IS NULL, m.canonical_name
	LIMIT $3
`

// MarketPriceOverview lists market-reference rows joined with our retail and
// supplier prices. Read-only over the 940 sync target — no pricing writes.
func (s *Service) MarketPriceOverview(ctx context.Context, q string, onlyResolved bool, limit int) ([]MarketPriceOverviewRow, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	global := globalEffective(settings)

	rows, err := s.pool.Query(ctx, marketPriceOverviewSQL, q, onlyResolved, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MarketPriceOverviewRow, 0, limit)
	for rows.Next() {
		var r MarketPriceOverviewRow
		var manualIn, manualOut *int64
		if err := rows.Scan(
			&r.CanonicalName, &r.CanonicalID, &r.DisplayName,
			&r.MarketMinInCNY, &r.MarketMinOutCNY,
			&r.MarketPlatforms, &r.MarketPlatformID,
			&r.VendorListInCNY, &r.VendorListOutCNY, &r.VendorVendor,
			&r.SupplierIn, &r.SupplierOut, &r.SupplierCur,
			&manualIn, &manualOut,
			&r.SourceRunDate,
		); err != nil {
			return nil, err
		}
		r.Resolved = r.CanonicalID != nil

		// P5 我方零售：语义与 effectiveModelRates 逐字同源（manual>0 优先，
		// 否则全局有效价 = ceil(base × discount)，out 回退 in）。
		retailIn := pickCreditRate(manualIn, global.In)
		retailOut := pickCreditRate(manualOut, global.Out)
		r.RetailCreditsIn = retailIn
		r.RetailCreditsOut = retailOut

		// 零售折 CNY：credits × cents_per_credit / 100（分→元），仅在计费
		// 币种为 CNY 时成立（币种未知拒绝不默认折算）。
		if settings.CurrencyDisplay == "CNY" && settings.CentsPerCredit > 0 {
			if retailIn != nil {
				v := float64(*retailIn) * settings.CentsPerCredit / 100.0
				r.RetailCNYIn = &v
			}
			if retailOut != nil {
				v := float64(*retailOut) * settings.CentsPerCredit / 100.0
				r.RetailCNYOut = &v
			}
		}
		r.CentsPerCredit = settings.CentsPerCredit
		r.CurrencyDisplay = settings.CurrencyDisplay

		// C2 市场位势 = 市场最低 CNY / 我方实付 CNY（>1 = 我们买得比市场
		// 便宜）。实付币种仅 CNY 成立；USD 实付不默认折算（无 fx 列读取）。
		if r.SupplierCur == "CNY" {
			if r.MarketMinOutCNY != nil && r.SupplierOut != nil && *r.SupplierOut > 0 {
				v := *r.MarketMinOutCNY / *r.SupplierOut
				r.MarketPosition = &v
			}
		}
		// 零售溢价 = 我方零售 CNY / 市场最低 CNY。
		if r.RetailCNYOut != nil && r.MarketMinOutCNY != nil && *r.MarketMinOutCNY > 0 {
			v := *r.RetailCNYOut / *r.MarketMinOutCNY
			r.RetailVsMarket = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pickCreditRate mirrors maas/rates.go effectiveModelRates.pick：
// 手工价（>0）优先，否则全局有效价；两态皆无 ⇒ NULL（未知价 ≠ 免费）。
func pickCreditRate(manual *int64, global int64) *int64 {
	if manual != nil && *manual > 0 {
		return manual
	}
	if global > 0 {
		v := global
		return &v
	}
	return nil
}

// MarketPriceSyncRuns returns the latest sync-ledger entries (可对账).
func (s *Service) MarketPriceSyncRuns(ctx context.Context, limit int) ([]MarketPriceSyncRun, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_date::text, source_platforms, source_rows,
		       landed_rows, rejected_rows, conflicts, detail, created_at::text
		FROM market_price_reference_sync
		ORDER BY id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MarketPriceSyncRun, 0, limit)
	for rows.Next() {
		var r MarketPriceSyncRun
		var conflicts, detail []byte
		if err := rows.Scan(&r.ID, &r.RunDate, &r.SourcePlatforms, &r.SourceRows,
			&r.LandedRows, &r.RejectedRows, &conflicts, &detail, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Conflicts = map[string]any{}
		if len(conflicts) > 0 {
			_ = json.Unmarshal(conflicts, &r.Conflicts)
		}
		r.Detail = map[string]any{}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &r.Detail)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// marketRetailCNYForTest re-exports the retail-CNY math used by tests.
func marketRetailCNYForTest(credits int64, centsPerCredit float64) float64 {
	return math.Max(0, float64(credits)*centsPerCredit/100.0)
}
