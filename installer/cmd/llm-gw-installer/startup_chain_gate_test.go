//go:build integration

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// knownStartupChainGaps lists the registered startup migrations that are known
// not to apply on a fresh install, each with the relation (or column) that has
// no creator in the fresh-install chain.
//
// R43 (2026-09-30) context. The previous round closed the 568 blocker by
// registering 541, its sole creator. Doing that surfaced a second failure at
// 622, then a third — the chain was being debugged one migration at a time.
// This allowlist is the replacement for that loop: one run reports every
// migration that fails, and the test fails when that set changes in either
// direction.
//
//   - A migration that fails and is NOT listed here is a new regression.
//   - A listed migration that now applies has been fixed; delete its entry so
//     the list keeps shrinking and cannot quietly go stale.
//
// 2026-10-20 订正（本轮实测，真库 PG 18.x 临时库，整链 282 条逐条跑）：
//
//	**本表已清空 —— 整条 fresh-install 链现在全绿（0 of 282 fail）。**
//
// 三处订正，各自都有实测支撑：
//
//  1. **本门自己的量具坏了**：`runInstallerPSQL` 无条件加 `--single-transaction`，
//     而真实安装器会把带 `dbinit:no-transaction` 标记的文件送到**非事务通道**
//     ⇒ 718/719/807/822/845/871/877 这 7 条含
//     `DROP/CREATE INDEX CONCURRENTLY` 的迁移在**本门里**被误报成
//     「链上新破坏」。**量具比被测对象更严 ⇒ 造假阳性**。修正后这 7 条全绿。
//  2. **489/490 补登**（本轮登记）：853 自己在文件头声明依赖
//     「489/490_credential_probe_queue_*（队列表本体）」，而那两条从未登记进链
//     ⇒ 新装在 853 处 42P01 并**中止整个安装**。补登后 853 绿。
//  3. **原 18 条已审阅失败项全部不再失败**（含 622/624/627 的 candidate_failure_logs
//     族、707/713/733/734/738/740/757/762 等）⇒ 按本表的收缩规则逐条删除。
//     这 18 条里有 10 条其实**早已**在链变好之后不再失败，只是没人跑这个门
//     ⇒ 门红着 = **信号被淹没**：真缺陷和假缺陷混在一起，没人看得见。
//
// ⇒ 现在这张表是**空的**，门的含义变成最锐的那一条：
// **「整条 fresh-install 链必须一条不落地应用成功」**。
// 任何新的断链都会被立刻点名；任何链变好也仍会被要求把表补回（或保持空）。
//
// ⚠ 口径：这条结论是在**本机 PG 18.x**、用真实 `psql` 逐条跑出来的，
// 不是静态分析。跨 PG 大版本重跑时若出现差异，先怀疑版本差异而不是直接改表。
var knownStartupChainGaps = map[string]string{}

// TestStartupChainGapsMatchReviewedSet applies the whole fresh-install chain to
// a disposable database and compares the set of migrations that fail against
// knownStartupChainGaps. Each migration runs in its own transaction so one
// failure does not hide the migrations behind it.
func TestStartupChainGapsMatchReviewedSet(t *testing.T) {
	dsn := os.Getenv(installerFreshDBURL)
	if dsn == "" {
		t.Skipf("%s not set; skipping startup chain gate", installerFreshDBURL)
	}

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up installer SQL directory: %v", err)
	}
	defer cleanup()

	for _, name := range []string{"00-prereqs.sql", "01-schema.sql", "02-seed.sql"} {
		applyInstallerSQL(t, dsn, filepath.Join(sqlDir, name))
	}

	runner := dbinit.NewRunner("", "", "", sqlDir)
	observed := map[string]string{}
	for _, name := range runner.StartupFiles {
		path := filepath.Join(sqlDir, "startup", name)
		if reason, fails := applyInstallerSQLTolerant(t, dsn, path); fails {
			observed[name] = reason
		}
	}

	var unexpected, fixed []string
	for name, reason := range observed {
		if _, known := knownStartupChainGaps[name]; !known {
			unexpected = append(unexpected, name+"  ->  "+reason)
		}
	}
	for name := range knownStartupChainGaps {
		if _, stillBroken := observed[name]; !stillBroken {
			fixed = append(fixed, name)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(fixed)

	for _, u := range unexpected {
		t.Errorf("startup chain gap not in knownStartupChainGaps (new breakage): %s\n"+
			"    add it with the missing relation and its creator, or fix the chain", u)
	}
	for _, f := range fixed {
		t.Errorf("knownStartupChainGaps entry %q now applies — the chain improved.\n"+
			"    Delete the entry so the reviewed set keeps shrinking", f)
	}
	t.Logf("startup chain: %d of %d registered migrations fail, %d expected",
		len(observed), len(runner.StartupFiles), len(knownStartupChainGaps))
}

// applyInstallerSQLTolerant runs one migration in its own transaction and
// reports the first ERROR line instead of failing the test. A migration that
// aborts leaves nothing behind, so the following migrations see the same
// catalog a real fresh install would give them.
func applyInstallerSQLTolerant(t *testing.T, dsn, path string) (reason string, failed bool) {
	t.Helper()
	out, code := runInstallerPSQL(t, dsn, path)
	if code == "0" {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "ERROR:") {
			return l, true
		}
	}
	return "exit " + code, true
}

// runInstallerPSQL mirrors applyInstaller's real invocation (stdin-free -f,
// ON_ERROR_STOP=1, single transaction) and returns the merged output plus the
// process exit code as a string. Merged output is deliberate here: this is a
// gate that wants to *read* failures, and unlike psqlScalar it never parses a
// scalar out of the output, so citus's background-worker warnings on stderr
// cannot be mistaken for a value.
//
// ⚠ 2026-10-20 订正：事务包裹必须**尊重 dbinit:no-transaction 标记**。
// 之前这里无条件加 `--single-transaction`，而真实安装器（dbinit.Runner.applySQL
// 与本包的 applyInstallerSQL）会把带标记的文件送到**非事务通道**。于是
// 718/719/807/822/845/871/877 这 7 条含
// `DROP/CREATE INDEX CONCURRENTLY` 的迁移在**这个门里**全部报
// “cannot run inside a transaction block”，被误记成「链上新破坏」。
// ⇒ **量具比被测对象更严 ⇒ 量具造假阳性**（判据纪律：坏尺子必须先修尺子，
// 否则「链是坏的」这个结论会把真缺陷和假缺陷混在一起）。
// 2026-10-20 实测：修正后这 7 条在本门里全部应用成功。
func runInstallerPSQL(t *testing.T, dsn, path string) (string, string) {
	t.Helper()
	args := []string{"-v", "ON_ERROR_STOP=1"}
	// 与真实安装器同一套事务分流（dbinit.Runner.applySQL）。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if !dbinit.RequiresNoTransaction(content) {
		args = append(args, "--single-transaction")
	}
	args = append(args, "--dbname", dsn, "-f", path)
	cmd := exec.Command("psql", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), "0"
	}
	var ee *exec.ExitError
	code := "unknown"
	if errors.As(err, &ee) {
		code = strconv.Itoa(ee.ExitCode())
	} else {
		code = err.Error()
	}
	return string(out), code
}
