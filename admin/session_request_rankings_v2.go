package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type sessionRankingScope struct {
	tenant, role, bypass string
	start, end           time.Time
	// owner/ownerIsUser 仅 user 档非零（2026-11-27 非管理员菜单重构轮）：
	// 「我的会话」整合页五个面板端点旧 default 分支 403 ⇒ 放 user 档；
	// owner 恒注入本人 username（与 dashboardapi/session_projected.go 的
	// JWT owner 分支同语义），ownerIsUser 供 rankings 维聚合按 owner 收窄。
	owner       string
	ownerIsUser bool
}

func prepareSessionRankingScope(w http.ResponseWriter, r *http.Request) (sessionRankingScope, bool) {
	var scope sessionRankingScope
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return scope, false
	}
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return scope, false
	}
	q := r.URL.Query()
	tenant := strings.TrimSpace(q.Get("tenant_id"))
	role, bypass := "tenant_admin", "false"
	var owner string
	ownerIsUser := false
	switch auth.Role {
	case "admin_key", "super_admin":
		role, bypass = "super_admin", "true"
	case "tenant_admin":
		if auth.TenantID == "" || (tenant != "" && tenant != auth.TenantID) {
			writeError(w, http.StatusForbidden, "tenant scope required")
			return scope, false
		}
		tenant = auth.TenantID
	case "user":
		// ★ 2026-11-27 非管理员菜单重构轮：放 user 档，三重收窄
		//   （与 dashboard session-overview 的 user 语义对齐）：
		//   ① 必须有租户绑定（无租户 ⇒ 无法收窄 ⇒ fail-closed 拒绝）；
		//   ② tenant 恒取 auth.TenantID，URL ?tenant_id 只许同值；
		//   ③ RLS 不 bypass（role=tenant_admin、bypass=false），
		//      owner 恒注入本人 username ⇒ people 排行只看自己
		//      （rank_peers=false），rankings 维聚合 handler 侧按 owner 过滤。
		if auth.TenantID == "" {
			writeError(w, http.StatusForbidden, "user must have a tenant binding to access session analytics")
			return scope, false
		}
		if tenant != "" && tenant != auth.TenantID {
			writeError(w, http.StatusForbidden, "tenant scope required")
			return scope, false
		}
		tenant = auth.TenantID
		owner = auth.Username
		ownerIsUser = true
	default:
		writeError(w, http.StatusForbidden, "admin role required")
		return scope, false
	}
	start, end, err := settings.SystemDayRangeUtc(q.Get("start_date"), q.Get("end_date"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return scope, false
	}
	if end.After(start.In(settings.SystemTimezoneLocation()).AddDate(0, 0, 366)) {
		writeError(w, http.StatusBadRequest, "date range cannot exceed 366 days")
		return scope, false
	}
	return sessionRankingScope{tenant: tenant, role: role, bypass: bypass, start: start, end: end, owner: owner, ownerIsUser: ownerIsUser}, true
}

func beginSessionRankingRead(ctx context.Context, db *pgxpool.Pool, scope sessionRankingScope) (pgx.Tx, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('app.current_tenant',$1,true), set_config('app.current_role',$2,true),
		set_config('app.bypass_rls',$3,true)`, scope.tenant, scope.role, scope.bypass)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (h *Handler) handleSessionRequestRankingV2(w http.ResponseWriter, r *http.Request) {
	scope, ok := prepareSessionRankingScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	dimension := map[string]string{
		"model-ranking": "models", "provider-ranking": "providers", "agent-ranking": "agents", "task-ranking": "tasks",
	}[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]
	sortBy, order := q.Get("sort_by"), q.Get("order")
	if !metrics.ValidSessionRankingSort(dimension, sortBy) || (order != "" && order != "asc" && order != "desc") {
		writeError(w, http.StatusBadRequest, "invalid ranking sort")
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
		writeInternalErr(w, "session request ranking unavailable", err)
		return
	}
	defer tx.Rollback(ctx)
	result, err := metrics.ReadSessionRequestRanking(ctx, tx, metrics.SessionRequestRankingQuery{
		Start: scope.start, End: scope.end, Tenant: scope.tenant, SortBy: sortBy, Order: order, Dimension: dimension,
	})
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "session request ranking unavailable", err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is in progress")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "session request ranking unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		dimension: result.Payload, "source": "request_metrics", "timezone": settings.SystemTimezoneName(),
		"period_start": scope.start, "period_end": scope.end, "updated_at": result.UpdatedAt, "pending_since": result.PendingSince,
	})
}
