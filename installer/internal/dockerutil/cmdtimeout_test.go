package dockerutil

// docker 命令**超时收口**的门（2026-11-23）
//
// ★★★ **这道门钉的是什么（★ 真实事故）**：★★★
//
//　★ 2026-11-23 本机 **Docker 守护进程僵死**（★ `docker ps` / `info` / `version`
//　★ 三个客户端**全部挂住不返回** ★）⇒ ★★ 而安装链上原有的 6 处 docker 调用
//　★ （★ `WaitForPG` / `VerifySchema` / 健康检查四处 ★）**全是 `exec.Command`
//　★ （★ 没有任何上限 ★）** ⇒ ★★ **安装链在 Docker 无响应时【无限挂住】**
//　★ ⇒ ★★ 连「PostgreSQL 启动超时 (Ns)」这句话**都打不出来** ★★。
//
// ★★★ **怎么测（★ 关键：★ 不依赖真 Docker）**：★★★
//
//　★ 用一只**假 `docker` 脚本**放在 PATH 前面（★ 与仓内既有做法同款
//　★ ——★ `emergency_cleanup_l3_truncate_guard_test.go` 等门都这么干 ★）：
//　★ ① **挂死形态**：★ `exec sleep 30` ⇒ ★ 模拟「客户端永不返回」★
//　★ ② **失败形态**：★ `exit 1` ⇒ ★ 模拟「命令本身失败」★
//　★ ③ **正常形态**：★ `echo ...` ⇒ ★ 模拟「命令成功」★
//
// ★ ⇒ ★★ **因此这道门在 Docker 完全不可用时也能跑、也能证** ★★
// ★ （★ 而 2026-11-23 那台机器上真 Docker 就是完全停着的 ★）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDocker 造一只假 `docker`（★ 放进 PATH 最前 ★）⇒ ★ 返回该目录。
//
// ★ ★ 脚本体用 `exec sleep …` ⇒ ★ ★ 挂死时 **sleep 就是 docker 的直接子进程**
// ★ ★ ⇒ ★ 我们超时 kill 掉它时**不留孤儿** ★★（★ 不像 census 门那条链有孙进程 ★）。
func fakeDocker(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("写假 docker 失败: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// TestRunDockerTimesOutOnHungDaemon —— ★★ 挂死的 docker ⇒ ★ 到点必须返回 ★★。
func TestRunDockerTimesOutOnHungDaemon(t *testing.T) {
	fakeDocker(t, "exec sleep 30")
	start := time.Now()
	_, err := RunDocker(2*time.Second, "ps")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("★ 挂死的 docker 不该返回成功")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("★ 错误应为 context.DeadlineExceeded（★ 供调用方区分「超时」与「真失败」），★ 实际: %v", err)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("★ 错误信息应含「超时」（★ 让运维一眼看出根因 ★），★ 实际: %v", err)
	}
	// ★ 到点收口：★ 2s 上限 ＋ 余量 ⇒ ★ 绝不能拖到 30s（★ 那就是没封顶 ★）
	if elapsed > 10*time.Second {
		t.Fatalf("★ 挂死 docker 下耗时 %v（★ 远超 2s 上限）⇒ ★ 没封顶", elapsed)
	}
}

// TestRunDockerReportsRealFailure —— ★ 命令本身失败 ⇒ ★ 错误里是「失败」而不是「超时」。
func TestRunDockerReportsRealFailure(t *testing.T) {
	fakeDocker(t, "echo 'boom' >&2\nexit 1")
	_, err := RunDocker(5*time.Second, "ps")
	if err == nil {
		t.Fatalf("★ 非零退出不该返回成功")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("★ 非零退出不该被当成超时: %v", err)
	}
	if !strings.Contains(err.Error(), "失败") {
		t.Fatalf("★ 错误信息应含「失败」，★ 实际: %v", err)
	}
}

// TestRunDockerReturnsStdout —— 成功路径仍要把 stdout 交给调用方。
func TestRunDockerReturnsStdout(t *testing.T) {
	fakeDocker(t, "echo kx-citus")
	out, err := RunDocker(5*time.Second, "ps")
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if !strings.Contains(string(out), "kx-citus") {
		t.Fatalf("stdout 应含容器名，★ 实际: %q", string(out))
	}
}

// TestHealthCheckerSurvivesHungDocker —— ★★★ 承重断言：★★
//
// ★★ 健康检查在 Docker 无响应时**必须到点返回**（★ 而不是把安装链卡死 ★）★★。
func TestHealthCheckerSurvivesHungDocker(t *testing.T) {
	fakeDocker(t, "exec sleep 30")
	hc := NewHealthChecker(18781, "kx-citus", "kx-redis", "pw", "kxuser", "llm_gateway")
	start := time.Now()
	report, _ := hc.RunAll(func(string) {})
	elapsed := time.Since(start)
	// ★ 四个 docker 检查各 10s 上限 ⇒ ★ 最坏 ~40s ⇒ ★ 断言它**没有无限挂**
	// ★ （★ 用一个明显更宽但仍有限的上界 ★）。
	if elapsed > 90*time.Second {
		t.Fatalf("★ 健康检查在挂死 docker 下耗时 %v ⇒ ★ 没收口", elapsed)
	}
	if report.ContainersOK || report.PGReadyOK || report.RedisOK || report.SchemaOK {
		t.Fatalf("★ 挂死 docker 下不该有任何一项判为 OK（★ 否则是「假绿」★）: %+v", report)
	}
}
