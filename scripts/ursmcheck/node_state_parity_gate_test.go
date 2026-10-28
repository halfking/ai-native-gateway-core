//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// node_state_parity_gate_test.go —— 节点冷却语义「多状态面对拍」检查器的**行为门**
// （P1-D-②，audit 2026-10-28 §4「六面写回的对拍门」）。
//
// # 这道门补的是哪块缺口
//
// 「这个节点现在不能路由」有三个**各自独立**的存储面：
//
//	① Redis NodeState（disabled / disabled_until —— 路由热路径权威）
//	② DB node_probe_state（next_retry_at —— 探针退避阶梯）
//	③ DB credentials.circuit_state（内存 breaker 的 DB 镜像）
//
// 三面无共享事务、写入方各异（探针 worker mirror / 热路径 Lua / DBStateSync
// 异步队列）⇒ 一面静默写失败（R149 实证形态）就退避被架空或静默下线。
//
// ⇒ 本门守的是 `scripts/audit/check-node-state-parity.sh`（★ 只读：
//   probe 模式 SELECT + redis GET，classify 模式纯文本判定）。
//
// # ★ 为什么必须做成【行为层】门（crc 门同理由）
//
// 只断言「脚本里出现过 class_P 字符串」的话，把判定写反（所有输入都返回
// consistent）⇒ 门依然绿。⇒ 本门核心是**真的跑 classify 模式**，喂合成清单，
// 断言【判定词】+【退出码】+【分类计数】三者同时对：
//
//	P 类（DB 退避在生效、NodeState 不知道）⇒ inconsistent / 退出码 2
//	Q 类（breaker open、全凭据无 NodeState 禁用证据）⇒ inconsistent / 退出码 2
//	无矛盾清单（含容差内行：短退避 / NodeState 已禁用 / paused 行根本不进清单）
//	                                          ⇒ consistent / 退出码 0
//
// ★★ 门必须自带【阳性对照】+【反向对照】：
//
//	① 喂 P ⇒ 必须红；② 喂空 ⇒ 必须绿（否则「永远红」也算通过）；
//	③ ★★ 同一批行，把「NodeState 不知道」改成「NodeState 已禁用」⇒
//	   判定必须从 inconsistent 翻回 consistent（证明不是恒红）；
//	④ P 与 Q 同时出现 ⇒ 分类计数必须分别报数（证明两类不是一锅算）。
//
// # ★ 诚实边界（本门**没有**做的事）
//
// 本门只用合成清单测 classify 模式，**不连任何库**（DB 与 Redis 都不连）。
// 「probe 模式在真库真 Redis 上能产出正确底表」不由本门证明 —— 那是
// 运维侧跑 probe 模式的人工观测面。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// nspRelPath 是被测脚本在仓内的相对路径。门指错文件 ⇒ 红；脚本被挪走 ⇒ 红。
const nspRelPath = "scripts/audit/check-node-state-parity.sh"

func nspScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return filepath.Join(root, filepath.FromSlash(nspRelPath))
}

