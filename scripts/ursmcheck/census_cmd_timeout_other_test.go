//go:build !unix

package ursm_test

// census 命令的**超时收口**（非 unix 兜底实现）——2026-11-23 ★★
//
// ★ 本仓的 census / 部署脚本链全是 **bash + docker** ⇒ ★ 实际运行环境是 unix；
// ★ 本文件只为「**在非 unix 平台上 `go vet` / 编译不炸**」而存在。
//
// ★★ **能力差异（如实登记）**：★ 非 unix 没有进程组 ⇒ ★
//
//	① `censusPrepareCmd`：★ **空实现**（★ 不建进程组）
//	② `censusKillGroup`：★ **只能杀直接子进程**（★ 子进程/孙进程可能留下）
//
// ★★ ⇒ ★ **在非 unix 上，本套超时收口是「弱版」**：★ 孙进程持有管道时
// ★★ 仍可能拖住 `Wait` ⇒ ★ **但调用方还设了 `cmd.WaitDelay`**（★ 到点强制
// ★★ 关闭管道 ⇒ 必返回）⇒ ★★ **「到点一定返回」这条性质在所有平台都成立**，
// ★★ 只是**「不留挂死子进程」这条只在 unix 成立**。

import (
	"os/exec"
)

// censusPrepareCmd 非 unix 无进程组概念 ⇒ 空实现。
func censusPrepareCmd(cmd *exec.Cmd) {}

// censusKillGroup 非 unix 只能杀直接子进程。
func censusKillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	return nil
}
