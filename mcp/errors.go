package mcp

import (
	"errors"
	"fmt"
	"net/http"
)

// JSON-RPC 2.0 错误码。
//
// ★ 分配原则（2026-11-09 逐字核对既有 tools/mcp-gateway 全仓后重排）：
//
//	既有实现**已占用**的码必须逐字沿用（既有客户端按码判分支，改号＝破坏契约）：
//	  -32000 上游/代理错误、-32001 bindings 拒绝、-32002 限流、
//	  -32003 tool-allow 拒绝、-32004 请求体超限(413)。
//	只有**既有未使用**的取值才允许分配给新增能力。
//
//	⚠ 此前本文件把 -32001 当成「新增码（工具不存在）」、把 -32004 当成
//	「新增码（审批缺失）」—— **两者都是既有已占用的码**（既有 -32001 是
//	bindings 拒绝、既有 -32004 是请求体超限）⇒ 本轮改正：既有码归位、
//	新增能力改用既有未使用的取值（-32005/-32006/-32007）。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// CodeUpstreamError 上游返回错误（含上游超时）。
	//
	// 取值 **-32000**：与既有 tools/mcp-gateway 的上游代理错误（gateway.js 的
	// "Gateway proxy error" / "Memora MCP upstream unavailable"、tool-proxy.js 的
	// "Tool proxy error"）逐字一致 —— 既有客户端按错误码判分支，不能改号。
	//
	// ★ 超时**不单列码**：既有的上游错误（含超时）统一是 -32000 ⇒ 拆码会打断
	//   既有客户端的 -32000 分支。超时与其它上游错误的区分放在
	//   `data.timeout=true` 与审计事件里，不动错误码。
	CodeUpstreamError = -32000
	// CodeBindingDenied 后端绑定被拒（key 失效/吊销、单边禁用绑定）。
	//
	// 取值 **-32001**：与既有 tools/mcp-gateway 的 bindings 执法逐字一致
	// （src/bindings.js 的 `jsonRpcError(res, null, -32001, ...)`，401/403 两处）。
	//
	// ★ 此前本实现把 bindings 拒绝归到 -32003 ⇒ 与既有不一致，已改正。
	CodeBindingDenied = -32001
	// CodeRateLimited 触发限流。
	//
	// 取值 **-32002**：与既有 tools/mcp-gateway 的限流错误（gateway.js 与
	// tool-proxy.js 两处）逐字一致。
	CodeRateLimited = -32002
	// CodeToolNotAllowed 工具不在允许集 / 不可见 / 缺 scope。
	//
	// 取值 **-32003**：与既有 tools/mcp-gateway 的 tool-allow 执法逐字一致
	// （src/tool-allow.js、src/gateway.js、src/tool-proxy.js 三处）。
	//
	// ★ 可见性与 scope 拒绝也归入本码：既有客户端对本码的语义是「工具不被
	//   允许」⇒ 命中合理，且避免为同族语义另开码。
	CodeToolNotAllowed = -32003
	// CodePayloadTooLarge 请求体超过执法/解析上限。
	//
	// 取值 **-32004**：与既有 tools/mcp-gateway 逐字一致
	// （src/gateway.js 的 tool-allow 预读体超限：HTTP 413 + code -32004）。
	//
	// ★ 此前本实现让请求体超限**静默截断**（LimitReader）⇒ 表现为 JSON 解析
	//   失败（-32700），把真因掩盖掉了；现改为显式 413 + -32004。
	CodePayloadTooLarge = -32004
	// CodeToolNotFound 工具不存在或未对调用方可见。
	//
	// ★ 本码是**新增**的（既有实现没有"工具不存在"这一分支：它的工具集由
	//   上游直接决定，找不到就由上游报错）。
	//   ★ 取值 -32005：**既有未使用**（既有用了 -32000/-32001/-32002/-32003/
	//   -32004）⇒ 新增能力只能落在未使用的取值上。
	CodeToolNotFound = -32005
	// CodeCircuitOpen 熔断器打开，暂不转发。
	//
	// ★ 本码是**新增**的（既有实现无熔断）。新增码占用 -32006。
	CodeCircuitOpen = -32006
	// CodeApprovalRequired 敏感工具缺少有效审批凭据。
	//
	// ★ 本码是**新增**的（既有实现没有审批这一层）。新增码占用 -32007
	//   （既有未使用）。★ 此前本实现声明了本码却从未使用（实际返回 -32003），
	//   而营销侧口径已对外发布 -32004 ⇒ 客户端按该码分支永不命中；本轮改正
	//   为「声明即启用」：审批闸实际返回 -32007。
	CodeApprovalRequired = -32007
)

// RPCError 是 JSON-RPC 错误体。
type RPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// Error 实现 error 接口。
func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp: jsonrpc error %d: %s", e.Code, e.Message)
}

// Errorf 构造一个 JSON-RPC 错误。
func Errorf(code int, format string, args ...any) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithData 附加数据并返回自身，便于链式构造。
func (e *RPCError) WithData(kv map[string]any) *RPCError {
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	for k, v := range kv {
		e.Data[k] = v
	}
	return e
}

// 哨兵错误：用于内部流程判定，不直接返回给客户端。
var (
	// ErrUnauthorized 身份无效或缺失。
	ErrUnauthorized = errors.New("mcp: unauthorized")
	// ErrForbidden 无权访问该资源。
	ErrForbidden = errors.New("mcp: forbidden")
	// ErrNotFound 资源不存在。
	ErrNotFound = errors.New("mcp: not found")
	// ErrConflict 资源冲突（如重名注册）。
	ErrConflict = errors.New("mcp: conflict")
	// ErrRateLimited 触发限流。
	ErrRateLimited = errors.New("mcp: rate limited")
	// ErrCredentialInactive 凭证存在但已失效/吊销（区别于「凭证不存在」）。
	//
	// ★ 既有 bindings.js 对「inactive or revoked」返回 401 + -32001 ⇒ 本实现
	//   同口径；「凭证不存在」在既有实现里不产生 JSON-RPC 错误（交由上游 401），
	//   本实现用 -32600（既有未占用该语义 ⇒ 保持既有标准段用法）。
	ErrCredentialInactive = errors.New("mcp: credential inactive or revoked")
)

// StatusError 把内部错误映射为 HTTP 状态码。
func StatusError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// RPCErrorOf 把内部错误映射为 JSON-RPC 错误。
func RPCErrorOf(err error) *RPCError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnauthorized):
		return Errorf(CodeInvalidRequest, "unauthorized")
	case errors.Is(err, ErrForbidden):
		return Errorf(CodeToolNotAllowed, "forbidden")
	case errors.Is(err, ErrNotFound):
		return Errorf(CodeToolNotFound, "not found")
	case errors.Is(err, ErrRateLimited):
		return Errorf(CodeRateLimited, "rate limited")
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return Errorf(CodeInternalError, "%s", err.Error())
}
