package ursm_test

// R99 轮 · 段九（§19.20.65）的**反回归门**：两条新性质必须有门盯着。
//
// ★★ 本门**不改动**主门（selfcreated_cleanup_quality_test.go）的判据语义 ——
//   主门判的是「兜底**合不合格**」（5 个轴），本门判的是主门**看不见的两个具体性质**：
//
// 【A】超时值的【量级】
//
//	主门只认「有没有 statement_timeout 类标记」，**不看取值**
//	⇒ ⇒ 「15s」这种「有标记、但量级不够」的值，主门照样判合格。
//	⇒ ⇒ 段九实测（PG 17.10，高负载）：terminate 之后 DROP 仍要等被 SIGTERM 的
//	  后端收尾 = 4.28s / 38.60s / 89.37s
//	  ⇒ ⇒ 15s 会把**合法**清理砍成「被 statement_timeout 取消」
//	  ⇒ ⇒ 而「清理失败」＝「库泄漏」⇒ ⇒ 与守卫的目的**正好相反**。
//	⇒ ⇒ 本门断言：清理语句上的 statement_timeout **≥ 120s**（仓内标准值）。
//
// 【B】失败【可见性】
//
//	★ 「命令退出 0」≠「它干了活」：实测 psql 的退出码**由读入方式决定** ——
//	  `-c` 形态失败 ⇒ exit 1；**stdin 脚本形态失败 ⇒ exit 0**（除非 ON_ERROR_STOP=1 ⇒ exit 3）。
//	⇒ ⇒ 走 stdin 脚本形态的 python 腿（★ 本仓 3 个都是），清理失败会**静默**
//	⇒ ⇒ 唯一可靠的判据是**查落点**：pg_database 里还有几行。
//	⇒ ⇒ 本门断言：python 腿的兜底载体（atexit 钩子 / finally 块）里
//	  **必须**带一条查落点的语句。
//
// ★★★ 复用主门的切分器与定位器（scqLogicalCommands / scqStripComments / scqPyBlockOf /
//   scqGoBraceBlock / scqReadLedger …）⇒ ⇒ 本门与主门看到的是**同一批位置**
//   ⇒ ⇒ 不会各说各话 ⇒ ⇒ 也不会因为「两套切分规则」而漂移。
//
// ★★ 量具自检（防恒绿门）：本门必须先证明自己**有牙** ——
//   合成源里塞一个 15s 的清理语句 ⇒ 必须被抓；塞一个 120s 的 ⇒ 必须不报；
//   合成一个「清理块里的 30s ctx」⇒ 必须被抓；合成一个「只有 DROP、没有查落点」的
//   python 载体 ⇒ 必须被抓。★ 四条全过才算本门成立。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	// scqvMinTimeoutSeconds 是清理语句上 statement_timeout 的**下界**（秒）。
	//
	// ★ 依据：段九实测「terminate 之后 DROP 仍要等被 SIGTERM 的后端收尾」的上界
	//   = 89.37s ⇒ 下界必须 > 89.37 ⇒ 取 120s（= 仓内统一采用的标准值）。
	// ★ 诚实边界：这是**负载相关**的观测值；空闲机器上的收尾延迟未测。
	scqvMinTimeoutSeconds = 120
)

