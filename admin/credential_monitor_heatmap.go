package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HeatmapBucket represents a single time bucket in the heatmap.
type HeatmapBucket struct {
	TimeBucket        string         `json:"time_bucket"`
	Status            string         `json:"status"`
	TotalRequests     int            `json:"total_requests"`
	SuccessCount      int            `json:"success_count"`
	FailedCount       int            `json:"failed_count"`
	SuccessRate       float64        `json:"success_rate"`
	AvgLatencyMs      *int           `json:"avg_latency_ms"`
	P95LatencyMs      *int           `json:"p95_latency_ms"`
	ErrorDistribution map[string]int `json:"error_distribution"`
	SampleRequestIDs  []string       `json:"sample_request_ids"`
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
		TimeStart   string `json:"time_start"`
		TimeEnd     string `json:"time_end"`
		Granularity string `json:"granularity"`
		BucketCount int    `json:"bucket_count"`
		CacheHit    bool   `json:"cache_hit"`
		GeneratedAt string `json:"generated_at"`
		ExpiresAt   string `json:"expires_at"`
		DurationMs  int64  `json:"duration_ms"`
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
	if m.h == nil || m.h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Parse and validate parameters
	timeStartStr := queryString(r, "time_start")
	timeEndStr := queryString(r, "time_end")
	granularity := queryString(r, "granularity")
	// exclude_self_test 缺省 true（与上方文档及 FEATURE-REQ §4.2 一致）；
	// 此前 queryBool 缺省返回 false，裸 API 调用方会把自检流量计入服务质量。
	excludeSelfTest := true
	if r.URL.Query().Has("exclude_self_test") {
		excludeSelfTest = queryBool(r, "exclude_self_test")
	}

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

	// 窗口与桶数上限：无界窗口（如 1970 起、1m 粒度）会触发全表扫描 +
	// 千万级桶聚合，30s ctx 才能救回来（2026-09-07 审计 P2）。
	const maxHeatmapWindow = 7 * 24 * time.Hour
	if timeEnd.Sub(timeStart) > maxHeatmapWindow {
		writeError(w, http.StatusBadRequest, "time range exceeds maximum of 7d")
		return
	}

	// Validate and normalize granularity
	if granularity == "" {
		granularity = "1m"
	}
	bucketSeconds, err := granularitySeconds(granularity)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 桶数上限：窗口 ≤7d 之下再兜一层，防止极细粒度 × 长窗口组合
	// （7d × 1m = 10080 桶）把响应与前端时间轴撑爆。
	const maxHeatmapBuckets = 5000
	if n := int(timeEnd.Sub(timeStart) / (time.Duration(bucketSeconds) * time.Second)); n > maxHeatmapBuckets {
		writeError(w, http.StatusBadRequest, "bucket count exceeds maximum of 5000, use a coarser granularity")
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
	credentials, err := runHeatmapQuery(ctx, m.h.db, heatmapQueryParams{
		TimeStart:       timeStart,
		TimeEnd:         timeEnd,
		BucketSeconds:   bucketSeconds,
		CredentialIDs:   credentialIDs,
		Models:          models,
		ExcludeSelfTest: excludeSelfTest,
		TenantID:        tenantID,
	})
	if err != nil {
		slog.Error("heatmap query failed", "error", err.Error())
		// 不回传内部 SQL 错误细节（表名/约束名），只留 trace 线索给日志。
		writeError(w, http.StatusInternalServerError, "heatmap query failed")
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
	BucketSeconds   int // bucket width in seconds (epoch-aligned)
	CredentialIDs   []int
	Models          []string
	ExcludeSelfTest bool
	TenantID        string
}

// runHeatmapQuery executes the time-series aggregation query for the heatmap.
// The aggregation is split into CTEs on purpose: error distribution needs
// COUNT per (bucket, error_kind) BEFORE jsonb_object_agg folds it into one
// map — PostgreSQL rejects aggregate calls nested inside another aggregate
// (SQLSTATE 42803). Failed-request samples reuse the same per-bucket grouping.
func buildHeatmapSQL(p heatmapQueryParams) (string, []any) {
	// Build WHERE clauses for the base CTE
	whereClauses := []string{
		"rl.ts >= $1",
		"rl.ts < $2",
		"rl.credential_id IS NOT NULL",
	}
	args := []any{p.TimeStart, p.TimeEnd}
	argIdx := 3

	if p.ExcludeSelfTest {
		// Probe/self-test rows are marked on request_logs itself: legacy
		// ActiveProbeWorker sets task_type='probe_triggered' and every probe
		// path tags quality_flags with 'probe'. request_context_attrs.is_probe
		// is NOT usable here — direct-probe rows carry no rca row at all.
		whereClauses = append(whereClauses, "COALESCE(rl.task_type, '') <> 'probe_triggered' AND NOT ('probe' = ANY(rl.quality_flags))")
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

	query := fmt.Sprintf(`
		WITH base AS (
			SELECT
				rl.credential_id,
				lower(COALESCE(rl.outbound_model, rl.client_model)) AS raw_model_name,
				to_timestamp(floor(EXTRACT(EPOCH FROM rl.ts) / %[1]d) * %[1]d) AS time_bucket,
				rl.success,
				rl.latency_ms,
				rl.error_kind,
				rl.request_id,
				rl.ts
			FROM request_logs_with_current_month rl
			JOIN credentials c ON c.id = rl.credential_id
			WHERE %[2]s
		),
		bucket_stats AS (
			SELECT
				credential_id,
				raw_model_name,
				time_bucket,
				COUNT(*) AS total_requests,
				COUNT(*) FILTER (WHERE success) AS success_count,
				COUNT(*) FILTER (WHERE NOT success) AS failed_count,
				ROUND(CAST(COUNT(*) FILTER (WHERE success) AS numeric) / NULLIF(COUNT(*), 0), 4) AS success_rate,
				ROUND(AVG(latency_ms))::int AS avg_latency_ms,
				percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)::int AS p95_latency_ms
			FROM base
			GROUP BY credential_id, raw_model_name, time_bucket
		),
		error_counts AS (
			SELECT
				credential_id,
				raw_model_name,
				time_bucket,
				error_kind,
				COUNT(*) AS error_count
			FROM base
			WHERE NOT success AND error_kind IS NOT NULL
			GROUP BY credential_id, raw_model_name, time_bucket, error_kind
		),
		error_agg AS (
			SELECT
				credential_id,
				raw_model_name,
				time_bucket,
				jsonb_object_agg(error_kind, error_count) AS error_distribution
			FROM error_counts
			GROUP BY credential_id, raw_model_name, time_bucket
		),
		failed_samples AS (
			SELECT
				credential_id,
				raw_model_name,
				time_bucket,
				ARRAY(
					SELECT request_id
					FROM base b2
					WHERE b2.credential_id = b.credential_id
					  AND b2.raw_model_name = b.raw_model_name
					  AND b2.time_bucket = b.time_bucket
					  AND NOT b2.success
					ORDER BY b2.ts DESC
					LIMIT 10
				) AS sample_request_ids
			FROM base b
			WHERE NOT success
			GROUP BY credential_id, raw_model_name, time_bucket
		)
		SELECT
			c.id AS credential_id,
			COALESCE(c.label, '') AS label,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			bs.raw_model_name,
			bs.time_bucket,
			bs.total_requests,
			bs.success_count,
			bs.failed_count,
			bs.success_rate,
			bs.avg_latency_ms,
			bs.p95_latency_ms,
			COALESCE(ea.error_distribution, '{}'::jsonb) AS error_distribution,
			COALESCE(fs.sample_request_ids, ARRAY[]::text[]) AS sample_request_ids
		FROM bucket_stats bs
		JOIN credentials c ON c.id = bs.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		LEFT JOIN error_agg ea
			ON ea.credential_id = bs.credential_id
			AND ea.raw_model_name = bs.raw_model_name
			AND ea.time_bucket = bs.time_bucket
		LEFT JOIN failed_samples fs
			ON fs.credential_id = bs.credential_id
			AND fs.raw_model_name = bs.raw_model_name
			AND fs.time_bucket = bs.time_bucket
		ORDER BY c.id, bs.raw_model_name, bs.time_bucket
	`, int(p.BucketSeconds), whereClause)

	return query, args
}

// runHeatmapQuery executes the time-series aggregation query for the heatmap.
//
// The aggregation is split into CTEs on purpose:
//   - error distribution needs COUNT per (bucket, error_kind) BEFORE
//     jsonb_object_agg folds it into one map — PostgreSQL rejects aggregate
//     calls nested inside another aggregate (SQLSTATE 42803);
//   - failed-request samples reuse the same per-bucket grouping.
//
// Buckets are epoch-aligned via to_timestamp(floor(epoch/N)*N) because
// date_trunc only accepts fixed field names, not '5 minutes'.
func runHeatmapQuery(ctx context.Context, db pgxQueryer, p heatmapQueryParams) ([]HeatmapCredential, error) {
	query, args := buildHeatmapSQL(p)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Parse results and group by credential -> model
	credMap := make(map[int]*HeatmapCredential)
	modelIndex := make(map[string]int) // key: "credID:modelName" -> index in cred.Models

	for rows.Next() {
		var (
			credentialID     int
			label            string
			providerName     string
			rawModelName     string
			timeBucket       time.Time
			totalRequests    int
			successCount     int
			failedCount      int
			successRate      float64
			avgLatencyMs     *int
			p95LatencyMs     *int
			errorDistJSON    []byte
			sampleRequestIDs []string
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
			if err := json.Unmarshal(errorDistJSON, &errorDist); err != nil {
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

		// Get or create model row (rows arrive ordered by credential, model, bucket)
		modelKey := fmt.Sprintf("%d:%s", credentialID, rawModelName)
		idx, ok := modelIndex[modelKey]
		if !ok {
			cred.Models = append(cred.Models, HeatmapModel{
				RawModelName: rawModelName,
				Buckets:      []HeatmapBucket{},
			})
			idx = len(cred.Models) - 1
			modelIndex[modelKey] = idx
		}
		cred.Models[idx].Buckets = append(cred.Models[idx].Buckets, bucket)
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

// granularitySeconds converts a granularity label to its bucket width in
// seconds. Buckets are epoch-aligned, so every granularity is representable —
// unlike date_trunc which only accepts fixed field names.
func granularitySeconds(granularity string) (int, error) {
	switch granularity {
	case "1m":
		return 60, nil
	case "5m":
		return 5 * 60, nil
	case "15m":
		return 15 * 60, nil
	case "1h":
		return 60 * 60, nil
	case "1d":
		return 24 * 60 * 60, nil
	default:
		return 0, fmt.Errorf("invalid granularity: must be one of 1m, 5m, 15m, 1h, 1d")
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
