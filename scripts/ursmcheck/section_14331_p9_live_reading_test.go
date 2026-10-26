package ursm_test

// §143.31 的文档门：**把 §143.28.5 的 P9 做掉了**（抓作业运行时的 `pg_stat_activity`）
// —— 现场读数证明 rollup **在等物理 I/O**（`IO|DataFileRead`），**没有 `Lock`、没有 `Client`**；
// 并据此把 **P11 从「方向」落成「有落点」**（`base_buckets` 每日物化，**零语义风险**）。
//
// 背景（runbook §143.31）：
// §143.28 把「生产那 30 s 里绝大部分不是这条语句的计算 ⇒ 是争用」立成判词，
// 并把「怎么证实」写成一张判别表（IO / Lock / active / Client）。本节按那张表采样，
// 抓到了 rollup 的两条语句 —— **两条都是 `IO|DataFileRead`**。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **P9 的现场读数是本节最硬的证据**：三条 DELETE 采样 + 一条聚合采样
//    ⇒ 门钉住时刻、语句头、`wait_event_type=IO`、`wait_event=DataFileRead` 与 age 序列。
// ② ★★★★ **「无 Lock / 无 Client」是同样重要的负向证据** ⇒ 门钉住它。
// ③ ★★★★ **P11 的「零语义风险」论证是本节最有价值的产出** ⇒ 门钉住它的三个支点：
//    「产出只依赖 (天, slot, 桶) 的行数」/「一天只变一次」/「判定输入逐字相同」。
// ④ ★★★ **CPU 侧竞争者（`stats_ground_truth_gap`）是顺带发现** ⇒ 门钉住它与 P16。
// ⑤ ★★★ **诚实边界**：本节**没有**抓到那一轮 rollup 最终成没成功、**没有**量化
//    `stats_ground_truth_gap`、**没有**实测 P11 的收益 ⇒ 门钉住这些边界。

import (
	"os"
	"strings"
	"testing"
)

const runbook14331 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14331 取出 §143.31 这一节的正文。
func section14331(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14331)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.31 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.31 的小节标题：§143.31 的结论整节丢了")
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

// mustContain14331 断言 §143.31 正文里至少含 needle 之一。
func mustContain14331(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.31 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14331SectionHeadingsPresent 钉住 §143.31 的子节骨架齐全。
func TestRunbook14331SectionHeadingsPresent(t *testing.T) {
	s := section14331(t)

	for _, h := range []string{
		"### §143.31.0 结论先行",
		"### §143.31.1 ",
		"### §143.31.2 ",
		"### §143.31.3 ",
		"### §143.31.4 ",
		"### §143.31.5 ",
		"### §143.31.6 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.31 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14331P9LiveReadingIsPinned 钉住 P9 的现场读数。
//
// ★ 这是本节最硬的证据：**rollup 的两条语句都在等物理 I/O**。
func TestRunbook14331P9LiveReadingIsPinned(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"17:50:36.038",
		"17:50:40.055",
		"17:50:42.076",
		"DELETE FROM stats_usage_daily",
		"WITH latest AS",
		"**IO**",
		"**DataFileRead**",
		"0.11 s",
		"2.12 s",
		"4.12 s",
		"0.91 s",
	)
}

// TestRunbook14331NegativeEvidenceIsRecorded 钉住负向证据。
//
// ★ 「没有 Lock / 没有 Client」与「在等 I/O」同样重要 —— 它排除了另外两个假设。
func TestRunbook14331NegativeEvidenceIsRecorded(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"没有一条是 `Lock`",
		"没有一条是 `Client`",
		"§143.28 的「争用」判断成立",
	)
}

// TestRunbook14331ThirdRulerAgrees 钉住「三把尺子对上」。
//
// ★ DELETE 的 age 序列与 §143.27 实测的 4,818 ms 互相印证 ⇒ 这是独立性的交叉验证。
func TestRunbook14331ThirdRulerAgrees(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"4,818 ms",
		"三把尺子",
		"吻合",
	)
}

// TestRunbook14331JoinOptimizationIsRefuted 钉住「换 JOIN 形态救不了它」。
//
// ★ 聚合语句**一启动就在等 I/O** ⇒ 这是 §143.28 方向性结论的生产现场支持。
func TestRunbook14331JoinOptimizationIsRefuted(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"一上来就在等磁盘",
		"换 JOIN 形态",
		"救不了它",
	)
}

// TestRunbook14331P11HasALandingSpot 钉住 P11 的落点与**零语义风险**论证。
//
// ★ 这是本节最有价值的产出 ⇒ 三个支点必须都在。
func TestRunbook14331P11HasALandingSpot(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"base_buckets",
		"2,600 万行",
		"61%",
		"每天只变一次",
		"判定输入逐字相同",
		"零语义风险",
		"−96%",
	)
}

// TestRunbook14331CpuCompetitorIsRecorded 钉住 CPU 侧竞争者与新增的 P16。
func TestRunbook14331CpuCompetitorIsRecorded(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"stats_ground_truth_gap",
		"bg/routing_health_checks.go:947",
		"并行 worker",
		"**P16（新增）**",
	)
}

// TestRunbook14331HonestBoundaries 钉住诚实边界。
func TestRunbook14331HonestBoundaries(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"估算，未实测",
		"只回答了「它在等什么」",
		"采样只覆盖了 1 次 rollup 运行",
		"漏抓是可能的",
	)
}

// TestRunbook14331P9IsClosed 钉住 P9 关闭。
func TestRunbook14331P9IsClosed(t *testing.T) {
	s := section14331(t)

	mustContain14331(t, s,
		"**P9**",
		"关闭",
		"需属主显式授权",
	)
}
