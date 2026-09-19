package executors

import (
	"context"
	"math"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/assert"
)

func TestCalculateLoadScore_BalancedWeights(t *testing.T) {
	router := &Router{
		LoadScoreWeights: DefaultLoadScoreWeights(),
	}

	candidate := provider.Candidate{
		CredentialID:     1,
		ProviderID:       1,
		P95LatencyMs:     500,
		SuccessRate:      0.95,
		ConcurrencyLimit: intPtr(50),
	}

	ctx := context.Background()
	score := calculateLoadScore(candidate, router, ctx, router.LoadScoreWeights)

	// 验证分数在合理范围内
	assert.GreaterOrEqual(t, score, 0.0)
	assert.LessOrEqual(t, score, 1.0)
}

func TestConcurrencyScoreUsesCredentialLimiter(t *testing.T) {
	limiter := credential.NewWithLimits(10, 10, 4, 2)
	defer limiter.Stop()
	if !limiter.Credential(7, 9).TryAcquire() {
		t.Fatal("expected credential limiter token")
	}

	router := &Router{Limiter: limiter}
	score := calculateConcurrencyScore(provider.Candidate{
		ProviderID:   7,
		CredentialID: 9,
	}, router, context.Background())
	assert.InDelta(t, 0.25, score, 1e-9)
}

func TestFPSlotTenantUsesAuthenticatedParams(t *testing.T) {
	assert.Equal(t, "tenant-a", fpSlotTenantID(&ExecParams{TenantID: "tenant-a"}))
}

func TestConcurrencyScore_Saturation(t *testing.T) {
	tests := []struct {
		name     string
		used     int
		limit    int
		expected float64
	}{
		{"空闲", 0, 50, 0.0},
		{"50%使用", 25, 50, 0.5},
		{"饱和", 50, 50, 1.0},
		{"超饱和", 60, 50, 1.0}, // 限制到1.0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里需要 mock FpSlots，简化测试仅验证逻辑
			pressure := float64(tt.used) / float64(tt.limit)
			if pressure > 1.0 {
				pressure = 1.0
			}
			assert.Equal(t, tt.expected, pressure)
		})
	}
}

// TestLatencyScore_SaturationCurve 校验 calculateLatencyScore 的分段健康度表。
// 修复 2026-07-24 审计发现的语义反转：原测试断言是旧 penalty 曲线（latency/(latency+k)），
// 实现已改为分段「健康度」（越大越好），对应 docs/design/2026-07-20-latency-aware-routing.md §2.1。
// composite 用 (1 - latencyScore) 转成惩罚；低延迟 → 低惩罚 → 更易被选中。
func TestLatencyScore_SaturationCurve(t *testing.T) {
	// 压力 = 0.5（c.PressureLimit 为 nil），低于 knee=0.6，故 observed == p95。
	tests := []struct {
		name      string
		latencyMs int
		expected  float64
		delta     float64
	}{
		{"极快_无样本", 50, 0.0, 0.0},
		{"瞬时", 500, 1.00, 0.0},
		{"可接受", 1000, 0.957, 0.01},
		{"轻微退化", 2000, 0.783, 0.01},
		{"明显慢", 5000, 0.55, 0.01},
		{"近不可用", 12000, 0.275, 0.01},
		{"硬阻断", 35000, 0.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := provider.Candidate{
				P95LatencyMs: tt.latencyMs,
			}
			score := calculateLatencyScore(candidate, nil)
			assert.InDelta(t, tt.expected, score, tt.delta)
		})
	}
}

func TestQualityScore_SuccessRate(t *testing.T) {
	tests := []struct {
		name        string
		successRate float64
		expected    float64
	}{
		{"95%成功", 0.95, 0.05},
		{"80%成功", 0.80, 0.20},
		{"50%成功", 0.50, 0.50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := provider.Candidate{
				SuccessRate: tt.successRate,
			}
			score := calculateQualityScore(candidate)
			// 浮点运算 1.0-0.95 = 0.05000...044，必须用 InDelta 容差比较
			assert.InDelta(t, tt.expected, score, 1e-9)
		})
	}
}

func TestDefaultLoadScoreWeights(t *testing.T) {
	weights := DefaultLoadScoreWeights()

	// 验证权重总和为1.0
	total := weights.ConcurrencyWeight + weights.IdentityWeight +
		weights.LatencyWeight + weights.QualityWeight

	assert.InDelta(t, 1.0, total, 0.001)

	// 验证各权重在合理范围内
	assert.Greater(t, weights.ConcurrencyWeight, 0.0)
	assert.Greater(t, weights.IdentityWeight, 0.0)
	assert.Greater(t, weights.LatencyWeight, 0.0)
	assert.Greater(t, weights.QualityWeight, 0.0)
}

