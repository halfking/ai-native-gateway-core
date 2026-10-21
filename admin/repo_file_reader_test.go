package admin

// repo_file_reader_test.go —— admin 包的**仓内文件读取助手**（无 build tag ⇒
// 普通树与 `-tags=integration` 树**都**在）。
//
// ── 为什么它必须没有 build tag（2026-10-22 R82 轮实测）──────────────────────
//
// `readRepoFile` 原来定义在 `admin/session_bodies_source_test.go`，而那个文件带
// `//go:build !integration` ⇒ 在 `-tags=integration` 树下它**不存在**。
// 而 `admin/admin_500_channel_gap_realdb_test.go`（**无** build tag ⇒ 两棵树都编译）
// 在 :281 调用了它 ⇒ ★★ **集成标签树编译不过**：
//
//	vet: admin/admin_500_channel_gap_realdb_test.go:281:9: undefined: readRepoFile
//
// ⇒ 后果不是「某个测试红了」，而是 **`sql/schema` 的集成树编译门
// `TestIntegrationTaggedTreeCompiles` 恒红** ⇒ 整条集成门（含真库腿）都跑不起来。
//
// ⇒ 修法：**把 helper 提到没有 build tag 的文件里**（本文件），并从原文件删掉定义。
//   ⇒ 两个选择对比：
//     ① 把**调用方**打成 `!integration` ⇒ 一行改动，但那个文件里还带着
//        真库腿（TestChannelGapObjectsAreDeliveredOnRealPG 等）⇒ **集成树会少跑几条真库腿**；
//     ② 把 **helper** 提到无 tag 文件（= 本文件）⇒ 两棵树都能编译、腿都不丢。
//   ⇒ 取 ②。
//
// ⚠ 与本文件同包的 `admin/s3_wave4_bodies_sources_test.go`（`!integration`）
//   也在用它 ⇒ 提到无 tag 文件后**两边都**能拿到同一个实现 ⇒ 不会出现两份分叉。

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// readRepoFile 按**仓库根**解析路径（parts 里带包名，如 "admin/session_export.go"）。
//
// ⚠ 第一版我把它写成 `readRepoFile(t, ".", rel)` 与 `readRepoFile(t, "..", "settings", …)`，
// 报 `open /tmp/.../session_export.go` 与 `open /tmp/settings/spec_storage.go`
// —— 因为这个 helper 向上找的是 **go.mod 所在的仓根**，不是包目录。
// 「.」= 仓根，「..」= 仓的父目录。⇒ 量具的路径语义要先确认，再写。
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}
