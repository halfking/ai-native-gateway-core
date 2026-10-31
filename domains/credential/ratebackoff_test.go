package credential

// ratebackoff_test.go — A2（429 累计升级）的门。
//
// # 这道门守住哪块缺口（判据 = 研究方案 §五 A2 的验收量具）
//
//	① 单次 429 冷却 = 3min 不变（变异：把首罚改 0 或改固定 30min ⇒ 红）；
//	② 连续第 2 次 = 6min、第 3 次 = 12min、第 4 次 = 24min、第 5 次起 = 30min
//	   封顶（变异：删升级 ⇒ 红）；
//	③ 遗忘窗（30min）后回 3min（变异：删遗忘窗 ⇒ 红）；
//	④ Retry-After 信任上限 1h（超过 1h 的 429 Retry-After 被压到 1h；
//	   ≤1h 照用且**不消耗计数推进判定**——vendor 明说了等待时长时，
//	   升级退避没有发言权）。
//
// 红线对照：429 不进断路器（breaker.go Wave 1 A4）——本门不触碰 breaker，
// 升级只作用于 writeModelLevelFailureOnly 写出的绑定级冷却。
//
// 半开探针豁免（判据④之外的第 4 条量具）：HALF_OPEN 探针撞 429 不加计数
// ——探针写路径（SetCredentialUnavailable / writeProtocolCredentialStateOnError
// 的 DispatchAttempt 分流）不经过 WriteOnError 的 KindRateLimit 分支，
// 由 writer_wiring 门断言（见文件尾）。

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// errorsxKindRateLimitForTest 让默认路径对拍测试不用再 import 两次同包常量。
func errorsxKindRateLimitForTest() errorsx.ErrorKind { return errorsx.KindRateLimit }

// newTestRateBackoff 造一个可控时钟的退避器，并装成进程级默认。
//
// ★ 退避器有状态 ⇒ 每个用例必须重置进程级默认（与 newTestSlowRest 同因），
// 否则"首次 3min"在上一个用例的残留计数下会变成"升级 6min"。
func newTestRateBackoff(t *testing.T) (*RateBackoffTracker, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	tracker := NewRateBackoffTracker()
	tracker.SetClock(func() time.Time { return clock })
	prev := defaultRateBackoff
	defaultRateBackoff = tracker
	t.Cleanup(func() { defaultRateBackoff = prev })
	return tracker, &clock
}

// 判据①：单次 429 冷却 = 3min（不变）。
func TestRateBackoffFirstHitIsThreeMinutes(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	d := tracker.Observe(RateBackoffKey(7, "gpt-x"))
	if d.Duration != RateBackoffBase {
		t.Fatalf("first 429 cooling = %s want %s (3min baseline unchanged)", d.Duration, RateBackoffBase)
	}
	if d.Escalated {
		t.Fatal("first 429 must not be marked escalated")
	}
	if d.Count != 1 {
		t.Fatalf("first 429 count = %d want 1", d.Count)
	}
}

// 判据②：连续升级 6/12/24/30 封顶。
func TestRateBackoffEscalates(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	key := RateBackoffKey(9, "m")
	want := []time.Duration{
		3 * time.Minute,  // n=1 基线
		6 * time.Minute,  // n=2
		12 * time.Minute, // n=3
		24 * time.Minute, // n=4
		30 * time.Minute, // n=5 封顶
		30 * time.Minute, // n=6 仍封顶
	}
	for i, w := range want {
		got := tracker.Observe(key)
		if got.Duration != w {
			t.Fatalf("hit #%d cooling = %s want %s", i+1, got.Duration, w)
		}
		if i >= 1 && !got.Escalated {
			t.Fatalf("hit #%d must be escalated", i+1)
		}
	}
}

// 判据③：遗忘窗后回 3min。
func TestRateBackoffForgetWindowResets(t *testing.T) {
	tracker, clock := newTestRateBackoff(t)
	key := RateBackoffKey(11, "m")
	// 首罚 3min 基线；再犯 ⇒ 升级到 6min。
	if got := tracker.Observe(key); got.Duration != RateBackoffBase {
		t.Fatalf("first 429 cooling = %s want %s", got.Duration, RateBackoffBase)
	}
	if got := tracker.Observe(key); got.Duration != 6*time.Minute {
		t.Fatalf("second 429 cooling = %s want 6m", got.Duration)
	}
	// 遗忘窗内再犯 ⇒ 继续升级（12min）。
	*clock = clock.Add(29 * time.Minute)
	if got := tracker.Observe(key); got.Duration != 12*time.Minute {
		t.Fatalf("in-window third 429 cooling = %s want 12m", got.Duration)
	}
	// 超过遗忘窗 ⇒ 计数归零，回 3min 基线。
	*clock = clock.Add(31 * time.Minute)
	got := tracker.Observe(key)
	if got.Duration != RateBackoffBase {
		t.Fatalf("post-forget 429 cooling = %s want %s", got.Duration, RateBackoffBase)
	}
	if got.Count != 1 || got.Escalated {
		t.Fatalf("post-forget count=%d escalated=%v want 1/false", got.Count, got.Escalated)
	}
}

