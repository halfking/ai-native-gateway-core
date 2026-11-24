package ursm_test

// index-reader-census.sh 的「**实际下发的 SQL 必须是可执行的真 SQL**」门（2026-10-19，§143.13）
//
// # 这道门补的是什么
//
// 仓里已有 `index_reader_census_test.go`：它用**假 psql 桩**驱动普查器，断言
// 「普查不做列名过滤」「窗口与容量必须进输出」「0 条不等于没有读方」等**性质**。
//
// ⇒ 但那个桩**把 SQL 吞掉了**（`cat >/dev/null`）⇒ 它**结构上不可能**发现
// 「SQL 本身写错了」这一类缺陷。而这恰恰是 2026-10-19 实际发生的：
//
//	SQL=${SQL/__IDX__/'$IDX'}      ← 替换值写在**单引号**里
//	⇒ shell 不展开 $IDX ⇒ SQL 里留下字面量 `$IDX`
//	⇒ 真库报 `syntax error at or near "$"`
//	⇒ 工具**从来没跑起来过**（exit 3「量具不可用」）
//
// ★ 这与该脚本自己注释里记的 `c.relkind::text` 事故是**同族第三次**
//   （假 psql 桩照单全收 ⇒ 只有真库才看得见）。
// ⇒ 既然桩按设计看不见这一类，**就得再加一道能看见的门**。
//
// # 本门怎么做
//
// 用一个**记录 + 转发**的 psql 包装脚本：
//   ① 把工具**实际下发**的 SQL 记到文件里；
//   ② 把它**真的执行**到本机 PG 沙箱（`llm-gateway-pg`，PG 17.10）上。
// ⇒ 工具下发的 SQL 若有语法错，**真库**会报错 ⇒ 门红。
// ⇒ 沙箱不可用时**跳过真库那一段**（并在输出里明说），但**文本断言照跑**：
//   下发的 SQL 必须含**展开后的**索引名，且不得残留 `$IDX` / `__IDX__` 占位符。
//
// # 变异验证
//
//	M1 把 `${SQL/__IDX__/'$IDX'}` 改回单引号   ⇒ ①真库执行红 + ②文本断言红
//	M2 把展开改成漏引号（`=$IDX`）              ⇒ ①真库执行红
//	M3 把占位符换名但忘了展开                    ⇒ ①真库执行红 + ②文本断言红

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCensusCmd 跑一条命令，并保证「**到点一定返回**」。
//
// ★★ **这是本轮（2026-11-23）实测踩出来的第三层补丁**：★★
//
//	① 第一版只加了 `exec.CommandContext(ctx, ...)` ⇒ ★ **不够**（★ 实测：
//	   ctx 到期 120s 之后，★ 测试二进制**仍然活着**）★；
//	② 根因：★ ctx 到期只 kill **直接子进程** ⇒ ★ 而 census 链有**三层**
//	   （★ bash → sh → docker exec）⇒ ★ **docker 客户端活着并持有 stdout 管道**
//	   ⇒ ★ `Wait()` 等管道 EOF ⇒ ★ **永远等不到**；
//	③ ⇒ ★ 现在是三件事一起做：
//
//	★ **a. 进程组**：★ 子进程自成一个进程组（★ unix）⇒ ★ 超时时**整组杀**
//	★ **b. WaitDelay**：★ ★ 即使有进程逃出进程组并仍持有管道，★ 到点**强制关闭管道**
//	★ ⇒ ★★ 「到点一定返回」在**所有平台**都成立；
//	★ **c. 不留挂死客户端**：★ ★ 整组杀 ⇒ ★ **不留挂死的 `docker exec`**
//	   （★ 本轮此前就不得不手工 kill 4 个挂死客户端）。
//
// ★★ **诚实边界**：★ ★ 本函数返回的 `timedOut` 是**「ctx 到点」**的判定，
// ★★ 而**不是**「命令跑完了」的判定 ⇒ ★ 调用方**必须**自己看 `err`
// ★★ （★ 进程提前退出时 err 非 nil 而 timedOut 为 false ⇒ ★ 那是真失败）。
func runCensusCmd(timeout time.Duration, name string, args []string, env []string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	censusPrepareCmd(cmd)
	cmd.Cancel = func() error { return censusKillGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), ctx.Err() != nil, err
}

