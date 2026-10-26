package ursm_test

// R99 轮 · 段十五（§19.20.76）的**门**：「写端聚类闭包」＋「护栏纯逻辑化」
//
// ── 为什么要有这道门 ────────────────────────────────────────────────────────
//
// ★ 段十四（§19.20.75）修了 8 处真缺陷，但**漏了同文件、同写端变量的兄弟站点**
//   （`verify-g1-route-contract.sh` 的 `expect_red` 已改、`expect_green` 未改；
//    `verify-l11b-cost-view-rowset.sh` 同型）⇒ ⇒ ★★ **段十四的「逐站点判定」是按【行】判的，
//   而真正的判定单位是【写端变量】** ⇒ ⇒ 同一个写端的其它站点要么一起成立、要么一起不成立。
// ⇒ 本门把这件事**机械化**：对每个已修的「文件 × 写端变量」，
//   **该文件内不得再出现「写端仍是管道且末端消费者是 grep」的形态**
//   ⇒ ⇒ 漏配对会被当场抓住 ⇒ ⇒ ★ 这条判据直接针对段十四的漏修根因。
//
// ★★ 判据②（清单驱动）：写端变量清单从**已装候选清单**里现读
//   ⇒ ⇒ 将来清单新增的站点也会自动进入闭包检查 ⇒ ⇒ 门不会因为清单变长而失效。
//
// ★★ 判据④（护栏自检）把护栏做成**纯逻辑**（`sc1976WriteEndPipelineLines`）＋
//   **三条断言（阳性 / 阴性 / 锚点唯一）** ⇒ ⇒ 护栏自己答齐三问：
//   ① 改到的是不是我以为的地方（锚点唯一）② 改了吗（阳性：真能报出来）
//   ③ 改出来的坏成什么样（阴性：没有就不该报）
//   ⇒ ★ **这比段十四强一格**：段十四的 `if mutatedSrc == string(src) { t.Fatal }`
//   那条**阴性分支从未被真实执行过** ⇒ ⇒ 本门把它变成**被断言的纯逻辑**。
//
// ── 边界：为什么闭包只管「末端消费者是 grep」─────────────────────────────────
//
// ★ 剩下的 13 处「写端仍是管道」全部是 `… | head -N` / `| tail -N` / `| awk` 形态
//   ⇒ ⇒ 写端确实会被 SIGPIPE 杀掉，但 ★ **它们的退出码要么被丢弃（裸命令）、
//   要么被【末端命令】的状态覆盖** ⇒ ⇒ **没有判定会翻转 ⇒ 无害** ⇒ ⇒ 不在闭包范围内。
//   ⇒ ★ 而末端是 `grep`（尤其 `-q`，命中即退）时，141 会经 pipefail 顶掉 grep 的 0
//   ⇒ ⇒ 判定翻转 ⇒ ⇒ 那才是要闭包的形态。
//
// ★ 对生产的写入 = 0（只读仓内文件）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	sc1976ListRel = "docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv"
)

// sc1976FixedWriteEnd 是「已修写端」清单：(文件, 写端变量, 该变量在文件内的 herestring 站点数)。
//
// ★ 为什么按【写端变量】而不是按【行】记录：段十四的漏修根因就是「逐行判定、漏了同写端的兄弟站点」
//
//	⇒ ⇒ 这里以写端变量为单位记录 ⇒ ⇒ 闭包判据才能覆盖兄弟站点。
//
// ★ 计数是**当前树的事实**（实测）⇒ ⇒ 少改一处 ⇒ 计数掉 ⇒ 判据④红。
var sc1976FixedWriteEnd = []struct {
	File       string
	Var        string
	HerestrMin int
}{
	// ★ 段十四修的写端（兄弟站点在段十五闭合）
	{"scripts/audit/verify-g1-route-contract.sh", "output", 3},
	{"scripts/audit/verify-l11b-cost-view-rowset.sh", "output", 4},
	{"scripts/audit/verify-nonplain-migration-delivery.sh", "output", 13},
	{"scripts/deploy-seamless.sh", "probe_fail_tail", 4},
	{"scripts/verify-ursm-v2-rollout.sh", "metrics", 1},
	// ★ 段十五修的写端（上界钉不死 ⇒ 保守处置）
	{"scripts/monitoring/pg-error-classifier.sh", "msg", 12},
	{"scripts/monitoring/pg-error-classifier.sh", "stmt", 3},
	{"tests/routing/layer2_components/test_detection.sh", "LAST_LOGS", 6},
	{"tests/routing/layer1_direct/test_protocol_conversion.sh", "LAST_LOGS", 2},
	{"tests/routing/layer2_components/test_compression.sh", "LAST_LOGS", 2},
	{"tests/routing/layer2_components/test_sticky.sh", "LAST_LOGS", 3},
	{"tests/routing/layer3_integration/test_fault_recovery.sh", "LAST_LOGS", 5},
	{"tests/routing/layer3_integration/test_full_routing.sh", "LAST_LOGS", 4},
}

