package ursm_test

// §143.28 的文档门：**把 §143.27.7 的 P3 做掉** —— 冷态 A/B 做了，**结论是「不成立」**；
// 并据此**重写 P0 的答案**、**就地订正 §143.27**（订正横幅九）、
// **把方向从「优化单条 join」翻到「减少每小时重复做的工作量」**。
//
// 背景（runbook §143.28）：
// §143.27 判定「V2（dedup 侧加时间范围）冷态更友好」是待测项。本节测了，**证伪了**。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **自我订正要留痕**：§143.27 把「冷态 12,804~26,295 ms / 冷热 5.1×」当成硬读数写进了
//    runbook 与 AGENTS.md ⇒ 若不订正，后人会拿它当「已证实的根因」⇒ 门钉住
//    **订正横幅九** + **原文保留**。
// ② ★★★★★ **离散度是本节的核心证据**：V1 同一形态 2.71×、V2 2.58× ⇒
//    **单点读数没有判别力** ⇒ 门钉住这四个数字（min / 中位 / max / 极差）。
// ③ ★★★★★ **「窗口对调」是决定性的一步**：没有它，第一轮会得出「V2 冷态好 2.8 倍」的错误结论
//    ⇒ 门钉住第二轮的读数（V1 冷/热 = 0.76×）。
// ④ ★★★★ **方向性修正**：既然成本不在单条语句上，「优化 join」不是正确方向 ⇒
//    门钉住 **P1/P2/P6 的分量上升** 与 **P3 关闭 / P4 不再推荐**。
// ⑤ ★★★ **诚实边界**：本节**没有**证明「冷态惩罚不存在」，也**没有**抓到
//    `pg_stat_activity` 现场 ⇒ 门钉住这两条边界。

import (
	"os"
	"strings"
	"testing"
)

const runbook14328 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14328 取出 §143.28 这一节的正文。
func section14328(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14328)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.28 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.28 的小节标题：§143.28 的结论整节丢了")
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

// mustContain14328 断言 §143.28 正文里至少含 needle 之一。
func mustContain14328(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.28 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14328SectionHeadingsPresent 钉住 §143.28 的子节骨架齐全。
func TestRunbook14328SectionHeadingsPresent(t *testing.T) {
	s := section14328(t)

	for _, h := range []string{
		"### §143.28.0 结论先行",
		"### §143.28.1 ",
		"### §143.28.2 ",
		"### §143.28.3 ",
		"### §143.28.4 ",
		"### §143.28.5 ",
		"### §143.28.6 ",
		"### §143.28.7 ",
		"### §143.28.8 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.28 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14328CorrectionBannerNineKeepsOriginal 钉住订正横幅九 + **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
// ★ ★★ **判据必须钉在横幅的「老家」§143.27 里，而不是全文件** ——
//   同一个字符串在别处也会出现（§143.28 的结论先行也引用了它），
//   ⇒ 若按全文件断言，**删掉横幅正文也照样绿**（这正是 §143.26.10 记的那类盲区）。
func TestRunbook14328CorrectionBannerNineKeepsOriginal(t *testing.T) {
	// 横幅住在 §143.27 里（就地订正）⇒ 就在 §143.27 里断言。
	home := section14327(t)

	if !strings.Contains(home, "订正横幅九") {
		t.Fatalf("§143.27 里找不到「订正横幅九」—— §143.27 的「冷态读数」误判没有就地留痕")
	}
	// ★ 被订正的原文必须仍在（§143.27 的「冷态 12,804~26,295 ms」与「冷/热 ≈ 5.1×」）。
	for _, n := range []string{
		"12,804~26,295 ms",
		"冷/热 ≈ 5.1×",
	} {
		if !strings.Contains(home, n) {
			t.Fatalf("§143.27 里被订正的原文 %q 不见了 —— 本仓纪律要求订正时**保留原文**", n)
		}
	}
	// ★ 订正后的判词必须在。
	mustContain14328(t, home,
		"误判",
		"被证伪",
	)
}

// TestRunbook14328DispersionIsTheCoreEvidence 钉住离散度这组读数。
//
// ★ 这是本节的核心：**同一臂、同一形态的极差就有 2.6~2.7 倍** ⇒ 单点读数没有判别力。
func TestRunbook14328DispersionIsTheCoreEvidence(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"3,172",
		"5,209",
		"8,587",
		"2.71×",
		"2,664",
		"4,421",
		"6,868",
		"2.58×",
		"没有判别力",
	)
}

// TestRunbook14328WindowSwapIsDecisive 钉住「窗口对调」这一步。
//
// ★ 缺这一步，第一轮会得出「V2 冷态好 2.8 倍」的错误结论 ⇒ 必须钉住第二轮读数。
func TestRunbook14328WindowSwapIsDecisive(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"窗口对调",
		"0.76×",
		"2.71×",
		"标签",
		"翻转",
	)
}

// TestRunbook14328P0IsRewritten 钉住 P0 的重写版答案。
//
// ★ 「单条语句的常态成本远低于 30 s」是本节的方向性结论 ⇒ 若丢了，后人会继续去优化 join。
func TestRunbook14328P0IsRewritten(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"常态 4,421~5,209 ms",
		"远低于 30 s",
		"被推翻",
		"争用",
		"wait_event_type",
	)
}

// TestRunbook14328DirectionIsFlipped 钉住「方向从优化 join 翻到减少重复工作量」。
func TestRunbook14328DirectionIsFlipped(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"分量上升",
		"分量最高",
		"不再推荐",
		"关闭",
		"减少每小时重复做的工作量",
	)
}

// TestRunbook14328HonestBoundaries 钉住诚实边界。
func TestRunbook14328HonestBoundaries(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"没有证明「冷态惩罚不存在」",
		"没有抓到作业运行时的 `pg_stat_activity` 现场",
		"未写迁移",
	)
}

// TestRunbook14328NextInstrumentIsSpecified 钉住「下一把尺子」是可执行的。
//
// ★ §143.28.5 的判别表是「算 vs 等」的判据 ⇒ 它必须完整（IO / Lock / active / Client）。
func TestRunbook14328NextInstrumentIsSpecified(t *testing.T) {
	s := section14328(t)

	for _, n := range []string{
		"'IO'",
		"'Lock'",
		"Client",
		"在算",
		"在等锁",
	} {
		if !strings.Contains(s, n) {
			t.Fatalf("§143.28.5 的判别表缺 %q —— 「下一把尺子」没有写全，下一轮无从下手", n)
		}
	}
}

// TestRunbook14328P9IsRegistered 钉住新增的 P9（「抓作业运行时的 wait_event」）。
func TestRunbook14328P9IsRegistered(t *testing.T) {
	s := section14328(t)

	mustContain14328(t, s,
		"**P9（新增）**",
		"pg_stat_activity",
		"下一轮的第一件事",
	)
}