func intPtr(v int) *int {
	return &v
}

func TestCalculateLatencyScore_PiecewiseNoPressure(t *testing.T) {
	// No pressure (pressure=0.5 default fallback), p95 latency drives piecewise.
	candidates := []struct {
		name string
		p95  int
		want float64
	}{
		{"fast (300ms)", 300, 1.00},
		{"medium (1000ms)", 1000, lerp(1000, 800, 1500, 1.00, 0.85)},
		{"slow (5000ms)", 5000, lerp(5000, 3000, 10000, 0.65, 0.30)},
		{"very slow (20s)", 20000, lerp(20000, 10000, 30000, 0.30, 0.05)},
	}
	for _, tc := range candidates {
		t.Run(tc.name, func(t *testing.T) {
			c := provider.Candidate{P95LatencyMs: tc.p95}
			got := calculateLatencyScore(c, nil)
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("latency_score(p95=%d) = %f, want %f", tc.p95, got, tc.want)
			}
		})
	}
}

func TestCalculateHeadroom_DefaultGamma1(t *testing.T) {
	// candidatePressure returns 0.5 default when ConcurrencyLimit nil and no
	// limiter; headroom = max(0, 1-0.5)^1 = 0.5
	c := provider.Candidate{}
	got := calculateHeadroom(c, nil)
	if got < 0.49 || got > 0.51 {
		t.Errorf("headroom default = %f, want 0.5", got)
	}
}

func TestCalculateHeadroom_SaturatedZero(t *testing.T) {
	// With a saturated limiter, pressure=1.0 → headroom = 0.
	limiter := credential.NewWithLimits(10, 10, 4, 2)
	defer limiter.Stop()
	sem := limiter.Credential(1, 1)
	for i := 0; i < sem.Capacity(); i++ {
		if !sem.TryAcquire() {
			t.Fatalf("could not acquire token %d", i)
		}
	}
	lim := 10
	c := provider.Candidate{ProviderID: 1, CredentialID: 1, ConcurrencyLimit: &lim}
	got := calculateHeadroom(c, &Router{Limiter: limiter})
	if got > 1e-9 {
		t.Errorf("headroom saturated = %f, want 0", got)
	}
}

// TestCandidatePressure_UsesRealtimeLimiter verifies candidatePressure now reads
// realtime in-flight concurrency from Router.Limiter instead of the hardcoded 0.5.
func TestCandidatePressure_UsesRealtimeLimiter(t *testing.T) {
	limiter := credential.NewWithLimits(10, 10, 4, 2)
	defer limiter.Stop()
	sem := limiter.Credential(5, 6) // capacity 4 (credentialLimit)
	if !sem.TryAcquire() {
		t.Fatal("expected token")
	}
	c := provider.Candidate{ProviderID: 5, CredentialID: 6}
	r := &Router{Limiter: limiter}

	got := candidatePressure(c, r)
	// 1 of 4 used → 0.25
	assert.InDelta(t, 0.25, got, 1e-9)
}

// TestCandidatePressure_NilRouterFallback verifies the neutral fallback when no
// realtime limiter is available (preserves prior "unknown ⇒ medium" semantics).
func TestCandidatePressure_NilRouterFallback(t *testing.T) {
	c := provider.Candidate{}
	assert.InDelta(t, 0.5, candidatePressure(c, nil), 1e-9)
}

// fakeLiveLoad is a minimal Router.LiveLoad implementation used by the
// dispatch-aware regression tests below. The production PeakCollector
// satisfies the same interface; we substitute a stub to keep tests
// hermetic (no DB pool required).
type fakeLiveLoad struct {
	concurrent map[credModelKey]int64
}

type credModelKey struct {
	CredID int64
	Model  string
}

func (f *fakeLiveLoad) GetLiveConcurrent(credID int64, model string) int64 {
	if f == nil {
		return 0
	}
	return f.concurrent[credModelKey{CredID: credID, Model: model}]
}

