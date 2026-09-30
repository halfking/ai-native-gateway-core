package errdiscard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownCandidates 是本门在当前代码上应当报出的集合。
//
// 门报出的是**候选**不是判决：每条都要人判断「调用方丢弃之后结果被怎么用」。
// 这里逐条钉住，是为了让「新增一条同类」和「误报了一条」都能立刻看出来。
var knownCandidates = []string{
	// admin/work_types.go:167 —— 有意决策。fetchL1Counts 的上抛是**承重**的：
	// 它把「半个 map」换成 nil，使 mergeL1TaskTypes(nil) 走调用方自己文档化的
	// canonical-only 回退；降级成 warn 反而会让半个 map 被当全量用。
	// 复审代理曾把它标为「机制不精确」，复核后判定原修正确。
	"admin/work_types.go",

	// bg/lite_retention_worker.go:69,80 —— 假阳性。`if _, _, _, err := w.RunOnce(ctx);
	// err != nil && !errors.Is(...)` 里的 `_` 是三个删除行数，err **有**被检查
	// 并 warn 记录。本门只认「LHS 上出现 `_`」，分不清「丢的是 err」与
	// 「丢的是另一个返回值」。
	"bg/lite_retention_worker.go",
}

// TestErrDiscard_NoNewDiscardingCallSites 是本门的主断言。
//
// 它要求候选集合**恰好**是已登记的那些：多一条说明代码里出现了新的
// 「上抛打到丢弃错误的调用方」，少一条说明门本身退化了（锚错形状、目录被
// SkipDir、AST 形态没覆盖）——后者正是本门开发过程中连踩三次的坑。
func TestErrDiscard_NoNewDiscardingCallSites(t *testing.T) {
	root := repoRoot(t)
	findings, err := CheckDir(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(findings) == 0 {
		t.Fatalf("CheckDir returned 0 findings. That means the guard is blind, not that the " +
			"repo is clean — the same three authoring bugs (HasPrefix('.') skipping the root, " +
			"checking the wrong side of ':=', IfStmt.Init vs Cond) all produced exactly this " +
			"symptom. Investigate before trusting a green run.")
	}

	allowed := map[string]string{}
	for _, k := range knownCandidates {
		allowed[k] = "registered"
	}

	seen := map[string]bool{}
	for _, f := range findings {
		if _, ok := allowed[f.File]; ok {
			seen[f.File] = true
			continue
		}
		t.Errorf("new discarding call site (register it with a reason if it is intentional): %s", f)
	}
	for k := range allowed {
		if !seen[k] {
			t.Errorf("registered candidate %s no longer reported — either it was fixed "+
				"(remove it from knownCandidates) or the guard lost coverage", k)
		}
	}
}

// TestCheckDir_ScansNonTrivially 钉住「门确实看见了东西」。
//
// 本门开发时三次静默返回 0 条：根目录名是 `../..`，`HasPrefix("..", ".")`
// 为真 ⇒ 整棵树被 SkipDir；`:=` 的 `_` 在 LHS 而我只扫 RHS；终检写在
// `IfStmt.Init` 而我只查 `Cond`。三次都表现为「0 findings，测试全绿」。
func TestCheckDir_ScansNonTrivially(t *testing.T) {
	root := repoRoot(t)
	f, scanned, err := CheckDirCounted(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	// 钉住「扫到了多少」。本门三次静默返回 0 条，其中一次是目录遍历把整棵
	// 树 SkipDir 掉了 —— 而那时主门对 0 条是**沉默通过**的：候选集合恰好
	// 为空，与「仓库干净」无法区分。钉住扫描量才能把这两者分开。
	if scanned < 500 {
		t.Fatalf("guard only scanned %d Go files; a repo-wide pass should see far more. "+
			"A tiny number means the directory walk skipped the tree (e.g. the root's own "+
			"name is '..' and a HasPrefix(name, '.') check skipped it).", scanned)
	}
	t.Logf("scanned %d Go files, %d findings", scanned, len(f))
	if len(f) < 1 {
		t.Fatal("guard scanned the tree but found nothing — the AST shape assumption is likely wrong")
	}
	// 交叉验证：每条 finding 的 file 必须是真实存在的 .go 文件，
	// 且不是本包自己（否则说明它在扫自己的测试）。
	for _, x := range f {
		p := filepath.Join(root, x.File)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("finding references a non-existent file %s", x.File)
		}
		if strings.HasPrefix(x.File, "internal/errdiscard/") {
			t.Errorf("guard reported itself: %s", x.File)
		}
	}
}

func repoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("go.mod not found")
	return ""
}

// TestCheckDir_RelativeRootIsNotSkipped 钉住根目录豁免。
//
// 排除规则是「目录**名**在黑名单里」，而第一版写成
// `strings.HasPrefix(info.Name(), ".")`。传入绝对根时看不出问题（根的名字是
// `llm-gateway-go-5`），但传入**相对**根（如测试里常见的 `../..`，其
// Name() 就是 `..`）时，HasPrefix 为真 ⇒ 整棵树被 SkipDir ⇒ 门静默扫过
// 0 个文件、返回 0 条，且测试全绿。
//
// 这就是本门三次「0 findings 全绿」中的一次，且它逃过了主门——因为候选集合
// 为空时「门瞎了」与「仓库干净」完全同形。
func TestCheckDir_RelativeRootIsNotSkipped(t *testing.T) {
	abs := repoRoot(t)
	rel, err := filepath.Rel(mustGetwd(t), abs)
	if err != nil {
		rel = ".."
	}
	_, absScanned, err := CheckDirCounted(abs)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	_, relScanned, err := CheckDirCounted(rel)
	if err != nil {
		t.Fatalf("rel %q: %v", rel, err)
	}
	if relScanned != absScanned {
		t.Errorf("relative root %q scanned %d files but absolute root scanned %d; "+
			"the root directory itself must be exempt from the skip list", rel, relScanned, absScanned)
	}
	if relScanned < 500 {
		t.Errorf("relative root scanned only %d files — the tree was skipped", relScanned)
	}
}

func mustGetwd(t *testing.T) string {
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