var (
	// scqvReTimeoutValue 抓 statement_timeout 的**字面量**取值。
	//   ★ 只认字面量（'120s' / 120s / 2m / 1h）⇒ 形如 ${VAR}s 的动态值
	//   ⇒ **显式 SKIP 并报出来**（不猜、不当合格算）。
	scqvReTimeoutValue = regexp.MustCompile(`statement_timeout\s*=\s*'?(\d+)\s*(ms|s|m|h)'?`)
	// scqvReWithTimeoutCall 定位 context.WithTimeout( 的调用点（括号配对由
	// scqvWithTimeoutArgs 做 ⇒ 不能用正则：首参 context.Background() 本身带括号）。
	scqvReWithTimeoutCall = regexp.MustCompile(`context\.WithTimeout\(`)
	// scqvReConstSeconds 抓「常量 = N * time.Second」形态。
	//   ★ 必须容许 `const ` 前缀 —— 否则 `const cleanupTimeout = 120 * time.Second`
	//   这行根本匹配不上（★ 本门的第一版就栽在这里 ⇒ 被量具自检当场抓住）。
	scqvReConstSeconds = regexp.MustCompile(`(?m)^\s*(?:const\s+)?(\w*[Tt]imeout)\s*=\s*(\d+)\s*\*\s*time\.Second`)
	// scqvReGoDurSeconds 抓「字面量 N * time.Second」形态。
	scqvReGoDurSeconds = regexp.MustCompile(`^(\d+)\s*\*\s*time\.Second$`)
	// scqvReResidual / scqvReCount 合起来 = 「查落点」的形态。
	scqvReResidual = regexp.MustCompile(`pg_database`)
	scqvReCount    = regexp.MustCompile(`count\s*\(`)
	// ★ 判据 C：信号接管必须是「**真的注册了**」，不是「文件里提到过」。
	//   ★ 主门那条轴（scqRePySignal = signal\.signal\s*\(|SIGTERM|SIGINT）是
	//   **存在性**判据 ⇒ 文件里只要**出现** SIGTERM 这个词就算通过
	//   ⇒ ⇒ 「把注册调用删掉、只留常量与注释」它照样绿 ⇒ ⇒ 这里收紧。
	scqvRePySignalCall = regexp.MustCompile(`signal\.signal\s*\(`)
	scqvReGoSignalCall = regexp.MustCompile(`signal\.Notify\s*\(`)
)

// ── 小工具 ────────────────────────────────────────────────────────────────

// scqvSeconds 把一条命令里 statement_timeout 的字面量取值解析成秒。
// 第二个返回值 false = 「没找到字面量」⇒ 调用方要显式 SKIP 而不是当合格。
func scqvSeconds(cmd string) (int, bool) {
	m := scqvReTimeoutValue.FindStringSubmatch(cmd)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "ms":
		return n / 1000, true
	case "s":
		return n, true
	case "m":
		return n * 60, true
	case "h":
		return n * 3600, true
	}
	return 0, false
}

// scqvWithTimeoutArgs 收出源码里每个 context.WithTimeout(…) 的**第二个参数**。
//
// ★ 为什么不能用正则：首参往往是 context.Background() —— 它**自带括号**
//
//	⇒ 「[^,()]+」这类模式匹配不到 ⇒ ⇒ 只能用括号配对来切。
//
// ★ 返回的第二个参数是**原文**（可能带空格），由 scqvGoDurationSeconds 解析。
func scqvWithTimeoutArgs(src string) []string {
	var out []string
	for _, loc := range scqvReWithTimeoutCall.FindAllStringIndex(src, -1) {
		i := loc[1] // 指向 '(' 之后
		depth := 1
		comma := -1
		end := -1
		for i < len(src) {
			c := src[i]
			switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			case ',':
				if depth == 1 && comma < 0 {
					comma = i
				}
			}
			if end >= 0 {
				break
			}
			i++
		}
		if comma < 0 || end < 0 {
			continue
		}
		out = append(out, src[comma+1:end])
	}
	return out
}

// scqvGoDurationSeconds 解析 go 侧 context.WithTimeout 的时长参数。
// 支持两种形态：字面量「N * time.Second」与具名常量（由 consts 给出）。
func scqvGoDurationSeconds(arg string, consts map[string]int) (int, bool) {
	arg = strings.TrimSpace(arg)
	if m := scqvReGoDurSeconds.FindStringSubmatch(arg); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, false
		}
		return n, true
	}
	if n, ok := consts[arg]; ok {
		return n, true
	}
	return 0, false
}

// scqvGoConstSeconds 收出文件里所有「常量 = N * time.Second」。
func scqvGoConstSeconds(src string) map[string]int {
	out := map[string]int{}
	for _, m := range scqvReConstSeconds.FindAllStringSubmatch(src, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out[m[1]] = n
	}
	return out
}

