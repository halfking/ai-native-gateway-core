package ursm_test

// R98 轮的门：**「SIGPIPE 竞态面」量具的 carry-over 路径不变量的变异套件**。
//
// # 这道门补的是哪块缺口（★ R98 轮实测出来的）
//
// R97 轮的门（`pipefail_race_ledger_test.go`）**只跑量具的「扫描」路径**：
//
//	python3 measure-pipefail-race-surface.py --root <root> --out <cand.tsv>
//
// ⇒ `build_ledger` / carry-over 六步级联 / 口径漂移作废 / 裁决串味自检（LedgerBleed）
//   ⇒ **整条路径没有任何门覆盖**。
// ⇒ 而 R97 轮声明的**核心不变量**（同形行不得互相冒用裁决、口径漂移不许自动重判、
//   组内行数变了不许自动重锚）**全都住在那条路径上**。
// ⇒ ⇒ **R97 的门守不住 R97 自己的核心不变量。**
//
// ★★ 实测还发现第二层：**真实台账里 `(file,fingerprint)` 组的裁决多样性 = 0**
//   （354 行 / 282 组 / 42 个多行组 / 114 行，**组内裁决全部同质**）
//   ⇒ ⇒ 工具自带的「裁决串味自检」在真实数据上**恒不触发**
//   ⇒ ⇒ 「门绿」与「闸从未开过火」**无法区分**。
//
// ⇒ ⇒ 所以本门**不用真实数据**造断言，而是造**合成夹具**：
//   ★ 三条核心不变量，每条配一个**量具级变异** ⇒ 变异之后断言**必须失败**
//   ⇒ ⇒ 断言的「有牙」不依赖真实数据碰巧出现 ⇒ **永远有牙**。
//
// # ★ 变异在**副本**上做（★ 比 R97 轮的做法更安全）
//
// R97 轮的变异套件是**直接改仓内真文件**再还原 ⇒ 还原失败就等于把仓改坏了
//   （R97 那轮确实踩到过「还原校验读错路径 ⇒ 报出假读数」）。
// ⇒ 本轮：**读源码 → 内存里替换 → 写到临时目录 → 跑临时副本**
//   ⇒ ⇒ **仓内真文件零改动** ⇒ 不需要还原 ⇒ 零风险。
//
// # 读数坐标
//
// 本门**不依赖仓库状态**（只用临时迷你仓）⇒ 跑得快（秒级）⇒ 适合进 CI 常规腿。

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	// r98ToolRel = 仓内普查量具（仓内相对路径）
	r98ToolRel = "scripts/audit/measure-pipefail-race-surface.py"

	// r98FixtureBody = 合成夹具（迷你仓里的唯一脚本）：
	//
	//	line 1  #!/usr/bin/env bash
	//	line 2  set -euo pipefail
	//	line 3  if cat big.txt | grep -q alpha; then echo hit; fi   ← 同形判断管道（组 A）
	//	line 4  if cat big.txt | grep -q alpha; then echo hit; fi   ← 同形判断管道（组 A）
	//	line 5  x="$(cat big.txt | head -1)"                        ← 同形取值管道（组 B）
	//	line 6  x="$(cat big.txt | head -1)"                        ← 同形取值管道（组 B）
	//
	// ★ 为什么要 4 行（而不是 2 行）：
	//   组 B（取值管道）的**自动初稿是终值**（`benign-value`）⇒
	//   ⇒ 「同形组行数变化」那条不变量被破坏时，会从 pending **掉到 benign-value**
	//   ⇒ ⇒ 这条不变量**测得出来**。
	//   ⇒ 若用组 A（判断管道），自动初稿恰好也是 pending ⇒ 破坏与否**看不出来**
	//   ⇒ ⇒ ★ **测「不许自动重锚」必须挑自动初稿是终值的那种行**
	//     （★ 同族：★ 断言的判别形态必须与被测形态**同构**）。
	r98FixtureBody = `#!/usr/bin/env bash
set -euo pipefail
if cat big.txt | grep -q alpha; then echo hit; fi
if cat big.txt | grep -q alpha; then echo hit; fi
x="$(cat big.txt | head -1)"
x="$(cat big.txt | head -1)"
`

	// r98DriftEvidence = 漂移场景里「旧台账」带的那条证据
	//   ⇒ 断言「漂移时**必须保留**原证据」时逐字比对它。
	r98DriftEvidence = "E5-旧台账的原始证据"
)

// r98Mut = 一处**定向**变异（锚点必须逐字命中且**唯一**）。
type r98Mut struct {
	name string
	old  string
	new  string
}

