package credential

// ratebackoff_pace_test.go — token 桶 W1「节奏校准冷却」（2026-11-08）的门。
//
// 判据 = docs/audit/2026-11-07-token桶双层路由优化方案.md §三 T1 验收门：
//
//	P-A 窗口下限：无 Retry-After 的首次 429 冷却 = 窗口重灌估计（默认 60s），
//	    不再是 3min（T1a；这是「单次 429=3min」既有承诺的**显式变更**，
//	    逃生口 refill=0 仍精确回到 3min，由 ratebackoff_test.go 钉）；
//	P-B 节奏违规升级梯：间隔 < 窗口的快速连击达 threshold（默认 3）次才进
//	    3min<<n-thr 梯：3→6→12→24→30 封顶（变异：删节奏判别 ⇒ 本序列红）；
//	P-C 节奏正常归零：间隔 ≥ 窗口 ⇒ 计数归零、回窗口冷却（仿真 S3 的全部
//	    杠杆来源；变异：删归零 ⇒ 本条红）；
//	P-D RA 优先不变：vendor 说了 45s 就 45s（压过窗口 60s 与升级值）；
//	P-E threshold 可调：threshold=2 时梯子从第 2 次快速连击起；
//	P-F 遗留逃生口经生产路径可达：refill=0 ⇒ 3min/6min（A2 逐字节）；
//	P-G nil tracker：pace 模式返回窗口值，legacy 模式返回 3min。
//
// 红线对照：429 不进断路器（breaker.go Wave 1 A4）不变——本套只动
// writer.go 写出的绑定级冷却时长。

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// paceClockTracker 造可控时钟的 tracker + 生产路径入口
// （rateLimitedCoolingDuration 走 settings ⇒ 用 setRatePolicySettingsForTest 控制两态）。
func paceClockTracker(t *testing.T, refillSeconds, threshold int) (*RateBackoffTracker, *time.Time) {
	t.Helper()
	setRatePolicySettingsForTest(t, refillSeconds, threshold)
	return newTestRateBackoff(t)
}

// P-A：首次 429 = 窗口重灌估计（60s），不是 3min。
func TestPaceCalibratedFirstHitIsWindowFloor(t *testing.T) {
	tracker, _ := paceClockTracker(t, 60, 3)
	got := rateLimitedCoolingDuration(0, tracker, RateBackoffKey(31, "m"))
	if got != 60*time.Second {
		t.Fatalf("first 429 (no RA) cooling = %s want 60s window floor (T1a)", got)
	}
	if tracker.Count(RateBackoffKey(31, "m")) != 1 {
		t.Fatalf("count = %d want 1", tracker.Count(RateBackoffKey(31, "m")))
	}
}

// P-B：快速连击（间隔 10s < 60s 窗口）达 threshold=3 起进升级梯。
func TestPaceCalibratedRapidSequenceEscalates(t *testing.T) {
	tracker, clock := paceClockTracker(t, 60, 3)
	key := RateBackoffKey(32, "m")
	want := []time.Duration{
		60 * time.Second, // n=1：窗口冷却
		60 * time.Second, // n=2：仍未达 threshold
		3 * time.Minute,  // n=3：违规起点 = 3min
		6 * time.Minute,  // n=4
		12 * time.Minute, // n=5
		24 * time.Minute, // n=6
		30 * time.Minute, // n=7：封顶
		30 * time.Minute, // n=8：仍封顶
	}
	for i, w := range want {
		if i > 0 {
			*clock = clock.Add(10 * time.Second) // 每次间隔 10s < 60s = 节奏违规
		}
		got := rateLimitedCoolingDuration(0, tracker, key)
		if got != w {
			t.Fatalf("rapid hit #%d cooling = %s want %s", i+1, got, w)
		}
	}
}