// nspRun 跑 classify 模式（★ 喂合成清单），返回 stdout / stderr / 退出码。
func nspRun(t *testing.T, stdin string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", nspScriptPath(t), "classify")
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("跑脚本失败（不是退出码问题）：%v\nstderr:\n%s", err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// nspMeta 从 stdout 的 TSV 里取一个 #meta 键的值。
func nspMeta(t *testing.T, stdout, key string) string {
	t.Helper()
	for _, ln := range strings.Split(stdout, "\n") {
		f := strings.Split(ln, "\t")
		if len(f) >= 3 && f[0] == "#meta" && f[1] == key {
			return f[2]
		}
	}
	t.Fatalf("stdout 里没有 #meta\\t%s 这一行：\n%s", key, stdout)
	return ""
}

// nspRow 铸一行对拍底表（列序 = 脚本 probe 输出的表头）。
func nspRow(cred, model, nextIn, provider, ckt, quota, avail, dflag, duntilIn string) string {
	return strings.Join([]string{cred, model, nextIn, provider, ckt, quota, avail, dflag, duntilIn}, "\t")
}

func TestNodeStateParityGate_ClassifyContract(t *testing.T) {
	// ── ② 反向对照先行：空清单必须 consistent / 0（否则「永远红」也算通过）。
	stdout, _, code := nspRun(t, "")
	if code != 0 || nspMeta(t, stdout, "verdict") != "consistent" {
		t.Fatalf("空清单必须 consistent/0：code=%d verdict=%s", code, nspMeta(t, stdout, "verdict"))
	}

	// ── 一致形态：容差内的三行（短退避 45s / NodeState 已禁用 /
	//    disabled_until 仍在未来）—— 全都不该判矛盾。
	consistent := strings.Join([]string{
		nspRow("3", "gpt-4", "45", "1", "closed", "", "ready", "0", "-10"),
		nspRow("4", "gpt-4", "600", "1", "closed", "", "ready", "1", "300"),
		nspRow("5", "claude", "3600", "2", "closed", "", "ready", "0", "1200"),
	}, "\n") + "\n"
	stdout, _, code = nspRun(t, consistent)
	if code != 0 || nspMeta(t, stdout, "verdict") != "consistent" {
		t.Fatalf("容差内清单必须 consistent/0：code=%d stderr 判定见上", code)
	}
	if nspMeta(t, stdout, "class_P_backoff_unseen") != "0" || nspMeta(t, stdout, "class_Q_open_without_trace") != "0" {
		t.Fatal("一致形态的分类计数必须全 0")
	}

	// ── ① 阳性对照 P：DB 退避 600s 在生效、NodeState 完全不知道。
	pRow := nspRow("7", "gpt-4", "600", "1", "closed", "", "ready", "0", "-999999")
	stdout, _, code = nspRun(t, pRow+"\n")
	if code != 2 || nspMeta(t, stdout, "verdict") != "inconsistent" {
		t.Fatalf("P 类必须 inconsistent/2：code=%d", code)
	}
	if nspMeta(t, stdout, "class_P_backoff_unseen") != "1" {
		t.Fatalf("P 类计数 = %s，want 1", nspMeta(t, stdout, "class_P_backoff_unseen"))
	}

	// ── ③ 反向对照：同一行，只把 NodeState 改成已禁用 ⇒ 必须翻绿。
	//    （证明 P 判定不是「看见退避行就红」的恒红门。）
	flipped := nspRow("7", "gpt-4", "600", "1", "closed", "", "ready", "1", "300")
	stdout, _, code = nspRun(t, flipped+"\n")
	if code != 0 {
		t.Fatalf("P 行翻转 NodeState 后必须 consistent/0：code=%d", code)
	}

	// ── ① 阳性对照 Q：breaker open + 凭据在服务 + 该凭据唯一一行无禁用证据。
	qRow := nspRow("9", "gpt-4", "45", "1", "open", "", "ready", "0", "-10")
	stdout, _, code = nspRun(t, qRow+"\n")
	if code != 2 || nspMeta(t, stdout, "verdict") != "inconsistent" {
		t.Fatalf("Q 类必须 inconsistent/2：code=%d", code)
	}
	if nspMeta(t, stdout, "class_Q_open_without_trace") != "1" {
		t.Fatalf("Q 类计数 = %s，want 1", nspMeta(t, stdout, "class_Q_open_without_trace"))
	}
	if nspMeta(t, stdout, "class_P_backoff_unseen") != "0" {
		t.Fatalf("Q 行不得误入 P 类：class_P = %s", nspMeta(t, stdout, "class_P_backoff_unseen"))
	}

	// ── Q 的两条豁免必须真的豁免（证明不是「见 open 就红」）：
	//    quota 耗尽 / suspended 的 open 行是生命周期停用，不是矛盾。
	exempt := strings.Join([]string{
		nspRow("10", "gpt-4", "45", "1", "open", "permanently_exhausted", "ready", "0", "-10"),
		nspRow("11", "gpt-4", "45", "1", "open", "", "suspended", "0", "-10"),
	}, "\n") + "\n"
	stdout, _, code = nspRun(t, exempt)
	if code != 0 || nspMeta(t, stdout, "class_Q_open_without_trace") != "0" {
		t.Fatalf("quota 耗尽 / suspended 的 open 必须豁免：code=%d class_Q=%s", code, nspMeta(t, stdout, "class_Q_open_without_trace"))
	}

	// ── Q 的正向豁免：open 凭据有另一行带禁用证据 ⇒ 该凭据不判 Q。
	qSaved := nspRow("9", "gpt-4-turbo", "45", "1", "open", "", "ready", "1", "600")
	stdout, _, code = nspRun(t, qSaved+"\n")
	if code != 0 || nspMeta(t, stdout, "class_Q_open_without_trace") != "0" {
		t.Fatalf("open 凭据任一行有禁用证据即豁免：code=%d class_Q=%s", code, nspMeta(t, stdout, "class_Q_open_without_trace"))
	}

	// ── ④ P 与 Q 同时出现 ⇒ 两类分别报数（不是一锅算）。
	both := strings.Join([]string{pRow, qRow}, "\n") + "\n"
	stdout, _, code = nspRun(t, both)
	if code != 2 {
		t.Fatalf("P+Q 必须 inconsistent/2：code=%d", code)
	}
	if nspMeta(t, stdout, "class_P_backoff_unseen") != "1" || nspMeta(t, stdout, "class_Q_open_without_trace") != "1" {
		t.Fatalf("P+Q 必须分别报 1/1：P=%s Q=%s",
			nspMeta(t, stdout, "class_P_backoff_unseen"), nspMeta(t, stdout, "class_Q_open_without_trace"))
	}
}

// TestNodeStateParityGate_ScriptShape pins the script's structural contract:
// probe 模式必须只读（无 UPDATE/DELETE/SET/DEL），判定逻辑必须在 classify
// 分支（probe 分支不得含 P/Q 类的判定词），Redis key 形状与仓内 nodeKey
// 一致 —— 脚本被顺手改成会写状态的话，这里红。
func TestNodeStateParityGate_ScriptShape(t *testing.T) {
	b, err := os.ReadFile(nspScriptPath(t))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", nspRelPath, err)
	}
	src := string(b)

	// 只读契约：SQL 区与 redis 调用都不得出现写形状。
	for _, forbidden := range []string{"UPDATE ", "DELETE ", "INSERT ", "DEL ", "SET ", "redis-cli -x"} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("对拍脚本必须只读，但出现了写形状 %q", forbidden)
		}
	}

	// Redis key 与 credentialfpslot.nodeKey（llmgw:cred_fp_node:<cred>:<model>）同形状。
	if !strings.Contains(src, "llmgw:cred_fp_node:${cred}:${model}") {
		t.Fatal("Redis key 形状与仓内 nodeKey 不同 ⇒ 对拍的面就不是那个面")
	}

	// probe 分支必须引用 node_probe_state 与 credentials 两张表（面要齐）。
	for _, table := range []string{"node_probe_state", "credentials"} {
		if !strings.Contains(src, table) {
			t.Fatalf("probe SQL 缺少表 %s ⇒ 对拍缺一面", table)
		}
	}

	// 判定必须在 classify：class_P / class_Q 的计数行只允许出现在 classify
	// 产出区（存在性断言 —— 行为层已由上面的 classify 合同测试钉死）。
	for _, marker := range []string{"class_P_backoff_unseen", "class_Q_open_without_trace"} {
		if !strings.Contains(src, marker) {
			t.Fatalf("classify 缺分类计数 %s", marker)
		}
	}
}