// r98ValidVerdicts = 合法的裁决值（含 pending）⇒ 用来把「非法裁决值」与
// 「合法的但不是 pending」**分开报**。
// ★★ 分开报的原因：R98 轮实测踩到过——第一版变异写成 `v = av[0]`
//
//	（`av` **本身就是**裁决值，不是元组 ⇒ `av[0]` 取到首字符 "b"）
//	⇒ 门照样抓到了变异，**但抓的理由是错的**（裁决值成了垃圾，不是「自动初稿」）
//	⇒ ⇒ ★ 断言必须能区分「非法值」与「合法但不是 pending」，否则报错会指错方向。
var r98ValidVerdicts = map[string]bool{
	"benign-bounded":     true,
	"benign-if-bounded":  true,
	"benign-value":       true,
	"benign-bare":        true,
	"risk-deterministic": true,
	"risk-race":          true,
	"conditional":        true,
	"pending":            true,
}

// r98Fixture = 合成夹具（迷你仓 + 一次真跑出来的候选集）。
type r98Fixture struct {
	dir    string
	root   string
	cand   [][]string
	byLine map[string][]string
}

// r98RepoRoot 用 runtime.Caller 定位仓库根（不依赖测试的工作目录）。
func r98RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓库根")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// r98ToolPath = 仓内量具的绝对路径。
func r98ToolPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(r98RepoRoot(t), r98ToolRel)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("量具不存在：%s ⇒ %v", p, err)
	}
	return p
}

// r98ToolSource = 量具源码（供变异使用）。
func r98ToolSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r98ToolPath(t))
	if err != nil {
		t.Fatalf("读量具源码失败：%v", err)
	}
	return string(b)
}

// r98ParseTSV 读 TSV（跳过注释行与表头）。
func r98ParseTSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows [][]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if cols[0] == "file" {
			continue
		}
		rows = append(rows, cols)
	}
	return rows, sc.Err()
}

// r98NewFixture 造合成夹具：写迷你仓 ⇒ 用**未变异**的量具跑一次 ⇒ 取候选集。
// ★ 候选集里带着**真指纹** ⇒ 合成台账不用硬编码 md5 ⇒ 量具改口径也不会让本门失效。
func r98NewFixture(t *testing.T) *r98Fixture {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("★ 本门依赖 python3（执行仓内量具 %s），本机没有 ⇒ 显式 SKIP：%v", r98ToolRel, err)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建迷你仓失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "fx.sh"), []byte(r98FixtureBody), 0o644); err != nil {
		t.Fatalf("写夹具脚本失败：%v", err)
	}
	candPath := filepath.Join(dir, "cand.tsv")
	cmd := exec.Command("python3", r98ToolPath(t), "--root", root, "--out", candPath)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("★ 量具在夹具上跑不起来（自检没过？）⇒ %v\n%s", err, combined)
	}
	rows, err := r98ParseTSV(candPath)
	if err != nil {
		t.Fatalf("读候选集失败：%v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("★ 夹具应产出 4 个候选行，实际 %d ⇒ 夹具形态变了 ⇒ 本门的断言会失准", len(rows))
	}
	fx := &r98Fixture{dir: dir, root: root, cand: rows, byLine: map[string][]string{}}
	for _, r := range rows {
		if len(r) < 12 {
			t.Fatalf("候选集行列数 = %d，不足 12：%v", len(r), r)
		}
		fx.byLine[r[1]] = r
	}
	// ★ 量具自检：确认两个同形组各有两行、且形态符合预期（否则断言的判别形态不对）。
	for _, want := range []struct{ line, kind, usage string }{
		{"3", "grep-q", "judgment"},
		{"4", "grep-q", "judgment"},
		{"5", "head", "value"},
		{"6", "head", "value"},
	} {
		r, ok := fx.byLine[want.line]
		if !ok {
			t.Fatalf("★ 夹具里没有第 %s 行", want.line)
		}
		if r[4] != want.kind || r[5] != want.usage {
			t.Fatalf("★ 夹具第 %s 行形态 = kind=%s usage=%s（期望 %s/%s）⇒ 夹具与量具口径脱节",
				want.line, r[4], r[5], want.kind, want.usage)
		}
	}
	if fx.byLine["3"][11] != fx.byLine["4"][11] {
		t.Fatalf("★ 夹具第 3/4 行不是同形（指纹不同）⇒ 组 A 造不出来")
	}
	if fx.byLine["5"][11] != fx.byLine["6"][11] {
		t.Fatalf("★ 夹具第 5/6 行不是同形（指纹不同）⇒ 组 B 造不出来")
	}
	if fx.byLine["3"][11] == fx.byLine["5"][11] {
		t.Fatalf("★ 夹具第 3 行与第 5 行同形 ⇒ 两个组会混成一组 ⇒ 夹具失效")
	}
	return fx
}

