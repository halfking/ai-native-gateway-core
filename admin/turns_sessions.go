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
//       &ts_from=...       会话最近更新时间（s.updated_at）起始（RFC3339）
//       &ts_to=...         会话最近更新时间截止（RFC3339）
//       &project_id=...    按会话所属项目（ss.gw_project_id）过滤
//       &task_id=...       按会话任务（sd.task_id）过滤
//       &search=...        标题 / topic / 摘要 模糊匹配
//       &tags=...          按 user_tags（逗号分隔，任一命中即保留，&& 数组重叠语义）过滤
//       &client=...        按客户端（sd.client_id / sd.application_code / s.client_type）过滤
//       &owner_user=...    按会话属主用户（sd.owner_user）过滤
//       &api_key_id=...    按会话内轮次关联的 API Key（_request_logs.api_key_id）过滤
//       &status=...        按会话状态（active/closed/archived/deleted）过滤
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

	"github.com/kaixuan/llm-gateway-go/db"
)

const (
	defaultTurnsSessionsLimit = 20
	maxTurnsSessionsLimit     = 50
	turnsSessionProjectExpr   = "COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id)"
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
	// API Key：取会话最近一轮关联 _request_logs.api_key_id；label 仅前缀/别名。
	APIKeyID    *int64  `json:"api_key_id,omitempty"`
	APIKeyLabel *string `json:"api_key_label,omitempty"`

	// 会话间父子/附属关系：本会话由哪个会话创建/派生。
	// handoff_logs（透明轮换，持久化）优先；gt_/gs_ 前缀（auto title/summary
	// 回环分支会话）按 ID 前缀推导。
	ParentSessionID *string `json:"parent_session_id,omitempty"`
	ParentRelation  *string `json:"parent_relation,omitempty"` // handoff | auto_title | auto_summary

	// SessionAnalysis 是 migration 567 的 session_analysis_metadata 读侧投影
	// （LEFT JOIN LATERAL 命中时非空）。Payload 字段是 sessionmeta.Result，
	// 与 admin/session-analytics/* 的窄视图保持同一形状。
	SessionAnalysis *SessionAnalysisView `json:"session_analysis,omitempty"`
}

