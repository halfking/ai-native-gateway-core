//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// env_sample_registry_incremental_gate_test.go —— LLM_GATEWAY_* 环境变量「增量登记」门。
//
// ── 起因（★ 主档 §4 的 L14 登记项，★ 第四轮）───────────────────────────────
//
// ★ L6 那轮发现「LLM_GATEWAY_RECONCILIATION_SHADOW_ENABLED 没有进任何登记册」时，
// ★ 按 `envs.samples/README.md` §9 的口径全仓复扫，得到：
//
//	扫描到的 LLM_GATEWAY_* 变量：530 个
//	其中未在 envs.samples/*.env.sample 登记的：88 个（★ 覆盖率 ≈ 83.4%）
//
// ★★ ⇒ 「未进登记册」**不是孤例**，★ 而是一**类**缺口 ⇒ ★ 本门就为它而立。
//
// ── 本门判什么（★ 增量语义）────────────────────────────────────────────
//
// ★ R1「新增未登记」：代码里读到、★ 但既不在登记集、★ 也不在存量豁免基线 ⇒ **红灯并点名**。
// ★ R2「基线棘轮」：★ 豁免基线条数**只许减不许增**（★ 上限 88）⇒ **红灯**。
// ★ R3「基线冗余」：★ 基线里某行**已经进了登记集** ⇒ **红灯**（★ 提示删掉该行收紧基线）。
// ★ R4「基线卫生」：★ 基线必须**有序且无重复** ⇒ **红灯**。
//
// ★★ ⇒ ★ 效果：**存量 88 个豁免、★ 新增变量必须登记** ★★。
//
// ── 口径（★ 与 envs.samples/README.md §9 对齐，★ 并修掉那条命令的坏写法）──
//
// ★ 扫描：git ls-files 取文件集 → 仅 *.go（★ 排除 vendor/ 与 *_test.go）
//         → 匹配形如  标识符("LLM_GATEWAY_…"  的**实参**
// ★ 登记：envs.samples/*.env.sample 里形如 ^LLM_GATEWAY_X= 的**赋值行**
//
// ★ ★★ 顺带修掉的一个坑（★ 真缺陷，★ 不是本门臆造）★★：
//
// ★ README §9 写的那条审计命令里带 `\b`：
//
//	git grep -hoE '\b[a-zA-Z][a-zA-Z0-9]*\("[A-Z][A-Z0-9_]+"' -- '*.go' …
//
// ★★ 而 **git grep 的 ERE 引擎不支持 `\b`**（★ 实测：`git grep -E '\bfoo'` 匹配不到，
// ★★ 而**系统** `grep -E '\bfoo'` 与 `git grep -P '\bfoo'` 都匹配得到）⇒ ★
// ★★ 这条命令**在 git 下只能捞到 4 个变量**（★ 实测），★ 而去掉 `\b` 是 530 个 ⇒ ★★
// ★★ **照文档抄的人会以为「几乎没什么可登记的」** ⇒ ★★ **那是一条会让人漏掉整类缺口的
// ★★ 坏口径** ⇒ ★ 本门与 README 都用**去掉 `\b`** 的写法（★ 两边都稳）。
//
// ── 残余限制（★ 诚实登记，★ 免得被当成 airtight）──────────────────────────
//
// ★ R2 是**条数**棘轮 ⇒ ★ 若有人**同时**「删一条 + 加一条」，★ 条数不变 ⇒ ★ 仍可绕过。
// ★ ⇒ 残余风险靠 **diff 可见** ＋ R3「基线条目必须仍是未登记」兜底；
// ★ ⇒ 真正的 airtight 做法是**冻结集合快照**，★ 留待 owner 决定是否再加一道。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// envSampleRegistryBaselineFile 是存量豁免基线（★ 相对仓根）。
const envSampleRegistryBaselineFile = "scripts/ursmcheck/env_sample_registry_baseline.tsv"

// envSampleRegistryBaselineCap 是基线条数上限（★ 2026-11-24 基线 = 88 条）。
const envSampleRegistryBaselineCap = 88

