// Package admin - turns_sessions.go
//
// 会话分组轮次列表端点（2026-08-10）。
//
// 与 GET /api/admin/turns 的扁平列表不同，本端点按会话分组返回：
//   外层是会话摘要（topic / title / summary / status / totals / 压缩汇总），
//   内层是该会话的轮次（request / response 摘要 + compression / cache 明细）。
//
// 供前端轮次列表页（TurnsListView.vue）的分层展示使用 —— 最外层是会话，
// 点击展开该会话下的轮次列表。
//
//   GET /api/admin/turns/sessions
//       ?cursor=...        会话级 cursor 分页令牌
//       &limit=20          页大小（1-50，按会话数计）
//       &tenant=...        租户筛选（仅 super_admin 可指定）
//       &model=...         仅返回包含该模型轮次的会话
//       &provider=...      仅返回包含该供应商轮次的会话
//       &status_code=...   仅返回包含该状态码轮次的会话
//       &ts_from=...       会话开始时间（COALESCE(ss.first_request_at, s.created_at)）起始（RFC3339）
//       &ts_to=...         会话开始时间截止（RFC3339）
//       &project_id=...    按会话所属项目（ss.gw_project_id）过滤
//       &task_id=...       按会话任务（sd.task_id）过滤
//       &search=...        标题 / topic / 摘要 模糊匹配
//       &tags=...          按 user_tags（逗号分隔，任一命中即保留，&& 数组重叠语义）过滤
//       &client=...        按客户端（sd.client_id / sd.application_code / s.client_type）过滤
//       &owner_user=...    按会话属主用户（sd.owner_user）过滤
//
// 鉴权：admin() 中间件。tenant_admin 只能看到自己的租户数据。
// 返回：{ items: TurnsSessionGroup[], has_more: bool, next_cursor: string }

package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTurnsSessionsLimit = 20
	maxTurnsSessionsLimit     = 50
)

// TurnsCompressionAgg 是会话内压缩操作的汇总。
type TurnsCompressionAgg struct {
	AppliedCount int      `json:"applied_count"`
	TokensSaved  int      `json:"tokens_saved"`
	Strategies   []string `json:"strategies"`
}