// TurnGroupItem 是会话内单个轮次的记录（含 compression / cache / failover 明细）。
type TurnGroupItem struct {
	TurnNo                 int       `json:"turn_no"`
	Ts                     time.Time `json:"ts"`
	RequestID              string    `json:"request_id,omitempty"`
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

// turnsSessionsListSQL 是会话列表主查询（不含 WHERE/LIMIT）。
// api_key / handoff 在分页结果确定后批量 enrich，避免每行 LATERAL 扫 _request_logs。
//
// 2026-08-26: 增加 LEFT JOIN LATERAL session_analysis_metadata（migration 567），
// 暴露 SessionAnalysisView。前端轮次列表页直接渲染 agent/work_types/project，
// 不再依赖额外的 /session-analytics/* 调用。该 join 走 (tenant_id, scoped_session_id)
// 选择 status='final' 优先、最新 updated_at 兜底（见 session_meta_view.go）。
func turnsSessionsListSQL() string {
	return fmt.Sprintf(`
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
					%s, sd.task_id, sd.owner_user,
					sd.client_id, sd.application_code, sd.end_user_id,
					COALESCE(ss.user_tags, '{}') AS user_tags,
					ss.first_request_at AS start_time,
					%s
			FROM public.sessions s
			LEFT JOIN session_dim sd
				ON sd.gw_session_id = s.session_id AND sd.tenant_id = s.tenant_id
			LEFT JOIN session_summaries ss
				ON ss.session_key = s.session_id AND ss.tenant_id = s.tenant_id
			LEFT JOIN public.session_title_states tstate
				ON tstate.tenant_id = s.tenant_id AND tstate.scoped_session_id = s.session_id
			%s
			%s`, turnsSessionProjectExpr,
		sessionAnalysisSelectCols(),
		sessionAnalysisJoinSQL(),
		sessionTitleFallbackJoinSQL("s.session_id", "sd.task_id"))
}

// turnsSessionsLatestPartitionSQL 返回「每个 (tenant_id, session_id) 只取最新分区行」
// 的谓词。
//
// 为什么必须去重（2026-10-04 §9.112/§9.113/§9.114）：`public.sessions` 是
// PARTITION BY RANGE (partition_date)，而 PostgreSQL 硬规则要求分区表的唯一约束
// 必须包含分区键，所以唯一键只能是 UNIQUE (session_id, partition_date)。
// **「一会话一行」在这个表上不成立**：跨月会话每月各有一行，252 上 413 个
// (session_id, tenant_id) 组合多行、全部跨 partition_date。
//
// 口径依据：写侧 titlestore 的 CommitTitle / DeleteTitle 早就用
// `partition_date = (SELECT MAX(partition_date) ...)` 只投影最新行
// （internal/titlestore/store.go）。本谓词让读侧与写侧一致。
//
// 性能：子查询按 (tenant_id, session_id) 走 idx_sessions_session_id 的分区级子索引，
// 不产生全表扫；谓词是普通 WHERE 而非子查询 FROM，故外层的分区裁剪与索引选择
// 不受影响（不用 DISTINCT ON 子查询正是为此——那会成为优化屏障并阻断谓词下推）。
func turnsSessionsLatestPartitionSQL() string {
	return `s.partition_date = (SELECT MAX(x.partition_date) FROM public.sessions x
		WHERE x.tenant_id = s.tenant_id AND x.session_id = s.session_id)`
}

// turnsSessionsFinalizeWhere 把跨月去重谓词与调用方过滤条件合成最终 WHERE 内容。
//
// 去重谓词无条件前置：这是"一会话一行"这个契约的落点，只在有过滤条件时才生效
// 等于没有。filter 整体加括号——buildTurnsSessionWhere 内部以 AND 拼接且自带括号，
// 但外层再包一层才不依赖那个约定（`a AND b OR c` 与 `a AND (b OR c)` 不等价）。
func turnsSessionsFinalizeWhere(filter string) string {
	latest := turnsSessionsLatestPartitionSQL()
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return latest
	}
	return latest + " AND (" + filter + ")"
}

