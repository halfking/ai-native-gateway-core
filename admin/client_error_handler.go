package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ClientErrorReport 表示前端上报的错误
type ClientErrorReport struct {
	Type          string                 `json:"type"`          // error, unhandledrejection, vue-error
	Message       string                 `json:"message"`       // 错误消息
	Stack         string                 `json:"stack"`         // 堆栈跟踪
	URL           string                 `json:"url"`           // 发生错误的页面 URL
	Timestamp     string                 `json:"timestamp"`     // ISO 8601 时间戳
	UserAgent     string                 `json:"userAgent"`     // 浏览器 User-Agent
	ComponentName string                 `json:"componentName"` // Vue 组件名（vue-error 专用）
	Extra         map[string]interface{} `json:"extra"`         // 额外上下文
}

// handleClientError 处理前端错误上报
// 公开端点（无需认证），用于页面加载失败、初始化错误等场景
func (h *Handler) handleClientError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制请求体大小（防止滥用）
	r.Body = http.MaxBytesReader(w, r.Body, 50*1024) // 50KB

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var report ClientErrorReport
	if err := json.Unmarshal(body, &report); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 记录到结构化日志（生产环境可接入 ELK/Loki）
	logFields := []any{
		"source", "client",
		"error_type", report.Type,
		"message", report.Message,
		"url", report.URL,
		"timestamp", report.Timestamp,
		"user_agent", report.UserAgent,
	}

	if report.ComponentName != "" {
		logFields = append(logFields, "component", report.ComponentName)
	}

	if len(report.Extra) > 0 {
		logFields = append(logFields, "extra", report.Extra)
	}

	// 堆栈跟踪单独记录（避免日志过长）
	if report.Stack != "" {
		logFields = append(logFields, "stack_preview", truncate(report.Stack, 200))
	}

	slog.Warn("Frontend error reported", logFields...)

	// 可选：存储到数据库（用于趋势分析）
	// TODO: 实现 client_errors 表和插入逻辑

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "accepted",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// truncate 截断字符串到指定长度
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
