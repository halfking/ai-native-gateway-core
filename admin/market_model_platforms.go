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
	RunDate        string   `json:"run_date"`
	SourceURL      string   `json:"source_url"`
}

const marketPlatformQuotesSQL = `
	SELECT canonical_name, platform_id, platform_name, region, native_currency,
	       is_vendor, vendor, currency,
	       input_per_1m, output_per_1m, cny_in_per_1m, cny_out_per_1m,
	       run_date::text, source_url
	FROM market_price_platform_quotes
	WHERE dimension = $1
	  AND ($2 = '' OR lower(canonical_name) = lower($2)
	       OR canonical_name ILIKE '%' || $2 || '%')
	ORDER BY is_vendor DESC, region, platform_id
	LIMIT $3
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
		if err := rows.Scan(
			&rrow.CanonicalName, &rrow.PlatformID, &rrow.PlatformName,
			&rrow.Region, &rrow.NativeCurrency, &rrow.IsVendor, &rrow.Vendor,
			&rrow.Currency, &inN, &outN, &cnyIn, &cnyOut, &rrow.RunDate, &rrow.SourceURL,
		); err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		rrow.InputPer1M = nullF(inN)
		rrow.OutputPer1M = nullF(outN)
		rrow.CNYInPer1M = nullF(cnyIn)
		rrow.CNYOutPer1M = nullF(cnyOut)
		out = append(out, rrow)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"items":     out,
		"model":     model,
		"dimension": dim,
	})
}
