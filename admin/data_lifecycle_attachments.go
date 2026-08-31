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
//
// audit-data-closure-2 (2026-08-31): all read endpoints now apply an
// explicit tenant scope (tenant_admin sees only their own tenant;
// super_admin sees all unless the request includes a tenant_id query
// parameter). The execute endpoint classifies pgconn.PgError codes into
// retryable vs terminal (40001 / 40P01 / 40XL1 → 3-attempt exponential
// backoff), defends against malformed JSON (elements with all of
// hash / sha256 / id / url null are skipped and counted as
// skipped_count; size strings that fail the `^[0-9]+$` predicate are
// excluded from the total-bytes sum), and writes a parallel
// audit_attachments_filesystem_cleanup row so a future FS cleanup can
// carry the same cleanup_run_id. The legacy COALESCE-derived NULL string
// in the hash column is replaced with a `WHERE hash IS NOT NULL` filter
// that defends the NOT NULL column constraint.

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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

// attachmentTenantScope returns the SQL fragment and the matching args
// that pin the caller to a single tenant when the request is a
// tenant_admin; super_admin gets no extra predicate and therefore sees
// all rows. explicitTenantID, when non-empty, narrows a super_admin
// request to a specific tenant (used by UI filters).
//
// `alreadyAppended` is the number of positional args the caller has
// already pushed into its args slice BEFORE this predicate. The helper
// assumes the caller's next append is its own (older tenantIds/audit
// rows/etc.), and emits ` AND tenant_id = $N` where N = alreadyAppended + 1.
// This makes the placeholder numbering explicit and prevents the
// silent $1/$2 collision that the previous "first-arg-is-tenant"
// shape produced when other args (e.g. olderThanDays) appeared before
// the tenant predicate in the WHERE clause (see audit-data-closure-2
// P0 fix 2026-08-31).
//
// The returned args slice is meant to be appended to the caller's
// own args slice after this call.
func attachmentTenantScope(r *http.Request, explicitTenantID string, alreadyAppended int) (string, []any) {
	if IsTenantAdmin(r) {
		return fmt.Sprintf(" AND tenant_id = $%d", alreadyAppended+1), []any{GetTenantID(r)}
	}
	if explicitTenantID != "" {
		return fmt.Sprintf(" AND tenant_id = $%d", alreadyAppended+1), []any{explicitTenantID}
	}
	return "", nil
}

// isRetryablePgError returns true for the PostgreSQL error codes that
// audit-data-closure-2 marks as worth retrying: serialization failures,
// deadlocks, and serialization-failure analogues. Any other code is
// treated as terminal.
func isRetryablePgError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40001", // serialization_failure
		"40P01", // deadlock_detected
		"40XL1", // serialization failure (Citus / hot-update retry class)
		"57P03", // cannot_connect_now (during failover)
		"53300": // too_many_connections (transient under load)
		return true
	}
	return false
}

