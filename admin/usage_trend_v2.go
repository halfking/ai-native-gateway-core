package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

func (h *Handler) handleUsageTrendV2(w http.ResponseWriter, r *http.Request) {
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
	tr, err := boardTimeRangeFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := usageTrendFiltersFromRequest(r)
	modelsOnly := strings.HasSuffix(r.URL.Path, "/trend-models")
	query := metrics.RequestStatsQuery{
		MetricsFilter: metrics.MetricsFilter{TenantID: tenant, StartTime: tr.Start, EndTime: tr.End},
		Group:         "trend-bucket", BucketMinutes: tr.trendBucketMinutes(), Timezone: "UTC", Models: f.models,
		ClientModel: f.dims.clientModel, EndUser: f.dims.endUserID, KeyPrefix: f.dims.apiKeyPrefix,
		AppCode: f.dims.applicationCode, TaskType: f.dims.taskType, Owner: owner,
	}
	// 三级配额制维度（2026-11-27）：org/user 深链过滤直达 request_metrics 投影列。
	if f.orgID != "" {
		query.OrgID = &f.orgID
	}
	if f.userID != "" {
		query.UserID = &f.userID
	}
	if f.providerID > 0 {
		query.ProviderID = &f.providerID
	}
	if f.apiKeyID > 0 {
		query.APIKey = &f.apiKeyID
	}
	if modelsOnly {
		query.Group, query.Limit = "trend-model", 100
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if strings.HasSuffix(r.URL.Path, "/top-rankings") {
		h.writeUsageRankingsV2(ctx, w, query, queryInt(r, "users_limit", 10))
		return
	}
	result, err := metrics.ReadRequestStats(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics trend failed", err)
		return
	}
	models := make([]usageTrendModelEntry, 0, len(result.Items))
	rows := make([]usageTrendRow, 0, len(result.Items))
	for _, item := range result.Items {
		var value struct {
			Key      string  `json:"key"`
			Model    string  `json:"model"`
			Requests int64   `json:"total_requests"`
			Tokens   int64   `json:"total_tokens"`
			Credits  int64   `json:"total_credits_charged"`
			CostUSD  float64 `json:"total_cost_usd"`
		}
		if err := json.Unmarshal(item, &value); err != nil {
			writeInternalErr(w, "request metrics trend decoding failed", err)
			return
		}
		if modelsOnly {
			models = append(models, usageTrendModelEntry{value.Model, value.Requests, value.Tokens, value.Credits, value.CostUSD})
			continue
		}
		bucket, err := time.Parse(time.RFC3339, value.Key)
		if err != nil {
			writeInternalErr(w, "request metrics trend bucket failed", err)
			return
		}
		rows = append(rows, usageTrendRow{value.Model, bucket, value.Requests, value.Tokens, value.Credits, value.CostUSD})
	}
	reason := ""
	if !result.Ready {
		reason = "request_metrics_backfill"
	}
	start, end := tr.Start.UTC().Format(time.RFC3339), tr.End.UTC().Format(time.RFC3339)
	if modelsOnly {
		writeJSON(w, http.StatusOK, usageTrendModelsResponse{
			Start: start, End: end, Source: result.Source, Models: models, Degraded: !result.Ready, DegradedReason: reason,
		})
		return
	}
	series := pivotUsageTrendRows(foldUsageTrendRows(rows, f.top, len(f.models) != 0))
	sort.SliceStable(series, func(i, j int) bool {
		if series[i].Model == usageTrendOthersKey {
			return false
		}
		return series[j].Model == usageTrendOthersKey || series[i].TotalRequests > series[j].TotalRequests
	})
	writeJSON(w, http.StatusOK, usageTrendSeriesResponse{
		Start: start, End: end, Source: result.Source, BucketMinutes: query.BucketMinutes, Top: f.top,
		Series: series, Degraded: !result.Ready, DegradedReason: reason,
	})
}

// Read all rankings from the same committed projection snapshot.
func (h *Handler) writeUsageRankingsV2(ctx context.Context, w http.ResponseWriter, query metrics.RequestStatsQuery, usersLimit int) {
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeInternalErr(w, "request metrics rankings failed", err)
		return
	}
	defer tx.Rollback(ctx)

	type entry struct {
		Name         string  `json:"name"`
		RequestCount int64   `json:"request_count"`
		TotalTokens  int64   `json:"total_tokens"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	response := map[string]any{"source": "request_metrics", "degraded": false}
	if usersLimit < 1 {
		usersLimit = 10
	}
	usersLimit = min(usersLimit, 50)
	for _, group := range []string{"models", "clients", "users", "keys", "projects", "tasks"} {
		query.Group, query.Limit = "top-"+group, 5
		if group == "users" {
			query.Limit = usersLimit
		}
		result, err := metrics.ReadRequestStats(ctx, tx, query)
		if err != nil {
			if IsMissingRelationError(err) {
				writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
				return
			}
			writeInternalErr(w, "request metrics rankings failed", err)
			return
		}
		if !result.Ready {
			response["degraded"], response["degraded_reason"] = true, "request_metrics_backfill"
		}
		entries := make([]entry, 0, len(result.Items))
		for _, row := range result.Items {
			var item struct {
				Key      string  `json:"key"`
				Requests int64   `json:"total_requests"`
				Tokens   int64   `json:"total_tokens"`
				Cost     float64 `json:"total_cost_usd"`
			}
			if err := json.Unmarshal(row, &item); err != nil {
				writeInternalErr(w, "request metrics ranking decoding failed", err)
				return
			}
			entries = append(entries, entry{item.Key, item.Requests, item.Tokens, item.Cost})
		}
		if group == "keys" {
			group = "api_keys"
		}
		response["top_"+group] = entries
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "request metrics rankings commit failed", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