// resolveTurnsSessionsTenant 解析 turns 列表的租户范围。
// tenant_admin 仅看本租户；super_admin 默认全租户（EffectiveTenantIDAll），
// 可通过 ?tenant= 显式收窄。
func resolveTurnsSessionsTenant(r *http.Request) string {
	if IsTenantAdmin(r) {
		return GetTenantID(r)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("tenant")); v != "" {
		return v
	}
	return EffectiveTenantIDAll(r)
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

	tenantID := resolveTurnsSessionsTenant(r)

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

	// 会话时间范围（基于 s.updated_at，与列表排序一致）。
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

	// 会话查询。WHERE 恒存在：turnsSessionsFinalizeWhere 无条件前置跨月去重谓词。
	where = turnsSessionsFinalizeWhere(where)
	query := fmt.Sprintf(`%s
		WHERE %s
		ORDER BY s.updated_at DESC, s.session_id DESC
		LIMIT $%d
		`, turnsSessionsListSQL(), where, argIdx)
	args = append(args, limit+1)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("admin handleTurnsSessions query failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "query sessions failed")
		return
	}
	defer rows.Close()

	sessions := make([]*TurnsSessionGroup, 0, limit+1)
	for rows.Next() {
		var g TurnsSessionGroup
		// session_analysis_metadata 列来自 LEFT JOIN LATERAL, 在 JOIN miss
		// (会话从未被分析) 时为 SQL NULL。必须用 *string 接收, 否则 pgx 报
		// "cannot scan NULL into *string" → /turns/sessions 接口 500。
		var (
			saStatus, saSchemaVersion, saInputHash *string
			saSourceTaskID                         *string
			saUpdatedAt                            *time.Time
			saPayloadRaw                           []byte
		)
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
			&saStatus, &saSchemaVersion, &saInputHash, &saSourceTaskID, &saUpdatedAt, &saPayloadRaw,
		); err != nil {
			slog.Warn("admin handleTurnsSessions scan failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "scan session failed")
			return
		}
		saStatusVal, saSchemaVal, saHashVal := "", "", ""
		if saStatus != nil {
			saStatusVal = *saStatus
		}
		if saSchemaVersion != nil {
			saSchemaVal = *saSchemaVersion
		}
		if saInputHash != nil {
			saHashVal = *saInputHash
		}
		g.ModelsUsed = []string{}
		if g.UserTags == nil {
			g.UserTags = []string{}
		}
		g.Compression = TurnsCompressionAgg{Strategies: []string{}}
		if saStatusVal != "" {
			var view SessionAnalysisView
			scanSessionAnalysis(&view, saStatusVal, saSchemaVal, saHashVal, saSourceTaskID, saUpdatedAt, saPayloadRaw)
			g.SessionAnalysis = &view
		}

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

	if len(sessions) > 0 {
		if err := h.enrichTurnsSessionMeta(ctx, sessions, tenantID); err != nil {
			slog.Warn("admin handleTurnsSessions enrich meta failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "enrich sessions failed")
			return
		}
		for _, g := range sessions {
			applySessionParent(g)
		}
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
// 时间范围基于 s.updated_at（与列表排序一致）；
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
		clauses = append(clauses, fmt.Sprintf("s.updated_at >= $%d", argIdx))
		args = append(args, tsFrom)
		argIdx++
	}
	if !tsTo.IsZero() {
		clauses = append(clauses, fmt.Sprintf("s.updated_at <= $%d", argIdx))
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
	//
	// ★ 2026-10-24（R90）：这三条**不再**走 `session_turns_with_current_month` 视图，
	//   改成 db.SessionFamilyTurnsSessionExistsSQL 的 **hot ∪ 父表两腿 EXISTS**。
	//   理由（252 EXPLAIN ANALYZE 实测，生产只读）：视图的 hot 腿带
	//   `NOT EXISTS (… FROM session_turns archived …)` 反连接 ⇒ 外层**每一行**
	//   都要在 hot 上跑一次 Nested Loop Anti Join ⇒ `?model=` 生产形态
	//   **36,415 ms** ⇒ 角色级 statement_timeout=30s ⇒ **稳定 500@30s**。
	//   同一手法在 admin/turns_filter_options.go 的 turnMergedSource
	//   早已在用（2026-10-18：视图 44,564 ms ⇒ 两腿 2,048/2,814 ms）。
	//   ★ 口径等价：视图 ≡ hot ∪ 父表（promote 是 DELETE hot + INSERT parent 的
	//   **移动** ⇒ 同一 request_id 不同时存在两边 ⇒ 反连接剔除 0 行；
	//   252 实测 hot ∩ 父表配对 **0 行**）⇒ 同一列名、同一谓词。
	//
	// ★ 2026-10-24（R92）：形态再从「两条 EXISTS 用 OR 组合」改成
	//   **派生表 UNION ALL + 单一 EXISTS**（同一 helper）。
	//   ⇒ OR 形态只能被规划成「会话驱动」⇒ **稀有过滤值**（生产实测：该 model
	//     只命中 30 / 441,112 个会话）⇒ 外层要扫 35 万个会话才凑够 LIMIT 21
	//     ⇒ 500@30s。★ R90 上线后实测：model 仍 500×3，provider/status_code
	//     已 200 ⇒ 「R90 四条全修」的结论被证伪。
	//   ⇒ V2 形态可被 pull-up ⇒ 规划器能自己选驱动方向：稀有 ⇒ 轮次驱动
	//     （外层只剩 30 行），常见 ⇒ 会话驱动（前 21 个会话立刻凑齐）。
	//   ⇒ 252 生产实测（事务内建索引 + ROLLBACK）：model 稀有值
	//     30,008 ms ⇒ **8.0 ms**；provider='14' 358 ms ⇒ **48.6 ms**。
	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		clauses = append(clauses, db.SessionFamilyTurnsSessionExistsSQL(
			"t.model", "model", "ft.model = $"+strconv.Itoa(argIdx)))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("provider")); v != "" {
		clauses = append(clauses, db.SessionFamilyTurnsSessionExistsSQL(
			"t.provider", "provider", "ft.provider = $"+strconv.Itoa(argIdx)))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status_code")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clauses = append(clauses, db.SessionFamilyTurnsSessionExistsSQL(
				"t.status_code", "status_code", "ft.status_code = $"+strconv.Itoa(argIdx)))
			args = append(args, n)
			argIdx++
		}
	}

	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		clauses = append(clauses, fmt.Sprintf("%s = $%d", turnsSessionProjectExpr, argIdx))
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
	if v := strings.TrimSpace(r.URL.Query().Get("api_key_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			// ★ 2026-10-24（R92）：这一支现在用 db.SessionFamilyTurnsSessionExistsSQL
			//   渲染的 **派生表 UNION ALL + 单一 EXISTS** 形态（R90 的 OR-两腿形态）。
			//   · 为什么换：OR 形态锁死了「会话驱动」方向 ⇒ 过滤值稀有时外层要扫
			//     35 万个会话才凑够 LIMIT 21 ⇒ 生产 500@30s（实测 2026-10-24）。
			//   · 口径：同谓词集、同半连接语义 ⇒ 与 R90 形态**逐行一致**
			//     （252 生产同快照双向 EXCEPT ALL：only_r90=0 / only_v2=0）。
			//
			// ★ 谓词必须与**投影**逐字一致（db.SessionFamilyAPIKeyIDExpr）：
			//   原始列是 text、参数是 int64 ⇒ 归一规则「纯数字才转 bigint」。
			//   若这里改成 `t.api_key_id = $n::text`，则 '0105' 这类值会与投影分叉
			//   ⇒ 「展示的 api_key」与「过滤用的 api_key」不是同一个口径。
			//
			// ★ 保留的语义：**参数非法（0 / 负数 / 非数字）⇒ 整个过滤子句被跳过**
			//   （等价于不过滤）—— 与改动前逐字一致。
			//
			// ★ 旧的「不能用 fmt.Sprintf」陷阱已随全量 source 一起消失：
			//   那个坑来自 source SQL 里的 `LIKE 'sys:%'` / `~ '^[0-9]+$'`
			//   含 `%` 字符 ⇒ 现在这条子句里已没有任何 `%`。
			clauses = append(clauses, db.SessionFamilyTurnsSessionExistsSQL(
				db.SessionFamilyAPIKeyIDExpr, "api_key_id",
				"ft.api_key_id = $"+strconv.Itoa(argIdx)))
			args = append(args, n)
			argIdx++
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		clauses = append(clauses, fmt.Sprintf("s.status = $%d", argIdx))
		args = append(args, v)
		argIdx++
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
			"(CASE WHEN tstate.tenant_id IS NOT NULL THEN COALESCE(tstate.title, '') ELSE COALESCE(NULLIF(s.title, ''), st.title, ss.title, '') END ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(NULLIF(s.topic, ''), '') ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(NULLIF(s.intent, ''), ss.user_intent, '') ILIKE '%%'||$%d||'%%'"+
				" OR COALESCE(NULLIF(s.summary, ''), ss.summary, '') ILIKE '%%'||$%d||'%%')",
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
	turnFilterActive := false

	if tenantID != "" {
		turnClauses = append(turnClauses, fmt.Sprintf("t.tenant_id = $%d", argIdx))
		turnArgs = append(turnArgs, tenantID)
		argIdx++
	}

	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		turnFilterActive = true
		turnClauses = append(turnClauses, fmt.Sprintf("t.model = $%d", argIdx))

		turnArgs = append(turnArgs, v)
		argIdx++
	}
	if v := strings.TrimSpace(r.URL.Query().Get("provider")); v != "" {
		turnFilterActive = true
		turnClauses = append(turnClauses, fmt.Sprintf("t.provider = $%d", argIdx))

		turnArgs = append(turnArgs, v)
		argIdx++
	}
	if v := r.URL.Query().Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			turnFilterActive = true
			turnClauses = append(turnClauses, fmt.Sprintf("t.status_code = $%d", argIdx))

			turnArgs = append(turnArgs, n)
			argIdx++
		}
	}

	turnWhere := strings.Join(turnClauses, " AND ")

	// ⚠ 下面 SELECT 里的 `attempt_no` **尚无写入路径，恒为 0** —— 读它得不到
	// 信息，别据此写「本轮是第 N 次尝试」的判据（那类判据恒真）。
	// 事实校准（2026-10-19 复核）：该列由 456 建为
	// `attempt_no INT NOT NULL DEFAULT 0`，所以它在库里是 **0 而非 NULL**；
	// 投影里的 COALESCE 是冗余的，不是「把 NULL 兜成 0」。
	// 缺的是写入方：turn_writer.go 的 INSERT 列清单里没有 attempt_no，
	// 全仓也无人写它（promote 函数只搬运既有值，同样不产生新值）。
	// 可用真源在 requestjourney 侧（AttemptNo，见
	// domains/requestjourney/attempt_facts.go），尚未接到本写入点。
	query := fmt.Sprintf(`
		SELECT t.session_id, t.turn_no, t.ts,
			COALESCE(t.request_id, '') AS request_id,
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
		FROM public.session_turns_with_current_month t
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
			&sessionID, &it.TurnNo, &it.Ts, &it.RequestID,
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

	// 计算会话级聚合。轮次筛选时 totals 以匹配轮次为准；无轮次筛选时保留 sessions 表的全会话累计值。
	type storedTotals struct {
		turns      int
		tokens     int
		cost       float64
		lastTurnNo *int
		lastModel  *string
		lastProv   *string
	}
	fullTotals := make(map[string]storedTotals, len(sessions))
	for _, g := range sessions {
		fullTotals[g.SessionID] = storedTotals{
			turns: g.TotalTurns, tokens: g.TotalTokens, cost: g.TotalCostUSD,
			lastTurnNo: g.LastTurnNo, lastModel: g.LastModel, lastProv: g.LastProvider,
		}
		ensureTurnsNonNil(g)
		computeSessionAggs(g)
		if !turnFilterActive {
			full := fullTotals[g.SessionID]
			g.TotalTurns = full.turns
			g.TotalTokens = full.tokens
			g.TotalCostUSD = full.cost
			g.LastTurnNo = full.lastTurnNo
			g.LastModel = full.lastModel
			g.LastProvider = full.lastProv
		}
	}

	return nil
}

// turnsSessionsAPIKeyEnrichSQL 构造「每个会话最新一轮的 api_key」回填查询。
//
// ★ 抽成包级函数（2026-10-24 R93）⇒ 门禁能**原样**构造它做「旧形态 vs 新形态」的
// 真库等价性对拍（而不是在测试里抄一份 ⇒ 抄的那份会与生产漂移）。
//
// ★ 形态与代价依据见 db.SessionFamilyLatestTurnPerSessionSQL 的注释。
// ★ tenantPred：额外的行级谓词（如 ` AND t.tenant_id = $2`），使用别名 `t`。
func turnsSessionsAPIKeyEnrichSQL(tenantPred string) string {
	return `
		SELECT DISTINCT ON (x.session_id)
			x.session_id,
			x.api_key_id,
			COALESCE(NULLIF(ak.key_alias, ''), ak.key_prefix, 'key#' || x.api_key_id::text) AS api_key_label
		FROM ` + db.SessionFamilyLatestTurnPerSessionSQL(tenantPred) + ` x
		LEFT JOIN public.api_keys ak ON ak.id = x.api_key_id
		WHERE x.api_key_id IS NOT NULL
		ORDER BY x.session_id, x.turn_no DESC`
}

// turnsSessionsAPIKeyEnrichLegacySQL 是 **R93 之前的旧形态**（保留**仅供门禁对拍**）。
//
// ★ 它刻意保留「视图 ⋈ 全量 history source」⇒ 生产实测同一批行被扫两遍
// ⇒ `?model=` 命中多轮会话时 **13,066 ms** ⇒ 端点 500@30s。
// ⇒ ⚠ **生产代码路径不得再引用它**（admin/turns_sessions_enrich_latest_test.go 的形态门会拦）。
func turnsSessionsAPIKeyEnrichLegacySQL(tenantPred string) string {
	return `
		SELECT DISTINCT ON (t.session_id)
			t.session_id,
			rl.api_key_id,
			COALESCE(NULLIF(ak.key_alias, ''), ak.key_prefix, 'key#' || rl.api_key_id::text) AS api_key_label
		FROM public.session_turns_with_current_month t
		JOIN ` + db.SessionFamilyTurnsForSessionsSQL() + ` rl ON rl.request_id = t.request_id
		LEFT JOIN public.api_keys ak ON ak.id = rl.api_key_id
		WHERE t.session_id = ANY($1)
		  AND rl.api_key_id IS NOT NULL` + tenantPred + `
		ORDER BY t.session_id, t.turn_no DESC`
}

// enrichTurnsSessionMeta 批量回填 api_key 与 handoff 父会话（仅针对当前页会话）。
func (h *Handler) enrichTurnsSessionMeta(ctx context.Context, sessions []*TurnsSessionGroup, tenantID string) error {
	if len(sessions) == 0 {
		return nil
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, g := range sessions {
		sessionIDs = append(sessionIDs, g.SessionID)
	}

	turnTenant := ""
	turnArgs := []any{sessionIDs}
	if tenantID != "" {
		turnTenant = " AND t.tenant_id = $2"
		turnArgs = append(turnArgs, tenantID)
	}

	// 同上：原生源 SQL 含 `%` 字符，不能过 fmt.Sprintf，否则 `%!'(MISSING)`
	// 残渣会污染查询。SQL 用拼接，tenant 片段用 %s 是安全的（它本身无 %）。
	//
	// ★ 2026-10-24（R84）：source 从 SessionFamilyTurnsSourceSQL() 换成
	//   SessionFamilyTurnsForSessionsSQL() —— 唯一差别是**把 session 谓词推进
	//   source 的两条腿内部**（`t.session_id = ANY($1)`）。
	//   原因（252 生产只读实测）：全量 source（hot UNION ALL 父表全部历史分区）
	//   在谓词位于外部时会被整体物化再 Hash Join ⇒ 估算 1,587,817 行 ⇒
	//   该腿每次调用 20,176 ms（pg_stat_statements 增量差分）⇒ 端点 21s。
	//   单变量 A/B（同 21 个真实 session_id）：11,478.486 ms → **6.369 ms**。
	//   JOIN 条件（rl.request_id = t.request_id）不变 ⇒ 入选行集合不变。
	//
	// ★★ 2026-10-24（R93）：**本条查询才是 `?model=` 500@30s 的真凶**
	//   （R90/R92 修的是**列表查询**，而 500 一直由本条 enrich 查询造成
	//   —— 生产 journal：`admin handleTurnsSessions enrich meta failed`）。
	//   旧形态把**同一批行扫两遍**再 1:1 join：
	//     · `public.session_turns_with_current_month` 的父表腿实测是 `FROM session_turns`
	//       （**全部分区**，名字骗人）⇒ 视图 = hot ∪ 全部历史
	//       ⇒ 视图侧 `Heap Fetches 86,507`；
	//     · source 侧同样是那 86,507 行（113 列投影）；
	//     · 两者按 `request_id` 1:1 join ⇒ **join 冗余**。
	//   ⇒ 命中 `?model=` 时那 21 个**探针会话**在 2026_09 有 **85,712 轮**
	//     ⇒ 实测 **13,066 ms**（无过滤批次只有 21×1 轮 ⇒ 23 ms）。
	//   ⇒ 改成 `db.SessionFamilyLatestTurnPerSessionSQL()`：**每个 source 先
	//     `GROUP BY session_id + max(turn_no)`（index-only，不回表）再回定位那一行**
	//     ⇒ 实测同批次 **1,339 ms**（**9.8×**），口径经生产三批次双向 EXCEPT ALL 证明等价。
	//   ⇒ ★ 旧的 `SessionFamilyTurnsForSessionsSQL()` **仍保留**（别处仍在用，
	//     且新门禁要用它来构造「旧形态」做等价性对拍）。
	apiKeyQuery := turnsSessionsAPIKeyEnrichSQL(turnTenant)

	rows, err := h.db.Query(ctx, apiKeyQuery, turnArgs...)
	if err != nil {
		return fmt.Errorf("batch api_key enrich failed: %w", err)
	}
	bySession := make(map[string]*TurnsSessionGroup, len(sessions))
	for _, g := range sessions {
		bySession[g.SessionID] = g
	}
	for rows.Next() {
		var sessionID string
		var keyID int64
		var label string
		if err := rows.Scan(&sessionID, &keyID, &label); err != nil {
			rows.Close()
			return fmt.Errorf("scan api_key enrich failed: %w", err)
		}
		if g := bySession[sessionID]; g != nil {
			g.APIKeyID = &keyID
			g.APIKeyLabel = &label
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate api_key enrich failed: %w", err)
	}

	handoffQuery := `
		SELECT DISTINCT ON (hl.new_session_id)
			hl.new_session_id,
			hl.session_id AS parent_session_id,
			hl.trigger_reason
		FROM public.handoff_logs_with_current_month hl
		WHERE hl.new_session_id = ANY($1)
		ORDER BY hl.new_session_id, hl.created_at DESC`
	hRows, err := h.db.Query(ctx, handoffQuery, sessionIDs)
	if err != nil {
		return fmt.Errorf("batch handoff enrich failed: %w", err)
	}
	defer hRows.Close()
	for hRows.Next() {
		var newSessionID, parentSessionID, triggerReason string
		if err := hRows.Scan(&newSessionID, &parentSessionID, &triggerReason); err != nil {
			return fmt.Errorf("scan handoff enrich failed: %w", err)
		}
		if g := bySession[newSessionID]; g != nil {
			g.ParentSessionID = &parentSessionID
			_ = triggerReason // applySessionParent sets relation=handoff when ParentSessionID set
		}
	}
	if err := hRows.Err(); err != nil {
		return fmt.Errorf("iterate handoff enrich failed: %w", err)
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
	g.TotalTurns = len(g.Turns)
	g.TotalTokens = 0
	g.TotalCostUSD = 0
	g.LastTurnNo = nil
	g.LastModel = nil
	g.LastProvider = nil
	g.ModelsUsed = []string{}
	g.FailoverCount = 0
	g.ErrorCount = 0
	g.DurationMs = 0
	g.Compression = TurnsCompressionAgg{Strategies: []string{}}

	modelSeen := map[string]bool{}
	strategySeen := map[string]bool{}
	var firstTS, lastTS time.Time
	for _, t := range g.Turns {
		g.TotalTokens += t.RequestTokens + t.ResponseTokens
		g.TotalCostUSD += t.CostUSD
		if g.LastTurnNo == nil || t.TurnNo > *g.LastTurnNo {
			turnNo := t.TurnNo
			g.LastTurnNo = &turnNo
			model := t.Model
			provider := t.Provider
			g.LastModel = &model
			g.LastProvider = &provider
		}
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
