package ursm_test

// 巡检脚本的**shell 引号类**门（2026-10-20，§143.14）
//
// # 这道门补的是什么
//
// 2026-10-20 实测发现：`ursm-snapshot-health.sh` 有一段 SQL 注释里用了**反引号**：
//
//	-- 两段聚合各自独立算完再 1:1 连接。写成 `base_buckets b LEFT JOIN base_active a
//	-- ON a.slot=b.slot GROUP BY b.slot` 会产生 7×3 的笛卡尔积……
//
// 而这段 SQL 位于**双引号字符串**里 ⇒ bash 把反引号内容当**命令替换**执行了。
// 反引号内容含换行 ⇒ 被拆成两条命令 ⇒ 每小时往 stderr 打两行假错：
//
//	行 478: base_buckets: 未找到命令
//	行 479: --: 未找到命令
//
// ★ 危害不在那两行假错本身，而在于：
// ① 它们**只在 stderr 恢复后才看得见** —— 此前被单实例闸门的
//    `exec 9>file 2>/dev/null` 吞掉整整 16 天（反引号是 2026-10-04 引入的）；
// ② 「靠读日志有没有错判断巡检健康」的人，会把真错当噪声；
// ③ 反引号是**命令替换**，内容一旦像命令，就会真的被执行 ——
//    这次是「找不到命令」的良性形态，形态一变就是任意命令执行。
//
// ⇒ 门的作用：**把这一类从「靠人眼发现」变成「提交即红」**。
//
// # 为什么判据必须带引号状态机（而不是简单 grep 反引号）
//
// 仓里这些脚本的**注释**里大量使用反引号做行内代码标注（如
// `` `docker exec ... psql` 僵死 ``）—— 那是**注释**，不是代码。
// 简单 grep 会把它们全报出来 ⇒ 恒红 ⇒ 门被稀释 ⇒ 没人看。
// ⇒ 所以判据必须区分：
//
//	· `#` 开头的**行注释**（含其后全部内容）→ 跳过
//	· 单引号区 → 跳过
//	· 双引号区 → **反引号即违规**（命令替换，且被引号包着，最容易误写）
//	· heredoc 正文（`<<'DELIM'` / `<<DELIM`）→ 跳过（不是 shell 代码）
//
// ★ 顺带钉住的一条**次生**事实：本仓这些脚本里**代码区一个反引号都没有**
//   （全部在注释里）⇒ 所以「双引号内不得有反引号」这条判据目前是
//   **真判据**而不是恒真门；下面有正向自证。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	shellMonitorDir = "../252-monitor"
)

// shellQuoteState 是扫描器的状态。
type shellQuoteState int

const (
	quoteNone shellQuoteState = iota
	quoteSingle
	quoteDouble
)

// backtickViolation 是扫描器抓到的一处违规。
type backtickViolation struct {
	Line int
	Col  int
	Why  string
}

// codeSegment 是一行里的「**代码位**」文本：引号外、注释外的部分。
//
// ★ 为什么需要「代码位」这个概念（§143.14 实测踩到）：
//
//	判据若在**原始文本**里搜模式串，就会把「注释里引用了那个模式」也算成违规。
//	本轮就撞上了：我在 SQL 注释里写下「exec 9>file 2>/dev/null」这段**说明文字**，
//	它位于**双引号字符串**内部 ⇒ 在 shell 语义里是**惰性文本**（不会被执行），
//	但「原始文本 grep」的门照样报红 ⇒ **判据自身的假阳性**。
//
//	⇒ 正确口径：**只有引号外的文本才可能被 shell 执行**。
//	所以「fd9 闸门吞 stderr」这条判据必须只扫**代码位**；
//	而「双引号内的反引号」那条判据恰好相反 —— 它要报的正是**引号内**的反引号
//	（因为双引号里也会做命令替换），但仍要排除**注释**。
//
//	⇒ 一次扫描同时给出这两个视图，避免两套解析器给出互相矛盾的结论。
type codeSegment struct {
	Line int
	Text string
}

// shellScan 是扫描器的完整产出。
type shellScan struct {
	Violations []backtickViolation
	Code       []codeSegment
}

