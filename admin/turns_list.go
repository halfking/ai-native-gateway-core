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
//       &project_id=...    按项目筛选
//       &namespace=...     按会话命名空间筛选
//       &parent_request_id=... 按派生父请求筛选
//       &task_type=...     按任务类型筛选
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

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
	"github.com/kaixuan/llm-gateway-go/internal/sessionfamily"
)

// TurnInList 是跨会话轮次列表的单行记录，继承 TurnListItem 的所有字段，
// 额外携带 session_id 以便前端跳转到会话详情页。
type TurnInList struct {
	TurnListItem
	SessionID       string `json:"session_id"`
	ProjectID       string `json:"project_id,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
	ParentRequestID string `json:"parent_request_id,omitempty"`
	TaskType        string `json:"task_type,omitempty"`
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
	// 构造两腿 FROM 源（★ R181 轮：列表读点从「视图形态」换成【两腿形态】）。
	//
	// ★ 视图（public.session_turns_with_current_month）的 hot 臂带一个相关反连接，
	// ★ 而相关子查询的**内侧拿不到外层谓词** ⇒ ⇒ 内侧 Append 遍历**全部**月分区、
	// ★ 每个分区按租户索引定位后扫该租户的全部索引项 ⇒「每 hot 行扫该租户全部索引项」。
	// ★ 两腿形态把分区窗口推进【反连接的内侧】⇒ 内侧裁到 1 个分区、反连接从
	// ★「每 hot 行一次嵌套循环」变成「一次性扫描」⇒ 本地真库实测 buffers 360 → 128。
	// ★ 等价性与前提（跨分区同键不可能由正常路径产生）见
	// ★ internal/sessionfamily/turns_reads.go 的 TurnsListArmsSQL 头注；
	// ★ 前提由真库门 TestTurnsListTwoLegs_NoCrossPartitionDuplicateKeys 持续检验。
	arms, args, argIdx := buildTurnsListArms(r, tenantID, tsFrom, tsTo, beforeTS, beforeSessionID, beforeNo)

	// 查询
	query := fmt.Sprintf(`
		SELECT t.session_id, t.turn_no, t.ts,
			COALESCE(t.project_id, '') AS project_id,
			COALESCE(t.namespace, '') AS namespace,
			COALESCE(t.parent_request_id, '') AS parent_request_id,
			COALESCE(t.task_type, '') AS task_type,
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
			COALESCE(t.attachment_count, 0) AS attachment_count,
			t.digest
		FROM %s t
		ORDER BY t.ts DESC, t.session_id DESC, t.turn_no DESC
		LIMIT $%d
	`, arms, argIdx)
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
		var (
			it                 TurnInList
			persistedDigestRaw []byte
		)
		if err := rows.Scan(
			&it.SessionID, &it.TurnNo, &it.Ts,
			&it.ProjectID, &it.Namespace, &it.ParentRequestID, &it.TaskType,
			&it.Title, &it.Summary,
			&it.RequestTokens, &it.ResponseTokens, &it.CostUSD,
			&it.Model, &it.Provider, &it.StatusCode,
			&it.SubmitMode, &it.InjectionVerdict, &it.OutputVerdict,
			&it.AttachmentCount, &persistedDigestRaw,
		); err != nil {
			slog.Warn("admin handleTurnsList scan failed", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "scan turn failed")
			return
		}
		// Persisted-digest only: this endpoint deliberately does not join
		// session_bodies_unified (that's the whole point of avoiding the
		// cross-session body-read cost), so unlike the per-session
		// list/detail endpoints there is no live-reconstruction fallback.
		// Turns written before migration 636, or with a digest that fails
		// to unmarshal, surface with Digest == nil rather than a body read.
		if persisted, err := sessiondigest.Unmarshal(persistedDigestRaw); err == nil && persisted != nil {
			it.Digest = turnDigestFromPayload(persisted.Payload)
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

// buildTurnsListWhere constructs the WHERE clause for /api/admin/turns from
// query parameters. It is extracted so unit tests can pin the exact SQL
// contract (column names, parameter positions) without spinning up the
// handler and a live database.
// buildTurnsListWhere constructs the WHERE clause for /api/admin/turns from
// query parameters. It is extracted so unit tests can pin the exact SQL
// contract (column names, parameter positions) without spinning up the
// handler and a live database.
//
// ★ 2026-10-29：★ 本函数现在是「两腿形态」的**谓词渲染器**——★ 同一段谓词要
// ★ 分别渲染成 hot 腿（别名 hot）与父表腿（别名 p）⇒ ★ 别名由
// ★ buildTurnsListWhereFor 显式传入（★ 不是对成品 SQL 做字符串替换 ⇒ ★ 替换
// ★ 会踩到 "hot." 里也含 "t." 的坑）。
func buildTurnsListWhere(r *http.Request, tenantID string, tsFrom, tsTo, beforeTS time.Time, beforeSessionID string, beforeTurnNo int) (string, []any, int) {
	return buildTurnsListWhereFor(r, tenantID, tsFrom, tsTo, beforeTS, beforeSessionID, beforeTurnNo, "t")
}

// buildTurnsListWhereFor 是 buildTurnsListWhere 的**别名参数化**版本。
//
// ★★ 为什么要别名化（★ R181 轮两腿形态）：★★
//
//	★ 两腿形态把谓词**推进两臂内部**⇒ ★ hot 腿与父表腿的列引用前缀不同
//	★ （hot. / p.）⇒ ★ 同一段谓词要渲染两遍 ⇒ ★ 参数编号必须**逐字一致**
//	★ （两臂共用同一批 $N）⇒ ★ 因此渲染器只换别名、不换编号。
//
// ★★ 参数编号（★ 不许重排：★ 既有测试钉死了这个契约）：★★
//
//	★ $1/$2 = partition_date 上下界　★ $3/$4 = ts 窗口　★ $5… = 过滤条件
func buildTurnsListWhereFor(r *http.Request, tenantID string, tsFrom, tsTo, beforeTS time.Time, beforeSessionID string, beforeTurnNo int, alias string) (string, []any, int) {
	q := r.URL.Query()
	c := alias + "."
	// ★★ R181 轮：★ 带上【分区键下界】⇒ ⇒ PG 能对按 partition_date 分区的
	// session_turns 做月分区裁剪（只按 ts 过滤 ⇒ 一个都裁不掉 ⇒ 窗口内
	// 每个月分区都要探一遍）。
	// ★★ 口径不变：★ partition_date = calendarDate(ts)（所有写入方都用它）
	// ⇒ ⇒ 该谓词是 ts 谓词的蕴含条件 ⇒ ⇒ **不丢行**；精确过滤仍由 ts 做。
	// ★★ 参数编号后移两位：★ $1/$2 = partition_date 上下界，★ $3/$4 = ts 窗口，
	// ★ $5… = 过滤条件。
	pdFrom, pdTo := sessionPartitionDateBounds(tsFrom, tsTo)
	clauses := []string{c + "ts >= $3", c + "ts <= $4", c + "partition_date >= $1", c + "partition_date <= $2"}
	args := []any{pdFrom, pdTo, tsFrom, tsTo}
	argIdx := 5

	if tenantID != "" {
		clauses = append(clauses, fmt.Sprintf(c+"tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}

	if !beforeTS.IsZero() {
		clauses = append(clauses, fmt.Sprintf(
			"("+c+"ts, "+c+"session_id, "+c+"turn_no) < ($%d, $%d, $%d)", argIdx, argIdx+1, argIdx+2))
		args = append(args, beforeTS, beforeSessionID, beforeTurnNo)
		argIdx += 3
	}

	if v := strings.TrimSpace(q.Get("model")); v != "" {
		clauses = append(clauses, fmt.Sprintf(c+"model = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := strings.TrimSpace(q.Get("provider")); v != "" {
		clauses = append(clauses, fmt.Sprintf(c+"provider = $%d", argIdx))
		args = append(args, v)
		argIdx++
	}
	if v := q.Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clauses = append(clauses, fmt.Sprintf(c+"status_code = $%d", argIdx))
			args = append(args, n)
			argIdx++
		}
	}
	for _, filter := range []struct {
		queryKey string
		column   string
	}{
		{queryKey: "project_id", column: "project_id"},
		{queryKey: "namespace", column: "namespace"},
		{queryKey: "parent_request_id", column: "parent_request_id"},
		{queryKey: "task_type", column: "task_type"},
	} {
		if v := strings.TrimSpace(q.Get(filter.queryKey)); v != "" {
			clauses = append(clauses, fmt.Sprintf(c+"%s = $%d", filter.column, argIdx))
			args = append(args, v)
			argIdx++
		}
	}

	return strings.Join(clauses, " AND "), args, argIdx
}

// turnsListArmColumns 是两臂投影的列清单（23 列）。
//
// ★★ 投影必须**自带过滤列与排序键**（★ R172 实测踩到：★ WHERE 引用了投影里
// ★ 没有的列 ⇒ PG 报 42703）：★ tenant_id / request_id（反连接用）、
// ★ partition_date（分区界用）、session_id / turn_no / ts（游标与排序用）
// ★ 一并列出。
//
// ★ 其余 20 列 = 列表端点要读的值（★ 与改前的 SELECT 列表逐字同序 ⇒
// ★ 读到的值逐字相同）。
var turnsListArmColumns = strings.Join([]string{
	"session_id", "turn_no", "ts",
	"project_id", "namespace", "parent_request_id", "task_type",
	"title", "summary",
	"prompt_tokens", "completion_tokens", "cost_usd",
	"model", "provider", "status_code", "submit_mode",
	"injection_verdict", "output_verdict", "attachment_count", "digest",
	"tenant_id", "request_id", "partition_date",
}, ", ")

// buildTurnsListArms 返回列表读点的两臂 FROM 源（★ R181 轮）。
//
// ★ 两臂的谓词**逐字同构、只有别名不同**（★ 参数编号完全一致 ⇒ 共用同一批 $N）。
// ★ 反连接的内侧带 partition_date 界（★ 等价性论证与前提见
// ★ internal/sessionfamily/turns_reads.go 的 TurnsListArmsSQL 头注）。
func buildTurnsListArms(r *http.Request, tenantID string, tsFrom, tsTo, beforeTS time.Time, beforeSessionID string, beforeTurnNo int) (string, []any, int) {
	hotPred, args, argIdx := buildTurnsListWhereFor(r, tenantID, tsFrom, tsTo, beforeTS, beforeSessionID, beforeTurnNo, "hot")
	parentPred, _, _ := buildTurnsListWhereFor(r, tenantID, tsFrom, tsTo, beforeTS, beforeSessionID, beforeTurnNo, "p")
	return sessionfamily.TurnsListArmsSQL(turnsListArmColumns, hotPred, parentPred, "$1", "$2"), args, argIdx
}