// r98SynthRow = 合成台账的一行。
type r98SynthRow struct {
	line     string
	verdict  string
	reason   string
	evidence string
	// fp 非空 ⇒ **故意写错的指纹**（用来造「口径漂移」）
	fp string
}

// writeLedger 写一份合成台账（13 列，顺序与量具的 LEDGER_COLS 一致）。
func (fx *r98Fixture) writeLedger(t *testing.T, name string, rows []r98SynthRow) string {
	t.Helper()
	var out []string
	for _, r := range rows {
		cand, ok := fx.byLine[r.line]
		if !ok {
			t.Fatalf("★ 夹具里没有第 %s 行 ⇒ 合成台账写不了", r.line)
		}
		fp := r.fp
		if fp == "" {
			fp = cand[11]
		}
		out = append(out, strings.Join([]string{
			cand[0], // file
			r.line,  // line
			cand[2], // occ
			cand[3], // scope
			cand[4], // kind
			cand[5], // usage
			cand[6], // volume
			cand[8], // writer
			r.verdict,
			r.reason,
			r.evidence,
			"test/",
			fp,
		}, "\t"))
	}
	p := filepath.Join(fx.dir, "ledger_"+name+".tsv")
	if err := os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("写合成台账失败：%v", err)
	}
	return p
}

// r98RunTool 跑（可能变异的）量具 ⇒ 返回 (退出码, 产出台账按行号索引)。
func r98RunTool(t *testing.T, toolPath string, fx *r98Fixture, ledgerPath, tag string) (int, map[string][]string) {
	t.Helper()
	out := filepath.Join(fx.dir, "cand_"+tag+".tsv")
	cmd := exec.Command("python3", toolPath, "--root", fx.root, "--out", out, "--ledger", ledgerPath)
	combined, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("跑量具失败：%v\n%s", err, combined)
		}
		code = ee.ExitCode()
	}
	rows, perr := r98ParseTSV(ledgerPath)
	if perr != nil {
		t.Fatalf("读产出台账失败：%v", perr)
	}
	byLine := map[string][]string{}
	for _, r := range rows {
		if len(r) < 13 {
			t.Fatalf("台账行列数 = %d，不足 13：%v", len(r), r)
		}
		byLine[r[1]] = r
	}
	return code, byLine
}

// ★★★★ **变异自检（并行会话 R95 轮·段 B 通报的量具纪律，★ 本轮落地）**：
// **「门绿」≠「变异真的发生了」** —— 若一条变异的 `old == new`（写错了、复制粘贴漏改、
// 或替换串与原串等价），则「锚点命中 1 次」照样成立 ⇒ **变异静默未发生** ⇒
// ⇒ **门会绿，而它绿得毫无意义**（测的是一个没被改过的工具）。
// ⇒ ⇒ 实测形态（`/tmp/r99probe2/m.go`）：`anchor_hits=1  mutated==original? true`
// ⇒ ⇒ 所以：**每施加一条变异，就断言副本确实与原文不同** ⇒ 否则直接失败。
// ⇒ ⇒ ★ 这条比「比对 md5」更强：md5 只能证明「变了」，**「变了吗」这件事本身**
// ⇒ ⇒ 在**施加时**就被断言 ⇒ 不会出现「先写错、后才发现」的空窗。
// ★★ **实现形态**：自检逻辑放在**可判定的纯函数** `r98ApplyMuts` 里（返回错误），
// ★★ `r98MutateTool` 只做「调用它 + 出错就 Fatal」⇒ ⇒ ★★ 这样护栏本身才能被**测到**
// ★★ （若护栏只存在于 `t.Fatalf` 分支里 ⇒ 测它就得靠子测试失败 ⇒ 父测试必红 ⇒ 恒红门）。
func r98ApplyMuts(src string, muts []r98Mut) (string, error) {
	for _, m := range muts {
		n := strings.Count(src, m.old)
		if n != 1 {
			return "", fmt.Errorf("★ 变异 %s 的锚点命中 %d 次（必须恰好 1 次 ⇒ 否则它改的不是你以为的地方）：\n%s",
				m.name, n, m.old)
		}
		before := src
		src = strings.Replace(src, m.old, m.new, 1)
		// ★★ **变异自检**：替换后必须与替换前不同 ⇒ 否则这条变异**没有真正发生**
		//   （典型成因：`old == new`，或替换串与原串等价）
		//   ⇒ ⇒ 此时门会绿，但绿得毫无意义 ⇒ 必须在这里失败。
		if src == before {
			return "", fmt.Errorf("★ 变异 %s 施加后源码**逐字节未变** ⇒ 该变异没有真正发生 ⇒ 门会绿但无意义"+
				"（★ 典型成因：`old == new`；★ 「门绿」≠「变异真的发生了」）：\nold=%q\nnew=%q",
				m.name, m.old, m.new)
		}
	}
	return src, nil
}

