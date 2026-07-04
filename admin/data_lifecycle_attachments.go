// Package admin — data_lifecycle_attachments.go
//
// 2026-07-01: 会话附件管理端点。
//
// 这些端点查询 request_logs.attachments JSONB 列（migration 325），
// 提供附件列表、统计、清理预览/执行、单项查看能力。附件实体文件存储在
// 文件系统（LLM_GATEWAY_ATTACHMENT_DIR），此处只管理数据库中的元数据引用。
//
// 端点（详见 handler.go 路由注册）：
//   GET  /api/admin/attachments                列出含附件的请求（分页）
//   GET  /api/admin/attachments/stats          附件统计（类型/大小分布）
//   GET  /api/admin/attachments/policy         清理策略配置
//   POST /api/admin/attachments/cleanup/preview 预览将要清理的附件
//   POST /api/admin/attachments/cleanup/execute 清理过期附件元数据（super_admin）
//   GET  /api/admin/attachments/{request_id}    查看某请求的附件详情
//
// 注意：本端点只清理 request_logs.attachments 列（置 NULL），
// 不删除文件系统中的实体文件（文件由 hash 命名可去重，删除需额外确认）。

package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// attachmentRow 是 attachments 列表查询的单行结果。
type attachmentRow struct { //nolint:unused
	RequestID   string          `json:"request_id"`
	Ts          time.Time       `json:"ts"`
	TenantID    string          `json:"tenant_id"`
	ClientModel string          `json:"client_model"`
	Success     bool            `json:"success"`
	Attachments json.RawMessage `json:"attachments"`
}

// handleDataLifecycleAttachments GET /api/admin/attachments
// 列出含附件的请求记录，按时间倒序分页。
func (h *Handler) handleDataLifecycleAttachments(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 50, 1, 200)
	offset := clampInt(r.URL.Query().Get("offset"), 0, 0, 100000)
	since, until := parseTimeRange(r)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	args := []any{limit, offset}
	where := "WHERE attachments IS NOT NULL"
	if !since.IsZero() {
		args = append(args, since)
		where += " AND ts >= $" + strconv.Itoa(len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		where += " AND ts < $" + strconv.Itoa(len(args))
	}

	query := `
		SELECT request_id, ts, tenant_id,
		       COALESCE(client_model, ''), success, attachments::text
		FROM request_logs
		` + where + `
		ORDER BY ts DESC
		LIMIT $1 OFFSET $2`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("attachments: list query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]attachmentRow, 0, limit)
	for rows.Next() {
		var row attachmentRow
		var attText string
		if err := rows.Scan(&row.RequestID, &row.Ts, &row.TenantID,
			&row.ClientModel, &row.Success, &attText); err != nil {
			continue
		}
		row.Attachments = json.RawMessage(attText)
		items = append(items, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"limit":  limit,
		"offset": offset,
		"count":  len(items),
	})
}

// handleDataLifecycleAttachmentStats GET /api/admin/attachments/stats
// 统计附件的类型、大小、数量分布。
func (h *Handler) handleDataLifecycleAttachmentStats(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	since, until := parseTimeRange(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	args := []any{}
	where := "WHERE attachments IS NOT NULL"
	if !since.IsZero() {
		args = append(args, since)
		where += " AND ts >= $" + strconv.Itoa(len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		where += " AND ts < $" + strconv.Itoa(len(args))
	}

	query := `
		SELECT elem->>'type' AS type,
		       elem->>'content_type' AS content_type,
		       COUNT(*) AS cnt,
		       COALESCE(SUM((elem->>'size')::bigint), 0) AS total_bytes
		FROM request_logs,
		     LATERAL jsonb_array_elements(attachments) AS elem
		` + where + `
		GROUP BY type, content_type
		ORDER BY cnt DESC`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("attachments: stats query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type bucket struct {
		Type        string `json:"type"`
		ContentType string `json:"content_type"`
		Count       int64  `json:"count"`
		TotalBytes  int64  `json:"total_bytes"`
	}
	buckets := make([]bucket, 0)
	var totalCount, totalBytes int64
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.Type, &b.ContentType, &b.Count, &b.TotalBytes); err != nil {
			continue
		}
		buckets = append(buckets, b)
		totalCount += b.Count
		totalBytes += b.TotalBytes
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"breakdown":   buckets,
		"total_count": totalCount,
		"total_bytes": totalBytes,
	})
}

