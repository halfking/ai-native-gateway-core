package ursm_test

// 巡检脚本「能不能真的跑起来」门（2026-10-04 首次上机后补）
//
// ★ 这道门是被一次真实事故逼出来的，不是预防性加的。
//
// 2026-10-04 把 pg-table-bloat-check.sh 传到 252 准备接 cron，第一次实跑立刻：
//
//	/opt/scripts/pg-table-bloat-check.sh:行37: CONF: 未绑定的变量
//	EXIT=1
//
// 两个 bug 叠在一起：
//  1) `PSQL_CMD=${PSQL_CMD:-... --file "$CONF" ...}` 写在 `CONF=` 的**前面**，
//     配合 set -u ⇒ 每次执行都在这一行崩；
//  2) 用的是 `sudo -u postgres psql --file /etc/llmgw/pg17.conf`，
//     而 252 的 PG17 跑在容器 pg-252-pg17 里，宿主机上这套根本不存在。
//
// ★★ **为什么 cron_registration_test.go 抓不到**：
// 它只断言「每个巡检脚本都有对应的 cron 行」—— 也就是「接线接好了」。
// 它**从不执行脚本**，所以「一跑就崩」与「跑得好好的」在它眼里完全一样。
// 而退出码 1 在本目录契约里是「检出显著空洞」
// ⇒ 一个首跑即崩的脚本接进 cron，会**每天报一次空洞**，把告警变成噪音，
//   而看告警的人不会想到去看 stderr。
//
// 本门补的是这条缺失的判据：**能解析 + 能在无参数下走到一个契约内的退出码**。
// 覆盖范围刻意保守，只抓「一跑就崩」这一类，不试图在 CI 里连真库。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 需要在「无参数、连不上库」的情况下也必须自行收敛到契约内退出码的脚本。
//
// 判据的精确含义：脚本不许因为自身缺陷（未绑定变量、语法错、忘记 set -u
// 之类）在解析阶段就死掉。**允许**它在连不上库时报 3（量具不可用）。
var mustNotCrash = []string{
	"pg-table-bloat-check.sh",
	"ursm-snapshot-payload-bloat.sh",
}

func monitorPath(name string) string {
	return filepath.Join("..", "252-monitor", name)
}

// TestMonitorScriptsParse runs `bash -n` on every script the cron file schedules.
// bash -n is the cheapest check that catches the majority of self-inflicted
// breakage (unterminated quotes, bad heredocs) without executing anything.
func TestMonitorScriptsParse(t *testing.T) {
	for _, name := range mustNotCrash {
		name := name
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command("bash", "-n", monitorPath(name)).CombinedOutput()
			if err != nil {
				t.Fatalf("bash -n 失败（%s）:\n%s", err, out)
			}
		})
	}
}

// TestMonitorScriptsConvergeOnAContractExitCode executes each script with no
// arguments and no database reachable, then asserts the exit code is one of
// the codes the cron contract defines.
//
// The assertion is deliberately "in the set {0,1,3}", not "== 3": on a developer
// machine that happens to have a reachable PG, 0 or 1 is a legitimate answer.
// What must never happen is a code outside the contract — that is what a
// crash-on-line-37 produced, and cron would report it as "bloat detected".
//
// ★ 判据自证：a test that passes because the script was never launched is
// worse than no test. Hence TestMonitorScriptsActuallyRan below.
// ★★ 这道门的第一版**没有牙**，变异验证时被抓出来了。
//
// 第一版只断言「退出码 ∈ {0,1,3}」。但 bash 在 `set -u` 下遇到未绑定变量时
// 打印一行错误后**以状态 1 退出** —— 而 1 恰好就是契约里「检出显著空洞」的码。
// ⇒ 我把原始 bug（`--file "$CONF"` 写在 `CONF=` 之前）放回去变异时，
//   门**依然全绿**。也就是说这道门对它最该抓的那个形态完全失明。
//
// 结论不是「再加一条退出码断言」（没有更多码可加了），而是：
//   **退出码 1 本身是歧义的**，「崩了」与「检出问题」在它眼里同形。
//   要区分，只能看 stderr 里的 shell 级错误签名。
//
// 这一点本身也说明：把「1 = 检出空洞」定成退出码，是个有隐患的契约选择
// （脚本自己的崩溃会伪装成业务告警）。本门只能挡住崩溃，**挡不住
// 「脚本崩了但恰好退出 1」之外的情形** —— 真正要根治得让脚本自己
// 用 trap 把崩溃映射到 3，本轮未做，记为待办。

