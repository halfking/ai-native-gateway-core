package session

import (
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// SessionContext 是贯穿整个请求生命周期的上下文对象
// 包含了从客户端请求到最终响应的所有数据快照和状态信息
type SessionContext struct {
	// ─── 基础标识 ───
	SessionID string // 会话 ID（gw_xxx 格式）
	RequestID string // 请求 ID（唯一标识单次请求）
	TenantID  string // 租户 ID
	APIKeyID  int    // API Key ID

	// ─── 当前状态 ───
	State       SessionState      // 当前状态
	Transitions []StateTransition // 状态转换历史

	// ─── 客户端信息 ───
	ClientType     string // 客户端类型：cursor, windsurf, copilot, vscode 等
	ClientProtocol string // 客户端协议：openai-chat, anthropic-messages 等
	ClientModel    string // 客户端请求的模型名

	// ─── 上游信息 ───
	UpstreamProtocol string // 上游协议：openai-chat, anthropic-messages 等
	UpstreamModel    string // 实际转发的模型名
	CredentialID     int    // 选中的凭据 ID
	ProviderID       int    // 提供商 ID

	// ─── 数据快照（每个阶段更新）───
	// 客户端原始请求
	ClientRawBody []byte                 // 原始 JSON 字节
	ClientIR      *ir.InternalRequest    // 解析后的 IR
	ClientHeaders http.Header            // 客户端请求头
	ClientMethod  string                 // HTTP 方法
	ClientPath    string                 // 请求路径

	// 上游请求
	UpstreamBody []byte              // 转换后的上游请求体
	UpstreamIR   *ir.InternalRequest // 上游请求 IR

	// LLM 响应
	LLMRawResponse   []byte               // 上游原始响应
	LLMResponseIR    *ir.InternalResponse // 解析后的响应 IR
	LLMStatusCode    int                  // 上游 HTTP 状态码
	LLMResponseTime  time.Duration        // LLM 响应时间

	// 客户端响应
	ClientResponseIR *ir.InternalResponse // 转换后的响应 IR
	ClientFinalBody  []byte               // 最终发送给客户端的字节

	// ─── 附件元数据 ───
	Attachments []attachments.AttachmentMetadata // 附件列表（只存元数据）

	// ─── 流式状态 ───
	IsStreaming   bool                  // 是否流式请求
	StreamCapture *audit.StreamCapture  // 流式捕获器

	// ─── 时间戳 ───
	CreatedAt         time.Time // 请求开始时间
	ClientReceivedAt  time.Time // 收到客户端请求时间
	LLMSentAt         time.Time // 发送给 LLM 时间
	LLMReceivedAt     time.Time // 收到 LLM 响应时间
	ClientRespondedAt time.Time // 响应客户端完成时间

	// ─── 扩展元数据 ───
	// 用于在不同组件间传递额外信息
	Metadata map[string]any

	// ─── 错误信息 ───
	Error error // 如果状态是 StateError，这里记录错误
}

// NewSessionContext 创建新的会话上下文
func NewSessionContext(r *http.Request) *SessionContext {
	now := time.Now()
	return &SessionContext{
		State:            StateInitial,
		CreatedAt:        now,
		ClientReceivedAt: now,
		ClientHeaders:    r.Header.Clone(),
		ClientMethod:     r.Method,
		ClientPath:       r.URL.Path,
		Metadata:         make(map[string]any),
		Transitions:      make([]StateTransition, 0, 10),
	}
}

// Duration 返回请求总耗时
func (sc *SessionContext) Duration() time.Duration {
	if sc.ClientRespondedAt.IsZero() {
		return time.Since(sc.CreatedAt)
	}
	return sc.ClientRespondedAt.Sub(sc.CreatedAt)
}

// UpstreamDuration 返回上游调用耗时
func (sc *SessionContext) UpstreamDuration() time.Duration {
	if sc.LLMSentAt.IsZero() || sc.LLMReceivedAt.IsZero() {
		return 0
	}
	return sc.LLMReceivedAt.Sub(sc.LLMSentAt)
}

// TransformDuration 返回转换总耗时（客户端→上游 + 上游→客户端）
func (sc *SessionContext) TransformDuration() time.Duration {
	total := sc.Duration()
	upstream := sc.UpstreamDuration()
	if upstream == 0 {
		return 0
	}
	return total - upstream
}

// GetMetadata 安全地获取元数据
func (sc *SessionContext) GetMetadata(key string) (any, bool) {
	if sc.Metadata == nil {
		return nil, false
	}
	val, ok := sc.Metadata[key]
	return val, ok
}

// SetMetadata 安全地设置元数据
func (sc *SessionContext) SetMetadata(key string, value any) {
	if sc.Metadata == nil {
		sc.Metadata = make(map[string]any)
	}
	sc.Metadata[key] = value
}

// GetStateHistory 返回状态转换历史摘要
func (sc *SessionContext) GetStateHistory() []string {
	if len(sc.Transitions) == 0 {
		return []string{sc.State.String()}
	}
	history := make([]string, len(sc.Transitions))
	for i, t := range sc.Transitions {
		history[i] = t.To.String()
	}
	return history
}

// IsError 返回当前是否处于错误状态
func (sc *SessionContext) IsError() bool {
	return sc.State == StateError
}

// IsCompleted 返回当前是否已完成
func (sc *SessionContext) IsCompleted() bool {
	return sc.State == StateCompleted
}

// MarkError 标记为错误状态并记录错误
func (sc *SessionContext) MarkError(err error) {
	sc.State = StateError
	sc.Error = err
}
