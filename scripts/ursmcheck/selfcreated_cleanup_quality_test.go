package ursm_test

// R95 轮 · 段 D 的门：**「自建资源回收」的兜底【质量】守卫**。
//
// 被测对象：scripts/ursmcheck/selfcreated_cleanup_quality_ledger.tsv
//
// ── 为什么需要它 ────────────────────────────────────────────────────────────
//
// 已有纪律：「自己建的资源必须用 trap 兜住所有退出路径」（R94 轮续九已落地）。
// ★★ 但本轮实测证明：**「有 trap」≠「兜底合格」**。兜底有 4 个独立可失效的轴：
//
//	A exit_trap       有没有 trap … EXIT
//	                 ⇒ 缺了它：提前退出（return/exit N）跳过回收（实测攒下 7 个残留库）
//	B signal_trap     有没有 trap … INT/TERM
//	                 ⇒ ★ **实测：bash 的 EXIT trap 在 SIGTERM/SIGINT 下【照样会跑】**
//	                   ⇒ 所以 B 的价值**不在「让 cleanup 跑」**，而在「它是执行收割的分支」
//	                 ⇒ ⇒ **⇒ B 只在【脚本确实会起后台子进程】时才是必需的**（见下）
//	C cleanup_guard   清理语句自带【防卡保护】（两种机制任一）
//	                 ⇒ C1 statement_timeout 类（中止语句）
//	                 ⇒ C2 pg_terminate_backend 类（先把阻塞会话踢掉再 DROP）
//	                 ⇒ ★ 缺了它：清理语句卡死 ⇒ 「一次慢 DROP」被放大成「每次退出都卡」
//	                   （实测：`DROP DATABASE … WITH (FORCE)` 卡 ≈13 分钟后被 `Killed: 9`；
//	                    而本机 PG 的 statement_timeout=0 / lock_timeout=0 ⇒ 无服务端兜底）
//	D child_reap      有后台子进程时，清理前有没有 kill + wait 收割
//	                 ⇒ ★ 缺了它：子进程被孤儿化 ⇒ 它在父进程 cleanup【之后】才把资源建出来 ⇒ 依然泄漏
//
// ★★ 本轮的三变体最小复现（实测）是把 B 与 D 的关系钉死的关键：
//
//	变体 A：只有 trap cleanup EXIT                               ⇒ ★ 泄漏
//	变体 B：EXIT + trap cleanup INT TERM（TERM 分支只是再 cleanup 一次，**不收割**） ⇒ ★ 泄漏
//	变体 C：EXIT + trap reap_and_cleanup INT TERM（**先 kill+wait 收割**再 cleanup）   ⇒ ✔ 无泄漏
//
//	⇒⇒⇒ **⇒ 「接管信号」本身【不解决泄漏】；真正解决它的是「收割子进程」。**
//	⇒⇒⇒ ⇒ B 只是 D 的**载体**（需要一个信号分支来执行收割）。
//	⇒⇒⇒ ⇒ ★★ 所以判据不能停在「有没有 trap INT/TERM」—— 那测不出 D 缺没缺。
//
// ── 「合格兜底」的定义（★ 按实测定，不按直觉定）──────────────────────────
//
//	合格 = A=yes
//	    ∧ 防卡保护（C1 ∨ C2）
//	    ∧ (没有后台子进程 ∨ (B=yes ∧ D=yes))
//
// ★★ 为什么 B（信号接管）**不是**无条件的必需项：
//   实测 bash 的 EXIT trap 在 SIGTERM/SIGINT 下【照样会跑】⇒ ⇒ 对**没有后台子进程**的脚本，
//   不接信号没有任何风险 ⇒ ⇒ ⇒ 若把 B 列为无条件必需项，那这条断言在防一个【不存在的风险】
//   ⇒ ⇒ ⇒ ⇒ 而且它会让 9 个脚本「因为一个无关的缺口」被判不合格 ⇒ ⇒ ⇒ ⇒ 假红。
//   ⇒ ⇒ ⇒ ⇒ **B 只在「脚本确实会起后台子进程」时才是必需的** —— 因为那时需要一条信号分支
//   来执行收割。
//
// ── 门的形态：台账 + 棘轮 ───────────────────────────────────────────────────
//
// ★ 本门**不**与并行会话 R94 续九 ⑤ 第 4 项（把 trap 纪律推广到其他脚本）重复：
//	他们做的是**修复轴**（加 trap）；本门做的是**验收轴**（什么算「兜底合格」）。
//	⇒ ⇒ 二者互补：他们的推广若只加 trap 而不加防卡保护 ⇒ 本门会红 ⇒ ⇒ 这就是「什么时候算修完」。
//
// ★ 本门**不阻塞**存量：今天已知的缺陷在台账里显式登记为 verdict=待修 + defects=<缺失项>
//	⇒ ⇒ 门是绿的。⇒ ⇒ 存量缺陷只减不增：源码里补上保护 ⇒ 重算的缺陷集少了 ⇒
//	⇒ ⇒ 台账没同步 ⇒ **门红** ⇒ ⇒ ⇒ 强制登记 ⇒ ⇒ ⇒ 缺陷集单调下降。
//
// 判据：
//	① 账实相符：台账的 6 个轴值必须与【从源码重算】的逐字一致
//	② 双向候选集：候选集（源码里自建资源 + 自回收的脚本）必须与台账行集完全一致
//	③ verdict=合格 ⇒ defects 必须为空（即重算出来确实合格）
//	④ verdict=待修 ⇒ defects 必须【逐项等于】重算出来的缺失项集合（⇒ 缺陷集单调下降）
//	⑤ verdict=无回收义务 ⇒ evidence 必须写明「建的是长驻资源」
//	⑥ 结构不变量：取值域合法 / evidence 非空 / 台账非空 / 每个文件只出现一次
//
// ★★ 量具自检（防恒绿门）：本门必须在**探针**上验证「合格 / 待修 / 超时标记误命中」
//	三种形态给出**不同读数** ⇒ ⇒ 否则整门可能恒绿。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const scqLedger = "selfcreated_cleanup_quality_ledger.tsv"

