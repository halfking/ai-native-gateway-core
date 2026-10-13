package bg

// auto_index_refresher_s3_wave3_test.go — S3 波 3（2026-10-13，停写切换
// 读侧第二分片）形状钉桩：rollupCredentialModelIndexSQL 的双源化。
//
// 停写后 request_logs_hot 排空，autoroute 候选池的核心指标源若无会话腿，
// 每 5 分钟退化为 half-2 cold-start 基线（success=0.9/p95=1000/分数 50），
// 路由质量静默坍缩。本判据钉三件事：
//  1. half-1 带会话腿（session_turns_hot + 冻结点守卫 + alias 链 offers 匹配）；
//  2. half-2 的反连接带同源会话抑制腿——half-1/half-2 必须同迁，否则基线
//     经 ON CONFLICT 每 5 分钟覆盖真实指标；
//  3. v1 腿保留（写门开 = 生产/回滚期它是唯一流量源）。
//
// 判据纪律：断言驱动包级变量文本（它就是发给 PostgreSQL 的语句本体）。
// 真库语义验证（alias 链匹配率 96.5% vs v1 96.7%、整句 INSERT 可执行、
// 会话腿产出 offers 名形态行）见审计文档
// docs/audit/2026-10-13-s3-wave2-requestlogs-readsource-migration.md 波 3 节。

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

func TestRollupCredentialModelIndex_ServesSessionFamily(t *testing.T) {
	sql := rollupCredentialModelIndexSQL
	// half-1 流量腿与 half-2 抑制腿各一条（count==2：只查在场会被
	// 「删 half-2 腿留 half-1 腿」的变异骗过——那种状态下基线每 5 分钟
	// 覆盖真实指标，是最危险的静默回退）。
	if got := strings.Count(sql, "FROM session_turns_hot t"); got != 2 {
		t.Fatalf("both half-1 traffic leg and half-2 suppression leg must read session_turns_hot (found %d); got:\n%s", got, sql)
	}
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("both the half-1 traffic leg and the half-2 suppression leg must be freeze-guarded (found %d); got:\n%s", got, sql)
	}
	// 会话腿的 offers 匹配必须是 alias 链（canonical 粒度）：真库实测
	// raw_model_name 精确等值只 54%（会话侧是剥前缀归一化名），alias 链
	// 恢复 96.5%。钉住链条形状，防回退成精确等值。
	if !strings.Contains(sql, "mo.canonical_id IN (SELECT ma.canonical_id FROM model_aliases ma") {
		t.Fatalf("session leg must match offers through the alias chain (canonical granularity); got:\n%s", sql)
	}
	// v1 腿保留：写门开（生产/回滚）时它是全部流量。
	if !strings.Contains(sql, "FROM request_logs_hot rl") {
		t.Fatalf("half-1 must keep the v1 request_logs_hot leg for gate-open eras; got:\n%s", sql)
	}
	// half-2 抑制腿：同 alias 链反向（canonical → aliases → lower(t.model)）。
	if !strings.Contains(sql, "lower(t.model) IN (SELECT ma.raw_name FROM model_aliases ma") {
		t.Fatalf("half-2 must suppress baselines via the same alias chain; got:\n%s", sql)
	}
}

// TestRollupModelTaskIndex_StaysV1_RegisteredNotMigrated pins the
// registered decision: auto requests (sys:% sessions) are NOT mirrored into
// the session family (measured 2026-10-13: v1 3,420 auto rows vs 0 session
// rows over 3 days), so this rollup has no session-side source and stays on
// request_logs_hot by design — the comment block must keep documenting that
// S4 fate so nobody "finishes" this migration into a silently-empty table.
func TestRollupModelTaskIndex_StaysV1_RegisteredNotMigrated(t *testing.T) {
	if !strings.Contains(rollupModelTaskIndexSQL, "FROM request_logs_hot rl") {
		t.Fatalf("model_task rollup stays on request_logs_hot by registered decision; got:\n%s", rollupModelTaskIndexSQL)
	}
	src, err := os.ReadFile("auto_index_refresher.go")
	if err != nil {
		t.Fatalf("read auto_index_refresher.go: %v", err)
	}
	if !strings.Contains(string(src), "S4 停写登记（2026-10-13 S3 波 3 实测，不迁）") {
		t.Fatal("the registered-not-migrated decision comment must stay on rollupModelTaskIndex")
	}
}