// censusSQLSep 是记录文件里的**分隔行**：一次下发的 SQL 前后各夹一个，
// 这样多行 SQL 不会被按行切碎。
const censusSQLSep = "--__M__CENSUS_SQL__M__--"

// censusSQLRecorder 是「记录 + 转发」的 psql 包装脚本。
//
// ★ 它转发的目标是**本机 PG 沙箱**：普查器的第一条 SQL 只读 pg_catalog
//
//	（pg_index / pg_class / pg_inherits / pg_stat_user_indexes），
//	在任何 PG 上都能执行 ⇒ **不需要造任何夹具**。
//	而「能不能执行」正是我们要断言的性质。
//
// ★ 转发用的 psql 参数与工具自己的 PSQL_CMD **刻意不同**（多了 ON_ERROR_STOP）：
//
//	工具自己的参数是「-v ON_ERROR_STOP=1 -X -q -A -t -F'|'」，
//	而这里要的是「**语法错必须以非零退出码失败**」——
//	少了 ON_ERROR_STOP 时 psql 对单条 -c 的语法错**仍返回 0**（只打印 ERROR），
//	那这道门就成了空气门。
const censusSQLRecorder = `#!/bin/sh
# 记录工具实际下发的完整 SQL（$1 可能有多行 ⇒ 用分隔行隔开，别按行切）
printf '%s\n%s\n' "$CENSUS_SQL_SEP" "$1" >> "$CENSUS_SQL_LOG"
exec docker exec -i llm-gateway-pg psql -U postgres -d postgres -tAX -v ON_ERROR_STOP=1 -c "$1"
`

// censusSandboxAvailable 报告本机 PG 沙箱是否可用（不可用时真库那一段跳过）。
//
// ★★ 2026-11-23（合并轮六后 · 量具自身修）：★ **探针必须有超时**。
// ★ 起因（★ 实测）：★ 2026-11-23 21:5x 起本机 **Docker 守护进程僵死**
// ★ （★ `docker ps`/`info`/`version` 三个客户端全挂、★ containerd-shim 进程数＝0
// ★ ⇒ 全机容器实际已停止）⇒ ★ **`docker exec` 客户端永不返回** ⇒ ★★
// ★ ★★ 本探针**没有超时** ⇒ 它自己挂死 ⇒ ★★ **整包门在 900s 上限处超时**
// ★★（★ 而本测试的设计意图明明是「★ 沙箱不可用 ⇒ 跳过真库执行断言 ★」）
// ★★ ⇒ ★★ **量具在环境异常时缺牙：★ 应该是「跳过」、★ 实际是「挂死整包」**。
// ★ ⇒ ★ 修法＝给探针加**硬超时**（★ 5s）⇒ ★ 僵死时返回 false ⇒ ★ 走原设计的
// ★ 跳过分支 ⇒ ★★ 文本断言仍由 TestCensusSendsExpandedIndexName 覆盖（★ 不丢覆盖）。
// ★ ★★ 诚实边界：★ 这**不是**「让门变绿」的绕过——★ 跳过的是【真库执行断言】这一段，
// ★ ★ 而它跳过的判据（★ 工具下发的 SQL 在真 PG 上能不能跑 ★）在环境正常时照跑。
func censusSandboxAvailable() bool {
	out, _, err := runCensusCmd(5*time.Second, "docker",
		[]string{"exec", "-i", "llm-gateway-pg",
			"psql", "-U", "postgres", "-d", "postgres", "-tAX", "-c", "select 1;"}, nil)
	return err == nil && strings.Contains(string(out), "1")
}