// r98MutateTool 复制量具源码 ⇒ 施加若干处定向变异 ⇒ 写到临时目录 ⇒ 返回副本路径。
// ★★ **仓内真文件零改动** ⇒ 不需要还原 ⇒ 没有「还原失败＝把仓改坏」的风险。
// ★★ 锚点必须**恰好命中一次** ⇒ 否则这个变异改的就不是我以为的地方 ⇒ 直接失败。
// ★★ 变异必须**真的发生** ⇒ 见 r98ApplyMuts 的变异自检。
func r98MutateTool(t *testing.T, muts []r98Mut) string {
	t.Helper()
	src, err := r98ApplyMuts(r98ToolSource(t), muts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	p := filepath.Join(t.TempDir(), "mutated_"+strings.ReplaceAll(muts[0].name, "/", "_")+".py")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("写变异量具副本失败：%v", err)
	}
	return p
}

// ── 三条核心不变量（每条返回空串 = 成立；否则 = 违反原因）─────────────────

// r98InvShiftPending：不变量①「同形组行数变了 ⇒ 该组新增行必须 pending」。
// ★ 判别形态选了组 B（取值管道）⇒ 它的自动初稿是**终值** ⇒ 违反时可观测。
func r98InvShiftPending(code int, led map[string][]string) string {
	if code != 0 {
		return fmt.Sprintf("量具退出码 = %d（期望 0）⇒ 工具拒绝写出", code)
	}
	row, ok := led["6"]
	if !ok {
		return "产出台账里没有第 6 行"
	}
	if row[8] != "pending" {
		if !r98ValidVerdicts[row[8]] {
			return fmt.Sprintf("第 6 行 verdict = %q（**非法裁决值**）⇒ 量具写出了不在允许集合里的裁决", row[8])
		}
		return fmt.Sprintf("第 6 行 verdict = %q（应 pending）⇒ 同形组行数变化时走了**自动初稿/自动重锚**", row[8])
	}
	if !strings.Contains(row[9], "同形组行数变化") {
		return fmt.Sprintf("第 6 行 reason = %q（应说明「同形组行数变化」）", row[9])
	}
	return ""
}

// r98InvDrift：不变量②「口径漂移 ⇒ pending + 保留原证据 + 不许把 risk 降级」。
func r98InvDrift(code int, led map[string][]string) string {
	if code != 0 {
		return fmt.Sprintf("量具退出码 = %d（期望 0）⇒ 工具拒绝写出", code)
	}
	row, ok := led["5"]
	if !ok {
		return "产出台账里没有第 5 行"
	}
	if row[8] != "pending" {
		if !r98ValidVerdicts[row[8]] {
			return fmt.Sprintf("第 5 行 verdict = %q（**非法裁决值**）", row[8])
		}
		return fmt.Sprintf("第 5 行 verdict = %q（应 pending）⇒ 口径漂移被**自动重判**了（★ 原裁决 risk-deterministic 被顶掉了）", row[8])
	}
	if !strings.Contains(row[9], "口径漂移") {
		return fmt.Sprintf("第 5 行 reason = %q（应说明「口径漂移」）", row[9])
	}
	if row[10] != r98DriftEvidence {
		return fmt.Sprintf("第 5 行 evidence = %q（应保留原证据 %q）⇒ 漂移时把证据丢了", row[10], r98DriftEvidence)
	}
	return ""
}

// r98InvNoBleed：不变量③「组内裁决多样 ⇒ 必须逐条保留（不得串味）」。
func r98InvNoBleed(code int, led map[string][]string) string {
	if code != 0 {
		return fmt.Sprintf("量具退出码 = %d（期望 0）⇒ 串味自检触发（工具拒绝写出）", code)
	}
	r3, ok3 := led["3"]
	r4, ok4 := led["4"]
	if !ok3 || !ok4 {
		return "产出台账里缺第 3/4 行"
	}
	if r3[8] != "risk-deterministic" {
		return fmt.Sprintf("第 3 行 verdict = %q（应 risk-deterministic）⇒ 同形行的裁决被**串味**了", r3[8])
	}
	if r4[8] != "benign-bare" {
		return fmt.Sprintf("第 4 行 verdict = %q（应 benign-bare）⇒ 同形行的裁决被**串味**了", r4[8])
	}
	return ""
}

