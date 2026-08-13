// Package admin - turns_list.go
//
// 跨会话轮次列表端点（2026-08-09）。
// 将 public.session_turns 表中的轮次数据按时间倒序暴露，供前端轮次列表页
// （TurnsListView.vue）使用。与现有 /api/admin/sessions/<id>/turns 不同，
// 本端点不限定单一会话，而是跨会话展示所有轮次。
//
//   GET /api/admin/turns
//       ?cursor=...        cursor 分页令牌
//       &limit=50          页大小（1-200）
//       &model=...         按模型筛选
//       &provider=...      按供应商筛选
//       &status_code=...   按状态码筛选
//       &ts_from=...       起始时间（RFC3339）
//       &ts_to=...         截止时间（RFC3339）
//       &tenant=...        租户筛选（仅 super_admin 可指定）
//
// 鉴权：admin() 中间件。tenant_admin 只能看到自己的租户数据。
// 返回：{ items: TurnInList[], has_more: bool, next_cursor: string }

package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TurnInList 是跨会话轮次列表的单行记录，继承 TurnListItem 的所有字段，
// 额外携带 session_id 以便前端跳转到会话详情页。
type TurnInList struct {
	TurnListItem
	SessionID string `json:"session_id"`
}

// handleTurnsList 处理 GET /api/admin/turns。
func (h *Handler) handleTurnsList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// 租户范围
	tenantID := ""
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
	} else {
		tenantID = tenantFromQueryOrContext(r)
	}

	// 分页参数
	limit := defaultTurnsListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxTurnsListLimit {
			limit = n
		}
	}

	// Cursor 解析
	var beforeNo int
	var beforeSessionID string
	var beforeTS time.Time
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := validateCursor(encoded, []byte(h.secret), tenantID, "")
		if err != nil {
			if errors.Is(err, errCursorMismatch) {
				writeError(w, http.StatusBadRequest, "cursor mismatch")
			} else {
				writeError(w, http.StatusBadRequest, "invalid cursor")
			}
			return
		}
		beforeNo = decoded.TurnNo
		beforeSessionID = decoded.SessionID
		beforeTS = decoded.TS
	}

	// 时间范围筛选
	now := time.Now().UTC()
	tsFrom := parseQueryTime(r, "ts_from", now.Add(-24*time.Hour))
	tsTo := parseQueryTime(r, "ts_to", now)

	// 构造 WHERE 条件
	clauses := []string{"t.ts >= $1", "t.ts <= $2"}
	args := []any{tsFrom, tsTo}
	argIdx := 3

	if tenantID != "" {
		clauses = append(clauses, fmt.Sprintf("t.tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}

	// Cursor 分页：按 (ts, session_id, turn_no) 复合序，cursor 传各字段
	if beforeTS != (time.Time{}) {
		clauses = append(clauses, fmt.Sprintf(
			"(t.ts, t.session_id, t.turn_no) < ($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2))
		args = append(args, beforeTS, beforeSessionID, beforeNo)
		argIdx += 3
	}

	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		clauses = append(clauses, fmt.Sprintf("t.model = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("provider")); v != "" {
		clauses = append(clauses, fmt.Sprintf("t.provider = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := r.URL.Query().Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clauses = append(clauses, fmt.Sprintf("t.status_code = $%d", argIdx))
			args = append(args, n)
			argIdx++
		}
	}

	where := strings.Join(clauses, " AND ")

	// 查询
	query := fmt.Sprintf(`
		SELECT t.session_id, t.turn_no, t.ts,
			COALESCE(t.title, '') AS title,
			COALESCE(t.summary, '') AS summary,
			COALESCE(t.prompt_tokens, 0) AS prompt_tokens,
			COALESCE(t.completion_tokens, 0) AS completion_tokens,
			COALESCE(t.cost_usd, 0) AS cost_usd,
			COALESCE(t.model, '') AS model,
			COALESCE(t.provider, '') AS provider,
			COALESCE(t.status_code, 0) AS status_code,
			COALESCE(t.submit_mode, '') AS submit_mode,
			COALESCE(t.injection_verdict, '') AS injection_verdict,
			COALESCE(t.output_verdict, '') AS output_verdict,
			COALESCE(t.attachment_count, 0) AS attachment_count
		FROM public.session_turns t
		WHERE %s
		ORDER BY t.ts DESC, t.session_id DESC, t.turn_no DESC
		LIMIT $%d
	`, where, argIdx)
	args = append(args, limit+1)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("admin handleTurnsList query failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "query turns failed")
		return
	}
	defer rows.Close()

	items := make([]TurnInList, 0, limit)
	for rows.Next() {
		var it TurnInList
		if err := rows.Scan(
			&it.SessionID, &it.TurnNo, &it.Ts,
			&it.Title, &it.Summary,
			&it.RequestTokens, &it.ResponseTokens, &it.CostUSD,
			&it.Model, &it.Provider, &it.StatusCode,
			&it.SubmitMode, &it.InjectionVerdict, &it.OutputVerdict,
			&it.AttachmentCount,
		); err != nil {
			slog.Warn("admin handleTurnsList scan failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "scan turn failed")
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("admin handleTurnsList rows.Err after iteration", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "iterate turns failed")
		return
	}

	hasMore := len(items) > limit
	var nextCursor string
	if hasMore {
		items = items[:limit]
		last := items[len(items)-1]
		nextCursor, _ = encodeCursor(cursorPayload{
			TenantID:  tenantID,
			SessionID: last.SessionID,
			TurnNo:    last.TurnNo,
			TS:        last.Ts,
		}, []byte(h.secret))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"has_more":    hasMore,
		"next_cursor": nextCursor,
	})
}
