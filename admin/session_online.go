// Package admin — session_online.go
//
// V3.2 (2026-08-13) BE-B2: 在线会话浏览 API。
//
// Routes (registered in RegisterRoutes):
//
//	GET /api/admin/sessions/online              在线会话列表（Redis SCAN + session_last_requests 丰富）
//	GET /api/admin/sessions/{id}/timeline       会话多轮次时间线（主请求 + 扩展请求子树）
//
// 数据源：
//   - 在线状态：Redis session:{id} Hash（domains/session Manager）
//   - 最后请求：session_last_requests（migrations/timeout-optimization/003）
//   - 多轮次：request_logs_with_current_month（hot + promoted 月度分区
//     UNION ALL 视图，migration 340/448/459/491/510/532）按 gw_session_id
//     归属 + parent_request_id 主从关联；v4 T7（2026-08-18）起仅读 hot 的
//     截断行为已修复，并标注会话级唯一成功（is_final_success）。
//
// 约束：Redis 用 SCAN 游标（禁 KEYS）；租户隔离（ADR-V3-005）。
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// OnlineSession 是在线会话列表的一项。
type OnlineSession struct {
	SessionID         string         `json:"session_id"`
	Title             string         `json:"title,omitempty"`
	LastRequestStatus string         `json:"last_request_status,omitempty"`
	LastModel         string         `json:"last_model,omitempty"`
	LastProviderID    *int           `json:"last_provider_id,omitempty"`
	LastLatencyMs     *int           `json:"last_latency_ms,omitempty"`
	LastActiveAt      string         `json:"last_active_at,omitempty"`
	DeviceCount       int            `json:"device_count,omitempty"`
	Freshness         *FreshnessInfo `json:"freshness,omitempty"` // V3.2 freshness 信息
}

