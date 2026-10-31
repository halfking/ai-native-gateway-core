//go:build !integration

package main

// §143.83 的门：把「读侧 ⓪ 的机制」钉成【代码面 + 文档面】双判据。
//
// ★★★★ 为什么这一轮要有这道门：
//
//	★ §143.81~§143.82 把「v2 落空 ⇒ 落回 V1 ⇒ 走完整 4 段链（含 L3）」当成一件事，
//	★ ⇒ 并据此算收益。★ ★★ 本轮读代码发现三处机制事实：
//
//	  ① ★★★ 「v2 落空」【不等于】「会走 L3」⇒ ★ `loadFromDB` 是【命中即止】的链
//	     （★ 腿① `LoadLatestOutbound` → 腿② `LoadChain` → 才到 L3）
//	  ② ★★★★ ★ L3 有【两个调用点】⇒ ⇒ ★★ 方案只改一处 ⇒ ⇒ 收益按比例打折
//	     ⇒ ⇒ ★★ ⇒ 这条【必须用代码判据钉住】⇒ ⇒ 否则下一个人又会只改主路径
//	  ③ ★★★★ 「库侧某条 SQL 的 calls」是【所有调用方】的合计 ⇒ ⇒ 不能当场景计数用
//
// ★★ 这道门要守的：
//
//	① ★★★★ 【代码面】L3（`loadFromLegacyDB`）的非测试调用点【必须 ≥ 2 个】
//	   ⇒ ⇒ 且必须【同时】落在本地链尾与共享路径两个文件里
//	② ★★★ 【文档面】§143.83 段必须登记【两个落点】与【收益重算后的数字】
//	③ ★★ LESSONS §274 与 AGENTS R126 必须存在

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14383Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14383Lessons = "LESSONS.md"
	section14383Agents  = "AGENTS.md"

	// ★★ L3 的两个落点所在的文件（★ 少一个 ⇒ 方案就会漏掉大头）
	section14383LocalChain   = "domains/hooks/compression/session_cache.go"
	section14383SharedPath   = "domains/hooks/compression/session_cache_shared.go"
	section14383LegacyDBFunc = "loadFromLegacyDB"
)