// handleDataLifecycleAttachmentPolicy GET /api/admin/attachments/policy
// 返回当前清理策略配置（只读展示）。
func (h *Handler) handleDataLifecycleAttachmentPolicy(w http.ResponseWriter, r *http.Request) { //nolint:unused
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": map[string]any{
			"retention_days":    30,
			"max_size_bytes":    20 * 1024 * 1024,
			"auto_cleanup":      false,
			"delete_filesystem": false,
			"description": "附件元数据保留 30 天；默认不自动清理，" +
				"不删除文件系统实体文件。通过 cleanup/preview + execute 手动操作。",
		},
		"note": "策略为内置默认值，暂不支持动态配置。可通过环境变量 " +
			"LLM_GATEWAY_ATTACHMENT_DISABLED=1 完全关闭附件捕获。",
	})
}

// handleDataLifecycleAttachmentCleanupPreview POST /api/admin/attachments/cleanup/preview
// 预览将要被清理的附件记录数量（dry-run）。
func (h *Handler) handleDataLifecycleAttachmentCleanupPreview(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	olderThanDays := parseOlderThanDays(r, 30)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var count int64
	var totalSize int64
	err := h.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM((elem->>'size')::bigint), 0)
		FROM request_logs,
		     LATERAL jsonb_array_elements(attachments) AS elem
		WHERE attachments IS NOT NULL
		  AND ts < NOW() - ($1 || ' days')::interval`,
		olderThanDays,
	).Scan(&count, &totalSize)
	if err != nil {
		slog.Warn("attachments: cleanup preview query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"older_than_days":  olderThanDays,
		"affected_records": count,
		"total_bytes":      totalSize,
		"dry_run":          true,
		"action":           "将把匹配记录的 attachments 列置为 NULL（保留行和元数据）",
	})
}

// handleDataLifecycleAttachmentCleanupExecute POST /api/admin/attachments/cleanup/execute
// 执行清理：将过期记录的 attachments 列置为 NULL。
func (h *Handler) handleDataLifecycleAttachmentCleanupExecute(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	olderThanDays := parseOlderThanDays(r, 30)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE request_logs
		SET attachments = NULL
		WHERE attachments IS NOT NULL
		  AND ts < NOW() - ($1 || ' days')::interval`,
		olderThanDays,
	)
	if err != nil {
		slog.Warn("attachments: cleanup execute failed", "error", err)
		writeError(w, http.StatusInternalServerError, "cleanup failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"older_than_days":  olderThanDays,
		"rows_affected":    tag.RowsAffected(),
		"action":           "attachments 列已置 NULL",
		"filesystem_files": "未删除（文件由 hash 命名，需单独清理）",
	})
}

// handleDataLifecycleAttachmentItem GET /api/admin/attachments/{request_id}
// 查看某个请求的附件详情。
func (h *Handler) handleDataLifecycleAttachmentItem(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	requestID := r.URL.Path[len("/api/admin/attachments/"):]
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var ts time.Time
	var tenantID, clientModel string
	var success bool
	var attText string
	err := h.db.QueryRow(ctx, `
		SELECT ts, tenant_id, COALESCE(client_model, ''), success,
		       COALESCE(attachments::text, '[]')
		FROM request_logs
		WHERE request_id = $1
		ORDER BY ts DESC LIMIT 1`,
		requestID,
	).Scan(&ts, &tenantID, &clientModel, &success, &attText)
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":   requestID,
		"ts":           ts,
		"tenant_id":    tenantID,
		"client_model": clientModel,
		"success":      success,
		"attachments":  json.RawMessage(attText),
	})
}

// clampInt 解析查询参数为 int，限定在 [min, max] 范围，默认 def。
func clampInt(s string, def, min, max int) int { //nolint:unused
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// parseTimeRange 解析 since/until 查询参数（RFC3339）。
func parseTimeRange(r *http.Request) (since, until time.Time) { //nolint:unused
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			since = t
		}
	}
	if u := r.URL.Query().Get("until"); u != "" {
		if t, err := time.Parse(time.RFC3339, u); err == nil {
			until = t
		}
	}
	return
}

// parseOlderThanDays 从 JSON 请求体解析 older_than_days，默认 def。
func parseOlderThanDays(r *http.Request, def int) int { //nolint:unused
	if r.Body != nil {
		var body struct {
			OlderThanDays int `json:"older_than_days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.OlderThanDays > 0 {
			return body.OlderThanDays
		}
	}
	return def
}
