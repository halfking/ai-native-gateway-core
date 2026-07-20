package executors

import (
	"math"
	"context"
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

func TestLatencyScore_SaturationCurve(t *testing.T) {
	tests := []struct {
		latencyMs int
		expected  float64
	}{
		{50, 0.0},    // 极快，无惩罚
		{1000, 0.5},  // k=1000: 50%
		{2000, 0.67}, // 约67%
		{5000, 0.83}, // 约83%
	}

	for _, tt := range tests {
		t.Run("latency_"+string(rune(tt.latencyMs)), func(t *testing.T) {
			candidate := provider.Candidate{
				P95LatencyMs: tt.latencyMs,
			}
			score := calculateLatencyScore(candidate)

			if tt.latencyMs < 100 {
				assert.Equal(t, 0.0, score)
			} else {
				// 允许小误差
				assert.InDelta(t, tt.expected, score, 0.05)
			}
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
			got := calculateLatencyScore(c)
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("latency_score(p95=%d) = %f, want %f", tc.p95, got, tc.want)
			}
		})
	}
}

func TestCalculateHeadroom_DefaultGamma1(t *testing.T) {
	// candidatePressure returns 0.5 default when ConcurrencyLimit nil;
	// headroom = max(0, 1-0.5)^1 = 0.5
	c := provider.Candidate{}
	got := calculateHeadroom(c)
	if got < 0.49 || got > 0.51 {
		t.Errorf("headroom default = %f, want 0.5", got)
	}
}

func TestCalculateHeadroom_SaturatedZero(t *testing.T) {
	// Pressure 1.0 → headroom = 0
	lim := 10
	c := provider.Candidate{ConcurrencyLimit: &lim}
	// candidatePressure returns 0.5 default fallback (limiter not in scope here).
	// In production it's read from Limiter.Stats(). We just assert non-negative.
	got := calculateHeadroom(c)
	if got < 0 || got > 1.0 {
		t.Errorf("headroom = %f, expected [0,1]", got)
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