// handleSessionsOnline 返回在线会话列表。
// GET /api/admin/sessions/online?limit=50&cursor=xxx
//
// V3.2 改动（LP6）：
//   - tenant 隔离：从认证上下文取 tenant_id，过滤 session_last_requests
//   - cursor 分页：base64(RFC3339Nano)，时间倒序
//   - freshness：data_source + freshness_ms + stale
func (h *Handler) handleSessionsOnline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 1. Tenant 隔离（契约：从认证上下文取，忽略 query 覆盖）
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tenantID := auth.TenantID
	if tenantID == "" {
		tenantID = "default"
	}

	// 2. 解析分页参数
	rawCursor := r.URL.Query().Get("cursor")
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	params := NormalizePaginationParams(PaginationParams{Cursor: rawCursor, Limit: limit})

	// 3. 解析 cursor
	pageCursor, err := parseOnlineSessionCursor(params.Cursor)
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "session.pagination_invalid_cursor", "invalid cursor")
		return
	}

	// 4. 查询数据库（tenant 过滤 + cursor 分页 + freshness）
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查询 session_last_requests，按 updated_at 倒序（最新的在前）
	// cursor 分页：updated_at < cursorTime（首次请求 cursorTime 为 zero，不限制）
	query := `
		SELECT slr.session_id, slr.last_request_status, COALESCE(slr.last_model,''),
		       slr.last_provider_id, slr.last_latency_ms, slr.updated_at, rl.tenant_id
		FROM session_last_requests slr
		JOIN request_logs rl ON rl.id = slr.last_request_id
		WHERE 1 = 1`
	args := []interface{}{}
	if !IsSuperAdminOrLegacy(r) {
		query += ` AND rl.tenant_id = $1`
		args = append(args, tenantID)
	}
	if !pageCursor.UpdatedAt.IsZero() {
		if pageCursor.SessionID == "" {
			query += fmt.Sprintf(` AND slr.updated_at < $%d`, len(args)+1)
			args = append(args, pageCursor.UpdatedAt)
		} else {
			query += fmt.Sprintf(` AND (slr.updated_at, slr.session_id) < ($%d, $%d)`, len(args)+1, len(args)+2)
			args = append(args, pageCursor.UpdatedAt, pageCursor.SessionID)
		}
	}
	query += ` ORDER BY slr.updated_at DESC, slr.session_id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, params.Limit+1)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	now := time.Now()
	out := make([]OnlineSession, 0, params.Limit+1)
	pageKeys := make([]onlineSessionCursor, 0, params.Limit+1)

	for rows.Next() {
		var sid, status, model string
		var providerID, latency *int
		var updatedAt time.Time
		var dbTenantID string

		if err := rows.Scan(&sid, &status, &model, &providerID, &latency, &updatedAt, &dbTenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "read online session failed")
			return
		}

		// 再次确认 tenant（防御性编程）
		if !IsSuperAdminOrLegacy(r) && dbTenantID != tenantID {
			continue
		}

		// 计算 freshness（数据源暂定 hot，后续可从表字段读取）
		freshness := CalculateFreshness(DataSourceHot, updatedAt, now)

		os := OnlineSession{
			SessionID:         sid,
			LastRequestStatus: status,
			LastModel:         model,
			LastProviderID:    providerID,
			LastLatencyMs:     latency,
			Freshness:         &freshness,
		}
		if !updatedAt.IsZero() {
			os.LastActiveAt = updatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, os)
		pageKeys = append(pageKeys, onlineSessionCursor{UpdatedAt: updatedAt, SessionID: sid})
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "rows iteration failed: "+err.Error())
		return
	}

	hasMore := len(out) > params.Limit
	lastKey := onlineSessionCursor{}
	if hasMore {
		out = out[:params.Limit]
		lastKey = pageKeys[params.Limit-1]
	}

	// 5. 构建分页响应
	pagination, err := buildOnlineSessionPaginationResponse(hasMore, lastKey.UpdatedAt, lastKey.SessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pagination cursor unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":    out,
		"count":       len(out),
		"next_cursor": pagination.NextCursor,
		"has_more":    pagination.HasMore,
	})
}

// SessionTurn 是会话时间线的一轮（主请求 + 扩展请求子树）。
type SessionTurn struct {
	RequestID   string         `json:"request_id"`
	RequestType string         `json:"request_type"`
	Status      string         `json:"status"`
	Model       string         `json:"model,omitempty"`
	LatencyMs   *int           `json:"latency_ms,omitempty"`
	StartedAt   string         `json:"started_at,omitempty"`
	Children    []*SessionTurn `json:"children,omitempty"`

	// v4 T7 (2026-08-18, migration 532) 会话级「唯一成功」标注：
	//   IsFinalSuccess — 该行是本会话唯一最终成功（is_final_success=TRUE）
	//   Outcome        — final_success / superseded_success / success /
	//                    failure / rate_limited / in_progress / other
	//   OutcomeReason  — 仅在被取代/失败时给出（superseded_by_final_success
	//                    或 error_kind / failure_stage 摘要）
	//   ErrorKind / FailureStage — 原样透传（客户端断开、穷尽失败等细节）
	IsFinalSuccess bool   `json:"is_final_success,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	OutcomeReason  string `json:"outcome_reason,omitempty"`
	ErrorKind      string `json:"error_kind,omitempty"`
	FailureStage   string `json:"failure_stage,omitempty"`
}

// 会话身份语义（v4 T7-2 / UT-FS-04，消除 sessions.id 与 session_id 歧义）：
//
//	gw_session_id（文本）  request_logs*.gw_session_id，时间线的主键语义；
//	                       dispatch.QueuedRequest.SessionID 的文档曾把它写成
//	                       public.sessions.id（数值），是歧义来源。
//	sessions.id（数值）    sessions 表代理键（BIGSERIAL，月分区）。
//
// 本端点契约：
//   - GET /api/admin/sessions/{id}/timeline             {id} 按文本 gw_session_id 解释（兼容旧行为）
//   - GET .../timeline?session_pk=<sessions.id 数值>    显式按数值 sessions.id 解析成文本 session_id
//   - 兜底：{id} 为纯数字且按文本查不到行时，再按 sessions.id 解析一次
//
// 响应回显 session_id（生效的文本 ID）、session_pk（已知时）、
// session_id_source（path_gw_session_id / session_pk_resolved /
// numeric_fallback_resolved），三个视图（timeline/详情/队列）以文本
// gw_session_id 为统一标识。
const (
	sessionIDSourcePath            = "path_gw_session_id"
	sessionIDSourcePKResolved      = "session_pk_resolved"
	sessionIDSourceNumericFallback = "numeric_fallback_resolved"
)

