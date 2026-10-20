package ursm_test

// pg17-emergency-cleanup.sh **L3 分支**的行为门（2026-10-21，R85 轮）
//
// # 这道门要拦的事故：★ **同一个错误只修了一半**
//
// L1 的死表分支在 §116 轮就补上了「DROP 前 COUNT(*) 复核」这道闸
// （`emergency_cleanup_l1_drop_guard_test.go` 钉着它）。
// ⇒ ★★ 而 **L3 分支**（TRUNCATE 清零小表）此前**只有「n_live_tup = 0」一个判据就裸 TRUNCATE**
// ⇒ ⇒ **同一个错误只修了一半**（本轮由并行会话的 §143.42-B 订正牵出）。
//
// ★★ 为什么这半边也危险：
//   `n_live_tup` 是**陈旧估算**，不是行数 —— 对「长期无写入 + 从未 analyze」的表**恒为 0**
//   （252 实测：`ursm_node_snapshot_min_legacy` 真实 **2,836,620** 行，n_live_tup 报 **0**）。
//   ⇒ 一个真人输 "yes" 就可能清掉这四个库里「估算为 0、实际有行」的表
//   ⇒ **清掉的是数据，不是空间**。
//
// ★ 作用域要写准（别夸大）：L3 只覆盖 `casdoor / kaixuan / crm / kxmemory` 四个库，
//   **不含 `llm_gateway`** ⇒ ⇒ 9 月分区那批**不在 L3 射程内**。
//   ⇒ 这道门盯的是「这四个库里的表」这条真实风险，不是「9 月分区被清掉」。
//
// # 为什么用「注入假 docker」而不是子串断言
//
// 子串量的是「源码里写过 COUNT(*) 这几个字」，不是「运行时真的复核了」。
// 本门让脚本**真的跑到 TRUNCATE 之前**，再断言它**对哪些表发出了 TRUNCATE**
// （与既有 L1 门同款手法：`DOCKER_BIN` 可注入 ⇒ `docker_exec` 走假 docker）。
//
// ★ 正控必须有：COUNT(*)=0 时**必须**仍然发出 TRUNCATE ——
//   否则这道门可以靠「一律 SKIP」变绿，而线上唯一该清的表恰好也会被拒。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// l3Candidate 表名（带 schema 与引号形态，与脚本 format('%I.%I') 的输出一致）。
const l3Candidate = `public."tok_table"`

// fakeL3Docker 写一个假 docker：
//
//	★ 按**库名**分派（脚本的 L1 分支打 `llm_gateway`，L3 分支打那四个库）：
//	  - llm_gateway：候选查询与 _default 查询一律**空** ⇒ L1 分支不产生任何 DROP
//	    （本门只关心 L3；让 L1 保持安静，断言才不会被 L1 的动作污染）。
//	  - casdoor：候选查询印 $L3_CANDIDATE；COUNT(*) 印 $FAKE_COUNT。
//	  - 其余三个库：候选查询空 ⇒ 不产生动作。
//
// ★ 所有调用都记进 $TRACE_FILE ⇒ 门能证明「脚本真的走到 TRUNCATE 之前」。
func fakeL3Docker(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

if [ "$1" != "exec" ]; then exit 0; fi

# 解析出 -d <db>（docker exec [-i] <container> psql -U <u> -d <db> ...）
db=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-d" ]; then db="$a"; fi
  prev="$a"
done

case "$db" in
  casdoor)
    case "$all" in
      *"COUNT(*) FROM"*) printf '%s' "${FAKE_COUNT:-0}"; exit 0 ;;
      *"n_live_tup"*)    printf '%s' "${L3_CANDIDATE:-}"; exit 0 ;;
    esac
    exit 0 ;;
  kaixuan|crm|kxmemory)
    exit 0 ;;
  *)
    # llm_gateway（L1 分支）：候选与 _default 都空 ⇒ 无 DROP
    exit 0 ;;
