package ursm_test

// §143.20 的文档门：四个物化视图的刷新成本根因、**「换 JOIN 形态」被证伪**、
// **211 侧独立实例普查**、以及 **877 在两个实例上是两个不同版本**。
//
// 背景（runbook §143.20）：
// §143.19 留下两条待拍板（B1 = 4 个 matview 的 REFRESH CONCURRENTLY，占本库
// 慢查询 DB 时间的 25.3%），并把「211 与 252」里的另一半（211）留空。本节两条一起做：
//   ① B1 的根因 = **7 天窗的全量重算**（不是索引、不是 JOIN 形态）；
//   ② 211 是**一个此前未被审计的独立生产实例**（自己的 PG、自己的 llm_gateway、
//      自己的慢查询、自己的迁移水位）。
//
// # 为什么「文档也要有门」
//
// ① **本节最贵的产出是一条阴性结论**：「`LEFT JOIN LATERAL` 对这条热路径
//    **无实质收益**」。阴性结论最容易被后人「顺手优化」推翻 ⇒ 必须钉住
//    **预热后交错 ABAB×4 的读数**与**两组极差完全重叠**这两个事实。
//    ⇒ 而且要钉住**过程里的三个坑**（A/B 两侧都不是现状 / `::text` 误判 /
//    单次读数），否则下一个人会重走一遍并得出一个更大的假收益。
//
// ② **「7 天窗」是语义契约不是性能参数**。视图名带 `7d`、读方（admin 面板 +
//    drift 监控）都按 7 天算 ⇒ 「1 天窗快 8~13.6 倍」这个读数**不能**被当成
//    「改窗就能快」⇒ 门钉住「改窗 = 改语义 = 属主决策」。
//
// ③ **211 与 252 是两个独立的 `llm_gateway` 库**。本仓的 `docs/db-changelog.md`
//    与 `schema_migrations` 是**单实例口径** ⇒ 跨实例会漏。
//    ⇒ 门钉住「877 在两实例上版本不同」这个事实（它同时**验证**了
//    §143.17.4 的判别方法，也暴露了台账缺口）。
//
// ④ **陈旧度预算的算术必须仍成立**（`admin/session_analytics_view_freshness_test.go`
//    钉着 `interval + 实测一轮 < 预算`）⇒ 实测均值从 26 s 涨到 58.8 s 之后，
//    这道门**没有破**（用 max 最坏值仍成立）⇒ **不改门的常量**
//    ⇒ 门钉住「算术仍成立 ⇒ 契约未破」这个结论。

import (
	"os"
	"strings"
	"testing"
)

