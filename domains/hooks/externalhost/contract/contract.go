// Package contract 定义外部钩子（进程外 hook host）的宿主无关合同。
//
// 定位（对应《方案-llm-gateway-go会话能力整合与插件化优化-2026-10-28》W3.1-W3.4）：
//
//	W3.1 合同先行：onRequest(body, ctx) / onResponse(reply, ctx) 两个钩子点，
//	      ctx 携带 protocol/model/agent/session/origin/abort。
//	W3.2 回滚语义：onRequest 可改写请求体；改 model/stream/工具声明 → 整体回滚记失败；
//	      超时/崩溃 → 原样直通；失败钩子本请求不再收 onResponse。
//	W3.3 onResponse 捕获合同：有界副本（8MiB）、fire-and-forget 不阻塞 EOF、
//	      ctx.state 串联 onRequest/onResponse、客户端先挂断仍完成捕获。
//	W3.4 origin 分类：local/gateway-key/remote，防伪造、不泄钥；
//	      钩子默认跳过 remote 来源的正文捕获（隐私原则）。
//
// 本包是纯合同层：不引入宿主实现，不依赖任何运行时（Go RPC / Bun / WASM 均可实现同一合同）。
// 宿主（B 路径 Go RPC）见父包 externalhost。
//
// 合同版本升级规则：任何影响线上互通的字段语义变更都必须提升 ContractVersion，
// 宿主与钩子实现方在握手阶段比对版本，不匹配即拒绝装载（fail-closed）。
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContractVersion 是外部钩子合同的线协议版本。
// 宿主与钩子实现方必须在握手/首帧声明同一版本，否则拒绝装载。
const ContractVersion = "gateway-external-hook-v1"

// ContractPath 是钩子实现方暴露钩子点时使用的路径前缀。
// 约定：<base>/hooks/v1/on_request 与 <base>/hooks/v1/on_response。
const ContractPath = "/hooks/v1"

// 默认限额与上限（W3.2 / W3.3）：
//
//	DefaultTimeout：默认 1s，可配区间 100ms-10s。
//	MinTimeout / MaxTimeout：可配区间的硬边界。
//	MaxBodyBytes：正文捕获有界副本上限 8MiB。
const (
	DefaultTimeout = time.Second
	MinTimeout     = 100 * time.Millisecond
	MaxTimeout     = 10 * time.Second
	MaxBodyBytes   = 8 << 20
)

// HookPoint 标识外部钩子的调用点。
type HookPoint string

const (
	// HookPointOnRequest 在协议翻译前调用（与 magpie recordConversation 同位）。
	HookPointOnRequest HookPoint = "on_request"
	// HookPointOnResponse 在上游响应返回后调用。
	HookPointOnResponse HookPoint = "on_response"
)

// IsValid 判断钩子点是否合法。
func (p HookPoint) IsValid() bool {
	switch p {
	case HookPointOnRequest, HookPointOnResponse:
		return true
	default:
		return false
	}
}

// Path 返回该钩子点在钩子实现方侧的相对路径。
func (p HookPoint) Path() string {
	return ContractPath + "/" + string(p)
}

// Origin 标识请求来源分类（W3.4）。
//
// 防伪造：origin 由网关认证中间件判定并写入，钩子实现方不得自行推断，
// 也不得据此猜测任何密钥；remote 语义为"第三方远程调用"。
type Origin string

const (
	// OriginLocal 本地直连（受信网络内的调用方）。
	OriginLocal Origin = "local"
	// OriginGatewayKey 使用网关自身密钥调用（系统内部）。
	OriginGatewayKey Origin = "gateway-key"
	// OriginRemote 第三方远程调用。
	OriginRemote Origin = "remote"
)

// IsValid 判断 origin 取值是否合法。未知取值一律拒绝（fail-closed）。
func (o Origin) IsValid() bool {
	switch o {
	case OriginLocal, OriginGatewayKey, OriginRemote:
		return true
	default:
		return false
	}
}

