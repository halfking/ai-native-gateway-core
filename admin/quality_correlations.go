package admin

// quality_correlations.go — P8.2: request-level quality correlation
// analytics.
//
// Where P7.2's /correlations endpoint surfaces (model, task) and
// (strategy, task) breakdowns, this endpoint surfaces correlations
// between REQUEST FEATURES (prompt length, image count, tool count,
// code block presence) and OUTCOME (success, latency, quality).
//
// Endpoints:
//
//	GET /api/admin/auto-route/quality-correlations?days=7
//	GET /api/admin/auto-route/quality-correlations?by=tools
//
// The `by` parameter selects the bucket dimension:
//   - prompt_length: bucket by prompt_tokens ranges
//   - tools:         bucket by tool count in request
//   - images:        bucket by image count
//   - code_block:    bucket by code-block presence
//
// The response also includes an "insights" section that ranks
// each predictor by absolute Pearson correlation with quality_score.

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// QualityCorrelationRow is a single bucket row.
type QualityCorrelationRow struct {
	Bucket      string  `json:"bucket"`
	Samples     int     `json:"samples"`
	SuccessRate float64 `json:"success_rate"`
	AvgLatency  int     `json:"avg_latency_ms"`
	AvgQuality  float64 `json:"avg_quality"`
	AvgCost     float64 `json:"avg_cost_usd"`
}

// QualityCorrelationInsight is one row in the "what predicts failure"
// section.
type QualityCorrelationInsight struct {
	Predictor    string  `json:"predictor"` // 'prompt_length', 'tools', 'images', 'code_block'
	Buckets       int     `json:"buckets"`  // number of distinct buckets
	Samples       int     `json:"samples"`
	Correlation  float64 `json:"correlation"`  // Pearson r
	AbsR         float64 `json:"abs_r"`
	Interpretation string `json:"interpretation"` // human-readable
}

// QualityCorrelationResponse is the JSON wire format.
type QualityCorrelationResponse struct {
	WindowDays int                          `json:"window_days"`
	By         string                       `json:"by"`
	Breakdown  []QualityCorrelationRow      `json:"breakdown"`
	Insights   []QualityCorrelationInsight  `json:"insights"`
	GeneratedAt string                      `json:"generated_at"`
}