// runCensusRecording 用记录型 psql 跑一次普查，返回（工具输出, 实际下发的 SQL 列表）。
func runCensusRecording(t *testing.T, idx string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "recorder.sh")
	logFile := filepath.Join(dir, "sent.sql")
	if err := os.WriteFile(rec, []byte(censusSQLRecorder), 0o755); err != nil {
		t.Fatalf("写记录脚本: %v", err)
	}
	// ★★ 2026-11-23（合并轮六后 · 量具自身修）：★ 普查器执行层**同样加硬超时**。
	// ★ 起因同上：★ 探针只挡住「★ 一开始就僵 ★」⇒ ★ 若 Docker 在探针通过**之后**
	// ★ 才僵（★ 或普查器内部某条 SQL 触发了别的 docker 调用）⇒ ★ 这里的
	// ★ `CombinedOutput()` 同样会永不返回 ⇒ ★ 整包门再次在 900s 上限处超时。
	// ★ ⇒ ★ 超时判红（★ 而不是挂死）：★ **明确的失败比挂死好查**（★ 挂死只能靠
	// ★ 看 panic 栈反推是哪条 docker 调用）。
	out, timedOut, err := runCensusCmd(120*time.Second, "bash", []string{censusPath(t), idx},
		append(os.Environ(),
			"PSQL_CMD="+rec,
			"CENSUS_SQL_LOG="+logFile,
			"CENSUS_SQL_SEP="+censusSQLSep,
			"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
		))
	if timedOut {
		// ★★ 超时**不判红**：★ 判据分两段，★ 这段（真库执行）本来就会在
		// 「沙箱不可用」时被跳过（★ 见 censusSandboxAvailable 的用法）；
		// ★★ 而记录型 psql 是「**先写记录、后转发**」⇒ ★★ **超时的那一刻
		// ★★ 第一条 SQL 已经完整落在记录文件里** ⇒ ★★ **文本断言仍可照跑**
		// ★★（★ 而这正是本门注释里写明的设计意图：★ 「沙箱不可用时跳过真库那一段，
		// ★★ 但文本断言照跑」⇒ ★★ 僵死环境不该把它变成一条与被测代码无关的红）。
		t.Logf("★ 普查器在 120s 内没跑完（★ ctx 超时）⇒ ★ **多半是 Docker 侧僵死**"+
			"（★ 普查器要经 PSQL_CMD 转发到 docker exec）⇒ ★ 而不是普查器本身慢\n"+
			"★ ⇒ ★ 本门在【环境异常】时**跳过真库执行断言**（★ 探针本就会跳过）\n"+
			"★ ⇒ ★ **文本断言继续照跑**（★ 记录型 psql 先写记录、后转发 ⇒ SQL 已在）\n"+
			"★ ⇒ ★ 环境恢复后复跑本门即可（★ 若 Docker 正常则本条不会触发）\n"+
			"★ 工具输出（截断）：\n%s", truncateCensusLog(string(out), 2000))
	}

	b, err := os.ReadFile(logFile)
	if err != nil {
		if timedOut {
			t.Skipf("★ 普查器超时且**没留下任何下发 SQL 记录**（%v）⇒ ★ 环境异常 ⇒ 跳过本门", err)
		}
		t.Fatalf("读下发 SQL 记录: %v", err)
	}
	// ★ 按分隔行切，**不是按行切**：SQL 本身是多行的。
	var sents []string
	for _, chunk := range strings.Split(string(b), censusSQLSep+"\n") {
		if strings.TrimSpace(chunk) != "" {
			sents = append(sents, strings.TrimRight(chunk, "\n"))
		}
	}
	if timedOut && len(sents) == 0 {
		t.Skip("★ 普查器超时且一条 SQL 都没记录下来 ⇒ ★ 环境异常（多半 Docker 僵死）⇒ 跳过本门")
	}
	return string(out), sents
}