// scanShell 对一段 shell 文本做**引号感知**扫描，
// 同时给出「反引号违规」与「每行的代码位文本」。
func scanShell(content string) shellScan {
	var out shellScan
	var violations []backtickViolation
	state := quoteNone
	heredocDelim := ""
	lines := strings.Split(content, "\n")

	for ln, line := range lines {
		// heredoc 正文：不是 shell 代码，整体跳过，直到遇到定界符行。
		if heredocDelim != "" {
			if strings.TrimSpace(line) == heredocDelim {
				heredocDelim = ""
			}
			continue
		}

		var code strings.Builder
		for i := 0; i < len(line); {
			c := line[i]
			switch state {
			case quoteNone:
				switch {
				case c == '\\':
					code.WriteByte(c)
					if i+1 < len(line) {
						code.WriteByte(line[i+1])
					}
					i += 2
					continue
				case c == '\'':
					state = quoteSingle
					i++
					continue
				case c == '"':
					state = quoteDouble
					i++
					continue
				case c == '#':
					// 行注释：后面整行都不是代码。
					i = len(line)
					continue
				case c == '`':
					violations = append(violations, backtickViolation{Line: ln + 1, Col: i + 1,
						Why: "反引号出现在引号之外（bash 会当命令替换执行）"})
					code.WriteByte(c)
				case c == '<' && strings.HasPrefix(line[i:], "<<<"):
					// here-string：没有正文
					i += 3
					continue
				case c == '<' && strings.HasPrefix(line[i:], "<<"):
					// heredoc：记录定界符，正文从下一行开始
					rest := line[i+2:]
					rest = strings.TrimPrefix(rest, "-")
					rest = strings.TrimSpace(rest)
					if rest != "" {
						if q := rest[0]; (q == '\'' || q == '"') && len(rest) > 1 {
							if e := strings.Index(rest[1:], string(q)); e >= 0 {
								heredocDelim = rest[1 : 1+e]
							}
						} else {
							heredocDelim = strings.Fields(rest)[0]
						}
					}
					i = len(line)
					continue
				}
				code.WriteByte(c)
			case quoteSingle:
				if c == '\'' {
					state = quoteNone
				}
			case quoteDouble:
				switch {
				case c == '\\':
					i += 2
					continue
				case c == '"':
					state = quoteNone
				case c == '`':
					violations = append(violations, backtickViolation{Line: ln + 1, Col: i + 1,
						Why: "反引号出现在双引号内（bash 会当命令替换执行）"})
				}
			}
			i++
		}
		out.Code = append(out.Code, codeSegment{Line: ln + 1, Text: code.String()})
	}
	out.Violations = violations
	return out
}

// scanShellForBackticksInDoubleQuotes 只取违规部分（薄包装）。
func scanShellForBackticksInDoubleQuotes(content string) []backtickViolation {
	return scanShell(content).Violations
}

// TestNoBacktickInsideDoubleQuotedStrings —— 巡检脚本的**代码区**不得有反引号。
//
// ★ 正向自证：这道门不是恒真门 ——
//
//	① 仓内确实存在**大量**反引号（注释里的行内代码标注）⇒ 简单 grep 会红，
//	   所以判据必须引号感知；
//	② 但那些反引号**全部在注释里** ⇒ 下面另有一条自证断言钉住这一点
//	   （「注释里的反引号数 > 0」且「代码区反引号数 == 0」），
//	   否则「代码区反引号恒为 0」可能只是「扫描器把代码也当注释跳过了」。
func TestNoBacktickInsideDoubleQuotedStrings(t *testing.T) {
	entries, err := os.ReadDir(shellMonitorDir)
	if err != nil {
		t.Fatalf("读 %s: %v", shellMonitorDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		files = append(files, e.Name())
	}
	if len(files) < 8 {
		t.Fatalf("只找到 %d 个 .sh（%v）—— 量具失效，这道门正在空转", len(files), files)
	}

	totalBackticksInComments := 0
	checked := 0
	for _, name := range files {
		path := filepath.Join(shellMonitorDir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", path, err)
		}
		content := string(b)

		// ① 主判据：引号感知的扫描
		vs := scanShellForBackticksInDoubleQuotes(content)
		for _, v := range vs {
			t.Errorf("%s:%d:%d %s\n"+
				"★ 双引号里的反引号会被 bash 当**命令替换**执行（§143.14 实测：\n"+
				"  ursm-snapshot-health.sh 的 SQL 注释里那对反引号，每小时往 stderr 打两行\n"+
				"  「base_buckets: 未找到命令」「--: 未找到命令」，假错淹没真错；\n"+
				"  且它们只因 stderr 被单实例闸门吞掉才「安静」了 16 天）。\n"+
				"⇒ 请把注释里的反引号换成「」（本仓既有风格）或删掉。",
				name, v.Line, v.Col, v.Why)
		}

		// ② 正向自证：注释里**确实有**反引号（否则「代码区 0 个」可能是扫描器太宽松）
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "`") {
				totalBackticksInComments++
			}
		}
		checked++
	}
	if checked < 8 {
		t.Fatalf("只检查了 %d 个文件 —— 覆盖面不对", checked)
	}
	if totalBackticksInComments == 0 {
		t.Errorf("注释里的反引号计数为 0 —— 说明这些脚本的注释风格变了，" +
			"或扫描器把注释也当代码跳过了（那样本门就退化成恒真门）。")
	}
	t.Logf("检查 %d 个脚本；注释里的反引号 %d 处（都被正确跳过）", checked, totalBackticksInComments)
}