// AllowsBodyCapture 返回该来源是否允许捕获请求/响应正文。
//
// 隐私原则：remote 来源默认不捕获正文。宿主侧还应按需进一步收紧。
func (o Origin) AllowsBodyCapture() bool {
	return o != OriginRemote
}

// Decision 是钩子对当前请求的处置结论。
type Decision string

const (
	// DecisionContinue 原样放行。
	DecisionContinue Decision = "continue"
	// DecisionRewrite 改写请求/响应正文后放行。
	DecisionRewrite Decision = "rewrite"
	// DecisionBlock 中止请求。
	DecisionBlock Decision = "block"
)

// IsValid 判断处置结论是否合法。
func (d Decision) IsValid() bool {
	switch d {
	case DecisionContinue, DecisionRewrite, DecisionBlock:
		return true
	default:
		return false
	}
}

// Context 是钩子调用上下文（W3.1）。
//
// state 用于串联 onRequest 与 onResponse（W3.3）：onRequest 阶段写入，
// onResponse 阶段原样带回，实现无状态的钩子也能做请求-响应配对。
type Context struct {
	// Protocol 上游协议（anthropic/openai/…）。
	Protocol string `json:"protocol"`
	// Model 目标模型。
	Model string `json:"model"`
	// Agent 发起方 agent 标识（可空）。
	Agent string `json:"agent,omitempty"`
	// Session 会话标识（可空）。
	Session string `json:"session,omitempty"`
	// RequestID 请求标识。
	RequestID string `json:"request_id"`
	// TenantID 租户标识。
	TenantID string `json:"tenant_id,omitempty"`
	// Origin 请求来源分类（W3.4）。
	Origin Origin `json:"origin"`
	// Abort 置位表示请求已被上游链路中止，钩子应尽快返回。
	Abort bool `json:"abort,omitempty"`
	// State onRequest 阶段产出、onResponse 阶段带回的串联状态。
	State map[string]string `json:"state,omitempty"`
	// DeadlineUnixNano 绝对截止时间（Unix 纳秒）；0 表示无截止时间。
	DeadlineUnixNano int64 `json:"deadline_unix_nano,omitempty"`
}

// Deadline 返回上下文的绝对截止时间；未设置时返回零值。
func (c *Context) Deadline() time.Time {
	if c == nil || c.DeadlineUnixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, c.DeadlineUnixNano)
}

// Payload 是请求或响应的正文载荷。
type Payload struct {
	// Body 正文原始字节。
	Body []byte `json:"body,omitempty"`
	// Truncated 置位表示正文超出 MaxBodyBytes，已按有界副本截断。
	Truncated bool `json:"truncated,omitempty"`
	// OriginalBytes 正文原始长度（截断时大于 len(Body)）。
	OriginalBytes int `json:"original_bytes,omitempty"`
}

// RequestPayload 是 onRequest 钩子点的载荷。
type RequestPayload struct {
	// Method 上游请求方法。
	Method string `json:"method,omitempty"`
	// Path 上游请求路径。
	Path string `json:"path,omitempty"`
	// Headers 请求头（已按脱敏策略过滤，钩子不得依赖任何鉴权头）。
	Headers map[string]string `json:"headers,omitempty"`
	// Body 请求正文。
	Body Payload `json:"body"`
	// Model 便于钩子按模型决策的冗余字段（与 Context.Model 同源）。
	Model string `json:"model,omitempty"`
	// Stream 标记是否为流式请求。
	Stream bool `json:"stream,omitempty"`
	// ToolDeclarations 工具声明摘要。改动它属于受保护字段（见 ProtectedField）。
	ToolDeclarations []string `json:"tool_declarations,omitempty"`
}

// ResponsePayload 是 onResponse 钩子点的载荷。
type ResponsePayload struct {
	// Status 上游响应状态码。
	Status int `json:"status,omitempty"`
	// Headers 响应头（已按脱敏策略过滤）。
	Headers map[string]string `json:"headers,omitempty"`
	// Body 响应正文。
	Body Payload `json:"body"`
	// Model 响应实际命中的模型（可能与请求模型不同）。
	Model string `json:"model,omitempty"`
	// Stream 标记是否为流式响应。
	Stream bool `json:"stream,omitempty"`
}