// ── 三个合成场景的台账配方 ────────────────────────────────────────────────

// r98LedgerShift = 组 B 的旧台账只有 1 行 ⇒ 当前有 2 行 ⇒ **组大小变了**
//
//	⇒ 第 6 行无行号锚 ⇒ 必须是 pending。
func r98LedgerShift(fx *r98Fixture) []r98SynthRow {
	return []r98SynthRow{{
		line: "5", verdict: "benign-value", reason: "人工裁决", evidence: "E5",
	}}
}

// r98LedgerDrift = 第 5 行的**指纹被写错** ⇒ 形态变了 ⇒ 必须走「口径漂移」。
func r98LedgerDrift(fx *r98Fixture) []r98SynthRow {
	return []r98SynthRow{{
		line: "5", verdict: "risk-deterministic", reason: "人工裁决",
		evidence: r98DriftEvidence, fp: "deadbeef0000",
	}}
}

// r98LedgerDiverse = 组 A 的两行裁决**不同**（一 risk 一 benign）
//
//	⇒ 必须逐条保留；一旦串味，risk 会被 benign 洗掉。
func r98LedgerDiverse(fx *r98Fixture) []r98SynthRow {
	return []r98SynthRow{
		{line: "3", verdict: "risk-deterministic", reason: "人工裁决", evidence: "E3"},
		{line: "4", verdict: "benign-bare", reason: "人工裁决", evidence: "E4"},
	}
}

// ── 门 1：未变异的量具 ⇒ 三条不变量必须都成立 ──────────────────────────────

// TestPipefailRaceToolInvariantsHold：三条核心不变量在**未变异**的量具上必须成立。
// ★ 这是整套变异套件的**基线**：若这条红 ⇒ 夹具或断言本身有问题 ⇒ 其余变异测试无意义。
func TestPipefailRaceToolInvariantsHold(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98ToolPath(t)

	cases := []struct {
		name   string
		ledger []r98SynthRow
		check  func(int, map[string][]string) string
	}{
		{"shift", r98LedgerShift(fx), r98InvShiftPending},
		{"drift", r98LedgerDrift(fx), r98InvDrift},
		{"diverse", r98LedgerDiverse(fx), r98InvNoBleed},
	}
	for _, c := range cases {
		lp := fx.writeLedger(t, c.name, c.ledger)
		code, led := r98RunTool(t, tool, fx, lp, c.name)
		if msg := c.check(code, led); msg != "" {
			t.Errorf("★ 未变异的量具竟然违反了不变量【%s】：%s", c.name, msg)
		} else {
			t.Logf("✔ 不变量【%s】成立", c.name)
		}
	}
}

// ── 门 2~6：每条不变量配一个量具级变异 ⇒ 变异后断言必须失败 ─────────────────

// TestPipefailRaceToolMutationM1BreaksShiftPending：破坏「第 4 级 ⇒ pending」。
// ★★ 变异写成 `v = av`（**不是** `av[0]`）—— `av` 本身就是自动初稿的**裁决值**
//
//	⇒ ⇒ 破坏后的行为 = 「人工裁决被**自动初稿的终值**顶替」= 我们要复现的真实缺陷形态。
//	⇒⇒ R98 轮第一版误写成 `av[0]` ⇒ 取到的是裁决值的**首字符** ⇒ 门照样抓到了变异，
//	   **但抓的理由是错的** ⇒ ⇒ 每条变异测试都要断言「产出正是预期的那个坏值」。
func TestPipefailRaceToolMutationM1BreaksShiftPending(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98MutateTool(t, []r98Mut{{
		name: "M1",
		old: "            v = \"pending\"\n" +
			"            reason = (\"★ 同形组行数变化 ⇒ occ 位移 ⇒ **无法安全重锚** ⇒ 需人工重裁\"",
		new: "            v = av\n" +
			"            reason = (\"★ 同形组行数变化 ⇒ occ 位移 ⇒ **无法安全重锚** ⇒ 需人工重裁\"",
	}})
	lp := fx.writeLedger(t, "m1", r98LedgerShift(fx))
	code, led := r98RunTool(t, tool, fx, lp, "m1")
	// ★ 先验「变异确实产出了预期的坏值」⇒ 否则门可能是因为别的理由红的。
	r6, ok := led["6"]
	if !ok {
		t.Fatalf("★ 变异 M1 的产出里没有第 6 行 ⇒ 变异没按预期生效")
	}
	if r6[8] != "benign-value" {
		t.Fatalf("★ 变异 M1 的产出不对：第 6 行 verdict = %q（应 = 自动初稿的终值 benign-value）⇒ "+
			"这个变异**没有**复现「自动初稿顶替人工裁决」那个形态 ⇒ 测的不是想测的东西", r6[8])
	}
	if msg := r98InvShiftPending(code, led); msg == "" {
		t.Fatalf("★ 门没有牙：变异 M1（第 4 级不再回 pending）之后，不变量①**仍然成立**")
	} else {
		t.Logf("✔ 变异 M1 被抓住：%s", msg)
	}
}

