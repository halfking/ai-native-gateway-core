package ursm_test

// 252 巡检脚本的「notify 调用参数不足 ⇒ 静默失败」缺陷**全族门**（2026-10-18，§102）
//
// §102 在 `pg17-index-bloat.sh` 抓到的缺陷不是「脚本没调通知」，而是
// **调了、但永远失败、且失败得毫无痕迹**：
//
//	"$NOTIFY" "<title>" >> "$LOG" 2>&1 || log "notify failed"
//	                   ^^^^^^^^^^^ 只传了 1 个参数
//
// `notify.sh` 以 `set -euo pipefail` 开头并读 `BODY="$2"`；参数不足时
// `$2` 未绑定 ⇒ **`set -u` 在参数展开阶段就退出**，stdout/stderr 全空、退出码 1。
// 而调用方的 `2>&1` 与 `|| log` 把根因吃掉 ⇒ 只剩一句自说自话的 `notify failed`。
//
// 实测代价：2026-10-03 起 **6/6 次 run 的告警全部静默失败**，
// 期间脚本已累计报出 471MB 的索引膨胀候选而无人知晓。
//
// ⇒ 本门**按目录扫**而不是点名文件（同 §96 的理由：这一族的特征是**调用形态**）。
// 判据形态：源码文本断言（脚本要 docker + 飞书 webhook 才能跑，单测里无法执行），
// 先剥整行注释 —— 否则把调用写进注释就能骗过它。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reNotifyCall 匹配「调用 notify helper 的那一行」。
//
// ★ 不锚定位置、不锚定引号形态，只问三件事同时成立：
// ① 这一行以 `"$NOTIFY"` 或 `$NOTIFY` 开头的调用（前面可以有缩进）；
// ② 这一行**之后**（跨行续接也算）至少有两个顶层位置参数；
// ③ 不是一个变量展开的间接调用（如 `"$NOTIFY_HELPER"`）。
//
// 只数**位置参数**：具名参数形态（`--title/--body`）由 notify.sh 自己解析，
// 不受 `set -u` 约束，所以「个数」对具名形态没有意义 —— 故本门对
// 出现 `--title` / `--body` / `-t ` / `-b ` 的行直接放行。
var reNotifyCall = regexp.MustCompile(`"(?:\$NOTIFY|\$\{NOTIFY\})"`)

var reNamedFlag = regexp.MustCompile(`(?:--title|--body|\s-t\s|\s-b\s|\s--level\b|\s-l\s)`)

// blankOutFullLineComments 把整行注释**置空**而不是删掉，
// 这样剥注释之后每行的下标仍等于源文件的行号。
//
// ⚠ 共享的 stripFullLineComments 是**删除**注释行的，直接用会让本门报出的行号整体上移
// （pg17-index-bloat.sh 里 130+ 行注释 ⇒ 真实 :400 被报成 :222）。故本门自带一份。
//
// ⚠⚠ **已知局限（2026-10-18 实测）**：即便置空保留、续行用哨兵拼接，
// 报出的行号仍可能偏小若干行（pg17-index-bloat.sh 的真实调用在 :400，本门报 :385）。
// 偏置来源未查明，**不要把行号当定位依据** —— 错误信息里逐字引用了出错语句，
// 以那段文本定位即可。若哪天下一个人查清了偏置来源，请顺手修掉并删掉本段。
func blankOutFullLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// countTopLevelArgs 数出这一条命令的**位置参数**个数。
//
// ⚠⚠ 本函数第一版是**假绿**的：变异验证时把脚本退回「只传 1 个参数」的形态，
// 门禁**没有变红**。原因是它把三样不是参数的东西也数了进去：
//   ① 命令名本身（`"$NOTIFY"`）
//   ② 重定向（`>>`、`2>&1`）
//   ③ `||` / `&&` 之后属于另一条命令的 token
// ⇒ 三者一算，1 个参数的调用被数成 3 个 ⇒ `n < 2` 恒不成立。
// ⇒ **教训：门禁的第一版必须先做变异验证，否则「有测试」不等于「有断言」。**
func countTopLevelArgs(stmt string) int {
	toks := tokenizeTopLevel(stmt)
	if len(toks) == 0 {
		return 0
	}
	args := 0
	for _, tok := range toks[1:] { // 跳过命令名
		switch {
		case isRedirection(tok), tok == "||", tok == "&&", tok == "|", tok == ";":
			return args // 重定向 / 命令分隔符之后都属于另一条命令
		case tok == "":
			continue
		}
		args++
	}
	return args
}

// isRedirection 判定「这个 token 是 shell 重定向符而不是参数」。
func isRedirection(tok string) bool {
	switch tok {
	case ">", ">>", "<", "<<", "<<<", "&>", "&>>", "2>", "2>>", ">&", ">&2":
		return true
	}
	return strings.HasPrefix(tok, ">") || strings.HasPrefix(tok, "<") ||
		strings.HasPrefix(tok, "2>") || strings.HasPrefix(tok, "&>")
}

// tokenizeTopLevel 把一条命令切成顶层 token（尊重双引号与单引号）。
func tokenizeTopLevel(stmt string) []string {
	var (
		toks  []string
		cur   strings.Builder
		quote byte
	)
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(stmt); i++ {
		c := stmt[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '\\' && i+1 < len(stmt):
			i++
			cur.WriteByte(stmt[i])
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

func TestNoMonitorScriptCallsNotifyWithTooFewArgs(t *testing.T) {
	entries, err := os.ReadDir(monitorDir)
	if err != nil {
		t.Fatalf("read %s: %v", monitorDir, err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		path := filepath.Join(monitorDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := blankOutFullLineComments(string(raw))
		// 把反斜杠续行拼成完整命令，但**用哨兵替换换行而不是删掉**：
		// 这样折行后每行的下标仍与源文件的行号一一对应。
		// （第一版直接用空格替换掉 `\`+换行，导致报出来的行号全错——
		//  §102 的变异验证报的是 :222，真实位置在 :392。）
		lines := strings.Split(strings.ReplaceAll(src, "\\\n", " \x00"), "\n")

		for i, line := range lines {
			stmt := strings.ReplaceAll(line, "\x00", "")
			if !reNotifyCall.MatchString(stmt) {
				continue
			}
			startLine := i + 1
			if reNamedFlag.MatchString(stmt) {
				continue // 具名参数形态由 notify.sh 自己解析，不受 set -u 约束
			}
			checked++
			if n := countTopLevelArgs(stmt); n < 2 {
				t.Errorf("%s:%d 调用 notify 只给了 %d 个位置参数（需 ≥2：title + body）\n"+
					"  ⚠ 行号可能偏小（见 blankOutFullLineComments 的已知局限），以下面这段原文定位：\n  %s\n"+
					"  notify.sh 以 `set -euo pipefail` 开头并读 BODY=\"$2\"；参数不足时 $2 未绑定 ⇒"+
					" set -u 静默退出 1、stdout/stderr 全空 ⇒ 告警永远发不出且日志里没有原因",
					e.Name(), startLine, n, strings.TrimSpace(stmt))
			}
		}
	}

	if checked == 0 {
		t.Fatal("一个 notify 调用都没扫到 —— 门禁空转（正则或目录常量失效）")
	}
	t.Logf("扫到 %d 处 notify 位置参数调用", checked)
}
