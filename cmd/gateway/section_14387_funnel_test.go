//go:build !integration

package main

// §143.87 的门：★ 本轮是【库侧漏斗代数轮】⇒ ⇒ 产出是【代数 + 文档登记 + 可复现脚本】。
// ★★★★★★ 这个门要守的是【「代数结论不许被悄悄改回去」】：
//
//	① ★★★★ §143.87 段必须存在（★ 过程可查）
//	② ★★★★ §143.87 段必须登记【关键数字】（★ 669 / 376 / 293 / 480 / 473 / 61.04%）
//	③ ★★★★★ §143.87 段必须写明【「189 次 / 64.5%」不成立】（★ 本轮的推翻对象）
//	④ ★★★★ §143.85 段必须有【订正横幅四】（★ 原文保留 + 就地标注）
//	⑤ ★★★ LESSONS §278 必须存在
//	⑥ ★★★ AGENTS R130 必须存在
//	⑦ ★★★★★ 【脚本常量 ⇔ 文档数字】必须一致（★ 防文档漂移）
//	⑧ ★★★★ 代数脚本必须存在且带【自检入口】
//
// ★★★★★★ 判据⑦ 是本门最值钱的一条：★ 它读 `scripts/audit/funnel_algebra.py` 里的
// `EXPECTED_*` 常量（★ 那些是【本轮实测读数】）⇒ ⇒ 逐个到 runbook 的 §143.87 段里找。
// ★ ⇒ ⇒ ★★ **文档里的数字与脚本里的数字一旦漂移 ⇒ ⇒ 门立刻红** ⇒ ⇒ 于是
// ★ 「散文里的数字没有可对拍性」这个病，★ 被门钉住了。
//
// ★ ★★ 纪律提醒：★ 本门是【登记门】，★ **不是**端到端门（★ 它不连库、不跑真实读链）。
// ★ ★★ 它守的是【结论不许被悄悄改回去】，★ 而【结论本身对不对】由 §143.87.4 的
// ★ 可复现脚本 + 变异台负责。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	section14387Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14387Lessons = "LESSONS.md"
	section14387Agents  = "AGENTS.md"
	section14387Script  = "scripts/audit/funnel_algebra.py"
)

func repoRoot14387(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14387(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14387(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// section14387Segment 返回 runbook 里 §143.87 段的文本（★ 从 §143.87 标题到文件末尾）。
func section14387Segment(t *testing.T) string {
	t.Helper()
	runbook := readRepoFile14387(t, section14387Runbook)
	i := strings.Index(runbook, "### §143.87")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.87 段 —— ★ 本轮的代数推导必须落在仓内（★ 过程可查）")
	}
	return runbook[i:]
}

// TestSection14387SectionExistsAndRegistersKeyNumbers（★ 判据①②）
func TestSection14387SectionExistsAndRegistersKeyNumbers(t *testing.T) {
	seg := section14387Segment(t)
	for _, needle := range []string{
		"669",   // ★ 腿① Δcalls
		"376",   // ★ 腿① Δrows
		"293",   // ★ 读链进入次数
		"480",   // ★ 服务侧同窗落空数（宽窗）
		"473",   // ★ 服务侧同窗落空数（逐秒对齐窗）
		"61.04", // ★ 宽窗穿透率上界
		"61.95", // ★ 逐秒对齐窗穿透率上界
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.87 段里没有 %q —— "+
				"★ ★★ 本轮的结论全是数字 ⇒ ⇒ 数字必须登记在段里"+
				"★ ⇒ ⇒ ★★ 窗口口径（★ 480 vs 473）也必须一起登记（★ 分母不同 ⇒ 上界不同）",
				needle)
		}
	}
}

