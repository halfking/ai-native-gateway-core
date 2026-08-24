package admin

// NOTE: All SELECTs on tenant-scoped tables (request_logs etc.) in this file
// rely on tenantLogsClause() (admin/session_tenant.go) to inject
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type hourBucket struct {
	Hour       time.Time `json:"hour"`
	Total      int       `json:"total"`
	Compressed int       `json:"compressed"`
	Rate       float64   `json:"rate"`
}

// compressionStatsEstimatedOrigSQL estimates original tokens from the stored
// request body length (chars / 4). P2-C1: rows persisted in CO-5 summary mode
// hold a {"_gw_body_summary":{...,"bytes":N,...}} digest envelope instead of
// the full body; for those rows the envelope's original byte count (bytes) is
// used, never the envelope document's own length (which would understate the
// original for large bodies). Malformed envelopes (missing/non-numeric bytes)
// fall back to the stored document length, and the second column counts
// summary-mode rows so the response can expose summary_mode_rows separately.
//
// The envelope test requires the "_gw_body_summary" value to be a JSON
// object (jsonb_typeof), matching the Go-side detector in body_envelope.go:
// a present-but-null or scalar key ({"_gw_body_summary":null} / :5) is not
// an envelope, so it neither contributes bytes nor counts as a summary-mode
// row. The `#>>` path operator is jsonb-only; both body columns are jsonb
// (deploy/sql/objects/tables/request_logs_bodies_hot.sql), and NULL
// request_body falls through the CASE to the length fallback.
// Note the fallback casts to text BEFORE coalescing with the empty-string
// literal: a bare empty string inside COALESCE next to jsonb resolves to
// jsonb, and the empty string is invalid JSON — the pre-P2-C1 query hit
// "invalid input syntax for type json" at runtime on NULL-body rows
// (silently swallowed by the err==nil guard, so estimated_original_tokens
// was never populated). The ::text restores the intended chars/4 estimate
// for non-envelope rows.
const compressionStatsEstimatedOrigSQL = `
			SELECT
				COALESCE(SUM(CEIL(CASE
						WHEN jsonb_typeof(rb.request_body->'_gw_body_summary') = 'object'
							AND (rb.request_body #>> '{_gw_body_summary,bytes}') ~ '^[0-9]+$'
						THEN (rb.request_body #>> '{_gw_body_summary,bytes}')::numeric
					ELSE LENGTH(COALESCE(COALESCE(rb.request_body, rl.request_body)::text, ''))::numeric
				END / 4.0)), 0)::bigint,
				COALESCE(SUM(CASE WHEN jsonb_typeof(rb.request_body->'_gw_body_summary') = 'object' THEN 1 ELSE 0 END), 0)::bigint
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)`

