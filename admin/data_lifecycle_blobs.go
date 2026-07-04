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
			request_id,
			COALESCE(gw_session_id, ''),
			COALESCE(tenant_id, ''),
			ts,
			COALESCE(pg_column_size(request_body), 0),
			COALESCE(pg_column_size(outbound_body), 0),
			COALESCE(outbound_model, '')
		FROM request_logs
		`+where+`
		ORDER BY (COALESCE(pg_column_size(request_body),0) + COALESCE(pg_column_size(outbound_body),0)) DESC
		LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		slog.Warn("blobs top query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "查询失败: "+err.Error())
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
			continue
		}
		b.OccurredAt = ts.UTC().Format(time.RFC3339)
		b.TotalBytes = b.RequestBodyBytes + b.OutboundBodyBytes
		b.TotalHuman = humanBytes(b.TotalBytes)
		totalBytes += b.TotalBytes
		out = append(out, b)
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
		where += " AND tenant_id = $" + strconv.Itoa(argIdx)
		args = append(args, GetTenantID(r))
		argIdx++
	}
	if req.OlderThanDays > 0 {
		where += " AND ts < NOW() - ($" + strconv.Itoa(argIdx) + " || ' days')::interval"
		args = append(args, strconv.Itoa(req.OlderThanDays))
		argIdx++
	}
	if req.LargerThanKB > 0 {
		where += " AND (pg_column_size(request_body) > $" + strconv.Itoa(argIdx) +
			" * 1024 OR pg_column_size(outbound_body) > $" + strconv.Itoa(argIdx) + " * 1024)"
		args = append(args, req.LargerThanKB)
	}

	startedAt := time.Now().UTC()
	resp := blobCleanupResponse{
		StartedAt: startedAt.Format(time.RFC3339),
		Executed:  execute,
	}

	// 1. 估算（两个 SELECT）
	var reqAffected, outAffected int64
	var freedBytes int64
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE request_body IS NOT NULL),
			COUNT(*) FILTER (WHERE outbound_body IS NOT NULL),
			COALESCE(SUM(COALESCE(pg_column_size(request_body),0) + COALESCE(pg_column_size(outbound_body),0)), 0)::bigint
		FROM request_logs
		`+where, args...).Scan(&reqAffected, &outAffected, &freedBytes)
	if err != nil {
		slog.Warn("blob cleanup preview failed", "error", err)
		writeError(w, http.StatusInternalServerError, "预览失败: "+err.Error())
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
			UPDATE request_logs_hot
			SET request_body = NULL,
			    outbound_body = NULL
			`+where, args...)
		if err != nil {
			slog.Error("blob cleanup execute failed", "error", err)
			writeError(w, http.StatusInternalServerError, "执行失败: "+err.Error())
			return
		}
		// VACUUM 释放空间
		_, _ = h.db.Exec(ctx, `VACUUM (VERBOSE, ANALYZE) request_logs_hot`)
	}

	resp.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, resp)
}
