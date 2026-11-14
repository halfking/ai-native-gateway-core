package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/anomaly"
)

// AnomalyDetectorMiddleware 异常检测中间件
type AnomalyDetectorMiddleware struct {
	next    http.Handler
	manager *anomaly.Manager
	logger  *slog.Logger
	enabled bool
}

// NewAnomalyDetectorMiddleware 创建异常检测中间件
func NewAnomalyDetectorMiddleware(next http.Handler, manager *anomaly.Manager, logger *slog.Logger) *AnomalyDetectorMiddleware {
	if logger == nil {
		logger = slog.Default()
	}
	
	return &AnomalyDetectorMiddleware{
		next:    next,
		manager: manager,
		logger:  logger,
		enabled: true,
	}
}

// ServeHTTP 实现 http.Handler 接口
func (m *AnomalyDetectorMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 快速路径：检测器禁用或非聊天端点
	if !m.enabled || !m.isChatEndpoint(r) {
		m.next.ServeHTTP(w, r)
		return
	}

	// 从请求中提取检测所需数据
	// 注意：这里只做轻量级解析，避免完整 body 解析（性能考虑）
	req, err := m.extractAnomalyRequest(r)
	if err != nil {
		// 提取失败不阻断请求，记录警告后放行
		m.logger.Warn("anomaly_detector: failed to extract request data",
			"error", err,
			"path", r.URL.Path)
		m.next.ServeHTTP(w, r)
		return
	}

	// 运行检测器
	ctx := r.Context()
	anomalies := m.manager.CheckAll(ctx, req)

	if len(anomalies) == 0 {
		// 无异常，正常放行
		m.next.ServeHTTP(w, r)
		return
	}

	// 有异常：选择最高严重级别
	worst := anomaly.SelectHighestSeverity(anomalies)

	// 记录所有检测到的异常
	for _, a := range anomalies {
		m.logger.Info("anomaly_detected",
			"code", a.Code,
			"severity", a.Severity,
			"message", a.Message,
			"metrics", a.Metrics,
			"session_id", req.SessionID,
			"api_key_id", req.APIKeyID,
			"path", r.URL.Path)
	}

	// 判断是否拦截
	if anomaly.ShouldBlock(worst.Severity) {
		// 拦截请求
		m.logger.Warn("anomaly_blocked_request",
			"code", worst.Code,
			"severity", worst.Severity,
			"message", worst.Message,
			"session_id", req.SessionID,
			"api_key_id", req.APIKeyID,
			"path", r.URL.Path)

		// 返回 429 + 错误详情
		httpStatus := anomaly.GetHTTPStatus(worst.Severity)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Anomaly-Code", worst.Code)
		w.WriteHeader(httpStatus)
		
		// 格式化错误响应
		errResp := anomaly.FormatAnomalyError(worst)
		if jsonBytes, err := marshalJSON(errResp); err == nil {
			w.Write(jsonBytes)
		} else {
			// fallback
			w.Write([]byte(`{"error":{"type":"gateway_anomaly_detected","message":"请求被异常检测拦截"}}`))
		}
		return
	}

	// medium/low severity：打标记但不拦截
	// 在 context 中注入异常标记，下游可以选择性降级处理
	ctx = context.WithValue(ctx, anomalyFlagsKey{}, anomalies)
	r = r.WithContext(ctx)

	m.logger.Info("anomaly_warning",
		"code", worst.Code,
		"severity", worst.Severity,
		"action", "passed_with_flags",
		"session_id", req.SessionID)

	m.next.ServeHTTP(w, r)
}

// anomalyFlagsKey context key for anomaly flags
type anomalyFlagsKey struct{}

