package ursm

import "errors"

// 业务错误常量
var (
	// 路由决策错误
	ErrNoAvailableNodes     = errors.New("no available nodes for routing")
	ErrFpSlotSaturated      = errors.New("fingerprint slot saturated")
	ErrConcurrencySaturated = errors.New("concurrency slot saturated")

	// 状态错误
	ErrProviderDisabled      = errors.New("provider disabled")
	ErrCredentialUnavailable = errors.New("credential unavailable")
	ErrModelUnavailable      = errors.New("model unavailable")
	ErrNodeUnavailable       = errors.New("node unavailable")
	ErrCredentialNotFound    = errors.New("credential not found")
	ErrProviderNotFound      = errors.New("provider not found")

	// 系统错误
	ErrInternalError  = errors.New("internal error")
	ErrDatabaseError  = errors.New("database error")
	ErrCacheError     = errors.New("cache error")
	ErrInvalidRequest = errors.New("invalid request")
	ErrUnauthorized   = errors.New("unauthorized")
)