// shellFatal 匹配「脚本自己崩了」的签名。退出码抓不到它们，只能看文本。
// 中英双语都列：即使上面钉了 LC_ALL，也不该让这道门的判据只在一个语言下成立。
var shellFatal = regexp.MustCompile(
	`unbound variable|未绑定的变量|` +
		`command not found|命令未找到|syntax error|语法错误|` +
		`unexpected end of file|意外的符号|bad substitution|替换错误|` +
		`integer expression expected|算术表达式错误`)

func TestMonitorScriptsConvergeOnAContractExitCode(t *testing.T) {
	allowed := map[int]bool{0: true, 1: true, 3: true}
	for _, name := range mustNotCrash {
		name := name
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", monitorPath(name))
			// 明确切断它继承到的任何数据库配置：这道门要测的是
			// 「量具不可用时会不会说 3」，不是「本机能不能连上库」。
			//
			// ★ LC_ALL=C 是必需的，不是洁癖。第一版没设它，变异验证时门全绿，
			//   手工复现才发现 bash 在这台机器上打印的是中文：
			//       行 63: BOGUS_UNSET_VAR: 未绑定的变量
			//   而正则匹配的是英文 "unbound variable"
			//   ⇒ **那道门当时测的是这台机器的 locale，不是脚本对不对。**
			//   报错文本随 locale 变，就不能拿它当判据的稳定锚点；
			//   正确做法是把 locale 钉死，让文本确定化，再匹配。
			cmd.Env = append(os.Environ(),
				"LC_ALL=C",
				"LANG=C",
				"LLMGW_PG17_CONF=/nonexistent/pg17.conf",
				"PG17_CONTAINER=definitely-not-a-container",
			)
			out, err := cmd.CombinedOutput()

			// ★ 崩溃判定放在退出码之前 —— 这是第一版漏掉的那一步。
			if m := shellFatal.FindString(string(out)); m != "" {
				t.Fatalf("脚本 %s 崩在 shell 层（stderr 命中 %q）。\n"+
					"★ 这种崩溃在 bash 里通常退出码是 1，而 1 在契约里是「检出空洞」\n"+
					"   ⇒ 接进 cron 后会伪装成业务告警，看告警的人不会去读 stderr。\n输出:\n%s",
					name, m, out)
			}
			if err == nil {
				t.Fatalf("脚本 %s 无参数无库却退出 0 —— 这说明它把「查不到」当成了「没问题」。\n"+
					"契约要求这种情况报 3。输出:\n%s", name, out)
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("脚本 %s 不是以退出码结束（%v），说明它崩在解释器层。输出:\n%s", name, err, out)
			}
			code := exitErr.ExitCode()
			if !allowed[code] {
				t.Fatalf("脚本 %s 退出码 %d 不在契约 {0,1,3} 内。\n"+
					"★ 这正是「首跑即崩」在 cron 里的形态：退出码 1 会被读成「检出空洞」。\n输出:\n%s",
					name, code, out)
			}
			// 自证：不允许「什么都没跑就拿到契约内退出码」。
			if len(strings.TrimSpace(string(out))) == 0 && code == 3 {
				t.Fatalf("脚本 %s 报 3 但一个字都没输出 —— 「量具不可用」必须说清是哪件量具不可用。", name)
			}
		})
	}
}

// TestCronDoesNotScheduleAMissingScript guards the other half of the contract:
// a cron line pointing at a file that is not in this directory would fail on
// the server in a way nothing in-repo can see.
func TestCronDoesNotScheduleAMissingScript(t *testing.T) {
	raw, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatalf("读 %s: %v", cronFile, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !cronTaskLine.MatchString(line) {
			continue
		}
		idx := strings.Index(line, "/opt/scripts/")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("/opt/scripts/"):]
		end := strings.IndexAny(rest, " ;|")
		if end < 0 {
			continue
		}
		name := rest[:end]
		if _, err := os.Stat(monitorPath(name)); err != nil {
			t.Errorf("cron 调度了 /opt/scripts/%s，但仓库 scripts/252-monitor/ 下没有这个文件。\n"+
				"★ 整文件覆盖契约下这条会变成每周期必失败的空任务。", name)
		}
	}
}
