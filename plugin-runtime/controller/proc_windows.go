//go:build windows

package controller

import "syscall"

// stillActive 是 Win32 的 STILL_ACTIVE 退出码（259）：
// 进程还没结束时 GetExitCodeProcess 返回的就是它。
const stillActive = 259

// processQueryLimitedInformation 是 Win32 的 PROCESS_QUERY_LIMITED_INFORMATION
// （0x1000，Vista+）。Go 的 syscall 包没有导出它，这里就地定义 ——
// 用「有限查询」而非 PROCESS_QUERY_INFORMATION，是为了让探活只拿到
// 「进程还在不在」这一个事实，不额外申请读内存的权限。
const processQueryLimitedInformation = 0x1000

// processAlive 报告 pid 对应的进程是否还活着。
//
// ★ 为什么 Windows 不能用 signal 0：Windows 没有信号语义，
//   `syscall.Kill` 在 Windows 上根本不存在（编译期即报 undefined: syscall.Kill），
//   ⇒ 整个 plugin-runtime/controller 的测试文件在 Windows 上**编译不过**，
//   ⇒ 该包的测试信号在 Windows 上一条都跑不了。
//   ⇒改用 Win32 的「退出码」判定：进程还在跑 ⇒ STILL_ACTIVE(259)；
//     已经退出 ⇒ 真实退出码（0 / 1 / …）。
func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killProcess 强制结束 pid 对应的进程。
//
// Windows 上 os.Process.Kill 是唯一受支持的终止方式（等价于 TerminateProcess）。
func killProcess(pid int) error {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}
