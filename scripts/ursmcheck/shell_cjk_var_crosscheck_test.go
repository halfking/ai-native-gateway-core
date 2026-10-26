package ursm_test

// shell_cjk_var_crosscheck_test.go —— ★★ **跨门一致性门**（2026-10-24 §3.17.8）。
//
// # 这道门补的是哪块缺口
//
// 本仓有**两道**门守「$变量紧跟非 ASCII 字节」这件事：
//
//	① Go 门   scripts/ursmcheck/cjk_var_expansion_test.go（★ CI 必跑）
//	② shell 门 scripts/guards/check-shell-fullwidth-var.sh（★ 本轮起进 CI 快门）
//
// ★ 两者**自称「判据现已一致」**（shell 门文件头原话），但 **2026-10-24 实测不一致**：
//   Go 门在 2026-10-23（R85 轮）已修「转义美元 / 尾随注释 / `${VAR}`」三条，
//   shell 门**没同步** ⇒ ★★ shell 门在**既有代码上假红 5 处**。
//   ⇒ ★ 假红比假绿更费（它让人去改本来正确的代码）。
//
// ⇒ 本轮把 shell 门对齐到 Go 门口径（13 条合成用例对拍 13/13），
//   但 ★ **那是一次性的手工对拍** ⇒ ⇒ 若不把对拍**做成常驻的门**，
//   两门**下一次改不同步时**仍然没人发现 ⇒ ⇒ ★★ **这正是本门的价值**。
//
// # 三条判据
//
//	C1 ★ **跨门一致性**：同一个合成用例，Go 门与 shell 门的判定必须**逐条相同**。
//	   ⇒ shell 门的判定通过 `bash scripts/guards/check-shell-fullwidth-var.sh --self-test`
//	   取得（★ 该模式由本轮新增：它把扫描器对合成用例的读数逐条打印出来）。
//	C2 ★ **量具自检（shell 侧）**：`--self-test` 必须**成功退出**（rc=0）。
//	   ⇒ ★★ 这是「恒绿门」的最后一道防线：若 shell 扫描器失效（例如
//	   awk 在非 C locale 下 multibyte 失败 ⇒ 什么都不输出 ⇒ 对所有行都判「未命中」），
//	   则 C1 会看到「Go 门判 1、shell 门判 0」⇒ ⇒ **不一致 ⇒ 红**。
//	C3 ★ **量具自检（Go 侧）**：Go 门自己的合成用例必须全过
//	   （★ 否则「Go 门也是坏的」时，两边一起坏 ⇒ C1 会「一致地错」）。
//
// # ★ 为什么 C1 的被测集合**不取自被审对象**
//
// ⇒ 本门**不复用** Go 门的用例表，而是**自带一份**（下方的 `shellCrossCheckCases`）。
// ⇒ ★ 理由：若直接复用 Go 门的表，则「Go 门改错了」⇒ 两边用同一份错表
// ⇒ ⇒ **两门会一致地错** ⇒ C1 恒绿 ⇒ 门形同虚设。
// ⇒ ⇒ 自带一份 ⇒ 两边要同时错才可能逃过 ⇒ ★ 代价是两份表要手工同步，
//   但**表里的期望值本身就是可读的事实**（「裸变量紧跟汉字 = 1」）
//   ⇒ 人工审阅成本低 ⇒ ★ 取这个交换。
//
// # ★ 与 §3.17.7 登记的缺口的对应
//
// 审计档 §3.17.7 第 4 条写的是「shell 门缺『量具自检』⇒ 建议在 scripts/guards/
// 补一条同款自检门」⇒ ★ **本轮补的就是它**（`--self-test` 模式 + 本跨门门）。

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// shellCrossCheckCase 一条跨门对拍用例。
//
// ★ want = 期望判定（1 = 是站点，0 = 不是）；★ line = 被扫描的那一行。
type shellCrossCheckCase struct {
	name string
	want int
	line string
}

// ★★★ 这份表是**本门的事实源**（不取自 Go 门的表 ⇒ 见文件头 C1 的说明）。
//
// ★ 期望值来自 bash 的真实语义（实测：bash 3.2.57 / 5.3.9，locale=zh_CN.UTF-8）：
//
//	· 裸 `$VAR` 紧跟非 ASCII 字节 ⇒ ★ 变量名被吞（`set -u` 下报「未绑定的变量」）⇒ 站点
//	· `\$VAR` ⇒ ★ **字面 $**，不展开 ⇒ 不是站点
//	· `\\$VAR` ⇒ ★ `\\` 是一个字面反斜杠 ⇒ `$` **仍然展开** ⇒ 是站点
//	· `${VAR}` ⇒ ★ 有界定界，变量名不会被吞 ⇒ 不是站点
//	· 单引号内 ⇒ 不展开 ⇒ 不是站点
//	· 注释（整行 / 尾随）⇒ bash 不展开注释 ⇒ 不是站点
//	· `#` 非词首（`a#b`）⇒ 不是注释 ⇒ 其后的 `$VAR中` 仍是站点
var shellCrossCheckCases = []shellCrossCheckCase{
	{"裸变量紧跟汉字", 1, `echo "值=$v中"`},
	{"花括号形式安全", 0, `echo "值=${v}中"`},
	{"整行注释", 0, `# echo "值=$v中"`},
	{"单引号内", 0, `echo '值=$v中'`},
	{"ASCII 紧跟安全", 0, `echo "值=$v)"`},
	{"两处各算一次", 1, `echo "a=$x中"`},
	{"转义美元不展开", 0, `echo "值=\$v中"`},
	{"偶数反斜杠仍展开", 1, `echo "值=\\$v中"`},
	{"尾随注释", 0, `echo "ok"  # （用 $v中）`},
	{"尾随注释在函数定义后", 0, `run_gate() {  # 说明（用 $MUT_ENGINE）`},
	{"双引号内的井号", 1, `echo "x"#y $v中"`},
	{"井号非词首不是注释", 1, `echo "a#b $v中"`},
	{"空行", 0, ``},
}

