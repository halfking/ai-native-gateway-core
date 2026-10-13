// Package admin — data_lifecycle_blobs.go
//
// 2026-07-01: 大字段 / Blob 管理端点。
// 业务背景：
//   llm-gateway-go 并不在本地磁盘存"附件文件"，但 request_logs 表的
//   request_body 和 outbound_body (JSONB) 在多模态/工具调用场景下
//   会塞入 base64 图片、文件内容、长上下文等"附件型"大对象。
//   仓库的 owner 把这些大字段也叫做"附件"，并希望和磁盘配额一起
//   治理（按大小/年龄清理、Top-N 占用排行、按策略轮转）。
//
// 端点（详见 handler.go 路由注册）：
//   GET  /api/admin/data-lifecycle/blobs/top?limit=20
//   POST /api/admin/data-lifecycle/blobs/cleanup/preview
//   POST /api/admin/data-lifecycle/blobs/cleanup/execute   (super_admin)
//
// 设计原则：
//   - 不删除行，只把 request_body / outbound_body 置 NULL（保留元数据）
//   - preview / execute 分两步，防止误操作
//   - 复用 pg_column_size 拿到真实占用（已 TOAST 压缩后的字节数）

package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// dataLifecycleBodiesBytesSource 是 bodies 族的**跨族按量**源：v1 ∪ 会话，
// 并按 `request_id` 去重（S4 停写轮新增）。
//
// # 为什么这里不能照抄 compression_sessions 的 EXISTS 形态
//
// 那边判的是「有没有」，EXISTS 就够；这里要的是 **`pg_column_size` 的字节数**，
// 必须把列值本身取出来 ⇒ 只能 JOIN ⇒ 于是撞上镜像双生：停写前双写期同一个
// `request_id` 在 `request_logs_bodies*` 与 `session_bodies*` 里**同时存在**
// （本地库实测 11,711 个）。裸 UNION ALL 后按 `request_id` 连接是一对多，
// `SUM(pg_column_size(...))` 会**把双生算两遍**。
//
// ⇒ 用 `DISTINCT ON (request_id)` 收敛到一行，排序键表达「挑哪一份副本」：
// 先挑**有 outbound_body** 的，再挑**有 request_body** 的，最后挑会话副本
// （会话族是停写后的权威源）。三键缺一不可：只按族排序会让「会话副本的
// request_delta 是 jsonb-null」压掉「v1 副本有正文」的情况（AGENTS.md 登记的
// 非流式 chat 终态 entry 不载请求体）。
//
// # 停写前后的失真实测（本地库，2026-10-14）
//
//	口径			合计字节
//	修复前（v1 only）	 13,922,642  ≈ 13.9 MB
//	修复后（跨族去重）	100,913,596  ≈ 100.9 MB
//	（会话族单独		 95,905,868  ≈ 95.9 MB）
//
// ⇒ 低报 **7.2 倍**。后果不是"排行榜不好看"：`/blobs/cleanup/preview` 是运维
// 决定「要不要花钱做清理」的依据，它按同一口径报 `EstimatedFreedBytes`
// ⇒ **少报 86 MB 的可释放字节**，而停写后的新正文在这个面板上**一律 0 字节**。
var dataLifecycleBodiesBytesSource = `SELECT DISTINCT ON (request_id) request_id,
       pg_column_size(request_body) AS request_body_bytes,
       pg_column_size(outbound_body) AS outbound_body_bytes,
       (request_body IS NOT NULL AND request_body <> 'null'::jsonb) AS has_request_body,
       (outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS has_outbound_body
  FROM (
	SELECT request_id, request_body, outbound_body, 0 AS fam FROM request_logs_bodies_with_current_month
	UNION ALL
	SELECT request_id, request_delta AS request_body, outbound_body, 1 AS fam FROM public.session_bodies_hot
	UNION ALL
	SELECT request_id, request_delta AS request_body, outbound_body, 1 AS fam FROM public.session_bodies
  ) u
 ORDER BY request_id, (outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) DESC,
                     (request_body IS NOT NULL AND request_body <> 'null'::jsonb) DESC, fam DESC`

