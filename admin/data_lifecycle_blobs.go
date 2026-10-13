// Package admin — data_lifecycle_blobs.go
//
// 2026-07-01: 大字段 / Blob 管理端点。
// 业务背景：
//   llm-gateway-go 并不在本地磁盘存"附件文件"，但会话族 bodies 表的
//   outbound_body 和 request_attachments/response_attachments (JSONB)
//   在多模态/工具调用场景下会塞入 base64 图片、文件内容、长上下文等
//   "附件型"大对象。仓库的 owner 把这些大字段也叫做"附件"，并希望和
//   磁盘配额一起治理（按大小/年龄清理、Top-N 占用排行、按策略轮转）。
//
// 端点（详见 handler.go 路由注册）：
//   GET  /api/admin/data-lifecycle/blobs/top?limit=20
//   POST /api/admin/data-lifecycle/blobs/cleanup/preview
//   POST /api/admin/data-lifecycle/blobs/cleanup/execute   (super_admin)
//
// 设计原则：
//   - 不删除行，只把大字段置 NULL（保留元数据与会话增量正文）
//   - preview / execute 分两步，防止误操作
//   - 复用 pg_column_size 拿到真实占用（已 TOAST 压缩后的字节数）
//
// 2026-10-13 S4 波 4 补记：治理对象会话族化（退役门登记的
// 「DROP 前必须落地」项）。v1 时代治理 request_logs_bodies 族
//（request_body/outbound_body）；停写后新正文只落 session_bodies 族，
// 大对象真身是 **outbound_body + request_attachments/response_attachments**
//（request_delta/response_delta 按设计是小增量，清理时刻意保留——
// 这比 v1 语义保留得更多：v1 把 request_body 置 NULL 连对话内容一起丢）。
// 字段映射（API 契约不变）：RequestBodyBytes =
// pg_column_size(request_delta) + 两 attachments；OutboundBodyBytes =
// pg_column_size(outbound_body)。preview/Top-N 读 hot∪父表全量，
// execute 只清 session_bodies_hot 热窗（与 v1 的「只治热表 0-7 天」
// 设计一致，已 promote 到月分区的数据不动）。

package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// blobRow 单行"附件"（大字段）信息
type blobRow struct {
	RequestID         string `json:"request_id"`
	SessionKey        string `json:"session_key"`
	TenantID          string `json:"tenant_id"`
	OccurredAt        string `json:"occurred_at"`
	RequestBodyBytes  int64  `json:"request_body_bytes"`
	OutboundBodyBytes int64  `json:"outbound_body_bytes"`
	TotalBytes        int64  `json:"total_bytes"`
	TotalHuman        string `json:"total_human"`
	Model             string `json:"model,omitempty"`
}

// blobTopResponse Top-N 大字段响应
type blobTopResponse struct {
	Rows        []blobRow `json:"rows"`
	TotalBytes  int64     `json:"total_bytes"`
	TotalHuman  string    `json:"total_human"`
	CollectedAt time.Time `json:"collected_at"`
}

// blobCleanupRequest 清理请求体
type blobCleanupRequest struct {
	// 任一条件满足即清理（AND 关系）
	OlderThanDays int `json:"older_than_days,omitempty"` // 超过 N 天的
	LargerThanKB  int `json:"larger_than_kb,omitempty"`  // 单字段超过 N KB 的
	// 限定租户（空 = 全部；super_admin 默认走全量；tenant_admin 强制只清自己）
	Scope string `json:"scope,omitempty"` // "all" | "current" （默认 current）
}

// blobCleanupResponse 预览 / 执行响应
type blobCleanupResponse struct {
	AffectedRows        int64  `json:"affected_rows"`
	RequestBodyAffected int64  `json:"request_body_affected"`
	OutboundAffected    int64  `json:"outbound_affected"`
	EstimatedFreedBytes int64  `json:"estimated_freed_bytes"`
	EstimatedFreedHuman string `json:"estimated_freed_human"`
	Executed            bool   `json:"executed"`
	WarningMessage      string `json:"warning_message,omitempty"`
	StartedAt           string `json:"started_at"`
	FinishedAt          string `json:"finished_at,omitempty"`
}