// ProtectedField 标识不可被外部钩子改写的受保护字段（W3.2）。
//
// 语义：钩子若改写这些字段，整体回滚并记失败——因为它们决定路由、流式形态
// 与工具能力，属于网关治理边界，不是钩子业务语义。
type ProtectedField string

const (
	// ProtectedFieldModel 模型选择。
	ProtectedFieldModel ProtectedField = "model"
	// ProtectedFieldStream 流式形态开关。
	ProtectedFieldStream ProtectedField = "stream"
	// ProtectedFieldTools 工具声明集合。
	ProtectedFieldTools ProtectedField = "tools"
)

// AllProtectedFields 返回全部受保护字段。
func AllProtectedFields() []ProtectedField {
	return []ProtectedField{ProtectedFieldModel, ProtectedFieldStream, ProtectedFieldTools}
}

// Invocation 是网关 → 钩子实现方的调用信封。
type Invocation struct {
	// ContractVersion 合同版本。
	ContractVersion string `json:"contract_version"`
	// HookPoint 钩子点。
	HookPoint HookPoint `json:"hook_point"`
	// Context 调用上下文。
	Context *Context `json:"context"`
	// Request 请求载荷（on_request 用）。
	Request *RequestPayload `json:"request,omitempty"`
	// Response 响应载荷（on_response 用）。
	Response *ResponsePayload `json:"response,omitempty"`
}

// RequestPatch 是 on_request 回复中的请求改写补丁。
//
// 语义（指针式，缺省即"不改写"）：
//
//   - 字段为 nil → 该字段保持原值。
//   - 字段非 nil → 该字段被改写；显式指向零值（如 "" / false）表示"清空"。
//
// 采用补丁而非整体替换的原因：整体替换会让"钩子只想改正文却忘了回填 model"
// 被判成受保护字段变更而回滚，语义含混。补丁语义下，
// 钩子改写 model 必须显式给出 model，受保护字段的守卫因此无歧义。
type RequestPatch struct {
	// Method 上游请求方法。
	Method *string `json:"method,omitempty"`
	// Path 上游请求路径。
	Path *string `json:"path,omitempty"`
	// Headers 请求头（整体替换）。
	Headers map[string]string `json:"headers,omitempty"`
	// Body 请求正文。
	Body *Payload `json:"body,omitempty"`
	// Model 模型选择；非 nil 即视为改写（受保护字段）。
	Model *string `json:"model,omitempty"`
	// Stream 流式形态；非 nil 即视为改写（受保护字段）。
	Stream *bool `json:"stream,omitempty"`
	// ToolDeclarations 工具声明；非 nil 即视为改写（受保护字段）。
	ToolDeclarations *[]string `json:"tool_declarations,omitempty"`
}

// ResponsePatch 是 on_response 回复中的响应改写补丁，语义同 RequestPatch。
type ResponsePatch struct {
	// Status 响应状态码。
	Status *int `json:"status,omitempty"`
	// Headers 响应头（整体替换）。
	Headers map[string]string `json:"headers,omitempty"`
	// Body 响应正文。
	Body *Payload `json:"body,omitempty"`
	// Model 响应实际命中的模型。
	Model *string `json:"model,omitempty"`
	// Stream 流式形态。
	Stream *bool `json:"stream,omitempty"`
}

// Reply 是钩子实现方 → 网关的回复信封。
type Reply struct {
	// ContractVersion 合同版本（必须与 Invocation 一致）。
	ContractVersion string `json:"contract_version"`
	// Decision 处置结论。
	Decision Decision `json:"decision"`
	// Request 请求改写补丁（decision=rewrite 时必填其一）。
	Request *RequestPatch `json:"request,omitempty"`
	// Response 响应改写补丁（decision=rewrite 时必填其一）。
	Response *ResponsePatch `json:"response,omitempty"`
	// Reason 拒绝/中止原因（decision=block 时建议填写）。
	Reason string `json:"reason,omitempty"`
	// Error 钩子实现方内部错误描述（不携带敏感载荷）。
	Error string `json:"error,omitempty"`
}