// TestSection14387RefutesThe189Claim（★ 判据③）
//
// ★ ★★★★★ 本门最重要的一条：★ §143.85.4 写下的「≥189 次 / ≥64.5% 的 L3 调用」
// ★ ★★ 已被本轮推翻 ⇒ ⇒ 推翻的声明必须留在 §143.87 段里 ⇒ ⇒
// ★ ★★ 否则下一轮读文档的人会【只读到那个已被推翻的数字】。
func TestSection14387RefutesThe189Claim(t *testing.T) {
	seg := section14387Segment(t)
	for _, needle := range []string{
		"189",   // ★ 被推翻的那个数
		"64.5",  // ★ 被推翻的那个比例
		"不成立",   // ★ 推翻的措辞
		"自由参数",  // ★ 正确的结论形态（★ 区间而不是点值）
		"结构性上界", // ★ 「61%」的改写后性质
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.87 段里没有 %q —— "+
				"★ ★★ 本轮的核心产出是【推翻 §143.85.4 的一处数字】⇒ ⇒ 推翻的声明"+
				"★ ⇒ ⇒ ★★ 必须与数字一起登记（★ 数字单独留着会被读成有效结论）",
				needle)
		}
	}
	// ★★ 必须同时给【区间】而不是只给一个点值（★ 自由参数的登记形态）。
	if !strings.Contains(seg, "s / 293") {
		t.Error("★ §143.87 段里没有 `s / 293` —— " +
			"★ ★★ 「⓪ 适用面」的正确写法是【s / 293（★ s 未闭合）】" +
			"★ ⇒ ⇒ ★★ 只写「61%」会把它读成【实测值】⇒ ⇒ 而它是【结构性上界】")
	}
}

// TestSection14387BannerFourOnSection14385（★ 判据④）
//
// ★ ★★ 纪律：★ 错误结论**就地加订正横幅 + 保留原文** ⇒ ⇒ 横幅必须挂在【出错的段】上，
// ★ 而不是只写在新段里 ⇒ ⇒ 否则读 §143.85 的人看不到它已被推翻。
func TestSection14387BannerFourOnSection14385(t *testing.T) {
	runbook := readRepoFile14387(t, section14387Runbook)
	i := strings.Index(runbook, "### §143.85")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.85 段")
	}
	j := strings.Index(runbook, "### §143.86")
	if j < 0 {
		t.Fatal("★ runbook 里找不到 §143.86 段")
	}
	seg := runbook[i:j]
	if !strings.Contains(seg, "订正横幅四（2026-10-23 §143.87 轮）") {
		t.Error("★ §143.85 段里没有「订正横幅四（2026-10-23 §143.87 轮）」 —— " +
			"★ ★★ 「错误结论就地加订正横幅 + 保留原文」是本仓的文档纪律" +
			"★ ⇒ ⇒ ★★ 横幅必须挂在【出错的段 §143.85】上（★ 不是只写在新段里）")
	}
	// ★★ 横幅必须明确指向被推翻的那句。
	if !strings.Contains(seg, "≥189 次") {
		t.Error("★ §143.85 段的订正横幅四里没有 `≥189 次` —— " +
			"★ ★★ 横幅必须【逐字指出】被推翻的原文 ⇒ ⇒ 否则读者无法定位")
	}
}

