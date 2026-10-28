package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// stubCircuitRecovery 是 Router.CircuitRecovery（P0-A）的测试桩：按
// (provider, credential) 返回固定的恢复时刻。
type stubCircuitRecovery struct {
	at map[[2]int]time.Time
}

func (s *stubCircuitRecovery) RecoveredAt(providerID, credentialID int) time.Time {
	return s.at[[2]int{providerID, credentialID}]
}

// TestRecoveryPenaltyForCandidate 钉 P0-A 评分惩罚的形状（线性 1→0 over
// ramp 窗口）与信号源优先级（两路取较晚）、关闭开关、时钟回拨保守值。
func TestRecoveryPenaltyForCandidate(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_RECOVERY_RAMP_SECONDS", "300")

	now := time.Now().Unix()
	c := provider.Candidate{ProviderID: 1, CredentialID: 7, RawModel: "m", Weight: 100}
	r := NewRouter(nil, nil)

	// 无信号 → 0。
	if p := recoveryPenaltyForCandidate(c, r); p != 0 {
		t.Fatalf("无恢复证据应惩罚 0，got %v", p)
	}

	// NodeState 侧信号（RoutedNodeState）：刚恢复 ≈ 1.0。
	fresh := c
	fresh.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now}
	if p := recoveryPenaltyForCandidate(fresh, r); p < 0.98 {
		t.Fatalf("刚恢复应惩罚 ≈1.0，got %v", p)
	}

	// 半窗 → ≈0.5。
	half := c
	half.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 150}
	if p := recoveryPenaltyForCandidate(half, r); p < 0.48 || p > 0.52 {
		t.Fatalf("半窗应惩罚 ≈0.5，got %v", p)
	}

	// 窗口外 → 0。
	old := c
	old.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 301}
	if p := recoveryPenaltyForCandidate(old, r); p != 0 {
		t.Fatalf("窗口外应惩罚 0，got %v", p)
	}

	// breaker 侧信号（Router.CircuitRecovery）单独生效。
	rb := NewRouter(nil, nil)
	rb.CircuitRecovery = &stubCircuitRecovery{at: map[[2]int]time.Time{
		{1, 7}: time.Unix(now-150, 0),
	}}
	if p := recoveryPenaltyForCandidate(c, rb); p < 0.48 || p > 0.52 {
		t.Fatalf("breaker 信号半窗应惩罚 ≈0.5，got %v", p)
	}

	// 两路取较晚：NodeState 处于窗口边界（now-300 ≈ 惩罚 0）+ breaker
	// 半窗（now-150）→ 以较晚的 breaker 为准 ≈0.5（若错误地取较早者会得 0）。
	both := c
	both.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 300}
	if p := recoveryPenaltyForCandidate(both, rb); p < 0.48 || p > 0.52 {
		t.Fatalf("两路信号应取较晚者（≈0.5），got %v", p)
	}

	// 时钟回拨（recoveredAt 在未来）→ 保守按刚恢复处理。
	future := c
	future.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now + 60}
	if p := recoveryPenaltyForCandidate(future, r); p != 1.0 {
		t.Fatalf("时钟回拨应保守惩罚 1.0，got %v", p)
	}

	// env 关闭（窗口置 0）→ 恒 0。
	t.Setenv("LLM_GATEWAY_ROUTING_RECOVERY_RAMP_SECONDS", "0")
	if p := recoveryPenaltyForCandidate(fresh, r); p != 0 {
		t.Fatalf("RAMP_SECONDS=0 应整体关闭，got %v", p)
	}
}

// TestRecoveryRampFactor 钉首跳份额 ramp 因子：刚恢复 0.2、半窗 0.6、
// 窗口外/无信号/关闭 1.0。
func TestRecoveryRampFactor(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_RECOVERY_RAMP_SECONDS", "300")

	now := time.Now().Unix()
	c := provider.Candidate{ProviderID: 1, CredentialID: 7, Weight: 100}
	r := NewRouter(nil, nil)

	if f := recoveryRampFactor(c, r); f != 1.0 {
		t.Fatalf("无信号因子应 1.0，got %v", f)
	}
	fresh := c
	fresh.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now}
	if f := recoveryRampFactor(fresh, r); f != 0.2 {
		t.Fatalf("刚恢复因子应 0.2，got %v", f)
	}
	half := c
	half.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 150}
	if f := recoveryRampFactor(half, r); f < 0.59 || f > 0.61 {
		t.Fatalf("半窗因子应 ≈0.6，got %v", f)
	}
	old := c
	old.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 301}
	if f := recoveryRampFactor(old, r); f != 1.0 {
		t.Fatalf("窗口外因子应 1.0，got %v", f)
	}
}

