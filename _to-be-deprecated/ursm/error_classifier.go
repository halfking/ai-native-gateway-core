package ursm

// ErrorClass 错误分类
type ErrorClass int

const (
	// ErrorClassIgnored 忽略的错误（不影响节点状态）
	ErrorClassIgnored ErrorClass = iota
	// ErrorClassTransient 临时故障（可恢复）
	ErrorClassTransient
	// ErrorClassPermanent 永久故障（不可恢复）
	ErrorClassPermanent
)

// String 实现Stringer接口
func (e ErrorClass) String() string {
	switch e {
	case ErrorClassIgnored:
		return "ignored"
	case ErrorClassTransient:
		return "transient"
	case ErrorClassPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// ClassifyError 分类错误类型
// 根据错误类型决定是否影响节点可用性以及如何影响
func ClassifyError(errorKind string) ErrorClass {
	if errorKind == "" {
		return ErrorClassIgnored
	}

	// 忽略：用户取消、客户端错误
	switch errorKind {
	case "canceled",
		"client_bug",
		"model_not_found_client",
		"invalid_request",
		"context_deadline_exceeded":
		return ErrorClassIgnored
	}

	// 永久故障：认证失败、模型不存在、永久配额耗尽
	switch errorKind {
	case "auth",
		"auth_failed",
		"auth_revoked",
		"auth_invalid",
		"model_not_found",
		"model_not_supported",
		"quota_permanent",
		"quota_permanently_exhausted",
		"provider_disabled",
		"credential_suspended":
		return ErrorClassPermanent
	}

	// 临时故障：限流、上游故障、超时
	switch errorKind {
	case "rate_limit",
		"rate_limited",
		"upstream_down",
		"upstream_error",
		"upstream_timeout",
		"timeout",
		"stream_timeout",
		"network_error",
		"connection_error",
		"quota_periodic",
		"quota_balance",
		"service_unavailable",
		"internal_error":
		return ErrorClassTransient
	}

	// 保守：未知错误视为临时故障
	return ErrorClassTransient
}