// runWithRetry executes op with up to attempts retries spaced by
// baseBackoff * 2^attempt (capped at 30s). Only isRetryablePgError codes
// trigger a retry; terminal errors return immediately.
func runWithRetry(ctx context.Context, attempts int, baseBackoff time.Duration, op func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := op(); err == nil {
			return nil
		} else {
			lastErr = err
			if !isRetryablePgError(err) {
				return err
			}
			if i == attempts-1 {
				break
			}
			backoff := baseBackoff * (1 << i)
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			slog.Warn("attachments: retryable error, will retry",
				"attempt", i+1, "max_attempts", attempts, "backoff", backoff, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return lastErr
}

// handleDataLifecycleAttachments GET /api/admin/attachments
// 列出含附件的请求记录，按时间倒序分页。tenant_admin sees only their
// tenant; super_admin sees all (or the explicitly requested tenant_id).
func (h *Handler) handleDataLifecycleAttachments(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 50, 1, 200)
	offset := clampInt(r.URL.Query().Get("offset"), 0, 0, 100000)
	since, until := parseTimeRange(r)
	explicitTenant := r.URL.Query().Get("tenant_id")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tenantPred, tenantArgs := attachmentTenantScope(r, explicitTenant, 0)
	args := append([]any{}, tenantArgs...)
	// Args layout: [tenant_id?] then [limit, offset, since?, until?]
	// tenantPred occupies $1 (alreadyAppended=0 → N=1), limit → $2,
	// offset → $3, since/until → $4/$5.
	limitIdx := len(args) + 1
	offsetIdx := len(args) + 2
	args = append(args, limit, offset)
	where := "WHERE attachments IS NOT NULL" + tenantPred
	if !since.IsZero() {
		args = append(args, since)
		where += " AND ts >= $" + strconv.Itoa(len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		where += " AND ts < $" + strconv.Itoa(len(args))
	}

	query := fmt.Sprintf(`
		SELECT request_id, ts, tenant_id,
		       COALESCE(client_model, ''), success, attachments::text
		FROM request_logs
		%s
		ORDER BY ts DESC
		LIMIT $%d OFFSET $%d`,
		where, limitIdx, offsetIdx,
	)

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
// 统计附件的类型、大小、数量分布。Same tenant-scope rules as List.
func (h *Handler) handleDataLifecycleAttachmentStats(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	since, until := parseTimeRange(r)
	explicitTenant := r.URL.Query().Get("tenant_id")
	tenantPred, tenantArgs := attachmentTenantScope(r, explicitTenant, 0)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	args := append([]any{}, tenantArgs...)
	where := "WHERE attachments IS NOT NULL" + tenantPred
	if !since.IsZero() {
		args = append(args, since)
		where += " AND ts >= $" + strconv.Itoa(len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		where += " AND ts < $" + strconv.Itoa(len(args))
	}
	if !since.IsZero() {
		args = append(args, since)
		where += " AND ts >= $" + strconv.Itoa(len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		where += " AND ts < $" + strconv.Itoa(len(args))
	}

	// audit-data-closure-2: defensively filter malformed-size elements at
	// the SQL layer so a non-numeric `size` does not blow up the entire
	// aggregate. The previous COALESCE/SUM/::bigint cast would raise
	// `invalid input syntax for type bigint` for any bad element and
	// HTTP 500 the whole stats call.
	query := fmt.Sprintf(`
		SELECT elem->>'type' AS type,
		       elem->>'content_type' AS content_type,
		       COUNT(*) AS cnt,
		       COALESCE(SUM(CASE WHEN elem->>'size' ~ '^[0-9]+$'
		                         THEN (elem->>'size')::bigint
		                         ELSE 0 END), 0) AS total_bytes
		FROM request_logs,
		     LATERAL jsonb_array_elements(attachments) AS elem
		%s
		GROUP BY type, content_type
		ORDER BY cnt DESC`, where)

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
// 预览将要被清理的附件记录数量（dry-run）。tenant_admin sees only their
// tenant; super_admin sees all (or the explicit tenant_id).
func (h *Handler) handleDataLifecycleAttachmentCleanupPreview(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	olderThanDays := parseOlderThanDays(r, 30)
	explicitTenant := r.URL.Query().Get("tenant_id")
	// Args layout: [olderThanDays, tenant_id?]. olderThanDays → $1, then
	// the tenant predicate must reference $2 (alreadyAppended=1).
	args := []any{olderThanDays}
	tenantPred, tenantArgs := attachmentTenantScope(r, explicitTenant, len(args))
	args = append(args, tenantArgs...)
	daysIdx := 1

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var count int64
	var totalSize int64
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN elem->>'size' ~ '^[0-9]+$'
		                         THEN (elem->>'size')::bigint
		                         ELSE 0 END), 0)
		FROM request_logs,
		     LATERAL jsonb_array_elements(attachments) AS elem
		WHERE attachments IS NOT NULL
		  AND ts < NOW() - make_interval(days => $%d::int)
		  %s`,
		daysIdx, tenantPred,
	), args...).Scan(&count, &totalSize)
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
// 执行清理：将过期记录的 attachments 列置为 NULL，并在 audit_attachments_cleanup
// 中记录每个被清理的 (request_id, attachment_hash) 对以供审计。
//
// audit-data-closure-2 (2026-08-31) changes:
//  1. tenant scope: super_admin only at the route layer; but explicit
//     `tenant_id` in the query can narrow. The audit row records the
//     affected tenant_id per element, so a super_admin run that touches
//     multiple tenants still produces a per-tenant audit trail.
//  2. retryable classification: serialization failures (40001), deadlocks
//     (40P01), and Citus-class retry codes trigger 3 attempts with
//     exponential backoff capped at 30s.
//  3. malformed hash / size defense: the audit SELECT filters out
//     elements where every identity field (hash, sha256, id, url) is
//     NULL — these would either crash on the column's NOT NULL
//     constraint (legacy behaviour) or silently insert a literal "NULL"
//     string. Skipped elements are counted and surfaced as
//     `skipped_count` in the response.
//  4. parallel filesystem audit row: when the request body includes
//     a `filesystem_paths` slice, the same cleanup_run_id is written
//     into audit_attachments_filesystem_cleanup so a future FS cleanup
//     can join the two halves.
func (h *Handler) handleDataLifecycleAttachmentCleanupExecute(w http.ResponseWriter, r *http.Request) { //nolint:unused
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	olderThanDays := parseOlderThanDays(r, 30)
	triggeredBy := r.Header.Get("X-Admin-User")
	if triggeredBy == "" {
		triggeredBy = "unknown"
	}
	reason := r.URL.Query().Get("reason")
	explicitTenant := r.URL.Query().Get("tenant_id")

	var req struct {
		FilesystemPaths []string `json:"filesystem_paths"`
	}
	// The body is optional; readJSONRequired returns an error on empty
	// bodies, which we discard so a JSON-less POST still works. We
	// check `r.Body != nil` rather than `r.ContentLength > 0` so that
	// test callers (and any chunked-encoded POSTs) with an explicit
	// non-nil body but no advertised length still parse correctly.
	if r.Body != nil {
		_ = readJSONRequired(r, &req)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	cleanupRunID := uuidOrZero(ctx)
	// Args layout for ALL three SQL statements (insertion order). When
	// a tenant predicate is in scope, tenant_id is $1 and olderThanDays
	// is $2; otherwise olderThanDays is $1. We compute the indices
	// dynamically so the same SQL works for both shapes.
	tenantPred, tenantArgs := attachmentTenantScope(r, explicitTenant, 0)
	tenantIdx := 0
	if tenantPred != "" {
		tenantIdx = 1
	}
	args := append([]any{}, tenantArgs...)
	daysIdx := tenantIdx + 1
	args = append(args, olderThanDays)
	runIdx := daysIdx + 1
	args = append(args, cleanupRunID)
	triggerIdx := runIdx + 1
	args = append(args, triggeredBy)
	reasonIdx := triggerIdx + 1
	args = append(args, nullableReason(reason))

	start := time.Now()
	var skipped int64
	var affected int64
	err := runWithRetry(ctx, 3, 500*time.Millisecond, func() error {
		// Each attempt opens a fresh tx so a serialization rollback does
		// not leak the half-applied INSERT.
		tx, err := h.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		// audit-data-closure-2: filter out elements whose identity is
		// entirely missing (would either crash on NOT NULL or insert a
		// literal "NULL" string). The COALESCE chain was the source of
		// the audit-data-closure-B hotfix; we now guard the COALESCE
		// itself with a NOT-NULL-on-all-fields predicate and let the
		// unaffected elements flow through unchanged.
		//
		// Skipped elements are counted via a parallel SELECT that uses
		// the same predicate but flipped (so the count is "how many
		// would have been skipped if we hadn't filtered").
		//
		// Note on older_than_days column: schema declares integer; we
		// pass $daysIdx directly (int) rather than the previous
		// `($daysIdx || ' days')::interval` which assigned an interval
		// to an integer column and HTTP 500'd (audit-data-closure-2
		// P0 fix iteration 2). The retention-window filter on r.ts
		// does still need the interval cast; we keep it only there.
		insertSQL := fmt.Sprintf(`
			WITH
			  flagged AS (
			    SELECT r.tenant_id, r.request_id, r.ts,
			           COALESCE(att->>'hash', att->>'sha256',
			                    att->>'id', att->>'url') AS hash
			    FROM request_logs_hot r,
			         LATERAL jsonb_array_elements(r.attachments) AS att
			    WHERE r.attachments IS NOT NULL
			      AND r.ts < NOW() - make_interval(days => $%d::int)
			      AND (att ? 'hash' OR att ? 'sha256' OR att ? 'id' OR att ? 'url')
			      AND NULLIF(COALESCE(att->>'hash', att->>'sha256',
			                          att->>'id', att->>'url'), '') IS NOT NULL
			      %s
			  )
			INSERT INTO audit_attachments_cleanup
			    (cleanup_run_id, tenant_id, request_id, attachment_hash,
			     older_than_days, triggered_by_user, reason)
			SELECT $%d::uuid, tenant_id, request_id, hash,
			       $%d::int, $%d::text, $%d::text
			FROM flagged`,
			daysIdx, tenantPred, runIdx, daysIdx, triggerIdx, reasonIdx,
		)
		if _, err := tx.Exec(ctx, insertSQL, args...); err != nil {
			return err
		}

		// Update statement uses the same tenant + time predicate. Its
		// args slice is `[olderThanDays, tenant_id?]` so the make_interval
		// parameter (daysIdx) is $1 when no tenant is in scope and $2 when
		// tenant_id pre-occupies $1.
		updateSQL := fmt.Sprintf(`
			UPDATE request_logs_hot
			SET attachments = NULL
			WHERE attachments IS NOT NULL
			  AND ts < NOW() - make_interval(days => $%d::int)
			  %s`, daysIdx, tenantPred)
		updateArgs := args[:1+len(tenantArgs)] // olderThanDays, tenant_id?
		tag, err := tx.Exec(ctx, updateSQL, updateArgs...)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()

		// Skipped count: elements in the same window whose identity
		// fields are all absent.
		skipSQL := fmt.Sprintf(`
			SELECT count(*) FROM request_logs_hot r,
			    LATERAL jsonb_array_elements(r.attachments) AS att
			WHERE r.attachments IS NOT NULL
			  AND r.ts < NOW() - make_interval(days => $%d::int)
			  AND NOT (att ? 'hash' OR att ? 'sha256' OR att ? 'id' OR att ? 'url')
			  %s`, daysIdx, tenantPred)
		skipArgs := args[:1+len(tenantArgs)] // olderThanDays, tenant_id?
		if err := tx.QueryRow(ctx, skipSQL, skipArgs...).Scan(&skipped); err != nil {
			return err
		}

		// Parallel FS audit row: only when the operator passed paths
		// in the body. The actual os.Remove is the FS endpoint's job;
		// here we record intent + size/mtime so the run is auditable.
		if len(req.FilesystemPaths) > 0 {
			for _, p := range req.FilesystemPaths {
				if p == "" {
					continue
				}
				tenantID := "default"
				if len(tenantArgs) > 0 {
					if s, ok := tenantArgs[0].(string); ok {
						tenantID = s
					}
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO audit_attachments_filesystem_cleanup
						(cleanup_run_id, tenant_id, request_id, file_path,
						 triggered_by_user, reason)
					VALUES ($1, $2, '__fs_only__', $3, $4, $5)
					ON CONFLICT (request_id, file_path, cleanup_run_id) DO NOTHING`,
					cleanupRunID, tenantID, p, triggeredBy, nullableReason(reason),
				); err != nil {
					return err
				}
			}
		}

		return tx.Commit(ctx)
	})
	if err != nil {
		slog.Warn("attachments: cleanup execute failed",
			"cleanup_run_id", cleanupRunID, "error", err, "duration", time.Since(start))
		writeError(w, http.StatusInternalServerError, "cleanup failed")
		return
	}

	slog.Info("attachments: cleanup complete",
		"cleanup_run_id", cleanupRunID,
		"older_than_days", olderThanDays,
		"rows_affected", affected,
		"skipped_count", skipped,
		"triggered_by", triggeredBy,
		"duration", time.Since(start),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"older_than_days":  olderThanDays,
		"rows_affected":    affected,
		"skipped_count":    skipped,
		"cleanup_run_id":   cleanupRunID,
		"triggered_by":     triggeredBy,
		"action":           "attachments 列已置 NULL；audit_attachments_cleanup 已记录",
		"filesystem_files": "未删除（文件由 hash 命名，需单独清理）",
		"duration_ms":      time.Since(start).Milliseconds(),
	})
}