// TestFirstHopLotteryWeights_RecoveryRamp 钉 P0-A 验收三件套：
// ① ramp 窗口内恢复期候选份额 < 全量份额；
// ② 窗口外份额恢复原值（函数返回 nil —— 与现状逐字节同构）；
// ③ W_RECOVERY=0 或 RAMP_SECONDS=0 时逐字节等于现状（返回 nil）。
func TestFirstHopLotteryWeights_RecoveryRamp(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_RECOVERY_RAMP_SECONDS", "300")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_PLANQUOTA", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")

	now := time.Now().Unix()
	healthy := provider.Candidate{ProviderID: 1, CredentialID: 1, Weight: 100}
	recovering := provider.Candidate{ProviderID: 1, CredentialID: 2, Weight: 100}
	recovering.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now}

	r := NewRouter(nil, nil)

	// ① 恢复期候选：份额 = 100 × 0.2 = 20；健康候选 p=0 无折算 → 100。
	//    sum=0（其余权重全关）→ p 路径不折算，仅 ramp 生效。
	got := firstHopLotteryWeights([]provider.Candidate{healthy, recovering}, r)
	if got == nil {
		t.Fatalf("恢复期候选份额应被折算（changed=true），got nil")
	}
	if got[0] != 100 {
		t.Fatalf("健康候选份额应保持 100，got %d", got[0])
	}
	if got[1] >= 100 {
		t.Fatalf("恢复期候选份额应 < 全量，got %d", got[1])
	}
	if got[1] != 20 {
		t.Fatalf("刚恢复（0.2 因子）份额应 = 100×0.2 = 20，got %d", got[1])
	}

	// ①b 叠加路径（批判审计轮补）：p 折算与 ramp 因子**相乘**而非互斥——
	// 低余额（balancePenalty=1，sum=wBalance）+ 无恢复的候选：p=1 ⇒
	// 份额 = int(100×(1−0.9)) = int(9.999…) = 9（浮点 + int 截断，R46
	// F8② 注明的截断语义）；同一候选**加上**刚恢复信号后 ramp 0.2 再乘
	// ⇒ int(9.999…×0.2) = int(1.999…) = 1。9 → 1 证明是乘法叠加
	//（若 recovery 被错误地放进 p 折算，两者会是同一个数）。
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0.05") // sum=0.05，weighted=1×0.05 → p=1
	lowBal := 0.0
	lowBalNoRecovery := provider.Candidate{ProviderID: 1, CredentialID: 3, Weight: 100, BillingMode: "per_token"}
	lowBalNoRecovery.BalanceUSD = &lowBal
	lowBalRecovering := lowBalNoRecovery
	lowBalRecovering.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now}
	gotB := firstHopLotteryWeights([]provider.Candidate{healthy, lowBalNoRecovery}, r)
	if gotB == nil || gotB[1] != 9 {
		t.Fatalf("纯 p 路径份额应 = int(100×0.1) = 9（浮点截断），got %v", gotB)
	}
	got = firstHopLotteryWeights([]provider.Candidate{healthy, lowBalRecovering}, r)
	if got == nil || got[1] != 1 {
		t.Fatalf("p×ramp 叠加份额应 = int(9.999…×0.2) = 1，got %v", got)
	}

	// ② 窗口外：全部候选无折算 → nil（走纯 Weight 原路径，与现状一致）。
	recovered := provider.Candidate{ProviderID: 1, CredentialID: 2, Weight: 100}
	recovered.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: now - 301}
	if got := firstHopLotteryWeights([]provider.Candidate{healthy, recovered}, r); got != nil {
		t.Fatalf("窗口外应返回 nil（无折算），got %v", got)
	}

	// ③ W_RECOVERY=0 → 整体关闭 → nil。
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECOVERY", "0")
	if got := firstHopLotteryWeights([]provider.Candidate{healthy, recovering}, r); got != nil {
		t.Fatalf("W_RECOVERY=0 应整体关闭返回 nil，got %v", got)
	}
	// RAMP_SECONDS=0 → 整体关闭 → nil（权重恢复默认）。
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECOVERY", "0.10")
	t.Setenv("LLM_GATEWAY_ROUTING_RECOVERY_RAMP_SECONDS", "0")
	if got := firstHopLotteryWeights([]provider.Candidate{healthy, recovering}, r); got != nil {
		t.Fatalf("RAMP_SECONDS=0 应整体关闭返回 nil，got %v", got)
	}
}

// TestCalculateLoadScore_RecoveryOffByteIdentical 钉 P0-A 的 Off 语义：
// 无恢复证据的候选，其 composite 不含 recovery 项——与 recoveryWeight=0
// 关闭时的分数完全相等（评分公式对无信号候选逐字节一致）。
func TestCalculateLoadScore_RecoveryOffByteIdentical(t *testing.T) {
	lim := 4
	c := provider.Candidate{
		CredentialID:     9,
		ProviderID:       1,
		RawModel:         "m",
		StandardizedName: "m",
		Tier:             2,
		Weight:           100,
		ConcurrencyLimit: &lim,
	}
	ctx := context.Background()
	weights := DefaultLoadScoreWeights()

	on := NewRouter(nil, nil) // 默认 RecoveryWeight=0.10
	off := NewRouter(nil, nil)
	ew := DefaultLoadScoreEnvWeights()
	ew.RecoveryWeight = 0
	off.EnvWeights = &ew

	scoreOn := calculateLoadScore(c, on, ctx, weights, StrategyInput{LoadScoreWeights: weights})
	scoreOff := calculateLoadScore(c, off, ctx, weights, StrategyInput{LoadScoreWeights: weights})
	if scoreOn != scoreOff {
		t.Fatalf("无恢复证据的候选：recovery 开（%v）与关（%v）分数应逐字节相等", scoreOn, scoreOff)
	}

	// 有恢复证据时两者必须分开——证明开关真的在评分里生效。
	c.RoutedNodeState = &credentialfpslot.NodeState{RecoveredAt: time.Now().Unix()}
	scoreOn = calculateLoadScore(c, on, ctx, weights, StrategyInput{LoadScoreWeights: weights})
	scoreOff = calculateLoadScore(c, off, ctx, weights, StrategyInput{LoadScoreWeights: weights})
	if scoreOn <= scoreOff {
		t.Fatalf("恢复期候选应被降权：on=%v 应 > off=%v", scoreOn, scoreOff)
	}
}
