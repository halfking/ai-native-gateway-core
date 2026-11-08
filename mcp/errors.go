package mcp

import (
	"errors"
	"fmt"
	"net/http"
)

// JSON-RPC 2.0 错误码。
//
// 标准段沿用 JSON-RPC 2.0（-32700..-32600）；-32000 段保留给实现自定义。
// 其中 -32003 与既有 tools/mcp-gateway 的 tool-allow 执法错误码保持一致
// （见 tools/mcp-gateway/src/tool-allow.js），以便既有客户端与告警规则平移。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// CodeToolNotFound 工具不存在或未对调用方可见。
	CodeToolNotFound = -32001
	// CodeToolNotAllowed 工具不在允许集（key×tool 执法 / 可见性 / RBAC）。
	CodeToolNotAllowed = -32003
	// CodeApprovalRequired 敏感工具缺少有效审批凭据。
	CodeApprovalRequired = -32004
	// CodeUpstreamError 上游返回错误。
	CodeUpstreamError = -32005
	// CodeCircuitOpen 熔断器打开，暂不转发。
	CodeCircuitOpen = -32006
	// CodeUpstreamTimeout 上游超时。
	CodeUpstreamTimeout = -32007
	// CodeRateLimited 触发限流。
	CodeRateLimited = -32008
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
