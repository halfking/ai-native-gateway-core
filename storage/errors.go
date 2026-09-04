package storage

import "errors"

// 存储层哨兵错误，供各存储实现与后续波次的子代理复用。
var (
	// ErrNotFound 目标数据不存在（含已过期被清理的情况）。
	ErrNotFound = errors.New("storage: not found")

	// ErrExpired 数据已过期。
	ErrExpired = errors.New("storage: expired")

	// ErrNotImplemented 能力尚未实现（当前为临时桩，Wave 2/3 会替换为真实实现）。
	ErrNotImplemented = errors.New("storage: not implemented")
)
