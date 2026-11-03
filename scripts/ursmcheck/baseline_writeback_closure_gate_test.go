//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// baseline_writeback_closure_gate_test.go —— 「运行时尺的基线写回**不得丢页**、**不得先写后判**」（2026-11-03 12h 审计 D2/D3 引入）。
//
// # 这道门治什么病
//
// 缺口本体（D2，已实测复现）：
//
//	三把运行时尺的 `--update-baseline` 写回都是这个形态：
//	  const nextCounts = {}                       // ★ **从空对象起**
//	  for (const p of report.pages) { … }        // ★ **只遍历本次量到的页**
//	⇒ ⇒ 任何一次以 `--pages=` **子集**运行的写回，会把**未量到的页**的基线条目
//	⇒ ⇒ **静默删除** ⇒ ⇒ 那些页的棘轮约束**凭空消失** ⇒ ⇒ 再也没有任何东西约束它们。
//	★ 而且**门兜不住**：`pagesMatchBaseline` 是**单向**检查
//	  （`measured.every(p => known.has(p))`）⇒ 它只兜「量到基线里没有的页」
//	  ⇒ **兜不住「基线里有、这次没量到的页」** ⇒ ⇒ 删除这一事实**永远不会被判红**。
//	★ 判据：★ **缺失 ≠ 未变**（★ 产物里没出现的条目，不等于产物里没有约束）。
//
// 缺口本体（D3，同族）：
//
//	写回发生在「零页测到」判定**之前** ⇒ 浏览器驱动失败但 `report.pages` 为空时，
//	`--update-baseline` **仍会写出一份空/残缺的基线**，然后才 `exit 3`。
//	⇒ 「没量到」与「已删除」在写回路径上被当成同一件事 ⇒ ⇒ 必须**先判、后写**。
//
// ★ 附带（D2 的第三个面）：基线里的「更新日期」此前用
//   `new Date().toISOString().slice(0, 10)` ⇒ ⇒ 那是 **UTC 日期**
//   ⇒ ⇒ 在 UTC+8 的本地 00:00–08:00 之间跑一次写回，日期**比本地日早一天**
//   ⇒ ⇒ **基线文件自己说它属于昨天**（★ 实证：2026-11-03 本地凌晨写出 `2026-11-02`）。
//   ⇒ 正确形态 = `localDateStamp()`（★ `web/scripts/lib/local-date.mjs`，
//   ★ 与 `web/src/i18n/localDate.ts` **逐字同款**）。
//
// # 判据（形态判据，不是具体值）
//
//	① 写回的初始映射必须**铺旧值**（`{ ...prev… }` 形态）⇒ 只遍历实测页的写法判红；
//	② 「零页测量 ⇒ 不写基线」的判定必须**排在写回之前**（按源码行号对拍）；
//	③ 这三把尺子里**不得**再出现 `toISOString().slice(0, 10)`（UTC 日期形态）。
//
// ★ 变异臂（证明门有牙）：把任一条改回旧形态 ⇒ 门必须红。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// baselineWritebackTools 三把「有基线写回」的运行时尺。
type baselineWritebackTool struct {
	// rel 仓内相对路径
	rel string
	// writeSeedRe 写回初始映射必须匹配「铺旧值」的形态
	writeSeedRe *regexp.Regexp
	// writeBlockRe 写回块的起始形态
	writeBlockRe *regexp.Regexp
	// measuredRe 零页判定的形态
	measuredRe *regexp.Regexp
}

var baselineWritebackTools = []baselineWritebackTool{
	{
		rel:          "web/scripts/design-metric-skeleton-runtime-check.mjs",
		writeSeedRe:  regexp.MustCompile(`const\s+nextCounts\s*=\s*\{\s*\.\.\.\(?prev\.bareMetricCards`),
		writeBlockRe: regexp.MustCompile(`if\s*\(argv\.includes\('--update-baseline'\)\)\s*\{`),
		measuredRe:   regexp.MustCompile(`const\s+measured\s*=\s*report\.pages\.length`),
	},
	{
		rel:          "web/scripts/design-nested-card-runtime-check.mjs",
		writeSeedRe:  regexp.MustCompile(`const\s+nextCounts\s*=\s*\{\s*\.\.\.\(?prevCounts`),
		writeBlockRe: regexp.MustCompile(`if\s*\(wantUpdate\)\s*\{`),
		measuredRe:   regexp.MustCompile(`const\s+measured\s*=\s*report\.pages\.length`),
	},
	{
		rel:          "web/scripts/design-icon-size-runtime-check.mjs",
		writeSeedRe:  regexp.MustCompile(`const\s+zeroSized\s*=\s*\{\s*\.\.\.\(?prev\.zeroSized`),
		writeBlockRe: regexp.MustCompile(`if\s*\(argv\.includes\('--update-baseline'\)\)\s*\{`),
		measuredRe:   regexp.MustCompile(`const\s+measured\s*=\s*report\.pages\.length`),
	},
}

