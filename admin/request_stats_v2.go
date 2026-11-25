package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const requestStatsV2Path = "/api/admin/stats/v2"
const usageV2Path = "/api/usage/v2"

func (h *Handler) handleRequestStatsV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, ok := requestStatsV2Scope(r)
	if !ok {
		writeError(w, http.StatusForbidden, "request statistics require administrator access")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	query, err := parseRequestStatsV2(r, tenant)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query.Owner = owner
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadRequestStats(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics query failed", err)
		return
	}
	if strings.HasPrefix(r.URL.Path, usageV2Path+"/") {
		writeUsageMetricsV2(w, r, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Regular users can read their own traffic. A requested tenant can only narrow
// an administrator's scope or name the authenticated user's own tenant.
func requestStatsV2Scope(r *http.Request) (tenant, owner string, ok bool) {
	requested := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if scope, allowed := requestReconciliationScope(r, requested); allowed {
		return scope.TenantID, "", true
	}
	if auth := GetAuthContext(r); IsRegularUser(r) && auth.TenantID != "" && auth.Username != "" &&
		(requested == "" || requested == auth.TenantID) {
		return auth.TenantID, auth.Username, true
	}
	return "", "", false
}

func parseExactUsageWindow(from, to string) (time.Time, time.Time, error) {
	start, startErr := time.Parse(time.RFC3339Nano, from)
	end, endErr := time.Parse(time.RFC3339Nano, to)
	if startErr != nil || endErr != nil || !end.After(start) || end.Sub(start) > 368*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid statistics window")
	}
	return start, end, nil
}

func parseRequestStatsV2(r *http.Request, tenant string) (metrics.RequestStatsQuery, error) {
	return parseRequestStatsV2WithDefaultDays(r, tenant, 0)
}

func parseRequestStatsV2WithDefaultDays(r *http.Request, tenant string, defaultDays int) (metrics.RequestStatsQuery, error) {
	params := r.URL.Query()
	query := metrics.RequestStatsQuery{Timezone: settings.SystemTimezoneName(), Group: "summary"}
	query.TenantID = tenant
	location := settings.SystemTimezoneLocation()
	if value := params.Get("timezone"); value != "" {
		if value == "Local" {
			return query, fmt.Errorf("timezone must be an IANA name or UTC")
		}
		explicitLocation, err := time.LoadLocation(value)
		if err != nil {
			return query, fmt.Errorf("timezone must be an IANA name or UTC")
		}
		location = explicitLocation
		query.Timezone = value
	}
	if params.Has("days") {
		days, err := strconv.Atoi(params.Get("days"))
		if err != nil || days < 1 || days > 366 || params.Has("start") || params.Has("end") {
			return query, fmt.Errorf("days must be between 1 and 366 and cannot be combined with start or end")
		}
	}
	var start, end time.Time
	var err error
	if day := params.Get("day"); day != "" {
		if params.Has("days") || params.Has("start") || params.Has("end") {
			return query, fmt.Errorf("day cannot be combined with days, start or end")
		}
		start, end, err = reconciliation.NaturalDay(day, query.Timezone)
	} else if defaultDays > 0 || strings.HasPrefix(r.URL.Path, usageV2Path+"/") || params.Has("days") {
		if strings.Contains(params.Get("start"), "T") || strings.Contains(params.Get("end"), "T") {
			start, end, err = parseExactUsageWindow(params.Get("start"), params.Get("end"))
		} else {
			if defaultDays == 0 {
				defaultDays = 7
			}
			start, end, err = resolveUsageTimeRangeInLocation(r, defaultDays, location)
		}
	} else {
		start, end, err = statsRange(r)
	}
	if err != nil {
		return query, err
	}
	query.StartTime, query.EndTime = start.UTC(), end.UTC()
	for _, filter := range []struct {
		name string
		dest **int64
	}{
		{"provider_id", &query.ProviderID},
		{"credential_id", &query.Credential},
		{"canonical_id", &query.Canonical},
		{"api_key_id", &query.APIKey},
		{"application_id", &query.Application},
	} {
		if raw := params.Get(filter.name); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return query, fmt.Errorf("%s must be a positive integer", filter.name)
			}
			*filter.dest = &id
		}
	}
	if v := params.Get("include_probes"); v != "" {
		include, err := strconv.ParseBool(v)
		if err != nil {
			return query, fmt.Errorf("include_probes must be a boolean")
		}
		query.IncludeProbes = include
	}
	if v := params.Get("traffic_class"); v != "" {
		query.IncludeProbes = true
		query.TrafficClass = &v
	}
	if v := params.Get("session_id"); v != "" {
		query.SessionID = &v
	}
	query.Model = params.Get("model")

	for _, detail := range []struct {
		name, summary string
		dest          **int64
	}{
		{"provider_id", "provider-summary", &query.ProviderID},
		{"api_key_id", "key-summary", &query.APIKey},
	} {
		raw := r.PathValue(detail.name)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return query, fmt.Errorf("%s must be a positive integer", detail.name)
		}
		*detail.dest = &id
		switch {
		case strings.HasSuffix(r.URL.Path, "/trend"):
			query.Group = params.Get("period")
			if query.Group == "" {
				query.Group = "day"
			}
			switch query.Group {
			case "minute", "hour", "day", "week", "month":
				if err := validateTrendGranularityWindow(start, end, query.Group); err != nil {
					return query, err
				}
			default:
				return query, fmt.Errorf("unsupported trend period")
			}
		case strings.HasSuffix(r.URL.Path, "/models"):
			query.Group, query.Limit = "model", 100
		case strings.HasSuffix(r.URL.Path, "/daily-models"):
			query.Group = "day-model"
		case strings.HasSuffix(r.URL.Path, "/export"):
			query.Group = "model"
		default:
			query.Group = detail.summary
		}
	}

	switch r.URL.Path {
	case usageV2Path + "/by-model":
		query.Group = "model"
	case usageV2Path + "/by-provider", usageV2Path + "/providers/export":
		query.Group = "provider"
	case usageV2Path + "/hot-keys":
		query.Group, query.Limit = "key", 10
	case requestStatsV2Path + "/errors":
		query.Group, query.Limit = "errors", 100
	case requestStatsV2Path + "/trend":
		query.Group = params.Get("interval")
		switch query.Group {
		case "":
			query.Group = "day"
		case "hour":
			if end.Sub(start) > 31*24*time.Hour {
				return query, fmt.Errorf("hourly trend window cannot exceed 31 days")
			}
		case "day", "month":
		default:
			return query, fmt.Errorf("interval must be hour, day or month")
		}
	case requestStatsV2Path + "/breakdown":
		query.Group, query.Limit = params.Get("dimension"), 25
		switch query.Group {
		case "model", "provider", "key", "credential", "canonical", "application", "tenant", "person", "client", "owner", "task", "session", "modality", "protocol", "currency":
		default:
			return query, fmt.Errorf("unsupported breakdown dimension")
		}
	}
	if query.Group != "summary" && params.Has("limit") {
		limit, err := strconv.Atoi(params.Get("limit"))
		if err != nil || limit < 1 || limit > 500 {
			return query, fmt.Errorf("limit must be between 1 and 500")
		}
		query.Limit = limit
	}
	return query, nil
}

