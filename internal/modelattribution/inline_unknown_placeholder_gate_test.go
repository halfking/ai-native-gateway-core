package modelattribution

// 内联 "<unknown>" 字面量门（2026-11-24 12h 审计轮补建）。
//
// ★ 为什么需要这道门：internal/modelattr/modelattr.go 的包注释**承诺**了
//   "Every write site must use this constant rather than an inline literal —
//   the regression gate in internal/modelattribution counts the inline
//   literals and goes red when one appears outside this package."
//   但该门此前**并不存在**（包内只有 SQL 链形状棘轮 model_chain_gate_test.go，
//   它只扫 SQL 字符串字面量、不扫 Go 代码字面量）⇒ 承诺虚构 ⇒ 占位符拼法
//   漂移无人拦截（211 实测：embeddings.go 两处内联 "<unknown>" 传给
//   recordRateLimited → ClientModel，正是该缺口下的产物）。
//
// ★ 门语义：生产 Go 代码中，行内字面量 `"<unknown>"` 只允许出现在——
//   ① internal/modelattr 包（哨兵定义处）；
//   ② 同一行含 SQL 归一链关键词（COALESCE / NULLIF —— SQL 字符串内的合法形态，
//      由 model_chain_gate_test.go 的 SQL 链棘轮管辖）；
//   ③ _test.go 文件（测试自由）。
//   其余任何出现 ⇒ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineUnknownPlaceholderLiteralIsCentralized(t *testing.T) {
	roots := []string{"../../domains", "../../internal", "../../maas", "../../admin", "../../cmd"}
	allowSuffix := "_test.go"
	sqlChainMarkers := []string{"COALESCE", "NULLIF"}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "node_modules", ".git":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, allowSuffix) {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(body), "\n") {
				if !strings.Contains(line, `"<unknown>"`) {
					continue
				}
				// 注释行（// 或 /* 块内）只是文档表述，不是代码字面量 ⇒ 跳过。
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
					continue
				}
				inModelattr := strings.Contains(path, "internal/modelattr/")
				if inModelattr {
					continue // 哨兵定义处
				}
				isSQLChain := false
				for _, m := range sqlChainMarkers {
					if strings.Contains(line, m) {
						isSQLChain = true
						break
					}
				}
				if isSQLChain {
					continue // SQL 字符串内的归一链形态
				}
				t.Fatalf("行内 \"<unknown>\" 字面量出现在哨兵包之外（modelattr.UnknownPlaceholder 必须收编）: %s:%d: %s",
					filepath.Clean(path), i+1, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
	}
}
