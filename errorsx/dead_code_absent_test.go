package errorsx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★★ 2026-11-02 第十六轮（F-4 / F-5 / F-9 死代码清理）：这些符号**不得回来**。
//
// ── 为什么需要这道门 ──────────────────────────────────────────────────
//
// 这一批符号在删除前是「**按同口径维护着的**死代码」：第四轮起，仓内每次改
// 归责矩阵都要**同步改一份没人用的分支**，好让「将来接线时不带回头的老口径」。
//
// ★ 那个维护是有代价的，而且代价已经付过一次：第十一轮（F-C）踩的坑正是
// 「回写 kind 必须逐点核对所有按 kind 字符串分档的消费点」——而
// DecideFailover 自己就是**按 kind 字符串分档**的消费点之一
// （它决定扇不扇出探针、扇几个、前端等多久）。
// ⇒ 一个没人用的分支，同样要跟着归责矩阵一次一次改；漏改的那一次，
// 就是「将来接线时带着老口径回来」的那个引信。
//
// ⇒ 删除之后，这道门把「不得回来」写成**可执行的事实**：
// 不是靠注释劝阻，而是任何人把任何一个名字写回生产代码，门当场红。
//
// ── 覆盖范围 ──────────────────────────────────────────────────────────
//
// 扫**整个仓库**的生产 .go 文件（跳过 vendor / .git / testdata），
// 用 go/ast 找**标识符**（注释不算 ⇒ 文档里提到这些名字是允许的，
// 例如 errorsx/failover_policy.go 的删除说明）。
//
// ★ 性能：先做**文本预筛**（文件里根本没有这些子串 ⇒ 不解析），
// 只有命中的文件才进 go/parser ⇒ 全仓扫描在百毫秒级。

// deadCodeRemovedNames 是第十六轮删掉的那些符号。
//
// ★ EnqueueProbe 是**字段名**（ActionDecision / FailoverDecision 上的
// write-only 字段）⇒ 它在生产路径上只被**写**、从不被**读**
// ⇒ 连字段本身都是死的。
var deadCodeRemovedNames = []string{
	"DecideFailover",
	"FailoverDecision",
	"IsUpstreamAuthFailure",
	"dispatchFailureIsCredentialHealthy",
	"reasoningAliasField",
	"EnqueueProbe",
}

// TestRemovedDeadCodeMustNotComeBack 是第十六轮的「不得回来」门。
func TestRemovedDeadCodeMustNotComeBack(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	textMatched := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata", ".audit-validation":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		scanned++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		hit := false
		for _, forbidden := range deadCodeRemovedNames {
			if strings.Contains(text, forbidden) {
				hit = true
				break
			}
		}
		if !hit {
			return nil
		}
		textMatched++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fset := token.NewFileSet()
		parsed, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbidden := range deadCodeRemovedNames {
				if ident.Name == forbidden {
					t.Errorf("%s: 生产代码里出现了已删除的死代码标识符 %q ⇒ "+
						"它必须保持删除（否则又是一份要跟着归责矩阵同步改、却没人用的分支；"+
						"第十一轮的跨闭集回归就是这么来的）", rel, forbidden)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓库失败: %v", err)
	}

	// 恒绿护栏：扫描范围不能是空的（否则这道门会静默通过）。
	if scanned < 1000 {
		t.Fatalf("只扫到 %d 个生产 .go 文件 ⇒ 扫描范围写错了（仓库根目录找错？），门会恒绿", scanned)
	}
	// 恒绿护栏（第二半）：文本预筛必须真的命中过文件，否则「预筛 ⇒ 解析」
	// 这条路径可能根本没被走过（例如预筛条件写错 ⇒ 一律跳过 ⇒ 恒绿）。
	if textMatched == 0 {
		t.Fatal("文本预筛一个文件都没命中 ⇒ 预筛条件写错了 ⇒ 这道门根本没在检查任何东西")
	}
}

// repoRoot 找到仓库根（往上找带 go.mod 的目录）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("找不到仓库根（没有 go.mod）")
	return ""
}