// Keep the established usage widget response fields in the versioned endpoints.
func writeUsageMetricsV2(w http.ResponseWriter, r *http.Request, result metrics.RequestStats) {
	detail := r.PathValue("provider_id") != "" || r.PathValue("api_key_id") != ""
	export := strings.HasSuffix(r.URL.Path, "/export")
	if !result.Ready && (detail || export) {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is running")
		return
	}
	if result.Group == "summary" || result.Group == "provider-summary" || result.Group == "key-summary" {
		fields := map[string]json.RawMessage{}
		if len(result.Items) == 1 {
			if err := json.Unmarshal(result.Items[0], &fields); err != nil {
				writeInternalErr(w, "request metrics response failed", err)
				return
			}
		}
		fields["source"] = json.RawMessage(`"request_metrics"`)
		fields["updated_at"], _ = json.Marshal(result.UpdatedAt)
		fields["pending_since"], _ = json.Marshal(result.PendingSince)
		fields["degraded"] = json.RawMessage(strconv.FormatBool(!result.Ready))
		if result.Group == "key-summary" {
			id, _ := strconv.ParseInt(r.PathValue("api_key_id"), 10, 64)
			fields["key_id"], _ = json.Marshal(id)
			fields["window_start"], _ = json.Marshal(result.Start)
			fields["window_end"], _ = json.Marshal(result.End)
		}
		if result.Group == "provider-summary" {
			fields["request_count"] = fields["total_requests"]
			fields["prompt_tokens"] = fields["total_prompt_tokens"]
			fields["completion_tokens"] = fields["total_completion_tokens"]
			fields["window_start"], _ = json.Marshal(result.Start)
			fields["window_end"], _ = json.Marshal(result.End)
		}
		if !result.Ready {
			fields["degraded_reason"] = json.RawMessage(`"request_metrics_backfill"`)
			fields["missing_view"] = json.RawMessage(`"request_metrics"`)
		}
		writeJSON(w, http.StatusOK, fields)
		return
	}
	for i, raw := range result.Items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			writeInternalErr(w, "request metrics response failed", err)
			return
		}
		fields["request_count"] = fields["total_requests"]
		fields["last_used_at"] = fields["last_request_at"]
		fields["first_used_at"] = fields["first_request_at"]
		fields["prompt_tokens"] = fields["total_prompt_tokens"]
		fields["completion_tokens"] = fields["total_completion_tokens"]
		fields["cost_usd"] = fields["total_cost_usd"]
		switch result.Group {
		case "day-model":
			fields["date"] = fields["key"]
		case "minute", "hour", "day", "week", "month":
			fields["period"] = fields["key"]
			fields["requests"] = fields["total_requests"]
		}
		item, err := json.Marshal(fields)
		if err != nil {
			writeInternalErr(w, "request metrics response failed", err)
			return
		}
		result.Items[i] = item
	}
	if export {
		writeProviderMetricsCSV(w, result)
		return
	}
	if detail {
		writeJSON(w, http.StatusOK, result.Items)
		return
	}
	payload := map[string]any{
		"source": result.Source, "items": result.Items, "degraded": !result.Ready,
		"updated_at": result.UpdatedAt, "pending_since": result.PendingSince,
	}
	if !result.Ready {
		payload["degraded_reason"] = "request_metrics_backfill"
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeProviderMetricsCSV(w http.ResponseWriter, result metrics.RequestStats) {
	headers := []string{"provider_id", "provider_name", "provider_code", "request_count", "prompt_tokens", "completion_tokens", "total_tokens", "total_cost_usd", "success_rate", "cost_rows_outside_usd"}
	if result.Group == "model" {
		headers = []string{"provider_code", "model", "request_count", "prompt_tokens", "completion_tokens", "total_tokens", "total_cost_usd", "success_rate", "cost_rows_outside_usd"}
	}
	data, err := reconciliationRowsCSV(headers, result.Items)
	if err != nil {
		writeInternalErr(w, "request metrics export failed", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="provider-usage.csv"`)
	_, _ = w.Write(data)
}