// ── 判据用的正则 ────────────────────────────────────────────────────────────

var (
	scqReCreateDB = regexp.MustCompile(`(?i)\bCREATE\s+DATABASE\b`)
	scqReCreateCT = regexp.MustCompile(`(?i)\bdocker\s+run\b|\bdocker\s+compose\s+up\b|\bdocker-compose\s+up\b`)
	scqReDropDB   = regexp.MustCompile(`(?i)\bDROP\s+DATABASE\b`)

	scqReTrapLine = regexp.MustCompile(`(?m)^\s*trap\s+.*$`)
	scqReSignal   = regexp.MustCompile(`\b(EXIT|INT|TERM|QUIT|HUP)\b`)

	// ★ 清理语句的「自带超时」标记（C1）。
	//
	// ★★ 判据必须收窄到【有确切语义的那一个】，而不是加更多上下文启发：
	//   第一版用 `timeout\s*=\s*\d` / `timeout\s+\d+[smh]?` ⇒ 手工核对时抓到两处假阳性：
	//     ① `db-init-lib.sh` 第 592/1188/1215 行的 `timeout=20/15/10` 是【内嵌 Python 的
	//        subprocess.run(timeout=)】，跟第 780/796 行的 DROP 毫无关系；
	//     ② `run-integration-gate.sh` 第 850 行的 `-timeout 20m` 是【go test】，
	//        而 DROP 在第 239/248 行。
	//   ⇒⇒ ⇒ 所以只认【数据库语义】的超时标记。
	//   ⇒⇒ ⇒ ★ 这也正是 R95 段 C 实测出来的那条修法（`SET statement_timeout='15s'`）⇒ 判据与修法对齐。
	scqReTimeout = regexp.MustCompile(
		`(?i)statement_timeout|lock_timeout|idle_in_transaction_session_timeout`)
	// ★ 防卡的【另一种】机制（C2）：先把阻塞会话踢掉再 DROP。
	//   ★ 仓内已有两个脚本这么写（scripts/local-r112-migrate.sh、
	//   scripts/sync-gateway-db-to-211.sh）⇒ ⇒ 这不是外来建议，是本仓既有实践。
	//   ★ 与 C1 机制不同（一个是中止语句，一个是清掉阻塞者）⇒ ⇒ 分成两轴记，信息量更大。
	scqReDisconnect = regexp.MustCompile(`(?i)pg_terminate_backend|pg_cancel_backend`)

	// ★ 后台子进程：行尾的 `&`（排除 `&&` / `2>&1` / `$(&` / `&>`）、以及 `docker run -d`。
	// ★★ 第一版用一条正则 `(?:\S)\s+&\s*(?:#.*)?$` ⇒ 只认「行尾的 &」⇒
	//   **认不出 `build & PID=$!` 这种「& 后面跟赋值」的形态** ⇒ ⇒ 而那正是本仓
	//   唯一的后台子进程写法 ⇒ ⇒ 第一版把所有脚本都判成「无后台子进程」⇒
	//   ⇒ ★ 而且这个缺陷是【门自己的量具自检】抓出来的（探针 2 与探针 3 的缺失项
	//   集合变得相同 ⇒ 判据无区分度）⇒ ⇒ ⇒ 这就是量具自检的价值。
	scqReDockerRunD = regexp.MustCompile(`(?i)docker\s+run\b[^\n]*\s-d\b`)
	// ★ 执行上下文：判据复用了并行会话 R94 续九那套量具的形态
	//   （scripts/audit/measure-selfcreated-cleanup.sh 的 C1/C3 判据）：
	//   「剥注释、排除正则字面量后仍能匹配到 CREATE DATABASE / createdb」
	//   且「匹配行 **或它 ±3 行窗口内** 含执行上下文」。
	// ★★ 但他们的版本对「第 88 行是注释、注释里含 psql」这一类会漏 ⇒ ⇒ 加上「剥注释」这步就闭合。
	scqReExecCtx  = regexp.MustCompile(`(?i)psql|createdb|\bexec\b|run_psql|pg_exec|docker\s+exec`)
	scqReKillWait = regexp.MustCompile(`(?i)\bkill\b|\bwait\b`)
	scqReFuncHead = regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*\)\s*\{`)
	scqReFuncTail = regexp.MustCompile(`^\}\s*$`)
)

// scqShellFuncs 抽出 shell 的 `name() {` … `^}` 函数体。
func scqShellFuncs(src string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(src, "\n")
	var cur string
	var buf []string
	flush := func() {
		if cur != "" {
			out[cur] = strings.Join(buf, "\n")
		}
		cur, buf = "", nil
	}
	for _, ln := range lines {
		if m := scqReFuncHead.FindStringSubmatch(ln); m != nil {
			flush()
			cur, buf = m[1], []string{ln}
			continue
		}
		if cur != "" {
			buf = append(buf, ln)
			if scqReFuncTail.MatchString(ln) {
				flush()
			}
		}
	}
	flush()
	return out
}

// scqLogicalCommands 把源码切成「逻辑命令」：把反斜杠续行与引号未闭合的行并成一条。
func scqLogicalCommands(src string) []string {
	lines := strings.Split(src, "\n")
	var cmds []string
	var acc string
	open := false
	for _, ln := range lines {
		t := ln
		cont := false
		if strings.HasSuffix(t, "\\") {
			t = strings.TrimSuffix(t, "\\")
			cont = true
		}
		acc += t
		q := 0
		for _, c := range acc {
			if c == '\'' {
				q++
			}
		}
		if q%2 == 1 {
			open = true
		}
		if cont || open {
			acc += "\n"
			continue
		}
		cmds = append(cmds, acc)
		acc, open = "", false
	}
	if acc != "" {
		cmds = append(cmds, acc)
	}
	return cmds
}

// scqStripComments 把「整行都是注释」的行清空（保留行数，便于 ±N 窗口定位）。
func scqStripComments(src string) []string {
	out := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			out = append(out, "")
			continue
		}
		out = append(out, ln)
	}
	return out
}

// scqHasExecContext 判断 src 里是否存在「真的在执行 CREATE/DROP DATABASE」。
//
// ★ 判据形态复用了并行会话 R94 续九那套量具的 C1/C3 判据：
//
//	剥注释 + 「匹配行或它 ±3 行窗口内含执行上下文」。
//
// ★★ 为什么必须剥注释（★ 本轮实测抓到的假阳性）：
//
//	scripts/audit/measure-selfcreated-cleanup.sh 是个【量具脚本】⇒ 它的源码里
//	「CREATE DATABASE」「DROP DATABASE」「psql」全是【分析用的正则与标签】
//	⇒ 不剥注释 ⇒ 它会被判成「自建资源 + 自回收」⇒ ⇒ 而它根本不建库
//	⇒ ⇒ ★ 这正是并行会话台账里点名的第 ③ 类假阳性「量具脚本自匹配」。
func scqHasExecContext(lines []string, re *regexp.Regexp) bool {
	for i, ln := range lines {
		if !re.MatchString(ln) {
			continue
		}
		lo, hi := i-3, i+3
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			if j == i && !scqReExecCtx.MatchString(ln) {
				continue
			}
			if scqReExecCtx.MatchString(lines[j]) {
				return true
			}
		}
	}
	return false
}

// scqHasBackground 判断脚本会不会起【后台子进程】。
//
// ★ 逐行扫并排除这些不是后台启动的 `&`：`&&`、`2>&1`/`>&2`、`&>`、`|&`。
func scqHasBackground(src string) bool {
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if scqReDockerRunD.MatchString(ln) {
			return true
		}
		// ★★ 只看【命令层】的 `&` ⇒ 引号内的不算。
		//   ★★ 手工核对抓到一处假阳性：scripts/omnifree/e2e-verify.sh 第 86 行
		//      `pass "psql & go 已安装"` ⇒ 引号里的 `&` 后面还跟空格
		//      ⇒ 「& 后面是空白就当后台启动」这条规则会误判。
		inS, inD := false, false
		for i := 0; i < len(ln); i++ {
			c := ln[i]
			switch {
			case c == '\\' && i+1 < len(ln):
				i++ // 跳过转义字符
				continue
			case c == '\'' && !inD:
				inS = !inS
				continue
			case c == '"' && !inS:
				inD = !inD
				continue
			}
			if inS || inD || c != '&' {
				continue
			}
			if i+1 < len(ln) && ln[i+1] == '&' {
				i++
				continue
			}
			if i > 0 && (ln[i-1] == '>' || ln[i-1] == '|') {
				continue
			}
			if i+1 < len(ln) && ln[i+1] == '>' {
				continue
			}
			if i+1 == len(ln) || ln[i+1] == ' ' || ln[i+1] == '\t' {
				return true
			}
		}
	}
	return false
}

// scqTrapSignals 抽出一份文件里 trap 注册过的信号集合。
func scqTrapSignals(src string) map[string]bool {
	sigs := map[string]bool{}
	for _, m := range scqReTrapLine.FindAllString(src, -1) {
		for _, tok := range scqReSignal.FindAllString(m, -1) {
			sigs[strings.ToUpper(tok)] = true
		}
	}
	return sigs
}

// scqAxes 是一个脚本的兜底质量轴。
type scqAxes struct {
	ExitTrap          bool // A
	SignalTrap        bool // B
	CleanupTimeout    bool // C1
	CleanupDisconnect bool // C2
	ChildReap         bool // D
	ChildApplies      bool // D 是否适用（文件确实会起后台子进程）
}

// scqGuarded 清理语句是否带【防卡保护】（两种机制任一）。
func (a scqAxes) scqGuarded() bool { return a.CleanupTimeout || a.CleanupDisconnect }

// scqDeficits 算出「为了合格还缺什么」。
//
// ★★ 缺陷名只取【真正有风险的缺口】：
//   - exit_trap     缺 EXIT trap ⇒ 提前退出跳过回收
//   - cleanup_guard 清理语句无防卡保护 ⇒ 慢 DROP 会在退出时卡死
//   - signal_trap   【仅当有后台子进程】没接管信号 ⇒ 信号路径上无处执行收割
//   - child_reap    【仅当有后台子进程】没 kill+wait ⇒ 孤儿子进程在 cleanup 之后建出资源
func scqDeficits(a scqAxes) []string {
	var miss []string
	if !a.ExitTrap {
		miss = append(miss, "exit_trap")
	}
	if !a.scqGuarded() {
		miss = append(miss, "cleanup_guard")
	}
	if a.ChildApplies {
		if !a.SignalTrap {
			miss = append(miss, "signal_trap")
		}
		if !a.ChildReap {
			miss = append(miss, "child_reap")
		}
	}
	sort.Strings(miss)
	return miss
}

// scqCompute 从源码重算 6 个轴。
func scqCompute(src string) scqAxes {
	ax := scqAxes{}
	sigs := scqTrapSignals(src)
	ax.ExitTrap = sigs["EXIT"]
	ax.SignalTrap = sigs["INT"] || sigs["TERM"] || sigs["QUIT"] || sigs["HUP"]

	funcs := scqShellFuncs(src)
	cmds := scqLogicalCommands(src)

	// C1 / C2：清理语句自带防卡保护。
	// ★★ 判据落在【含 DROP DATABASE 的那条逻辑命令】上（或紧邻它的前一条命令）
	//   ⇒⇒ 「全文件里出现过 timeout」不行（会把 go test / Python 的 timeout 吃进来）；
	//   ⇒⇒ 「函数体里出现过 timeout」也不行（shell 函数边界在含 heredoc 的文件里不可靠）。
	for i, c := range cmds {
		if !scqReDropDB.MatchString(c) {
			continue
		}
		hitTO, hitDC := false, false
		if scqReTimeout.MatchString(c) {
			hitTO = true
		}
		if scqReDisconnect.MatchString(c) {
			hitDC = true
		}
		if i > 0 {
			if scqReTimeout.MatchString(cmds[i-1]) {
				hitTO = true
			}
			if scqReDisconnect.MatchString(cmds[i-1]) {
				hitDC = true
			}
		}
		if hitTO {
			ax.CleanupTimeout = true
		}
		if hitDC {
			ax.CleanupDisconnect = true
		}
	}

	// D：会不会起后台子进程；起了的话，清理前有没有 kill + wait。
	// ★★ 这里【按函数体】找 kill/wait 是安全的：kill/wait 只可能出现在 cleanup 函数里，
	//   不会出现在内嵌 Python / Go 代码段里。
	ax.ChildApplies = scqHasBackground(src)
	if ax.ChildApplies {
		for _, b := range funcs {
			if scqReDropDB.MatchString(b) && scqReKillWait.MatchString(b) {
				ax.ChildReap = true
			}
		}
		if !ax.ChildReap {
			for _, c := range cmds {
				if scqReDropDB.MatchString(c) && scqReKillWait.MatchString(c) {
					ax.ChildReap = true
				}
			}
		}
		if !ax.ChildReap {
			for _, m := range scqReTrapLine.FindAllString(src, -1) {
				for _, tok := range regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*$`).FindAllStringSubmatch(m, -1) {
					if b, ok := funcs[tok[1]]; ok && scqReKillWait.MatchString(b) {
						ax.ChildReap = true
					}
				}
			}
		}
	}
	return ax
}

