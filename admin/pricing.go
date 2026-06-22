package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) handlePricing(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// Block write operations for tenant_admin (import, bulk-update, copy, auto-inherit)
	writeEndpoints := map[string]bool{
		"import":         true,
		"bulk-update":    true,
		"copy":           true,
		"auto-inherit":   true,
	}

	remaining := r.URL.Path[len("/api/pricing/"):]
	if writeEndpoints[remaining] {
		if RequireSuperAdminForWrite(w, r) {
			return
		}
	}

	switch {
	case remaining == "tree":
		h.pricingTree(w, r)
	case remaining == "summary":
		h.pricingSummary(w, r)
	case remaining == "bulk-update":
		h.pricingBulkUpdate(w, r)
	case remaining == "export":
		h.pricingExport(w, r)
	case remaining == "import":
		h.pricingImport(w, r)
	case remaining == "table":
		h.pricingTable(w, r)
	case remaining == "stats/window":
		h.pricingStatsWindow(w, r)
	case remaining == "copy":
		h.pricingCopy(w, r)
	case remaining == "auto-inherit":
		h.pricingAutoInherit(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) pricingTree(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	where := "WHERE 1=1"
	args := []any{}
	argIdx := 1

	if v := queryString(r, "family"); v != "" {
		where += fmt.Sprintf(" AND mc.family = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v := queryString(r, "provider_id"); v != "" {
		if pid, err := strconv.Atoi(v); err == nil {
			where += fmt.Sprintf(" AND p.id = $%d", argIdx)
			args = append(args, pid)
			argIdx++
		}
	}
	if v := queryString(r, "billing_mode"); v != "" {
		where += fmt.Sprintf(" AND mo.billing_mode = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v := queryString(r, "currency"); v != "" {
		where += fmt.Sprintf(" AND mo.currency = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if queryString(r, "availability") == "true" {
		where += " AND mo.available = TRUE"
	}
	if v := queryString(r, "pricing_status"); v != "" {
		switch v {
		case "priced":
			where += " AND mo.unit_price_in_per_1m IS NOT NULL"
		case "unpriced":
			where += " AND mo.unit_price_in_per_1m IS NULL"
		case "free":
			where += " AND mo.billing_mode = 'free'"
		}
	}
	if v := queryString(r, "search"); v != "" {
		where += fmt.Sprintf(" AND (mc.canonical_name ILIKE '%%' || $%d || '%%' OR mo.raw_model_name ILIKE '%%' || $%d || '%%')", argIdx, argIdx)
		args = append(args, v)
		argIdx++
	}

	query := fmt.Sprintf(`
		SELECT
			mc.id AS canonical_id, mc.canonical_name, mc.family, mc.modality,
			mo.id AS offer_id, mo.raw_model_name,
			mo.unit_price_in_per_1m, mo.unit_price_out_per_1m,
			mo.cache_read_price_per_1m, mo.cache_write_price_per_1m,
			mo.currency, mo.billing_mode, mo.pricing_source, mo.pricing_updated_at,
			mo.available, mo.routing_tier, mo.weight, mo.last_seen_at,
			COALESCE(mo.success_rate, 0.9)::float8, COALESCE(mo.p95_latency_ms, 9999)::int,
			c.id AS credential_id, c.label AS credential_label, c.status AS credential_status,
			c.balance_usd, c.pool_group,
			p.id AS provider_id, p.display_name AS provider_name,
			COALESCE(pc.code, '') AS catalog_code, COALESCE(pc.display_name, '') AS catalog_display_name
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = p.catalog_code
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		%s
		ORDER BY mc.family, mc.canonical_name, p.display_name, c.label
	`, where)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type offerEntry struct {
		OfferID           int      `json:"offer_id"`
		RawModelName      string   `json:"raw_model_name"`
		PriceInPer1M      *float64 `json:"unit_price_in_per_1m"`
		PriceOutPer1M     *float64 `json:"unit_price_out_per_1m"`
		CacheReadPrice    *float64 `json:"cache_read_price_per_1m"`
		CacheWritePrice   *float64 `json:"cache_write_price_per_1m"`
		Currency          *string  `json:"currency"`
		BillingMode       *string  `json:"billing_mode"`
		PricingSource     *string  `json:"pricing_source"`
		PricingUpdatedAt  *time.Time `json:"pricing_updated_at"`
		Available         bool     `json:"available"`
		RoutingTier       *int     `json:"routing_tier"`
		Weight            *int     `json:"weight"`
		LastSeenAt        *time.Time `json:"last_seen_at"`
		SuccessRate       float64  `json:"success_rate"`
		P95LatencyMs      int      `json:"p95_latency_ms"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   string   `json:"credential_label"`
		CredentialStatus  string   `json:"credential_status"`
		BalanceUSD        *float64 `json:"balance_usd"`
		PoolGroup         *string  `json:"pool_group"`
		ProviderID        int      `json:"provider_id"`
		ProviderName      string   `json:"provider_name"`
		CatalogCode       string   `json:"catalog_code"`
		CatalogDisplayName string  `json:"catalog_display_name"`
	}

	type familyGroup struct {
		CanonicalID   int          `json:"canonical_id"`
		CanonicalName string       `json:"canonical_name"`
		Family        string       `json:"family"`
		Modality      string       `json:"modality"`
		Offers        []offerEntry `json:"offers"`
	}

	familyMap := map[string]*familyGroup{}
	for rows.Next() {
		var o offerEntry
		var canonID *int
		var canonName, family, modality *string

		if err := rows.Scan(&canonID, &canonName, &family, &modality,
			&o.OfferID, &o.RawModelName,
			&o.PriceInPer1M, &o.PriceOutPer1M,
			&o.CacheReadPrice, &o.CacheWritePrice,
			&o.Currency, &o.BillingMode, &o.PricingSource, &o.PricingUpdatedAt,
			&o.Available, &o.RoutingTier, &o.Weight, &o.LastSeenAt,
			&o.SuccessRate, &o.P95LatencyMs,
			&o.CredentialID, &o.CredentialLabel, &o.CredentialStatus,
			&o.BalanceUSD, &o.PoolGroup,
			&o.ProviderID, &o.ProviderName,
			&o.CatalogCode, &o.CatalogDisplayName); err != nil {
			continue
		}

		cn := o.RawModelName
		if canonName != nil {
			cn = *canonName
		}
		fm := "Unclassified"
		if family != nil && *family != "" {
			fm = *family
		}

		key := cn + "|" + fm
		if fg, ok := familyMap[key]; ok {
			fg.Offers = append(fg.Offers, o)
		} else {
			cid := 0
			if canonID != nil {
				cid = *canonID
			}
			md := "text"
			if modality != nil && *modality != "" {
				md = *modality
			}
			familyMap[key] = &familyGroup{
				CanonicalID:   cid,
				CanonicalName: cn,
				Family:        fm,
				Modality:      md,
				Offers:        []offerEntry{o},
			}
		}
	}

	families := make([]familyGroup, 0, len(familyMap))
	for _, fg := range familyMap {
		families = append(families, *fg)
	}

	writeJSON(w, http.StatusOK, map[string]any{"families": families})
}

func (h *Handler) pricingSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var summary struct {
		TotalOffers       int `json:"total_offers"`
		PricedIn          int `json:"priced_in"`
		PricedOut         int `json:"priced_out"`
		PricedCacheRead   int `json:"priced_cache_read"`
		PricedCacheWrite  int `json:"priced_cache_write"`
		CNYOffers         int `json:"cny_offers"`
		USDOffers         int `json:"usd_offers"`
		FreeOffers        int `json:"free_offers"`
		CanonicalCovered  int `json:"canonical_covered"`
		TotalCanonical    int `json:"total_canonical"`
		FreeCredentials   int `json:"free_credentials"`
	}
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT
			COUNT(DISTINCT mo.id),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.unit_price_in_per_1m IS NOT NULL),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.unit_price_out_per_1m IS NOT NULL),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.cache_read_price_per_1m IS NOT NULL),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.cache_write_price_per_1m IS NOT NULL),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.currency = 'CNY'),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.currency = 'USD'),
			COUNT(DISTINCT mo.id) FILTER (WHERE mo.billing_mode = 'free'),
			COUNT(DISTINCT mc.id),
			COUNT(DISTINCT mc.id) FILTER (WHERE mc.id IS NOT NULL),
			COUNT(DISTINCT c.id) FILTER (WHERE c.pool_group = 'free')
		FROM model_offers mo
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		LEFT JOIN credentials c ON c.id = mo.credential_id
	`).Scan(
		&summary.TotalOffers, &summary.PricedIn, &summary.PricedOut,
		&summary.PricedCacheRead, &summary.PricedCacheWrite,
		&summary.CNYOffers, &summary.USDOffers, &summary.FreeOffers,
		&summary.CanonicalCovered, &summary.TotalCanonical, &summary.FreeCredentials,
	)

	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) pricingBulkUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Updates []struct {
			OfferID           int      `json:"offer_id"`
			UnitPriceInPer1M  *float64 `json:"unit_price_in_per_1m"`
			UnitPriceOutPer1M *float64 `json:"unit_price_out_per_1m"`
			CacheReadPrice    *float64 `json:"cache_read_price_per_1m"`
			CacheWritePrice   *float64 `json:"cache_write_price_per_1m"`
			Currency          *string  `json:"currency"`
			BillingMode       *string  `json:"billing_mode"`
		} `json:"updates"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	updated := 0
	for _, u := range req.Updates {
		setClauses := []string{"pricing_source = 'manual'", "pricing_updated_at = now()"}
		args := []any{}
		argIdx := 2 // $1 is offer_id

		if u.UnitPriceInPer1M != nil {
			setClauses = append(setClauses, fmt.Sprintf("unit_price_in_per_1m = $%d", argIdx))
			args = append(args, *u.UnitPriceInPer1M)
			argIdx++
		}
		if u.UnitPriceOutPer1M != nil {
			setClauses = append(setClauses, fmt.Sprintf("unit_price_out_per_1m = $%d", argIdx))
			args = append(args, *u.UnitPriceOutPer1M)
			argIdx++
		}
		if u.CacheReadPrice != nil {
			setClauses = append(setClauses, fmt.Sprintf("cache_read_price_per_1m = $%d", argIdx))
			args = append(args, *u.CacheReadPrice)
			argIdx++
		}
		if u.CacheWritePrice != nil {
			setClauses = append(setClauses, fmt.Sprintf("cache_write_price_per_1m = $%d", argIdx))
			args = append(args, *u.CacheWritePrice)
			argIdx++
		}
		if u.Currency != nil {
			setClauses = append(setClauses, fmt.Sprintf("currency = $%d", argIdx))
			args = append(args, *u.Currency)
			argIdx++
		}
		if u.BillingMode != nil {
			setClauses = append(setClauses, fmt.Sprintf("billing_mode = $%d", argIdx))
			args = append(args, *u.BillingMode)
			argIdx++
		}

		query := fmt.Sprintf("UPDATE model_offers SET %s WHERE id = $1", strings.Join(setClauses, ", "))
		tag, err := h.db.Exec(ctx, query, append([]any{u.OfferID}, args...)...)
		if err != nil {
			continue
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

func (h *Handler) pricingExport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(mc.canonical_name, '') AS canonical_name,
			mo.raw_model_name,
			p.display_name AS provider,
			c.label AS credential,
			mo.unit_price_in_per_1m, mo.unit_price_out_per_1m,
			mo.cache_read_price_per_1m, mo.cache_write_price_per_1m,
			COALESCE(mo.currency, 'USD'), COALESCE(mo.billing_mode, 'token'),
			COALESCE(mo.pricing_source, '')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		ORDER BY mc.canonical_name, p.display_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=pricing_export.csv")
	writer := csv.NewWriter(w)
	//nolint:errcheck // HTTP write error non-recoverable
	writer.Write([]string{"canonical_name", "raw_model_name", "provider", "credential",
		"unit_price_in_per_1m", "unit_price_out_per_1m",
		"cache_read_price_per_1m", "cache_write_price_per_1m",
		"currency", "billing_mode", "pricing_source"})

	for rows.Next() {
		var canonName, rawName, prov, cred, currency, billingMode, pricingSource string
		var priceIn, priceOut, cacheRead, cacheWrite *float64
		//nolint:errcheck // best-effort
		rows.Scan(&canonName, &rawName, &prov, &cred,
			&priceIn, &priceOut, &cacheRead, &cacheWrite,
			&currency, &billingMode, &pricingSource)

		f := func(v *float64) string {
			if v == nil {
				return ""
			}
			return strconv.FormatFloat(*v, 'f', -1, 64)
		}
		//nolint:errcheck // HTTP write error non-recoverable
		writer.Write([]string{canonName, rawName, prov, cred,
			f(priceIn), f(priceOut), f(cacheRead), f(cacheWrite),
			currency, billingMode, pricingSource})
	}
	writer.Flush()
}

func (h *Handler) pricingImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	//nolint:errcheck // best-effort close
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read CSV: "+err.Error())
		return
	}

	if len(records) < 2 {
		writeError(w, http.StatusBadRequest, "CSV must have header + at least one data row")
		return
	}

	// Parse header
	header := records[0]
	colMap := map[string]int{}
	for i, h := range header {
		colMap[strings.TrimSpace(h)] = i
	}

	offerIDIdx, ok := colMap["offer_id"]
	if !ok {
		writeError(w, http.StatusBadRequest, "CSV must have 'offer_id' column")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	updated := 0
	for _, row := range records[1:] {
		if offerIDIdx >= len(row) || row[offerIDIdx] == "" {
			continue
		}
		offerID, err := strconv.Atoi(strings.TrimSpace(row[offerIDIdx]))
		if err != nil {
			continue
		}

		setClauses := []string{"pricing_source = 'imported'", "pricing_updated_at = now()"}
		args := []any{}
		argIdx := 2

		fields := map[string]string{
			"unit_price_in_per_1m":  "float",
			"unit_price_out_per_1m": "float",
			"cache_read_price_per_1m": "float",
			"cache_write_price_per_1m": "float",
			"currency":    "string",
			"billing_mode": "string",
		}

		for col, typ := range fields {
			idx, ok := colMap[col]
			if !ok || idx >= len(row) || strings.TrimSpace(row[idx]) == "" {
				continue
			}
			val := strings.TrimSpace(row[idx])
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			if typ == "float" {
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					args = append(args, f)
				} else {
					continue
				}
			} else {
				args = append(args, val)
			}
			argIdx++
		}

		query := fmt.Sprintf("UPDATE model_offers SET %s WHERE id = $1", strings.Join(setClauses, ", "))
		tag, err := h.db.Exec(ctx, query, append([]any{offerID}, args...)...)
		if err != nil {
			continue
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

func (h *Handler) pricingStatsWindow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	credFilter := queryString(r, "credential_id")

	rows, err := h.db.Query(ctx, `
		SELECT
			credential_id,
			raw_model_name,
			COALESCE(request_count, 0)::int,
			COALESCE(success_count, 0)::int,
			COALESCE(failure_count, 0)::int,
			COALESCE(success_rate, 0.0)::float8,
			latency_p50_ms,
			latency_p95_ms,
			latency_p99_ms,
			COALESCE(prompt_tokens, 0)::int,
			COALESCE(completion_tokens, 0)::int,
			COALESCE(cost_usd, 0.0)::float8
		FROM credential_model_stats_1m
		WHERE bucket >= now() - INTERVAL '10 minutes'
	`)
	if err != nil {
		// Table might not exist — return empty
		writeJSON(w, http.StatusOK, map[string]any{"stats": []any{}, "total": 0})
		return
	}
	defer rows.Close()

	type windowStat struct {
		CredentialID     int      `json:"credential_id"`
		RawModel         string   `json:"raw_model"`
		Requests         int      `json:"requests"`
		Successes        int      `json:"successes"`
		Failures         int      `json:"failures"`
		SuccessRate      float64  `json:"success_rate"`
		LatencyP50Ms     *int     `json:"latency_p50_ms"`
		LatencyP95Ms     *int     `json:"latency_p95_ms"`
		LatencyP99Ms     *int     `json:"latency_p99_ms"`
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		CostUSD          float64  `json:"cost_usd"`
	}

	stats := make([]windowStat, 0)
	credFilterInt := 0
	if credFilter != "" {
		credFilterInt, _ = strconv.Atoi(credFilter)
	}

	for rows.Next() {
		var s windowStat
		if err := rows.Scan(&s.CredentialID, &s.RawModel,
			&s.Requests, &s.Successes, &s.Failures, &s.SuccessRate,
			&s.LatencyP50Ms, &s.LatencyP95Ms, &s.LatencyP99Ms,
			&s.PromptTokens, &s.CompletionTokens, &s.CostUSD); err != nil {
			continue
		}
		if credFilterInt > 0 && s.CredentialID != credFilterInt {
			continue
		}
		s.SuccessRate = math.Round(s.SuccessRate*10000) / 10000
		s.CostUSD = math.Round(s.CostUSD*100000000) / 100000000
		stats = append(stats, s)
	}

	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "total": len(stats)})
}

func (h *Handler) pricingTable(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize

	sortBy := queryString(r, "sort_by")
	allowedSorts := map[string]bool{
		"canonical_name": true, "raw_model_name": true,
		"unit_price_in_per_1m": true, "provider_name": true,
		"success_rate": true, "p95_latency_ms": true,
	}
	if !allowedSorts[sortBy] {
		sortBy = "canonical_name"
	}
	sortDir := "ASC"
	if queryString(r, "sort_dir") == "desc" {
		sortDir = "DESC"
	}

	where := "WHERE 1=1"
	args := []any{}
	argIdx := 1

	if v := queryString(r, "family"); v != "" {
		where += fmt.Sprintf(" AND mc.family = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v := queryString(r, "provider_id"); v != "" {
		if pid, err := strconv.Atoi(v); err == nil {
			where += fmt.Sprintf(" AND p.id = $%d", argIdx)
			args = append(args, pid)
			argIdx++
		}
	}
	if v := queryString(r, "billing_mode"); v != "" {
		where += fmt.Sprintf(" AND mo.billing_mode = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if queryString(r, "availability") == "true" {
		where += " AND mo.available = TRUE"
	}
	if v := queryString(r, "pricing_status"); v != "" {
		switch v {
		case "priced":
			where += " AND mo.unit_price_in_per_1m IS NOT NULL"
		case "unpriced":
			where += " AND mo.unit_price_in_per_1m IS NULL"
		case "free":
			where += " AND mo.billing_mode = 'free'"
		}
	}
	if v := queryString(r, "search"); v != "" {
		where += fmt.Sprintf(" AND (mc.canonical_name ILIKE '%%' || $%d || '%%' OR mo.raw_model_name ILIKE '%%' || $%d || '%%')", argIdx, argIdx)
		args = append(args, v)
		argIdx++
	}

	// Count
	var total int
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id %s`, where)
	//nolint:errcheck // best-effort exec, non-critical
	h.db.QueryRow(ctx, countQuery, args...).Scan(&total)

	// Data
	dataQuery := fmt.Sprintf(`
		SELECT
			COALESCE(mc.canonical_name, mo.raw_model_name) AS canonical_name,
			mo.id AS offer_id, mo.raw_model_name,
			mo.unit_price_in_per_1m, mo.unit_price_out_per_1m,
			mo.cache_read_price_per_1m, mo.cache_write_price_per_1m,
			mo.currency, mo.billing_mode, mo.pricing_source, mo.pricing_updated_at,
			mo.available, mo.routing_tier, mo.weight,
			COALESCE(mo.success_rate, 0.9)::float8, COALESCE(mo.p95_latency_ms, 9999)::int,
			c.id AS credential_id, c.label AS credential_label, c.status AS credential_status,
			c.balance_usd, c.pool_group,
			p.id AS provider_id, p.display_name AS provider_name
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		%s ORDER BY %s %s NULLS LAST LIMIT %d OFFSET %d
	`, where, sortBy, sortDir, pageSize, offset)

	rows, err := h.db.Query(ctx, dataQuery, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type tableRow struct {
		CanonicalName     string   `json:"canonical_name"`
		OfferID           int      `json:"offer_id"`
		RawModelName      string   `json:"raw_model_name"`
		PriceInPer1M      *float64 `json:"unit_price_in_per_1m"`
		PriceOutPer1M     *float64 `json:"unit_price_out_per_1m"`
		CacheReadPrice    *float64 `json:"cache_read_price_per_1m"`
		CacheWritePrice   *float64 `json:"cache_write_price_per_1m"`
		Currency          *string  `json:"currency"`
		BillingMode       *string  `json:"billing_mode"`
		PricingSource     *string  `json:"pricing_source"`
		PricingUpdatedAt  *time.Time `json:"pricing_updated_at"`
		Available         bool     `json:"available"`
		RoutingTier       *int     `json:"routing_tier"`
		Weight            *int     `json:"weight"`
		SuccessRate       float64  `json:"success_rate"`
		P95LatencyMs      int      `json:"p95_latency_ms"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   string   `json:"credential_label"`
		CredentialStatus  string   `json:"credential_status"`
		BalanceUSD        *float64 `json:"balance_usd"`
		PoolGroup         *string  `json:"pool_group"`
		ProviderID        int      `json:"provider_id"`
		ProviderName      string   `json:"provider_name"`
	}

	items := make([]tableRow, 0)
	for rows.Next() {
		var tr tableRow
		if err := rows.Scan(&tr.CanonicalName, &tr.OfferID, &tr.RawModelName,
			&tr.PriceInPer1M, &tr.PriceOutPer1M,
			&tr.CacheReadPrice, &tr.CacheWritePrice,
			&tr.Currency, &tr.BillingMode, &tr.PricingSource, &tr.PricingUpdatedAt,
			&tr.Available, &tr.RoutingTier, &tr.Weight,
			&tr.SuccessRate, &tr.P95LatencyMs,
			&tr.CredentialID, &tr.CredentialLabel, &tr.CredentialStatus,
			&tr.BalanceUSD, &tr.PoolGroup,
			&tr.ProviderID, &tr.ProviderName); err != nil {
			continue
		}
		items = append(items, tr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) pricingCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		SourceOfferID  int    `json:"source_offer_id"`
		TargetOfferIDs []int  `json:"target_offer_ids"`
		CopyFields     []string `json:"copy_fields"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get source
	var source struct {
		PriceIn, PriceOut, CacheRead, CacheWrite *float64
		Currency, BillingMode *string
	}
	err := h.db.QueryRow(ctx, `
		SELECT unit_price_in_per_1m, unit_price_out_per_1m,
		       cache_read_price_per_1m, cache_write_price_per_1m,
		       currency, billing_mode
		FROM model_offers WHERE id = $1
	`, req.SourceOfferID).Scan(&source.PriceIn, &source.PriceOut,
		&source.CacheRead, &source.CacheWrite,
		&source.Currency, &source.BillingMode)
	if err != nil {
		writeError(w, http.StatusNotFound, "source offer not found")
		return
	}

	allowedFields := map[string]bool{
		"unit_price_in_per_1m": true, "unit_price_out_per_1m": true,
		"cache_read_price_per_1m": true, "cache_write_price_per_1m": true,
		"currency": true, "billing_mode": true,
	}

	updated := 0
	for _, targetID := range req.TargetOfferIDs {
		setClauses := []string{"pricing_source = 'copied'", "pricing_updated_at = now()"}
		var args []any
		argIdx := 1
		for _, field := range req.CopyFields {
			if !allowedFields[field] {
				continue
			}
			switch field {
			case "unit_price_in_per_1m":
				if source.PriceIn != nil {
					args = append(args, *source.PriceIn)
					setClauses = append(setClauses, fmt.Sprintf("unit_price_in_per_1m = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "unit_price_in_per_1m = NULL")
				}
			case "unit_price_out_per_1m":
				if source.PriceOut != nil {
					args = append(args, *source.PriceOut)
					setClauses = append(setClauses, fmt.Sprintf("unit_price_out_per_1m = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "unit_price_out_per_1m = NULL")
				}
			case "cache_read_price_per_1m":
				if source.CacheRead != nil {
					args = append(args, *source.CacheRead)
					setClauses = append(setClauses, fmt.Sprintf("cache_read_price_per_1m = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "cache_read_price_per_1m = NULL")
				}
			case "cache_write_price_per_1m":
				if source.CacheWrite != nil {
					args = append(args, *source.CacheWrite)
					setClauses = append(setClauses, fmt.Sprintf("cache_write_price_per_1m = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "cache_write_price_per_1m = NULL")
				}
			case "currency":
				if source.Currency != nil {
					args = append(args, *source.Currency)
					setClauses = append(setClauses, fmt.Sprintf("currency = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "currency = NULL")
				}
			case "billing_mode":
				if source.BillingMode != nil {
					args = append(args, *source.BillingMode)
					setClauses = append(setClauses, fmt.Sprintf("billing_mode = $%d", argIdx))
					argIdx++
				} else {
					setClauses = append(setClauses, "billing_mode = NULL")
				}
			}
		}
		args = append(args, targetID)
		query := fmt.Sprintf("UPDATE model_offers SET %s WHERE id = $%d",
			strings.Join(setClauses, ", "), argIdx)
		tag, err := h.db.Exec(ctx, query, args...)
		if err != nil {
			continue
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"updated":         updated,
		"source_offer_id": req.SourceOfferID,
	})
}

func (h *Handler) pricingAutoInherit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		DryRun bool `json:"dry_run"`
	}
	//nolint:errcheck // best-effort
	readJSON(r, &req)
	if !req.DryRun {
		req.DryRun = true // default to dry run
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		WITH priced AS (
			SELECT DISTINCT
				c.provider_id, mo.raw_model_name,
				FIRST_VALUE(mo.id) OVER (
					PARTITION BY c.provider_id, mo.raw_model_name
					ORDER BY mo.pricing_updated_at DESC NULLS LAST
				) AS source_id
			FROM model_offers mo
			JOIN credentials c ON c.id = mo.credential_id
			WHERE mo.unit_price_in_per_1m IS NOT NULL
			  AND mo.billing_mode != 'free'
		),
		unpriced AS (
			SELECT mo.id, c.provider_id, mo.raw_model_name
			FROM model_offers mo
			JOIN credentials c ON c.id = mo.credential_id
			WHERE mo.unit_price_in_per_1m IS NULL
			  AND mo.billing_mode != 'free'
		)
		SELECT u.id AS target_id, pr.source_id
		FROM unpriced u
		JOIN priced pr ON pr.provider_id = u.provider_id AND pr.raw_model_name = u.raw_model_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type inheritPair struct {
		TargetID int `json:"target_offer_id"`
		SourceID int `json:"source_offer_id"`
	}
	pairs := make([]inheritPair, 0)
	for rows.Next() {
		var p inheritPair
		if err := rows.Scan(&p.TargetID, &p.SourceID); err != nil {
			continue
		}
		pairs = append(pairs, p)
	}

	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"would_inherit": len(pairs),
			"details":       pairs,
		})
		return
	}

	// Execute inheritance
	inherited := 0
	for _, p := range pairs {
		tag, err := h.db.Exec(ctx, `
			UPDATE model_offers mo SET
				pricing_source = 'inherited',
				pricing_updated_at = now(),
				unit_price_in_per_1m = src.unit_price_in_per_1m,
				unit_price_out_per_1m = src.unit_price_out_per_1m,
				cache_read_price_per_1m = src.cache_read_price_per_1m,
				cache_write_price_per_1m = src.cache_write_price_per_1m,
				currency = src.currency,
				billing_mode = src.billing_mode
			FROM model_offers src
			WHERE mo.id = $1 AND src.id = $2
		`, p.TargetID, p.SourceID)
		if err == nil && tag.RowsAffected() > 0 {
			inherited++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"inherited": inherited})
}

func nullableFloat(v *float64) string {
	if v == nil {
		return "NULL"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func nullableStr(v *string) string {
	if v == nil {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(*v, "'", "''") + "'"
}
