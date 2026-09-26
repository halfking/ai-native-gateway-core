// Package admin - session_list_v2.go
//
// 会话列表 v2 — 把 sessionforensics.Exporter.ListRecentSessions 用 admin 端点暴露。
//
//   GET /api/admin/sessions/list?tenant=...&limit=...
//     返回最近活跃 session 列表（用 sessionforensics.ListRecentSessions 实现）
//
// 仅 super 用户可用。

package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// SessionListV2API 用 sessionforensics 实现的会话列表端点。
//
// production constructor (NewSessionListV2API) 接受 *pgxpool.Pool；测试
// constructor (newSessionListV2APIWithDB) 接受 sessionforensics.Store，
// 由 admin 层把任意 sessionListV2DB 适配为 Store —— 这是 5 类 ID 契约测试
// 跨包注入 pgxmock 的关键通路。
type SessionListV2API struct {
	pool  *pgxpool.Pool
	store sessionforensics.Store
}

// NewSessionListV2API 构造函数。
func NewSessionListV2API(pool *pgxpool.Pool) *SessionListV2API {
	return &SessionListV2API{pool: pool}
}

// sessionListV2DB 是 SessionListV2API 实际需要的数据库方法子集（仅 Query）。
// *pgxpool.Pool 与 pgxmock.PgxPoolIface 都满足此接口。
type sessionListV2DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// sessionListV2Adapter 把任意 sessionListV2DB 适配为 sessionforensics.Store。
// 单测路径专用 —— 生产路径走 *pgxpool.Pool + sessionforensics.NewExporter。
type sessionListV2Adapter struct {
	pool sessionListV2DB
}

func (a *sessionListV2Adapter) QueryRow(ctx context.Context, sql string, args ...any) sessionforensics.Row {
	// sessionforensics.Store.QueryRow 返回 sessionforensics.Row，不是 pgx.Row；
	// 这里二次适配：把 pgxmock 暴露的 pgx.Row → sessionforensics.Row。
	if r, ok := a.pool.(interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}); ok {
		return &pgxRowToSFAdapter{row: r.QueryRow(ctx, sql, args...)}
	}
	// 12h 审计 F-5：静默返回 nil 会让调用方在 Scan 时空指针 panic 且无
	// 任何线索；显式指出缺失的方法（sessionListV2DB 契约只强制 Query，
	// QueryRow 靠本类型断言补齐）。
	panic("sessionListV2Adapter: underlying sessionListV2DB lacks QueryRow — cannot adapt to sessionforensics.Row")
}

func (a *sessionListV2Adapter) Query(ctx context.Context, sql string, args ...any) (sessionforensics.RowIterator, error) {
	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowsToSFAdapter{rows: rows}, nil
}

// pgxRowToSFAdapter wraps pgx.Row into sessionforensics.Row。
type pgxRowToSFAdapter struct{ row pgx.Row }

func (r *pgxRowToSFAdapter) Scan(dest ...any) error { return r.row.Scan(dest...) }

// pgxRowsToSFAdapter wraps pgx.Rows into sessionforensics.RowIterator。
type pgxRowsToSFAdapter struct{ rows pgx.Rows }

func (r *pgxRowsToSFAdapter) Next() bool             { return r.rows.Next() }
func (r *pgxRowsToSFAdapter) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r *pgxRowsToSFAdapter) Err() error             { return r.rows.Err() }
func (r *pgxRowsToSFAdapter) Close() error           { r.rows.Close(); return nil }

// newSessionListV2APIWithDB 允许测试注入 pgxmock 池。它把传入的
// sessionListV2DB 适配为 sessionforensics.PgxStore，再注入 Exporter。
// 这样生产路径（NewSessionListV2API(*pgxpool.Pool)）与测试路径共用
// sessionforensics.ListRecentSessions 的 SQL 与权限策略。
func newSessionListV2APIWithDB(pool sessionListV2DB) *SessionListV2API {
	return &SessionListV2API{
		store: &sessionListV2Adapter{pool: pool},
	}
}

// NewSessionListV2APIWithDB 是 newSessionListV2APIWithDB 的导出别名，
// 供跨包单测（tests/session_identity_contract/...）注入 pgxmock。
// 与 NewSessionListV2API 的 *pgxpool.Pool 接受范围等价，仅多了一步
// 接口收缩，便于 pgxmock / 自实现 Store 接入。
func NewSessionListV2APIWithDB(pool sessionListV2DB) *SessionListV2API {
	return newSessionListV2APIWithDB(pool)
}

func (api *SessionListV2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.pool == nil && api.store == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session list v2 API requires database")
		return
	}
	if r.URL.Path != "/api/admin/sessions/list" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	var exp *sessionforensics.Exporter
	if api.store != nil {
		// 测试路径（pgxmock）：store 已是预适配的 Store，直接复用。
		exp = sessionforensics.NewExporterWithStore(api.store)
	} else {
		exp = sessionforensics.NewExporter(api.pool)
	}
	audits, err := exp.ListRecentSessions(r.Context(), tenantID, limit)
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// contract_freeze §1.6：list_v2 端点的数据源是
	// sessionforensics.Exporter.ListRecentSessions，**实际拉到的是
	// request_logs.gw_session_id**（V1 文本客户端标识，不是
	// sessions.session_id）。因此响应 id_kind 严格标注 "gw_session_id"
	// —— 5 类 ID 互不替代（contract_freeze §1.6 互查表）。
	//
	// 注：端点路径命名是 /sessions/list 但内部数据流仍是 V1 request_logs
	// 聚合。把 list_v2 真正切到 sessions_v2 表（session_id 主键）属于另一
	// 条独立任务（5 类 ID 数据源迁移），不在本 P0 范围内。
	items := make([]map[string]any, 0, len(audits))
	for _, a := range audits {
		items = append(items, map[string]any{
			"id_kind":     "gw_session_id",
			"primary_key": a.SessionID,
			"audit":       a,
		})
	}

	writeExportJSON(w, http.StatusOK, map[string]any{
		"tenant":   tenantID,
		"limit":    limit,
		"sessions": items,
		"id_kind":  "gw_session_id",
	})
}
