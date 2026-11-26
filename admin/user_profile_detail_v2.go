package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) handleUserProfileDetailV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, self, allowed := requestStatsV2Scope(r)
	owner := strings.TrimSpace(r.PathValue("owner"))
	if !allowed || (self != "" && self != owner) {
		writeError(w, http.StatusForbidden, "user profile is outside the request scope")
		return
	}
	if owner == "" {
		writeError(w, http.StatusBadRequest, "owner_user is required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	r = sessionAnalyticsWithDefaultDates(r, time.Now())
	params := r.URL.Query()
	start, end, err := settings.SystemDayRangeUtc(params.Get("start_date"), params.Get("end_date"))
	if params.Get("start_time") != "" || params.Get("end_time") != "" {
		start, end, err = parseExactUsageWindow(params.Get("start_time"), params.Get("end_time"))
	}
	if err != nil || end.After(start.In(settings.SystemTimezoneLocation()).AddDate(0, 0, 90)) {
		writeError(w, http.StatusBadRequest, "invalid profile date range")
		return
	}
	scope := sessionRankingScope{tenant: tenant, role: "tenant_admin", bypass: "false"}
	if IsSuperAdminOrLegacy(r) {
		scope.role, scope.bypass = "super_admin", "true"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	tx, err := beginSessionRankingRead(ctx, h.db, scope)
	if err != nil {
		writeInternalErr(w, "user profile scope unavailable", err)
		return
	}
	defer tx.Rollback(ctx)

	lookup := metrics.SessionPeopleQuery{
		Start: start, End: end, PreviousStart: start, Tenant: tenant, Owner: owner,
		Timezone: settings.SystemTimezoneName(), Section: "identity",
	}
	selected, err := metrics.ReadSessionPeople(ctx, tx, lookup)
	if err != nil {
		writeUserProfileV2ReadError(w, err)
		return
	}
	if !selected.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is in progress")
		return
	}
	if selected.MatchingTenants != 1 {
		if selected.MatchingTenants > 1 {
			writeError(w, http.StatusConflict, "owner is present in multiple tenants; tenant_id is required")
		} else {
			writeError(w, http.StatusNotFound, "owner is unavailable")
		}
		return
	}
	var identity struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(selected.Payload, &identity); err != nil {
		writeInternalErr(w, "invalid projected profile identity", err)
		return
	}
	response := map[string]any{
		"owner_user": owner, "tenant_id": identity.TenantID, "source": "request_metrics",
		"timezone": lookup.Timezone, "period_start": start, "period_end": end, "degraded": false,
		"usage_meta": userUsageMeta{SourceView: "request_metrics", WindowStart: start.UTC().Format(time.RFC3339Nano), WindowEnd: end.UTC().Format(time.RFC3339Nano)},
	}
	if strings.HasSuffix(r.Pattern, "/projects") {
		lookup.Tenant, lookup.Section = identity.TenantID, "projects"
		projects, readErr := metrics.ReadSessionPeople(ctx, tx, lookup)
		if readErr != nil {
			writeUserProfileV2ReadError(w, readErr)
			return
		}
		var payload struct {
			Projects []map[string]json.RawMessage `json:"projects"`
		}
		if err := json.Unmarshal(projects.Payload, &payload); err != nil {
			writeInternalErr(w, "invalid projected profile projects", err)
			return
		}
		for _, row := range payload.Projects {
			row["project_id"], row["last_activity_at"] = row["project"], row["last_active_at"]
			row["total_cost_usd"], row["total_credits_charged"] = row["total_cost"], row["credits_charged"]
		}
		response["projects"], response["pending_since"] = payload.Projects, projects.PendingSince
	} else {
		result, readErr := metrics.ReadSessionDimensions(ctx, tx, metrics.SessionDimensionQuery{
			Start: start, End: end, ActiveSince: end.Add(-24 * time.Hour), Tenant: identity.TenantID,
			Timezone: lookup.Timezone, Dimension: "owners", ID: owner,
		})
		if readErr != nil {
			writeUserProfileV2ReadError(w, readErr)
			return
		}
		response["pending_since"] = result.PendingSince
		if len(result.Payload) == 0 {
			for _, field := range []string{"session_count", "total_requests", "total_success", "total_errors", "end_user_count"} {
				response[field] = 0
			}
			for _, field := range []string{"models_used", "daily_cost_trend", "top_tasks", "top_end_users", "recent_sessions"} {
				response[field] = []any{}
			}
			response["first_seen_at"], response["last_seen_at"], response["avg_cost_per_session"] = nil, nil, nil
			response["total_cost_usd"] = nil
			response["health_distribution"] = HealthDistribution{}
		} else {
			var detail map[string]json.RawMessage
			if err := json.Unmarshal(result.Payload, &detail); err != nil {
				writeInternalErr(w, "invalid projected profile", err)
				return
			}
			for key, value := range detail {
				response[key] = value
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "user profile unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeUserProfileV2ReadError(w http.ResponseWriter, err error) {
	if IsMissingRelationError(err) {
		writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
		return
	}
	writeInternalErr(w, "user profile unavailable", err)
}