// sessionBodiesGovernanceSource 是 blob 治理读侧（Top-N/preview）共用的
// 会话族 bodies 源：hot∪父表、全列投影（switch 层只投影 4 列合同，不含
// attachments——治理对象恰恰是 attachments，故本地持有一份源）。
// hot 与父表 request_id 交集为 0（promote 排空 hot，db 切换层注释同款实测）。
var sessionBodiesGovernanceSource = `
	FROM (
		SELECT request_id, session_id, tenant_id, ts, outbound_body,
		       request_delta, request_attachments, response_attachments
		FROM session_bodies_hot
		UNION ALL
		SELECT request_id, session_id, tenant_id, ts, outbound_body,
		       request_delta, request_attachments, response_attachments
		FROM session_bodies
	) sb
`

// blob 治理的尺寸口径（API 字段映射见文件头）：
// 入站侧 = 增量正文 + 双 attachments；出站侧 = 完整上游请求对象。
// Top-N 展示、preview 估算、execute 的 LargerThanKB 谓词共用同一份，
// 三处口径不允许各自漂移。
const blobInboundSizeExpr = `(COALESCE(pg_column_size(sb.request_delta),0) + COALESCE(pg_column_size(sb.request_attachments),0) + COALESCE(pg_column_size(sb.response_attachments),0))`
const blobOutboundSizeExpr = `(COALESCE(pg_column_size(sb.outbound_body),0))`

