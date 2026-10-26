package ursm_test

// §143.32 的文档门：**补上 §143.30.8 的诚实边界缺口** —— 查 211 / 154 / 245 上
// 有没有同名巡检。结论：**211 也跑同一个巡检**（:17），但 **211 缺的是【分区】与【可观测性面】**，
// 而不是一个规模缩小版的 252 问题。
//
// 背景（runbook §143.32）：
// ① 211 的巡检探针每轮报「0 day(s) with data」⇒ **假告警**（表里其实有 7 天数据）；
// ② 根因 = **211 的 `ursm_node_snapshot_min` 是普通表（relkind='r'）不是分区表**
//    ⇒ 探针走 `pg_inherits` + 分区名正则 ⇒ 必然返回 0；
// ③ ⇒⇒⇒ **211 的三件事其实是同一件（表没分区）**：探针假告警 / 留存走 DELETE / 表占库 38%；
// ④ ⇒ **P11 的适用范围被收窄为「只对 252」**（211 的巡检一轮只要 ~4 s）；
// ⑤ ⇒ **211 的可观测性面比 252 窄 11 项，且没有部署台账** ⇒ P8 升级。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **「211 也跑同一个巡检」是此前没有任何文档写过的事实** ⇒ 门钉住它。
// ② ★★★★★ **假告警的现场与反证（「表里其实有 7 天数据」）必须并排钉住** ——
//    只钉「0 day(s)」会被后人当成「211 真的没数据」⇒ 那是**错的**。
// ③ ★★★★★ **「三件事是同一件」+「830 一次解决三件 + 零 Go 改动」** 是本节最有价值的结论 ⇒ 门钉住。
// ④ ★★★★ **P11 的范围收窄**是方向性修正 ⇒ 门钉住「只对 252」。
// ⑤ ★★★ **诚实边界**：★ 「252 慢是争用」只是提示不是证明；★ 211 没有 pgss；
//    ★ 154/245 没查 ⇒ 门钉住这些。

import (
	"os"
	"strings"
	"testing"
)

const runbook14332 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14332 取出 §143.32 这一节的正文。
func section14332(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14332)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.32 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.32 的小节标题：§143.32 的结论整节丢了")
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

// mustContain14332 断言 §143.32 正文里至少含 needle 之一。
func mustContain14332(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.32 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14332SectionHeadingsPresent 钉住 §143.32 的子节骨架齐全。
func TestRunbook14332SectionHeadingsPresent(t *testing.T) {
	s := section14332(t)

	for _, h := range []string{
		"### §143.32.0 结论先行",
		"### §143.32.1 ",
		"### §143.32.2 ",
		"### §143.32.3 ",
		"### §143.32.4 ",
		"### §143.32.5 ",
		"### §143.32.6 ",
		"### §143.32.7 ",
		"### §143.32.8 ",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.32 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14332PatrolExistsOn211 钉住「211 也跑同一个巡检」这个事实。
func TestRunbook14332PatrolExistsOn211(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"/etc/cron.d/llmgw-ursm-snapshot-health",
		"17 * * * * root LLMGW_PG17_CONF=/etc/llmgw/pg17.conf",
		"/opt/llm-gateway-go-211/scripts/ursm-snapshot-health.sh",
		"acc-pg17",
		"psql-acc-pg17.sh",
	)
}

// TestRunbook14332FalseAlarmIsPinned 钉住假告警的现场 **与它的反证**。
//
// ★ 只钉「0 day(s) with data」会被后人当成「211 真的没数据」⇒ 那是错的。
func TestRunbook14332FalseAlarmIsPinned(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"0 day(s) with data",
		"baseline_thin=thin:0d/7d",
		"5,933,760",
		"2026-10-13 17:30",
		"39,494",
		"假告警",
	)
}

// TestRunbook14332RootCauseIsPlainTable 钉住根因：普通表 ⇒ 探针必然返回 0。
func TestRunbook14332RootCauseIsPlainTable(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"relkind='r'",
		"pg_inherits",
		"_[0-9]{8}$",
		"0|0|0",
	)
}

// TestRunbook14332ThreeThingsAreOne 钉住「三件事是同一件」这个核心结论。
func TestRunbook14332ThreeThingsAreOne(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"三件事其实是【同一件】",
		"表没分区",
		"磁盘单调增长",
		"38.0%",
		"2,296 MB",
		"6,047 MB",
		"652,856",
		"35 MB",
	)
}

// TestRunbook14332Migration830FixesAll 钉住「830 一次解决三件 + 零 Go 改动」。
func TestRunbook14332Migration830FixesAll(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"ursm_node_snapshot_min_YYYYMMDD",
		"retention_partition.go",
		"relkind='p'",
		"零 Go 改动",
		"`.skip`",
	)
}

// TestRunbook14332P11ScopeIsNarrowed 钉住 P11 的范围收窄。
func TestRunbook14332P11ScopeIsNarrowed(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"适用范围因此被收窄",
		"只对 252 有收益",
		"5,933,760",
		"27,806,075",
		"~4 s",
	)
}

// TestRunbook14332ObservabilityGapIsRecorded 钉住 211 的可观测性缺口。
func TestRunbook14332ObservabilityGapIsRecorded(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"deployed-252.tsv",
		"无**",
		"**12**",
		"pg_stat_statements",
		"P8",
	)
}

// TestRunbook14332NewProposalsAreRegistered 钉住新增的 P17 / P18 / P19。
func TestRunbook14332NewProposalsAreRegistered(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"**P17（新增）**",
		"**P18（新增）**",
		"**P19（新增）**",
		"需属主显式授权",
	)
}

// TestRunbook14332HonestBoundaries 钉住诚实边界。
func TestRunbook14332HonestBoundaries(t *testing.T) {
	s := section14332(t)

	mustContain14332(t, s,
		"对生产的写入 = 0",
		"生产行为未变",
		"是【提示】不是【证明】",
		"没有 `pg_stat_statements` 的读数可用",
		"没有查 154 / 245",
		"211 的巡检成功率没统计",
	)
}