// stmtTimeoutDefaultRe 抓 `STMT_TIMEOUT:=NNNs` 与 `STMT_TIMEOUT:-NNNs` 两种默认值写法。
//
// ★ 两种写法都要抓：`de()` 里的 `:-` 是**兜底**（`:=` 那行已经赋过值），
//
//	它其实是**死代码** —— 但正因为是死代码，它会骗过「改了一处就生效」的直觉。
//	门要盯住两处一致（下面那条断言就是为此）。
var stmtTimeoutDefaultRe = regexp.MustCompile(`STMT_TIMEOUT(?::=|:-)(\d+)s`)

// TestSnapshotHealthStatementTimeoutHasHeadroom —— 快照健康巡检的查询超时必须留足余量。
//
// ★ 为什么这道门有牙（§143.14 实测）：
//
//	252 上这道巡检的日志里，「ABORT: main query failed: ERROR: canceling statement
//	due to statement timeout」与「OK:」的每日条数：
//	    10-04~10-08  OK 216 / ABORT 4     ← 几乎全绿
//	    10-10        OK   0 / ABORT 53    ← 全灭
//	    10-18~10-19  OK  19 / ABORT 29
//	⇒ 超时上限 180s 卡在实测成本（空闲 105s / 有负载 180s+）的边缘 ⇒
//	  **相当一部分小时数「量具不可用、本次不出结论」**，
//	  而 §5.6「落库次数塌陷」指纹的探测通道在那些小时里是瞎的。
//	⇒ 实测 105s ⇒ 600s 有 5.7 倍余量。
//
// ★ 判据钉的是**下限**（≥600s）：上限无意义（PG 自己会超时），
//
//	而下限一旦被调回 180s，就会原样退回「一半小时数没有结论」的状态。
func TestSnapshotHealthStatementTimeoutHasHeadroom(t *testing.T) {
	path := filepath.Join(shellMonitorDir, "ursm-snapshot-health.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	content := string(b)

	ms := stmtTimeoutDefaultRe.FindAllStringSubmatch(content, -1)
	if len(ms) < 2 {
		t.Fatalf("在 %s 里只找到 %d 处 STMT_TIMEOUT 默认值（%v）—— "+
			"判据的定位失效（应当至少两处：`:=\"${STMT_TIMEOUT:=XXXs}\"` 与 de() 里的 `:-` 兜底）",
			path, len(ms), ms)
	}
	for _, m := range ms {
		secs, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("解析超时秒数 %q 失败: %v", m[1], err)
		}
		if secs < 600 {
			t.Errorf("%s: STMT_TIMEOUT 默认值 %ds < 600s\n"+
				"★ 实测（252，2026-10-20 00:24 空闲时段，STMT_TIMEOUT=600s）：整轮 105s，exit 0。\n"+
				"  而 180s 的旧上限在有负载时会把主判据掐掉 ⇒ 巡检判「量具不可用、本次不出结论」\n"+
				"  ⇒ §5.6「落库次数塌陷」指纹的探测通道在那些小时里是瞎的。\n"+
				"⇒ 请把默认值提到 ≥600s，并同步 de() 里的 `:-` 兜底（两处必须一致）。",
				path, secs)
		}
	}
	// 两处默认值必须一致（不一致 = 有一处是死代码，后来的人会改错那处）
	first, _ := strconv.Atoi(ms[0][1])
	for _, m := range ms[1:] {
		v, _ := strconv.Atoi(m[1])
		if v != first {
			t.Errorf("%s: 两处 STMT_TIMEOUT 默认值不一致（%ds vs %ds）⇒ 有一处是死代码，改错那处等于没改",
				path, first, v)
		}
	}
	t.Logf("STMT_TIMEOUT 默认值 = %ds（%d 处，一致）", first, len(ms))
}

