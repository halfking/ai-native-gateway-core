package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ResponseFormatPreference 存储会话级别的响应格式偏好
type ResponseFormatPreference struct {
	mu    sync.RWMutex
	prefs map[string]string // session_id -> preferred_format ("openai-chat" | "anthropic-messages" | "openai-responses")
	// 过期清理
	expiry map[string]time.Time
}

// NewResponseFormatPreference 创建新的格式偏好管理器
func NewResponseFormatPreference() *ResponseFormatPreference {
	rfp := &ResponseFormatPreference{
		prefs:  make(map[string]string),
		expiry: make(map[string]time.Time),
	}
	go rfp.cleanupLoop()
	return rfp
}

// Get 获取会话的格式偏好
func (rfp *ResponseFormatPreference) Get(sessionID string) (string, bool) {
	rfp.mu.RLock()
	defer rfp.mu.RUnlock()

	pref, ok := rfp.prefs[sessionID]
	if !ok {
		return "", false
	}

	// 检查是否过期
	if expiry, exists := rfp.expiry[sessionID]; exists && time.Now().After(expiry) {
		return "", false
	}

	return pref, true
}

// Set 设置会话的格式偏好
func (rfp *ResponseFormatPreference) Set(sessionID, format string) {
	rfp.mu.Lock()
	defer rfp.mu.Unlock()

	rfp.prefs[sessionID] = format
	rfp.expiry[sessionID] = time.Now().Add(24 * time.Hour) // 24小时过期

	slog.Info("response_format_preference_set",
		"session_id", sessionID,
		"format", format,
		"expires_at", rfp.expiry[sessionID])
}

// Delete 删除会话的格式偏好
func (rfp *ResponseFormatPreference) Delete(sessionID string) {
	rfp.mu.Lock()
	defer rfp.mu.Unlock()

	delete(rfp.prefs, sessionID)
	delete(rfp.expiry, sessionID)
}

// cleanupLoop 定期清理过期的偏好
func (rfp *ResponseFormatPreference) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		rfp.cleanup()
	}
}

func (rfp *ResponseFormatPreference) cleanup() {
	rfp.mu.Lock()
	defer rfp.mu.Unlock()

	now := time.Now()
	for sessionID, expiry := range rfp.expiry {
		if now.After(expiry) {
			delete(rfp.prefs, sessionID)
			delete(rfp.expiry, sessionID)
		}
	}
}

// IRConverter 接口定义(与 executors.IRConverter 兼容)
type IRConverter interface {
	ParseOpenAI(body []byte) (*ir.InternalRequest, error)
	ParseAnthropic(body []byte) (*ir.InternalRequest, error)
	SerializeOpenAI(req *ir.InternalRequest) ([]byte, error)
	SerializeAnthropic(req *ir.InternalRequest) ([]byte, error)
	ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error)
	ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error)
	SerializeOpenAIResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error)
	SerializeAnthropicResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error)
	SerializeResponsesResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error)
}

// AdaptiveResponseConverter 自适应响应格式转换器
type AdaptiveResponseConverter struct {
	prefs *ResponseFormatPreference
	ir    IRConverter
}

// NewAdaptiveResponseConverter 创建自适应转换器
func NewAdaptiveResponseConverter(prefs *ResponseFormatPreference, ir IRConverter) *AdaptiveResponseConverter {
	return &AdaptiveResponseConverter{
		prefs: prefs,
		ir:    ir,
	}
}

// FormatAttempt 表示一次格式尝试
type FormatAttempt struct {
	Format string
	Body   []byte
	Error  error
}

// ConvertResponse 自适应转换响应格式
// 策略:
// 1. 如果有会话偏好,直接用偏好格式
// 2. 如果没有偏好,用检测到的客户端协议
// 3. 如果转换失败,按 openai-chat → openai-responses → anthropic-messages 顺序重试
// 4. 成功后记录偏好
func (arc *AdaptiveResponseConverter) ConvertResponse(
	ctx context.Context,
	sessionID string,
	clientProtocol string,
	upstreamBody []byte,
	upstreamProtocol string,
	clientModel string,
) ([]byte, string, error) {
	if arc.ir == nil {
		return nil, "", fmt.Errorf("IR converter not configured")
	}

	// 1. 检查会话偏好
	preferredFormat, hasPref := arc.prefs.Get(sessionID)
	if hasPref {
		slog.Info("using_session_format_preference",
			"session_id", sessionID,
			"preferred_format", preferredFormat)

		body, err := arc.convertToFormat(upstreamBody, upstreamProtocol, preferredFormat, clientModel)
		if err == nil {
			return body, preferredFormat, nil
		}

		// 偏好格式失败,清除偏好并重试
		slog.Warn("session_format_preference_failed",
			"session_id", sessionID,
			"preferred_format", preferredFormat,
			"error", err)
		arc.prefs.Delete(sessionID)
	}

	// 2. 尝试检测到的客户端协议
	if clientProtocol != "" && clientProtocol != upstreamProtocol {
		body, err := arc.convertToFormat(upstreamBody, upstreamProtocol, clientProtocol, clientModel)
		if err == nil {
			arc.prefs.Set(sessionID, clientProtocol)
			return body, clientProtocol, nil
		}

		slog.Warn("detected_format_failed",
			"session_id", sessionID,
			"client_protocol", clientProtocol,
			"error", err)
	}

	// 3. 按顺序尝试所有格式
	fallbackFormats := []string{"openai-chat", "openai-responses", "anthropic-messages"}

	var attempts []FormatAttempt
	for _, format := range fallbackFormats {
		if format == clientProtocol {
			continue // 已经尝试过
		}

		body, err := arc.convertToFormat(upstreamBody, upstreamProtocol, format, clientModel)
		attempts = append(attempts, FormatAttempt{
			Format: format,
			Body:   body,
			Error:  err,
		})

		if err == nil {
			slog.Info("fallback_format_succeeded",
				"session_id", sessionID,
				"format", format,
				"client_protocol", clientProtocol)

			arc.prefs.Set(sessionID, format)
			return body, format, nil
		}
	}

	// 4. 所有格式都失败,返回最详细的错误
	return nil, "", fmt.Errorf("all response formats failed: client_protocol=%s, attempts=%d, last_error=%v",
		clientProtocol, len(attempts), attempts[len(attempts)-1].Error)
}

