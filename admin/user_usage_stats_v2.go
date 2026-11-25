package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

type accountProjectionMeta struct {
	Source       string     `json:"source"`
	WindowStart  time.Time  `json:"window_start"`
	WindowEnd    time.Time  `json:"window_end"`
	UpdatedAt    *time.Time `json:"updated_at"`
	PendingSince *time.Time `json:"pending_since"`
	Degraded     bool       `json:"degraded"`
}

func accountUsageQueryV2(w http.ResponseWriter, r *http.Request) (metrics.RequestStatsQuery, bool) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return metrics.RequestStatsQuery{}, false
	}
	if !requireUserManager(w, r) {
		return metrics.RequestStatsQuery{}, false
	}
	tenant, _, allowed := requestStatsV2Scope(r)
	if !allowed {
		writeError(w, http.StatusForbidden, "tenant scope unavailable")
		return metrics.RequestStatsQuery{}, false
	}
	query, err := parseRequestStatsV2WithDefaultDays(r, tenant, 30)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return query, false
	}
	return query, true
}

func accountUsageSourceV2(result metrics.ProjectedAccountActivity, query metrics.RequestStatsQuery) accountProjectionMeta {
	return accountProjectionMeta{
		Source: "request_metrics", WindowStart: query.StartTime, WindowEnd: query.EndTime,
		UpdatedAt: result.UpdatedAt, PendingSince: result.PendingSince, Degraded: !result.Ready,
	}
}

func (h *Handler) handleUserUsageSummaryV2(w http.ResponseWriter, r *http.Request) {
	query, ok := accountUsageQueryV2(w, r)
	if !ok {
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadProjectedAccounts(ctx, h.db, query.MetricsFilter)
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		accountProjectionMeta
		Days  int             `json:"days"`
		Items json.RawMessage `json:"items"`
	}{accountUsageSourceV2(result, query), queryInt(r, "days", 30), result.Payload})
}

func (h *Handler) handleUserStatsV2(w http.ResponseWriter, r *http.Request) {
	query, ok := accountUsageQueryV2(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var username, tenant string
	var keyCount int
	err = tx.QueryRow(ctx, `SELECT u.username, u.tenant_id,
		(SELECT count(*) FROM public.api_keys k WHERE k.tenant_id=u.tenant_id AND k.owner_user=u.username)
		FROM public.users u WHERE u.id=$1 AND ($2='' OR u.tenant_id=$2)`, id, query.TenantID).Scan(&username, &tenant, &keyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	query.TenantID = tenant
	result, err := metrics.ReadProjectedAccountActivity(ctx, tx, query.MetricsFilter, username, query.Timezone)
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	fields := map[string]json.RawMessage{
		"daily": json.RawMessage(`[]`), "top_models": json.RawMessage(`[]`), "top_apps": json.RawMessage(`[]`),
		"top_keys": json.RawMessage(`[]`), "recent_requests": json.RawMessage(`[]`), "kpi": json.RawMessage(`null`),
	}
	if err := json.Unmarshal(result.Payload, &fields); err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	meta := accountUsageSourceV2(result, query)
	for key, value := range map[string]any{
		"user_id": id, "username": username, "tenant_id": tenant, "days": queryInt(r, "days", 30), "key_count": keyCount,
		"source": meta.Source, "window_start": meta.WindowStart, "window_end": meta.WindowEnd,
		"updated_at": meta.UpdatedAt, "pending_since": meta.PendingSince, "degraded": meta.Degraded,
	} {
		fields[key], err = json.Marshal(value)
		if err != nil {
			writeAccountUsageErrorV2(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, fields)
}

func writeAccountUsageErrorV2(w http.ResponseWriter, err error) {
	if IsMissingRelationError(err) {
		writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
		return
	}
	writeInternalErr(w, "request metrics account statistics failed", err)
}