// TurnsSessionGroup 是会话分组的最外层记录（会话摘要）。
type TurnsSessionGroup struct {
	SessionID          string              `json:"session_id"`
	TenantID           string              `json:"tenant_id"`
	Title              *string             `json:"title,omitempty"`
	Topic              *string             `json:"topic,omitempty"`
	Intent             *string             `json:"intent,omitempty"`
	Summary            *string             `json:"summary,omitempty"`
	SummaryModel       *string             `json:"summary_model,omitempty"`
	SummaryGeneratedAt *time.Time          `json:"summary_generated_at,omitempty"`
	Status             string              `json:"status"`
	TaskType           *string             `json:"task_type,omitempty"`
	ClientType         *string             `json:"client_type,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	ClosedAt           *time.Time          `json:"closed_at,omitempty"`
	TotalTurns         int                 `json:"total_turns"`
	TotalTokens        int                 `json:"total_tokens"`
	TotalCostUSD       float64             `json:"total_cost_usd"`
	LastTurnNo         *int                `json:"last_turn_no,omitempty"`
	LastModel          *string             `json:"last_model,omitempty"`
	LastProvider       *string             `json:"last_provider,omitempty"`
	ModelsUsed         []string            `json:"models_used"`
	FailoverCount      int                 `json:"failover_count"`
	ErrorCount         int                 `json:"error_count"`
	DurationMs         int64               `json:"duration_ms"`
	Compression        TurnsCompressionAgg `json:"compression"`
	Turns              []TurnGroupItem     `json:"turns"`

	// 会话级附加信息（来自 session_dim / session_summaries LEFT JOIN，可能为空）
	ProjectID       *string    `json:"project_id,omitempty"`
	TaskID          *string    `json:"task_id,omitempty"`
	OwnerUser       *string    `json:"owner_user,omitempty"`
	ClientID        *string    `json:"client_id,omitempty"`
	ApplicationCode *string    `json:"application_code,omitempty"`
	EndUserID       *string    `json:"end_user_id,omitempty"`
	UserTags        []string   `json:"user_tags"`
	StartTime       *time.Time `json:"start_time,omitempty"`

	// 会话间父子/附属关系：本会话由哪个会话创建/派生。
	// handoff_logs（透明轮换，持久化）优先；gt_/gs_ 前缀（auto title/summary
	// 回环分支会话）按 ID 前缀推导。
	ParentSessionID *string `json:"parent_session_id,omitempty"`
	ParentRelation  *string `json:"parent_relation,omitempty"` // handoff | auto_title | auto_summary
}

// TurnGroupItem 是会话内单个轮次的记录（含 compression / cache / failover 明细）。
type TurnGroupItem struct {
	TurnNo                 int       `json:"turn_no"`
	Ts                     time.Time `json:"ts"`
	Title                  string    `json:"title,omitempty"`
	Summary                string    `json:"summary,omitempty"`
	RequestTokens          int       `json:"request_tokens"`
	ResponseTokens         int       `json:"response_tokens"`
	CacheReadTokens        int       `json:"cache_read_tokens"`
	CacheWriteTokens       int       `json:"cache_write_tokens"`
	CostUSD                float64   `json:"cost_usd"`
	Model                  string    `json:"model"`
	Provider               string    `json:"provider"`
	StatusCode             int       `json:"status_code"`
	Success                bool      `json:"success"`
	ErrorKind              *string   `json:"error_kind,omitempty"`
	SubmitMode             string    `json:"submit_mode"`
	CompressionApplied     bool      `json:"compression_applied"`
	CompressionStrategy    *string   `json:"compression_strategy,omitempty"`
	CompressionTokensSaved *int      `json:"compression_tokens_saved,omitempty"`
	InjectionVerdict       string    `json:"injection_verdict"`
	OutputVerdict          string    `json:"output_verdict"`
	AttachmentCount        int       `json:"attachment_count"`
	AttemptNo              int       `json:"attempt_no"`
	LatencyMs              *int      `json:"latency_ms,omitempty"`
}

// handleTurnsSessions 处理 GET /api/admin/turns/sessions。
func (h *Handler) handleTurnsSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
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

	// 页大小（按会话数计）
	limit := defaultTurnsSessionsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxTurnsSessionsLimit {
			limit = n
		}
	}

	// Cursor 解析（会话级：TS=updated_at, SessionID=session_id, TurnNo 忽略）
	var beforeTS time.Time
	var beforeSessionID string
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeCursor(encoded, []byte(h.secret))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		if decoded.TenantID != tenantID {
			writeError(w, http.StatusBadRequest, "cursor mismatch")
			return
		}
		beforeTS = decoded.TS
		beforeSessionID = decoded.SessionID
	}

	// 会话时间范围（基于会话开始时间 COALESCE(ss.first_request_at, s.created_at)）。
	// 未提供 ts_from / ts_to 时不加时间限制 —— 默认返回最近（按 updated_at 倒序）的会话，
	// 配合 LIMIT 即"最近 20 个会话"。
	var tsFrom, tsTo time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("ts_from")); raw != "" {
		tsFrom = parseQueryTime(r, "ts_from", time.Time{})
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("ts_to")); raw != "" {
		tsTo = parseQueryTime(r, "ts_to", time.Time{})
	}

	// 构造会话 WHERE（含 project/task/search/tags/client/owner 过滤）
	where, args, argIdx := buildTurnsSessionWhere(r, tenantID, tsFrom, tsTo, beforeTS, beforeSessionID, 1)

	// 会话查询
	queryClause := ""
	if where != "" {
		queryClause = "WHERE " + where
	}
	query := fmt.Sprintf(`
		SELECT s.session_id, s.tenant_id,
			COALESCE(NULLIF(s.title, ''), st.title, ss.title) AS title,
			s.topic,
			COALESCE(NULLIF(s.intent, ''), ss.user_intent) AS intent,
			COALESCE(NULLIF(s.summary, ''), ss.summary) AS summary,
			s.summary_model, s.summary_generated_at,
			s.status, s.task_type, s.client_type,
			s.created_at, s.updated_at, s.closed_at,
			s.total_turns, s.total_tokens, s.total_cost_usd,
			s.last_turn_no, s.last_model, s.last_provider,
			COALESCE(ss.gw_project_id, sd.project_id), sd.task_id, sd.owner_user,
			sd.client_id, sd.application_code, sd.end_user_id,
			COALESCE(ss.user_tags, '{}') AS user_tags,
			ss.first_request_at AS start_time,
			ho.parent_session_id, ho.trigger_reason
		FROM public.sessions s
		LEFT JOIN session_dim sd
			ON sd.gw_session_id = s.session_id AND sd.tenant_id = s.tenant_id		LEFT JOIN session_summaries ss
			ON ss.session_key = s.session_id AND ss.tenant_id = s.tenant_id
		-- 会话父子关系：本会话若是 handoff（透明轮换）创建的新会话，
		-- handoff_logs 里 new_session_id = 本会话 的记录给出父会话。
		-- LATERAL LIMIT 1 防止多次轮换记录导致行扩展。
		LEFT JOIN LATERAL (
			SELECT hl.session_id AS parent_session_id, hl.trigger_reason
			FROM public.handoff_logs hl
			WHERE hl.new_session_id = s.session_id
			ORDER BY hl.created_at DESC
			LIMIT 1
		) ho ON TRUE
		-- session_titles: auto_title_generator 写入的标题（V1 表），
		-- 取最新一条作为 s.title 的 fallback。用 LATERAL 避免一个会话多行导致行扩展。
		-- task_id 过滤防止跨任务/租户泄漏：优先匹配真实 task_id（sd.task_id），
		-- 其次接受 'auto'（auto_title_generator 旧版默认值）。
		%s
		%s
		ORDER BY s.updated_at DESC, s.session_id DESC
		LIMIT $%d
	`, sessionTitleFallbackJoinSQL("s.session_id", "sd.task_id"), queryClause, argIdx)
	args = append(args, limit+1)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("admin handleTurnsSessions query failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "query sessions failed")
		return
	}
	defer rows.Close()

	var handoffReason *string
	sessions := make([]*TurnsSessionGroup, 0, limit+1)
	for rows.Next() {
		var g TurnsSessionGroup
		if err := rows.Scan(
			&g.SessionID, &g.TenantID, &g.Title, &g.Topic, &g.Intent,
			&g.Summary, &g.SummaryModel, &g.SummaryGeneratedAt,
			&g.Status, &g.TaskType, &g.ClientType,
			&g.CreatedAt, &g.UpdatedAt, &g.ClosedAt,
			&g.TotalTurns, &g.TotalTokens, &g.TotalCostUSD,
			&g.LastTurnNo, &g.LastModel, &g.LastProvider,
			&g.ProjectID, &g.TaskID, &g.OwnerUser,
			&g.ClientID, &g.ApplicationCode, &g.EndUserID,
			&g.UserTags, &g.StartTime,
			&g.ParentSessionID, &handoffReason,
		); err != nil {
			slog.Warn("admin handleTurnsSessions scan failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "scan session failed")
			return
		}
		g.ModelsUsed = []string{}
		g.UserTags = []string{}
		g.Compression = TurnsCompressionAgg{Strategies: []string{}}
		applySessionParent(&g)
		sessions = append(sessions, &g)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("admin handleTurnsSessions rows.Err after iteration", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "iterate sessions failed")
		return
	}

	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}

	// 批量加载这些会话的轮次
	if len(sessions) > 0 {
		if err := h.loadTurnsForSessions(ctx, sessions, tenantID, r); err != nil {
			slog.Warn("admin handleTurnsSessions load turns failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "query turns failed")
			return
		}
	}

	// next cursor
	var nextCursor string
	if hasMore {
		last := sessions[len(sessions)-1]
		nextCursor, _ = encodeCursor(cursorPayload{
			TenantID:  tenantID,
			SessionID: last.SessionID,
			TS:        last.UpdatedAt,
		}, []byte(h.secret))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       sessions,
		"has_more":    hasMore,
		"next_cursor": nextCursor,
	})
}

// buildTurnsSessionWhere 构造会话级 WHERE 条件。
// 时间范围基于会话开始时间 COALESCE(ss.first_request_at, s.created_at)；
// tsFrom / tsTo 为零值（time.Time{}）时表示未指定，不生成对应时间子句。
// 支持 project_id / task_id / search / tags / client / owner_user 过滤
// （来自 session_dim sd / session_summaries ss LEFT JOIN）。
// startArg 为第一个占位符索引（通常为 1）。
// 返回 WHERE 片段、参数、下一个占位符索引。
func buildTurnsSessionWhere(r *http.Request, tenantID string, tsFrom, tsTo time.Time, beforeTS time.Time, beforeSessionID string, startArg int) (string, []any, int) {
	clauses := []string{}
	args := make([]any, 0, 10)
	argIdx := startArg

	if !tsFrom.IsZero() {
		clauses = append(clauses, fmt.Sprintf("COALESCE(ss.first_request_at, s.created_at) >= $%d", argIdx))
		args = append(args, tsFrom)
		argIdx++
	}
	if !tsTo.IsZero() {
		clauses = append(clauses, fmt.Sprintf("COALESCE(ss.first_request_at, s.created_at) <= $%d", argIdx))
		args = append(args, tsTo)
		argIdx++
	}

	if tenantID != "" {
		clauses = append(clauses, fmt.Sprintf("s.tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}

	// model / provider / status_code 是轮次级条件：在会话查询里用 EXISTS 直接
	// 排除不含匹配轮次的会话，避免整页"无匹配轮次"的空会话卡片。
	// loadTurnsForSessions 会用同样条件过滤会话内展示的轮次。
	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		clauses = append(clauses, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM public.session_turns ft WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id AND ft.model = $%d)", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("provider")); v != "" {
		clauses = append(clauses, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM public.session_turns ft WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id AND ft.provider = $%d)", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status_code")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clauses = append(clauses, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM public.session_turns ft WHERE ft.session_id = s.session_id AND ft.tenant_id = s.tenant_id AND ft.status_code = $%d)", argIdx))
			args = append(args, n)
			argIdx++
		}
	}

	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		clauses = append(clauses, fmt.Sprintf("ss.gw_project_id = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("task_id")); v != "" {
		clauses = append(clauses, fmt.Sprintf("sd.task_id = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("owner_user")); v != "" {
		clauses = append(clauses, fmt.Sprintf("sd.owner_user = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("client")); v != "" {
		clauses = append(clauses, fmt.Sprintf(
			"(sd.client_id = $%d OR sd.application_code = $%d OR s.client_type = $%d)",
			argIdx, argIdx+1, argIdx+2))
		args = append(args, v, v, v)
		argIdx += 3
	}
	if v := strings.TrimSpace(r.URL.Query().Get("tags")); v != "" {
		clauses = append(clauses, fmt.Sprintf("ss.user_tags && $%d", argIdx))
		args = append(args, strings.Split(v, ","))
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("search")); v != "" {
		// search 同时匹配标题（含 session_titles / session_summaries fallback）、
		// topic、user_intent、摘要（含 session_summaries fallback），让自动生成
		// 的标题/摘要也能被搜到。
		clauses = append(clauses, fmt.Sprintf(
			"(COALESCE(s.title, st.title, ss.title) ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(s.topic, '') ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(s.intent, ss.user_intent, '') ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(s.summary, ss.summary) ILIKE '%%'||$%d||'%%')",
			argIdx, argIdx+1, argIdx+2, argIdx+3))
		args = append(args, v, v, v, v)
		argIdx += 4
	}

	// 会话级 cursor：(s.updated_at, s.session_id) < (beforeTS, beforeSessionID)
	if !beforeTS.IsZero() {
		clauses = append(clauses, fmt.Sprintf(
			"(s.updated_at, s.session_id) < ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, beforeTS, beforeSessionID)
		argIdx += 2
	}

	return strings.Join(clauses, " AND "), args, argIdx
}

// loadTurnsForSessions 批量查询给定会话的轮次并回填到每个会话分组中。
// 支持按 model / provider / status_code 过滤轮次；同时计算会话级聚合
// （models_used / failover_count / error_count / duration_ms / compression）。
func (h *Handler) loadTurnsForSessions(ctx context.Context, sessions []*TurnsSessionGroup, tenantID string, r *http.Request) error {
	sessionIDs := make([]string, 0, len(sessions))
	for _, g := range sessions {
		sessionIDs = append(sessionIDs, g.SessionID)
	}

	// 轮次过滤条件
	turnClauses := []string{"t.session_id = ANY($1)"}
	turnArgs := []any{sessionIDs}
	argIdx := 2

	if tenantID != "" {
		turnClauses = append(turnClauses, fmt.Sprintf("t.tenant_id = $%d", argIdx))
		turnArgs = append(turnArgs, tenantID)
		argIdx++
	}

	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		turnClauses = append(turnClauses, fmt.Sprintf("t.model = $%d", argIdx))
		turnArgs = append(turnArgs, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("provider")); v != "" {
		turnClauses = append(turnClauses, fmt.Sprintf("t.provider = $%d", argIdx))
		turnArgs = append(turnArgs, v)
		argIdx++
	}
	if v := r.URL.Query().Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			turnClauses = append(turnClauses, fmt.Sprintf("t.status_code = $%d", argIdx))
			turnArgs = append(turnArgs, n)
			argIdx++
		}
	}

	turnWhere := strings.Join(turnClauses, " AND ")

	query := fmt.Sprintf(`
		SELECT t.session_id, t.turn_no, t.ts,
			COALESCE(t.title, '') AS title,
			COALESCE(t.summary, '') AS summary,
			COALESCE(t.prompt_tokens, 0) AS prompt_tokens,
			COALESCE(t.completion_tokens, 0) AS completion_tokens,
			COALESCE(t.cache_read_tokens, 0) AS cache_read_tokens,
			COALESCE(t.cache_write_tokens, 0) AS cache_write_tokens,
			COALESCE(t.cost_usd, 0) AS cost_usd,
			COALESCE(t.model, '') AS model,
			COALESCE(t.provider, '') AS provider,
			COALESCE(t.status_code, 0) AS status_code,
			COALESCE(t.success, false) AS success,
			t.error_kind,
			COALESCE(t.submit_mode, '') AS submit_mode,
			COALESCE(t.compression_applied, false) AS compression_applied,
			t.compression_strategy,
			t.compression_tokens_saved,
			COALESCE(t.injection_verdict, '') AS injection_verdict,
			COALESCE(t.output_verdict, '') AS output_verdict,
			COALESCE(t.attachment_count, 0) AS attachment_count,
			COALESCE(t.attempt_no, 0) AS attempt_no,
			t.latency_ms
		FROM public.session_turns t
		WHERE %s
		ORDER BY t.session_id, t.turn_no ASC
	`, turnWhere)

	rows, err := h.db.Query(ctx, query, turnArgs...)
	if err != nil {
		return fmt.Errorf("query turns for sessions failed: %w", err)
	}
	defer rows.Close()

	bySession := make(map[string]*TurnsSessionGroup, len(sessions))
	for _, g := range sessions {
		bySession[g.SessionID] = g
	}

	for rows.Next() {
		var it TurnGroupItem
		var sessionID string
		if err := rows.Scan(
			&sessionID, &it.TurnNo, &it.Ts,
			&it.Title, &it.Summary,
			&it.RequestTokens, &it.ResponseTokens,
			&it.CacheReadTokens, &it.CacheWriteTokens,
			&it.CostUSD,
			&it.Model, &it.Provider, &it.StatusCode, &it.Success, &it.ErrorKind,
			&it.SubmitMode, &it.CompressionApplied, &it.CompressionStrategy,
			&it.CompressionTokensSaved,
			&it.InjectionVerdict, &it.OutputVerdict,
			&it.AttachmentCount, &it.AttemptNo, &it.LatencyMs,
		); err != nil {
			return fmt.Errorf("scan turn failed: %w", err)
		}

		g := bySession[sessionID]
		if g == nil {
			continue
		}
		g.Turns = append(g.Turns, it)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate turns failed: %w", err)
	}

	// 计算会话级聚合
	for _, g := range sessions {
		ensureTurnsNonNil(g)
		computeSessionAggs(g)
	}
	return nil
}

// applySessionParent 回填会话的父子关系。
// 优先使用 handoff_logs 的持久化记录（关系 = handoff）；
// 无记录时按 ID 前缀推导 auto title/summary 回环分支会话的父会话。
func applySessionParent(g *TurnsSessionGroup) {
	if g == nil {
		return
	}
	if g.ParentSessionID != nil && *g.ParentSessionID != "" {
		g.ParentRelation = strPtrTurns("handoff")
		return
	}
	if parent, relation := deriveSessionParent(g.SessionID); parent != "" {
		g.ParentSessionID = &parent
		g.ParentRelation = &relation
	}
}

// deriveSessionParent 按 ID 前缀推导派生会话的父会话。
// auto title/summary 的 loopback 会话 ID 是 "gt_" / "gs_" + 原会话 ID
// （见 admin/auto_title_generator.go、admin/auto_summary_generator.go）。
func deriveSessionParent(sessionID string) (parent, relation string) {
	switch {
	case strings.HasPrefix(sessionID, "gt_"):
		return strings.TrimPrefix(sessionID, "gt_"), "auto_title"
	case strings.HasPrefix(sessionID, "gs_"):
		return strings.TrimPrefix(sessionID, "gs_"), "auto_summary"
	}
	return "", ""
}

func strPtrTurns(s string) *string { return &s }

// ensureTurnsNonNil 确保会话的 Turns 为非 nil 空切片。// 会话因过滤（model/provider/status_code）无匹配轮次时 g.Turns 为 nil，
// 若直接返回 nil 会让 JSON 输出 "turns": null，导致前端读取 .length 崩溃。
func ensureTurnsNonNil(g *TurnsSessionGroup) {
	if g != nil && g.Turns == nil {
		g.Turns = []TurnGroupItem{}
	}
}

// computeSessionAggs 根据回填的轮次计算会话级聚合指标。
func computeSessionAggs(g *TurnsSessionGroup) {
	if g == nil {
		return
	}
	modelSeen := map[string]bool{}
	strategySeen := map[string]bool{}
	var firstTS, lastTS time.Time
	for _, t := range g.Turns {
		if t.Model != "" && !modelSeen[t.Model] {
			modelSeen[t.Model] = true
			g.ModelsUsed = append(g.ModelsUsed, t.Model)
		}
		if t.AttemptNo > 0 {
			g.FailoverCount++
		}
		if !t.Success || t.StatusCode >= 400 {
			g.ErrorCount++
		}
		if t.CompressionApplied {
			g.Compression.AppliedCount++
			if t.CompressionTokensSaved != nil {
				g.Compression.TokensSaved += *t.CompressionTokensSaved
			}
			if t.CompressionStrategy != nil && *t.CompressionStrategy != "" && !strategySeen[*t.CompressionStrategy] {
				strategySeen[*t.CompressionStrategy] = true
				g.Compression.Strategies = append(g.Compression.Strategies, *t.CompressionStrategy)
			}
		}
		if firstTS.IsZero() || t.Ts.Before(firstTS) {
			firstTS = t.Ts
		}
		if t.Ts.After(lastTS) {
			lastTS = t.Ts
		}
	}
	if !firstTS.IsZero() && !lastTS.IsZero() {
		g.DurationMs = lastTS.Sub(firstTS).Milliseconds()
		if g.DurationMs < 0 {
			g.DurationMs = 0
		}
	}
}
