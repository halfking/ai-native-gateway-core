package admin

// market_model_platforms.go — GET /api/admin/market/model-platforms
// 运营定价参考：按模型列出各平台现价（970 market_price_platform_quotes）。
// 对齐 salesdecorate Compare 现价模式；只读；表缺席 ⇒ 200 + available:false。
// NULL≠0：无价列走 NullFloat64 → nil。

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type marketPlatformQuoteRow struct {
	CanonicalName  string   `json:"canonical_name"`
	PlatformID     string   `json:"platform_id"`
	PlatformName   string   `json:"platform_name"`
	Region         string   `json:"region"`
	NativeCurrency string   `json:"native_currency"`
	IsVendor       bool     `json:"is_vendor"`
	Vendor         string   `json:"vendor"`
	Currency       string   `json:"currency"`
	InputPer1M     *float64 `json:"input_per_1m"`
	OutputPer1M    *float64 `json:"output_per_1m"`
	CNYInPer1M     *float64 `json:"cny_in_per_1m"`
	CNYOutPer1M    *float64 `json:"cny_out_per_1m"`
	// ★ 2026-11-17（972 迁移）：缓存命中价（真实成本的大头，此前网关侧无此列）。
	CacheHitPer1M     *float64 `json:"cache_hit_per_1m"`
	CNYCacheHitPer1M  *float64 `json:"cny_cache_hit_per_1m"`
	// 阶梯（上下文档位 / Priority / Flex…）：972 起与 PK 同列，阶梯价不再互相覆盖。
	Tier string `json:"tier"`
	// 模型类别（文本/对话 / 多模态 / 推理 / 向量…）：供读面筛选。
	ModelKind string `json:"model_kind"`
	RunDate   string `json:"run_date"`
	SourceURL string `json:"source_url"`
}

// marketPlatformQuoteDirectoryRow —— 「按模型跨平台现价」的**模型目录**
// （没有指定 model 时返回）：供前端做模型选择器，不再要求运营手打 canonical 名。
//
// ★ 口径（对齐 salesdecorate Compare 的比价目录）：
//   · 排序 = **可比平台数降序**（平台多的排前面 ⇒ 可比性强的排前面），
//     同数按模型名；
//   · 只收「至少 1 个平台有价」的模型；
//   · 最低价只算**非原厂**行（原厂是基准锚，不是竞品 —— 同 salesdecorate
//     的极值统计口径）；原厂行数单独给出来。
type marketPlatformQuoteDirectoryRow struct {
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	Platforms     int      `json:"platforms"`
	VendorRows    int      `json:"vendor_rows"`
	MinCNYIn      *float64 `json:"min_cny_in_per_1m"`
	MinCNYOut     *float64 `json:"min_cny_out_per_1m"`
	RunDate       string   `json:"run_date"`
}

const marketPlatformQuotesSQL = `
	SELECT canonical_name, platform_id, platform_name, region, native_currency,
	       is_vendor, vendor, currency,
	       input_per_1m, output_per_1m, cny_in_per_1m, cny_out_per_1m,
	       cache_hit_per_1m, cny_cache_hit_per_1m,
	       tier, model_kind,
	       run_date::text, source_url
	FROM market_price_platform_quotes
	WHERE dimension = $1
	  AND ($2 = '' OR lower(canonical_name) = lower($2)
	       OR canonical_name ILIKE '%' || $2 || '%')
	ORDER BY is_vendor DESC, region, platform_id
	LIMIT $3
`

