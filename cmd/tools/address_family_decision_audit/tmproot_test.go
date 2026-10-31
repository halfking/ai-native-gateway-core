package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot 走 go.mod 向上找仓根。
//
// ★ 这个 helper 住在**没有 build tag** 的文件里：`go vet -tags=integration ./...`
// （sql/schema 的 TestIntegrationTaggedTreeCompiles）会把 tagged 树也编译一遍，
// ⇒ 若定义方被 `//go:build !integration` 排除、调用方没有，tagged 树里就
// 会报 `undefined: repoRoot` ⇒ **默认构建看不见的编译错误**。
// （与 cmd/tools/sql_source_indirection_audit/tmproot_test.go 同一处置。）
//
// 本 helper 无 DB / 环境依赖 ⇒ 在 tagged 与非 tagged 两种编译下都成立。
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
			t.Fatal("go.mod not found above " + thisFile)
		}
		dir = parent
	}
}