// ── 台账 ────────────────────────────────────────────────────────────────────

// scqRow 是台账的一行。
type scqRow struct {
	File              string
	ExitTrap          string // yes/no
	SignalTrap        string // yes/no
	CleanupTimeout    string // yes/no
	CleanupDisconnect string // yes/no
	ChildReap         string // yes/no/na
	Origin            string // static/manual
	Verdict           string // 合格/待修/无回收义务
	Deficits          string // 逗号分隔的缺失项名（待修行用）
	Evidence          string
}

var scqValidVerdict = map[string]bool{"合格": true, "待修": true, "无回收义务": true}

// scqReadLedger 读台账。
func scqReadLedger(t *testing.T) []scqRow {
	t.Helper()
	fh, err := os.Open(scqLedger)
	if err != nil {
		t.Fatalf("打开台账失败: %v", err)
	}
	defer fh.Close()
	var rows []scqRow
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	ln := 0
	for sc.Scan() {
		ln++
		text := sc.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		f := strings.Split(text, "\t")
		if len(f) != 10 {
			t.Fatalf("台账第 %d 行列数 = %d，期望 10\n%s", ln, len(f), text)
		}
		rows = append(rows, scqRow{
			File: f[0], ExitTrap: f[1], SignalTrap: f[2], CleanupTimeout: f[3],
			CleanupDisconnect: f[4], ChildReap: f[5], Origin: f[6], Verdict: f[7],
			Deficits: f[8], Evidence: f[9],
		})
	}
	return rows
}

