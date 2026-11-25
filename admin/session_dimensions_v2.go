package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) sessionDimensionV2(dimension string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.handleSessionDimensionV2(w, r, dimension)
	}
}

func sessionAnalyticsWithDefaultDates(r *http.Request, now time.Time) *http.Request {
	q := r.URL.Query()
	if q.Get("start_date") == "" && q.Get("end_date") == "" {
		days := queryInt(r, "days", 30)
		if hours := queryInt(r, "hours", 0); hours > 0 && hours <= 90*24 {
			days = (hours + 23) / 24
		}
		if days < 1 || days > 90 {
			days = 30
		}
		today := now.In(settings.SystemTimezoneLocation())
		q.Set("start_date", today.AddDate(0, 0, 1-days).Format(time.DateOnly))
		q.Set("end_date", today.Format(time.DateOnly))
		r = r.Clone(r.Context())
		r.URL.RawQuery = q.Encode()
	}
	return r
}

func (h *Handler) handleSessionDimensionV2(w http.ResponseWriter, r *http.Request, dimension string) {
	now := time.Now()
	r = sessionAnalyticsWithDefaultDates(r, now)
	q := r.URL.Query()
	scope, ok := prepareSessionRankingScope(w, r)
	if !ok {
		return
	}
	if q.Get("start_time") != "" || q.Get("end_time") != "" {
		start, end, err := parseExactUsageWindow(q.Get("start_time"), q.Get("end_time"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		scope.start, scope.end = start, end
		location := settings.SystemTimezoneLocation()
		q.Set("start_date", start.In(location).Format(time.DateOnly))
		q.Set("end_date", end.Add(-time.Nanosecond).In(location).Format(time.DateOnly))
	}
	id := r.PathValue("id")
	limit, offset := queryInt(r, "limit", 50), queryInt(r, "offset", 0)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 || offset > 2147483647 {
		writeError(w, http.StatusBadRequest, "invalid pagination offset")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	tx, err := beginSessionRankingRead(ctx, h.db, scope)
	if err != nil {
		writeInternalErr(w, "session dimension statistics unavailable", err)
		return
	}
	defer tx.Rollback(ctx)
	activeEnd := now
	if scope.end.Before(activeEnd) {
		activeEnd = scope.end
	}
	result, err := metrics.ReadSessionDimensions(ctx, tx, metrics.SessionDimensionQuery{
		Start: scope.start, End: scope.end, ActiveSince: activeEnd.Add(-24 * time.Hour),
		Tenant: scope.tenant, Timezone: settings.SystemTimezoneName(), Dimension: dimension, ID: id,
		Order: q.Get("order_by"), Limit: limit, Offset: offset, IncludeProbes: queryBool(r, "include_probes"),
	})
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "session dimension statistics unavailable", err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is in progress")
		return
	}
	if len(result.Payload) == 0 {
		writeError(w, http.StatusNotFound, "dimension has no requests in the selected range")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "session dimension statistics unavailable", err)
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result.Payload, &data); err != nil {
		writeInternalErr(w, "invalid session dimension statistics", err)
		return
	}
	response := map[string]any{
		"source": "request_metrics", "timezone": settings.SystemTimezoneName(), "tenant_id": scope.tenant,
		"period_start": scope.start, "period_end": scope.end, "pending_since": result.PendingSince,
		"start_date": q.Get("start_date"), "end_date": q.Get("end_date"),
		"refreshed_at": result.UpdatedAt,
	}
	for key, value := range data {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}
