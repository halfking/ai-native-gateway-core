package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

type analyticsDetailV2Payload struct {
	AnalyticsDetailPayload
	Source       string     `json:"source"`
	Ready        bool       `json:"ready"`
	UpdatedAt    *time.Time `json:"updated_at"`
	PendingSince *time.Time `json:"pending_since"`
	NonUSDRows   int64      `json:"cost_rows_outside_usd"`
}

func (h *Handler) handleAnalyticsDetailV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, allowed := requestStatsV2Scope(r)
	if !allowed {
		writeError(w, http.StatusForbidden, "request statistics require a resolved identity")
		return
	}
	dimension, identifier := r.PathValue("dimension"), strings.TrimSpace(r.PathValue("identifier"))
	if !isValidDimension(dimension) || identifier == "" {
		writeError(w, http.StatusBadRequest, "a supported dimension and an identifier are required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	tr, err := projectedBoardTimeRange(r)
	if err != nil || !tr.End.After(tr.Start) {
		writeError(w, http.StatusBadRequest, "invalid statistics window")
		return
	}
	query := metrics.BoardQuery{
		MetricsFilter: metrics.MetricsFilter{TenantID: tenant, StartTime: tr.Start, EndTime: tr.End, IncludeProbes: queryBool(r, "include_probes")},
		Owner:         owner, BucketMinutes: 60,
	}
	if tr.Days <= 1 {
		query.BucketMinutes = 15
	} else if tr.Days > 7 {
		query.BucketMinutes = 360
	}
	switch dimension {
	case "model":
		query.Model = identifier
	case "provider":
		if id, err := strconv.ParseInt(identifier, 10, 64); err == nil && id > 0 {
			query.ProviderID = &id
		} else {
			query.ProviderCode = identifier
		}
	case "client":
		query.Agent = identifier
	case "tenant":
		if tenant != "" && tenant != identifier {
			writeError(w, http.StatusForbidden, "tenant is outside the request scope")
			return
		}
		query.TenantID = identifier
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	board, err := metrics.ReadRequestBoard(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics analytics failed", err)
		return
	}
	payload, err := projectedAnalyticsPayload(board, tr, dimension, identifier)
	if err != nil {
		writeInternalErr(w, "request metrics analytics response failed", err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func projectedAnalyticsPayload(board metrics.RequestBoard, tr boardTimeRange, dimension, identifier string) (analyticsDetailV2Payload, error) {
	var summary struct {
		AnalyticsSummary
		Failures   int64   `json:"failed_count"`
		Credits    float64 `json:"total_credits_charged"`
		NonUSDRows int64   `json:"cost_rows_outside_usd"`
	}
	if err := json.Unmarshal(board.Summary, &summary); err != nil {
		return analyticsDetailV2Payload{}, err
	}
	summary.ErrorCount, summary.TotalCredits = summary.Failures, summary.Credits
	trends := make([]AnalyticsTrendPoint, 0, len(board.Trends))
	for _, p := range board.Trends {
		trends = append(trends, AnalyticsTrendPoint{Bucket: p.Bucket.Format(time.RFC3339), Requests: p.Requests, Success: p.Success,
			Errors: p.Requests - p.Success, Tokens: p.Tokens, CostUSD: p.CostUSD, Credits: float64(p.Credits), AvgLatencyMS: p.AvgLatencyMS})
	}
	cross := func(name string) []boardPieItem {
		items := board.Pies[name]
		out := make([]boardPieItem, 0, min(10, len(items)))
		for _, item := range items[:min(10, len(items))] {
			out = append(out, boardPieItem(item))
		}
		return out
	}
	payload := AnalyticsDetailPayload{
		Dimension: dimension, Identifier: identifier, DisplayName: identifier,
		TimeRange: AnalyticsTimeRange{Start: tr.Start, End: tr.End, Days: tr.Days},
		Summary:   summary.AnalyticsSummary, Trends: trends,
		CrossAnalysis: AnalyticsCrossAnalysis{ByModel: cross("models"), ByProvider: cross("providers"),
			ByClient: cross("clients"), ByTenant: cross("tenants"), ByStatus: cross("statuses"), ByError: cross("errors")},
		Degraded: !board.Ready,
	}
	if !board.Ready {
		payload.DegradedReason = "request_metrics_backfill_incomplete"
	}
	return analyticsDetailV2Payload{
		AnalyticsDetailPayload: payload, Source: "request_metrics", Ready: board.Ready,
		UpdatedAt: board.UpdatedAt, PendingSince: board.PendingSince, NonUSDRows: summary.NonUSDRows,
	}, nil
}
