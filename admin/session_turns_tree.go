// Package admin — session_turns_tree.go
//
// V3.3-OBS (2026-08-15) OBS-BE6: 会话轮次-子请求树 API（26 号 §5）。
//
// Route (registered in RegisterRoutes):
//
//	GET /api/admin/sessions/{id}/turns    会话轮次分页列表（主请求 + 扩展请求子树，仅元数据）
//
// 数据源：
//   - 主轮次：request_logs_with_current_month（hot + 归档分区 UNION，migration 510 重建）
//     中 gw_session_id 归属、parent_request_id 为空的主请求，turn_number 由
//     ROW_NUMBER() OVER (ORDER BY ts, request_id) 派生。
//   - 子请求：parent_request_id 关联的扩展请求（title/summary/sensitive_word 等）。
//   - request_type：优先 request_logs.request_type（510 迁移列）；缺失/为 'main'
//     时回退 origin_actor（X-Gw-Source-Actor 头在 handler 入口读取后由 telemetry
//     落库为 request_logs_hot.origin_actor，见 request_log_pipeline.go）。
//
// TODO(12号口径): session_turns 与 request_logs_hot 双源一致性（归档轮次覆盖）尚未
// 对齐 —— 当前以 request_logs_with_current_month 为单一事实源，session_turns 的
// turn_no 与本端点派生的 turn_number 不保证一致；按 12 号文档"归档覆盖需补齐"口径
// 标注，不阻塞本任务（双源合并待后续任务统一）。
//
// 硬约束：响应只含 id/类型/状态/延迟等元数据，绝不包含正文（body/prompt）。
package admin

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionTurnTreeItem 是一轮（主请求 + 内联子请求树）。
type SessionTurnTreeItem struct {
	TurnNumber    int                    `json:"turn_number"`
	RequestID     string                 `json:"request_id"`
	Status        string                 `json:"status"`
	Model         string                 `json:"model,omitempty"`
	LatencyMs     *int                   `json:"latency"` // 毫秒；NULL 未知
	ChildRequests []*SessionChildRequest `json:"child_requests"`
}

// SessionChildRequest 是挂在主请求下的扩展请求（仅元数据）。
type SessionChildRequest struct {
	RequestID   string `json:"request_id"`
	RequestType string `json:"request_type"` // title | summary | sensitive_word | compression | other
	Status      string `json:"status"`
	LatencyMs   *int   `json:"latency"` // 毫秒；NULL 未知
}

// sessionTurnsTreeDB 是 querySessionTurnsTree 依赖的最小查询接口
// （*pgxpool.Pool 与 pgxmock 均可实现，便于单测）。
type sessionTurnsTreeDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sessionTurnsTreeParams 是查询参数。
type sessionTurnsTreeParams struct {
	SessionID string
	TenantID  string // 非空时过滤 tenant_id（super_admin/legacy 传空）
	Limit     int
	Cursor    sessionTurnsTreeCursor // 首页为零值
}

// sessionTurnsTreeCursor 是 (turn_number, request_id) 复合分页游标。
type sessionTurnsTreeCursor struct {
	TurnNumber int64
	RequestID  string
}

// sessionTurnsTreeResult 是一页查询结果。
type sessionTurnsTreeResult struct {
	Turns     []*SessionTurnTreeItem
	HasMore   bool
	NextKey   sessionTurnsTreeCursor // HasMore 时有效
	NotFound  bool                   // 会话不存在（任何租户均无请求记录）
	Forbidden bool                   // 会话存在但属于其他租户（仅非 super_admin）
}

// errSessionTurnsTreeCursor 表示 cursor 非法或与当前会话不匹配。
var errSessionTurnsTreeCursor = errors.New("invalid turns cursor")

