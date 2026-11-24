// Package dashboardapi - session_active.go
// 活跃会话 API：查询当前活跃的会话列表
//
// 2026-07-13: 增加 session_summaries 表缺失时的优雅降级
//
// 2026-11-25 窗口对齐轮：本端点此前**硬编码**「最近 1 小时」，把前端选的
// 自定义日期区间静默丢弃 ⇒ 现在也消费 start_date/end_date（parseSessionWindow）。
//
// ★ 不变量①：滚动路径（没给 start_date/end_date）**逐字**保留旧的
//
//	「最近 1 小时」语义，且**不追加任何参数** ⇒ 现役调用方的查询计划与
//	结果零变化。
//
// ★ 参数编号链：窗口参数先占 $1/$2（calendar 模式），后面的
//
//	appendDashboardScope / buildOwnerWhere / LIMIT / OFFSET 必须接着 argIdx
//	往后排，不能从 1 重来（否则占位符与实参错位）。
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
)

// SessionActiveHandler 活跃会话 Handler
type SessionActiveHandler struct {
	db *pgxpool.Pool
}

// NewSessionActiveHandler 创建 Handler
func NewSessionActiveHandler(db *pgxpool.Pool) *SessionActiveHandler {
	return &SessionActiveHandler{db: db}
}

// ActiveSessionItem 活跃会话项
type ActiveSessionItem struct {
	SessionKey   string    `json:"session_key"`
	TenantID     string    `json:"tenant_id"`
	ClientID     string    `json:"client_id"`
	Model        string    `json:"model"`
	RequestCount int       `json:"request_count"`
	TotalCost    float64   `json:"total_cost"`
	HealthScore  *int      `json:"health_score,omitempty"`
	HealthGrade  string    `json:"health_grade"`
	LastActiveAt time.Time `json:"last_active_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionActiveResponse 活跃会话响应
type SessionActiveResponse struct {
	Sessions    []ActiveSessionItem `json:"sessions"`
	TotalActive int                 `json:"total_active"`
	Page        int                 `json:"page"`
	Size        int                 `json:"size"`
}

// HandleSessionActive 处理活跃会话请求
//
// GET /api/admin/dashboard/session-active
func (h *SessionActiveHandler) HandleSessionActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-active", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	// 2026-11-25 窗口对齐轮：显式日历窗优先；解析失败回 400（参数错不是
	// 服务端故障），与 errors.go / performance.go 同一写法。
	win, werr := parseSessionWindow(r, params)
	if werr != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, werr.Error(), "")
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	totalActive, sessions, err := h.queryActiveSessions(ctx, params, win, true)
	if err != nil && isMissingSessionDim(err) {
		totalActive, sessions, err = h.queryActiveSessions(ctx, params, win, false)
	}
	if err != nil {
		if dashboarddegrade.IsMissingRelationError(err) {
			h.writeDegraded(w, params, startTime, "session-active", err)
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query active sessions", err.Error())
		return
	}

	resp := SessionActiveResponse{
		Sessions:    sessions,
		TotalActive: totalActive,
		Page:        params.Page,
		Size:        params.Size,
	}

	metadata := &Metadata{
		Total:       totalActive,
		Page:        params.Page,
		Size:        params.Size,
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func isMissingSessionDim(err error) bool {
	return dashboarddegrade.IsMissingRelationError(err) &&
		dashboarddegrade.ExtractRelationName(err) == "session_dim"
}

// activeWindowClause 返回「活跃会话」的窗口谓词片段，并把边界参数追加进 args。
//
// ★ rolling 模式（没给 start_date/end_date）：**逐字**返回
//
//	"ss.last_request_at >= NOW() - INTERVAL '1 hour'" 且**不追加任何参数**
//	⇒ 现役结果零变化（不变量①）。
//
// ★ calendar 模式（给了 start_date/end_date）：参数化边界
//
//	"ss.last_request_at >= $N AND ss.last_request_at < $N+1"，
//	实参是 win.startUTC / win.endUTC（end 独占 ⇒ 末日整天被包含）。
//
//	占位符编号用 *argIdx（不是从 1 重来）⇒ 调用方后面的
//	appendDashboardScope / buildOwnerWhere / LIMIT / OFFSET 接着它往后排。
func activeWindowClause(win sessionWindow, args *[]interface{}, argIdx *int) string {
	if !win.calendar {
		return "ss.last_request_at >= NOW() - INTERVAL '1 hour'"
	}
	clause := fmt.Sprintf("ss.last_request_at >= $%d AND ss.last_request_at < $%d", *argIdx, *argIdx+1)
	*args = append(*args, win.startUTC, win.endUTC)
	*argIdx += 2
	return clause
}

func (h *SessionActiveHandler) queryActiveSessions(ctx context.Context, params QueryParams, win sessionWindow, withDim bool) (int, []ActiveSessionItem, error) {
	fromClause := "FROM session_summaries ss"
	if withDim {
		fromClause = `FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key`
	}

	where := []string{}
	args := []interface{}{}
	argIdx := 1

	// ★ 窗口片段**只追加一次**：本函数一次调用里 whereClause 只构造一遍
	//   （withDim 的两条腿是两次独立调用，各自构造自己的 where/args）⇒
	//   参数不会被追加两遍、占位符不会错位。
	where = append(where, activeWindowClause(win, &args, &argIdx))

	appendDashboardScope(&where, params, &args, &argIdx, "ss", false)
	if withDim {
		if clause := buildOwnerWhere(params.auth, &args, &argIdx, "sd"); clause != "" {
			where = append(where, clause)
		}
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	var totalActive int
	if err := h.db.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) %s %s", fromClause, whereClause), args...).Scan(&totalActive); err != nil {
		return 0, nil, err
	}

	clientExpr := "COALESCE(NULLIF(ss.client_models[1], ''), '')"
	if withDim {
		clientExpr = "COALESCE(NULLIF(TRIM(sd.client_id), ''), NULLIF(ss.client_models[1], ''), '')"
	}

	offset := (params.Page - 1) * params.Size
	listArgs := append([]interface{}{}, args...)
	listArgs = append(listArgs, params.Size, offset)
	query := fmt.Sprintf(`
		SELECT
			ss.session_key,
			COALESCE(ss.tenant_id, '') as tenant_id,
			%s as client_id,
			COALESCE(ss.models_used[1], '') as model,
			COALESCE(ss.request_count, 0) as request_count,
			COALESCE(ss.total_cost_usd, 0) as total_cost,
			ss.health_score,
			COALESCE(ss.health_grade, '') as health_grade,
			ss.last_request_at,
			ss.first_request_at
		%s
		%s
		ORDER BY ss.last_request_at DESC
		LIMIT $%d OFFSET $%d
	`, clientExpr, fromClause, whereClause, argIdx, argIdx+1)

	rows, err := h.db.Query(ctx, query, listArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	sessions := make([]ActiveSessionItem, 0)
	for rows.Next() {
		var item ActiveSessionItem
		if err := rows.Scan(
			&item.SessionKey, &item.TenantID, &item.ClientID, &item.Model,
			&item.RequestCount, &item.TotalCost, &item.HealthScore, &item.HealthGrade,
			&item.LastActiveAt, &item.CreatedAt,
		); err != nil {
			return 0, nil, err
		}
		sessions = append(sessions, item)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	return totalActive, sessions, nil
}

// writeDegraded 缺表降级 — 返回 0 值 + degraded:true，前端按空态渲染。
func (h *SessionActiveHandler) writeDegraded(w http.ResponseWriter, params QueryParams, startTime time.Time, op string, err error) {
	view := dashboarddegrade.ExtractRelationName(err)
	writeSuccessJSON(w, SessionActiveResponse{
		Sessions:    []ActiveSessionItem{},
		TotalActive: 0,
		Page:        params.Page,
		Size:        params.Size,
	}, &Metadata{
		Total:       0,
		Page:        params.Page,
		Size:        params.Size,
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
		Degraded:    true,
		MissingView: view,
		Hint:        "数据视图尚未初始化，请先执行数据聚合迁移",
	})
}
