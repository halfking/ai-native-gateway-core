package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func projectedBoardTimeRange(r *http.Request) (boardTimeRange, error) {
	from, to := r.URL.Query().Get("start"), r.URL.Query().Get("end")
	if strings.Contains(from, "T") || strings.Contains(to, "T") {
		start, end, err := parseExactUsageWindow(from, to)
		if err != nil {
			return boardTimeRange{}, err
		}
		return boardTimeRange{Start: start, End: end, Days: max(1, int(end.Sub(start)/(24*time.Hour))), Custom: true}, nil
	}
	return boardTimeRangeFromRequest(r)
}

func projectedBoardQuery(r *http.Request, tenant, owner string) (metrics.BoardQuery, boardTimeRange, error) {
	tr, err := projectedBoardTimeRange(r)
	if err != nil {
		return metrics.BoardQuery{}, tr, err
	}
	q := metrics.BoardQuery{
		MetricsFilter: metrics.MetricsFilter{
			TenantID: tenant, StartTime: tr.Start, EndTime: tr.End,
			IncludeProbes: queryBool(r, "include_probes"),
		},
		Owner: owner, BucketMinutes: tr.trendBucketMinutes(),
	}
	if raw := r.URL.Query().Get("provider_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			return q, tr, fmt.Errorf("provider_id must be a nonnegative integer")
		}
		if id > 0 {
			q.ProviderID = &id
		}
	}
	return q, tr, nil
}

func (h *Handler) handleDashboardBoardV2(w http.ResponseWriter, r *http.Request) {
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
	query, tr, err := projectedBoardQuery(r, tenant, owner)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()

	board, err := metrics.ReadRequestBoard(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics board failed", err)
		return
	}
	payload, err := projectedBoardPayload(board, query, tr)
	if err != nil {
		writeInternalErr(w, "request metrics board response failed", err)
		return
	}
	if tenant == "" && owner == "" {
		if h.bodySizeTracker != nil {
			if stats, err := h.bodySizeTracker.GetStats(ctx); err == nil {
				payload["body_stats"] = stats
			}
		}
		if includeBoardOperational(r) {
			payload["operational"] = h.boardOperationalPayload(ctx)
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func projectedBoardPayload(board metrics.RequestBoard, query metrics.BoardQuery, tr boardTimeRange) (map[string]any, error) {
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(board.Summary, &summary); err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, fmt.Errorf("missing projected board summary")
	}
	summary["summary_source"] = json.RawMessage(`"request_metrics"`)
	if !board.Ready {
		summary["degraded_summary"] = json.RawMessage("true")
		summary["summary_missing_view"] = json.RawMessage(`"request_metrics"`)
	}
	pies := emptyBoardPies()
	for dimension, rows := range board.Pies {
		items := make([]boardPieItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, boardPieItem(row))
		}
		if dimension == "client_ips" {
			items = classifyClientIPPie(items)
		}
		pies[dimension] = items
	}
	rows := make([]usageTrendRow, 0, len(board.ModelPoints))
	for _, p := range board.ModelPoints {
		rows = append(rows, usageTrendRow{Model: p.Model, Bucket: p.Bucket, Requests: p.Requests, Tokens: p.Tokens, Credits: p.Credits, CostUSD: p.CostUSD})
	}
	modelSeries := pivotUsageTrendRows(rows)
	sort.SliceStable(modelSeries, func(i, j int) bool {
		if modelSeries[i].Model == usageTrendOthersKey {
			return false
		}
		return modelSeries[j].Model == usageTrendOthersKey || modelSeries[i].TotalRequests > modelSeries[j].TotalRequests
	})
	probeScope := "excluded"
	if query.IncludeProbes {
		probeScope = "included"
	}
	payload := map[string]any{
		"summary": summary, "pies": pies, "trends": board.Trends,
		"model_series": modelSeries, "bucket_minutes": query.BucketMinutes, "provider_rows": board.ProviderRows,
		"source": "request_metrics", "days": tr.Days, "probe_scope": probeScope,
		"window_start": query.StartTime.UTC(), "window_end": query.EndTime.UTC(),
		"tenant_id": query.TenantID, "provider_id": query.ProviderID,
		"updated_at": board.UpdatedAt, "pending_since": board.PendingSince,
		"degraded": !board.Ready,
	}
	if tr.Custom {
		location := settings.SystemTimezoneLocation()
		payload["range_start"] = tr.Start.In(location).Format(time.DateOnly)
		payload["range_end"] = tr.End.Add(-time.Nanosecond).In(location).Format(time.DateOnly)
		payload["includes_today"] = tr.includesTodaySystemTz()
	}
	if !board.Ready {
		payload["degraded_reason"] = "request_metrics_backfill"
		payload["degraded_pies"] = map[string]any{
			"dimensions": []string{"clients", "client_ips", "identity_hashes", "models", "errors", "tenants", "providers"},
			"reason":     "request_metrics_backfill",
		}
		payload["degraded_trends"] = true
		payload["trends_missing_view"] = "request_metrics"
	}
	return payload, nil
}

func (h *Handler) handleDashboardBoardErrorDrillV2(w http.ResponseWriter, r *http.Request) {
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
	kind, dimension := strings.TrimSpace(params.Get("error_kind")), params.Get("dimension")
	if kind == "" {
		writeError(w, http.StatusBadRequest, "error_kind required")
		return
	}
	if dimension == "" {
		dimension = "model"
	}
	switch dimension {
	case "model", "provider", "client":
	default:
		writeError(w, http.StatusBadRequest, "unsupported board drill dimension")
		return
	}
	query, _, err := projectedBoardQuery(r, tenant, owner)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from, to := params.Get("window_start"), params.Get("window_end"); from != "" || to != "" {
		start, end, err := parseExactUsageWindow(from, to)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid board drill window")
			return
		}
		query.StartTime, query.EndTime = start, end
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	items, ready, err := metrics.ReadBoardErrorDrill(ctx, h.db, query, kind, dimension)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics drill failed", err)
		return
	}
	if !ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "request_metrics", "error_kind": kind, "dimension": dimension, "items": items,
	})
}