// TestPipefailRaceToolMutationM2BreaksDriftPending：破坏「口径漂移 ⇒ pending」。
func TestPipefailRaceToolMutationM2BreaksDriftPending(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98MutateTool(t, []r98Mut{{
		name: "M2",
		old: "                v = \"pending\"\n" +
			"                reason = (\"★ 口径漂移（原裁决作废：\" + \"，\".join(drift) + \"）\"",
		new: "                v = av\n" +
			"                reason = (\"★ 口径漂移（原裁决作废：\" + \"，\".join(drift) + \"）\"",
	}})
	lp := fx.writeLedger(t, "m2", r98LedgerDrift(fx))
	code, led := r98RunTool(t, tool, fx, lp, "m2")
	r5, ok := led["5"]
	if !ok {
		t.Fatalf("★ 变异 M2 的产出里没有第 5 行 ⇒ 变异没按预期生效")
	}
	if r5[8] != "benign-value" {
		t.Fatalf("★ 变异 M2 的产出不对：第 5 行 verdict = %q（应 = 自动初稿的终值 benign-value；"+
			"原裁决是 risk-deterministic ⇒ 破坏后应被顶成 benign-value）⇒ 测的不是想测的东西", r5[8])
	}
	if msg := r98InvDrift(code, led); msg == "" {
		t.Fatalf("★ 门没有牙：变异 M2（口径漂移不再回 pending）之后，不变量②**仍然成立**")
	} else {
		t.Logf("✔ 变异 M2 被抓住：%s", msg)
	}
}

// TestPipefailRaceToolMutationM3BreaksDriftEvidence：破坏「漂移时保留原证据」。
func TestPipefailRaceToolMutationM3BreaksDriftEvidence(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98MutateTool(t, []r98Mut{{
		name: "M3",
		old: "                evid = old[\"evidence\"]\n" +
			"                own = old[\"owner\"]\n" +
			"                stats[\"invalidated\"] += 1\n" +
			"                stats[\"drift\"] += 1",
		new: "                evid = ae\n" +
			"                own = old[\"owner\"]\n" +
			"                stats[\"invalidated\"] += 1\n" +
			"                stats[\"drift\"] += 1",
	}})
	lp := fx.writeLedger(t, "m3", r98LedgerDrift(fx))
	code, led := r98RunTool(t, tool, fx, lp, "m3")
	r5, ok := led["5"]
	if !ok {
		t.Fatalf("★ 变异 M3 的产出里没有第 5 行 ⇒ 变异没按预期生效")
	}
	if r5[10] == r98DriftEvidence {
		t.Fatalf("★ 变异 M3 的产出不对：第 5 行 evidence 仍是原证据 ⇒ 变异没按预期生效")
	}
	if msg := r98InvDrift(code, led); msg == "" {
		t.Fatalf("★ 门没有牙：变异 M3（漂移时丢弃原证据）之后，不变量②**仍然成立**")
	} else {
		t.Logf("✔ 变异 M3 被抓住：%s", msg)
	}
}

// TestPipefailRaceToolMutationM4BreaksNoBleed：把 carry-over 的键**降级**回
// `(file, fp)`（★ R97 轮实测踩出来的 D1 原样回退）⇒ 同形行互相冒用裁决。
func TestPipefailRaceToolMutationM4BreaksNoBleed(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98MutateTool(t, []r98Mut{{
		name: "M4",
		old:  "            old, status = old_by_key[(path, fp, o)], \"exact\"",
		new:  "            old, status = old_by_fp[(path, fp)][-1], \"exact\"",
	}})
	lp := fx.writeLedger(t, "m4", r98LedgerDiverse(fx))
	code, led := r98RunTool(t, tool, fx, lp, "m4")
	// ★ 先验「变异确实产出了预期的坏值」⇒ M4 的形态是「串味自检触发 ⇒ 拒绝写出」。
	if code == 0 {
		t.Fatalf("★ 变异 M4 的产出不对：退出码 = 0（期望 ≠ 0 ⇒ 串味自检应当触发并拒绝写出）⇒ " +
			"这个变异没有复现「串味 ⇒ 工具自己拦下来」那个形态")
	}
	if msg := r98InvNoBleed(code, led); msg == "" {
		t.Fatalf("★ 门没有牙：变异 M4（carry-over 键降级为 (file,fp)）之后，不变量③**仍然成立**")
	} else {
		t.Logf("✔ 变异 M4 被抓住：%s", msg)
	}
}

