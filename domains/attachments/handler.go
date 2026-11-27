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
	"github.com/kaixuan/llm-gateway-go/internal/httpx"
)

// Handler 提供附件下载和查询的 HTTP 接口。
//
// 路由：
//
//	GET /api/attachments/{path...}        下载单个附件（按文件系统路径）
//	GET /api/logs/{request_id}/attachments 列出某请求的所有附件元数据
//
// 安全：下载路径做了目录遍历防护；列表查询受 tenant 隔离（调用方应叠加 admin 中间件）。
//
// 认证：支持多种认证模式（none/apikey/signed/admin），通过 SetAuthenticator 配置。
type Handler struct {
	storage       *Storage
	dbPool        *pgxpool.Pool
	authenticator *Authenticator
}

// NewHandler 构造附件 HTTP handler。dbPool 可为 nil（仅禁用列表查询，下载仍可用）。
func NewHandler(storage *Storage, dbPool *pgxpool.Pool) *Handler {
	return &Handler{
		storage:       storage,
		dbPool:        dbPool,
		authenticator: nil, // 默认无认证，需调用 SetAuthenticator 配置
	}
}

// SetAuthenticator 设置认证器（可选）。
func (h *Handler) SetAuthenticator(auth *Authenticator) {
	h.authenticator = auth
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

	// 认证检查（如果配置了 authenticator）
	var tenantID string
	if h.authenticator != nil {
		authCtx, err := h.authenticator.Authenticate(r)
		if err != nil {
			slog.Warn("attachments: authentication failed",
				"path", r.URL.Path,
				"error", err,
				"remote_addr", r.RemoteAddr)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized",
				"authentication failed: "+err.Error())
			return
		}
		if !authCtx.Authenticated {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized",
				"authentication required")
			return
		}
		tenantID = authCtx.TenantID
		// TODO: 审计日志记录 authCtx.TenantID / KeyPrefix
	}

	// 提取路径：去掉 /api/attachments/ 前缀
	relPath := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_path",
			"attachment path is required")
		return
	}

	// P0 安全修复 (2026-11-26): 验证附件归属
	// 防止租户 A 通过路径枚举访问租户 B 的附件
	if tenantID != "" && h.dbPool != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		belongs, err := h.verifyAttachmentOwnership(ctx, relPath, tenantID)
		if err != nil {
			slog.Error("attachments: ownership verification failed",
				"path", relPath,
				"tenant_id", tenantID,
				"error", err)
			writeJSONError(w, http.StatusInternalServerError, "verification_failed",
				"attachment ownership verification failed")
			return
		}
		if !belongs {
			slog.Warn("attachments: cross-tenant access attempt blocked",
				"path", relPath,
				"tenant_id", tenantID,
				"remote_addr", r.RemoteAddr)
			writeJSONError(w, http.StatusForbidden, "forbidden",
				"attachment not accessible")
			return
		}
	}

	// 流式打开（大文件友好）
	f, contentType, err := h.storage.OpenStream(relPath)
	if err != nil {
		slog.Warn("attachments: open failed", "path", relPath, "error", err)
		writeJSONError(w, http.StatusNotFound, "not_found",
			"attachment not found")
		return
	}
	defer func() { _ = f.Close() }()

	// 获取文件信息用于 Content-Length 和 Last-Modified
	fullPath, _ := h.storage.FullPath(relPath)
	if fi, statErr := os.Stat(fullPath); statErr == nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	}

	w.Header().Set("Content-Type", contentType)
	// 审计 R8 P2：上传 MIME 客户端可控，禁用浏览器 MIME 嗅探，
	// 防 Content-Type 被嗅探改写绕过下方的强制下载。
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// 图片类型支持内联显示；非图片强制下载。SVG 是可携带脚本的 XML
	// 文档（image/ 前缀判断对它失效），内联渲染即网关域存储型 XSS，
	// 单独强制下载。
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
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
// 从 _request_logs.attachments 列读取该请求的附件元数据数组并返回 JSON。
// 路径中的 request_id 由上层 mux 提取后传入。
// R35 (2026-09-17 audit P1): tenantID 非空时（tenant_admin 调用方由 admin
// 包装层传入）附加租户谓词——此前仅按 request_id 查询，租户管理员可枚举
// 任意请求的附件元数据（含下载路径）进而跨租户取文件。
func (h *Handler) ListByRequest(w http.ResponseWriter, r *http.Request, requestID string, tenantScope ...string) {
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

	tenantID := ""
	if len(tenantScope) > 0 {
		tenantID = strings.TrimSpace(tenantScope[0])
	}

	var raw []byte
	// Query from view (hot + partitions) to include recent 7-day data
	query := `SELECT attachments::text FROM request_logs_with_current_month WHERE request_id = $1`
	args := []any{requestID}
	if tenantID != "" {
		query += ` AND tenant_id = $2`
		args = append(args, tenantID)
	}
	query += ` ORDER BY ts DESC LIMIT 1`
	err := h.dbPool.QueryRow(ctx, query, args...).Scan(&raw)

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

// verifyAttachmentOwnership 验证指定附件路径是否属于给定租户。
//
// 从 request_logs.attachments JSONB 中查询该路径是否被该租户的任一请求引用。
// P0 安全修复 (2026-11-26): 防止跨租户附件访问。
func (h *Handler) verifyAttachmentOwnership(ctx context.Context, relPath, tenantID string) (bool, error) {
	// ★ 2026-11-27 12h 审计轮（P2-D）：探针 JSON 改为 SQL 侧 jsonb_build_array
	//   参数化构造。此前 fmt.Sprintf(`[{"path":"%s"}]`, relPath) 在应用层拼
	//   JSON——relPath 来自用户可控的 URL 路径（TrimPrefix(r.URL.Path,...)），
	//   内嵌 `"` 或 `\` 可破坏 JSON 结构，使 @> 探针语义漂移（伪造/漏配
	//   path 键）。jsonb_build_array($2)::jsonb 把整个字符串作为一个 text
	//   值安全转义进 JSON，探针语义恒为「完整 path 等值包含」。
	query := `
		SELECT EXISTS(
			SELECT 1 FROM request_logs_with_current_month
			WHERE tenant_id = $1
			AND attachments @> jsonb_build_array(jsonb_build_object('path', $2::text))
		)
	`

	var exists bool
	err := h.dbPool.QueryRow(ctx, query, tenantID, relPath).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("ownership query failed: %w", err)
	}

	return exists, nil
}

// ─── 响应辅助 ───────────────────────────────────────────────────

// writeJSONOK / writeJSONError 薄委托 internal/httpx（2026-09-04 writeJSON
// 收敛）；charset 差异保留在本地。
func writeJSONOK(w http.ResponseWriter, v any) {
	//nolint:errcheck // best-effort, matches previous streaming helper
	httpx.WriteJSON(w, http.StatusOK, "application/json; charset=utf-8", v)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	//nolint:errcheck // best-effort, matches previous streaming helper
	httpx.WriteJSON(w, status, "application/json; charset=utf-8", map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
