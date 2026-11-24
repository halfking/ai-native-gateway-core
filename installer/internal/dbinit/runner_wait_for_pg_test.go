package dbinit

// `WaitForPG` 的**超时收口**门（2026-11-23）
//
// ★★★ **这道门钉的是什么**：★★★
//
//　★ `WaitForPG` 的注释**一直写着**「★ 最多 timeout 秒 ★」⇒ ★★ 而旧实现里
//　★ **每一次 `docker exec` 都没有任何上限** ⇒ ★★ Docker 守护进程僵死时
//　★ （★ `docker exec` 客户端永不返回 ★）**第一次调用就无限挂**
//　★ ⇒ ★★ **注释里的契约今天本来就是破的** ★★
//　★ ⇒ ★★ 而且连「PostgreSQL 启动超时 (Ns)」这句话**都打不出来** ★★。
//
// ★★★ **怎么测（★ 不依赖真 Docker）**：★★★
//
//　★ 假 `docker` 脚本放在 PATH 最前（★ 与仓内既有做法同款 ★）★ 三形态：
//　★ ① **挂死**（★ `exec sleep 30` ★）⇒ ★ 必须**到点返回** ★
//　★ ② **非零退出**（★ PG 还没起来 ★）⇒ ★ 保持原口径「PostgreSQL 启动超时 (Ns)」 ★
//　★ ③ **成功**（★ `exit 0` ★）⇒ ★ 立刻返回 nil ★
//
// ★ ⇒ ★★ **这道门在 Docker 完全不可用时也能跑、也能证** ★★。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDockerForWait 造一只假 `docker` 放进 PATH 最前（★ 并返回其目录 ★）。
//
// ★ ★ 脚本体用 `exec sleep …` ⇒ ★ ★ 挂死时 **sleep 就是 docker 的直接子进程**
// ★ ★ ⇒ ★ 我们超时 kill 掉它时**不留孤儿** ★★。
func fakeDockerForWait(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("写假 docker 失败: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestWaitForPGReturnsWhenDockerHangs —— ★★★ 承重断言：★★★
//
// ★★ **Docker 僵死 ⇒ `WaitForPG` 必须到点返回错误**（★ 而不是无限挂 ★）★★。
func TestWaitForPGReturnsWhenDockerHangs(t *testing.T) {
	fakeDockerForWait(t, "exec sleep 30")
	r := NewRunner("kx-citus", "kxuser", "llm_gateway", t.TempDir())
	start := time.Now()
	err := r.WaitForPG(3)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("★ docker 挂死时 WaitForPG 不该返回成功")
	}
	if !strings.Contains(err.Error(), "PostgreSQL 启动超时") {
		t.Fatalf("★ 错误应含「PostgreSQL 启动超时」，★ 实际: %v", err)
	}
	// ★ ★ 契约：★ 「最多 timeout 秒」⇒ ★ 3s 的预算 ⇒ ★ 允许一点余量
	// ★ （★ 但绝不能拖到假 docker 的 30s ⇒ ★ 那是没封顶 ★）★★。
	if elapsed > 12*time.Second {
		t.Fatalf("★ docker 挂死下耗时 %v（★ 3s 预算却拖了这么久）⇒ ★ 没收口", elapsed)
	}
	// ★ ★ 根因可诊断：★ 要能看出「★ 是 docker 无响应、★ 不是 PG 没起来 ★」★★。
	if !strings.Contains(err.Error(), "无响应") {
		t.Fatalf("★ 错误应指出 docker 无响应（★ 供运维一眼看出根因 ★），★ 实际: %v", err)
	}
}

// TestWaitForPGTimesOutWhenNotReady —— ★ docker 正常、★ 但 PG 还没起来
// ⇒ ★ 保持**原口径**的错误文案（★ 不带「无响应」★）。
func TestWaitForPGTimesOutWhenNotReady(t *testing.T) {
	fakeDockerForWait(t, "exit 1")
	r := NewRunner("kx-citus", "kxuser", "llm_gateway", t.TempDir())
	start := time.Now()
	err := r.WaitForPG(2)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("★ PG 没起来时不该返回成功")
	}
	if !strings.Contains(err.Error(), "PostgreSQL 启动超时") {
		t.Fatalf("★ 错误应含「PostgreSQL 启动超时」，★ 实际: %v", err)
	}
	if strings.Contains(err.Error(), "无响应") {
		t.Fatalf("★ docker 正常时不该说「无响应」（★ 那是另一种失败 ★），★ 实际: %v", err)
	}
	if elapsed > 12*time.Second {
		t.Fatalf("★ 耗时 %v 超出预算", elapsed)
	}
}

// TestWaitForPGSucceedsWhenReady —— 成功路径：docker 立刻返回 0 ⇒ 立刻 nil。
func TestWaitForPGSucceedsWhenReady(t *testing.T) {
	fakeDockerForWait(t, "exit 0")
	r := NewRunner("kx-citus", "kxuser", "llm_gateway", t.TempDir())
	start := time.Now()
	if err := r.WaitForPG(5); err != nil {
		t.Fatalf("★ docker 成功时不该报错: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("★ 成功路径耗时 %v ⇒ ★ 不该有多余等待", elapsed)
	}
}