// marketPlatformQuoteDirectorySQL —— 模型目录（没有指定 model 时用）。
const marketPlatformQuoteDirectorySQL = `
	SELECT q.canonical_name,
	       COALESCE(NULLIF(TRIM(mc.display_name), ''), q.canonical_name, ''),
	       count(DISTINCT q.platform_id) FILTER (WHERE NOT q.is_vendor),
	       count(DISTINCT q.platform_id) FILTER (WHERE q.is_vendor),
	       min(q.cny_in_per_1m)  FILTER (WHERE NOT q.is_vendor),
	       min(q.cny_out_per_1m) FILTER (WHERE NOT q.is_vendor),
	       max(q.run_date)::text
	  FROM market_price_platform_quotes q
	  LEFT JOIN models_canonical mc ON lower(mc.canonical_name) = lower(q.canonical_name)
	 WHERE q.dimension = $1
	 GROUP BY q.canonical_name, mc.display_name
	HAVING count(DISTINCT q.platform_id) FILTER (WHERE NOT q.is_vendor) > 0
	    OR count(DISTINCT q.platform_id) FILTER (WHERE q.is_vendor) > 0
	 ORDER BY 3 DESC, 1
	 LIMIT $2
`

// handleMarketModelPlatforms GET /api/admin/market/model-platforms?model=&dimension=&limit=
func (h *Handler) handleMarketModelPlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := h.marketPlatformsQuerier()
	if q == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var present bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('public.market_price_platform_quotes') IS NOT NULL").Scan(&present); err != nil || !present {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "market_price_platform_quotes missing (apply migration 970 + re-run market-price-sync)",
			"items":     []any{},
		})
		return
	}

	qs := r.URL.Query()
	model := strings.TrimSpace(qs.Get("model"))
	dim := qs.Get("dimension")
	if dim == "" {
		dim = "standard"
	}
	limit := 200
	if v := qs.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := q.Query(ctx, marketPlatformQuotesSQL, dim, model, limit)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	defer rows.Close()

	out := make([]marketPlatformQuoteRow, 0, 32)
	for rows.Next() {
		var rrow marketPlatformQuoteRow
		var inN, outN, cnyIn, cnyOut sql.NullFloat64
		var cacheN, cnyCacheN sql.NullFloat64
		if err := rows.Scan(
			&rrow.CanonicalName, &rrow.PlatformID, &rrow.PlatformName,
			&rrow.Region, &rrow.NativeCurrency, &rrow.IsVendor, &rrow.Vendor,
			&rrow.Currency, &inN, &outN, &cnyIn, &cnyOut,
			&cacheN, &cnyCacheN, &rrow.Tier, &rrow.ModelKind,
			&rrow.RunDate, &rrow.SourceURL,
		); err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		rrow.InputPer1M = nullF(inN)
		rrow.OutputPer1M = nullF(outN)
		rrow.CNYInPer1M = nullF(cnyIn)
		rrow.CNYOutPer1M = nullF(cnyOut)
		rrow.CacheHitPer1M = nullF(cacheN)
		rrow.CNYCacheHitPer1M = nullF(cnyCacheN)
		out = append(out, rrow)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}

	// ★ 2026-11-17：**没指定 model ⇒ 返回模型目录**（而不是全量明细）。
	//   旧行为把「没填模型」当成「查全部」⇒ 前端拿到 200 条明细当目录用 ⇒
	//   用户必须手打 canonical 名才能用（实测：这是该页最刺眼的可用性缺口）。
	directory := make([]marketPlatformQuoteDirectoryRow, 0, 64)
	if model == "" {
		drows, derr := q.Query(ctx, marketPlatformQuoteDirectorySQL, dim, limit)
		if derr != nil {
			writeInternalErr(w, "internal error (see server logs)", derr)
			return
		}
		defer drows.Close()
		for drows.Next() {
			var d marketPlatformQuoteDirectoryRow
			var minIn, minOut sql.NullFloat64
			if err := drows.Scan(&d.CanonicalName, &d.DisplayName, &d.Platforms,
				&d.VendorRows, &minIn, &minOut, &d.RunDate); err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
			d.MinCNYIn = nullF(minIn)
			d.MinCNYOut = nullF(minOut)
			directory = append(directory, d)
		}
		if err := drows.Err(); err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available":  true,
		"items":      out,
		"model":      model,
		"dimension":  dim,
		"directory":  directory,
		"is_dir":     model == "",
	})
}