// FailureKind 描述钩子失败/回滚的归因类别。
type FailureKind string

const (
	// FailureNone 无失败。
	FailureNone FailureKind = ""
	// FailureTimeout 调用超时（W3.2：超时原样直通）。
	FailureTimeout FailureKind = "timeout"
	// FailureCrash 宿主进程崩溃/连接断开（W3.2：原样直通）。
	FailureCrash FailureKind = "crash"
	// FailureProtocol 协议违规（版本不匹配、字段非法、载荷超限）。
	FailureProtocol FailureKind = "protocol"
	// FailureProtectedField 触碰受保护字段（W3.2：整体回滚记失败）。
	FailureProtectedField FailureKind = "protected_field"
	// FailureHook钩子实现方自身返回错误。
	FailureHook FailureKind = "hook"
)

// ErrContractVersionMismatch 表示合同版本不一致。
var ErrContractVersionMismatch = errors.New("external hook contract version mismatch")

// ErrProtectedField 表示钩子改写了受保护字段。
var ErrProtectedField = errors.New("external hook touched protected field")

// ErrPayloadTooLarge 表示载荷超过有界副本上限。
var ErrPayloadTooLarge = errors.New("external hook payload exceeds limit")

// ErrInvalidDecision 表示处置结论非法。
var ErrInvalidDecision = errors.New("external hook returned invalid decision")

// ValidateInvocation校验调用信封合法性。
func ValidateInvocation(inv *Invocation) error {
	if inv == nil {
		return errors.New("external hook invocation is nil")
	}
	if inv.ContractVersion != ContractVersion {
		return fmt.Errorf("%w: got %q want %q", ErrContractVersionMismatch, inv.ContractVersion, ContractVersion)
	}
	if !inv.HookPoint.IsValid() {
		return fmt.Errorf("external hook: invalid hook point %q", inv.HookPoint)
	}
	if inv.Context == nil {
		return errors.New("external hook invocation: context is required")
	}
	if inv.Context.RequestID == "" {
		return errors.New("external hook invocation: context.request_id is required")
	}
	if !inv.Context.Origin.IsValid() {
		return fmt.Errorf("external hook invocation: invalid origin %q", inv.Context.Origin)
	}
	switch inv.HookPoint {
	case HookPointOnRequest:
		if inv.Request == nil {
			return errors.New("external hook invocation: on_request requires request payload")
		}
		if inv.Response != nil {
			return errors.New("external hook invocation: on_request must not carry response payload")
		}
	case HookPointOnResponse:
		if inv.Response == nil {
			return errors.New("external hook invocation: on_response requires response payload")
		}
		if inv.Request != nil {
			return errors.New("external hook invocation: on_response must not carry request payload")
		}
	}
	return nil
}

// ValidateReply 校验回复信封合法性。
func ValidateReply(reply *Reply) error {
	if reply == nil {
		return errors.New("external hook reply is nil")
	}
	if reply.ContractVersion != ContractVersion {
		return fmt.Errorf("%w: got %q want %q", ErrContractVersionMismatch, reply.ContractVersion, ContractVersion)
	}
	if !reply.Decision.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidDecision, reply.Decision)
	}
	switch reply.Decision {
	case DecisionRewrite:
		if reply.Request == nil && reply.Response == nil {
			return errors.New("external hook reply: rewrite requires a rewritten payload")
		}
	case DecisionBlock:
		if strings.TrimSpace(reply.Reason) == "" {
			return errors.New("external hook reply: block requires a reason")
		}
	}
	return nil
}

