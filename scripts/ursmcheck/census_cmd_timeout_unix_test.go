//go:build unix

package ursm_test

// census 命令的**超时收口**（unix 实现）——2026-11-23 ★★
//
// ★★ **为什么必须单独一个文件**：★ 进程组（pgid）语义是 unix 专有的
// ★ ⇒ ★ `syscall.SysProcAttr{Setpgid: true}` **在 windows 上编译不过**
// ★ ⇒ ★ 故拆成 unix / 非 unix 两份（★ 同包、同函数名、互斥构建）。
//
// ★★★ **这块要解决的问题（★ 实测踩到两次）**：★★
//
//	① `exec.CommandContext(ctx, ...)` 在 ctx 到期时**只 kill 直接子进程**；
//	② 而 census 这条链是**三层**的：
//
//	   Go 测试进程 → bash（普查器）→ sh（记录型 psql）→ docker exec 客户端
//	                                              ↑ `exec` 替换了 sh ⇒ 它是 bash 的孙子
//
//	③ ★★ ⇒ ctx 到期时 **docker 客户端活着** ⇒ ★★ 它**继承了 stdout 管道**
//	④ ⇒ ★★ `cmd.Wait()` 会等「拷贝管道的 goroutine 结束」⇒ ★★ **管道写端没关**
//	⑤ ⇒ ★★ ⇒★ **即便 ctx 已到期、即便直接子进程已被 kill，★ `CombinedOutput()`
//	   依然会一直阻塞** ⇒ ★★ **整包门在 900s 上限处超时**。
//
// ★★ 实测证据（★ 2026-11-23）：★ 挂了 120s 的 ctx 超时之后，★ 测试二进制**仍然活着**
// ★（★ `ps` 里 `ursmcheck.test` 与 `sleep 3600` 都在）★ ⇒ ★ **只加 ctx 超时是不够的**。
//
// ★★ ⇒★ **本文件给的两件事**：★
//
//	① `censusPrepareCmd`：★ 让子进程**自成一个进程组**（★ `Setpgid: true`）
//	② `censusKillGroup`：★ 超时时杀**整个进程组**（★ `kill(-pgid, SIGKILL)`）
//	   ⇒ ★ 子、孙、曾孙**一并清掉** ⇒ ★ 不留挂死的 docker 客户端
//	   ⇒ ★ ★ 而本轮此前就不得不**手工 kill 4 个挂死的 `docker exec` 客户端**。
//
// ★★ **诚实边界**：★ 即使进程组杀掉了，★ 若有进程**逃出进程组**且仍持有管道，
// ★ 仍可能卡住 `Wait` ⇒ ★ 因此调用方**还必须**设 `cmd.WaitDelay`（★ 兜底：
// ★ 到点强制关闭管道 ⇒ 必返回）⇒ ★★ **两道保险缺一不可**。

import (
	"os/exec"
	"syscall"
)

// censusPrepareCmd 让被启动的进程**自成一个进程组**（★ 超时时可整组杀）。
func censusPrepareCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// censusKillGroup 杀掉**整个进程组**（★ 负 pid ⇒ 进程组）。
//
// ★ ESRCH（★ 进程已经自己退了）**不是错误** ⇒ ★ 返回 nil，★ 不让 exec 的
// ★ 「Cancel 返回非 nil」路径把真正的 ctx 超时错误**顶掉**。
func censusKillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	if err != nil {
		// ★ 兜底：进程组杀不掉时，★ 至少把直接子进程杀掉。
		_ = cmd.Process.Kill()
		return nil
	}
	return nil
}