// handleSessionTurnsTree 返回会话的轮次-子请求树（分页）。
// GET /api/admin/sessions/{id}/turns?limit=20&cursor=xxx
//
// tenant 过滤与权限沿用现有 session admin 端点模式（同 handleSessionTimeline）：
//   - 认证上下文必需（401）；tenant_id 从认证主体取，忽略 query 覆盖
//   - super_admin / legacy admin_key 不加 tenant 过滤
//   - 会话不存在 → 404；会话属于其他租户 → 403（参考 session-audit 端点口径）
func (h *Handler) handleSessionTurnsTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tenantID := auth.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	filterTenant := ""
	if !IsSuperAdminOrLegacy(r) {
		filterTenant = tenantID
	}

	params := NormalizePaginationParams(PaginationParams{
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  parseTurnsTreeLimit(r),
	})
	cursor, err := parseSessionTurnsTreeCursor(params.Cursor, sessionID)
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "session.pagination_invalid_cursor", "invalid cursor")
		return
	}

	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := querySessionTurnsTree(ctx, h.db, sessionTurnsTreeParams{
		SessionID: sessionID,
		TenantID:  filterTenant,
		Limit:     params.Limit,
		Cursor:    cursor,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if result.NotFound {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if result.Forbidden {
		writeError(w, http.StatusForbidden, "session belongs to another tenant")
		return
	}

	nextCursor := ""
	if result.HasMore {
		nextCursor, err = encodeSessionTurnsTreeCursor(result.NextKey, sessionID)
		if err != nil {
			// 与 handleSessionsOnline 同口径：cursor 签名不可用视为服务降级
			writeError(w, http.StatusServiceUnavailable, "pagination cursor unavailable")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  sessionID,
		"turns":       result.Turns,
		"count":       len(result.Turns),
		"has_more":    result.HasMore,
		"next_cursor": nextCursor,
	})
}

func parseTurnsTreeLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

// querySessionTurnsTree 执行两段查询：主请求分页页 + 子请求批量关联。
// 查询顺序与 SQL 均为 pgxmock 可断言形态。
func querySessionTurnsTree(ctx context.Context, db sessionTurnsTreeDB, p sessionTurnsTreeParams) (*sessionTurnsTreeResult, error) {
	// 1) 主请求页（ROW_NUMBER 派生 turn_number，(turn_number, request_id) 游标）
	mainSQL := `
		SELECT t.turn_number, t.request_id, t.status, t.model, t.latency_ms
		FROM (
			SELECT ROW_NUMBER() OVER (ORDER BY ts ASC, request_id ASC) AS turn_number,
			       request_id,
			       COALESCE(request_status, '') AS status,
			       COALESCE(outbound_model, client_model, '') AS model,
			       latency_ms
			FROM request_logs_with_current_month
			WHERE gw_session_id = $1
			  AND (parent_request_id IS NULL OR parent_request_id = '')`
	args := []any{p.SessionID}
	if p.TenantID != "" {
		mainSQL += fmt.Sprintf("\n\t\t\t  AND tenant_id = $%d", len(args)+1)
		args = append(args, p.TenantID)
	}
	mainSQL += fmt.Sprintf(`
		) t
		WHERE (t.turn_number > $%d OR (t.turn_number = $%d AND t.request_id > $%d))
		ORDER BY t.turn_number ASC, t.request_id ASC
		LIMIT $%d`,
		len(args)+1, len(args)+2, len(args)+3, len(args)+4)
	args = append(args, p.Cursor.TurnNumber, p.Cursor.RequestID, p.Limit+1)

	rows, err := db.Query(ctx, mainSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	turns := make([]*SessionTurnTreeItem, 0, p.Limit)
	var lastKey sessionTurnsTreeCursor
	for rows.Next() {
		var tn int64
		t := &SessionTurnTreeItem{}
		if err := rows.Scan(&tn, &t.RequestID, &t.Status, &t.Model, &t.LatencyMs); err != nil {
			return nil, fmt.Errorf("read session turns failed: %w", err)
		}
		t.TurnNumber = int(tn)
		t.ChildRequests = []*SessionChildRequest{}
		turns = append(turns, t)
		lastKey = sessionTurnsTreeCursor{TurnNumber: tn, RequestID: t.RequestID}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 2) 空首页判空：区分 404（会话不存在）与 403（跨租户）
	if len(turns) == 0 && p.Cursor.TurnNumber == 0 && p.Cursor.RequestID == "" {
		exists, ownerTenant, err := sessionTurnsTreeExists(ctx, db, p.SessionID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return &sessionTurnsTreeResult{Turns: turns, NotFound: true}, nil
		}
		if p.TenantID != "" && ownerTenant != p.TenantID {
			return &sessionTurnsTreeResult{Turns: turns, Forbidden: true}, nil
		}
		return &sessionTurnsTreeResult{Turns: turns}, nil
	}

	hasMore := len(turns) > p.Limit
	if hasMore {
		turns = turns[:p.Limit]
		// lastKey 在截断前记录的是第 limit+1 条，需回退到最后保留的一条
		if kept := turns[len(turns)-1]; kept != nil {
			lastKey = sessionTurnsTreeCursor{TurnNumber: int64(kept.TurnNumber), RequestID: kept.RequestID}
		}
	} else {
		lastKey = sessionTurnsTreeCursor{}
	}

	// 3) 子请求批量关联（parent_request_id，仅元数据）
	if len(turns) > 0 {
		ids := make([]string, 0, len(turns))
		index := make(map[string]*SessionTurnTreeItem, len(turns))
		for _, t := range turns {
			ids = append(ids, t.RequestID)
			index[t.RequestID] = t
		}
		childSQL := `
			SELECT parent_request_id, request_id, COALESCE(request_status, ''),
			       latency_ms, COALESCE(request_type, 'main'), COALESCE(origin_actor, '')
			FROM request_logs_with_current_month
			WHERE parent_request_id = ANY($1)`
		childArgs := []any{ids}
		if p.TenantID != "" {
			childSQL += " AND tenant_id = $2"
			childArgs = append(childArgs, p.TenantID)
		}
		childSQL += " ORDER BY ts ASC, request_id ASC"

		crows, err := db.Query(ctx, childSQL, childArgs...)
		if err != nil {
			return nil, err
		}
		defer crows.Close()
		for crows.Next() {
			var parentID string
			c := &SessionChildRequest{}
			var requestType, originActor string
			if err := crows.Scan(&parentID, &c.RequestID, &c.Status, &c.LatencyMs, &requestType, &originActor); err != nil {
				return nil, fmt.Errorf("read child requests failed: %w", err)
			}
			c.RequestType = normalizeChildRequestType(requestType, originActor)
			if parent, ok := index[parentID]; ok {
				parent.ChildRequests = append(parent.ChildRequests, c)
			}
		}
		if err := crows.Err(); err != nil {
			return nil, err
		}
	}

	return &sessionTurnsTreeResult{Turns: turns, HasMore: hasMore, NextKey: lastKey}, nil
}

// sessionTurnsTreeExists 检查会话是否有任何请求记录（不限租户），
// 返回归属租户用于跨租户 403 判定。
func sessionTurnsTreeExists(ctx context.Context, db sessionTurnsTreeDB, sessionID string) (bool, string, error) {
	var tenantID string
	err := db.QueryRow(ctx, `
		SELECT tenant_id FROM request_logs_with_current_month
		WHERE gw_session_id = $1 LIMIT 1`, sessionID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, tenantID, nil
}

// normalizeChildRequestType 把 510 迁移的 request_type 枚举映射为前端契约值
// （title | summary | sensitive_word），request_type 缺失/为 main 时回退
// origin_actor（X-Gw-Source-Actor 头落库列）推断。best-effort：当前写路径只有
// auto-title-generator / auto-summary-generator 会带 actor，sensitive_check
// 尚无写入方，无法推断时归为 other。
func normalizeChildRequestType(requestType, originActor string) string {
	switch requestType {
	case "title_gen":
		return "title"
	case "summary":
		return "summary"
	case "sensitive_check":
		return "sensitive_word"
	case "compression":
		return "compression"
	}
	// 回退：request_type 缺失（存量行默认 'main'）或未分类 → origin_actor 推断
	switch originActor {
	case "auto-title-generator":
		return "title"
	case "auto-summary-generator", "session-summary":
		return "summary"
	}
	return "other"
}

// sessionTurnsTreeCursorPayload 是 cursor 的签名载荷（绑定 session 防跨会话复用）。
type sessionTurnsTreeCursorPayload struct {
	TurnNumber int64  `json:"n"`
	RequestID  string `json:"r"`
	SessionID  string `json:"s"`
}

func encodeSessionTurnsTreeCursor(key sessionTurnsTreeCursor, sessionID string) (string, error) {
	if key.TurnNumber <= 0 || key.RequestID == "" {
		return "", errors.New("turns cursor requires turn_number and request_id")
	}
	payload := fmt.Sprintf("turns|%s|%d|%s", sessionID, key.TurnNumber, key.RequestID)
	mac, err := computeCursorHMAC(payload)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + mac)), nil
}

func parseSessionTurnsTreeCursor(cursor, sessionID string) (sessionTurnsTreeCursor, error) {
	if cursor == "" {
		return sessionTurnsTreeCursor{}, nil
	}
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return sessionTurnsTreeCursor{}, err
	}
	// 布局: turns|<session_id>|<turn_number>|<request_id>|<hmac>
	parts := strings.SplitN(string(raw), "|", 5)
	if len(parts) != 5 || parts[0] != "turns" {
		return sessionTurnsTreeCursor{}, errSessionTurnsTreeCursor
	}
	cursorSession, tnStr, requestID, providedMAC := parts[1], parts[2], parts[3], parts[4]
	if cursorSession != sessionID || requestID == "" {
		return sessionTurnsTreeCursor{}, errSessionTurnsTreeCursor
	}
	tn, err := strconv.ParseInt(tnStr, 10, 64)
	if err != nil || tn <= 0 {
		return sessionTurnsTreeCursor{}, errSessionTurnsTreeCursor
	}
	mac, err := computeCursorHMAC(strings.Join(parts[:4], "|"))
	if err != nil {
		return sessionTurnsTreeCursor{}, err
	}
	if !hmac.Equal([]byte(providedMAC), []byte(mac)) {
		return sessionTurnsTreeCursor{}, errSessionTurnsTreeCursor
	}
	return sessionTurnsTreeCursor{TurnNumber: tn, RequestID: requestID}, nil
}
