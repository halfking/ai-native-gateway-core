//go:build unix

package startup

// legacy_cross_surface_live_runner_unix_test.go —— 外部命令的**超时收口**（unix 实现）
//
// ★★ 为什么要有这个文件：★ 进程组（pgid）语义是 unix 专有的
// ★ ⇒ ★ `syscall.SysProcAttr{Setpgid: true}` **在 windows 上编译不过**
// ★ ⇒ ★ 故拆成 unix / 非 unix 两份（★ 同包、同函数名、互斥构建）。
//
// ★★ 为什么必须收口：★★
//
//	★ 这道门会**调外部命令**（★ 例：`ssh 211 "docker exec -i … psql -tA -q"`）★
//	★ ⇒ ★ 如果那条命令挂住（★ 网络断、★ 远端僵死、★ docker 客户端卡在管道上）★
//	★ ⇒ ★ **整包门会在 go test 的上限处被杀**（★ 那正是 L30 轮撞出的
//	★ 「闸门红的第四种成因＝量具自己挂死」★★）★
//	★★ ⇒ ★★ **凡是调外部命令的门、必须同时给：ctx 超时 ＋ `cmd.WaitDelay` ＋ 进程组整组杀** ★★
//	★★ ⇒ ★★ 三道保险缺一不可（★ 只给 ctx 超时只 kill 直接子进程、★ 孙进程仍持有 stdout 管道）★★。

import (
	"os/exec"
	"syscall"
)

// legacyLivePrepareCmd 让被启动的进程**自成一个进程组**（★ 超时可整组杀）。
func legacyLivePrepareCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// legacyLiveKillGroup 杀掉**整个进程组**（★ 负 pid ⇒ 进程组）。
//
// ★ ESRCH（★ 进程已经自己退了）**不是错误** ⇒ 返回 nil，★ 不让 exec 的
// 「Cancel 返回非 nil」路径把真正的 ctx 超时错误**顶掉**。
func legacyLiveKillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	if err != nil {
		// ★ 兜底：进程组杀不掉时、★ 至少把直接子进程杀掉。
		_ = cmd.Process.Kill()
		return nil
	}
	return nil
}