// scqCandidateFiles 扫全仓，找出「自建资源 + 自己回收（DROP DATABASE）」的脚本。
func scqCandidateFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("取仓库根失败: %v", err)
	}
	var out []string
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(p)
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".sh") {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		data, rerr2 := os.ReadFile(p)
		if rerr2 != nil {
			return nil
		}
		src := string(data)
		lines := scqStripComments(src)
		// ★ 「自建」：CREATE DATABASE 真在执行（剥注释 + ±3 行执行上下文）
		//   ⇒ 或它会起 docker 容器（容器里的库是它自己建的）
		creates := scqHasExecContext(lines, scqReCreateDB) || scqReCreateCT.MatchString(src)
		if !creates {
			return nil
		}
		// ★ 「自回收」：DROP DATABASE 真在执行
		if !scqHasExecContext(lines, scqReDropDB) {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("扫仓失败: %v", err)
	}
	sort.Strings(out)
	return out
}

// ── 判据 ① 账实相符 + ② 双向候选集 + ③④⑤⑥ ───────────────────────────────

func TestSelfCreatedCleanupQualityLedger(t *testing.T) {
	rows := scqReadLedger(t)
	if len(rows) == 0 {
		t.Fatal("台账为空 ⇒ 分母为 0，拒绝给结论")
	}

	// ⑥ 结构不变量
	seen := map[string]bool{}
	for i, r := range rows {
		ln := i + 1
		if r.File == "" {
			t.Errorf("台账第 %d 行 file 为空", ln)
			continue
		}
		if seen[r.File] {
			t.Errorf("台账里 %s 出现多次", r.File)
		}
		seen[r.File] = true
		for _, v := range []string{r.ExitTrap, r.SignalTrap, r.CleanupTimeout, r.CleanupDisconnect} {
			if v != "yes" && v != "no" {
				t.Errorf("台账 %s：轴值 %q 非法（只允许 yes/no）", r.File, v)
			}
		}
		if r.ChildReap != "yes" && r.ChildReap != "no" && r.ChildReap != "na" {
			t.Errorf("台账 %s：child_reap = %q 非法（只允许 yes/no/na）", r.File, r.ChildReap)
		}
		if r.Origin != "static" && r.Origin != "manual" {
			t.Errorf("台账 %s：origin = %q 非法（只允许 static/manual）", r.File, r.Origin)
		}
		if !scqValidVerdict[r.Verdict] {
			t.Errorf("台账 %s：verdict = %q 非法（只允许 合格/待修/无回收义务）", r.File, r.Verdict)
		}
		if strings.TrimSpace(r.Evidence) == "" {
			t.Errorf("台账 %s：evidence 为空（每行必须写清依据）", r.File)
		}
	}

	// ② 双向候选集
	cands := scqCandidateFiles(t)
	candSet := map[string]bool{}
	for _, c := range cands {
		candSet[c] = true
	}
	for _, c := range cands {
		if !seen[c] {
			t.Errorf("★ 新候选未登记：%s\n"+
				"  ⇒ 它自建资源且自回收 ⇒ 必须登记进台账；\n"+
				"  ⇒ 「合格」的定义 = 有 EXIT trap ∧ 清理语句自带防卡保护 ∧\n"+
				"  ⇒   (无后台子进程 ∨ (接管 INT/TERM ∧ 先 kill+wait 收割))", c)
		}
	}
	// ★★ origin 自洽：
	//   origin=static ⇒ 必须能被静态候选集认出（否则说明它已经不该是 static ⇒ 改 origin 或删行）
	//   origin=manual ⇒ 必须【不能】被静态候选集认出（能认出的就该是 static）
	//   ⇒⇒★ manual 这一档的存在理由：**静态候选集判据会漏掉「经由调用别的脚本自建资源」的对象**
	//   ⇒⇒★ 而那个对象恰恰是本仓唯一有「后台子进程 + kill+wait 收割」形态的脚本
	//   ⇒⇒★ ⇒ 若只靠静态候选集，D 轴（收割）就会永远无对象 ⇒ 门会漏掉真正需要收割的那个脚本。
	for _, r := range rows {
		inCand := candSet[r.File]
		switch r.Origin {
		case "static":
			if !inCand {
				t.Errorf("★ 台账里的 %s 标 origin=static，但静态候选集认不出它\n"+
					"  ⇒ 若它已不是「自建资源 + 自回收」⇒ 请删行；\n"+
					"  ⇒ 若它仍是这样但静态判据认不出 ⇒ 请改 origin=manual 并在 evidence 里说明", r.File)
			}
		case "manual":
			if inCand {
				t.Errorf("台账里的 %s 标 origin=manual，但静态候选集能认出它 ⇒ 请改 origin=static", r.File)
			}
		}
	}

	// ① 账实相符 + ③④
	for _, r := range rows {
		data, err := os.ReadFile(filepath.Join("../..", r.File))
		if err != nil {
			t.Errorf("读 %s 失败: %v", r.File, err)
			continue
		}
		ax := scqCompute(string(data))
		yn := func(b bool) string {
			if b {
				return "yes"
			}
			return "no"
		}
		gotReap := "na"
		if ax.ChildApplies {
			gotReap = yn(ax.ChildReap)
		}
		if r.ExitTrap != yn(ax.ExitTrap) || r.SignalTrap != yn(ax.SignalTrap) ||
			r.CleanupTimeout != yn(ax.CleanupTimeout) || r.CleanupDisconnect != yn(ax.CleanupDisconnect) ||
			r.ChildReap != gotReap {
			t.Errorf("★ %s 账实不符\n  台账: exit=%s signal=%s timeout=%s disconnect=%s reap=%s\n"+
				"  重算: exit=%s signal=%s timeout=%s disconnect=%s reap=%s\n"+
				"  ⇒ 源码改了必须同步台账（这正是本门要抓的「改了没登记」）",
				r.File, r.ExitTrap, r.SignalTrap, r.CleanupTimeout, r.CleanupDisconnect, r.ChildReap,
				yn(ax.ExitTrap), yn(ax.SignalTrap), yn(ax.CleanupTimeout), yn(ax.CleanupDisconnect), gotReap)
		}

		miss := strings.Join(scqDeficits(ax), ",")
		switch r.Verdict {
		case "合格":
			if miss != "" {
				t.Errorf("★ %s 标了「合格」，但重算出来还缺 %s\n"+
					"  ⇒ 「合格」的定义见本文件头 ⇒ ⇒ 不能标合格", r.File, miss)
			}
			if r.Deficits != "" {
				t.Errorf("台账 %s：verdict=合格 但 deficits 非空（%q）", r.File, r.Deficits)
			}
		case "待修":
			if r.Deficits != miss {
				t.Errorf("★ %s 的 deficits = %q，重算缺失项 = %q\n"+
					"  ⇒ deficits 必须逐项等于重算结果 ⇒ ⇒ 补上后要同步台账 ⇒ ⇒ 缺陷集只减不增",
					r.File, r.Deficits, miss)
			}
		case "无回收义务":
			if r.Deficits != "" {
				t.Errorf("台账 %s：verdict=无回收义务 但 deficits 非空（%q）", r.File, r.Deficits)
			}
			// ★ 无回收义务有三种正当理由：长驻 / 静态假阳性 / 根本不建库
			//   （第三、第四种是并行会话 R94 续九台账里已登记的裁决类别）
			ok := false
			for _, marker := range []string{"长驻", "假阳性", "不建库"} {
				if strings.Contains(r.Evidence, marker) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("台账 %s：verdict=无回收义务 ⇒ evidence 必须写明「长驻」/「假阳性」/「不建库」", r.File)
			}
		}
	}
}