// scqvCleanupCommands 找出「清理语句」及其**紧邻的前一条非空命令」。
//
// ★ 与主门 scqCompute 的定位规则**逐字一致**（含「注释/空行不打断相邻性」这条）
//
//	⇒ ⇒ 本门与主门看到的是同一批位置。
func scqvCleanupCommands(src, lang string) [][]string {
	cmds := scqLogicalCommands(strings.Join(scqStripComments(src, lang), "\n"))
	var out [][]string
	for i, c := range cmds {
		if !scqReDropDB.MatchString(c) {
			continue
		}
		group := []string{c}
		for j := i - 1; j >= 0; j-- {
			if strings.TrimSpace(cmds[j]) == "" {
				continue
			}
			group = append(group, cmds[j])
			break
		}
		out = append(out, group)
	}
	return out
}

// scqvPyCarrierBlocks 收出 python 的兜底载体块：
//
//	① atexit.register(fn) 里 fn 的函数体；② finally: 块。
//
// ★ 与主门 scqPyFallbackCoversDrop 的定位规则一致。
func scqvPyCarrierBlocks(src string) []string {
	lines := strings.Split(src, "\n")
	var out []string
	for _, m := range scqRePyAtexitReg.FindAllStringSubmatch(src, -1) {
		name := m[1]
		for i, ln := range lines {
			mm := scqRePyDef.FindStringSubmatch(ln)
			if mm == nil || mm[2] != name {
				continue
			}
			out = append(out, scqPyBlockOf(lines, i, len(mm[1])))
		}
	}
	for i, ln := range lines {
		mm := scqRePyFinally.FindStringSubmatch(ln)
		if mm == nil {
			continue
		}
		out = append(out, scqPyBlockOf(lines, i, len(mm[1])))
	}
	return out
}

// scqvPyHasResidual 判一个 python 兜底载体块里有没有「查落点」。
//
// ★ 载体里可能**不直接**写那条查询，而是调一个 helper（★ 本轮 3 个 python 腿里
//
//	有 2 个把查询放在 `_residual()` 里，载体只调它）⇒ ⇒ 这里做**一层调用解析**：
//	载体里调到的、且**本文件里定义**的函数，其函数体里若有 pg_database + count(*)
//	⇒ 就算有查落点。
//
// ★ 只解析一层（不多层递归）⇒ 形态够用，且不会无限递归。
func scqvPyHasResidual(src, block string) bool {
	if scqvReResidual.MatchString(block) && scqvReCount.MatchString(block) {
		return true
	}
	lines := strings.Split(src, "\n")
	// ★ scqIdentRe 没有捕获组 ⇒ 必须用 FindAllString（用 FindAllStringSubmatch 取 m[1] 会 panic）
	for _, name := range scqIdentRe.FindAllString(block, -1) {
		for i, ln := range lines {
			mm := scqRePyDef.FindStringSubmatch(ln)
			if mm == nil || mm[2] != name {
				continue
			}
			body := scqPyBlockOf(lines, i, len(mm[1]))
			if scqvReResidual.MatchString(body) && scqvReCount.MatchString(body) {
				return true
			}
		}
	}
	return false
}

// scqvGoDropBlocks 收出 go 侧「含 DROP DATABASE」的块。
//
// ★ 三种来源：t.Cleanup(func(){…})、defer func(){…}()、具名函数体。
// ★ 与主门 scqGoFallbackCoversDrop 的定位规则一致。
func scqvGoDropBlocks(src string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{scqReGoCleanupFn, scqReGoDeferFn} {
		for _, loc := range re.FindAllStringIndex(src, -1) {
			open := strings.Index(src[loc[0]:loc[1]], "{")
			if open < 0 {
				continue
			}
			blk := scqGoBraceBlock(src, loc[0]+open)
			if scqReDropDB.MatchString(blk) {
				out = append(out, blk)
			}
		}
	}
	for _, fm := range scqReGoFuncDef.FindAllStringSubmatchIndex(src, -1) {
		open := strings.Index(src[fm[1]:], "{")
		if open < 0 {
			continue
		}
		blk := scqGoBraceBlock(src, fm[1]+open)
		if scqReDropDB.MatchString(blk) {
			out = append(out, blk)
		}
	}
	return out
}

