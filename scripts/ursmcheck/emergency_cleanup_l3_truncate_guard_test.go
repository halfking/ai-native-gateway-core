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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
//
// ★★ R89 轮补：**stdin drain**（R86 轮回执要求的第 ② 条）。
//
// 为什么需要：真实的 `docker exec` 在**不带 `-i`** 时（本脚本的 `docker_exec`
// 就是 `docker exec "$CONTAINER" …`，没有 `-i`）⇒ 容器进程的 stdin 被接成
// /dev/null ⇒ **写侧立刻拿到 EOF，不会 EPIPE/SIGPIPE**。
// 而这个假 docker 若**完全不读 stdin** ⇒ 写侧会拿到 EPIPE ⇒ **与真实行为不一致**。
// ⇒ 一旦将来脚本改成用 stdin 传 SQL（`docker exec -i … psql < SQL`），
// 门会读到「假 docker 没收到 SQL」⇒ **门静默失真**（而不是响亮地红）。
// ⇒ 所以这里显式 drain：读到 EOF 为止，并把读到的字节数记进 $STDIN_TRACE_FILE。
//
// ⚠ 阻塞边界（必须写清，否则这条 drain 自己会变成新缺陷）：
// 本门**刻意不设** `cmd.Stdin` ⇒ Go 会把子进程的 stdin 接成 **/dev/null**
// ⇒ drain 立刻拿到 EOF ⇒ 不会挂住。
// ⇒ 也正因为脚本的两次 `read -p`（`:106` 有 `[ -t 0 ]` 守卫、`:334` 被 `--yes` 旁路）
// 在测试路径上**根本不执行** ⇒ drain 不可能抢走任何确认输入。
func fakeL3Docker(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

# ★★ stdin drain（读到底，把字节数记进 $STDIN_TRACE_FILE）—— 理由见上方注释。
_stdin_bytes=$(cat | wc -c | tr -d '[:space:]')
if [ -n "${STDIN_TRACE_FILE:-}" ]; then
  echo "stdin_bytes=${_stdin_bytes}" >> "$STDIN_TRACE_FILE"
fi

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

// runEmergencyL3 跑一轮 L3，返回（调用轨迹, 脚本日志, stdin 轨迹）。
//
// ★ 第三个返回值是 R89 轮加的：它记录「假 docker 每次从 stdin 读到了多少字节」⇒
// ① 自检用它证明 drain 真的在工作；
// ② 另一条门用它钉住「被测脚本今天**不走 stdin**」这个事实。
func runEmergencyL3(t *testing.T, candidate, fakeCount string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := fakeL3Docker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	stdinTrace := filepath.Join(dir, "stdin.txt")
	logPath := filepath.Join(dir, "emergency.log")

	// ★ 必须带 --yes：L3 的确认是 `read -p`（**没有** `-t 0` 守卫）⇒
	//   非交互环境下 `read` 立刻返回空 ⇒ 脚本会 abort L3 ⇒ 门会读到「什么都没做」。
	// ★ 并且刻意**不设** cmd.Stdin ⇒ 子进程 stdin = /dev/null ⇒ 假 docker 的 drain
	//   立刻拿到 EOF ⇒ 不会挂住（见 fakeL3Docker 的阻塞边界说明）。
	cmd := exec.Command(bashForScriptFor(t, emergencyScript), emergencyScript, "--level", "L3", "--yes")
	cmd.Env = append(os.Environ(),
		"DOCKER_BIN="+fake,
		"TRACE_FILE="+trace,
		"STDIN_TRACE_FILE="+stdinTrace,
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
	stdinBody, _ := os.ReadFile(stdinTrace)
	return string(traceBody), string(logBody), string(stdinBody)
}

// TestEmergencyL3RefusesToTruncateTableWithRows 钉住第三/第四道闸。
//
// 252 实测形态（§143.42-B 订正）：`n_live_tup = 0` 但 `COUNT(*) = 2,836,620`
// ⇒ 旧写法会把这种表**真的 TRUNCATE 掉** ⇒ 数据没了、空间也没省多少。
//
// ★ 门断的是**行为**：脚本在 COUNT(*) 非 0 时**不得**发出 TRUNCATE，且必须留痕。
func TestEmergencyL3RefusesToTruncateTableWithRows(t *testing.T) {
	skipWindows(t)

	trace, logBody, _ := runEmergencyL3(t, l3Candidate+"|0", "2836620")

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

	trace, logBody, _ := runEmergencyL3(t, l3Candidate+"|0", "0")

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

	trace, logBody, _ := runEmergencyL3(t, l3Candidate+"|1", "0")

	requireCalls(t, trace)
	if strings.Contains(trace, "TRUNCATE TABLE") {
		t.Fatalf("★ 分区仍被 TRUNCATE ⇒ 该范围的行被静默丢掉，写路径随后报 23514\ntrace:\n%s\nlog:\n%s",
			trace, logBody)
	}
	if !strings.Contains(logBody, "是分区") {
		t.Fatalf("必须在日志里说明「是分区」，否则事后无法反查为什么没清\nlog:\n%s", logBody)
	}
}

// TestFakeL3DockerDrainsStdin 是**假 docker 自身的行为级自检**（R89 轮）。
//
// ★ 为什么这道自检不是「多写一条断言」而是必需：
// 假 docker 的职责是**模拟真实 docker exec 的边界行为**。
// 真实 `docker exec`（不带 `-i`）把容器进程的 stdin 接成 /dev/null ⇒
// **写侧拿到 EOF，而不是 EPIPE**。
// ⇒ 若假 docker 完全不读 stdin，它就与真实行为不一致 ⇒
// **一旦被测脚本将来改用 stdin 传 SQL，门会读到「假 docker 没收到 SQL」**
// ⇒ 而本门断言的是 **argv 里的 "TRUNCATE TABLE"**（SQL 走 argv 时才看得到）
// ⇒ ⇒ 那种改动会让门**静默失真**（正控会红，但红的原因会被误读成「脚本被改瘫」）。
//
// ★ 所以这里直接灌 1MB payload 进假 docker 的 stdin，断言它**读满了**。
// ⇒ 变异 M1（把 drain 换成 `_stdin_bytes=0`）必须让这道自检变红。
func TestFakeL3DockerDrainsStdin(t *testing.T) {
	skipWindows(t)

	dir := t.TempDir()
	fake := fakeL3Docker(t, dir)
	trace := filepath.Join(dir, "trace.txt")
	stdinTrace := filepath.Join(dir, "stdin.txt")

	const payloadSize = 1024 * 1024
	payload := bytes.Repeat([]byte("A"), payloadSize)

	cmd := exec.Command(fake, "exec", "llm-gateway-pg", "psql", "-U", "u", "-d", "llm_gateway", "-tAc", "SELECT 1")
	cmd.Env = append(os.Environ(),
		"TRACE_FILE="+trace,
		"STDIN_TRACE_FILE="+stdinTrace,
	)
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("★ 假 docker 消费 1MB stdin 之后不应失败 —— 真实 docker exec（不带 -i）给写侧的是 EOF 而不是 EPIPE；"+
			"实测 err=%v\nout:\n%s", err, out)
	}

	body, readErr := os.ReadFile(stdinTrace)
	if readErr != nil {
		t.Fatalf("读 stdin 轨迹失败（假 docker 没有 drain stdin ⇒ 写侧会拿到 EPIPE，与真实 docker 不一致）: %v", readErr)
	}
	want := "stdin_bytes=" + strconv.Itoa(payloadSize)
	if !strings.Contains(string(body), want) {
		t.Fatalf("★ 假 docker 必须把 stdin 全部读完（期望 %s）—— 实测轨迹:\n%s", want, body)
	}
}

// TestEmergencyL3ScriptNeverWritesToStdin 钉住「被测脚本今天**不走 stdin**」这个事实。
//
// ★ 它是上一条自检的**另一半**：自检证明「假 docker 能读 stdin」，
// 而这一条证明「被测脚本今天不需要 stdin」⇒ 两者合起来才是完整的契约：
// 「假 docker 忠实（能读）＋ 脚本现状（不写）」。
//
// ★ 为什么这条不是「过度约束」：
// 被测脚本的 SQL **全部**走 `psql -tAc "<SQL>"` 的 argv（实测：脚本里 0 处
// `docker exec -i`、0 处 `< SQL` 重定向、0 处 `| docker exec` 管道）。
// ⇒ 一旦有人把 SQL 改成走 stdin，这道门会**响亮地红**，提醒维护者
// 「假 docker 的解析要同步改」⇒ ⇒ 而不是让门悄悄失真。
func TestEmergencyL3ScriptNeverWritesToStdin(t *testing.T) {
	skipWindows(t)

	_, _, stdinTrace := runEmergencyL3(t, l3Candidate+"|0", "0")

	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(stdinTrace), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		if line != "stdin_bytes=0" {
			t.Fatalf("★ 被测脚本今天**不走 stdin**（SQL 全部走 psql -c 的 argv）⇒ 假 docker 每次读到的 stdin 必须是 0 字节；"+
				"实测 %q\n⇒ 若脚本改成了 `docker exec -i … psql < SQL`，本门会失真（假 docker 收不到 SQL）⇒ 请同步更新假 docker 的解析",
				line)
		}
	}
	if lines == 0 {
		t.Fatalf("★ 一次 docker 调用都没记录到 stdin 轨迹 ⇒ drain 没生效（假 docker 没跑？）\nstdin trace:\n%s", stdinTrace)
	}
}
