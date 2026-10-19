package bg

import (
	"strings"
	"testing"
)

// TestProviderErrorAggregatorStallGuardSkipsCaughtUpCase 是 2026-10-18 24h 审计
// 轮新增的形态门：把 stall 决策的「假阳性」分支钉死。aggregator 水位停摆
// 超过 promote retention（7h）时，旧的实现不论成因一律打 ERROR，但本机实测
// S4 停写 + 仅 mock 流量时 pre_watermark == source_max（aggregator 已追平到
// 源、源没新行），不是「写路径坏」也不是「promote 期间丢行」。这条门只覆盖
// 决策表本身，集成路径（带 SQL 读源）由 integration_test.go 兜底。
func TestProviderErrorAggregatorStallGuardSkipsCaughtUpCase(t *testing.T) {
	// 真停摆：aggregator 落后于源、停摆期间被 promote 的行会丢。
	if got := providerErrorAggregatorClassifyStall(40, 50); got != stallGenuine {
		t.Fatalf("pre_watermark=40, source_max=50 ⇒ stallGenuine, got %v", got)
	}
	// 边界：aggregator 刚好追平。
	if got := providerErrorAggregatorClassifyStall(50, 50); got != stallCaughtUp {
		t.Fatalf("pre_watermark=50, source_max=50 ⇒ stallCaughtUp, got %v", got)
	}
	// 假阳性：aggregator 已超过源的最大 id（源空或聚合器独立推进）。
	if got := providerErrorAggregatorClassifyStall(60, 50); got != stallCaughtUp {
		t.Fatalf("pre_watermark=60, source_max=50 ⇒ stallCaughtUp, got %v", got)
	}
	// 退化：源空、aggregator 也是 0（首 tick 或从未写过）—— 不打 ERROR。
	if got := providerErrorAggregatorClassifyStall(0, 0); got != stallCaughtUp {
		t.Fatalf("pre_watermark=0, source_max=0 ⇒ stallCaughtUp, got %v", got)
	}
	// 退化：源空、aggregator 已超前（人工回填 / migration 627 seeding）—— 不打 ERROR。
	if got := providerErrorAggregatorClassifyStall(1_000_000, 0); got != stallCaughtUp {
		t.Fatalf("pre_watermark=1000000, source_max=0 ⇒ stallCaughtUp, got %v", got)
	}
}

// TestProviderErrorAggregatorStallGuardSourceSQLPinsTable 锁「stall guard 读源」
// 的表名：必须从 staging 源同表读（candidate_failure_logs_hot），否则会被
// promote 出去的 columnar 月分区虚高，pre_watermark < source_max 永远成立，
// stall 永远真——又退回到旧假阳性。表名漂移会让决策功能失能。
func TestProviderErrorAggregatorStallGuardSourceSQLPinsTable(t *testing.T) {
	lower := strings.ToLower(providerErrorAggregatorSourceMaxIDSQL)
	if !strings.Contains(lower, "candidate_failure_logs_hot") {
		t.Fatalf("providerErrorAggregatorSourceMaxIDSQL %q 必须读 candidate_failure_logs_hot，"+
			"否则会被 columnar 月分区虚高、退回到旧假阳性（aggregator 永远落后）。",
			providerErrorAggregatorSourceMaxIDSQL)
	}
	// 锁死防回退：绝不允许读父表 candidate_failure_logs（含 columnar 月分区
	// 虚高 max id）或 unified 视图（含 promote 后已落分区的行）。
	for _, banned := range []string{
		"candidate_failure_logs_unified",
		"candidate_failure_logs WHERE",
		"candidate_failure_logs c",
	} {
		if strings.Contains(lower, banned) {
			t.Fatalf("providerErrorAggregatorSourceMaxIDSQL %q 含禁用形态 %q，"+
				"会引入 promote 虚高或与 staging 源不同源；stall guard 决策失效。",
				providerErrorAggregatorSourceMaxIDSQL, banned)
		}
	}
	// 锁形态：必须用 COALESCE(MAX(...), 0)，否则空表返 NULL 会让 Scan 报错、
	// 退路分支打 ERROR（行为同旧假阳性）。
	if !strings.Contains(providerErrorAggregatorSourceMaxIDSQL, "COALESCE(MAX(aggregation_id), 0)") {
		t.Fatalf("providerErrorAggregatorSourceMaxIDSQL %q 必须用 COALESCE(MAX(aggregation_id), 0)，"+
			"否则空源时 Scan 报错、走退路 ERROR 分支。",
			providerErrorAggregatorSourceMaxIDSQL)
	}
}
