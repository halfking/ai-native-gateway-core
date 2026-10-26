package ursm_test

// periodic_review_registry_test.go —— 2026-10-28 R86 轮（周期性复核项登记册的门）
//
// # 这道门要钉住的事
//
// ★ 承接 §19.25.19.10 优先级 2：「全仓无掏空洞」是本线**第一条全量阴性结论**
// ⇒ ⇒ 它同样会被现盘推翻 ⇒ ⇒ 登记成**周期性复核项**（★ 一条命令、~9 分钟）。
//
// ★★★ 但「登记」本身很容易变成一句空话 ⇒ ⇒ ★ 所以这份登记册**自带一道门**：
//
//	① 登记册**必须存在**（★ 不存在 ⇒ 门红）；
//	② 登记项引用的**测试名必须真的存在**（★ 门被改名/删除 ⇒ 登记册过期 ⇒ 门红）
//	   ⇒ ★★ 这一条是「登记册不会悄悄腐烂」的**唯一**保证；
//	③ 每条登记项必须给出**一条命令**（★ 没有命令的复核项不是复核项）。
//
// ★ 诚实边界：★ 本门**不跑**那些命令（★ 那属于「人」的周期性动作，★ 不是每轮门禁的职责）
// ⇒ ⇒ 本门保证的是「**复核入口还在、还指得对**」，★ 而不是「结论此刻仍然成立」。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// periodicReviewFile 是登记册的仓内相对路径。
const periodicReviewFile = "scripts/ursmcheck/periodic-review.md"

// periodicTestNameRe 抽出一条复核命令里的 `-run` 模式。
var periodicTestNameRe = regexp.MustCompile(`-run\s+'([^']+)'|-run\s+"([^"]+)"|-run\s+(\S+)`)

// periodicGoTestNameRe 从 `-run` 模式里抽出测试名（★ 形如 `Foo\|Bar` 的分段）。
var periodicGoTestNameRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// TestPeriodicReviewRegistryIsWellFormed 是【登记册自身的形状判据】。
func TestPeriodicReviewRegistryIsWellFormed(t *testing.T) {
	root := hollowRepoRoot(t)
	path := filepath.Join(root, periodicReviewFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读周期性复核项登记册失败（%s）：%v\n★ 这份登记册是「结论会被现盘推翻」的对冲机制 ⇒ ⇒ 必须存在", path, err)
	}
	text := string(data)

	// ★ ① 必须有表头行（★ 登记表 ⇒ ⇒ 至少要有「复核命令」这一列）。
	if !strings.Contains(text, "复核命令") {
		t.Errorf("%s：找不到「复核命令」列 ⇒ ⇒ 这不是一份可执行的复核登记册", periodicReviewFile)
	}
	// ★ ② 必须有「上次复核」与「上次读数」两列（★ ⇒ ⇒ 复核动作必须留下**可比较的痕迹**）。
	for _, col := range []string{"上次复核", "上次读数"} {
		if !strings.Contains(text, col) {
			t.Errorf("%s：缺少「%s」列 ⇒ ⇒ 复核动作不会被回填 ⇒ ⇒ 登记册会退化成一句承诺", periodicReviewFile, col)
		}
	}
}

// TestPeriodicReviewRegistryCommandsPointAtRealTests 是【登记项不许指向不存在的测试】。
//
// ★★ 这条是「登记册不会悄悄腐烂」的**唯一**保证：
// ★★ 门被改名 / 被删 ⇒ ⇒ 登记册里的复核命令会**静默地**跑 0 条 ⇒ ⇒ 复核人会以为「跑过了」
// ⇒ ⇒ 这里要求：**命令里点名的每个测试名都必须真的存在于仓内**。
func TestPeriodicReviewRegistryCommandsPointAtRealTests(t *testing.T) {
	root := hollowRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, periodicReviewFile))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	text := string(data)

	// ★ 仓内这个包里真实存在的测试名（★ 扫 .go 源文件的 `func TestXxx(`）。
	existing := map[string]bool{}
	pkgFiles, err := filepath.Glob(filepath.Join(root, "scripts", "ursmcheck", "*.go"))
	if err != nil || len(pkgFiles) == 0 {
		t.Fatalf("扫仓内 scripts/ursmcheck/*.go 失败：%v", err)
	}
	testNameRe := regexp.MustCompile(`func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	for _, f := range pkgFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", f, err)
		}
		for _, m := range testNameRe.FindAllStringSubmatch(string(b), -1) {
			existing[m[1]] = true
		}
	}

	// ★ 逐行找登记项（★ 表格行以 `|` 开头且含 `go test`）。
	rows := 0
	for _, ln := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "|") || !strings.Contains(trimmed, "go test") {
			continue
		}
		rows++
		m := periodicTestNameRe.FindStringSubmatch(trimmed)
		if m == nil {
			t.Errorf("%s：登记项没有给出 `-run` 模式 ⇒ ⇒ 「一条命令」的复核项名不副实：%s",
				periodicReviewFile, trimmed)
			continue
		}
		pattern := ""
		for _, g := range m[1:] {
			if g != "" {
				pattern = g
				break
			}
		}
		names := periodicGoTestNameRe.FindAllString(pattern, -1)
		if len(names) == 0 {
			t.Errorf("%s：`-run` 模式 %q 里没有测试名", periodicReviewFile, pattern)
			continue
		}
		// ★★★ `go test -run` 的模式是【**未锚定**的正则】⇒ ⇒ 判据不是「模式里的词
		// ★★★ 恰好等于某个测试名」，而是「**这个模式能匹配到仓内至少一个真实测试**」。
		// ★★★ （★ R86 轮第一版写成了「必须恰好相等」⇒ ⇒ 当场把 `-run 'TestTsvHollowout'`
		// ★★★ 这种**前缀式**的合法写法判成了红 ⇒ ⇒ 自己踩了自己的坑。）
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Errorf("%s：`-run` 模式 %q 不是合法正则：%v", periodicReviewFile, pattern, err)
			continue
		}
		matched := false
		for name := range existing {
			if re.MatchString(name) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s：登记项的 `-run` 模式 %q 匹配不到仓内**任何**真实测试（★ 门被改名/被删 ⇒ ⇒ 这条复核命令会**静默跑 0 条**）",
				periodicReviewFile, pattern)
		}
	}
	if rows == 0 {
		t.Errorf("%s：一条登记项都没有 ⇒ ⇒ 登记册空转", periodicReviewFile)
	}
}

// TestPeriodicReviewRegistryCoversTheHollowoutNegativeConclusion 是【覆盖判据】：
//
// ★ 本线**第一条全量阴性结论**（「全仓无掏空洞」）必须出现在登记册里
// ⇒ ⇒ 否则它就会以「曾经成立」的身份被继续引用（★ 而它依赖的现盘会变）。
func TestPeriodicReviewRegistryCoversTheHollowoutNegativeConclusion(t *testing.T) {
	root := hollowRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, periodicReviewFile))
	if err != nil {
		t.Fatalf("读登记册失败：%v", err)
	}
	text := string(data)
	if !strings.Contains(text, "TestTsvHollowout") {
		t.Errorf("%s：登记册里没有「全仓无掏空洞」的复核项（★ 它是本线第一条全量阴性结论 ⇒ ⇒ 必须登记）",
			periodicReviewFile)
	}
	if !strings.Contains(text, "全仓无掏空洞") {
		t.Errorf("%s：登记册里没有「全仓无掏空洞」这条复核项的名字 ⇒ ⇒ 复核人找不到它", periodicReviewFile)
	}
}