// sessionTimelineDB is the minimal query surface querySessionTimeline and
// resolveSessionIDByPK need; *pgxpool.Pool and pgxmock pools both satisfy it
// （admin 包测试惯例，见 session_turns_tree.go）。
type sessionTimelineDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// resolveSessionIDByPK 把数值 sessions.id 解析为文本 session_id。
// sessions 按月分区（UNIQUE (session_id, partition_date)），同 id 只可能
// 出现在一个分区，ORDER BY partition_date DESC 仅作确定性兜底。
// tenantID 非空时附加租户过滤（super admin 由调用方置空跳过）。
// 未命中返回 ""（pgx.ErrNoRows 归一为空，调用方按 404 处理）。
func resolveSessionIDByPK(ctx context.Context, db sessionTimelineDB, tenantID string, pk int64) (string, error) {
	query := `SELECT session_id FROM sessions WHERE id = $1`
	args := []any{pk}
	if tenantID != "" {
		query += ` AND tenant_id = $2`
		args = append(args, tenantID)
	}
	query += ` ORDER BY partition_date DESC LIMIT 1`
	var sessionID string
	err := db.QueryRow(ctx, query, args...).Scan(&sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return sessionID, nil
}

// deriveTurnOutcome 从行数据推导 v4 T7 outcome 标注。
// sessionHasFinalSuccess 为本会话是否存在 is_final_success=TRUE 的行
// （promoted 分区 + hot 联合结果，由查询本身覆盖）。
func deriveTurnOutcome(status string, isFinalSuccess, sessionHasFinalSuccess bool) (outcome, reason string) {
	switch {
	case isFinalSuccess:
		return "final_success", ""
	case status == "success":
		if sessionHasFinalSuccess {
			// 同会话后续/先前成功已持有唯一标记：本行是被取代的成功
			//（客户端重发场景，migration 054）。历史行不回改，仅读侧标注。
			return "superseded_success", "superseded_by_final_success"
		}
		// 迁移前的历史成功行（无人持有标记）：保持 success，交由
		// sql/scripts/report_duplicate_session_success.sql 报告回填。
		return "success", ""
	case status == "failure":
		return "failure", ""
	case status == "rate_limited":
		return "rate_limited", ""
	case status == "in_progress":
		return "in_progress", ""
	default:
		// disconnect 等自定义终态：原样透传，other 兜底。
		if status == "" {
			return "other", ""
		}
		return status, ""
	}
}

// handleSessionTimeline 返回会话的多轮次时间线（主请求 + 扩展请求子树）。
// GET /api/admin/sessions/{id}/timeline[?session_pk=<sessions.id>]
//
// v4 T7 改动：
//  1. 读路径从 request_logs_hot 扩到 request_logs_with_current_month
//     （= request_logs_hot UNION ALL request_logs 分区母表，migration
//     340/448/459/491/510/532 维护），修复 >7d 会话被截断的问题
//     （promote_request_logs_hot_to_partition 7 天后把行搬离 hot）。
//  2. 统一 session 标识语义（见 sessionIDSource* 常量注释）。
//  3. 标注唯一成功行（is_final_success）与被取代原因。
func (h *Handler) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pathValue := r.PathValue("id")
	if pathValue == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 身份解析：显式 session_pk 优先；否则 {id} 按文本 gw_session_id。
	// super admin 不附加租户过滤（tenantScope 为空）。
	tenantScope := ""
	if !IsSuperAdminOrLegacy(r) {
		if auth.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "tenant_id required")
			return
		}
		tenantScope = auth.TenantID
	}
	sessionID := pathValue
	sessionIDSource := sessionIDSourcePath
	var sessionPK *int64
	if pkStr := r.URL.Query().Get("session_pk"); pkStr != "" {
		pk, err := strconv.ParseInt(pkStr, 10, 64)
		if err != nil || pk <= 0 {
			writeError(w, http.StatusBadRequest, "session_pk must be a positive numeric sessions.id")
			return
		}
		resolved, err := resolveSessionIDByPK(ctx, h.db, tenantScope, pk)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "resolve session_pk failed: "+err.Error())
			return
		}
		if resolved == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found", "session_pk": pk})
			return
		}
		sessionID = resolved
		sessionIDSource = sessionIDSourcePKResolved
		sessionPK = &pk
	}

	turns, hasMore, err := querySessionTimeline(ctx, h.db, sessionID, tenantScope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	// 兜底：{id} 为纯数字且按文本查不到任何行 → 尝试按数值 sessions.id
	// 解析（兼容历史上把 sessions.id 当字符串传进来的调用方）。
	if len(turns) == 0 && sessionIDSource == sessionIDSourcePath && isAllDigits(sessionID) {
		if pk, perr := strconv.ParseInt(sessionID, 10, 64); perr == nil {
			resolved, rerr := resolveSessionIDByPK(ctx, h.db, tenantScope, pk)
			if rerr == nil && resolved != "" && resolved != sessionID {
				sessionID = resolved
				sessionIDSource = sessionIDSourceNumericFallback
				sessionPK = &pk
				turns, hasMore, err = querySessionTimeline(ctx, h.db, sessionID, tenantScope)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
					return
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":        sessionID,
		"session_pk":        sessionPK,
		"session_id_source": sessionIDSource,
		"turns":             turns,
		"count":             len(turns),
		"has_more":          hasMore,
		"truncated":         hasMore,
	})
}

// querySessionTimeline 读取一个会话的全部请求行（hot + promoted 分区，
// 经 request_logs_with_current_month 视图），组装主/子树并做 T7 标注。
// tenantID 非空时附加租户过滤（super admin 传空）。
func querySessionTimeline(ctx context.Context, db sessionTimelineDB, sessionID, tenantID string) ([]*SessionTurn, bool, error) {
	// 查该会话的所有请求（主请求 + 扩展请求），按时间升序。
	// 主请求：gw_session_id = sessionID 且 parent_request_id IS NULL
	// 扩展请求：parent_request_id 指向主请求（request_type 区分类型）
	//
	// v4 T7: FROM request_logs_with_current_month（hot UNION ALL 分区母表）
	// 而非 request_logs_hot —— >7d 的行已被 promote 搬进月度分区，仅读 hot
	// 会截断历史会话（UT-FS-03）。is_final_success 由 migration 532 追加到视图。
	query := `
		SELECT request_id, COALESCE(request_type,'main'), COALESCE(request_status,''),
		       COALESCE(outbound_model, client_model, ''), latency_ms, ts,
		       COALESCE(parent_request_id, ''),
		       COALESCE(is_final_success, FALSE),
		       COALESCE(error_kind, ''), COALESCE(failure_stage, '')
		FROM request_logs_with_current_month
		WHERE gw_session_id = $1`
	args := []any{sessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	const timelineLimit = 200
	query += " ORDER BY ts ASC LIMIT 201"
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	// 第一遍：读所有行，记录 parent 关系
	type row struct {
		turn     *SessionTurn
		parentID string
	}
	mains := make([]*SessionTurn, 0, 32)
	byID := make(map[string]*SessionTurn)
	var children []*row
	rowCount := 0
	hasMore := false
	sessionHasFinalSuccess := false

	for rows.Next() {
		rowCount++
		if rowCount > timelineLimit {
			hasMore = true
			break
		}
		t := &SessionTurn{}
		var latency *int
		var ts time.Time
		var parentID string
		if err := rows.Scan(&t.RequestID, &t.RequestType, &t.Status,
			&t.Model, &latency, &ts, &parentID,
			&t.IsFinalSuccess, &t.ErrorKind, &t.FailureStage); err != nil {
			return nil, false, fmt.Errorf("read session timeline failed")
		}
		t.LatencyMs = latency
		t.StartedAt = ts.UTC().Format(time.RFC3339)
		if t.IsFinalSuccess {
			sessionHasFinalSuccess = true
		}
		byID[t.RequestID] = t
		if parentID == "" {
			mains = append(mains, t)
		} else {
			children = append(children, &row{turn: t, parentID: parentID})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read session timeline rows failed: %w (session_id=%s)", err, sessionID)
	}

	// 第二遍：把扩展请求挂到父请求（循环保护：父不存在则挂为顶层）
	for _, c := range children {
		if parent, ok := byID[c.parentID]; ok && parent.RequestID != c.turn.RequestID {
			parent.Children = append(parent.Children, c.turn)
		} else {
			// 父请求不在结果集（可能跨会话或已过期）：作为顶层展示，保留 parent 标记
			mains = append(mains, c.turn)
		}
	}

	// 第三遍：T7 outcome 标注（被取代原因等）。sessionHasFinalSuccess 需在
	// 全部行读完后才可知，故独立于挂树阶段。
	for _, t := range byID {
		outcome, reason := deriveTurnOutcome(t.Status, t.IsFinalSuccess, sessionHasFinalSuccess)
		t.Outcome = outcome
		t.OutcomeReason = reason
	}

	return mains, hasMore, nil
}

// isAllDigits 报告 s 是否全为 ASCII 数字（sessions.id 数值形态探测）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// countColon 返回字符串中冒号数量（用于区分 session:{id} 主键与子键）。
func countColon(s string) int {
	n := 0
	for _, c := range s {
		if c == ':' {
			n++
		}
	}
	return n
}