// handleQualityCorrelations: GET /auto-route/quality-correlations
func (h *AutoRouteHandlers) handleQualityCorrelations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		v, err := strconv.Atoi(d)
		if err != nil || v <= 0 || v > 90 {
			writeJSONErr(w, http.StatusBadRequest, "days must be 1-90")
			return
		}
		days = v
	}

	by := r.URL.Query().Get("by")
	if by == "" {
		by = "prompt_length"
	}
	allowedBy := map[string]bool{
		"prompt_length": true,
		"tools":         true,
		"images":        true,
		"code_block":    true,
	}
	if !allowedBy[by] {
		writeJSONErr(w, http.StatusBadRequest, "by must be prompt_length|tools|images|code_block")
		return
	}

	// Build the breakdown query based on `by`.
	breakdownQuery, _ := buildBreakdownQuery(by)
	rows, err := h.db.Query(r.Context(), breakdownQuery, days)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	var breakdown []QualityCorrelationRow
	for rows.Next() {
		var row QualityCorrelationRow
		if err := rows.Scan(&row.Bucket, &row.Samples, &row.SuccessRate,
			&row.AvgLatency, &row.AvgQuality, &row.AvgCost); err != nil {
			writeInternalErr(w, err)
			return
		}
		breakdown = append(breakdown, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	// Compute insights by running all 4 breakdowns and ranking
	// by Pearson correlation. Skip if total samples < 30.
	insights := []QualityCorrelationInsight{}
	if totalSamples(breakdown) >= 30 {
		insights = computeAllInsights(r.Context(), h.db, days)
	}

	writeJSON(w, http.StatusOK, QualityCorrelationResponse{
		WindowDays:  days,
		By:         by,
		Breakdown:  breakdown,
		Insights:   insights,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// buildBreakdownQuery returns the SQL for the requested `by` dimension.
// Each query aggregates per-bucket and computes the average
// success/latency/quality/cost. The bucket definition is inlined
// in the SELECT/GROUP BY.
func buildBreakdownQuery(by string) (string, error) {
	var bucketExpr string
	switch by {
	case "prompt_length":
		// Bucket prompt_tokens into log-scale ranges
		bucketExpr = `
			CASE
				WHEN prompt_tokens < 500 THEN '0-500'
				WHEN prompt_tokens < 2000 THEN '500-2000'
				WHEN prompt_tokens < 8000 THEN '2000-8000'
				ELSE '8000+'
			END`
	case "tools":
		// Count tools in the JSONB request_body
		bucketExpr = `
			CASE
				WHEN jsonb_array_length(COALESCE(request_body->'tools', '[]'::jsonb)) = 0
					THEN '0'
				WHEN jsonb_array_length(COALESCE(request_body->'tools', '[]'::jsonb)) = 1
					THEN '1'
				ELSE '2+'
			END`
	case "images":
		// Count images in messages
		bucketExpr = `
			CASE
				WHEN request_body->'messages' @> '[{"content":[{"type":"image_url"}]}]'::jsonb
					THEN 'has_image'
				ELSE 'no_image'
			END`
	case "code_block":
		// Check for triple-backtick in any message. We use
		// chr(96) three times to avoid backticks inside the
		// raw string literal (which would terminate it).
		bucketExpr = `
			CASE
				WHEN position(chr(96) || chr(96) || chr(96) in request_body::text) > 0
					THEN 'has_code'
				ELSE 'no_code'
			END`
	default:
		return "", nil // unreachable: caller already validated
	}

	return `
		SELECT ` + bucketExpr + ` AS bucket,
		       COUNT(*) AS samples,
		       AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success_rate,
		       COALESCE(AVG(latency_ms), 0)::int AS avg_latency,
		       AVG(quality_score) AS avg_quality,
		       COALESCE(AVG(cost_usd), 0) AS avg_cost
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		GROUP BY 1
		ORDER BY 1
	`, nil
}

// totalSamples sums the Samples field across all breakdown rows.
func totalSamples(rows []QualityCorrelationRow) int {
	n := 0
	for _, r := range rows {
		n += r.Samples
	}
	return n
}

// computeAllInsights runs all 4 breakdowns and ranks by Pearson
// correlation. Each query returns (bucket, samples, success, quality)
// and the function uses bucket-index as X and avg_quality as Y
// (weighted by samples). The result is sorted by |r| desc.
func computeAllInsights(ctx interface{ Done() <-chan struct{} }, db interface {
	Query(interface{ Done() <-chan struct{} }, string, ...any) (interface {
		Next() bool
		Scan(...any) error
		Err() error
	}, error)
}, days int) []QualityCorrelationInsight {
	predictors := []string{"prompt_length", "tools", "images", "code_block"}
	results := make([]QualityCorrelationInsight, 0, len(predictors))

	for _, p := range predictors {
		q, _ := buildBreakdownQuery(p)
		rows, err := db.Query(ctx, q, days)
		if err != nil {
			continue
		}
		var xs, ys, ws []float64
		for rows.Next() {
			var bucket string
			var samples int
			var successRate, avgQuality float64
			if err := rows.Scan(&bucket, &samples, &successRate,
				nil, &avgQuality, nil); err != nil {
				continue
			}
			xs = append(xs, float64(bucketIndex(p, bucket)))
			ys = append(ys, avgQuality)
			ws = append(ws, float64(samples))
		}
		if len(xs) < 2 {
			continue
		}
		r := weightedPearson(xs, ys, ws)
		results = append(results, QualityCorrelationInsight{
			Predictor:     p,
			Buckets:       len(xs),
			Samples:       int(sumFloat(ws)),
			Correlation:   r,
			AbsR:          math.Abs(r),
			Interpretation: interpretCorrelation(p, r),
		})
	}
	// Sort by abs r desc
	for i := 0; i+1 < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].AbsR > results[i].AbsR {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results
}

// bucketIndex maps a bucket label to an ordinal index for
// correlation purposes. For ordinal predictors (prompt_length)
// this is the natural numeric order; for binary (images,
// code_block) it's 0/1; for tools it's 0/1/2+.
func bucketIndex(predictor, bucket string) int {
	switch predictor {
	case "prompt_length":
		switch bucket {
		case "0-500":
			return 0
		case "500-2000":
			return 1
		case "2000-8000":
			return 2
		}
		return 3
	case "tools":
		switch bucket {
		case "0":
			return 0
		case "1":
			return 1
		}
		return 2
	case "images":
		if bucket == "has_image" {
			return 1
		}
		return 0
	case "code_block":
		if bucket == "has_code" {
			return 1
		}
		return 0
	}
	return 0
}

// weightedPearson computes the weighted Pearson correlation
// coefficient. Returns 0 if denominator is 0 (e.g. all ys equal).
func weightedPearson(xs, ys, ws []float64) float64 {
	if len(xs) != len(ys) || len(xs) != len(ws) || len(xs) < 2 {
		return 0
	}
	// Weighted means
	var sumW, sumWX, sumWY float64
	for i := range xs {
		sumW += ws[i]
		sumWX += ws[i] * xs[i]
		sumWY += ws[i] * ys[i]
	}
	if sumW == 0 {
		return 0
	}
	meanX := sumWX / sumW
	meanY := sumWY / sumW

	// Weighted covariance and variances
	var cov, varX, varY float64
	for i := range xs {
		dx := xs[i] - meanX
		dy := ys[i] - meanY
		cov += ws[i] * dx * dy
		varX += ws[i] * dx * dx
		varY += ws[i] * dy * dy
	}
	denom := math.Sqrt(varX * varY)
	if denom == 0 {
		return 0
	}
	return cov / denom
}

// interpretCorrelation produces a human-readable explanation of
// the sign and magnitude of the correlation.
func interpretCorrelation(predictor string, r float64) string {
	var dir string
	if r > 0 {
		dir = "positively"
	} else if r < 0 {
		dir = "negatively"
	} else {
		dir = "not"
	}
	var mag string
	absR := math.Abs(r)
	switch {
	case absR >= 0.7:
		mag = "strongly"
	case absR >= 0.4:
		mag = "moderately"
	case absR >= 0.2:
		mag = "weakly"
	default:
		mag = "very weakly"
	}
	return predictor + " is " + mag + " " + dir + " correlated with quality"
}

// sumFloat sums a float64 slice.
func sumFloat(s []float64) float64 {
	n := 0.0
	for _, v := range s {
		n += v
	}
	return n
}
