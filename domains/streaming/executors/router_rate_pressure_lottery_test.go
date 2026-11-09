package executors

// 2026-11-09 上游桶加权均衡轮 · 判据面
//
// 背景（211 实测）：kimi-k3/glm-5.2/5.3 各 15-19 个上游桶全部 weight=100、
// rpm 未配 ⇒ 首跳抽签等权，紧 RPM 桶拿满额首跳份额，429 闪断成主形状
// （RecentSuccessRate 0.5-0.9）；loadScore 惩罚只影响 failover 顺序，救不了首跳。
//
// 本文件钉住：
//   L1 限流压力惩罚（ratePressurePenaltyForCandidate）的信号优先级与中性面；
//   L2 首跳份额倾斜：闪断桶份额 < 健康桶（默认 env），健康桶不受罚；
//   L3 Off-is-byte-identical：W_RATE_PRESSURE=0 ⇒ 折算输出与关闭前逐值一致；
//   L4 容量份额：rpm 比例折算 + floor 0.1 + 无信号队列全 1 + 开关关闭全 1。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func healthyCand(cred int) provider.Candidate {
	rate := 1.0
	c := provider.Candidate{ProviderID: 1, CredentialID: cred, Weight: 100}
	c.RecentSuccessRate = &rate
	c.RecentSamples = 50
	return c
}

func flappingCand(cred int, rate float64) provider.Candidate {
	r := rate
	c := provider.Candidate{ProviderID: 1, CredentialID: cred, Weight: 100}
	c.RecentSuccessRate = &r
	c.RecentSamples = 50
	return c
}

// L1：RecentSuccessRate（samples≥10）优先 → SuccessRate 回退 → 全无信号中性 0；
// 越界/NaN 防御中性。
func TestRatePressurePenaltySignalPriority(t *testing.T) {
	recent := 0.5
	c := provider.Candidate{SuccessRate: 0.9, RecentSuccessRate: &recent, RecentSamples: 50}
	if p := ratePressurePenaltyForCandidate(c); p != 0.5 {
		t.Fatalf("live recent(0.5) must drive the penalty, got %v", p)
	}
	c.RecentSamples = 9 // 样本不足 ⇒ 中性（不回退静态慢信号）
	if p := ratePressurePenaltyForCandidate(c); p != 0 {
		t.Fatalf("insufficient samples must be neutral, got %v", p)
	}
	c.RecentSuccessRate = nil // 无活体 ⇒ 中性
	if p := ratePressurePenaltyForCandidate(c); p != 0 {
		t.Fatalf("nil recent must be neutral (no static fallback), got %v", p)
	}
	nan := nanRate()
	c2 := provider.Candidate{RecentSuccessRate: &nan, RecentSamples: 50}
	if p := ratePressurePenaltyForCandidate(c2); p != 0 {
		t.Fatalf("NaN must be neutral 0, got %v", p)
	}
}

func nanRate() float64 {
	var z float64
	return z / z
}

// L2：默认权重下，429 闪断桶（0.6）份额必须低于健康桶（1.0），健康桶不被罚。
// 其它维度关掉以隔离（同族隔离法，见 TestFirstHopLotteryWeights_QuotaFoldMath）。
func TestLotteryTiltsAwayFromRateFlappingBuckets(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_PLANQUOTA", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECOVERY", "0")
	r := NewRouter(nil, nil)

	got := firstHopLotteryWeights([]provider.Candidate{
		healthyCand(1), flappingCand(2, 0.6), flappingCand(3, 0.2),
	}, r)
	if got == nil {
		t.Fatal("flapping buckets must tilt shares (changed)")
	}
	if got[0] != 100 {
		t.Fatalf("healthy bucket must keep full share, got %d", got[0])
	}
	if got[1] >= got[0] || got[2] >= got[1] {
		t.Fatalf("shares must decrease with pressure: got %v", got)
	}
	// 满压桶（0 成功）份额下限仍 ≥1（floor 防饿死）。
	gotWorst := firstHopLotteryWeights([]provider.Candidate{healthyCand(1), flappingCand(2, 0.0)}, r)
	if gotWorst[1] < 1 {
		t.Fatalf("worst bucket share floor must be 1, got %d", gotWorst[1])
	}
}

// L3：W_RATE_PRESSURE=0 ⇒ 与加前逐值一致（OffIsByteIdentical 族）。
func TestLotteryRatePressureOffIsByteIdentical(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_PLANQUOTA", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RATE_PRESSURE", "0")
	r := NewRouter(nil, nil)
	// 全健康候选：所有惩罚 0 ⇒ 输出 nil（纯 Weight 路径）。
	if got := firstHopLotteryWeights([]provider.Candidate{healthyCand(1), healthyCand(2)}, r); got != nil {
		t.Fatalf("all-off + healthy candidates must be nil (pure Weight), got %v", got)
	}
}

// L4：容量份额 = rpm 比例（floor 0.1）；无 rpm 信号的候选/队列全 1；开关关全 1。
func TestCapacityShareFactors(t *testing.T) {
	small, mid, big := 30, 90, 300
	cands := []provider.Candidate{
		{CredentialID: 1, Weight: 100, RPMLimit: &small},
		{CredentialID: 2, Weight: 100, RPMLimit: &big},
		{CredentialID: 3, Weight: 100}, // 未配置 ⇒ 中性 1
		{CredentialID: 4, Weight: 100, RPMLimit: &mid},
	}
	f := capacityShareFactors(cands)
	if f[0] < 0.099 || f[0] > 0.101 {
		t.Fatalf("30/300 rpm factor = %v, want 0.1 (floor)", f[0])
	}
	if f[1] != 1.0 {
		t.Fatalf("max-rpm bucket factor = %v, want 1.0", f[1])
	}
	if f[2] != 1.0 {
		t.Fatalf("unconfigured bucket must stay neutral 1.0, got %v", f[2])
	}
	if f[3] < 0.299 || f[3] > 0.301 {
		t.Fatalf("90/300 rpm factor = %v, want 0.3", f[3])
	}
	// 队列内无任何 rpm 信号 ⇒ 全 1。
	fNone := capacityShareFactors([]provider.Candidate{{Weight: 100}, {Weight: 100}})
	for i, v := range fNone {
		if v != 1.0 {
			t.Fatalf("no-signal cohort must be all 1.0, got [%d]=%v", i, v)
		}
	}
	// 开关关闭 ⇒ 全 1。
	t.Setenv("LLM_GATEWAY_ROUTING_CAPACITY_SHARE", "0")
	fOff := capacityShareFactors(cands)
	for i, v := range fOff {
		if v != 1.0 {
			t.Fatalf("capacity share off must be all 1.0, got [%d]=%v", i, v)
		}
	}
}

// L4b：容量份额×压力折算的合成——小 rpm 且闪断的桶份额最低，大 rpm 健康
// 桶拿满份额。
func TestLotteryCapacityAndPressureCompose(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_PLANQUOTA", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECOVERY", "0")
	r := NewRouter(nil, nil)
	small, big := 30, 300
	tightFlap := flappingCand(1, 0.5)
	tightFlap.RPMLimit = &small
	bigHealthy := healthyCand(2)
	bigHealthy.RPMLimit = &big
	got := firstHopLotteryWeights([]provider.Candidate{tightFlap, bigHealthy}, r)
	if got == nil {
		t.Fatal("composition must tilt")
	}
	if got[1] != 100 {
		t.Fatalf("big healthy bucket keeps full share, got %d", got[1])
	}
	if got[0] >= got[1] {
		t.Fatalf("tight flapping bucket must have the smaller share, got %v", got)
	}
}