// baselineWritebackRead 读仓内相对路径。
func baselineWritebackRead(t *testing.T, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// TestBaselineWritebackPreservesUnmeasuredPages 判据①②：铺旧值 + 先判后写。
func TestBaselineWritebackPreservesUnmeasuredPages(t *testing.T) {
	for _, tool := range baselineWritebackTools {
		t.Run(tool.rel, func(t *testing.T) {
			src := baselineWritebackRead(t, tool.rel)

			// ① 写回的初始映射必须**铺旧值**。
			if !tool.writeSeedRe.MatchString(src) {
				t.Errorf("★★ %s 的写回**没有铺旧值** ⇒ ★ 它只遍历本次量到的页 ⇒ ⇒ "+
					"任何一次 `--pages=` 子集写回都会**静默删除**未量到的页的基线条目 ⇒ ⇒ "+
					"那些页的棘轮约束凭空消失 ⇒ ⇒ 而页集检查是**单向**的 ⇒ **兜不住** ⇒ ⇒ "+
					"要求形态：`const nextCounts = { ...prev… }`（★ 先铺旧、再按 min 覆盖量到的页）",
					tool.rel)
			}

			// ② 零页判定必须排在写回**之前**。
			m := tool.measuredRe.FindStringIndex(src)
			w := tool.writeBlockRe.FindStringIndex(src)
			if m == nil {
				t.Fatalf("★ %s 里找不到「零页测量」判定 ⇒ 门判红（★ 否则「没量到」与「已删除」分不开）", tool.rel)
			}
			if w == nil {
				t.Fatalf("★ %s 里找不到写回块 ⇒ 门判红（★ 提取器坏了）", tool.rel)
			}
			if m[0] > w[0] {
				t.Errorf("★★ %s 的「零页测量 ⇒ 不写基线」判定排在写回**之后**（判定@%d vs 写回@%d）⇒ ⇒ "+
					"浏览器驱动失败但零页时，`--update-baseline` **仍会写出一份空/残缺基线** ⇒ ⇒ "+
					"「没量到」被当成「已删除」⇒ ⇒ 必须**先判、后写**",
					tool.rel, m[0], w[0])
			}
		})
	}
}

// TestBaselineWritebackUsesLocalDate 判据③：不得再用 UTC 日期形态。
func TestBaselineWritebackUsesLocalDate(t *testing.T) {
	utcRe := regexp.MustCompile(`toISOString\(\)\.slice\(0,\s*10\)`)
	for _, tool := range baselineWritebackTools {
		src := baselineWritebackRead(t, tool.rel)
		if loc := utcRe.FindStringIndex(src); loc != nil {
			t.Errorf("★★ %s 里仍有 `toISOString().slice(0, 10)`（第 %d 字节处）⇒ ⇒ 那是 **UTC 日期** ⇒ ⇒ "+
				"UTC+8 的本地 00:00–08:00 之间跑写回 ⇒ 日期**比本地日早一天** ⇒ ⇒ "+
				"**基线文件自己说它属于昨天** ⇒ ⇒ 要求用 `localDateStamp()`（★ 本地时区）",
				tool.rel, loc[0])
		}
		if !strings.Contains(src, "localDateStamp") {
			t.Errorf("★ %s 里找不到 `localDateStamp` ⇒ 判据形变 ⇒ 门判红", tool.rel)
		}
	}
}

// TestBaselineWritebackLocalDateHelperMatchesI18nHelper 是**两处实现必须逐字同款**的对拍。
//
// ★ 为什么要有这道：i18n 两把尺子用的是 `web/src/i18n/localDate.ts`，
//
//	运行时尺用的是 `web/scripts/lib/local-date.mjs` ⇒ ⇒ 两者**必须同款**，
//	否则「同一个仓里有两个『今天』」⇒ ⇒ 基线的日期字段在不同族之间不可比。
func TestBaselineWritebackLocalDateHelperMatchesI18nHelper(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读 %s 失败：%v", rel, err)
		}
		return string(b)
	}

	mjs := read("web/scripts/lib/local-date.mjs")
	ts := read("web/src/i18n/localDate.ts")

	// ★ 判据是**形态**（逐字同款的几行实现），不是「两个文件里都有 localDateStamp」。
	for _, frag := range []string{
		"getFullYear()",
		"getMonth() + 1",
		"getDate()",
		"padStart(2, '0')",
	} {
		if !strings.Contains(mjs, frag) {
			t.Errorf("★ web/scripts/lib/local-date.mjs 里缺 %q ⇒ 两处实现已不同款 ⇒ 门判红", frag)
		}
		if !strings.Contains(ts, frag) {
			t.Errorf("★ web/src/i18n/localDate.ts 里缺 %q ⇒ 两处实现已不同款 ⇒ 门判红", frag)
		}
	}
}
