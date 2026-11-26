package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) HandleSessionFilterOptionsV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, ok := requestStatsV2Scope(r)
	if !ok {
		writeError(w, http.StatusForbidden, "session filters are outside the request scope")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	r = sessionAnalyticsWithDefaultDates(r, time.Now())
	params := r.URL.Query()
	start, end, err := settings.SystemDayRangeUtc(params.Get("start_date"), params.Get("end_date"))
	if err != nil || end.After(start.In(settings.SystemTimezoneLocation()).AddDate(0, 0, 366)) {
		writeError(w, http.StatusBadRequest, "invalid session filter date range")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := metrics.ReadSessionFilterOptions(ctx, h.db, start, end, tenant, owner)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "session filter options unavailable", err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is in progress")
		return
	}
	var data struct {
		Models    []string `json:"models"`
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(result.Payload, &data); err != nil {
		writeInternalErr(w, "invalid session filter options", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": data.Models, "providers": data.Providers, "source": "request_metrics",
		"start_date": params.Get("start_date"), "end_date": params.Get("end_date"),
		"timezone": settings.SystemTimezoneName(), "updated_at": result.UpdatedAt, "pending_since": result.PendingSince,
	})
}
