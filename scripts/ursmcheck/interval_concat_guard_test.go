package ursm_test

// interval_concat_guard_test.go —— 「interval 由字符串拼接拼出来」的全仓守卫。
//
// AUDIT-GUARD: interval 不得用 `($N || ' days')` 拼出来；一律 make_interval。
//
// # 为什么要有这道门（2026-10-30，12h 审计轮实测）
//
// 形态：SQL 里写 `NOW() - ($1 || ' days')::interval`。
//
// `||` 是字符串拼接 ⇒ ⇒ PG 只能把 `$1` 定型为 **text**
// ⇒ ⇒ 于是 pgx 要把 Go 的 **int** 编码进一个 text 类型的绑定参数
// ⇒ ⇒ pgx v5 找不到 int→text 的编码计划 ⇒ ⇒ 报：
//
//	failed to encode args[0]: unable to encode 7 into text format
//	for text (OID 25): cannot find encode plan
//
// ★★★ **这不是「一直坏着」** ⇒ ★★★ 它是**被一个全局开关打坏的**：
// pgx v5 的默认执行模式就是 `QueryExecModeCacheStatement`（bind 参数），
// 而 `db/db.go` 曾经显式钉 `QueryExecModeSimpleProtocol`
// （SimpleProtocol 把参数在**客户端**内联成字面量 ⇒ `7 || ' days'` 正常）。
// ⇒ ⇒ 2026-10-30 00:27 那笔（`55d8a00a1`）把 statement cache **默认打开**后
// ⇒ ⇒ 全仓所有「绑 int」的 `||` 时间单位站点**同时炸掉**
// ⇒ ⇒ 作者随后只修了 12 处（`e2223d9a6`），**剩下 9 处仍在生产失败**。
//
// ★ 本门把**整类**钉死：不是「再修一处」，而是「以后不许再写」。
//
// ★ 为什么门扫的是**全仓**而不是那几个文件：
// ⇒ ⇒ 「一次手修会漏下一次新增」正是该建门的理由
// ⇒ ⇒ 本仓已有一道同族门（`char_concat_guard_test.go`，守 `"char"` 参与 `||`）
// ⇒ ⇒ 本门是它的姊妹门，同一面、同口径。
//
// ★ 口径边界（有意收窄）：
//   - 只扫**生产** `.go` 文件（跳过 `_test.go` / `vendor/` / `node_modules/`）
//   - 只扫**代码行**（跳过整行注释）⇒ 注释里可以解释「为什么不用这个形态」
//   - 只认**时间单位**（day/hour/minute/second/week/month 及其复数）
//     ⇒ 其它 `||` 拼接（字符串拼接、路径拼接）不归本门管
//
// ★ 正确的写法（唯一允许的形态）：
//
//	NOW() - make_interval(days => $1::int)
//	NOW() - make_interval(hours => $1::int)
//	NOW() - make_interval(mins => $1::int)
//	NOW() - make_interval(secs => $1::int)
//	NOW() - make_interval(weeks => $1::int)
//
// ★★ 语义等价（PG 真库实测）：`'7 days'::interval` ≡ `make_interval(days => 7)`。
//
// ★★ Go 侧必须绑 **int**（不是 `fmt.Sprintf("%d", x)`）⇒
// ⇒ 因为 `make_interval(days => $1::int)` 给 `$1` 的类型上下文是 **int4**
// ⇒ ⇒ 绑 Go int ⇒ pgx 原生编码；绑 Go string ⇒ 反而编不进 int4。
// ⇒ ⇒ 本门扫 SQL 形态；Go 侧的「绑 int」由 §1 的正控用例一起钉。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// intervalConcatRE 是本门的判据：`$N || '<时间单位>'`。
//
// ★ `$N` 只允许数字占位符 ⇒ ⇒ 排掉 `$1::int || ' hours'` 这种
//
//	显式转型写法（它把 $1 定型成 int4 ⇒ 本身是安全的）。
var intervalConcatRE = regexp.MustCompile(
	`\$\d+\s*\|\|\s*'\s*(day|days|hour|hours|minute|minutes|` +
		`second|seconds|week|weeks|month|months)\s*'`)

// intervalSkipDirs 是扫描时跳过的目录。
var intervalSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	"web":          true,
	"web-mobile":   true,
}

// intervalConcatRepoRoot 返回仓根。
func intervalConcatRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("解析仓根失败（需要 git 在 PATH）：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestIntervalConcatGuardPositiveControl 证明判据本身有牙。
//
// ★ 没有正控的门 = 恒绿 = 形同虚设 ⇒ ⇒ 必须先证明正则能抓到东西。
func TestIntervalConcatGuardPositiveControl(t *testing.T) {
	bad := []string{
		"NOW() - ($1 || ' days')::interval",
		"NOW() - ($2 || ' hours')::INTERVAL",
		"now() + ($1 || ' minutes')::interval",
		"($3 || ' seconds')::interval",
	}
	for _, s := range bad {
		if !intervalConcatRE.MatchString(s) {
			t.Fatalf("判据无牙：正控样本未被命中 %q", s)
		}
	}
	good := []string{
		"NOW() - make_interval(days => $1::int)",
		"NOW() - make_interval(hours => $1::int)",
		"NOW() - make_interval(mins => $1::int)",
		"NOW() - make_interval(secs => $1::int)",
		"NOW() - make_interval(weeks => $1::int)",
		"WHERE name || ' suffix' = $1",
	}
	for _, s := range good {
		if intervalConcatRE.MatchString(s) {
			t.Fatalf("判据误伤：合法形态被命中 %q", s)
		}
	}
}

// TestIntervalConcatGuardNoStringBuiltInterval 是主门：
// 全仓生产代码里不得再出现 `$N || '<时间单位>'`。
func TestIntervalConcatGuardNoStringBuiltInterval(t *testing.T) {
	root := intervalConcatRepoRoot(t)
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if intervalSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "scripts/ursmcheck/") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if intervalConcatRE.MatchString(line) {
				hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓失败：%v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("发现 %d 处 interval 仍由字符串拼接拼出 ⇒ "+
			"PG 会把参数定型成 text ⇒ pgx 编不进 Go int ⇒ 生产运行期失败。\n"+
			"★ 改法：make_interval(days => $1::int)（Go 侧绑 int，不是字符串）。\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}
