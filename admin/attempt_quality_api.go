package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

const attemptQualityAPIPath = "/api/admin/quality/attempts"

type attemptQualityAnalyzer interface {
	Analyze(context.Context, string, time.Time, time.Time) ([]providerprofile.AttemptQualityAggregate, error)
}

// AttemptQualityAPI exposes tenant-scoped attempt metrics without changing the
// existing final-request quality API contract.
type AttemptQualityAPI struct {
	analyzer attemptQualityAnalyzer
	pool     *pgxpool.Pool
	now      func() time.Time
}

func NewAttemptQualityAPI(analyzer attemptQualityAnalyzer) *AttemptQualityAPI {
	return &AttemptQualityAPI{analyzer: analyzer, now: time.Now}
}

// NewAttemptQualityAPIWithPool constructs the production API. Reads execute in
// one tenant-scoped read-only transaction so requestjourney RLS is active.
func NewAttemptQualityAPIWithPool(pool *pgxpool.Pool) *AttemptQualityAPI {
	return &AttemptQualityAPI{pool: pool, now: time.Now}
}

func (api *AttemptQualityAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if api == nil || (api.analyzer == nil && api.pool == nil) {
		writeError(w, http.StatusServiceUnavailable, "attempt quality analytics unavailable")
		return
	}
	auth := GetAuthContext(r)
	tenantID := ""
	if auth != nil {
		tenantID = strings.TrimSpace(auth.TenantID)
	}
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant is required")
		return
	}
	hours, err := positiveIntQuery(r, "hours", 24, 168)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerID, err := optionalPositiveInt64Query(r, "provider_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	credentialID, err := optionalPositiveInt64Query(r, "credential_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	end := api.now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	serve := func(analyzer attemptQualityAnalyzer, queryRow func(context.Context, string, ...any) pgx.Row) error {
		aggregates, analyzeErr := analyzer.Analyze(r.Context(), tenantID, start, end)
		if analyzeErr != nil {
			return analyzeErr
		}
		filtered := filterAttemptQuality(aggregates, providerID, credentialID, model)
		finalMetrics, metricsErr := loadFinalRequestMetrics(r.Context(), queryRow, tenantID, start, end, providerID, credentialID, model)
		if metricsErr != nil {
			return metricsErr
		}
		writeJSON(w, http.StatusOK, attemptQualityResponse{
			TenantID: tenantID, Start: start, End: end, Hours: hours,
			SampleSize: attemptQualitySampleSize(filtered), Aggregates: filtered,
			ObservationStatus:   attemptQualityObservationStatus(filtered),
			FinalRequestMetrics: finalMetrics,
		})
		return nil
	}
	if api.pool != nil {
		err = withTenantTx(r.Context(), api.pool, tenantID, func(tx pgx.Tx) error {
			return serve(providerprofile.NewAttemptQualityAnalyzer(requestjourney.NewPostgresRepository(tx)), tx.QueryRow)
		})
	} else {
		err = serve(api.analyzer, nil)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "attempt quality analytics unavailable")
	}
}

type attemptQualityResponse struct {
	TenantID            string                                    `json:"tenant_id"`
	Start               time.Time                                 `json:"start"`
	End                 time.Time                                 `json:"end"`
	Hours               int                                       `json:"hours"`
	SampleSize          int                                       `json:"sample_size"`
	ObservationStatus   requestjourney.ObservationStatus          `json:"observation_status"`
	Aggregates          []providerprofile.AttemptQualityAggregate `json:"aggregates"`
	FinalRequestMetrics finalRequestMetrics                       `json:"final_request_metrics"`
}

type finalRequestMetrics struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessRequests int64   `json:"success_requests"`
	FailureRequests int64   `json:"failure_requests"`
	TotalTokens     int64   `json:"total_tokens"`
	SuccessRate     float64 `json:"success_rate"`
}

func loadFinalRequestMetrics(ctx context.Context, queryRow func(context.Context, string, ...any) pgx.Row, tenantID string, start, end time.Time, providerID, credentialID int64, model string) (finalRequestMetrics, error) {
	if queryRow == nil {
		return finalRequestMetrics{}, nil
	}
	query := `SELECT COUNT(*)::bigint,
		COUNT(*) FILTER (WHERE success)::bigint,
		COUNT(*) FILTER (WHERE NOT success)::bigint,
		COALESCE(SUM(total_tokens), 0)::bigint
		FROM request_logs_hot
		WHERE tenant_id = $1 AND ts >= $2 AND ts < $3`
	args := []any{tenantID, start.UTC(), end.UTC()}
	if providerID > 0 {
		query += " AND provider_id = $4"
		args = append(args, providerID)
	}
	if credentialID > 0 {
		query += fmt.Sprintf(" AND credential_id = $%d", len(args)+1)
		args = append(args, credentialID)
	}
	if model != "" {
		query += fmt.Sprintf(" AND COALESCE(outbound_model, client_model) = $%d", len(args)+1)
		args = append(args, model)
	}
	var metrics finalRequestMetrics
	if err := queryRow(ctx, query, args...).Scan(&metrics.TotalRequests, &metrics.SuccessRequests, &metrics.FailureRequests, &metrics.TotalTokens); err != nil {
		return finalRequestMetrics{}, err
	}
	if metrics.TotalRequests > 0 {
		metrics.SuccessRate = float64(metrics.SuccessRequests) / float64(metrics.TotalRequests)
	}
	return metrics, nil
}

func positiveIntQuery(r *http.Request, name string, defaultValue, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maximum)
	}
	return value, nil
}

func optionalPositiveInt64Query(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func filterAttemptQuality(aggregates []providerprofile.AttemptQualityAggregate, providerID, credentialID int64, model string) []providerprofile.AttemptQualityAggregate {
	filtered := make([]providerprofile.AttemptQualityAggregate, 0, len(aggregates))
	for _, aggregate := range aggregates {
		if providerID != 0 && aggregate.ProviderID != providerID {
			continue
		}
		if credentialID != 0 && aggregate.CredentialID != credentialID {
			continue
		}
		if model != "" && aggregate.Model != model {
			continue
		}
		filtered = append(filtered, aggregate)
	}
	return filtered
}

func attemptQualitySampleSize(aggregates []providerprofile.AttemptQualityAggregate) int {
	total := 0
	for _, aggregate := range aggregates {
		total += aggregate.AttemptTotal
	}
	return total
}

func attemptQualityObservationStatus(aggregates []providerprofile.AttemptQualityAggregate) requestjourney.ObservationStatus {
	for _, aggregate := range aggregates {
		if aggregate.ObservationDegradedCount > 0 {
			return requestjourney.ObservationDegraded
		}
	}
	return requestjourney.ObservationComplete
}
