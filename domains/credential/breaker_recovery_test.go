package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestBreaker_RecoveredAt_Lifecycle 钉 P0-A（2026-10-28 两层路由审计 G1）
// 的 breaker 侧恢复时刻语义：
// ① 一直 CLOSED 的常规成功不写（否则所有节点永远"刚恢复"）；
// ② 从 OPEN 恢复（2 连败 → OPEN → 成功）写恢复时刻；
// ③ 再翻红清零（ramp 只属于当前 CLOSED 会话，不留幽灵时刻）；
// ④ Reset（管理员手动恢复）同样进入 ramp；
// ⑤ Manager.RecoveredAt 对不存在的 breaker 返回零值（fail-open 不惩罚）。
func TestBreaker_RecoveredAt_Lifecycle(t *testing.T) {
	m := NewManager()

	// ① 常规成功（一直 CLOSED）不写。
	m.RecordSuccess(1, 1)
	if b := m.Get(1, 1); b != nil && !b.RecoveredAt().IsZero() {
		t.Fatalf("CLOSED 下的常规成功不应写 RecoveredAt，got %v", b.RecoveredAt())
	}

	// ② 2 连败（autoRecoveryFailureThreshold=2）→ OPEN，恢复成功后写入。
	m.RecordFailure(1, 2, errorsx.KindTimeout)
	m.RecordFailure(1, 2, errorsx.KindTimeout)
	if m.Get(1, 2).State() != StateOpen {
		t.Fatalf("2 连败后 breaker 应 OPEN，got %s", m.Get(1, 2).State())
	}
	if !m.Get(1, 2).RecoveredAt().IsZero() {
		t.Fatalf("翻红后旧恢复证据应被清零")
	}
	before := time.Now().Unix()
	m.RecordSuccess(1, 2)
	at := m.Get(1, 2).RecoveredAt()
	if at.IsZero() {
		t.Fatalf("从 OPEN 恢复应写 RecoveredAt")
	}
	if at.Unix() < before {
		t.Fatalf("RecoveredAt 应不早于恢复前一刻: %v < %v", at.Unix(), before)
	}

	// ③ 再翻红 → 清零。
	m.RecordFailure(1, 2, errorsx.KindTimeout)
	m.RecordFailure(1, 2, errorsx.KindTimeout)
	if !m.Get(1, 2).RecoveredAt().IsZero() {
		t.Fatalf("再次翻红后恢复证据应作废")
	}

	// ④ Reset（管理员手动恢复）→ 同样写恢复时刻。
	m.Reset(1, 2)
	if m.Get(1, 2).RecoveredAt().IsZero() {
		t.Fatalf("管理员 Reset 应写 RecoveredAt（未经流量验证的恢复恰是 ramp 最需要的场景）")
	}

	// ⑤ 不存在的 breaker → 零值；存在的与 Breaker.RecoveredAt 同源。
	if !m.RecoveredAt(999, 999).IsZero() {
		t.Fatalf("无 breaker 应返回零值")
	}
	if m.RecoveredAt(1, 2).IsZero() {
		t.Fatalf("Manager.RecoveredAt 应与 Breaker.RecoveredAt 同源")
	}
}
