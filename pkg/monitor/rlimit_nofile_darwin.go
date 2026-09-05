//go:build darwin

package monitor

// rlimitNOFile 与 Darwin RLIMIT_NOFILE 编号一致；Linux 上该值为 7，
// 硬编码 7 会导致 macOS 上 max_fds/used_ratio 恒为 0。
const rlimitNOFile = 8
