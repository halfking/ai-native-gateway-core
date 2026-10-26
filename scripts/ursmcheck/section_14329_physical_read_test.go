package ursm_test

// §143.29 的文档门：**换一把不受负载影响的尺子** —— 在 252（load 14 / 4 核）这台机器上，
// 墙钟时间不可用作成本度量（§143.28 实测同一臂同一形态波动 2.71×），
// 改用 **`pg_stat_statements.shared_blks_read / calls`（每次执行的物理块数）** 给成本中心排名。
//
// 背景（runbook §143.29）：
// §143.28 留下的口是「生产那 30 s 里绝大部分不是这条语句的计算 ⇒ 是与同机其它作业的争用」，
// 定位只能靠生产现场采样。本节**没有**去抓现场，而是**换尺子**：把「谁最贵」这个问题
// 从「谁耗时长」换成「谁搬的块多」⇒ 头号成本中心变成了 **ursm 快照健康巡检**
// （每次 2.9 GB 物理读 + 190 s），而 rollup 聚合**排第 11 位**。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **尺子换了 ⇒ 结论换了**：§143.26 把 B1（matview 刷新）排第一、§143.27 把 rollup 排第一
//    ⇒ 这两个排序都建立在**墙钟**上 ⇒ 门钉住「结构性成本排名」这组读数，
//    钉住「rollup 排第 11」这个**方向性反转**。
// ② ★★★★★ **头号成本中心的读数**（374,496 块/次、2.9 GB、190,473 ms、26 calls、74 GB）
//    是本节最贵的结论 ⇒ 后人会拿它定优先级 ⇒ 门钉住这些数字。
// ③ ★★★★ **因果链**（巡检烧 I/O ⇒ rollup 抢不到 I/O ⇒ 被 30 s 杀）目前是**推断不是实测**
//    ⇒ 门钉住「推断」这个定性词 + 钉住「需要一次验证」。
// ④ ★★★★ **未查清的矛盾**（26 次成功 190 s vs 17 次被 30 s 杀）⇒ P10
//    ⇒ 门钉住这个矛盾**仍然存在**（防止后人把它当已解决）。
// ⑤ ★★★ **诚实边界** ⇒ 门钉住「本节对生产的写入 = 0」「本节未写迁移」。

import (
	"os"
	"strings"
	"testing"
)

const runbook14329 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14329 取出 §143.29 这一节的正文。
func section14329(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14329)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.29 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.29 的小节标题：§143.29 的结论整节丢了")
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

// mustContain14329 断言 §143.29 正文里至少含 needle 之一。
func mustContain14329(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.29 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14329SectionHeadingsPresent 钉住 §143.29 的子节骨架齐全。
func TestRunbook14329SectionHeadingsPresent(t *testing.T) {
	s := section14329(t)

	for _, h := range []string{
		"### §143.29.0 结论先行",
		"### §143.29.1 ",
		"### §143.29.2 ",
		"### §143.29.3 ",
		"### §143.29.4 ",
		"### §143.29.5 ",
		"### §143.29.6 ",
		"### §143.29.7 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.29 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14329NewRulerIsExplicit 钉住「换尺子」这件事本身。
//
// ★ 本节的方法论核心：**在超载机器上墙钟时间不可用作成本度量**（波动 2.71×），
//   改用 `shared_blks_read / calls`（物理块/次）⇒ 门钉住这两句与这个式子。
func TestRunbook14329NewRulerIsExplicit(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"shared_blks_read",
		"calls",
		"物理块/次",
		"2.71×",
		"墙钟时间不可用作成本度量",
		"结构性成本",
	)
}

// TestRunbook14329TopCostCenterIsPinned 钉住头号成本中心（ursm 快照健康巡检）的读数。
//
// ★ 这是本节最贵的结论：每次 2.9 GB 物理读 + 190 s ⇒ 它在 30 s 超时下永远跑不完。
func TestRunbook14329TopCostCenterIsPinned(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"374,496",
		"2.9 GB",
		"190,473 ms",
		"9,736,905",
		"76,069.6 MB",
		"ursm_node_snapshot_min",
	)
}

// TestRunbook14329RankingIsStructuralCost 钉住「按物理块/次排名」这张表的关键行。
//
// ★ 特别要钉住 **rollup 排第 11**（9,512 块/次）—— 这是**方向性反转**：
//   §143.26 把 B1 排第一、§143.27 把 rollup 排第一，都建立在墙钟上。
func TestRunbook14329RankingIsStructuralCost(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"59,319",
		"55,563",
		"35,285",
		"16,570",
		"14,761",
		"13,806",
		"9,512",
		"rollup 聚合",
	)
}

// TestRunbook14329RollupIsNotTheCostCenter 钉住「rollup 不是成本中心」这个反转结论。
func TestRunbook14329RollupIsNotTheCostCenter(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"排第 11 位",
		"它本来就不是成本中心",
		"救不了它",
	)
}

// TestRunbook14329CausalChainIsMarkedAsInference 钉住因果链 **且标明是推断**。
//
// ★ ★★ 本仓纪律：**因果推断不能写成实测** ⇒ 门必须同时钉住「因果链」与「推断 / 未实测」。
func TestRunbook14329CausalChainIsMarkedAsInference(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"因果链",
		"因果推断，不是实测",
		"它需要一次验证",
	)
}

// TestRunbook14329UnresolvedContradictionIsRegistered 钉住「26 次成功 vs 17 次被杀」这个矛盾。
//
// ★ 这个矛盾**没有**被查清 ⇒ 门钉住它仍然存在 + P10 是它的出口。
func TestRunbook14329UnresolvedContradictionIsRegistered(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"被 `statement timeout` 杀 17 次",
		"26 次「成功」记录",
		"两条调用路径",
		"**P10（新增）**",
		"本节没有查清",
	)
}

// TestRunbook14329PriorityReorderIsRecorded 钉住对既有结论的**重排**。
//
// ★ 本节的产出不是「新结论」而是「**既有优先级被推翻**」⇒ 门钉住重排的四个方向。
func TestRunbook14329PriorityReorderIsRecorded(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"全局第一优先",
		"升为第二",
		"降到第三",
		"降到第四",
		"撤下",
		"换一把尺子就会换一个答案",
	)
}

// TestRunbook14329RulerComparisonTableIsPresent 钉住「三种排序 + 三把尺子」对照。
//
// ★ 这张表是「为什么会换答案」的完整交代 ⇒ 门钉住三把尺子都在。
func TestRunbook14329RulerComparisonTableIsPresent(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"墙钟总时间",
		"单条语句的墙钟",
		"物理块/次（不受负载影响）",
	)
}

// TestRunbook14329HonestBoundaries 钉住诚实边界。
func TestRunbook14329HonestBoundaries(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"对生产的写入 = 0",
		"未写迁移",
		"生产行为未变",
		"pg_buffercache",
		"它高估了",
	)
}

// TestRunbook14329NewProposalsAreRegistered 钉住新增的 P10~P13。
func TestRunbook14329NewProposalsAreRegistered(t *testing.T) {
	s := section14329(t)

	mustContain14329(t, s,
		"**P10（新增）**",
		"**P11（新增）**",
		"**P12（新增）**",
		"**P13（新增）**",
		"需属主显式授权",
	)
}