// handleDataLifecycleBlobTop GET /api/admin/data-lifecycle/blobs/top
func (h *Handler) handleDataLifecycleBlobTop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	isTenantAdmin := IsTenantAdmin(r)
	where := ""
	args := []interface{}{}
	if isTenantAdmin {
		where = "WHERE sb.tenant_id = $1"
		args = append(args, GetTenantID(r))
	}

	rows, err := h.db.Query(ctx, `
		SELECT
			sb.request_id,
			sb.session_id,
			sb.tenant_id,
			sb.ts,
			`+blobInboundSizeExpr+`,
			`+blobOutboundSizeExpr+`,
			COALESCE(t.model, '')
		`+sessionBodiesGovernanceSource+`
		LEFT JOIN (
			SELECT request_id, model FROM session_turns_hot
			UNION ALL
			SELECT request_id, model FROM session_turns
		) t ON t.request_id = sb.request_id
		`+where+`
		ORDER BY (`+blobInboundSizeExpr+` + `+blobOutboundSizeExpr+`) DESC
		LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		slog.Warn("blobs top query failed", "error", err)
		writeInternalErr(w, "查询失败", err)
		return
	}
	defer rows.Close()

	out := make([]blobRow, 0, limit)
	var totalBytes int64
	for rows.Next() {
		var b blobRow
		var ts time.Time
		if err := rows.Scan(
			&b.RequestID, &b.SessionKey, &b.TenantID, &ts,
			&b.RequestBodyBytes, &b.OutboundBodyBytes, &b.Model,
		); err != nil {
			warnRowSkip("dataLifecycle.blobs.top", err)
			continue
		}
		b.OccurredAt = ts.UTC().Format(time.RFC3339)
		b.TotalBytes = b.RequestBodyBytes + b.OutboundBodyBytes
		b.TotalHuman = humanBytes(b.TotalBytes)
		totalBytes += b.TotalBytes
		out = append(out, b)
	}
	// Top-N blob 清单的截断会让运维漏掉最大的存储占用行。
	if writeAggRowsErr(w, "dataLifecycle.blobs.top", rows.Err()) {
		return
	}

	writeJSON(w, http.StatusOK, blobTopResponse{
		Rows:        out,
		TotalBytes:  totalBytes,
		TotalHuman:  humanBytes(totalBytes),
		CollectedAt: time.Now().UTC(),
	})
}

// handleDataLifecycleBlobCleanupPreview POST /api/admin/data-lifecycle/blobs/cleanup/preview
func (h *Handler) handleDataLifecycleBlobCleanupPreview(w http.ResponseWriter, r *http.Request) {
	h.handleBlobCleanup(w, r, false)
}

// handleDataLifecycleBlobCleanupExecute POST /api/admin/data-lifecycle/blobs/cleanup/execute
func (h *Handler) handleDataLifecycleBlobCleanupExecute(w http.ResponseWriter, r *http.Request) {
	h.handleBlobCleanup(w, r, true)
}

func (h *Handler) handleBlobCleanup(w http.ResponseWriter, r *http.Request, execute bool) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req blobCleanupRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OlderThanDays <= 0 && req.LargerThanKB <= 0 {
		writeError(w, http.StatusBadRequest, "请至少指定 older_than_days 或 larger_than_kb 之一")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	isTenantAdmin := IsTenantAdmin(r)
	scope := req.Scope
	if scope == "" {
		scope = "current"
	}
	if isTenantAdmin && scope == "all" {
		writeError(w, http.StatusForbidden, "租户管理员只能清理自己租户的数据")
		return
	}

	// 构造 WHERE（谓词全部落在 sb 别名上——preview 读 hot∪父表子查询与
	// execute 直接 UPDATE session_bodies_hot 共用同一份片段）
	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1
	if isTenantAdmin || scope == "current" {
		where += " AND sb.tenant_id = $" + strconv.Itoa(argIdx)
		args = append(args, GetTenantID(r))
		argIdx++
	}
	if req.OlderThanDays > 0 {
		where += " AND sb.ts < NOW() - ($" + strconv.Itoa(argIdx) + " || ' days')::interval"
		args = append(args, strconv.Itoa(req.OlderThanDays))
		argIdx++
	}

	if req.LargerThanKB > 0 {
		where += " AND (pg_column_size(sb.outbound_body) > $" + strconv.Itoa(argIdx) +
			" * 1024 OR pg_column_size(sb.request_attachments) > $" + strconv.Itoa(argIdx) +
			" * 1024 OR pg_column_size(sb.response_attachments) > $" + strconv.Itoa(argIdx) + " * 1024)"
		args = append(args, req.LargerThanKB)
		argIdx++
	}

	startedAt := time.Now().UTC()
	resp := blobCleanupResponse{
		StartedAt: startedAt.Format(time.RFC3339),
		Executed:  execute,
	}

	// 1. 估算（读 hot∪父表全量——含已 promote 的月分区行；execute 只清
	//    热窗，两者差集 = 已归档分区里的大对象，与 v1 时代的口径一致）
	var reqAffected, outAffected int64
	var freedBytes int64
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE sb.request_delta IS NOT NULL OR sb.request_attachments IS NOT NULL OR sb.response_attachments IS NOT NULL),
			COUNT(*) FILTER (WHERE sb.outbound_body IS NOT NULL),
			COALESCE(SUM(`+blobInboundSizeExpr+` + `+blobOutboundSizeExpr+`), 0)::bigint
		`+sessionBodiesGovernanceSource+`
		`+where, args...).Scan(&reqAffected, &outAffected, &freedBytes)
	if err != nil {
		slog.Warn("blob cleanup preview failed", "error", err)
		writeInternalErr(w, "预览失败", err)
		return
	}
	resp.RequestBodyAffected = reqAffected
	resp.OutboundAffected = outAffected
	resp.EstimatedFreedBytes = freedBytes
	resp.EstimatedFreedHuman = humanBytes(freedBytes)
	resp.AffectedRows = reqAffected + outAffected

	if resp.AffectedRows == 0 {
		resp.WarningMessage = "没有匹配的大字段需要清理"
	} else if resp.AffectedRows > 500000 {
		resp.WarningMessage = "影响行数 > 50 万，建议分批清理（按月/按租户分片）"
	}

	// 2. 执行（仅 super_admin）
	if execute {
		// S4 波 4（2026-10-13）：UPDATE 目标改 session_bodies_hot（v1 热表
		// 停写后恒空，治理已无对象）。语义与 v1 一致：只清热窗，已 promote
		// 到月分区的数据不动；比 v1 保留更多——增量正文（request_delta/
		// response_delta）不置 NULL，对话历史对导出/对比仍可见。
		_, err := h.db.Exec(ctx, `
				UPDATE session_bodies_hot sb
				SET outbound_body = NULL,
				    request_attachments = NULL,
				    response_attachments = NULL
				`+where, args...)

		if err != nil {
			slog.Error("blob cleanup execute failed", "error", err)
			writeInternalErr(w, "执行失败", err)
			return
		}
		// VACUUM 释放会话族 bodies 热表空间。
		_, _ = h.db.Exec(ctx, `VACUUM (VERBOSE, ANALYZE) session_bodies_hot`)
	}

	resp.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, resp)
}
