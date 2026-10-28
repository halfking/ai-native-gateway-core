package gocache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirLivesUnderBuildLocal —— ★ 专用缓存目录**必须**在 `.build-local/` 下。
//
// ★ ★★ **为什么这条是硬要求**：★ `.build-local/` 已经在 `.gitignore` 里
// ★ ★★ ⇒ ⇒ 专用缓存**绝不会被提交进仓库** ⇒ ⇒ 而专用缓存里躺着的
// ★ ★★ **几十 GB 的编译产物** ⇒ ⇒ ★★ ⇒ 一旦它进了仓库 ⇒ ⇒ 那是一次
// ★ ★★ **灾难性的提交**（★ 而 `git clean -xfd` 也顺手回收不了它）。
func TestDirLivesUnderBuildLocal(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, "unit")

	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("换算相对路径失败：%v", err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".build-local/") {
		t.Fatalf("★ 专用缓存目录不在 `.build-local/` 下（实际 %s）⇒ ⇒ 可能被提交进仓库", rel)
	}
	if !strings.HasPrefix(filepath.ToSlash(rel), RelDirPrefix) {
		t.Fatalf("★ 专用缓存目录不以 %s 开头（实际 %s）", RelDirPrefix, rel)
	}
}

// TestDifferentPurposesGetDifferentDirs —— ★ 两个用途**不许**共用同一个目录。
//
// ★ ★★ **为什么**：★ 两个用途的缓存**失效节奏不同**（★ 一个可能天天变、另一个
// ★ ★★ 可能常年不变）⇒ ⇒ ★ 共用一个目录 ⇒ ⇒ ★★ 清理其中一个会把另一个的
// ★ ★★ 热缓存一起清掉 ⇒ ⇒ ★★ ⇒ 于是「保留热的」这个意图落空。
func TestDifferentPurposesGetDifferentDirs(t *testing.T) {
	root := t.TempDir()
	if Dir(root, "alpha") == Dir(root, "beta") {
		t.Fatalf("★ 两个用途拿到了同一个目录 ⇒ ⇒ 清一个会连累另一个的热缓存")
	}
	if Dir(root, "alpha") != Dir(root, "alpha") {
		t.Fatalf("★ 同一用途两次拿到不同目录（★ ⇒ ⇒ 调用点自己拼路径了 ⇒ ⇒ 约定就有两份）")
	}
}

// TestEnvOverridesAmbientGoCache —— ★ Env 必须**无条件覆盖**环境里那个 `GOCACHE`。
//
// ★ ★★ **为什么必须无条件**：★ Go `exec` 的语义是「**后出现的同名变量赢**」
// ★ ★★ ⇒ ⇒ ★ 所以把 `GOCACHE` 追加在 `os.Environ()` 的**最后**即可覆盖
// ★ ★★ ⇒ ⇒ ★★ 而如果用「仅当没设时才设」的条件式 ⇒ ⇒ ★★ 用户环境里已经
// ★ ★★ 设了 `GOCACHE` ⇒ ⇒ ★★ 那道门就会**悄悄回落用全局缓存** ⇒ ⇒ ★★
// ★ ★★ ⇒ **门绿着，而隔离根本没生效**（★ 与 r1046 那轮 `envValueLast` 的
// ★ ★★ 同款口径问题）。
func TestEnvOverridesAmbientGoCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "ambient-cache"))

	env := Env(root, "unit")
	got := envValueLast(env, "GOCACHE")
	want := Dir(root, "unit")

	if got != want {
		t.Fatalf("★ Env 覆盖后的 GOCACHE = %s，期望 %s", got, want)
	}
	if got == envValueLast(os.Environ(), "GOCACHE") {
		t.Fatalf("★ 覆盖后的 GOCACHE 与环境里那个相同（%s）⇒ ⇒ 隔离没生效", got)
	}
	// ★★ 必须断言「取最后一个」这个口径本身（★ 否则「取第一个」的实现会拿到
	// ★★ 用户那个全局缓存 ⇒ ⇒ 上面那条断言就变成恒真）。
	if first := envValueFirst(env, "GOCACHE"); first == got {
		t.Fatalf("★ 环境切片里 GOCACHE 只有一个匹配项 ⇒ ⇒ 「取最后一个」这条口径没被覆盖到")
	}
}

// TestValueIsASingleEnvEntry —— ★ Value 必须是一条**完整的环境变量条目**。
func TestValueIsASingleEnvEntry(t *testing.T) {
	root := t.TempDir()
	v := Value(root, "unit")
	if !strings.HasPrefix(v, "GOCACHE=") {
		t.Fatalf("★ Value 的形态不对（%s）⇒ ⇒ 期望形如 `GOCACHE=<dir>`", v)
	}
	if strings.Count(v, "=") != 1 {
		t.Fatalf("★ Value 里出现了多个 `=`（%s）⇒ ⇒ 目录名里不该有 `=`", v)
	}
}

// TestEnsureCreatesDirectory —— ★ Ensure 必须**幂等**地建出目录。
func TestEnsureCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, "unit")
	if err := Ensure(root, "unit"); err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("★ Ensure 之后目录不在（err=%v）", err)
	}
	if err := Ensure(root, "unit"); err != nil {
		t.Fatalf("★ Ensure 不幂等（★ 第二次报错）：%v", err)
	}
}

// TestRepoRootFindsTheCheckout —— ★ RepoRoot 必须问 git 自己 ⇒ ⇒ 拿到仓根。
func TestRepoRootFindsTheCheckout(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Skipf("★ 当前不在 git 仓里（★ 跳过）：%v", err)
	}
	if root == "" {
		t.Fatalf("★ RepoRoot 返回空串")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("★ RepoRoot 返回的路径 %s 下没有 go.mod ⇒ ⇒ 它不是仓根", root)
	}
}

// envValueLast 取环境变量切片里**最后一个**匹配项的值。
func envValueLast(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			val = kv[len(prefix):]
		}
	}
	return val
}

// envValueFirst 取环境变量切片里**第一个**匹配项的值。
func envValueFirst(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}
