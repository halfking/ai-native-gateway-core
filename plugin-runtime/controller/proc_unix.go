//go:build !windows

package controller

import "syscall"

// processAlive 报告 pid 对应的进程是否还活着。
//
// POSIX 侧用 signal 0 探活（signal 0 不投递信号，只做存在性/权限检查）。
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killProcess 强制杀死 pid 对应的进程。
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