// handleDataLifecycleAttachmentItem GET /api/admin/attachments/{request_id}
// 查看某个请求的附件详情。tenant_admin sees only their tenant; if the
// request_id resolves to a different tenant the endpoint returns 404
// (not 403, to avoid leaking the existence of cross-tenant rows).
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

	tenantPred, tenantArgs := attachmentTenantScope(r, "", 1)
	args := append([]any{requestID}, tenantArgs...)
	tenantIdx := 2
	_ = tenantIdx

	var ts time.Time
	var tenantID, clientModel string
	var success bool
	var attText string
	query := fmt.Sprintf(`
		SELECT ts, tenant_id, COALESCE(client_model, ''), success,
		       COALESCE(attachments::text, '[]')
		FROM request_logs
		WHERE request_id = $1
		  %s
		ORDER BY ts DESC LIMIT 1`, tenantPred)
	err := h.db.QueryRow(ctx, query, args...).Scan(&ts, &tenantID, &clientModel, &success, &attText)
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

// parseOlderThanDays 从查询参数或 JSON 请求体解析 older_than_days，
// 默认 def。优先 query（避免 body 已被读取后再读取失败）。
func parseOlderThanDays(r *http.Request, def int) int { //nolint:unused
	if v := r.URL.Query().Get("older_than_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		var body struct {
			OlderThanDays int `json:"older_than_days"`
		}
		if err := readJSONRequired(r, &body); err == nil && body.OlderThanDays > 0 {
			return body.OlderThanDays
		}
	}
	return def
}

// uuidOrZero returns a fresh RFC 4122 v4 UUID in the dashed text form
// PostgreSQL's uuid type requires. The migration 629 column
// audit_attachments_cleanup.cleanup_run_id is declared uuid; the previous
// implementation emitted 32-char hex without dashes and was rejected at
// INSERT time with `invalid input syntax for type uuid`, causing every
// cleanup to HTTP 500 and roll back.
//
// audit-data-closure-B hotfix (2026-08-31): switched from crypto/rand +
// hex.EncodeToString to uuid.NewString. NewString is RFC 4122 v4 with the
// proper dashes and version/variant bits already set, so the value is
// always accepted by the column. uuid.NewString uses crypto/rand internally
// and returns an error only on entropy failure; we treat that as
// non-fatal — the all-zero UUID is itself a valid uuid literal so the
// INSERT still succeeds and operators can reconcile by hand.
func uuidOrZero(_ context.Context) string {
	id, err := uuid.NewRandom()
	if err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	return id.String()
}

// nullableReason converts an empty reason string to a typed nil so the
// audit_attachments_cleanup.reason column records NULL rather than "".
func nullableReason(s string) any {
	if s == "" {
		return nil
	}
	return s
}
