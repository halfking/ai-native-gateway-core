package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot 走 go.mod 向上找仓根。
//
// ★ 这个 helper 住在**没有 build tag** 的文件里，是 2026-10-05 审计
// 修 integration 门红时挪过来的（原先在 manifest_test.go 里，而那个文件
// 带 `//go:build !integration`）。
//
// 为什么必须挪而不是给调用方加标签：`b5ec9a784`（§9.260）在
// **无标签**的 resolve_test.go 里调用了本函数，于是
// `go vet -tags=integration ./...`（sql/schema 的
// TestIntegrationTaggedTreeCompiles）编译 tagged 树时，调用方在、
// 定义方被标签排除 ⇒ `undefined: repoRoot`。
//
// 两个方向各错一半，且**两个方向都会让某条检查消失**：
//
//	给 resolve_test.go 加 !integration ⇒ 它的用例在 tagged 树里不再被类型检查
//	（那道门正是靠它保证 tagged 树可编译）
//	再复制一份 repoRoot 留在无标签文件 ⇒ 定义散落两处，改一处漏一处
//
// ⇒ 正确形态是「helper 无标签、两个调用方各自保留自己的标签语义」。
// 本 helper 无 DB / 环境依赖（两文件均无 Getenv、无 pgx、无 Skip），
// 所以在 tagged 与非 tagged 两种编译下都成立。
//
// ⚠ 反向的教训：往**无标签**测试文件里加一个只存在于**有标签**文件的
// 符号，编译期只在 `-tags=integration` 下才炸 ⇒ 默认 `go build ./...`
// 与不带 tag 的 `go test` 全绿，只有那道 integration 门会响。
// 那是「默认路径看不见的编译错误」，本文件的存在就是为了让它变可见。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