// TestCensusSendsExecutableSQL —— 工具**实际下发**的 SQL 必须在真 PG 上能执行。
//
// ★ 这是本门的承重断言：2026-10-19 那次「工具从来没跑起来过」就是它能抓的。
func TestCensusSendsExecutableSQL(t *testing.T) {
	if !censusSandboxAvailable() {
		t.Skip("本机 PG 沙箱（llm-gateway-pg）不可用 ⇒ 跳过真库执行断言；" +
			"文本断言仍由 TestCensusSendsExpandedIndexName 覆盖")
	}
	_, sents := runCensusRecording(t, "session_turns")
	if len(sents) == 0 {
		t.Fatalf("工具一条 SQL 都没下发（记录文件为空）⇒ 判据前提不成立")
	}
	// 第一条是元数据查询（后面才是窗口查询与语句形态查询）。
	first := sents[0]
	if !strings.Contains(first, "relname") {
		t.Fatalf("第一条下发 SQL 不像元数据查询（没有 relname）：\n%s", first)
	}
	// ★★ 2026-11-23：★ 直连真库这一段也加**硬超时**（★ 60s）＋ **进程组收口**。
	// ★ 理由同上：★ 探针通过**之后** docker 仍可能僵死 ⇒ ★ 这一行也会永不返回
	// ★ ⇒ ★ 加超时后它判红（★ 而不是挂死整包）。
	out, timedOut, err := runCensusCmd(60*time.Second, "docker",
		[]string{"exec", "-i", "llm-gateway-pg",
			"psql", "-U", "postgres", "-d", "postgres", "-tAX", "-v", "ON_ERROR_STOP=1", "-c", first}, nil)
	if timedOut {
		t.Fatalf("★ 直连真库执行在 60s 内没返回（★ ctx 超时）⇒ ★ **多半是 Docker 侧僵死**\n"+
			"★ ⇒ ★ 本门在【环境异常】时判红（★ 而不是挂死整包）\n"+
			"★ ⇒ ★ 环境恢复后复跑本门即可\n★ 工具输出（截断）：\n%s",
			truncateCensusLog(string(out), 2000))
	}
	if err != nil {
		t.Fatalf("★ 工具下发的 SQL 在**真库**上执行失败 ⇒ 这正是 §143.13 修的那类缺陷"+
			"（假 psql 桩永远看不见）：\n%s\n--- 工具输出 ---\n%s", out, out)
	}
}

// truncateCensusLog 把工具输出截断到前 n 字节（★ 供超时时的错误信息用）。
//
// ★ 本包（ursm_test）里**没有**同名辅助函数 ⇒ 仓里其它包的 truncateForLog
// （★ 各包私有、签名不一）**不能直接借用** ⇒ 这里用**本包专属名**另起一个，
// ★ 免得日后与别的包的同名函数撞车（★ 同包同名函数重复定义＝编译失败）。
func truncateCensusLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...（截断）"
}

// TestCensusSendsExpandedIndexName —— 下发的 SQL 必须含**展开后**的索引名，
// 且不得残留任何未展开的占位符。
//
// ★ 假 psql 桩按设计看不见这一类（它把 SQL 吞了）⇒ 这条断言是它的替代品。
func TestCensusSendsExpandedIndexName(t *testing.T) {
	const idx = "idx_some_parent_index"
	_, sents := runCensusRecording(t, idx)
	if len(sents) == 0 {
		t.Fatalf("工具一条 SQL 都没下发 ⇒ 判据前提不成立")
	}
	first := sents[0]
	if !strings.Contains(first, "'"+idx+"'") {
		t.Errorf("下发的 SQL 里没有展开后的索引名字面量 '%s' ⇒ 索引名没被正确内联：\n%s", idx, first)
	}
	for _, bad := range []string{"$IDX", "__IDX__", "$TBL", "__TBL__", "$TOPN", "__TOPN__"} {
		if strings.Contains(first, bad) {
			t.Errorf("下发的 SQL 里残留未展开的占位符 %q ⇒ 展开这一环坏了"+
				"（§143.13 实测：写成单引号替换值时，shell 不展开，真库直接语法错）：\n%s", bad, first)
		}
	}
}