func (h *Handler) handleCompressionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 720 {
		hours = 720
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var from, to time.Time
	if fromStr != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
	} else {
		to = time.Now().UTC()
		from = to.Add(-time.Duration(hours) * time.Hour)
	}
	if toStr != "" {
		var err error
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
	} else if fromStr != "" {
		to = time.Now().UTC()
	}

	tenantFilter := IsTenantAdmin(r)

	var result = struct {
		TotalRequests        int            `json:"total_requests"`
		CompressedTotal      int            `json:"compressed_total"`
		CompressionRate      float64        `json:"compression_rate"`
		StrategyDistribution map[string]int `json:"strategy_distribution"`
		// 2026-08-19: token-band observability counters (preliminary / forced
		// vs the below baseline). Below-band rows also count for transparency
		// so the band ratios are derivable; only the two non-baseline bands
		// are surfaced as separate numeric fields for alert wiring.
		TokenBandBelow       *int64       `json:"token_band_below,omitempty"`
		TokenBandPreliminary *int64       `json:"token_band_preliminary,omitempty"`
		TokenBandForced      *int64       `json:"token_band_forced,omitempty"`
		TotalOutboundTokens  *int64       `json:"total_outbound_tokens,omitempty"`
		EstimatedOrigTokens  *int64       `json:"estimated_original_tokens,omitempty"`
		EstimatedTokensSaved *int64       `json:"estimated_tokens_saved,omitempty"`
		SummaryModeRows      *int64       `json:"summary_mode_rows,omitempty"`
		HourlySeries         []hourBucket `json:"hourly_series"`
	}{
		StrategyDistribution: make(map[string]int),
		HourlySeries:         make([]hourBucket, 0),
	}

	aggArgs := []any{from, to, !tenantFilter}
	aggArgIdx := 4
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, aggArgIdx)
	aggWhere := ""
	if tenantFrag != "" {
		aggWhere = tenantFrag
		aggArgs = append(aggArgs, tenantArgs...)
	}

	aggRows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(NULLIF(rl.compression_strategy,''), 'none') AS strategy,
			COUNT(*) AS cnt,
			COUNT(rb.outbound_body)::bigint AS with_outbound,
			SUM(COALESCE(rl.outbound_token_est, 0))::bigint AS total_tok_after,
			SUM(CASE WHEN rb.outbound_body IS NOT NULL THEN COALESCE(rl.outbound_token_est, 0) ELSE 0 END)::bigint AS compressed_tok
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)`+aggWhere+`
		GROUP BY strategy
		ORDER BY cnt DESC
	`, aggArgs...)
	if err != nil {
		slog.Warn("compression_stats agg query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer aggRows.Close()

	var totalToksAfter int64
	for aggRows.Next() {
		var strategy string
		var cnt, withOutbound, tokAfter, compressedTok int
		if err := aggRows.Scan(&strategy, &cnt, &withOutbound, &tokAfter, &compressedTok); err != nil {
			slog.Warn("compression_stats agg scan failed", "error", err)
			continue
		}
		result.TotalRequests += cnt
		if withOutbound > 0 {
			result.CompressedTotal += cnt
		}
		result.StrategyDistribution[strategy] = cnt
		totalToksAfter += int64(tokAfter)
	}

	if result.TotalRequests > 0 {
		result.CompressionRate = float64(result.CompressedTotal) / float64(result.TotalRequests)
	}
	if totalToksAfter > 0 {
		result.TotalOutboundTokens = &totalToksAfter
	}

	var estimatedOrig, summaryModeRows int64
	err = h.db.QueryRow(ctx, compressionStatsEstimatedOrigSQL+aggWhere+`
	`, aggArgs...).Scan(&estimatedOrig, &summaryModeRows)
	if err != nil {
		// The estimate is best-effort and the endpoint still returns the
		// aggregates above, but stay diagnosable: the pre-P2-C1 ::text bug
		// was hidden here by a silent err==nil guard for its whole life.
		slog.Warn("compression_stats estimated-orig query failed", "error", err)
	} else {
		if estimatedOrig > 0 {
			result.EstimatedOrigTokens = &estimatedOrig
			if totalToksAfter > 0 && estimatedOrig > totalToksAfter {
				saved := estimatedOrig - totalToksAfter
				result.EstimatedTokensSaved = &saved
			}
		}
		// P2-C1: surface how many rows are digest envelopes (summary mode).
		// Omitted (omitempty) when zero, so the flag-off response payload is
		// byte-identical to the pre-P2-C1 shape.
		if summaryModeRows > 0 {
			result.SummaryModeRows = &summaryModeRows
		}
	}

	// 2026-08-19: token-band aggregation. Uses the dedicated column (indexed
	// by token_band, ts DESC) so the query is O(band rows in window) and
	// unaffected by the much larger uncompressed-row count.
	bandRows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(token_band, '') AS band,
			COUNT(*) AS cnt
		FROM request_logs_with_current_month
		WHERE ts >= $1 AND ts <= $2
		  AND ($3 OR success)`+aggWhere+`
		GROUP BY token_band
		ORDER BY cnt DESC
	`, aggArgs...)
	if err != nil {
		slog.Warn("compression_stats band query failed", "error", err)
	} else {
		defer bandRows.Close()
		for bandRows.Next() {
			var band string
			var cnt int
			if err := bandRows.Scan(&band, &cnt); err != nil {
				continue
			}
			switch band {
			case "below":
				v := int64(cnt)
				result.TokenBandBelow = &v
			case "preliminary":
				v := int64(cnt)
				result.TokenBandPreliminary = &v
			case "forced":
				v := int64(cnt)
				result.TokenBandForced = &v
			}
		}
	}

	rangeHours := to.Sub(from).Hours()
	var bucketExpr string
	switch {
	case rangeHours <= 48:
		bucketExpr = "date_trunc('hour', rl.ts)"
	case rangeHours <= 168:
		bucketExpr = "date_trunc('day', rl.ts) + INTERVAL '6 hours' * (EXTRACT(HOUR FROM rl.ts)::integer / 6)"
	default:
		bucketExpr = "date_trunc('day', rl.ts)"
	}

	bucketRows, err := h.db.Query(ctx, `
		SELECT `+bucketExpr+` AS bucket,
			COUNT(*) AS total,
			COUNT(rb.outbound_body)::int AS compressed
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)`+aggWhere+`
		GROUP BY bucket
		ORDER BY bucket
	`, aggArgs...)
	if err != nil {
		slog.Warn("compression_stats bucket query failed", "error", err)
	} else {
		defer bucketRows.Close()
		for bucketRows.Next() {
			var b struct {
				Bucket     time.Time
				Total      int
				Compressed int
			}
			if err := bucketRows.Scan(&b.Bucket, &b.Total, &b.Compressed); err != nil {
				continue
			}
			var rate float64
			if b.Total > 0 {
				rate = float64(b.Compressed) / float64(b.Total)
			}
			result.HourlySeries = append(result.HourlySeries, hourBucket{
				Hour:       b.Bucket,
				Total:      b.Total,
				Compressed: b.Compressed,
				Rate:       rate,
			})
		}
	}

	writeJSON(w, http.StatusOK, result)
}
