package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) sessionPeopleV2(section string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.handleSessionPeopleV2(w, r, section)
	}
}

func (h *Handler) handleSessionPeopleV2(w http.ResponseWriter, r *http.Request, section string) {
	scope, ok := prepareSessionRankingScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	window := metrics.SessionPeopleQuery{
		Start: scope.start, End: scope.end, PreviousStart: scope.start, Tenant: scope.tenant,
		Owner: r.PathValue("owner"), Timezone: settings.SystemTimezoneName(),
		Section: section, SortBy: q.Get("sort_by"), Order: q.Get("order"),
		Limit: queryInt(r, "limit", 50), Offset: queryInt(r, "offset", 0),
	}
	switch window.SortBy {
	case "", "sessions", "tokens", "cost", "last_active":
	default:
		writeError(w, http.StatusBadRequest, "invalid personnel statistics sort")
		return
	}
	if (window.Order != "" && window.Order != "asc" && window.Order != "desc") ||
		window.Limit < 1 || window.Limit > 200 || window.Offset < 0 || window.Offset > 2147483647 {
		writeError(w, http.StatusBadRequest, "invalid personnel statistics pagination or order")
		return
	}
	if section == "detail" || section == "overview" {
		location := settings.SystemTimezoneLocation()
		start, end := scope.start.In(location), scope.end.In(location)
		startLabel := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		endLabel := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		window.PreviousStart = start.AddDate(0, 0, -int(endLabel.Sub(startLabel).Hours()/24))
	}
	switch section {
	case "ranking":
		window.Project = q.Get("project")
	case "tasks":
		window.Project = r.PathValue("project")
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	tx, err := beginSessionRankingRead(ctx, h.db, scope)
	if err != nil {
		writeInternalErr(w, "session personnel statistics unavailable", err)
		return
	}
	defer tx.Rollback(ctx)
	var result metrics.ProjectedAccountActivity
	var matchingTenants int
	if section == "overview" {
		result, err = metrics.ReadSessionAnalyticsOverview(ctx, tx, metrics.SessionAnalyticsOverviewQuery{
			Start: window.Start, End: window.End, PreviousStart: window.PreviousStart,
			Tenant: window.Tenant, Timezone: window.Timezone,
		})
	} else {
		var people metrics.SessionPeopleResult
		people, err = metrics.ReadSessionPeople(ctx, tx, window)
		result, matchingTenants = people.ProjectedAccountActivity, people.MatchingTenants
	}
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "session personnel statistics unavailable", err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is in progress")
		return
	}
	if section != "ranking" && section != "overview" && matchingTenants != 1 {
		if matchingTenants > 1 {
			writeError(w, http.StatusConflict, "owner is present in multiple tenants; tenant_id is required")
			return
		}
		writeError(w, http.StatusNotFound, "owner is unavailable")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "session personnel statistics unavailable", err)
		return
	}
	data := map[string]json.RawMessage{}
	if err := json.Unmarshal(result.Payload, &data); err != nil {
		writeInternalErr(w, "invalid session personnel statistics", err)
		return
	}
	response := map[string]any{
		"source": "request_metrics", "timezone": window.Timezone, "period_start": scope.start,
		"period_end": scope.end, "updated_at": result.UpdatedAt, "pending_since": result.PendingSince,
	}
	for key, value := range data {
		response[key] = value
	}
	writeJSON(w, http.StatusOK, response)
}