// 判据④：Retry-After ≤1h 照用且不消耗计数；>1h 压到 1h。
func TestRateLimitedCoolingHonorsRetryAfterWithCap(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	key := RateBackoffKey(13, "m")

	// 先制造连败计数（首罚 3min + 升级态 6min）。
	if got := tracker.Observe(key); got.Duration != RateBackoffBase {
		t.Fatalf("precondition: first 429 = %s want %s", got.Duration, RateBackoffBase)
	}
	if got := tracker.Observe(key); got.Duration != 6*time.Minute {
		t.Fatalf("precondition: second 429 = %s want 6m", got.Duration)
	}
	// vendor 说等 90s ⇒ 信 vendor（90s < 6min 升级值也照用）。
	if got := rateLimitedCoolingDuration(90*time.Second, tracker, key); got != 90*time.Second {
		t.Fatalf("retry-after 90s cooling = %s want 90s (vendor hint wins over escalation)", got)
	}
	// 计数未被消耗（vendor 说了等待时长 ⇒ 升级退避没有发言权）。
	if got := rateLimitedCoolingDuration(90*time.Second, tracker, key); got != 90*time.Second {
		t.Fatalf("repeat 90s = %s want 90s (count must not advance via retry-after path)", got)
	}
	// 1h 整（信任上限边界内）照用。
	if got := rateLimitedCoolingDuration(1*time.Hour, tracker, key); got != 1*time.Hour {
		t.Fatalf("retry-after 1h cooling = %s want 1h", got)
	}
	// >1h 压到 1h。
	if got := rateLimitedCoolingDuration(2*time.Hour, tracker, key); got != MaxRateRetryAfter {
		t.Fatalf("retry-after 2h cooling = %s want 1h cap", got)
	}
	// 无 Retry-After ⇒ 升级路径仍在（计数从未被上面的 Retry-After 推进，
	// 但 Observe 在最早那次已经 n=2 ⇒ 这里 n=3 = 12min）。
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 12*time.Minute {
		t.Fatalf("no-hint cooling = %s want 12m (escalation intact)", got)
	}
}

// 键隔离：同一凭据的另一个模型不受升级影响（绑定粒度）。
func TestRateBackoffIsBindingScoped(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	kA := RateBackoffKey(21, "model-a")
	kB := RateBackoffKey(21, "model-b")
	if got := tracker.Observe(kA); got.Duration != RateBackoffBase {
		t.Fatalf("model-a first = %s", got.Duration)
	}
	if got := tracker.Observe(kA); got.Duration != 6*time.Minute {
		t.Fatalf("model-a second = %s want 6m", got.Duration)
	}
	if got := tracker.Observe(kB); got.Duration != RateBackoffBase {
		t.Fatalf("model-b first = %s want 3m (binding scope)", got.Duration)
	}
	// 同键不同凭据也隔离。
	kC := RateBackoffKey(22, "model-a")
	if got := tracker.Observe(kC); got.Duration != RateBackoffBase {
		t.Fatalf("cred-22 model-a first = %s want 3m", got.Duration)
	}
}

// nil 接收者安全（与 slowrest 同款防御）。
func TestRateBackoffNilReceiverIsSafe(t *testing.T) {
	var tracker *RateBackoffTracker
	if got := tracker.Observe("k"); got.Duration != 0 {
		t.Fatalf("nil Observe = %s want 0", got.Duration)
	}
	if got := rateLimitedCoolingDuration(0, nil, "k"); got != RateBackoffBase {
		t.Fatalf("nil tracker fallback = %s want 3m", got)
	}
	if tracker.Count("k") != 0 {
		t.Fatal("nil Count must be 0")
	}
}

// coolingDuration 对 KindRateLimit 的语义不变（其它 kind 的既有门继续成立）；
// writer.go 的 KindRateLimit 分支已改走 rateLimitedCoolingDuration，
// 该函数在无计数器注入时（DefaultRateBackoff 全新进程态）首罚 = 3min。
// 这里补一条「无 Retry-After 且计数器未推进」的进程级默认路径断言，
// 保证默认路径与旧 coolingDuration(KindRateLimit, 0) = 3min 逐字节同值。
func TestRateLimitedCoolingDefaultPathMatchesLegacy(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	if got := rateLimitedCoolingDuration(0, tracker, RateBackoffKey(999, "fresh")); got != coolingDuration(errorsxKindRateLimitForTest(), 0) {
		t.Fatalf("default path = %s want legacy coolingDuration = %s", got, coolingDuration(errorsxKindRateLimitForTest(), 0))
	}
}