// ── 判据 A：清理语句上的超时值必须够大 ──────────────────────────────────────

func TestSelfCreatedCleanupTimeoutValueNotTooSmall(t *testing.T) {
	rows := scqReadLedger(t)
	checked, skipped := 0, 0
	for _, r := range rows {
		if r.CleanupTimeout != "yes" {
			continue
		}
		switch r.Language {
		case "shell", "python":
			data, err := os.ReadFile(filepath.Join("../..", r.File))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", r.File, err)
			}
			for _, group := range scqvCleanupCommands(string(data), r.Language) {
				hit := false
				for _, c := range group {
					secs, ok := scqvSeconds(c)
					if !ok {
						continue
					}
					hit = true
					checked++
					if secs < scqvMinTimeoutSeconds {
						t.Errorf("★ %s 的清理语句上的 statement_timeout = %ds，低于下界 %ds\n"+
							"  ⇒ 实测「terminate 之后 DROP 仍要等被 SIGTERM 的后端收尾」可达 89.37s\n"+
							"  ⇒ ⇒ 超时太短会把【合法】清理砍成失败 ⇒ 「清理失败」＝「库泄漏」",
							r.File, secs, scqvMinTimeoutSeconds)
					}
				}
				if !hit {
					skipped++
				}
			}
		case "go":
			data, err := os.ReadFile(filepath.Join("../..", r.File))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", r.File, err)
			}
			src := string(data)
			consts := scqvGoConstSeconds(src)
			fileHit := false
			for _, blk := range scqvGoDropBlocks(src) {
				for _, arg := range scqvWithTimeoutArgs(blk) {
					secs, ok := scqvGoDurationSeconds(arg, consts)
					if !ok {
						skipped++
						continue
					}
					fileHit = true
					checked++
					if secs < scqvMinTimeoutSeconds {
						t.Errorf("★ %s 的清理块里 context.WithTimeout 的时长 = %ds，低于下界 %ds\n"+
							"  ⇒ 实测收尾延迟可达 89.37s ⇒ 超时太短 ⇒ 合法清理被砍成失败 ⇒ 库泄漏",
							r.File, secs, scqvMinTimeoutSeconds)
					}
				}
			}
			// ★ 清理可能**委托**给别的函数（internal/testdb 的 t.Cleanup → dropDatabase）
			//   ⇒ ⇒ 块里没有 ctx 不算缺陷；但文件里必须存在一个够大的 ctx。
			if !fileHit {
				for _, arg := range scqvWithTimeoutArgs(src) {
					secs, ok := scqvGoDurationSeconds(arg, consts)
					if !ok {
						continue
					}
					if secs >= scqvMinTimeoutSeconds {
						fileHit = true
						checked++
					}
				}
			}
			if !fileHit {
				t.Errorf("★ %s 里找不到任何时长 ≥ %ds 的 context.WithTimeout ⇒ 清理语句无上界",
					r.File, scqvMinTimeoutSeconds)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("★ 本门一条都没量到（checked=0）⇒ 量具失效 ⇒ 拒绝给结论")
	}
	t.Logf("scqv 超时值：量到 %d 处、显式 SKIP %d 处（非字面量取值）", checked, skipped)
}

// ── 判据 B：python 腿的兜底必须【查落点】 ──────────────────────────────────