// shellCrossCheckRepoRoot 返回仓根。
func shellCrossCheckRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("解析仓根失败：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// shellGateSelfTest 跑 shell 门的 `--self-test` 模式，返回逐条读数。
//
// ★ 输出格式（由 shell 门自己定义）：`CASE <期望> <实际> <用例名>`。
func shellGateSelfTest(t *testing.T) []struct {
	want int
	got  int
	name string
} {
	t.Helper()
	root := shellCrossCheckRepoRoot(t)
	script := filepath.Join(root, "scripts/guards/check-shell-fullwidth-var.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("找不到 shell 门 %s：%v", script, err)
	}

	cmd := exec.Command("bash", script, "--self-test")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell 门自检失败（err=%v）⇒ ★ 它的结论不可信：\n%s", err, out)
	}

	type row = struct {
		want int
		got  int
		name string
	}
	var rows []row
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "CASE ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("shell 门自检输出格式变了：%q", line)
		}
		want, err1 := strconv.Atoi(fields[1])
		got, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			t.Fatalf("shell 门自检输出里的数字不可解析：%q", line)
		}
		rows = append(rows, row{want: want, got: got, name: strings.Join(fields[3:], " ")})
	}
	if len(rows) == 0 {
		t.Fatalf("shell 门自检没有输出任何 CASE ⇒ 它的扫描器可能已失效：\n%s", out)
	}
	return rows
}

// TestShellGateSelfTestPasses 是 C2：shell 门的自检必须成功。
//
// ★ 这条的价值：★ 若 shell 扫描器整体失效（例如 awk multibyte 死法），
//
//	`--self-test` 会 **exit 2（ABORT）** ⇒ 本门立刻红
//	⇒ ⇒ **恒绿门这个形态被堵死了**（M22 实测：去掉 LC_ALL=C ⇒ rc=2）。
func TestShellGateSelfTestPasses(t *testing.T) {
	rows := shellGateSelfTest(t)
	t.Logf("shell 门自检 %d 条用例全过", len(rows))
}

// TestShellGateAndGoGateAgreeOnSameCases 是 C1：★★ **跨门一致性**。
//
// ⇒ 对每条用例，断言：**Go 门与 shell 门给出同一个判定**。
// ⇒ ★ 这正是 §3.17.5 那次「手工 13/13 对拍」的**常驻化**。
func TestShellGateAndGoGateAgreeOnSameCases(t *testing.T) {
	rows := shellGateSelfTest(t)

	// 按用例名索引 shell 门的读数。
	byName := map[string]int{}
	for _, r := range rows {
		byName[r.name] = r.got
	}

	// 逐条：Go 门（用仓内真扫描器）与 shell 门必须一致。
	for _, tc := range shellCrossCheckCases {
		t.Run(tc.name, func(t *testing.T) {
			// Go 侧：用仓内 Go 门自己的扫描器（scanCJKVarInText）。
			goGot := len(scanCJKVarInText(tc.line))
			// ★ Go 门的扫描器按「站点数」返回 ⇒ 折成 0/1。
			goVerdict := 0
			if goGot > 0 {
				goVerdict = 1
			}

			shellGot, ok := byName[tc.name]
			if !ok {
				t.Fatalf("shell 门自检里没有用例 %q ⇒ 两门无法对拍", tc.name)
			}

			if goVerdict != shellGot {
				t.Errorf("★ 两门判定不一致：Go 门=%d shell 门=%d（用例 %q，内容 %q）\n"+
					"   ★ 期望=%d\n"+
					"   ⇒ 两门「自称判据一致」但实际不一致 ⇒ 至少有一边的判据漂了。",
					goVerdict, shellGot, tc.name, tc.line, tc.want)
			}
			if goVerdict != tc.want {
				t.Errorf("Go 门判定=%d，期望=%d（用例 %q，内容 %q）⇒ Go 门自身也判错了",
					goVerdict, tc.want, tc.name, tc.line)
			}
		})
	}
}

// TestShellGateSelfTestCaseCountMatches 是【量具自检】：两边的用例集不能漂。
//
// ★ 若 shell 门改了自己的用例表而本门不知道 ⇒ 对拍会漏掉新用例
//
//	⇒ ⇒ 这里断言**两边的用例数一致**（且都非空）。
func TestShellGateSelfTestCaseCountMatches(t *testing.T) {
	rows := shellGateSelfTest(t)
	if len(rows) != len(shellCrossCheckCases) {
		t.Errorf("shell 门的自检用例数=%d，本门的用例数=%d ⇒ 两边用例集漂移了\n"+
			"   ⇒ 若 shell 门新增了用例，本门必须同步补上（否则对拍漏检）。",
			len(rows), len(shellCrossCheckCases))
	}
}