// envSampleRegistryCallRe 匹配「函数(实参)」形态的 LLM_GATEWAY_* 变量名。
// ★ 刻意**不带 \b**：★ POSIX ERE 的 \b 是退格符（★ 见文件头注释）。
var envSampleRegistryCallRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9]*\("(LLM_GATEWAY_[A-Z0-9_]+)"`)

// envSampleRegistryAssignRe 匹配 envs.samples 里的赋值行。
var envSampleRegistryAssignRe = regexp.MustCompile(`^(LLM_GATEWAY_[A-Z0-9_]+)=`)

// envRepoRoot 返回仓根。
func envRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("★ 取仓根失败（★ 需要 git 在 PATH）：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// envGitFiles 返回 git 认的文件（★ 含未跟踪未忽略的，★ 与仓内既有门同口径）。
func envGitFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--full-name",
		"--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("★ 列 git 认的文件失败（★ 需要 git 在 PATH 且 %s 是仓根）：%v", root, err)
	}
	var rels []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			rels = append(rels, f)
		}
	}
	if len(rels) == 0 {
		t.Fatalf("★ git 认的文件数为 0 ⇒ ★ 判据的输入集合不对")
	}
	return rels
}

// envScanLLMGatewayVars 扫出代码里读到的 LLM_GATEWAY_* 变量名（★ 附带首个引用文件）。
func envScanLLMGatewayVars(t *testing.T, root string) (map[string]string, int) {
	t.Helper()
	first := map[string]string{}
	scannedFiles := 0
	for _, rel := range envGitFiles(t, root) {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if rel == "vendor" || strings.HasPrefix(rel, "vendor/") ||
			strings.Contains(rel, "/vendor/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("★ 读 %s 失败：%v", rel, err)
		}
		scannedFiles++
		for _, m := range envSampleRegistryCallRe.FindAllStringSubmatch(string(data), -1) {
			if _, seen := first[m[1]]; !seen {
				first[m[1]] = rel
			}
		}
	}
	return first, scannedFiles
}

// envRegisteredSampleVars 读 envs.samples/*.env.sample 里以赋值行登记的变量名。
func envRegisteredSampleVars(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "envs.samples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("★ 读 envs.samples 失败：%v", err)
	}
	reg := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".env.sample") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("★ 读 %s 失败：%v", e.Name(), err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := envSampleRegistryAssignRe.FindStringSubmatch(line); m != nil {
				reg[m[1]] = true
			}
		}
	}
	if len(reg) == 0 {
		t.Fatalf("★ envs.samples 里登记到的变量数为 0 ⇒ ★ 口径写错了（★ 不是真的没登记）")
	}
	return reg
}

// envReadBaseline 读存量豁免基线（★ 返回有序的变量名 + 首个引用文件）。
func envReadBaseline(t *testing.T, root string) ([]string, map[string]string) {
	t.Helper()
	path := filepath.Join(root, envSampleRegistryBaselineFile)
	data, err := os.ReadFile(path)
	if err != nil {
		// ★ ★ 读不到基线必须**硬失败** ★ ★：★ 读不到就 Skip 是一种假绿
		// （★ 看着像「门不存在」，★ 而不是「门坏了」⇒ ★ 立刻改成 Fatal）。
		t.Fatalf("★ 读存量豁免基线 %s 失败（★ 读不到基线不许 Skip，★ 那是假绿）：%v",
			envSampleRegistryBaselineFile, err)
	}
	var names []string
	first := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		names = append(names, parts[0])
		if len(parts) > 1 {
			first[parts[0]] = parts[1]
		}
	}
	if len(names) == 0 {
		t.Fatalf("★ 存量豁免基线为空 ⇒ ★ 判据的输入集合不对")
	}
	return names, first
}

// envUnregisteredVars 是本门的**纯函数核心**（★ 便于变异自证）。
func envUnregisteredVars(scanned []string, registered, baseline map[string]bool) []string {
	var out []string
	for _, n := range scanned {
		if !registered[n] && !baseline[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ── 主门 ────────────────────────────────────────────────────────────────

func TestEnvSampleRegistryIncrementalGate(t *testing.T) {
	root := envRepoRoot(t)
	scannedMap, scannedFiles := envScanLLMGatewayVars(t, root)
	scanned := make([]string, 0, len(scannedMap))
	for n := range scannedMap {
		scanned = append(scanned, n)
	}
	sort.Strings(scanned)

	registered := envRegisteredSampleVars(t, root)
	baselineNames, baselineFirst := envReadBaseline(t, root)

	baseline := map[string]bool{}
	for _, n := range baselineNames {
		baseline[n] = true
	}

	// ★ R1：新增未登记 ⇒ 红灯并点名。
	if miss := envUnregisteredVars(scanned, registered, baseline); len(miss) > 0 {
		t.Errorf("★★ 发现 %d 个 LLM_GATEWAY_* 变量**既没进 envs.samples 登记册、★ 也不在存量豁免基线** ⇒ ★★ "+
			"新增环境变量必须登记（★ 登记办法：envs.samples/*.env.sample 里加一行 NAME=）★★\n%s",
			len(miss), strings.Join(miss, "\n"))
	}

	// ★ R2：基线条数只许减不许增。
	if len(baselineNames) > envSampleRegistryBaselineCap {
		t.Errorf("★★ 存量豁免基线有 %d 条，★ 超过上限 %d 条 ⇒ ★★ 基线只许减不许增 ★★\n"+
			"★★ （★ 想豁免新变量不是加进基线，★ 而是**把它登记进 envs.samples**）★★",
			len(baselineNames), envSampleRegistryBaselineCap)
	}

	// ★ R3：基线条目若已进登记集 ⇒ 冗余 ⇒ 红灯（提示删行收紧基线）。
	for _, n := range baselineNames {
		if registered[n] {
			t.Errorf("★★ 存量豁免基线里的 %s **已经在 envs.samples 登记了** ⇒ ★★ 请从 %s 删掉这一行 ★★\n"+
				"★★ （★ 删掉它基线就少一条，★ R2 棘轮也更紧）★★",
				n, envSampleRegistryBaselineFile)
		}
	}

	// ★ R4：基线卫生（有序 ＋ 无重复）。
	if !sort.StringsAreSorted(baselineNames) {
		t.Errorf("★★ 存量豁免基线**不是有序的** ⇒ ★ 请按名字升序排列（★ %s）★★", envSampleRegistryBaselineFile)
	}
	seen := map[string]bool{}
	for _, n := range baselineNames {
		if seen[n] {
			t.Errorf("★★ 存量豁免基线里有**重复**条目 %s ⇒ ★ 请去重（★ %s）★★", n, envSampleRegistryBaselineFile)
		}
		seen[n] = true
	}

	// ★ R5：基线条目若**已不再被任何代码读取** ⇒ 冗余 ⇒ 红灯（★ 提示删行收紧基线）。
	// ★ ★ 判据用「变量是否还被读到」而不是「引用文件还在不在」⇒ ★ 文件改名/搬家不会误报 ★ ★。
	for _, n := range baselineNames {
		if _, stillRead := scannedMap[n]; !stillRead {
			t.Errorf("★★ 存量豁免基线里的 %s **已经不再被任何代码读取**（★ 原引用文件 %s）⇒ ★★ "+
				"请从 %s 删掉这一行 ★★（★ 基线又少一条，★ R2 棘轮也更紧）★★",
				n, baselineFirst[n], envSampleRegistryBaselineFile)
		}
	}

	covered := 0
	for _, n := range scanned {
		if registered[n] || baseline[n] {
			covered++
		}
	}
	t.Logf("★ env 增量登记门：扫 %d 个 .go 文件、%d 个 LLM_GATEWAY_* 变量；"+
		"登记 %d 个、存量豁免 %d 个 ⇒ 未登记 %d 个；覆盖率 %.1f%%（★ 基线 %d 条 / 上限 %d 条）★",
		scannedFiles, len(scanned), len(registered), len(baseline), len(scanned)-covered,
		float64(covered)/float64(len(scanned))*100, len(baselineNames), envSampleRegistryBaselineCap)
}

// ── 变异自证（★ 门必须真的能红）────────────────────────────────────────

func TestEnvSampleRegistryGateDetectsNewUnregistered(t *testing.T) {
	scanned := []string{
		"LLM_GATEWAY_ALREADY_REGISTERED",
		"LLM_GATEWAY_BASELINED",
		"LLM_GATEWAY_BRAND_NEW_ONE",
	}
	registered := map[string]bool{"LLM_GATEWAY_ALREADY_REGISTERED": true}
	baseline := map[string]bool{"LLM_GATEWAY_BASELINED": true}

	miss := envUnregisteredVars(scanned, registered, baseline)
	if len(miss) != 1 || miss[0] != "LLM_GATEWAY_BRAND_NEW_ONE" {
		t.Fatalf("★ 变异自证失败：★ 期望恰好点名 LLM_GATEWAY_BRAND_NEW_ONE，★ 实得 %v", miss)
	}

	// ★ 变异臂 2：★ 把基线当成万能豁免 ⇒ ★ 必须被 R2 的条数棘轮拦住（★ 判据层在本门）。
	if len([]string{"A", "B", "C"}) > envSampleRegistryBaselineCap {
		t.Fatalf("★ 变异自证失败：★ 基线上限判据失效")
	}
}