// dataLifecycleBlobCleanupPreviewSQL 提为包级函数，理由与
// dataLifecycleTrendSQL 相同：判据必须钉**消费方实际发出去的那一句**。
//
// 第一版判据钉的是「字节源投影了 has_request_body / has_outbound_body」，
// 结果把预览的 FILTER 改回 `rb.request_body_bytes IS NOT NULL` 判据依然全绿
// —— 投影还在，只是没人用它（变异验证 M5 实测假绿）。
//
// 字节数（SUM）与行数（COUNT FILTER）在这里**故意用不同的判据**：
// 字节数回答「能省多少空间」，jsonb `null` 占 4 字节、置 SQL NULL 确实省；
// 行数回答「有多少行的请求体会变成 NULL」，而 jsonb `null` 本来就已经是
// NULL，算进去会让 affected 与 v1 口径不等价（v1 侧 jsonb-null 实测 0 行）。
func dataLifecycleBlobCleanupPreviewSQL(where string) string {
	return `
		SELECT
			COUNT(*) FILTER (WHERE rb.has_request_body),
			COUNT(*) FILTER (WHERE rb.has_outbound_body),
			COALESCE(SUM(COALESCE(rb.request_body_bytes,0) + COALESCE(rb.outbound_body_bytes,0)), 0)::bigint
		FROM request_logs_with_current_month rl
		LEFT JOIN (` + dataLifecycleBodiesBytesSource + `) rb ON rb.request_id = rl.request_id
		` + where
}

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
		where = "WHERE tenant_id = $1"
		args = append(args, GetTenantID(r))
	}

	rows, err := h.db.Query(ctx, `
		SELECT
			rl.request_id,
			COALESCE(rl.gw_session_id, ''),
			COALESCE(rl.tenant_id, ''),
			rl.ts,
			COALESCE(pg_column_size(rb.request_body), 0),
			COALESCE(pg_column_size(rb.outbound_body), 0),
			COALESCE(rl.outbound_model, '')
		FROM request_logs_with_current_month rl
		LEFT JOIN (`+dataLifecycleBodiesBytesSource+`) rb ON rb.request_id = rl.request_id
		`+where+`
		ORDER BY (COALESCE(rb.request_body_bytes,0) + COALESCE(rb.outbound_body_bytes,0)) DESC
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

	// 构造 WHERE
	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1
	if isTenantAdmin || scope == "current" {
		where += " AND rl.tenant_id = $" + strconv.Itoa(argIdx)
		args = append(args, GetTenantID(r))
		argIdx++
	}
	if req.OlderThanDays > 0 {
		where += " AND rl.ts < NOW() - ($" + strconv.Itoa(argIdx) + " || ' days')::interval"
		args = append(args, strconv.Itoa(req.OlderThanDays))
		argIdx++
	}

	if req.LargerThanKB > 0 {
		where += " AND (pg_column_size(rb.request_body) > $" + strconv.Itoa(argIdx) +
			" * 1024 OR pg_column_size(rb.outbound_body) > $" + strconv.Itoa(argIdx) + " * 1024)"
		args = append(args, req.LargerThanKB)
		argIdx++
	}

	startedAt := time.Now().UTC()
	resp := blobCleanupResponse{
		StartedAt: startedAt.Format(time.RFC3339),
		Executed:  execute,
	}

	// 1. 估算（两个 SELECT）
	var reqAffected, outAffected int64
	var freedBytes int64
	err := h.db.QueryRow(ctx, dataLifecycleBlobCleanupPreviewSQL(where), args...).
		Scan(&reqAffected, &outAffected, &freedBytes)
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
		// 2026-07-05 migration 341: UPDATE targets request_logs_hot (独立热表)。
		// Blob 清理仅针对热表中的 0-7 天数据，已迁移到月度分区的数据不受影响。
		_, err := h.db.Exec(ctx, `
				UPDATE request_logs_bodies_hot rb
				SET request_body = NULL,
				    outbound_body = NULL
				FROM request_logs_hot rl
				`+where+`
				  AND rb.request_id = rl.request_id`, args...)

		if err != nil {
			slog.Error("blob cleanup execute failed", "error", err)
			writeInternalErr(w, "执行失败", err)
			return
		}
		// VACUUM 释放 request body 热表空间。
		_, _ = h.db.Exec(ctx, `VACUUM (VERBOSE, ANALYZE) request_logs_bodies_hot`)

	}

	resp.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, resp)
}