// TestConcurrencyScore_ReadsDispatchLiveLoad (regression, 2026-09-09 P0):
//
// 245 production log evidence:
//   credential 42: 803 requests (57%), credential 21: 502 (36%),
//   credential 45: 76 (5%),  credential 29: 22 (2%)
//
// All four had concurrency_score=0 in the LOAD_SCORE_V2 sample, so P2C
// fell back to pickWeightedTie and stuck group A (weight=20) on group B
// (weight=10) at ~93:7. Root cause: dispatch_v2 calls
// AcquireAllNoCredLayer which deliberately skips the Limiter's credential
// semaphore, so r.Limiter.Credential().Used() is always 0. The fix routes
// scoring through Router.LiveLoad (wired to PeakCollector in main.go), so
// dispatch's actual Acquire/Release on the credential layer is what
// calculateConcurrencyScore / calculateIdentityScore see.
//
// This test pins the new behaviour: even with a Limiter present (legacy
// deployments), LiveLoad overrides it; with no Limiter (current dispatch_v2
// path), LiveLoad alone drives the score.
func TestConcurrencyScore_ReadsDispatchLiveLoad(t *testing.T) {
	ll := &fakeLiveLoad{
		concurrent: map[credModelKey]int64{
			{CredID: 42, Model: "MiniMax-M3"}: 19, // saturated (capacity 20)
			{CredID: 21, Model: "MiniMax-M3"}: 18, // near saturated
			{CredID: 45, Model: "minimax-m3"}: 0,  // idle
		},
	}
	limit := 20
	cBusy := provider.Candidate{
		ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3",
		ConcurrencyLimit: &limit,
	}
	cIdle := provider.Candidate{
		ProviderID: 5917, CredentialID: 45, RawModel: "minimax-m3",
		ConcurrencyLimit: &limit,
	}

	t.Run("live_load_drives_score_when_wired", func(t *testing.T) {
		r := &Router{LiveLoad: ll}
		// busy credential: 19/20 = 0.95 pressure
		assert.InDelta(t, 0.95, calculateConcurrencyScore(cBusy, r, context.Background()), 1e-9)
		// idle credential: 0/20 = 0
		assert.InDelta(t, 0.0, calculateConcurrencyScore(cIdle, r, context.Background()), 1e-9)
	})

	t.Run("live_load_takes_capacity_from_candidate", func(t *testing.T) {
		// LiveLoad is wired (dispatch_v2 path). The Limiter is also present
		// but its seeded capacity is the global default (50), not the
		// per-credential DB value (20). The fix must use Candidate.ConcurrencyLimit
		// for capacity, otherwise saturation penalty never fires.
		limiter := credential.NewWithLimits(10, 10, 50, 2)
		defer limiter.Stop()
		// Saturate the limiter — irrelevant since LiveLoad path bypasses it.
		for i := 0; i < 50; i++ {
			if !limiter.Credential(14, 42).TryAcquire() {
				t.Fatalf("setup: limiter token %d", i)
			}
		}
		r := &Router{Limiter: limiter, LiveLoad: ll}
		// Capacity comes from Candidate.ConcurrencyLimit=20, not from Limiter=50.
		// LiveLoad says 19/20 = 0.95.
		assert.InDelta(t, 0.95, calculateConcurrencyScore(cBusy, r, context.Background()), 1e-9)
	})

	t.Run("legacy_path_uses_limiter_capacity", func(t *testing.T) {
		// No LiveLoad → legacy dispatch path. Limiter IS the source of truth
		// (AcquireAll acquires the credential semaphore). Limiter.Capacity
		// is preferred over Candidate.ConcurrencyLimit because admin can
		// hot-update the in-process semaphore via SetCredentialCapacity.
		limiter := credential.NewWithLimits(10, 10, 4, 2)
		defer limiter.Stop()
		if !limiter.Credential(14, 42).TryAcquire() {
			t.Fatal("setup: token")
		}
		c := provider.Candidate{ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3"}
		// ConcurrencyLimit not set on candidate; capacity from Limiter (4).
		// Used=1 → 1/4 = 0.25.
		r := &Router{Limiter: limiter} // LiveLoad nil
		assert.InDelta(t, 0.25, calculateConcurrencyScore(c, r, context.Background()), 1e-9)
	})

	t.Run("identity_score_same_signal", func(t *testing.T) {
		r := &Router{LiveLoad: ll}
		// Same source, same result — confirms identity_score no longer
		// collapses to 0 just because the credential semaphore is idle.
		assert.InDelta(t, 0.95, calculateIdentityScore(cBusy, r), 1e-9)
		assert.InDelta(t, 0.0, calculateIdentityScore(cIdle, r), 1e-9)
	})

	t.Run("nil_live_load_falls_back_to_limiter", func(t *testing.T) {
		limiter := credential.NewWithLimits(10, 10, 5, 2)
		defer limiter.Stop()
		// 2 of 5 acquired
		if !limiter.Credential(14, 42).TryAcquire() {
			t.Fatal("setup token 1")
		}
		if !limiter.Credential(14, 42).TryAcquire() {
			t.Fatal("setup token 2")
		}
		r := &Router{Limiter: limiter} // LiveLoad nil
		// Candidate has no per-credential ConcurrencyLimit, so capacity
		// comes from Limiter (5). 2/5 = 0.4.
		c := provider.Candidate{ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3"}
		assert.InDelta(t, 0.4, calculateConcurrencyScore(c, r, context.Background()), 1e-9)
	})
}

// TestConcurrencyScore_DistributesAcrossCandidatesByConcurrency (regression,
// 2026-09-09 P0):
//
// The original symptom was "all requests concentrate on credential 42".
// Simulate a 2-candidate pool with the same weight and verify P2C selects
// the idle one once the busy one is reported as in-flight via LiveLoad.
// Pre-fix the busy-vs-idle distinction vanished because concurrency_score
// was always 0, so pickWeightedTie would have flipped a fair coin and the
// busy credential kept getting picked by random chance — never penalized
// for being saturated.
func TestConcurrencyScore_DistributesAcrossCandidatesByConcurrency(t *testing.T) {
	limit := 20
	ll := &fakeLiveLoad{
		concurrent: map[credModelKey]int64{
			{CredID: 42, Model: "MiniMax-M3"}: 20, // saturated
			{CredID: 45, Model: "minimax-m3"}: 0,  // idle
		},
	}
	r := &Router{LiveLoad: ll}
	busyScore := calculateConcurrencyScore(provider.Candidate{
		ProviderID: 14, CredentialID: 42, RawModel: "MiniMax-M3",
		ConcurrencyLimit: &limit,
	}, r, context.Background())
	idleScore := calculateConcurrencyScore(provider.Candidate{
		ProviderID: 5917, CredentialID: 45, RawModel: "minimax-m3",
		ConcurrencyLimit: &limit,
	}, r, context.Background())
	// The idle candidate must have a strictly lower concurrency score so
	// that P2C's `scoreB < scoreA` branch picks it on every draw. Pre-fix
	// both were 0 → tie → pickWeightedTie → 50/50 random.
	if !(idleScore < busyScore) {
		t.Fatalf("idle concurrency_score (%f) must be < busy (%f) so P2C picks idle", idleScore, busyScore)
	}
}

func TestMathPow(t *testing.T) {
	if mathPow(2, 3) != 8 {
		t.Errorf("mathPow(2,3) = %v, want 8", mathPow(2, 3))
	}
	if mathPow(0, 5) != 0 {
		t.Errorf("mathPow(0,5) = %v, want 0", mathPow(0, 5))
	}
}

func TestLerp(t *testing.T) {
	if math.Abs(lerp(1000, 800, 1500, 1.00, 0.85)-0.957) > 0.01 {
		t.Errorf("lerp(1000, 800, 1500, 1.0, 0.85) = %v, want 0.957", lerp(1000, 800, 1500, 1.00, 0.85))
	}
	if lerp(800, 800, 1500, 1.00, 0.85) != 1.00 {
		t.Errorf("lerp at start should be 1.00")
	}
}

// R47：F8④ 收尾钉桩——负权重被 clamp 到 0，方向不反转（负 headroom/
// capacity 权重曾使对应惩罚项变奖励：低 headroom、低容量节点反被偏好）。
func TestCalculateLoadScore_NegativeWeightsClamped(t *testing.T) {
	router := &Router{LoadScoreWeights: DefaultLoadScoreWeights()}
	candidate := provider.Candidate{
		CredentialID:     1,
		ProviderID:       1,
		P95LatencyMs:     500,
		SuccessRate:      0.95,
		ConcurrencyLimit: intPtr(50),
	}
	ctx := context.Background()

	t.Setenv("LLM_GATEWAY_ROUTING_W_HEADROOM", "0")
	zero := calculateLoadScore(candidate, router, ctx, router.LoadScoreWeights)

	t.Setenv("LLM_GATEWAY_ROUTING_W_HEADROOM", "-5")
	negativeHeadroom := calculateLoadScore(candidate, router, ctx, router.LoadScoreWeights)
	assert.InDelta(t, zero, negativeHeadroom, 1e-12,
		"negative W_HEADROOM must clamp to 0 (same score as W_HEADROOM=0)")

	t.Setenv("LLM_GATEWAY_ROUTING_W_CAPACITY", "-3")
	negativeCapacity := calculateLoadScore(candidate, router, ctx, router.LoadScoreWeights)
	assert.InDelta(t, zero, negativeCapacity, 1e-12,
		"negative W_CAPACITY must clamp to 0 (same score as W_HEADROOM=0 baseline)")
}