func TestSelfCreatedCleanupPythonVerifiesResidual(t *testing.T) {
	rows := scqReadLedger(t)
	checked := 0
	for _, r := range rows {
		if r.Language != "python" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("../..", r.File))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", r.File, err)
		}
		src := string(data)
		carriers := scqvPyCarrierBlocks(src)
		dropCarriers := 0
		okCarrier := 0
		for _, blk := range carriers {
			if !scqReDropDB.MatchString(blk) {
				continue
			}
			dropCarriers++
			// ★ 「查落点」= 既有 pg_database、又有 count(*)（载体里直接写，
			//   或调一个本文件里定义的 helper —— 见 scqvPyHasResidual）
			if scqvPyHasResidual(src, blk) {
				okCarrier++
			}
		}
		if dropCarriers == 0 {
			t.Errorf("★ %s 找不到含 DROP 的兜底载体（atexit 钩子 / finally 块）⇒ 本门量不到", r.File)
			continue
		}
		checked++
		if okCarrier == 0 {
			t.Errorf("★ %s 的兜底载体里**没有**查落点的语句\n"+
				"  ⇒ 实测 psql 在 stdin 脚本形态下【失败也 exit 0】⇒ 退出码不可信\n"+
				"  ⇒ ⇒ 唯一可靠的判据是「pg_database 里还有几行」⇒ 必须显式查一次",
				r.File)
		}
	}
	if checked == 0 {
		t.Fatalf("★ 本门一条都没量到（checked=0）⇒ 量具失效 ⇒ 拒绝给结论")
	}
	t.Logf("scqv 失败可见性：量到 %d 个 python 腿", checked)
}

// ── 判据 C：信号接管必须是「真的注册了」，不是「提到过」 ────────────────────

func TestSelfCreatedCleanupSignalActuallyRegistered(t *testing.T) {
	rows := scqReadLedger(t)
	checked := 0
	for _, r := range rows {
		if r.SignalTrap != "yes" {
			continue
		}
		var ok bool
		switch r.Language {
		case "python":
			data, err := os.ReadFile(filepath.Join("../..", r.File))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", r.File, err)
			}
			ok = scqvRePySignalCall.MatchString(string(data))
		case "go":
			data, err := os.ReadFile(filepath.Join("../..", r.File))
			if err != nil {
				t.Fatalf("读 %s 失败: %v", r.File, err)
			}
			ok = scqvReGoSignalCall.MatchString(string(data))
		default:
			// shell 的信号机制是 trap，主门已经按语言映射表判 ⇒ 本门不管
			continue
		}
		checked++
		if !ok {
			t.Errorf("★ %s 的信号接管**没有真的注册**（找不到 signal.signal( / signal.Notify( 调用）\n"+
				"  ⇒ 主门那条轴是【存在性】判据（文件里出现 SIGTERM 就算过）⇒ 这里收紧\n"+
				"  ⇒ 真正的风险是「注册调用被删、只留常量与注释」⇒ 那样兜底在信号路径上仍然是空的",
				r.File)
		}
	}
	if checked == 0 {
		t.Fatalf("★ 本门一条都没量到（checked=0）⇒ 量具失效 ⇒ 拒绝给结论")
	}
	t.Logf("scqv 信号注册：量到 %d 个 python/go 腿", checked)
}

// ── 量具自检：证明本门有牙（★ 恒绿门 = 没用的门）──────────────────────────