// TestSection14387HandoffSurfacesExist（★ 判据⑤⑥）
func TestSection14387HandoffSurfacesExist(t *testing.T) {
	if !strings.Contains(readRepoFile14387(t, section14387Lessons), "## §278") {
		t.Error("★ LESSONS.md 里没有 §278 —— " +
			"★ ⇒ ⇒ 「相减的两个数必须同总体」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14387(t, section14387Agents), "R130") {
		t.Error("★ AGENTS.md 里没有 R130 —— " +
			"★ ⇒ ⇒ 本轮「自由参数要登记成区间」这条必须落在交接段")
	}
}

// TestSection14387ScriptConstantsMatchDocs（★ 判据⑦）
//
// ★★★★★★ 【门钉形态、脚本钉数字】：★ 本判据读代数脚本里的 `EXPECTED_*` 常量
// ★ （★ 那是本轮实测读数）⇒ ⇒ 逐个到 §143.87 段里找 ⇒ ⇒ 数字漂移 ⇒ ⇒ 门红。
func TestSection14387ScriptConstantsMatchDocs(t *testing.T) {
	script := readRepoFile14387(t, section14387Script)
	seg := section14387Segment(t)

	// ★★ 允许行尾注释（★ 脚本里的常量都带口径说明 ⇒ ⇒ 匹配必须容得下它）。
	re := regexp.MustCompile(`(?m)^(EXPECTED_[A-Z0-9_]+)\s*=\s*(\d+)\s*(#.*)?$`)
	matches := re.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		t.Fatalf("★ %s 里没有 EXPECTED_* 常量 —— "+
			"★ ★★ 判据⑦ 依赖这些常量（★ 它们是【本轮实测读数】）⇒ ⇒ 脚本必须把它们显式写出来",
			section14387Script)
	}
	seen := map[string]bool{}
	for _, m := range matches {
		name, numStr := m[1], m[2]
		n, err := strconv.Atoi(numStr)
		if err != nil {
			t.Fatalf("★ 常量 %s 的值 %q 不是整数：%v", name, numStr, err)
		}
		seen[name] = true
		// ★ 用数字边界匹配 ⇒ ⇒ 避免「16690」里含「669」这种假命中。
		pat := regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(numStr) + `([^0-9]|$)`)
		if !pat.MatchString(seg) {
			t.Errorf("★ 脚本常量 %s = %d 在 §143.87 段里找不到 —— "+
				"★ ★★ 【脚本常量 ⇔ 文档数字】必须一致⇒ ⇒ 文档漂移 ⇒ ⇒ 门红"+
				"★ ⇒ ⇒ ★★ 若这是【有意】改读数 ⇒ ⇒ 请同步改脚本与段（★ 别只改一处）",
				name, n)
		}
	}
	// ★★ 关键常量必须齐（★ 少一个就说明脚本被瘦身了 ⇒ ⇒ 判据会失去覆盖面）。
	for _, need := range []string{
		"EXPECTED_LEG1_CALLS",
		"EXPECTED_LEG1_ROWS",
		"EXPECTED_LEG2_CALLS",
		"EXPECTED_LEG3_CALLS",
		"EXPECTED_L3_CALLS",
		"EXPECTED_SVC_WIDE",
		"EXPECTED_SVC_PRECISE",
	} {
		if !seen[need] {
			t.Errorf("★ %s 里没有常量 %s —— "+
				"★ ★★ 判据⑦ 依赖它（★ 少一个 ⇒ ⇒ 文档漂移检查少覆盖一条）",
				section14387Script, need)
		}
	}
}

// TestSection14387AlgebraScriptHasSelfCheck（★ 判据⑧）
//
// ★★★★ 代数必须【可重跑】：★ 脚本要有自检入口（★ `--self-check`），
// ★ 且不变量清单要覆盖本轮的核心结论。
func TestSection14387AlgebraScriptHasSelfCheck(t *testing.T) {
	script := readRepoFile14387(t, section14387Script)
	for _, needle := range []string{
		"--self-check",
		"INV1", // ★ 漏斗闭合
		"INV3", // ★ 非 v2 腿① 下界 376
		"INV4", // ★ s 的区间
		"INV5", // ★ 非 v2 L3 下界 = 0
		"INV6", // ★ 旧算式 669 − 480 = 189（★ 数字对、语义错）
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("★ %s 里没有 %q —— "+
				"★ ★★ 本轮的代数必须【可重跑对拍】⇒ ⇒ 脚本要有自检入口与不变量清单"+
				"★ ⇒ ⇒ ★★ 否则「代数」只是散文里的推导",
				section14387Script, needle)
		}
	}
}
