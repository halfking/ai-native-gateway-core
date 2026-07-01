package admin

import (
	"net/http"
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

// handleAttachmentsDownload 处理 GET /api/attachments/{path...}。
// 请求转发给 attachments.Handler.ServeHTTP，后者已实现路径遍历防护、
// Content-Type 推断、大文件流式传输与缓存头。未配置存储时返回 503。
func (h *Handler) handleAttachmentsDownload(w http.ResponseWriter, r *http.Request) {
	if h.attachmentHandler == nil {
		writeError(w, http.StatusServiceUnavailable, "attachment storage is not configured")
		return
	}
	h.attachmentHandler.ServeHTTP(w, r)
}

// listRequestAttachments 处理 GET /api/logs/{request_id}/attachments。
// 委托给 attachments.Handler.ListByRequest；未配置存储或无附件时返回空数组。
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
	h.attachmentHandler.ListByRequest(w, r, requestID)
}
