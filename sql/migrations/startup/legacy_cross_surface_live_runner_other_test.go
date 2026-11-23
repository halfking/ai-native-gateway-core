//go:build !unix

package startup

// legacy_cross_surface_live_runner_other_test.go —— 外部命令的**超时收口**（非 unix 实现）
//
// ★ 与 unix 版**同包、同函数名、互斥构建**（★ 进程组语义是 unix 专有的）★。
// ★ 这里没有进程组 ⇒ 只靠 ctx 超时 ＋ `cmd.WaitDelay` 兜底。

import "os/exec"

// legacyLivePrepareCmd 非 unix 平台没有进程组 ⇒ 不做额外处理。
func legacyLivePrepareCmd(cmd *exec.Cmd) {}

// legacyLiveKillGroup 非 unix 平台：★ 没有进程组可杀 ⇒ 返回 nil（★ 由 ctx 负责收口）。
func legacyLiveKillGroup(cmd *exec.Cmd) error { return nil }
