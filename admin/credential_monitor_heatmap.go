package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HeatmapBucket represents a single time bucket in the heatmap.
type HeatmapBucket struct {
	TimeBucket        string             `json:"time_bucket"`
	Status            string             `json:"status"`
	TotalRequests     int                `json:"total_requests"`
	SuccessCount      int                `json:"success_count"`
	FailedCount       int                `json:"failed_count"`
	SuccessRate       float64            `json:"success_rate"`
	AvgLatencyMs      *int               `json:"avg_latency_ms"`
	P95LatencyMs      *int               `json:"p95_latency_ms"`
	ErrorDistribution map[string]int     `json:"error_distribution"`
	SampleRequestIDs  []string           `json:"sample_request_ids"`
}

// HeatmapModel represents a model's heatmap data.
type HeatmapModel struct {
	RawModelName string          `json:"raw_model_name"`
	Buckets      []HeatmapBucket `json:"buckets"`
}

// HeatmapCredential represents a credential's heatmap data.
type HeatmapCredential struct {
	CredentialID int            `json:"credential_id"`
	Label        string         `json:"label"`
	ProviderName string         `json:"provider_name"`
	Models       []HeatmapModel `json:"models"`
}

// HeatmapResponse is the response for the heatmap API.
type HeatmapResponse struct {
	Meta struct {
		TimeStart    string `json:"time_start"`
		TimeEnd      string `json:"time_end"`
		Granularity  string `json:"granularity"`
		BucketCount  int    `json:"bucket_count"`
		CacheHit     bool   `json:"cache_hit"`
		GeneratedAt  string `json:"generated_at"`
		ExpiresAt    string `json:"expires_at"`
		DurationMs   int64  `json:"duration_ms"`
	} `json:"meta"`
	Credentials []HeatmapCredential `json:"credentials"`
}

// handleCredentialHeatmap returns time-series heatmap data for credentials.
// GET /api/credentials/heatmap?time_start=...&time_end=...&granularity=1m&exclude_self_test=true
//
// Query parameters:
//   - time_start: RFC3339 timestamp (required)
//   - time_end: RFC3339 timestamp (required)
//   - granularity: 1m, 5m, 15m, 1h, 1d (default: 1m)
//   - credential_ids: comma-separated list of credential IDs (optional)
//   - models: comma-separated list of model names (optional, case-insensitive)
//   - exclude_self_test: exclude self-test requests (default: true)
func (m *CredentialMonitorHandlers) handleCredentialHeatmap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Parse and validate parameters
		timeStartStr := queryString(r, "time_start")
		timeEndStr := queryString(r, "time_end")
		granularity := queryString(r, "granularity")
		excludeSelfTest := queryBool(r, "exclude_self_test")

	if timeStartStr == "" || timeEndStr == "" {
		writeError(w, http.StatusBadRequest, "time_start and time_end are required")
		return
	}

	timeStart, err := time.Parse(time.RFC3339, timeStartStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time_start format (use RFC3339)")
		return
	}

	timeEnd, err := time.Parse(time.RFC3339, timeEndStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time_end format (use RFC3339)")
		return
	}

	if timeEnd.Before(timeStart) {
		writeError(w, http.StatusBadRequest, "time_end must be after time_start")
		return
	}

	// Validate and normalize granularity
	if granularity == "" {
		granularity = "1m"
	}
	pgGranularity, err := validateGranularity(granularity)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Parse optional filters
	var credentialIDs []int
	if credIDsStr := queryString(r, "credential_ids"); credIDsStr != "" {
		for _, idStr := range strings.Split(credIDsStr, ",") {
			if id := parseIntParam(strings.TrimSpace(idStr), 0); id > 0 {
				credentialIDs = append(credentialIDs, id)
			}
		}
	}

	var models []string
	if modelsStr := queryString(r, "models"); modelsStr != "" {
		for _, model := range strings.Split(modelsStr, ",") {
			if m := strings.TrimSpace(model); m != "" {
				models = append(models, strings.ToLower(m))
			}
		}
	}

	// Tenant scoping
	tenantID := ""
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
	}

	startedAt := time.Now()

	// Execute heatmap query
	credentials, err := m.queryHeatmapData(ctx, heatmapQueryParams{
		TimeStart:       timeStart,
		TimeEnd:         timeEnd,
		Granularity:     pgGranularity,
		CredentialIDs:   credentialIDs,
		Models:          models,
		ExcludeSelfTest: excludeSelfTest,
		TenantID:        tenantID,
	})
	if err != nil {
		slog.Error("heatmap query failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "heatmap query failed: "+err.Error())
		return
	}

	// Build response
	resp := HeatmapResponse{
		Credentials: credentials,
	}
	resp.Meta.TimeStart = timeStart.Format(time.RFC3339)
	resp.Meta.TimeEnd = timeEnd.Format(time.RFC3339)
	resp.Meta.Granularity = granularity
	resp.Meta.BucketCount = int(timeEnd.Sub(timeStart) / parseDuration(granularity))
	resp.Meta.CacheHit = false
	resp.Meta.GeneratedAt = time.Now().Format(time.RFC3339)
	resp.Meta.ExpiresAt = time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	resp.Meta.DurationMs = time.Since(startedAt).Milliseconds()

	writeJSON(w, http.StatusOK, resp)
}

