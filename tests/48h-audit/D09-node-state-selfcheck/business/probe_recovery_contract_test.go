// business/probe_recovery_contract_test.go
//
// D09 · business —— 凭据节点探测的恢复及时性契约（2026-11-07 探测时间点规划轮）。
// 方案档：docs/audit/2026-11-07-凭据探测时间点规划与恢复及时性轮.md
//
// B-01 可恢复类（network/timeout/5xx/429）梯子必须被恢复 SLA 削顶（默认 5min）
//      ——「凭据已恢复但状态不及时更正」的时间点下界保证。
// B-02 配额族固定节奏不随 attempt 退化（quota periodic 5m / balance·permanent 2m）。
// B-03 确认 404 从第 2 次起停 6h（model-not-served 长停放；第 1 次仍走普通梯）。
// B-04 配额失败体分类（智谱 1310 周窗口形态）必须落在配额族 —— F4「钉在
//      quota_recover_at+60s」的前置判据。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D09-node-state-selfcheck/business/...

package business

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestBusiness_ProbeRecoverySLA_NetworkClassCapped 钉「恢复发现 ≤ 恢复 SLA」的
// **两档契约**（2026-11-07 探测浪费根修轮修订）：
//   档 1（新失败/中低失败，attempt < sustained 阈值）：网络族下一跳 ≤ 恢复 SLA
//        （默认 300s）——「出错凭据恢复后 5 分钟内被复检」对该档逐字保留。
//   档 2（持续失败，attempt ≥ 阈值）：≤ sustained_failure_recheck（默认 1h）。
//        修订动因＝用户报「正常桶被反复探测浪费资金」：211 实测单相档 1 恒 5m
//        ⇒ ~480 次/天、cf≈1000 仍不停。恢复及时性在档 2 由事件路径承担
//        （quota/availability 翻转 → RearmCredentialFailures 即时回拉；
//        业务成功 → 立即停放），盲探节奏放慢不是恢复感知降级。
func TestBusiness_ProbeRecoverySLA_NetworkClassCapped(t *testing.T) {
	const sla = 300 * time.Second // probe.recovery_max_backoff_seconds 默认值
	threshold := bg.SustainedFailureAttemptThreshold()
	sustained := bg.SustainedFailureRecheck()
	kinds := []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindUpstreamDown, errorsx.KindUpstreamOverloaded, errorsx.KindEmptyResponse,
		errorsx.KindRateLimit, errorsx.KindConcurrent, // 可恢复的供应商侧压力同样封顶
	}
	for _, kind := range kinds {
		for attempt := 1; attempt <= 40; attempt++ {
			d := bg.ProbeBackoffForKind(kind, attempt)
			if attempt < threshold && d > sla {
				t.Errorf("kind=%s attempt=%d backoff=%v > SLA %v — 档 1（阈值以下）恢复发现会被拖过操作员 SLA", kind, attempt, d, sla)
			}
			if attempt >= threshold && d > sustained {
				t.Errorf("kind=%s attempt=%d backoff=%v > sustained 档 %v — 梯子无界", kind, attempt, d, sustained)
			}
			if d <= 0 {
				t.Errorf("kind=%s attempt=%d backoff=%v 必须为正", kind, attempt, d)
			}
		}
	}
}

// TestBusiness_QuotaKindsFixedCadence 钉配额族固定节奏：fixed 策略不随 attempt
// 变化（periodic 5m；balance/permanent 2m）——与 BalanceQuotaProbe 的凭据级
// due 闸（2^n 封顶 1h）分层：节点级节奏恒定，凭据级指数退避。
func TestBusiness_QuotaKindsFixedCadence(t *testing.T) {
	cases := []struct {
		kind errorsx.ErrorKind
		want time.Duration
	}{
		{errorsx.KindQuotaPeriodic, 5 * time.Minute},
		{errorsx.KindQuotaBalance, 2 * time.Minute},
		{errorsx.KindQuotaPermanent, 2 * time.Minute},
		{errorsx.KindQuota, 2 * time.Minute},
	}
	for _, tc := range cases {
		for _, attempt := range []int{1, 3, 7, 20} {
			if got := bg.ProbeBackoffForKind(tc.kind, attempt); got != tc.want {
				t.Errorf("kind=%s attempt=%d = %v, want fixed %v", tc.kind, attempt, got, tc.want)
			}
		}
	}
}

// TestBusiness_Confirmed404ParksAt6h 钉 model-not-served 长停放：第 2 次 404
// 起 = 6h（不再走通用梯），第 1 次 = 普通梯首级（5s），一次性抖动仍快速复验。
func TestBusiness_Confirmed404ParksAt6h(t *testing.T) {
	if got := bg.ProbeBackoffForErrCode("http_404", 2); got != 6*time.Hour {
		t.Errorf("confirmed 404 (attempt 2) = %v, want 6h", got)
	}
	if got := bg.ProbeBackoffForErrCode("http_404", 5); got != 6*time.Hour {
		t.Errorf("confirmed 404 (attempt 5) = %v, want 6h", got)
	}
	if got := bg.ProbeBackoffForErrCode("http_404", 1); got == 6*time.Hour {
		t.Error("first (unconfirmed) 404 must keep the generic ladder, not the 6h park")
	}
}

// TestBusiness_QuotaBodyClassifiesForPin 钉 F4 的前置：智谱 1310（周窗口）
// 形态的 429 体必须分类进配额族（quota_periodic / quota_permanent），
// 否则「钉在 quota_recover_at+60s」永远不会命中。
func TestBusiness_QuotaBodyClassifiesForPin(t *testing.T) {
	bodies := []struct {
		name   string
		status int
		body   string
	}{
		{"zhipu-1310-weekly", 429, `{"error":{"code":"1310","message":"您已达到每周使用上限，将于2026-11-14 01:00重置"}}`},
		{"out-of-quota-en", 429, `{"error":{"message":"You have run out of quota for this week"}}`},
		{"insufficient-balance-403", 403, `{"error":{"code":"INSUFFICIENT_BALANCE","message":"balance is insufficient"}}`},
	}
	for _, b := range bodies {
		kind := errorsx.ClassifyErrorWithBody(b.status, []byte(b.body))
		switch kind {
		case errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent:
			// 命中配额族 = 可被重置点对齐退避钉住
		default:
			t.Errorf("%s: classify = %q, want quota family (F4 pin precondition)", b.name, kind)
		}
	}
	// 反例：普通限流体不是配额（不得钉）
	plain := errorsx.ClassifyErrorWithBody(429, []byte(`{"error":{"message":"too many requests, slow down"}}`))
	if plain == errorsx.KindQuota || plain == errorsx.KindQuotaPeriodic ||
		plain == errorsx.KindQuotaBalance || plain == errorsx.KindQuotaPermanent {
		t.Errorf("plain rate-limit body classified as quota family (%q) — would wrongly pin", plain)
	}
}
