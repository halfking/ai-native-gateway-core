package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type vendorCredentialErrorDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type vendorCredentialErrorHandlers struct{ db vendorCredentialErrorDB }

type vendorCredentialMeta struct {
	ID                  int64      `json:"id"`
	Label               string     `json:"label"`
	ProviderID          int64      `json:"provider_id"`
	HealthStatus        string     `json:"health_status"`
	HealthError         *string    `json:"health_error"`
	HealthLatencyMs     *int       `json:"health_latency_ms"`
	AvailabilityState   string     `json:"availability_state"`
	StateReasonCode     *string    `json:"state_reason_code"`
	StateReasonDetail   *string    `json:"state_reason_detail"`
	StateUpdatedAt      *time.Time `json:"state_updated_at"`
	QuotaState          string     `json:"quota_state"`
	LifecycleStatus     string     `json:"lifecycle_status"`
	CircuitState        string     `json:"circuit_state"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	ManualDisabled      bool       `json:"manual_disabled"`
	BalanceUSD          *float64   `json:"balance_usd"`
	BalanceCurrency     *string    `json:"balance_currency"`
}

type vendorErrorKindStat struct {
	ErrorKind           string    `json:"error_kind"`
	Count               int       `json:"count"`
	LastSeen            time.Time `json:"last_seen"`
	DistinctStatusCodes int       `json:"distinct_status_codes"`
}

type vendorRecentFailure struct {
	Ts                      time.Time `json:"ts"`
	RequestID               string    `json:"request_id"`
	RawModelName            string    `json:"raw_model_name"`
	AttemptIndex            int       `json:"attempt_index"`
	ErrorKind               string    `json:"error_kind"`
	ErrorMessage            *string   `json:"error_message"`
	UpstreamStatusCode      *int      `json:"upstream_status_code"`
	UpstreamResponsePreview *string   `json:"upstream_response_preview"`
	LatencyMs               *int      `json:"latency_ms"`
}

type vendorQualityScore struct {
	ProfileDate       time.Time `json:"profile_date"`
	TotalScore        float64   `json:"total_score"`
	AvailabilityScore float64   `json:"availability_score"`
	StabilityScore    float64   `json:"stability_score"`
}

func (h *vendorCredentialErrorHandlers) getVendorCredentialErrorDetail(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeErrorWithCode(w, http.StatusServiceUnavailable, "db_not_configured", "database is not configured")
		return
	}

	credentialID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || credentialID <= 0 {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_credential_id", "credential id must be a positive integer")
		return
	}
	hours, err := parseVendorErrorHours(r.URL.Query().Get("hours"))
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_hours", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	credential, err := h.loadVendorCredentialMeta(ctx, credentialID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErrorWithCode(w, http.StatusNotFound, "credential_not_found", "credential not found")
			return
		}
		slog.Error("load vendor credential detail failed", "operation", "load credential", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "credential_query_failed", "failed to load credential detail")
		return
	}

	summary, err := h.loadVendorErrorSummary(ctx, credentialID, since)
	if err != nil {
		slog.Error("load vendor error summary failed", "operation", "load error summary", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "error_summary_query_failed", "failed to load error summary")
		return
	}
	recent, err := h.loadVendorRecentFailures(ctx, credentialID, since)
	if err != nil {
		slog.Error("load vendor recent failures failed", "operation", "load recent failures", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "recent_failures_query_failed", "failed to load recent failures")
		return
	}
	scores, err := h.loadVendorQualityScores(ctx, credentialID)
	if err != nil {
		slog.Error("load vendor quality scores failed", "operation", "load quality scores", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "quality_scores_query_failed", "failed to load quality scores")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":     credentialID,
		"credential_label":  credential.Label,
		"credential":        credential,
		"error_summary":     summary,
		"recent_failures":   recent,
		"quality_scores_7d": scores,
		"hours":             hours,
		"since":             since,
	})
}

func parseVendorErrorHours(raw string) (int, error) {
	if raw == "" {
		return 24, nil
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || (hours != 1 && hours != 24 && hours != 168) {
		return 0, fmt.Errorf("hours must be 1, 24, or 168")
	}
	return hours, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorCredentialMeta(ctx context.Context, id int64) (vendorCredentialMeta, error) {
	var c vendorCredentialMeta
	err := h.db.QueryRow(ctx, `
		SELECT id, label, provider_id, health_status, health_error, health_latency_ms,
		       availability_state, state_reason_code, state_reason_detail, state_updated_at,
		       quota_state, lifecycle_status, circuit_state, consecutive_failures,
		       manual_disabled, balance_usd, balance_currency
		FROM credentials WHERE id = $1
	`, id).Scan(&c.ID, &c.Label, &c.ProviderID, &c.HealthStatus, &c.HealthError, &c.HealthLatencyMs,
		&c.AvailabilityState, &c.StateReasonCode, &c.StateReasonDetail, &c.StateUpdatedAt,
		&c.QuotaState, &c.LifecycleStatus, &c.CircuitState, &c.ConsecutiveFailures,
		&c.ManualDisabled, &c.BalanceUSD, &c.BalanceCurrency)
	if err != nil {
		return c, fmt.Errorf("load credential metadata failed: %w (credential_id=%d)", err, id)
	}
	return c, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorErrorSummary(ctx context.Context, id int64, since time.Time) ([]vendorErrorKindStat, error) {
	rows, err := h.db.Query(ctx, `
		SELECT error_kind, COUNT(*)::int, MAX(ts), COUNT(DISTINCT upstream_status_code)::int
		FROM candidate_failure_logs_with_current_month
		WHERE credential_id = $1 AND ts >= $2
		GROUP BY error_kind ORDER BY COUNT(*) DESC
	`, id, since)
	if err != nil {
		return nil, fmt.Errorf("query vendor error summary failed: %w (credential_id=%d)", err, id)
	}
	defer rows.Close()
	result := make([]vendorErrorKindStat, 0)
	for rows.Next() {
		var item vendorErrorKindStat
		if err := rows.Scan(&item.ErrorKind, &item.Count, &item.LastSeen, &item.DistinctStatusCodes); err != nil {
			return nil, fmt.Errorf("scan vendor error summary failed: %w (credential_id=%d)", err, id)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor error summary failed: %w (credential_id=%d)", err, id)
	}
	return result, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorRecentFailures(ctx context.Context, id int64, since time.Time) ([]vendorRecentFailure, error) {
	rows, err := h.db.Query(ctx, `
		SELECT ts, request_id, raw_model_name, attempt_index, error_kind, error_message,
		       upstream_status_code, upstream_response_preview, latency_ms
		FROM candidate_failure_logs_with_current_month
		WHERE credential_id = $1 AND ts >= $2
		ORDER BY ts DESC LIMIT 10
	`, id, since)
	if err != nil {
		return nil, fmt.Errorf("query vendor recent failures failed: %w (credential_id=%d)", err, id)
	}
	defer rows.Close()
	result := make([]vendorRecentFailure, 0, 10)
	for rows.Next() {
		var item vendorRecentFailure
		if err := rows.Scan(&item.Ts, &item.RequestID, &item.RawModelName, &item.AttemptIndex, &item.ErrorKind,
			&item.ErrorMessage, &item.UpstreamStatusCode, &item.UpstreamResponsePreview, &item.LatencyMs); err != nil {
			return nil, fmt.Errorf("scan vendor recent failure failed: %w (credential_id=%d)", err, id)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor recent failures failed: %w (credential_id=%d)", err, id)
	}
	return result, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorQualityScores(ctx context.Context, id int64) ([]vendorQualityScore, error) {
	rows, err := h.db.Query(ctx, `
		SELECT profile_date, total_score, availability_score, stability_score
		FROM provider_profile_daily
		WHERE credential_id = $1 AND profile_date >= CURRENT_DATE - 7
		ORDER BY profile_date DESC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("query vendor quality scores failed: %w (credential_id=%d)", err, id)
	}
	defer rows.Close()
	result := make([]vendorQualityScore, 0, 7)
	for rows.Next() {
		var item vendorQualityScore
		if err := rows.Scan(&item.ProfileDate, &item.TotalScore, &item.AvailabilityScore, &item.StabilityScore); err != nil {
			return nil, fmt.Errorf("scan vendor quality score failed: %w (credential_id=%d)", err, id)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor quality scores failed: %w (credential_id=%d)", err, id)
	}
	return result, nil
}