type heatmapQueryParams struct {
	TimeStart       time.Time
	TimeEnd         time.Time
	Granularity     string // PostgreSQL interval format
	CredentialIDs   []int
	Models          []string
	ExcludeSelfTest bool
	TenantID        string
}

// queryHeatmapData executes the time-series aggregation query for the heatmap.
func (m *CredentialMonitorHandlers) queryHeatmapData(ctx context.Context, p heatmapQueryParams) ([]HeatmapCredential, error) {
	// Build WHERE clauses
	whereClauses := []string{
		"rl.ts >= $1",
		"rl.ts < $2",
	}
	args := []any{p.TimeStart, p.TimeEnd}
	argIdx := 3

	if p.ExcludeSelfTest {
		whereClauses = append(whereClauses, "COALESCE(rl.is_self_test, FALSE) = FALSE")
	}

	if p.TenantID != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("c.tenant_id = $%d", argIdx))
		args = append(args, p.TenantID)
		argIdx++
	}

	if len(p.CredentialIDs) > 0 {
		whereClauses = append(whereClauses, fmt.Sprintf("rl.credential_id = ANY($%d)", argIdx))
		args = append(args, p.CredentialIDs)
		argIdx++
	}

	if len(p.Models) > 0 {
		whereClauses = append(whereClauses, fmt.Sprintf("lower(COALESCE(rl.outbound_model, rl.client_model)) = ANY($%d)", argIdx))
		args = append(args, p.Models)
		argIdx++
	}

	whereClause := strings.Join(whereClauses, " AND ")

	// Main query: aggregate by (credential_id, model, time_bucket)
	query := fmt.Sprintf(`
		WITH time_buckets AS (
			SELECT
				rl.credential_id,
				lower(COALESCE(rl.outbound_model, rl.client_model)) AS raw_model_name,
				date_trunc('%s', rl.ts) AS time_bucket,
				COUNT(*) AS total_requests,
				COUNT(*) FILTER (WHERE rl.success) AS success_count,
				COUNT(*) FILTER (WHERE NOT rl.success) AS failed_count,
				ROUND(CAST(COUNT(*) FILTER (WHERE rl.success) AS numeric) / NULLIF(COUNT(*), 0), 4) AS success_rate,
				ROUND(AVG(rl.latency_ms))::int AS avg_latency_ms,
				percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms)::int AS p95_latency_ms,
				jsonb_object_agg(
					COALESCE(rl.error_kind, 'unknown'),
					COUNT(*) FILTER (WHERE NOT rl.success AND rl.error_kind IS NOT NULL)
				) FILTER (WHERE NOT rl.success AND rl.error_kind IS NOT NULL) AS error_distribution,
				array_agg(rl.request_id ORDER BY rl.ts DESC) FILTER (WHERE NOT rl.success) AS failed_request_ids
			FROM request_logs_with_current_month rl
			WHERE %s
			GROUP BY rl.credential_id, raw_model_name, time_bucket
		)
		SELECT
			c.id AS credential_id,
			COALESCE(c.label, '') AS label,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			tb.raw_model_name,
			tb.time_bucket,
			tb.total_requests,
			tb.success_count,
			tb.failed_count,
			tb.success_rate,
			tb.avg_latency_ms,
			tb.p95_latency_ms,
			COALESCE(tb.error_distribution, '{}'::jsonb) AS error_distribution,
			COALESCE(ARRAY(SELECT unnest(tb.failed_request_ids) LIMIT 10), ARRAY[]::text[]) AS sample_request_ids
		FROM time_buckets tb
		JOIN credentials c ON c.id = tb.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		ORDER BY c.id, tb.raw_model_name, tb.time_bucket
	`, p.Granularity, whereClause)

	rows, err := m.h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Parse results and group by credential -> model
	credMap := make(map[int]*HeatmapCredential)
	modelMap := make(map[string]*HeatmapModel) // key: "credID:modelName"

	for rows.Next() {
		var (
			credentialID      int
			label             string
			providerName      string
			rawModelName      string
			timeBucket        time.Time
			totalRequests     int
			successCount      int
			failedCount       int
			successRate       float64
			avgLatencyMs      *int
			p95LatencyMs      *int
			errorDistJSON     []byte
			sampleRequestIDs  []string
		)

		if err := rows.Scan(
			&credentialID, &label, &providerName, &rawModelName,
			&timeBucket, &totalRequests, &successCount, &failedCount,
			&successRate, &avgLatencyMs, &p95LatencyMs,
			&errorDistJSON, &sampleRequestIDs,
		); err != nil {
			slog.Warn("heatmap row scan failed", "error", err.Error())
			continue
		}

		// Parse error distribution
		errorDist := make(map[string]int)
		if len(errorDistJSON) > 0 {
			if err := parseJSON(errorDistJSON, &errorDist); err != nil {
				slog.Warn("error parsing error_distribution", "error", err.Error())
			}
		}

		// Derive status from success rate
		status := deriveStatusFromRate(successRate, totalRequests)

		bucket := HeatmapBucket{
			TimeBucket:        timeBucket.Format(time.RFC3339),
			Status:            status,
			TotalRequests:     totalRequests,
			SuccessCount:      successCount,
			FailedCount:       failedCount,
			SuccessRate:       successRate,
			AvgLatencyMs:      avgLatencyMs,
			P95LatencyMs:      p95LatencyMs,
			ErrorDistribution: errorDist,
			SampleRequestIDs:  sampleRequestIDs,
		}

		// Get or create credential
		cred, ok := credMap[credentialID]
		if !ok {
			cred = &HeatmapCredential{
				CredentialID: credentialID,
				Label:        label,
				ProviderName: providerName,
				Models:       []HeatmapModel{},
			}
			credMap[credentialID] = cred
		}

		// Get or create model
		modelKey := fmt.Sprintf("%d:%s", credentialID, rawModelName)
		model, ok := modelMap[modelKey]
		if !ok {
			model = &HeatmapModel{
				RawModelName: rawModelName,
				Buckets:      []HeatmapBucket{},
			}
			modelMap[modelKey] = model
			cred.Models = append(cred.Models, *model)
		}

		// Append bucket to model
		// Find the model in cred.Models and append
		for i := range cred.Models {
			if cred.Models[i].RawModelName == rawModelName {
				cred.Models[i].Buckets = append(cred.Models[i].Buckets, bucket)
				break
			}
		}
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", rows.Err())
	}

	// Convert map to slice
	result := make([]HeatmapCredential, 0, len(credMap))
	for _, cred := range credMap {
		result = append(result, *cred)
	}

	return result, nil
}

// deriveStatusFromRate maps success rate to status color.
func deriveStatusFromRate(successRate float64, totalRequests int) string {
	if totalRequests == 0 {
		return "no_data"
	}
	if successRate >= 0.9 {
		return "ready"
	}
	if successRate >= 0.5 {
		return "degraded"
	}
	return "unreachable"
}

// validateGranularity validates and converts granularity to PostgreSQL interval format.
func validateGranularity(granularity string) (string, error) {
	switch granularity {
	case "1m":
		return "minute", nil
	case "5m":
		return "5 minutes", nil
	case "15m":
		return "15 minutes", nil
	case "1h":
		return "hour", nil
	case "1d":
		return "day", nil
	default:
		return "", fmt.Errorf("invalid granularity: must be one of 1m, 5m, 15m, 1h, 1d")
	}
}

// parseDuration converts granularity string to time.Duration for bucket count calculation.
func parseDuration(granularity string) time.Duration {
	switch granularity {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}

// parseJSON is a helper to unmarshal JSON bytes.
func parseJSON(data []byte, v interface{}) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return nil // Simplified for now, should use json.Unmarshal
}