const runbook14320 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14320 取出 §143.20 这一节的正文。
//
// ★ 与 §143.17/§143.18/§143.19 同款纪律：必须**按节切**再断言
// （本 runbook 有 36,000+ 行，同一个量会出现十几次 ⇒ 整篇断言等于断言不到东西）。
func section14320(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14320)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.20 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.20 的小节标题：§143.20 的结论整节丢了")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// mustContain14320 断言 §143.20 正文里至少含 needle 之一。
func mustContain14320(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.20 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// mustNotAssert14320 是**行级负控**：断言 §143.20 正文里没有「断言某个错误结论」的行。
//
// ★ 为什么不能纯子串匹配（§143.19 已踩过两次）：正文**合法地否定**一个错误推论时，
// 措辞必然包含那个被否掉的短语 ⇒ 纯子串会误伤否定语境。
// ⇒ 命中 needle 的行若**同时**含否定/引用标记则放行；否则判为「把错误结论当结论写了」。
func mustNotAssert14320(t *testing.T, section string, needles []string) {
	t.Helper()
	negMarkers := []string{
		"不能", "不成立", "不可", "没有", "并非", "被否", "不该", "不是",
		"而非", "误判", "无效推理", "错", "证伪", "无收益", "死路", "不成立",
	}
	quoteMarkers := []string{"『", "』", "「", "」", `"`}

	for _, ln := range strings.Split(section, "\n") {
		for _, n := range needles {
			if !strings.Contains(ln, n) {
				continue
			}
			hasNeg := false
			for _, m := range negMarkers {
				if strings.Contains(ln, m) {
					hasNeg = true
					break
				}
			}
			hasQuote := false
			for _, m := range quoteMarkers {
				if strings.Contains(ln, m) {
					hasQuote = true
					break
				}
			}
			if hasNeg || hasQuote {
				continue
			}
			t.Fatalf("§143.20 有一行把 %q 当成了结论（无否定/引用标记）：%s", n, strings.TrimSpace(ln))
		}
	}
}

// TestRunbook14320MatviewRootCauseIsSevenDayWindow 钉住「成本根因 = 7 天窗全量重算」。
//
// ★ 为什么要钉「7 天窗」而不只是钉「59.5 秒」：
// 「59.5 秒」这个数**混了三样东西**（重算源表 / CONCURRENTLY 机制 / 过载排队），
// 在当前负载下不可分离 ⇒ 钉住它只会让人以为「优化到 30 秒」是可达的。
// ⇒ 真正可行动的是「窗内行数」这个变量（130,126 vs 15,868 ⇒ −87.8%）。
func TestRunbook14320MatviewRootCauseIsSevenDayWindow(t *testing.T) {
	s := section14320(t)

	// 决定性 A/B 的读数（7 天窗 vs 1 天窗）。
	mustContain14320(t, s,
		"3,517.948",
		"441.874",
		"8.0×",
		"13.6×",
	)

	// 源数据量（成本 ∝ 窗内行数）。
	mustContain14320(t, s,
		"130,126",
		"15,868",
		"−87.8%",
	)

	// ★ 物化视图本身很小 ⇒ 成本不在物化视图上。
	mustContain14320(t, s,
		"3,568 kB",
		"58,849",
	)

	// ⚠ 「7 天窗是语义契约」必须写出来，否则「改窗」会被当成免费优化。
	mustContain14320(t, s,
		"语义契约",
		"改窗 = 改语义",
	)
}

// TestRunbook14320LateralIsProvenUseless 钉住「换 JOIN 形态 = 死路」这条阴性结论。
//
// ★ 这是本节最贵的产出。阴性结论必须钉住**三样东西**：
//
//	① 交错重复后的读数（不是单次）；② 两组极差重叠（落在噪声带内）；
//	③ **过程里的三个坑**（否则下一个人会重走并得出更大的假收益）。
func TestRunbook14320LateralIsProvenUseless(t *testing.T) {
	s := section14320(t)

	// 交错 ABAB×4 的读数与中位。
	mustContain14320(t, s,
		"94.178",
		"123.859",
		"99.05",
		"107.94",
	)

	// ★ 落在噪声带内的判据（组内极差完全重叠）。
	mustContain14320(t, s,
		"27.4",
		"32.6",
		"噪声带",
	)

	// ★ 过程三坑必须都在场（这是「不要重走一遍」的保险）。
	mustContain14320(t, s,
		"两个都不是现状",  // 坑一：A/B 两侧都不是现状
		"::text",   // 坑二：误判 ::text 废索引
		"先跑的那条是冷的", // 坑三：单次读数
	)

	// 语义等价性（万一将来有人要用）。
	mustContain14320(t, s,
		"1,043,956",
		"129,916",
	)

	// ★ 不得把「LATERAL 有收益」写成结论。
	mustNotAssert14320(t, s, []string{
		"LATERAL 快",
		"LATERAL 更快",
		"改用 LATERAL 即可",
	})
}

// TestRunbook14320TwoElevenIsSeparateInstance 钉住「211 是独立生产实例」。
//
// ★ 为什么要钉：§143.19~§143.20 全程查的是 252 的库（172.16.2.210）。
// 211 有**自己的** PG、**自己的** llm_gateway、**自己的**迁移水位
// ⇒ 本仓的台账是单实例口径 ⇒ 跨实例会漏 ⇒ 这是一条**结构性**发现。
func TestRunbook14320TwoElevenIsSeparateInstance(t *testing.T) {
	s := section14320(t)

	// 211 的实例事实。
	mustContain14320(t, s,
		"acc-pg17",
		"llm_gateway_pre_cutover_20261014",
		"1,169 MB",
	)

	// ★ 观测缺口：扩展装了但没 preload ⇒ 量具不可用。
	mustContain14320(t, s,
		"shared_preload_libraries",
		"must be loaded via",
	)

	// ★ 211 的同一症状（同一 matview 占 57.5% 的慢查块）。
	mustContain14320(t, s,
		"568,908.9",
		"57.5%",
	)

	// ★ 读法必须按运行时分叉（这是给 §143.19 补的订正）。
	mustContain14320(t, s,
		"json-file",
		"conmon",
	)
}

// TestRunbook14320Migration877DiffersAcrossInstances 钉住「同一迁移在两实例上版本不同」。
//
// ★ 这条有双重价值：
//
//	① 它**验证**了 §143.17.4 的判别方法（252 判「新版没跑」、211 判「新版跑过」都成立）；
//	② 它暴露了**台账缺口**（211 上对象在、`schema_migrations` 无 877 那行）。
//
// ⇒ 必须钉住两个实例的**叶实例名形态**与**迁移水位差**。
func TestRunbook14320Migration877DiffersAcrossInstances(t *testing.T) {
	s := section14320(t)

	// 两实例的叶实例名形态（新版 vs 旧版）。
	mustContain14320(t, s,
		"session_turns_2026_10_search_trgm",
		"session_turns_2026_10_search_text_idx",
	)

	// 迁移水位差。
	mustContain14320(t, s,
		"869",
		"882",
		"63 条",
	)

	// ★ 台账缺口这条判词。
	mustContain14320(t, s,
		"对象在、台账无",
	)
}

// TestRunbook14320BudgetArithmeticStillHolds 钉住「陈旧度预算的算术仍成立」。
//
// ★ 这是一条**阴性结论的阴性结论**：实测均值从 26 s 涨到 58.8 s，
// 门里那行「上沿 ~26s」已经过期 —— 但**算术本身没破**（用 max 最坏值仍成立）
// ⇒ ⇒ **不改门的常量**（改了就把「实测基准」变成另一个会漂移的硬编码）。
// ⇒ 门钉住：算术仍成立 + 不改常量 + 注释已过期这件事被登记。
func TestRunbook14320BudgetArithmeticStillHolds(t *testing.T) {
	s := section14320(t)

	// 算术的两端。
	mustContain14320(t, s,
		"1,975.5",
		"2,700",
		"算术仍成立",
	)

	// ★ 必须出现「契约未破 ⇒ 不改门」。
	mustContain14320(t, s,
		"契约未破",
		"不改门",
	)

	// ★ 门注释的基准已过期这件事必须被登记（否则下一个人会以为 26 s 仍然准）。
	mustContain14320(t, s,
		"2.26",
	)
}

// TestRunbook14320HonestBoundaries 钉住本节的诚实边界。
//
// ★ 尤其「不给秒数承诺」：本节的所有读数都混了「重算 / CONCURRENTLY 机制 /
// 过载排队」三样，在当前负载下不可分离 ⇒ 任何「修完能省 N 秒」都是编的。
func TestRunbook14320HonestBoundaries(t *testing.T) {
	s := section14320(t)

	mustContain14320(t, s,
		"本节对生产的写入 = 0",
		"不可分离",
		"load 14.17",
		"未闭合",
	)

	// 不得出现「修完能省 X 秒」这类承诺。
	mustNotAssert14320(t, s, []string{
		"能省",
		"预计可省",
		"优化后耗时",
	})
}