// TestPipefailRaceToolMutationM5SilencesBleedCheck：M4 + **摘掉串味自检**
//
//	⇒ ⇒ 串味变成**静默**的（工具退出码 0、台账照写）。
//
// ★★★ 这是本门**最有价值**的一条：它证明「不变量③」不是靠工具自己的闸守住的
//
//	⇒ ⇒ 工具的闸可以被摘掉而不影响「门是绿的」⇒ ⇒ 门必须自己长牙。
func TestPipefailRaceToolMutationM5SilencesBleedCheck(t *testing.T) {
	fx := r98NewFixture(t)
	tool := r98MutateTool(t, []r98Mut{
		{
			name: "M4",
			old:  "            old, status = old_by_key[(path, fp, o)], \"exact\"",
			new:  "            old, status = old_by_fp[(path, fp)][-1], \"exact\"",
		},
		{
			name: "M5",
			old:  "    if bleed_errors:\n        raise LedgerBleed(bleed_errors)",
			new:  "    if bleed_errors:\n        pass  # MUTATED：串味自检被摘掉",
		},
	})
	lp := fx.writeLedger(t, "m5", r98LedgerDiverse(fx))
	code, led := r98RunTool(t, tool, fx, lp, "m5")
	if code != 0 {
		t.Fatalf("★ 变异 M5 的前提不成立：量具退出码 = %d（期望 0 ⇒ 串味应当是静默的）", code)
	}
	// ★ 先验「变异确实产出了预期的坏值」⇒ M5 的形态是「静默串味：risk 被 benign 洗掉」。
	r3, ok := led["3"]
	if !ok {
		t.Fatalf("★ 变异 M5 的产出里没有第 3 行 ⇒ 变异没按预期生效")
	}
	if r3[8] != "benign-bare" {
		t.Fatalf("★ 变异 M5 的产出不对：第 3 行 verdict = %q（应 = benign-bare ⇒ 即 risk 被 benign 洗掉）⇒ "+
			"这个变异没有复现「静默串味」那个形态 ⇒ 测的不是想测的东西", r3[8])
	}
	if msg := r98InvNoBleed(code, led); msg == "" {
		t.Fatalf("★ 门没有牙：变异 M5（键降级 + 摘掉串味自检 ⇒ 静默串味）之后，不变量③**仍然成立**")
	} else {
		t.Logf("✔ 变异 M5 被抓住：%s", msg)
	}
}

// TestPipefailRaceToolMutationAnchorsStayUnique：变异锚点必须**逐字唯一**。
// ★ 这条是「变异套件自己的自检」：锚点一旦不唯一（有人改了量具的措辞），
//
//	那些变异测试就会**悄悄变异到别的地方** ⇒ ⇒ 门会假装自己有牙。
func TestPipefailRaceToolMutationAnchorsStayUnique(t *testing.T) {
	src := r98ToolSource(t)
	anchors := []r98Mut{
		{
			name: "M1",
			old: "            v = \"pending\"\n" +
				"            reason = (\"★ 同形组行数变化 ⇒ occ 位移 ⇒ **无法安全重锚** ⇒ 需人工重裁\"",
		},
		{
			name: "M2",
			old: "                v = \"pending\"\n" +
				"                reason = (\"★ 口径漂移（原裁决作废：\" + \"，\".join(drift) + \"）\"",
		},
		{
			name: "M3",
			old: "                evid = old[\"evidence\"]\n" +
				"                own = old[\"owner\"]\n" +
				"                stats[\"invalidated\"] += 1\n" +
				"                stats[\"drift\"] += 1",
		},
		{
			name: "M4",
			old:  "            old, status = old_by_key[(path, fp, o)], \"exact\"",
		},
		{
			name: "M5",
			old:  "    if bleed_errors:\n        raise LedgerBleed(bleed_errors)",
		},
	}
	for _, m := range anchors {
		if n := strings.Count(src, m.old); n != 1 {
			t.Errorf("★ 变异 %s 的锚点命中 %d 次（必须恰好 1 次）⇒ 量具措辞变了 ⇒ "+
				"变异套件会**变异到别的地方**⇒ 门会假装自己有牙", m.name, n)
		}
	}
}

