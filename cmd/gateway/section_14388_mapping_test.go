//go:build !integration

package main

// §143.88 的门：★ 本轮是【「v2 落空 → 读链进入」映射链轮】⇒ ⇒ 产出是【读码事实 + 代数 + 文档登记】。
// ★★★★★★ 这个门要守的是【结论不许被悄悄改回去】+【关键事实不许被代码悄悄改坏】：
//
//	① ★★★★ §143.88 段必须存在（★ 过程可查）
//	② ★★★★ §143.88 段必须登记【关键数字】（★ 669 / 376 / 180 / 293 / 473）
//	③ ★★★★ §143.88 段必须登记【关键机制事实】（★ F1~F6 的关键词）
//	④ ★★★★★★ 【代码面钉住「腿① 读的是 v2 侧」】—— ★★★ 本门最硬的一条：
//	    ★ 直接读 `domains/session/v2/turn_reader.go` 的 `LoadLatestOutbound` 函数体
//	    ★ ⇒ 断言它同时含 `public.session_bodies_unified` 与 `kind = 'final_full'`
//	    ★ ⇒ ⇒ 腿① 改读别的表 ⇒ ⇒ 门立刻红（★ 变异 M8 实测）
//	    ★ ⇒ ⇒ 「前提 A」不会【悄悄失效】而没人发现
//	⑤ ★★★★ §143.87 段必须有【订正横幅五】
//	⑥ ★★★ LESSONS §282 + AGENTS R132 必须存在
//	⑦ ★★★★ 代数脚本必须有 §143.88 的 3 条新不变量
//	⑧ ★★★★★ 订正必须落在【文档里】：★ 「A / 473」与「上界 100%」与「s 拿不到」
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 代码事实门】，★ **不是**端到端门（★ 它不连库、不跑真实读链）。
// ★ ★★ 它守的是【「结论所依赖的代码事实」不许漂移】，★ 而【结论本身对不对】由 §143.88 的
// ★ 可复现脚本 + 变异台负责。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14388Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14388Lessons = "LESSONS.md"
	section14388Agents  = "AGENTS.md"
	section14388Script  = "scripts/audit/funnel_algebra.py"
	section14388Reader  = "domains/session/v2/turn_reader.go"
)

func repoRoot14388(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14388(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14388(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// funcBody14388 取出某个函数的源码片段（★ 从函数声明到下一个顶层 `func ` 为止）。
func funcBody14388(t *testing.T, rel, sig string) string {
	t.Helper()
	lines := strings.Split(readRepoFile14388(t, rel), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), sig) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("★ %s 里找不到函数 %s", rel, sig)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "func ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// section14388Segment 返回 runbook 里 §143.88 段的文本。
func section14388Segment(t *testing.T) string {
	t.Helper()
	runbook := readRepoFile14388(t, section14388Runbook)
	i := strings.Index(runbook, "### §143.88")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.88 段 —— ★ 本轮的映射链必须落在仓内（★ 过程可查）")
	}
	return runbook[i:]
}

// TestSection14388SectionExistsAndRegistersKeyNumbers（★ 判据①②）
func TestSection14388SectionExistsAndRegistersKeyNumbers(t *testing.T) {
	seg := section14388Segment(t)
	for _, needle := range []string{
		"669", // ★ 读链进入（腿① 调用）
		"376", // ★ 非 v2 来源的读链进入下界
		"180", // ★ v2 落空但没进读链的下界（L1 命中层）
		"293", // ★ 腿① 落空
		"473", // ★ 服务侧 v2 落空（逐秒对齐窗）
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.88 段里没有 %q —— "+
				"★ ★★ 本轮全部是数字 ⇒ ⇒ 数字必须登记在段里"+
				"★ ⇒ ⇒ ★★ ★★ 180 这条是【新量化】（★ L1 命中层）⇒ ⇒ 尤其不能丢",
				needle)
		}
	}
}

// TestSection14388SectionRegistersMechanismFacts（★ 判据③）
//
// ★ ★★ 六条代码级事实（F1~F6）必须登记在段里 —— ★ 因为它们是【全部结论的地基】。
func TestSection14388SectionRegistersMechanismFacts(t *testing.T) {
	seg := section14388Segment(t)
	for _, needle := range []string{
		"session_bodies_unified", // ★ 腿① 读的表（v2 侧）
		"final_full",             // ★ 腿① 的 kind
		"HasState",               // ★ 「v2 落空」的判定 API
		"getLocal",               // ★ L1 命中那一层
		"GetOrLoad",              // ★ v2 落空后的必经调用
		"L1",                     // ★ 缓存层名
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.88 段里没有 %q —— "+
				"★ ★★ F1~F6 是【全部结论的地基】⇒ ⇒ 必须在段里可查"+
				"★ ⇒ ⇒ ★★ 少了任何一条，★ 下面的数字就失去依据",
				needle)
		}
	}
}

