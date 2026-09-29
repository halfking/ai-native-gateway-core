package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Test_isInterruptionCode_covers_all_MarkInterruptedWithReason_literals —
// 白名单↔发射点差集棘轮（三十七轮审计）。历史两次事故同构：empty_response 族
// （二十轮闭合）与本次 11 码差集（stream_panic/client_write_failed 等）都因
// 「发射点新增码、白名单没跟上」而 failure_detail_code 恒丢、指标恒零。
// 本门扫描仓库非测试 Go 源里的 MarkInterruptedWithReason("<literal>") 字面量，
// 逐个断言 isInterruptionCode 放行。变量发射点静态不可枚举，新增时须手检。
func Test_isInterruptionCode_covers_all_MarkInterruptedWithReason_literals(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	litRe := regexp.MustCompile(`MarkInterruptedWithReason\("([a-z0-9_]+)"\)`)
	seen := map[string]string{} // code → file

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".codegraph", "node_modules", "web", "installer", "docs", "tests", "scripts", "deploy":
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range litRe.FindAllStringSubmatch(string(data), -1) {
			rel, _ := filepath.Rel(repoRoot, path)
			if _, dup := seen[m[1]]; !dup {
				seen[m[1]] = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("scan found zero MarkInterruptedWithReason literals — 扫描根路径或正则失效")
	}
	var missing []string
	for code, file := range seen {
		if !isInterruptionCode(code) {
			missing = append(missing, code+" ("+file+")")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("以下 MarkInterruptedWithReason 字面量不在 isInterruptionCode 白名单（failure_detail_code 会恒丢+指标恒零）:\n  %s",
			strings.Join(missing, "\n  "))
	}
}