// TestPipefailRaceToolMutationSelfCheckHasTeeth：★ **变异自检本身必须有牙**。
//
// ★ 这条测的是【护栏自己】，不是量具：
// ★ 并行会话 R95 轮·段 B 通报的量具纪律 —— 「**门绿」≠「变异真的发生了**」。
// ★ 具体形态：`old == new` ⇒ 锚点照样命中 1 次 ⇒ 变异**静默未发生** ⇒ 门会绿，
// ★ 而它绿得毫无意义（测的是一个**没被改过**的工具）。
//
// ⇒ ⇒ 所以这里构造一条**故意写错的变异**（`old == new`）⇒
// ⇒ ⇒ 期望：护栏**拒绝**它（返回错误）。
// ⇒ ⇒ ★ 断言形态必须是「护栏拒绝了它」，而不是「变异后门抓到了它」
// ⇒ ⇒ （后者在 `old == new` 时**永远不可能发生** ⇒ 那才是恒绿门）。
// ★★ **为什么判据落在纯函数 `r98ApplyMuts` 上**：
// ★★ 若护栏只存在于 `t.Fatalf` 分支里 ⇒ 要测它就只能靠「子测试失败」⇒
// ★★ 而子测试失败会**连带把父测试标红** ⇒ ⇒ **那条测试永远红** ⇒ 恒红门。
// ★★ ⇒ 所以护栏必须是**可返回错误的纯逻辑** ⇒ 它的「有牙」才能被**正面判定**。
func TestPipefailRaceToolMutationSelfCheckHasTeeth(t *testing.T) {
	src := r98ToolSource(t)
	const anchor = `if bleed_errors:`
	if n := strings.Count(src, anchor); n != 1 {
		t.Skipf("★ 阳性对照锚点命中 %d 次（期望 1）⇒ 量具措辞已变 ⇒ 本门需重写锚点", n)
	}

	// ★ 阳性对照：这条假变异的 old 与 new **逐字相同** ⇒ 替换是恒等替换
	//   ⇒ 「锚点命中 1 次」照样成立 ⇒ 若无变异自检，它会**静默通过**。
	fake := r98Mut{name: "FAKE-noop", old: anchor, new: anchor}
	if fake.old != fake.new {
		t.Fatalf("★ 阳性对照构造错了：old != new ⇒ 这不是一条假变异")
	}
	out, err := r98ApplyMuts(src, []r98Mut{fake})
	if err == nil {
		t.Fatalf("★ 变异自检没有牙：一条 `old == new` 的假变异竟然被静默接受了（输出与原文相同? %v）⇒ "+
			"⇒ 任何「复制粘贴漏改 / 替换串与原串等价」的变异都会**静默通过** ⇒ "+
			"⇒ 门会绿，但绿得毫无意义", out == src)
	}
	if out != "" {
		t.Fatalf("★ 护栏失败时应返回空源码，实际返回了非空内容 ⇒ 失败路径的语义不对")
	}
	t.Logf("✔ 变异自检有牙：`old == new` 的假变异在**施加时**就被拒绝了 ⇒ %v", err)

	// ★ 阴性对照：一条**真的会改**的变异 ⇒ 必须被接受 ⇒ 否则护栏是「逢变异必杀」的恒红门。
	real := r98Mut{name: "REAL", old: anchor, new: anchor + "\n    pass"}
	out2, err2 := r98ApplyMuts(src, []r98Mut{real})
	if err2 != nil {
		t.Fatalf("★ 阴性对照失败：一条**真的会改**的变异被拒了 ⇒ 护栏成了「逢变异必杀」⇒ %v", err2)
	}
	if out2 == src {
		t.Fatalf("★ 阴性对照失败：真变异接受但输出与原文相同 ⇒ 护栏逻辑不对")
	}
	t.Logf("✔ 阴性对照通过：一条真变异被正常接受（输出确实变了）")

	// ★ 锚点护栏的阴性对照：锚点命中 2 次 ⇒ 必须被拒（否则它改的不是你以为的地方）。
	dup := r98Mut{name: "DUP", old: "    ", new: "    "} // 空格在量具里出现多次
	if _, err3 := r98ApplyMuts(src, []r98Mut{dup}); err3 == nil {
		t.Fatalf("★ 锚点护栏没有牙：一个命中多次的锚点竟然被接受了 ⇒ 变异会改到别的地方")
	}
	t.Logf("✔ 锚点护栏有牙：命中多次的锚点被拒绝")
}
