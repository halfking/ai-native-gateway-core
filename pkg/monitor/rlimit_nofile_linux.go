//go:build linux

package monitor

// rlimitNOFile 与 Linux 内核 RLIMIT_NOFILE 编号一致；gopsutil 原样透传内核编号。
const rlimitNOFile = 7