// convertToFormat 转换到指定格式
func (arc *AdaptiveResponseConverter) convertToFormat(
	upstreamBody []byte,
	upstreamProtocol string,
	targetFormat string,
	clientModel string,
) ([]byte, error) {
	// 如果上游和目标格式相同,直接返回
	if upstreamProtocol == targetFormat {
		return upstreamBody, nil
	}

	// 1. 上游 → IR
	var irResp *ir.InternalResponse
	var err error

	switch upstreamProtocol {
	case "anthropic-messages", "anthropic":
		irResp, err = arc.ir.ParseAnthropicResponse(upstreamBody)
	case "openai-chat", "openai", "openai-completions":
		irResp, err = arc.ir.ParseOpenAIResponse(upstreamBody)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %s", upstreamProtocol)
	}

	if err != nil {
		return nil, fmt.Errorf("parse upstream response: %w", err)
	}

	// 2. IR → 目标格式
	switch targetFormat {
	case "openai-chat", "openai", "openai-completions":
		return arc.ir.SerializeOpenAIResponse(irResp, clientModel)
	case "anthropic-messages", "anthropic":
		return arc.ir.SerializeAnthropicResponse(irResp, clientModel)
	case "openai-responses", "responses":
		return arc.ir.SerializeResponsesResponse(irResp, clientModel)
	default:
		return nil, fmt.Errorf("unsupported target format: %s", targetFormat)
	}
}

// ConvertStreamChunk 自适应转换流式响应块
func (arc *AdaptiveResponseConverter) ConvertStreamChunk(
	sessionID string,
	clientProtocol string,
	upstreamChunk []byte,
	upstreamProtocol string,
	requestID string,
	clientModel string,
	createdAt int64,
) (string, string, error) {
	// 1. 检查会话偏好
	preferredFormat, hasPref := arc.prefs.Get(sessionID)
	if !hasPref {
		preferredFormat = clientProtocol
	}

	// 2. 解析上游块 → IR
	var chunk *ir.StreamChunk
	var err error

	switch upstreamProtocol {
	case "anthropic-messages", "anthropic":
		// 需要从 SSE 格式解析
		eventType, data := parseSSELine(upstreamChunk)
		chunk, err = ir.ParseAnthropicStreamEvent(eventType, data)
	case "openai-chat", "openai":
		chunk, err = ir.ParseOpenAIStreamChunk(string(upstreamChunk))
	default:
		return "", "", fmt.Errorf("unsupported upstream protocol: %s", upstreamProtocol)
	}

	if err != nil {
		return "", "", fmt.Errorf("parse upstream chunk: %w", err)
	}

	// 3. IR → 目标格式 SSE
	var sseLine string
	switch preferredFormat {
	case "openai-chat", "openai", "openai-completions":
		sseLine = chunk.SerializeOpenAI(requestID, clientModel, createdAt)
	case "anthropic-messages", "anthropic":
		sseLine = chunk.SerializeAnthropic(requestID, clientModel)
	case "openai-responses", "responses":
		itemID := deriveResponsesMessageID(requestID)
		sseLine = chunk.SerializeResponses(itemID)
	default:
		return "", "", fmt.Errorf("unsupported target format: %s", preferredFormat)
	}

	return sseLine, preferredFormat, nil
}

// parseSSELine 解析 SSE 行
func parseSSELine(line []byte) (eventType string, data []byte) {
	str := string(line)
	if len(str) == 0 {
		return "", nil
	}

	// event: message_start
	if len(str) > 7 && str[:7] == "event: " {
		return str[7:], nil
	}

	// data: {...}
	if len(str) > 6 && str[:6] == "data: " {
		return "", []byte(str[6:])
	}

	return "", line
}

// deriveResponsesMessageID 派生 Responses API 的 message ID
func deriveResponsesMessageID(requestID string) string {
	if len(requestID) >= 24 {
		return "msg_" + requestID[8:24]
	}
	return "msg_" + requestID
}

// ValidateResponseFormat 验证响应格式是否匹配客户端期望
// 用于检测客户端是否接受了响应(通过后续请求判断)
func ValidateResponseFormat(body []byte, expectedFormat string) error {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	switch expectedFormat {
	case "openai-chat", "openai":
		if _, ok := data["choices"]; !ok {
			return fmt.Errorf("missing 'choices' field for OpenAI format")
		}
	case "anthropic-messages", "anthropic":
		if _, ok := data["content"]; !ok {
			return fmt.Errorf("missing 'content' field for Anthropic format")
		}
	case "openai-responses", "responses":
		if _, ok := data["output_text"]; !ok {
			return fmt.Errorf("missing 'output_text' field for Responses format")
		}
	}

	return nil
}