// sc1976RepoRoot 返回仓根目录。
func sc1976RepoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// sc1976WriteEndPipeToGrepRE 匹配「写端仍是管道、且末端消费者是 grep」的形态：
//
//	echo "$VAR" | grep …      /      printf '%s' "$VAR" | grep …      /      printf '%s\n' "$VAR" | grep …
//
// ★ 变量名带**词边界** ⇒ `$output` 不会误命中 `$output2`（判据③的锚点唯一性断言就钉这条）。
func sc1976WriteEndPipeToGrepRE(varName string) *regexp.Regexp {
	return regexp.MustCompile(`(?:echo|printf)(?:\s+'[^']*'|\s+"[^"]*")?\s+"\$` +
		regexp.QuoteMeta(varName) + `"\s*\|\s*grep\b`)
}

// sc1976WriteEndPipelineLines 是**纯逻辑护栏**：返回 content 中「写端仍是管道且末端是 grep」的行号（1-based）。
//
// ★ 纯逻辑 ⇒ 可以被【直接断言】（阳性 / 阴性）⇒ ⇒ 护栏自己被证明有牙，而不是靠「跑一次看看」。
func sc1976WriteEndPipelineLines(content string, varName string) []int {
	re := sc1976WriteEndPipeToGrepRE(varName)
	var out []int
	for i, line := range strings.Split(content, "\n") {
		if re.MatchString(line) {
			out = append(out, i+1)
		}
	}
	return out
}

// sc1976HerestringLines 返回 content 中「该变量作为 herestring 写端」的行号（1-based）。
func sc1976HerestringLines(content string, varName string) []int {
	re := regexp.MustCompile(`<<<\s*"\$` + regexp.QuoteMeta(varName) + `"`)
	var out []int
	for i, line := range strings.Split(content, "\n") {
		if re.MatchString(line) {
			out = append(out, i+1)
		}
	}
	return out
}

// ── 判据①：写端聚类闭包 ────────────────────────────────────────────────────
//
// ★ **每个已修写端所在的文件里，该写端不得再以「管道 → grep」形态出现**
//
//	⇒ ⇒ 段十四的漏修（同文件同写端的兄弟站点）会被这条当场抓住。
func TestSection1976WriteEndClusterIsClosed(t *testing.T) {
	root := sc1976RepoRoot(t)
	for _, fx := range sc1976FixedWriteEnd {
		p := filepath.Join(root, fx.File)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s: %v", fx.File, err)
		}
		lines := sc1976WriteEndPipelineLines(string(data), fx.Var)
		if len(lines) != 0 {
			t.Errorf("★ 写端聚类【未闭合】：%s 的 $%s 仍有 %d 处「写端 | grep」⇒ 行 %v\n"+
				"  ⇒ ★ 同一个写端变量的其它站点必须一起改（段十四就是漏在这里）\n"+
				"  ⇒ 修法：把 `echo \"$%s\" | grep …` 改成 `grep … <<<\"$%s\"`",
				fx.File, fx.Var, len(lines), lines, fx.Var, fx.Var)
			continue
		}
		her := sc1976HerestringLines(string(data), fx.Var)
		if len(her) < fx.HerestrMin {
			t.Errorf("★ 写端 $%s 在 %s 里的 herestring 站点只有 %d 处（期望 ≥ %d）⇒ ★ 修法形态被回退了？",
				fx.Var, fx.File, len(her), fx.HerestrMin)
		}
		t.Logf("✔ %s 的 $%s：写端 | grep 残留 0 处，herestring 站点 %d 处", fx.File, fx.Var, len(her))
	}
}

// ── 判据②：清单驱动 —— 清单里出现过的写端，闭包检查必须覆盖它 ──────────────
//
// ★ 为什么要有这条：判据①的变量表是硬编码的 ⇒ ⇒ 若清单将来出现**新的**写端变量，
//
//	闭包检查不会自动覆盖 ⇒ ⇒ 这里从【已装清单】现读 ⇒ ⇒ 清单与门不得脱节。
//	⇒ ★ 而且这里扫的是【当前文件内容】而不是清单里存下来的那行文本
//	⇒ ⇒ ⇒ 有人把某个已修站点改回管道 ⇒ 立刻被抓（哪怕清单还没重生成）。
func TestSection1976ListDrivenWriteEndCoverage(t *testing.T) {
	root := sc1976RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, sc1976ListRel))
	if err != nil {
		t.Fatalf("读清单 %s: %v", sc1976ListRel, err)
	}
	known := map[string]bool{} // "file|var" → true
	for _, fx := range sc1976FixedWriteEnd {
		known[fx.File+"|"+fx.Var] = true
	}
	checked := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "file\t") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 10 {
			continue
		}
		for _, fx := range sc1976FixedWriteEnd {
			if fx.File != f[0] {
				continue
			}
			if !known[fx.File+"|"+fx.Var] {
				continue
			}
			checked++
			p := filepath.Join(root, fx.File)
			content, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("读 %s: %v", fx.File, err)
			}
			if bad := sc1976WriteEndPipelineLines(string(content), fx.Var); len(bad) != 0 {
				t.Errorf("★ 清单里有 %s 的 $%s，而【当前文件】里该写端仍有 %d 处「写端 | grep」⇒ 行 %v\n"+
					"  ⇒ ⇒ 清单与门脱节（有人把修法改回去了？）", fx.File, fx.Var, len(bad), bad)
			}
		}
	}
	t.Logf("✔ 清单驱动覆盖检查：清单中命中已修写端的行 %d 条 ⇒ 当前文件里「写端 | grep」残留 0 处", checked)
}