// ── 量具自检：探针必须给出不同读数（防整门恒绿）─────────────────────────────

// TestSelfCreatedCleanupQualityProbesDistinct 量具自检。
func TestSelfCreatedCleanupQualityProbesDistinct(t *testing.T) {
	// 探针 1：合格形态（EXIT trap + statement_timeout + 收割 + 接管信号）
	ok := `#!/usr/bin/env bash
cleanup() {
  if [ -n "$PID" ]; then kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; fi
  psql -c "SET statement_timeout='15s'; DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
trap cleanup INT TERM
build & PID=$!
`
	// 探针 2：待修形态（有 EXIT trap，但无防卡保护、无信号接管、无收割）
	bad := `#!/usr/bin/env bash
cleanup() {
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
build & PID=$!
`
	// 探针 3：超时标记出现在【别的命令】里（`go test -timeout 600s`）⇒ 不得被判成
	//	「清理语句自带防卡保护」⇒ 这是上一版判据太宽的坑。
	mis := `#!/usr/bin/env bash
cleanup() {
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
go test -timeout 600s ./...
`
	// 探针 4：合格形态的另一种（pg_terminate_backend 类）⇒ 判据要认仓内既有实践
	dis := `#!/usr/bin/env bash
cleanup() {
  psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='x';" || true
  psql -c "DROP DATABASE IF EXISTS x" || true
}
trap cleanup EXIT
`

	axOK := scqCompute(ok)
	if !axOK.ExitTrap || !axOK.SignalTrap || !axOK.CleanupTimeout || !axOK.ChildApplies || !axOK.ChildReap {
		t.Errorf("探针 1（合格形态）判据读数不对: %+v\n"+
			"  ⇒ 期望 exit=yes signal=yes timeout=yes reap=yes", axOK)
	}
	if d := scqDeficits(axOK); len(d) != 0 {
		t.Errorf("探针 1（合格形态）不应有缺失项，实际 = %v", d)
	}

	axBad := scqCompute(bad)
	if !axBad.ExitTrap {
		t.Errorf("探针 2 应认出 EXIT trap: %+v", axBad)
	}
	if axBad.CleanupTimeout || axBad.CleanupDisconnect {
		t.Errorf("★ 探针 2 不该判成「清理语句带防卡保护」: %+v", axBad)
	}
	if axBad.SignalTrap {
		t.Errorf("探针 2 应判成无信号接管: %+v", axBad)
	}
	if !axBad.ChildApplies || axBad.ChildReap {
		t.Errorf("探针 2 有后台子进程但无收割 ⇒ 期望 reap=no: %+v", axBad)
	}

	axMis := scqCompute(mis)
	if axMis.CleanupTimeout || axMis.CleanupDisconnect {
		t.Errorf("★ 探针 3：`go test -timeout 600s` 不在清理命令里 ⇒ 不得判成「清理语句带防卡保护」: %+v", axMis)
	}

	axDis := scqCompute(dis)
	if !axDis.CleanupDisconnect {
		t.Errorf("探针 4 应认出 pg_terminate_backend 类防卡保护: %+v", axDis)
	}
	if d := scqDeficits(axDis); len(d) != 0 {
		t.Errorf("探针 4（pg_terminate_backend 形态）不应有缺失项，实际 = %v", d)
	}

	// ★★ 区分度的正确要求（★ 第一版写成「四个探针两两不同」是错的：
	//   探针 1 与探针 4 都是**合格**形态 ⇒ 它们的缺失项集合【本来就该相同】⇒
	//   ⇒ 那样写等于要求「合格形态之间必须不同」⇒ ⇒ 门会红在一个假问题上）。
	// ⇒ 正确要求：① 两个合格形态都无缺失项；② 两个不合格形态都有缺失项；
	//   ③ 两个不合格形态之间【必须不同】（否则其中一条判据其实没生效）。
	dOK := strings.Join(scqDeficits(axOK), ",")
	dDis := strings.Join(scqDeficits(axDis), ",")
	dBad := strings.Join(scqDeficits(axBad), ",")
	dMis := strings.Join(scqDeficits(axMis), ",")
	if dOK != "" || dDis != "" {
		t.Errorf("合格形态的探针不该有缺失项: ok=%q dis=%q", dOK, dDis)
	}
	if dBad == "" || dMis == "" {
		t.Errorf("不合格形态的探针必须有缺失项: bad=%q mis=%q", dBad, dMis)
	}
	if dBad == dMis {
		t.Errorf("★ 两个不合格探针的缺失项集合相同（%q）⇒ 其中一条判据其实没生效（判据无区分度）", dBad)
	}
}