// TestSection14388LegOneReadsV2SideInCode（★ 判据④）—— ★★★ 本门最硬的一条
//
// ★★★★★★ 【代码面钉住「腿① 读的是 v2 侧」】—— ★★★
// ★ 本轮的决定性证据：★ 腿①（与腿②）读 `public.session_bodies_unified` 的 `kind='final_full'`
// ★ ⇒ ⇒ 与「v2 落空」（★ `HasState == false`）是【同一侧】
// ★ ⇒ ⇒ 前提 A（★ v2 落空 ⇒ 腿① 必落空）成立
// ★ ⇒ ⇒ **若有人把腿① 改读别的表 ⇒ ⇒ 这条前提【立即失效】⇒ ⇒ 门必须红。**
func TestSection14388LegOneReadsV2SideInCode(t *testing.T) {
	body := funcBody14388(t, section14388Reader, "func (r *TurnReader) LoadLatestOutbound(")
	for _, needle := range []string{
		"public.session_bodies_unified",
		"kind = 'final_full'",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("★ %s 的 `LoadLatestOutbound` 函数体里没有 %q —— "+
				"★ ★★★★ §143.88.2 的决定性证据就是它：★ 腿① 读的是【v2 侧】的"+
				"★ ⇒ ⇒ 「v2 落空 ⇒ 腿① 必落空」⇒ ⇒ 376 下界才成立"+
				"★ ⇒ ⇒ ★★ 若腿① 改读 V1 表 ⇒ ⇒ 前提 A 立即失效 ⇒ ⇒ 门必须红（★ %s）",
				section14388Reader, needle, section14388Reader)
		}
	}
}

// TestSection14388BannerFiveOnSection14387（★ 判据⑤）
//
// ★ ★★ 纪律：★ 订正必须【就地落在被订正的段上】⇒ ⇒ 否则读 §143.87 的人看不到它已被改。
func TestSection14388BannerFiveOnSection14387(t *testing.T) {
	runbook := readRepoFile14388(t, section14388Runbook)
	i := strings.Index(runbook, "### §143.87")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.87 段")
	}
	j := strings.Index(runbook, "### §143.88")
	if j < 0 {
		t.Fatal("★ runbook 里找不到 §143.88 段")
	}
	seg := runbook[i:j]
	if !strings.Contains(seg, "订正横幅五（2026-10-23 §143.88 轮）") {
		t.Error("★ §143.87 段里没有「订正横幅五（2026-10-23 §143.88 轮）」 —— " +
			"★ ★★ 本轮对 §143.87 做了三处升级/订正（★ 前提 A 已证 / ★ ⓪ 适用面口径 / ★ 「一次同时拿到 s 与收益」）" +
			"★ ⇒ ⇒ ★★ 订正必须【就地挂在被订正的段上】（★ 而不是只写在新段里）")
	}
	// ★★ 横幅必须逐字点到被订正的内容。
	for _, needle := range []string{"376", "A / 473", "s"} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.87 段的订正横幅五里没有 %q —— "+
				"★ ★★ 横幅必须【逐字指出】被订正的对象 ⇒ ⇒ 否则读者无法定位",
				needle)
		}
	}
}

// TestSection14388HandoffSurfacesExist（★ 判据⑥）
func TestSection14388HandoffSurfacesExist(t *testing.T) {
	if !strings.Contains(readRepoFile14388(t, section14388Lessons), "## §282") {
		t.Error("★ LESSONS.md 里没有 §282 —— " +
			"★ ⇒ ⇒ 「服务端事件数与库端查询数之间可能隔着若干层缓存短路」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14388(t, section14388Agents), "R132") {
		t.Error("★ AGENTS.md 里没有 R132 —— " +
			"★ ⇒ ⇒ 本轮「上界 ≠ 实际占比」这条必须落在交接段")
	}
}

// TestSection14388AlgebraScriptHasNewInvariants（★ 判据⑦）
func TestSection14388AlgebraScriptHasNewInvariants(t *testing.T) {
	script := readRepoFile14388(t, section14388Script)
	for _, needle := range []string{
		"EXPECTED_L1HIT_LOWER = 180",
		"EXPECTED_NON_V2_LEG1_LOWER = 376",
		"INV9",
		"INV10",
		"INV11",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("★ %s 里没有 %q —— "+
				"★ ★★ 本轮新增了 3 条不变量（★ 非 v2 下界 / ★ L1 命中层 / ★ 前提 A 的代数式）"+
				"★ ⇒ ⇒ ★★ 少了它们 ⇒ ⇒ 「180」与「376」就只是散文里的数字",
				section14388Script, needle)
		}
	}
}

// TestSection14388CorrectionsAreRegistered（★ 判据⑧）
//
// ★★★★★ 【订正必须落在文档里】：★ 本轮改了两个口径 ——
// ★ ★★ ① ⓪ 适用面 ＝ **A / 473**（★ 而 A 的上界是 100%、★ 不是 61%）
// ★ ★★ ② 「一次同时拿到 s 与收益」【不成立】⇒ ⇒ s **拿不到**
func TestSection14388CorrectionsAreRegistered(t *testing.T) {
	seg := section14388Segment(t)
	for _, needle := range []string{
		"A / 473", // ★ 正确口径
		"100%",    // ★ A 的上界
		"拿不到",     // ★ s 的诚实结论
		"跳过日志",    // ★ A 的精确计数来源
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.88 段里没有 %q —— "+
				"★ ★★ 本轮【订正】了两个口径（★ ⓪ 适用面 ＝ A/473、★ s 拿不到）"+
				"★ ⇒ ⇒ ★★ 订正必须落在文档里 ⇒ ⇒ 否则下一轮会继续用「61%%」当收益占比",
				needle)
		}
	}
}