// NormalizeTimeout 把配置的超时收敛到 [MinTimeout, MaxTimeout] 区间。
// 非正值按 DefaultTimeout 处理；越界值收敛到边界而不是报错（配置容错）。
func NormalizeTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d < MinTimeout {
		return MinTimeout
	}
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}

// BoundedCopy 返回正文的有界副本（W3.3）。
//
// 超出 MaxBodyBytes 时按前缀截断并置 Truncated=true，OriginalBytes 记录原长度。
// 不修改入参切片。
func BoundedCopy(body []byte) Payload {
	if len(body) <= MaxBodyBytes {
		out := make([]byte, len(body))
		copy(out, body)
		return Payload{Body: out, OriginalBytes: len(out)}
	}
	out := make([]byte, MaxBodyBytes)
	copy(out, body[:MaxBodyBytes])
	return Payload{Body: out, Truncated: true, OriginalBytes: len(body)}
}

// MergeRequest 把请求改写补丁应用到原载荷，返回生效后的新载荷。
// 原载荷不被修改；补丁为 nil 时返回原载荷的副本。
func MergeRequest(base *RequestPayload, patch *RequestPatch) *RequestPayload {
	if base == nil {
		return nil
	}
	out := *base
	out.Headers = copyStringMap(base.Headers)
	out.ToolDeclarations = append([]string(nil), base.ToolDeclarations...)
	if patch == nil {
		return &out
	}
	if patch.Method != nil {
		out.Method = *patch.Method
	}
	if patch.Path != nil {
		out.Path = *patch.Path
	}
	if patch.Headers != nil {
		out.Headers = copyStringMap(patch.Headers)
	}
	if patch.Body != nil {
		out.Body = *patch.Body
	}
	if patch.Model != nil {
		out.Model = *patch.Model
	}
	if patch.Stream != nil {
		out.Stream = *patch.Stream
	}
	if patch.ToolDeclarations != nil {
		out.ToolDeclarations = append([]string(nil), (*patch.ToolDeclarations)...)
	}
	return &out
}

// MergeResponse 把响应改写补丁应用到原载荷，返回生效后的新载荷。
func MergeResponse(base *ResponsePayload, patch *ResponsePatch) *ResponsePayload {
	if base == nil {
		return nil
	}
	out := *base
	out.Headers = copyStringMap(base.Headers)
	if patch == nil {
		return &out
	}
	if patch.Status != nil {
		out.Status = *patch.Status
	}
	if patch.Headers != nil {
		out.Headers = copyStringMap(patch.Headers)
	}
	if patch.Body != nil {
		out.Body = *patch.Body
	}
	if patch.Model != nil {
		out.Model = *patch.Model
	}
	if patch.Stream != nil {
		out.Stream = *patch.Stream
	}
	return &out
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// CheckProtectedFields 比对改写前后的载荷，返回被触碰的受保护字段。
//
// W3.2：命中任一受保护字段即整体回滚并记失败（FailureProtectedField）。
// 语义细节：
//   - model：改写即触碰（含清空）。
//   - stream：布尔形态变化即触碰。
//   - tools：工具声明集合变化即触碰（顺序不敏感，按集合比较）。
func CheckProtectedFields(before, after *RequestPayload) []ProtectedField {
	if before == nil || after == nil {
		return nil
	}
	var touched []ProtectedField
	if before.Model != after.Model {
		touched = append(touched, ProtectedFieldModel)
	}
	if before.Stream != after.Stream {
		touched = append(touched, ProtectedFieldStream)
	}
	if !sameStringSet(before.ToolDeclarations, after.ToolDeclarations) {
		touched = append(touched, ProtectedFieldTools)
	}
	return touched
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// EncodeInvocation 把调用信封编码为 JSON。
func EncodeInvocation(inv *Invocation) ([]byte, error) {
	return json.Marshal(inv)
}

// DecodeReply 解码回复信封。
func DecodeReply(data []byte) (*Reply, error) {
	var reply Reply
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("external hook: decode reply: %w", err)
	}
	return &reply, nil
}