// ── 量具自检：判据层与枚举层分离（从源码抽取，而不是复制）────────────────────

// TestSelfCreatedCleanupQualityUsesRepoFacts 验证判据本身不是空壳。
func TestSelfCreatedCleanupQualityUsesRepoFacts(t *testing.T) {
	if !scqReCreateDB.MatchString("CREATE DATABASE foo") {
		t.Fatal("scqReCreateDB 认不出 CREATE DATABASE ⇒ 候选集恒空 ⇒ 整门恒绿")
	}
	if scqReCreateDB.MatchString("SELECT 1") {
		t.Fatal("scqReCreateDB 误判 SELECT ⇒ 判据过宽")
	}
	if !scqReDropDB.MatchString("DROP DATABASE IF EXISTS foo") {
		t.Fatal("scqReDropDB 认不出 DROP DATABASE")
	}
	if scqReDropDB.MatchString("DROP TABLE foo") {
		t.Fatal("scqReDropDB 误判 DROP TABLE")
	}
	// ★ 判据必须认仓内既有实践：两个脚本用的是 pg_terminate_backend 类防卡
	if !scqReDisconnect.MatchString("SELECT pg_terminate_backend(pid) FROM pg_stat_activity") {
		t.Fatal("scqReDisconnect 认不出仓内既有的 pg_terminate_backend 形态")
	}
	// ★ 而 Python subprocess 的 timeout / go test 的 timeout 都不算
	if scqReTimeout.MatchString("subprocess.run(cmd, timeout=20)") {
		t.Fatal("★ scqReTimeout 误认 Python subprocess 的 timeout ⇒ 判据过宽")
	}
	if scqReTimeout.MatchString("go test -timeout 600s ./...") {
		t.Fatal("★ scqReTimeout 误认 go test 的 timeout ⇒ 判据过宽")
	}
	// 台账里必须真的引用了仓内存在的文件
	rows := scqReadLedger(t)
	for _, r := range rows {
		if _, err := os.Stat(filepath.Join("../..", r.File)); err != nil {
			t.Errorf("台账里的 %s 在仓内不存在: %v", r.File, err)
		}
	}
	fmt.Fprintf(os.Stderr, "scq 台账 %d 行；候选集 %d 个\n", len(rows), len(scqCandidateFiles(t)))
}
