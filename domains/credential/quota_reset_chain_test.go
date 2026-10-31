package credential

// quota_reset_chain_test.go — A1（quota reset 时长来源链）的门。
//
// 判据（研究方案 §五 A1 验收量具 + 实施要点）：
//	① body 带 reset 时间 ⇒ 恢复点 = 该时间（来源 = body_reset）
//	   （变异：删 parseQuotaResetTimestamp 消费 ⇒ 落到启发式 ⇒ 恢复点漂移 ⇒ 红）；
//	② Retry-After 作为第二级来源被 quota 类消费，信任上限 8d
//	   （变异：删 8d 帽 ⇒ 40d 级 header 挂出月度窗口 ⇒ 红）；
//	③ 两级都无 ⇒ 落既有窗口启发式（行为逐字节不变），来源 = window_heuristic；
//	④ 来源写进 state_reason_detail（[recover_source=...] 后缀）；
//	   detail 为空时保持 nil 语义（不发明 "[recover_source=…]" 空段）。

import (
	"testing"
	"time"
)

func TestQuotaRecoveryBodyResetWins(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	detail := `quota exceeded, resets at 2026-11-02 08:00:00 UTC`
	got := inferQuotaRecoveryAtNow(detail, 0, now)
	want := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if !got.at.Equal(want) {
		t.Fatalf("body reset recovery = %s want %s", got.at, want)
	}
	if got.source != quotaSourceBodyReset {
		t.Fatalf("source = %s want body_reset", got.source)
	}
	// Retry-After 同时在场时 body reset 仍赢（来源链顺序）。
	got2 := inferQuotaRecoveryAtNow(detail, 5*time.Minute, now)
	if !got2.at.Equal(want) || got2.source != quotaSourceBodyReset {
		t.Fatalf("with retry-after: %s/%s want body_reset wins", got2.at, got2.source)
	}
}

func TestQuotaRecoveryBodyResetRelativeDuration(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	got := inferQuotaRecoveryAtNow("resets in 2h 30min", 0, now)
	// 相对时长由 ParseQuotaResetAt 以墙钟 now 解析（包装器固定 time.Now()），
	// 注入时钟对它无效 ⇒ 用墙钟容差断言（恢复点 = 墙钟 + 2h30m，±5min）。
	want := time.Now().Add(2*time.Hour + 30*time.Minute)
	if delta := got.at.Sub(want); delta < -5*time.Minute || delta > 5*time.Minute {
		t.Fatalf("relative reset = %s want ~%s (wall clock ±5m)", got.at, want)
	}
	if got.source != quotaSourceBodyReset {
		t.Fatalf("source = %s want body_reset", got.source)
	}
}

func TestQuotaRecoveryRetryAfterSecondSource(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// body 无词表命中 ⇒ Retry-After 接管。
	got := inferQuotaRecoveryAtNow("upstream quota exhausted", 90*time.Minute, now)
	want := now.Add(90 * time.Minute)
	if !got.at.Equal(want) {
		t.Fatalf("retry-after recovery = %s want %s", got.at, want)
	}
	if got.source != quotaSourceRetryAfter {
		t.Fatalf("source = %s want retry_after", got.source)
	}
}

func TestQuotaRecoveryRetryAfterTrustCap8d(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// > 8d 的 header 被压到 8d（信任上限；40d 级畸形 header 不再挂出月度窗口）。
	got := inferQuotaRecoveryAtNow("quota exhausted", 40*24*time.Hour, now)
	want := now.Add(8 * 24 * time.Hour)
	if !got.at.Equal(want) {
		t.Fatalf("capped recovery = %s want %s (8d trust cap)", got.at, want)
	}
	if got.source != quotaSourceRetryAfter {
		t.Fatalf("source = %s want retry_after", got.source)
	}
}

func TestQuotaRecoveryFallsBackToWindowHeuristic(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// 无词表、无 header ⇒ 既有启发式（普通 detail ⇒ 次日 UTC 零点）。
	got := inferQuotaRecoveryAtNow("upstream quota exhausted", 0, now)
	want := midnightUTC(now.AddDate(0, 0, 1))
	if !got.at.Equal(want) {
		t.Fatalf("heuristic recovery = %s want %s", got.at, want)
	}
	if got.source != quotaSourceWindowHeuristic {
		t.Fatalf("source = %s want window_heuristic", got.source)
	}
	// 5h 窗词表仍走 nextFiveHourBoundary（既有行为，归来源 3）。
	got2 := inferQuotaRecoveryAtNow("five hours limit reached", 0, now)
	if !got2.at.Equal(nextFiveHourBoundary(now)) {
		t.Fatalf("5h window = %s want %s", got2.at, nextFiveHourBoundary(now))
	}
	if got2.source != quotaSourceWindowHeuristic {
		t.Fatalf("5h window source = %s want window_heuristic", got2.source)
	}
}

func TestQuotaRecoverySuffixTagging(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	detail := "quota exceeded"
	rec := inferQuotaRecoveryAtNow(detail, 0, now)
	tagged := quotaRecoverySuffix(detail, rec)
	if tagged == nil {
		t.Fatal("non-empty detail must produce tagged detail")
	}
	if got := *tagged; got != "quota exceeded [recover_source=window_heuristic]" {
		t.Fatalf("tagged detail = %q", got)
	}
	// trimDetail 的 500 字符帽对 tagged detail 仍生效。
	long := make([]byte, 0, 600)
	for i := 0; i < 600; i++ {
		long = append(long, 'x')
	}
	taggedLong := quotaRecoverySuffix(string(long), rec)
	if taggedLong == nil || len(*taggedLong) != 500 {
		t.Fatalf("tagged long detail len = %d want 500 (trim cap intact)", len(*taggedLong))
	}
	// 空 detail ⇒ nil（不发明空段）。
	if got := quotaRecoverySuffix("", rec); got != nil {
		t.Fatalf("empty detail suffix = %v want nil", got)
	}
}

// 回归：inferQuotaRecoverAtNow 既有行为未被本轮改动（ writer_test.go 的
// 既有用例继续成立是主证据，这里钉一条 5h 窗 + 零点组合防漂移）。
func TestInferQuotaRecoverAtNowUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	// 月度词表 ⇒ 下月 1 日零点。
	got := inferQuotaRecoverAtNow("monthly quota exhausted", now)
	want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("monthly = %s want %s", got, want)
	}
}