func repoRoot14383(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14383(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14383(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// countNonTestCallSites14383 数某个函数名在非测试文件里的【调用形态】出现次数。
//
// ★★ 只数调用形态（★ 后面紧跟 `(`）⇒ ⇒ 不把【定义处】算进去
//
//	⇒ ⇒ ★ 定义与调用必须分开判（★ 本项目里定义处也是 `func (…) loadFromLegacyDB(`）。
func countNonTestCallSites14383(t *testing.T, fnName string) map[string]int {
	t.Helper()
	root := repoRoot14383(t)
	counts := map[string]int{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		// Windows 上 Rel 返回反斜杠 ⇒ 与下方正斜杠常量永不相等 ⇒
		// 判据全判 0（假红）。归一到正斜杠（仓内先例：internal/clockguard/scan.go）。
		rel = filepath.ToSlash(rel)
		lines := strings.Split(string(raw), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			// ★ 排除定义行（★ `func (…) loadFromLegacyDB(`）
			if strings.HasPrefix(trimmed, "func ") {
				continue
			}
			// ★ 排除注释行
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			idx := strings.Index(line, fnName+"(")
			if idx < 0 {
				continue
			}
			counts[rel]++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历失败：%v", err)
	}
	return counts
}

// TestSection14383L3HasTwoCallSites（★ 判据①）
//
// ★★★★ 【代码面】L3（`loadFromLegacyDB`）必须【有两个非测试调用点】，
//
//	★ 且必须【同时】落在：
//	  ★ 本地链尾：★ `session_cache.go`（★ `loadFromDB` 的末尾）
//	  ★ 共享路径：★ `session_cache_shared.go`（★ legacy 校验分支，★ 直连 L3）
//
// ★★★ 为什么这条要有门（★ 本轮的真实教训）：
//
//	★ §143.81~§143.82 只读了【本地链尾】，★ 并据此认为「落空 ⇒ 必走 L3」。
//	★ ⇒ ⇒ ★★ 结果漏掉了共享路径那条 ⇒ ⇒ ★★ **方案若只改一处 ⇒ ⇒ 收益按比例打折**
//	★ ⇒ ⇒ ★★ **而漏掉一个落点这件事【在文档里看不出来】⇒ ⇒ 只能靠代码判据守住。**
func TestSection14383L3HasTwoCallSites(t *testing.T) {
	counts := countNonTestCallSites14383(t, section14383LegacyDBFunc)

	// ① 两个落点都必须存在
	for _, rel := range []string{section14383LocalChain, section14383SharedPath} {
		if counts[rel] == 0 {
			t.Errorf("★ L3（`%s`）在 %s 里没有调用点 —— "+
				"★ ★★ 本轮实测：★ L3 有【两个】落点（★ 本地链尾 %s + ★ 共享路径 %s）"+
				"★ ⇒ ⇒ ★★ 只改一处 ⇒ ⇒ 收益按比例打折"+
				"★ ⇒ ⇒ ★★ 【两个落点必须都在】⇒ ⇒ 这条由门守住",
				section14383LegacyDBFunc, rel, section14383LocalChain, section14383SharedPath)
		}
	}

	// ② 全仓非测试调用点总数必须 ≥ 2（★ 新增落点时门会提示要更新文档）
	total := 0
	files := make([]string, 0, len(counts))
	for f, c := range counts {
		total += c
		files = append(files, f)
	}
	if total < 2 {
		t.Errorf("★ L3（`%s`）的非测试调用点只有 %d 个（★ 文件：%v）—— "+
			"★ ★★ 本轮实测是两个（★ 本地链尾 + 共享路径）⇒ ⇒ 少一个 ⇒ ⇒ "+
			"★ ★★ 【「落空 ⇒ 必走 L3」这个前提就不成立】⇒ ⇒ 必须重新核对读链",
			section14383LegacyDBFunc, total, files)
	}
}

// TestSection14383RunbookMustRegisterBothLandingPointsAndBenefit（★ 判据②）
//
// ★★★ 【文档面】§143.83 段必须登记【两个落点】与【收益重算后的数字】。
//
//	★ 收益口径本轮从「L3 占现时 85%」上调到【整个读链的 ~95%】⇒ ⇒ ★★ 必须落在仓内，
//	★ 且必须带上【两个落点】这个前置条件 ⇒ ⇒ 否则属主会以为「改一处就行」。
func TestSection14383RunbookMustRegisterBothLandingPointsAndBenefit(t *testing.T) {
	runbook := readRepoFile14383(t, section14383Runbook)
	i := strings.Index(runbook, "### §143.83")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.83 段 —— ★ 本轮的机制修正与收益重算必须落在仓内")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"session_cache_shared.go", // ★ 第二个落点
		"loadFromDB",              // ★ 第一个落点
		"23.8",                    // ★ 收益重算后的分钟/天
		"95%",                     // ★ 收益占读链现时成本的比例
		"命中即止",                    // ★ 机制修正①的关键判词
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.83 段里没有 %q —— "+
				"★ ★★ 本轮的三处机制修正与收益重算必须可复核"+
				"★ ⇒ ⇒ 尤其「两个落点」⇒ ⇒ 漏了它 ⇒ ⇒ 属主会以为改一处就行", needle)
		}
	}
}

// TestSection14383HandoffSurfacesExist（★ 判据③）
func TestSection14383HandoffSurfacesExist(t *testing.T) {
	lessons := readRepoFile14383(t, section14383Lessons)
	if !strings.Contains(lessons, "## §274") {
		t.Error("★ LESSONS.md 里没有 §274 —— " +
			"★ ⇒ ⇒ 「一次落空会触发哪些查询要按代码分支顺序算」与「calls 是所有调用方的合计」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	agents := readRepoFile14383(t, section14383Agents)
	if !strings.Contains(agents, "R126") {
		t.Error("★ AGENTS.md 里没有 R126 —— " +
			"★ ⇒ ⇒ 本轮的三处机制修正与收益重算必须落在交接段（★ 否则下一轮会重走一遍）")
	}
}