// TestSnapshotHealthHasNoCommandSubstitutionInSQL —— 快照健康巡检的 SQL 里不得有反引号。
//
// 这条比族门更**定点**：它只盯这一个脚本的 SQL 段，
// 便于「谁改了这个脚本」的人第一眼看到。
// ★ 但它**不能**替代族门（`TestNoBacktickInsideDoubleQuotedStrings`）——
//
//	族门管全族；这条管本脚本的**语义位置**（SQL 注释里的反引号是本轮事故的根）。
func TestSnapshotHealthHasNoCommandSubstitutionInSQL(t *testing.T) {
	path := filepath.Join(shellMonitorDir, "ursm-snapshot-health.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	content := string(b)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// SQL 注释行（`--` 开头）出现在**双引号字符串**里的那一段
		if !strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line, "`") {
			t.Errorf("%s:%d SQL 注释里出现反引号：%s\n"+
				"★ 本段 SQL 在双引号里 ⇒ 反引号内容会被 bash 当命令替换执行\n"+
				"  （§143.14 实测：每小时两行「base_buckets: 未找到命令」「--: 未找到命令」）。\n"+
				"⇒ 注释里请用「」代替反引号。",
				path, i+1, trimmed)
		}
	}
	// 正向自证：本脚本的注释里**确实有** `--` 形式的 SQL 注释
	// （否则上面那个循环可能一行都没进 ⇒ 判据空转）
	sqlCommentLines := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			sqlCommentLines++
		}
	}
	if sqlCommentLines == 0 {
		t.Errorf("%s 里没有 `--` 开头的 SQL 注释行 —— 判据的定位失效", path)
	}
	t.Logf("SQL 注释行 %d 处，全部无反引号", sqlCommentLines)
}

// TestBacktickScannerDetectsInjectedBacktick —— **量具自证**。
//
// ★ 这条是给「判据本身」做的门：故意造一段**违规**文本，
//
//	断言扫描器**能**报出来 —— 否则上面几条门可能因为扫描器坏了而恒绿。
//
// ★ 这是本轮反复用到的纪律：「0 命中的第一嫌疑是量具坏了」；
//
//	对「恒真门」来说，反向就是「恒假门」—— 一样要证伪。
func TestBacktickScannerDetectsInjectedBacktick(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		// ★ 一对反引号 = **2 处**（开与闭各一处）—— 扫描器按「反引号个数」计数，
		//   不是按「命令替换几处」计数。★ 这个期望值我第一版写成了 1，
		//   被自己的门打回来 ⇒ 记一笔：**量具自证要连「计数的口径」一起证**。
		{"双引号内的反引号", "x=\"echo `date`\"\n", 2},
		{"单引号内的反引号（不算违规）", "x='echo `date`'\n", 0},
		{"行注释里的反引号（不算违规）", "# 参照 `date` 的用法\n", 0},
		{"heredoc 正文里的反引号（不算违规）", "cat <<'EOF'\n`date`\nEOF\n", 0},
		{"引号外的反引号（算违规）", "y=`date`\n", 2},
		{"双引号里被反斜杠转义的反引号（不算违规）", "z=\"a \\` b\"\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanShellForBackticksInDoubleQuotes(tc.content)
			if len(got) != tc.want {
				t.Errorf("扫描器对 %s 报 %d 处（期望 %d）：%v\n"+
					"★ 量具自证失败 ⇒ 上面几条门可能恒绿（扫描器坏了）",
					tc.name, len(got), tc.want, got)
			}
		})
	}
	// 再来一条：真实事故形态必须被报出来（引号感知 + 跨行反引号）
	accident := "rows=$(de \"SELECT 1,\n-- 写成 `base_buckets b`\n-- ON x.y GROUP BY z` 会错\")\n"
	vs := scanShellForBackticksInDoubleQuotes(accident)
	if len(vs) == 0 {
		t.Errorf("§143.14 事故形态（双引号里跨行的反引号）没有被报出来 ⇒ 扫描器失效")
	}
	for _, v := range vs {
		if !strings.Contains(v.Why, "双引号") {
			t.Errorf("事故形态的归因不对：%s（应为「双引号内」）", v.Why)
		}
	}
	fmt.Fprintln(os.Stderr, "量具自证：扫描器对事故形态报出", len(vs), "处")
}