esac
exit 0
`
	p := filepath.Join(dir, "fake-docker")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	return p
}

// runEmergencyL3 跑一轮 L3，返回（调用轨迹, 脚本日志）。
func runEmergencyL3(t *testing.T, candidate, fakeCount string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeL3Docker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "emergency.log")

	// ★ 必须带 --yes：L3 的确认是 `read -p`（**没有** `-t 0` 守卫）⇒
	//   非交互环境下 `read` 立刻返回空 ⇒ 脚本会 abort L3 ⇒ 门会读到「什么都没做」。
	cmd := exec.Command(bashForScriptFor(t, emergencyScript), emergencyScript, "--level", "L3", "--yes")
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"L3_CANDIDATE="+candidate,
		"FAKE_COUNT="+fakeCount,
		"LOG="+logPath,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		"LLMGW_LOCK_DIR="+dir,
	)
	_, _ = cmd.CombinedOutput()

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没跑到候选查询）: %v", err)
	}
	logBody, _ := os.ReadFile(logPath)
	return string(traceBody), string(logBody)
}

// TestEmergencyL3RefusesToTruncateTableWithRows 钉住第三/第四道闸。
//
// 252 实测形态（§143.42-B 订正）：`n_live_tup = 0` 但 `COUNT(*) = 2,836,620`
// ⇒ 旧写法会把这种表**真的 TRUNCATE 掉** ⇒ 数据没了、空间也没省多少。
//
// ★ 门断的是**行为**：脚本在 COUNT(*) 非 0 时**不得**发出 TRUNCATE，且必须留痕。
func TestEmergencyL3RefusesToTruncateTableWithRows(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyL3(t, l3Candidate+"|0", "2836620")

	requireCalls(t, trace)
	if strings.Contains(trace, "TRUNCATE TABLE") {
		t.Fatalf("★ n_live_tup=0 但 COUNT(*)=2836620 的表仍被 TRUNCATE ⇒ 数据被清掉\ntrace:\n%s\nlog:\n%s",
			trace, logBody)
	}
	if !strings.Contains(logBody, "拒绝 TRUNCATE") {
		t.Fatalf("必须在日志里留下「拒绝 TRUNCATE」与真实行数，否则事后无法反查为什么没清\nlog:\n%s", logBody)
	}
	if !strings.Contains(logBody, "2836620") {
		t.Fatalf("日志里必须带上真实行数（否则只有「拒绝」没有原因）\nlog:\n%s", logBody)
	}
}

// TestEmergencyL3StillTruncatesGenuinelyEmptyTable 是**正向下限**：
// COUNT(*)=0 的表**仍然必须被清掉**。没有这条，上面那道门可以靠「一律 SKIP」变绿。
func TestEmergencyL3StillTruncatesGenuinelyEmptyTable(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyL3(t, l3Candidate+"|0", "0")

	requireCalls(t, trace)
	if !strings.Contains(trace, "TRUNCATE TABLE") {
		t.Fatalf("★ COUNT(*)=0 的表应当被清掉 —— 脚本可能被改瘫，本门失去意义\ntrace:\n%s\nlog:\n%s",
			trace, logBody)
	}
}

// TestEmergencyL3RefusesToTruncatePartition 钉住分区排除这道闸。
//
// 分区生命周期不归本脚本管（L1 第二道闸的同一口径）：
// TRUNCATE 一个已 ATTACH 的分区会静默丢掉该范围的行，
// 之后该范围 INSERT 直接报 `no partition of relation found` ⇒ 写路径事故。
func TestEmergencyL3RefusesToTruncatePartition(t *testing.T) {
	skipWindows(t)

	trace, logBody := runEmergencyL3(t, l3Candidate+"|1", "0")

	requireCalls(t, trace)
	if strings.Contains(trace, "TRUNCATE TABLE") {
		t.Fatalf("★ 分区仍被 TRUNCATE ⇒ 该范围的行被静默丢掉，写路径随后报 23514\ntrace:\n%s\nlog:\n%s",
			trace, logBody)
	}
	if !strings.Contains(logBody, "是分区") {
		t.Fatalf("必须在日志里说明「是分区」，否则事后无法反查为什么没清\nlog:\n%s", logBody)
	}
}
