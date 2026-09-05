//go:build !linux && !darwin

package monitor

// rlimitNOFile 未知平台：0 表示跳过 RLIMIT_NOFILE 匹配，相关字段保持零值。
const rlimitNOFile = 0