func TestSelfCreatedCleanupTimeoutGateHasTeeth(t *testing.T) {
	// ① 15s 的清理语句必须被抓
	bad := "psql -c \"SET statement_timeout = '15s'\" -c \"DROP DATABASE IF EXISTS x WITH (FORCE);\""
	secs, ok := scqvSeconds(bad)
	if !ok {
		t.Fatalf("★ 量具自检失败：15s 的清理语句没解析出取值 ⇒ 本门抓不到它")
	}
	if secs >= scqvMinTimeoutSeconds {
		t.Errorf("★ 量具自检失败：15s 被算成 %ds ⇒ 本门对「量级不够」没有牙", secs)
	}

	// ② 120s 必须不被抓
	good := "psql -c \"SET statement_timeout = '120s'\" -c \"DROP DATABASE IF EXISTS x WITH (FORCE);\""
	secs, ok = scqvSeconds(good)
	if !ok || secs < scqvMinTimeoutSeconds {
		t.Errorf("★ 量具自检失败：120s 的取值解析为 (%d,%v) ⇒ 本门会误报", secs, ok)
	}

	// ③ 清理块里的 30s ctx 必须被抓
	consts := scqvGoConstSeconds("const cleanupTimeout = 120 * time.Second\n")
	blk := "func dropDatabase() { _ = context.WithTimeout(context.Background(), 30*time.Second) }\n" +
		"// DROP DATABASE IF EXISTS x"
	args := scqvWithTimeoutArgs(blk)
	if len(args) == 0 {
		t.Fatalf("★ 量具自检失败：没抓到 context.WithTimeout")
	}
	secs, ok = scqvGoDurationSeconds(args[0], consts)
	if !ok || secs >= scqvMinTimeoutSeconds {
		t.Errorf("★ 量具自检失败：30s 的 ctx 解析为 (%d,%v) ⇒ 本门对「清理块 ctx 太小」没有牙", secs, ok)
	}

	// ④ 具名常量形态必须能解析（internal/testdb 用的是常量）
	if n, ok := scqvGoDurationSeconds("cleanupTimeout", consts); !ok || n != 120 {
		t.Errorf("★ 量具自检失败：具名常量解析为 (%d,%v) ⇒ 本门读不懂 internal/testdb 的写法", n, ok)
	}

	// ⑤ python 载体：只有 DROP、没有查落点 ⇒ 必须被判不合格
	noResidual := "atexit.register(_drop_db)\n" +
		"def _drop_db():\n    try:\n        run_sql('DROP DATABASE IF EXISTS x;')\n    except Exception:\n        pass\n"
	found := false
	for _, blk := range scqvPyCarrierBlocks(noResidual) {
		if scqReDropDB.MatchString(blk) && scqvPyHasResidual(noResidual, blk) {
			found = true
		}
	}
	if found {
		t.Errorf("★ 量具自检失败：「只有 DROP、没有查落点」的载体被判合格 ⇒ 本门没有牙")
	}
	// ⑥ 有查落点 ⇒ 必须被判合格
	withResidual := "atexit.register(_drop_db)\n" +
		"def _drop_db():\n    try:\n        run_sql('DROP DATABASE IF EXISTS x;')\n" +
		"        n = run_sql(\"SELECT 'LEFT=' || count(*) FROM pg_database WHERE datname='x';\")\n" +
		"    except Exception:\n        pass\n"
	found = false
	for _, blk := range scqvPyCarrierBlocks(withResidual) {
		if scqReDropDB.MatchString(blk) && scqvPyHasResidual(withResidual, blk) {
			found = true
		}
	}
	if !found {
		t.Errorf("★ 量具自检失败：「有查落点」的载体被判不合格 ⇒ 本门误报")
	}
	// ⑦ 载体只【调】一个 helper、查询在 helper 里 ⇒ 也必须算合格
	//   （★ 本轮 3 个 python 腿里有 2 个是这个形态 ⇒ 不解析调用就会误报）
	viaHelper := "atexit.register(_drop_db)\n" +
		"def _residual():\n    return run_sql(\"SELECT count(*) FROM pg_database WHERE datname='x';\")\n" +
		"def _drop_db():\n    try:\n        run_sql('DROP DATABASE IF EXISTS x;')\n" +
		"        n = _residual()\n    except Exception:\n        pass\n"
	found = false
	for _, blk := range scqvPyCarrierBlocks(viaHelper) {
		if scqReDropDB.MatchString(blk) && scqvPyHasResidual(viaHelper, blk) {
			found = true
		}
	}
	if !found {
		t.Errorf("★ 量具自检失败：「查落点在 helper 里」的载体被判不合格 ⇒ 本门读不懂调用形态")
	}
	// ⑧ 判据 C：只「提到」SIGTERM 而没有注册调用 ⇒ 必须被判不合格
	mentionOnly := "import signal\n# 收到 SIGTERM 时要处理\ndef _drop_db():\n    pass\n"
	if scqvRePySignalCall.MatchString(mentionOnly) {
		t.Errorf("★ 量具自检失败：「只提到 SIGTERM、没有 signal.signal( 调用」被判合格 ⇒ 判据 C 没有牙")
	}
	registered := "import signal\nsignal.signal(signal.SIGTERM, _on_signal)\n"
	if !scqvRePySignalCall.MatchString(registered) {
		t.Errorf("★ 量具自检失败：「有 signal.signal( 调用」被判不合格 ⇒ 判据 C 误报")
	}
}
