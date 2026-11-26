package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const userProfilesV2Path = "/api/admin/session-analytics/v2/users"

type projectedOwnerStats struct {
	TenantID    string    `json:"tenant_id"`
	Key         string    `json:"key"`
	Owner       string    `json:"owner_user"`
	Provider    string    `json:"provider"`
	Requests    int64     `json:"total_requests"`
	Success     int64     `json:"success_count"`
	Failures    int64     `json:"failed_count"`
	Sessions    int64     `json:"active_sessions"`
	Users       int64     `json:"end_user_count"`
	Models      int64     `json:"unique_models"`
	Input       int64     `json:"total_prompt_tokens"`
	Output      int64     `json:"total_completion_tokens"`
	Tokens      int64     `json:"total_tokens"`
	Credits     float64   `json:"total_credits_charged"`
	CostUSD     float64   `json:"total_cost_usd"`
	USDMeasured int64     `json:"usd_measured_requests"`
	AvgUSD      *float64  `json:"avg_profile_usd"`
	NonUSDRows  int64     `json:"cost_rows_outside_usd"`
	FirstSeenAt time.Time `json:"first_request_at"`
	LastSeenAt  time.Time `json:"last_request_at"`
}

func (row projectedOwnerStats) costWithEvidence() *float64 {
	if row.USDMeasured == 0 {
		return nil
	}
	return &row.CostUSD
}

func (h *Handler) handleUserProfilesV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, allowed := requestStatsV2Scope(r)
	if !allowed {
		writeError(w, http.StatusForbidden, "request statistics require a resolved identity")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	params := r.URL.Query()
	group, startKey, endKey := "owner-directory", "date_from", "date_to"
	if r.URL.Path == userProfilesV2Path+"/usage-matrix" {
		group, startKey, endKey = "owner-models", "start_date", "end_date"
		if selected := strings.TrimSpace(params.Get("owner")); selected != "" {
			if owner != "" && selected != owner {
				writeError(w, http.StatusForbidden, "user profile is outside the request scope")
				return
			}
			owner = selected
		}
	}
	start, end, err := projectedOwnerUsageWindow(params.Get(startKey), params.Get(endKey), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := metrics.RequestStatsQuery{
		MetricsFilter: metrics.MetricsFilter{TenantID: tenant, StartTime: start, EndTime: end, IncludeProbes: queryBool(r, "include_probes")},
		Group:         group, Owner: owner, Timezone: settings.SystemTimezoneName(),
		Limit: min(200, max(1, queryInt(r, "limit", 50))), Offset: max(0, queryInt(r, "offset", 0)),
		OwnerSearch: strings.TrimSpace(params.Get("search")),
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadRequestStats(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics user profiles failed", err)
		return
	}
	payload, err := projectedOwnerPayload(result, query)
	if err != nil {
		writeInternalErr(w, "request metrics user profile response failed", err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func projectedOwnerUsageWindow(from, to string, now time.Time) (time.Time, time.Time, error) {
	location := settings.SystemTimezoneLocation()
	parse := func(value string, isEnd bool) (time.Time, error) {
		if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return stamp, nil
		}
		day, err := time.ParseInLocation(time.DateOnly, value, location)
		if err != nil {
			return time.Time{}, fmt.Errorf("profile dates must use YYYY-MM-DD or RFC3339")
		}
		if isEnd {
			day = day.AddDate(0, 0, 1)
		}
		return day, nil
	}
	end := now
	var err error
	if to != "" {
		end, err = parse(to, true)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	start := end.In(location).AddDate(0, 0, -30)
	if from != "" {
		start, err = parse(from, false)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !end.After(start) || end.After(start.In(location).AddDate(0, 0, 90)) {
		return time.Time{}, time.Time{}, fmt.Errorf("profile statistics require a positive window of at most 90 calendar days")
	}
	return start.UTC(), end.UTC(), nil
}

func projectedOwnerPayload(result metrics.RequestStats, query metrics.RequestStatsQuery) (map[string]any, error) {
	meta := userUsageMeta{SourceView: result.Source, WindowStart: result.Start.UTC().Format(time.RFC3339Nano), WindowEnd: result.End.UTC().Format(time.RFC3339Nano)}
	payload := map[string]any{"usage_meta": meta, "ready": result.Ready, "updated_at": result.UpdatedAt, "pending_since": result.PendingSince}
	if !result.Ready {
		payload["degraded"], payload["degraded_reason"] = true, "request_metrics_backfill_incomplete"
	}
	if query.Group == "owner-models" {
		rows := make([]map[string]any, 0, len(result.Items))
		for _, item := range result.Items {
			var row projectedOwnerStats
			if err := json.Unmarshal(item, &row); err != nil {
				return nil, err
			}
			rows = append(rows, map[string]any{
				"tenant_id": row.TenantID, "owner_user": row.Owner, "model_name": row.Key, "provider": row.Provider,
				"request_count": row.Requests, "input_tokens": row.Input, "output_tokens": row.Output, "total_tokens": row.Tokens,
				"internal_credits": row.Credits, "external_cost_usd": row.costWithEvidence(), "cost_rows_outside_usd": row.NonUSDRows,
				"usd_unmeasured_requests": row.Requests - row.USDMeasured,
				"first_used_at":           row.FirstSeenAt, "last_used_at": row.LastSeenAt,
			})
		}
		payload["rows"], payload["start_date"], payload["end_date"] = rows, meta.WindowStart, meta.WindowEnd
		return payload, nil
	}
	var directory struct {
		Total int                   `json:"total"`
		Users []projectedOwnerStats `json:"users"`
	}
	if len(result.Items) != 0 {
		if err := json.Unmarshal(result.Items[0], &directory); err != nil {
			return nil, err
		}
	}
	users := make([]map[string]any, 0, len(directory.Users))
	for _, row := range directory.Users {
		users = append(users, map[string]any{
			"tenant_id": row.TenantID, "owner_user": row.Key, "session_count": row.Sessions, "total_requests": row.Requests,
			"total_cost_usd": row.costWithEvidence(), "avg_cost_per_session": row.AvgUSD, "end_user_count": row.Users,
			"usd_unmeasured_requests": row.Requests - row.USDMeasured,
			"total_success":           row.Success, "total_errors": row.Failures, "first_seen_at": row.FirstSeenAt, "last_seen_at": row.LastSeenAt,
			"window_usage": map[string]any{
				"request_count": row.Requests, "input_tokens": row.Input, "output_tokens": row.Output, "total_tokens": row.Tokens,
				"internal_credits": row.Credits, "external_cost_usd": row.costWithEvidence(), "cost_rows_outside_usd": row.NonUSDRows,
				"usd_unmeasured_requests": row.Requests - row.USDMeasured,
				"session_count":           row.Sessions, "model_count": row.Models,
			},
		})
	}
	payload["users"], payload["total"], payload["limit"], payload["offset"] = users, directory.Total, query.Limit, query.Offset
	payload["usage_window"] = map[string]string{"start": meta.WindowStart, "end": meta.WindowEnd}
	return payload, nil
}