// P-C：节奏正常（间隔 ≥ 窗口）⇒ 计数归零、回窗口冷却（S3 杠杆）。
// 变异臂：删 ObserveWithPace 的 pace 归零分支 ⇒ 本条红（会停留在升级梯）。
func TestPaceCalibratedNormalRhythmResets(t *testing.T) {
	tracker, clock := paceClockTracker(t, 60, 3)
	key := RateBackoffKey(33, "m")
	// 两次快速连击推到 n=2。
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 60*time.Second {
		t.Fatalf("first = %s want 60s", got)
	}
	*clock = clock.Add(10 * time.Second)
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 60*time.Second {
		t.Fatalf("second rapid = %s want 60s (below threshold)", got)
	}
	// 70s 后再来一次：≥ 60s 窗口 = 节奏正常 ⇒ 归零，仍是窗口冷却、未升级。
	*clock = clock.Add(70 * time.Second)
	got := rateLimitedCoolingDuration(0, tracker, key)
	if got != 60*time.Second {
		t.Fatalf("rhythm-normal 429 cooling = %s want 60s (reset, not escalated)", got)
	}
	if c := tracker.Count(key); c != 1 {
		t.Fatalf("count = %d want 1 after rhythm reset", c)
	}
}

// P-D：RA 优先在节奏模式下不变——vendor 说 45s 就 45s（压过窗口 60s）。
func TestPaceCalibratedRetryAfterStillWins(t *testing.T) {
	tracker, _ := paceClockTracker(t, 60, 3)
	key := RateBackoffKey(34, "m")
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 60*time.Second {
		t.Fatalf("precondition first = %s want 60s", got)
	}
	if got := rateLimitedCoolingDuration(45*time.Second, tracker, key); got != 45*time.Second {
		t.Fatalf("RA 45s cooling = %s want 45s (vendor hint wins over window floor)", got)
	}
	// RA 路径不消耗计数：下次无 RA 仍是 n=2 的窗口冷却。
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 60*time.Second {
		t.Fatalf("post-RA no-hint = %s want 60s (count must not advance via RA path)", got)
	}
}

// P-E：threshold 可调 —— threshold=2 时梯子从第 2 次快速连击起。
func TestPaceCalibratedThresholdConfigurable(t *testing.T) {
	tracker, clock := paceClockTracker(t, 60, 2)
	key := RateBackoffKey(35, "m")
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 60*time.Second {
		t.Fatalf("first = %s want 60s", got)
	}
	*clock = clock.Add(10 * time.Second)
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 3*time.Minute {
		t.Fatalf("second rapid (thr=2) = %s want 3m (ladder starts)", got)
	}
}

// P-F：遗留逃生口经生产路径可达 —— refill=0 ⇒ 精确 A2（3min/6min/12min）。
func TestPaceCalibratedLegacyEscapeViaSettings(t *testing.T) {
	tracker, _ := paceClockTracker(t, 0, 3)
	key := RateBackoffKey(36, "m")
	if got := rateLimitedCoolingDuration(0, tracker, key); got != RateBackoffBase {
		t.Fatalf("legacy first = %s want 3m", got)
	}
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 6*time.Minute {
		t.Fatalf("legacy second = %s want 6m", got)
	}
	if got := rateLimitedCoolingDuration(0, tracker, key); got != 12*time.Minute {
		t.Fatalf("legacy third = %s want 12m", got)
	}
}

// P-G：nil tracker —— pace 模式回窗口值，legacy 模式回 3min。
func TestPaceCalibratedNilTracker(t *testing.T) {
	paceClockTracker(t, 60, 3)
	if got := rateLimitedCoolingDuration(0, nil, "k"); got != 60*time.Second {
		t.Fatalf("nil tracker (pace) = %s want 60s", got)
	}
	setRatePolicySettingsForTest(t, 0, 3)
	if got := rateLimitedCoolingDuration(0, nil, "k"); got != RateBackoffBase {
		t.Fatalf("nil tracker (legacy) = %s want 3m", got)
	}
}

// P-H：未注册 settings（全新进程态）＝ spec Default（60s/3）生效。
// Global 是空注册表 ⇒ GetPlatformInt 走 fallback（helpers.go nil/缺 spec 分支）。
func TestPaceCalibratedUnsetSettingsUseSpecDefaults(t *testing.T) {
	tracker, _ := newTestRateBackoff(t)
	if got := rateLimitedCoolingDuration(0, tracker, RateBackoffKey(37, "m")); got != 60*time.Second {
		t.Fatalf("unset settings first hit = %s want 60s (spec default)", got)
	}
	if settings.RateWindowRefillSeconds() != 60 || settings.RatePaceViolationThreshold() != 3 {
		t.Fatalf("getters = %d/%d want 60/3 (spec defaults)", settings.RateWindowRefillSeconds(), settings.RatePaceViolationThreshold())
	}
}