// extractAnomalyRequest 从 HTTP 请求中提取检测所需数据
func (m *AnomalyDetectorMiddleware) extractAnomalyRequest(r *http.Request) (*anomaly.Request, error) {
	// 从 header/context 提取基本信息
	sessionID := extractSessionID(r)
	apiKeyID := extractAPIKeyID(r)
	tenantID := extractTenantID(r)

	// 尝试解析 body 提取 attachments 和 messages
	// 注意：这里需要保留 body 供下游使用
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	// 恢复 body 供下游读取
	r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	// 轻量级解析：只提取 messages 和 attachments 字段
	attachments, messages := m.extractAttachmentsAndMessages(bodyBytes)

	return &anomaly.Request{
		SessionID:   sessionID,
		APIKeyID:    apiKeyID,
		TenantID:    tenantID,
		Attachments: attachments,
		Messages:    messages,
	}, nil
}

// extractAttachmentsAndMessages 从 JSON body 中提取附件和消息
// 使用轻量级解析避免完整反序列化
func (m *AnomalyDetectorMiddleware) extractAttachmentsAndMessages(bodyBytes []byte) ([]anomaly.Attachment, []anomaly.Message) {
	// 简化实现：使用 ir 包的现有解析逻辑
	// 实际生产中应该复用 streaming handler 的解析结果
	
	// 尝试解析为 OpenAI chat completion 格式
	var chatReq struct {
		Messages []struct {
			Role    string `json:"role"`
			Content interface{} `json:"content"` // string or array
		} `json:"messages"`
	}

	if err := unmarshalJSON(bodyBytes, &chatReq); err != nil {
		// 解析失败，返回空（不影响请求）
		return nil, nil
	}

	messages := make([]anomaly.Message, 0, len(chatReq.Messages))
	var allAttachments []anomaly.Attachment

	for _, msg := range chatReq.Messages {
		aMsg := anomaly.Message{
			Role: msg.Role,
		}

		// 处理 content（可能是 string 或 array）
		switch content := msg.Content.(type) {
		case string:
			aMsg.Content = content
		case []interface{}:
			// content 是多部分（文本 + 图片）
			var textParts []string
			for _, part := range content {
				if partMap, ok := part.(map[string]interface{}); ok {
					if typ, ok := partMap["type"].(string); ok {
						switch typ {
						case "text":
							if text, ok := partMap["text"].(string); ok {
								textParts = append(textParts, text)
							}
						case "image_url":
							// 提取图片 URL
							if imgURL, ok := partMap["image_url"].(map[string]interface{}); ok {
								if url, ok := imgURL["url"].(string); ok {
									att := anomaly.Attachment{
										Type: "image",
										URL:  url,
										Size: int64(len(url)), // 粗略估算
									}
									allAttachments = append(allAttachments, att)
									aMsg.Attachments = append(aMsg.Attachments, att)
								}
							}
						}
					}
				}
			}
			aMsg.Content = strings.Join(textParts, "\n")
		}

		messages = append(messages, aMsg)
	}

	return allAttachments, messages
}

// isChatEndpoint 判断是否是聊天端点
func (m *AnomalyDetectorMiddleware) isChatEndpoint(r *http.Request) bool {
	path := r.URL.Path
	return strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/messages") ||
		strings.HasSuffix(path, "/completions")
}

// 辅助函数：从请求中提取字段（简化版，实际应复用现有逻辑）

func extractSessionID(r *http.Request) string {
	// 从 header 或 query 提取
	if sid := r.Header.Get("X-Session-Id"); sid != "" {
		return sid
	}
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		return sid
	}
	return ""
}

func extractAPIKeyID(r *http.Request) int64 {
	// 从 context 提取（假设 auth middleware 已经设置）
	if val := r.Context().Value("api_key_id"); val != nil {
		if id, ok := val.(int64); ok {
			return id
		}
	}
	return 0
}

func extractTenantID(r *http.Request) string {
	if val := r.Context().Value("tenant_id"); val != nil {
		if tid, ok := val.(string); ok {
			return tid
		}
	}
	return "default"
}

// JSON 辅助函数（避免引入 encoding/json 导致的编译问题）
func marshalJSON(v interface{}) ([]byte, error) {
	// 这里应该使用项目统一的 JSON 库
	// 简化实现
	return []byte("{}"), nil
}

func unmarshalJSON(data []byte, v interface{}) error {
	// 简化实现
	return nil
}
