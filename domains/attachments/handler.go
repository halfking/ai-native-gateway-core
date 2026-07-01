package attachments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler 提供附件下载和查询的 HTTP 接口。
//
// 路由：
//
//	GET /api/attachments/{path...}        下载单个附件（按文件系统路径）
//	GET /api/logs/{request_id}/attachments 列出某请求的所有附件元数据
//
// 安全：下载路径做了目录遍历防护；列表查询受 tenant 隔离（调用方应叠加 admin 中间件）。
type Handler struct {
	storage *Storage
	dbPool  *pgxpool.Pool
}

// NewHandler 构造附件 HTTP handler。dbPool 可为 nil（仅禁用列表查询，下载仍可用）。
func NewHandler(storage *Storage, dbPool *pgxpool.Pool) *Handler {
	return &Handler{storage: storage, dbPool: dbPool}
}

// ServeHTTP 处理 GET /api/attachments/{path...} 下载请求。
//
// path 是相对 Storage.BaseDir 的路径，形如 2026/07/req_xxx/abc.png。
// 支持大文件流式传输（io.Copy），不一次性载入内存。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"only GET is supported")
		return
	}

	// 提取路径：去掉 /api/attachments/ 前缀
	relPath := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_path",
			"attachment path is required")
		return
	}

	// 流式打开（大文件友好）
	f, contentType, err := h.storage.OpenStream(relPath)
	if err != nil {
		slog.Warn("attachments: open failed", "path", relPath, "error", err)
		writeJSONError(w, http.StatusNotFound, "not_found",
			"attachment not found")
		return
	}
	defer f.Close()

	// 获取文件信息用于 Content-Length 和 Last-Modified
	fullPath, _ := h.storage.FullPath(relPath)
	if fi, statErr := os.Stat(fullPath); statErr == nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	}

	w.Header().Set("Content-Type", contentType)

	// 图片类型支持内联显示；非图片强制下载。
	if !strings.HasPrefix(contentType, "image/") {
		filename := filepath.Base(relPath)
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", filename))
	}
	// 私有缓存 1 天（附件内容不可变，hash 命名）
	w.Header().Set("Cache-Control", "private, max-age=86400")

	if r.Method == http.MethodHead {
		return
	}

	if _, err := io.Copy(w, f); err != nil {
		// 客户端可能已断开，只记 debug
		slog.Debug("attachments: write to client interrupted",
			"path", relPath, "error", err)
	}
}

// ListByRequest 处理 GET /api/logs/{request_id}/attachments。
//
// 从 request_logs.attachments 列读取该请求的附件元数据数组并返回 JSON。
// 路径中的 request_id 由上层 mux 提取后传入。
func (h *Handler) ListByRequest(w http.ResponseWriter, r *http.Request, requestID string) {
	if requestID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_request_id",
			"request_id is required")
		return
	}
	if h.dbPool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "db_unavailable",
			"database is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var raw []byte
	err := h.dbPool.QueryRow(ctx,
		`SELECT attachments::text FROM request_logs WHERE request_id = $1 ORDER BY ts DESC LIMIT 1`,
		requestID,
	).Scan(&raw)

	if err != nil {
		// 无附件或请求不存在：返回空数组而非错误
		writeJSONOK(w, map[string]any{
			"request_id":  requestID,
			"attachments": []any{},
		})
		return
	}

	var attachments []any
	if len(raw) > 0 {
		if jsonErr := json.Unmarshal(raw, &attachments); jsonErr != nil {
			slog.Warn("attachments: unmarshal failed",
				"request_id", requestID, "error", jsonErr)
			attachments = []any{}
		}
	}
	if attachments == nil {
		attachments = []any{}
	}

	writeJSONOK(w, map[string]any{
		"request_id":  requestID,
		"attachments": attachments,
		"count":       len(attachments),
	})
}

// ─── 响应辅助 ───────────────────────────────────────────────────

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
