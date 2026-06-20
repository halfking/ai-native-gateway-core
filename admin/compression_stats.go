package admin

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
		TotalRequests        int             `json:"total_requests"`
		CompressedTotal      int             `json:"compressed_total"`
		CompressionRate      float64         `json:"compression_rate"`
		StrategyDistribution map[string]int  `json:"strategy_distribution"`
		TotalOutboundTokens  *int64          `json:"total_outbound_tokens,omitempty"`
		EstimatedOrigTokens  *int64          `json:"estimated_original_tokens,omitempty"`
		EstimatedTokensSaved *int64          `json:"estimated_tokens_saved,omitempty"`
		HourlySeries         []hourBucket    `json:"hourly_series"`
	}{
		StrategyDistribution: make(map[string]int),
		HourlySeries:         make([]hourBucket, 0),
	}

	aggRows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(NULLIF(rl.compression_strategy,''), 'none') AS strategy,
			COUNT(*) AS cnt,
			COUNT(rl.outbound_body)::bigint AS with_outbound,
			SUM(COALESCE(rl.outbound_token_est, 0))::bigint AS total_tok_after,
			SUM(CASE WHEN rl.outbound_body IS NOT NULL THEN COALESCE(rl.outbound_token_est, 0) ELSE 0 END)::bigint AS compressed_tok
		FROM request_logs rl
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)
		GROUP BY strategy
		ORDER BY cnt DESC
	`, from, to, !tenantFilter)
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

	var estimatedOrig int64
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(CEIL(LENGTH(COALESCE(rl.request_body, ''))::numeric / 4.0)), 0)::bigint
		FROM request_logs rl
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)
	`, from, to, !tenantFilter).Scan(&estimatedOrig)
	if err == nil && estimatedOrig > 0 {
		result.EstimatedOrigTokens = &estimatedOrig
		if totalToksAfter > 0 && estimatedOrig > totalToksAfter {
			saved := estimatedOrig - totalToksAfter
			result.EstimatedTokensSaved = &saved
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
			COUNT(rl.outbound_body)::int AS compressed
		FROM request_logs rl
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)
		GROUP BY bucket
		ORDER BY bucket
	`, from, to, !tenantFilter)
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
