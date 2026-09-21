package admin

import (
	"errors"
	"net/http"
	"strings"
)

// attachments_routes.go — 附件下载与按请求列出附件的 HTTP 路由 (migration 325)。
//
// 这两个端点复用 domains/attachments.Handler 已实现的 ServeHTTP / ListByRequest
// 能力，仅在 admin 层做 nil 守卫（启动时未配置附件存储则返回 503/空数组）。
//
//	GET /api/attachments/{path...}          流式下载单个附件文件（图片内联，其他强制下载）
//	GET /api/logs/{request_id}/attachments  返回某请求的附件元数据数组（在 handleLogs 中分发）
//
// 两者均经 admin 中间件鉴权，附件内容不会公开暴露。

var errAttachmentOwnershipNoDB = errors.New("admin: no db pool for attachment ownership check")

// handleAttachmentsDownload 处理 GET /api/attachments/{path...}。
// 请求转发给 attachments.Handler.ServeHTTP，后者已实现路径遍历防护、
// Content-Type 推断、大文件流式传输与缓存头。未配置存储时返回 503。
//
// R36 (2026-09-17 audit, closes R35-gap 遗留#4 的归属校验半边)：tenant_admin
// 角色下载前先做租户归属校验——新写入是内容寻址布局
// （2026/07/a1/b2/<sha256>.png），URL 不携带 request_id/租户，tenant_admin
// 拿到（或枚举到）他人租户的 relPath 即可跨租户读文件。校验经
// request_attachments.request_id → request_logs.tenant_id 联查；历史
// req_<request_id>/ 布局走 request_id 快路径。super_admin / admin_key 与
// 列表端点同口径不收敛；校验失败 fail-closed，拒绝以 404 返回（不确认存在性）。
func (h *Handler) handleAttachmentsDownload(w http.ResponseWriter, r *http.Request) {
	if h.attachmentHandler == nil {
		writeError(w, http.StatusServiceUnavailable, "attachment storage is not configured")
		return
	}
	if IsTenantAdmin(r) {
		relPath := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
		if relPath == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		owned, err := h.attachmentOwnedByTenant(r, relPath, GetTenantID(r))
		if err != nil {
			// Access control must fail closed; a storage/DB hiccup must not
			// open a cross-tenant read window.
			writeError(w, http.StatusInternalServerError, "attachment ownership check failed")
			return
		}
		if !owned {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
	}
	h.attachmentHandler.ServeHTTP(w, r)
}

// attachmentOwnedByTenant reports whether relPath resolves to an attachment
// whose owning request belongs to the given tenant.
func (h *Handler) attachmentOwnedByTenant(r *http.Request, relPath, tenantID string) (bool, error) {
	if h.db == nil {
		return false, errAttachmentOwnershipNoDB
	}
	ctx := r.Context()
	// Historical layout: req_<request_id>/... — the request id is in the path.
	if rest, ok := strings.CutPrefix(relPath, "req_"); ok {
		requestID := rest
		if i := strings.Index(rest, "/"); i >= 0 {
			requestID = rest[:i]
		}
		if requestID != "" {
			var owned bool
			err := h.db.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM request_logs_with_current_month
					WHERE request_id = $1 AND tenant_id = $2
				)`, requestID, tenantID).Scan(&owned)
			return owned, err
		}
	}
	// Content-addressed layout: match the metadata table's storage_path and
	// reach the tenant through the owning request.
	var owned bool
	err := h.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM request_attachments ra
			JOIN request_logs_with_current_month rl ON rl.request_id = ra.request_id
			WHERE ra.storage_path = $1 AND rl.tenant_id = $2
		)`, relPath, tenantID).Scan(&owned)
	return owned, err
}

// listRequestAttachments 处理 GET /api/logs/{request_id}/attachments。
// 委托给 attachments.Handler.ListByRequest；未配置存储或无附件时返回空数组。
// R35 (2026-09-17 audit P1): tenant_admin 收敛到本租户（此前仅按
// request_id 查询，可跨租户枚举附件元数据并取得下载路径）。
func (h *Handler) listRequestAttachments(w http.ResponseWriter, r *http.Request, requestID string) {
	if h.attachmentHandler == nil {
		// 用与 ListByRequest 相同的空结构响应，保证前端无需区分"未配置"与"无附件"。
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id":  requestID,
			"attachments": []any{},
			"count":       0,
		})
		return
	}
	tenantScope := ""
	if IsTenantAdmin(r) {
		tenantScope = GetTenantID(r)
	}
	h.attachmentHandler.ListByRequest(w, r, requestID, tenantScope)
}