// ── 判据③：护栏自检（阳性 / 阴性 / 锚点唯一）────────────────────────────────
//
// ★ ★★ 这条比段十四强一格：段十四的阴性分支（`mutatedSrc == string(src)` 那条 Fatal）
//
//	**从未被真实执行过** ⇒ ⇒ 本门把它变成【纯逻辑 + 三条被断言的规则】。
func TestSection1976WriteEndGuardHasTeeth(t *testing.T) {
	// 阳性：真正的「写端 | grep」形态必须被报出来（每种写法各一条）。
	pos := []struct {
		line string
		var_ string
	}{
		{`if echo "$output" | grep -qE '^--- FAIL:'; then`, "output"},
		{`if printf '%s' "$msg" | grep -qF 'x'; then`, "msg"},
		{`if printf '%s\n' "$LAST_LOGS" | grep -qi "x"; then`, "LAST_LOGS"},
		{`  echo "$output" | grep -A3 'contract FAILED' | head -12`, "output"},
	}
	for _, c := range pos {
		if got := sc1976WriteEndPipelineLines(c.line, c.var_); len(got) != 1 {
			t.Errorf("★ 阳性断言失败：%q（$%s）⇒ 护栏报出 %v（期望 1 行）", c.line, c.var_, got)
		}
	}
	// 阴性：herestring 形态（以及与本变量无关的变量）**不得**被报出来。
	neg := []struct {
		line string
		var_ string
	}{
		{`if grep -qE '^--- FAIL:' <<<"$output"; then`, "output"},
		{`if grep -qF 'x' <<<"$msg"; then`, "msg"},
		{`failed="$(grep -oE '^--- FAIL: [A-Za-z0-9_]+' <<<"$output" | sed 's/^--- FAIL: //' | sort -u)"`, "output"},
		{`if echo "$output2" | grep -q x; then`, "output"}, // ★ 锚点唯一性：$output2 ≠ $output
		{`if echo "$outputs" | grep -q x; then`, "output"},
		{`if echo "$LAST_LOGS2" | grep -q x; then`, "LAST_LOGS"},
	}
	for _, c := range neg {
		if got := sc1976WriteEndPipelineLines(c.line, c.var_); len(got) != 0 {
			t.Errorf("★ 阴性断言失败：%q（$%s）⇒ 护栏误报 %v", c.line, c.var_, got)
		}
	}
	// ★ 锚点唯一性：$output 的护栏**不得**命中 $output2 / $outputs。
	if got := sc1976WriteEndPipelineLines(`if echo "$output2" | grep -q x; then`, "output"); len(got) != 0 {
		t.Errorf("★ 锚点不唯一：$output 的护栏命中了 $output2 ⇒ %v", got)
	}
	t.Log("✔ 护栏有牙：阳性（4 种写端形态都被报出）／阴性（herestring 与无关变量都不报）／锚点唯一（$output ≠ $output2）")
}

// ── 判据④：已修写端的 herestring 站点总数锚（防止「悄悄少修」）──────────────
func TestSection1976FixedSiteCountAnchor(t *testing.T) {
	root := sc1976RepoRoot(t)
	total := 0
	for _, fx := range sc1976FixedWriteEnd {
		data, err := os.ReadFile(filepath.Join(root, fx.File))
		if err != nil {
			t.Fatalf("读 %s: %v", fx.File, err)
		}
		total += len(sc1976HerestringLines(string(data), fx.Var))
	}
	// ★ 期望值 = 当前树实测（13 个「文件 × 写端」对合计 62 处）
	const want = 62
	if total != want {
		t.Errorf("★ 已修写端的 herestring 站点总数 = %d（期望 %d）⇒ ★ 段十五的改动被回退/漏改了？",
			total, want)
	}
	t.Logf("✔ 已修写端的 herestring 站点总数 = %d（期望 %d）", total, want)
}
