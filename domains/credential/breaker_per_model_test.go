package credential

import (
	"testing"
)

// TestPerModelIsolation 验证单模型故障不连坐其他模型
// W1 缺口① (2026-11-19): per-model 隔离测试
func TestPerModelIsolation(t *testing.T) {
	mgr := NewManager()

	// 同一凭据，不同模型
	b1 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b2 := mgr.GetOrCreatePerModel(1, 100, "gpt-3.5-turbo")

	// 验证初始状态
	if b1.State() != StateClosed {
		t.Fatal("gpt-4 should start closed")
	}
	if b2.State() != StateClosed {
		t.Fatal("gpt-3.5-turbo should start closed")
	}

	// gpt-4 触发两次失败，进入 Open 状态
	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)

	// 验证隔离：gpt-4 Open，gpt-3.5-turbo 仍为 Closed
	if b1.State() != StateOpen {
		t.Fatalf("gpt-4 should be open after 2 failures, got %s", b1.State())
	}
	if b2.State() != StateClosed {
		t.Fatalf("gpt-3.5-turbo should still be closed, got %s", b2.State())
	}

	// 验证键格式
	if b1.Key() != "1/100/gpt-4" {
		t.Fatalf("expected key '1/100/gpt-4', got '%s'", b1.Key())
	}
	if b2.Key() != "1/100/gpt-3.5-turbo" {
		t.Fatalf("expected key '1/100/gpt-3.5-turbo', got '%s'", b2.Key())
	}
}

// TestCredentialStateRollup 验证 DB 上卷逻辑
// W1 缺口① (2026-11-19): 上卷态应为该凭据所有模型的最差状态
func TestCredentialStateRollup(t *testing.T) {
	mgr := NewManager()

	b1 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b2 := mgr.GetOrCreatePerModel(1, 100, "gpt-3.5-turbo")
	b3 := mgr.GetOrCreatePerModel(1, 100, "text-embedding-ada-002")

	// 初始上卷态应为 Closed
	if state := mgr.GetCredentialState(100); state != StateClosed {
		t.Fatalf("initial credential state should be closed, got %s", state)
	}

	// gpt-4 进入 Open
	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)

	// 上卷态应为 Open（最差）
	if state := mgr.GetCredentialState(100); state != StateOpen {
		t.Fatalf("credential state should be rolled up to Open, got %s", state)
	}

	// gpt-3.5-turbo 进入 Quarantined（更差，使用 KindQuota）
	b2.RecordFailure(KindQuota)
	b2.RecordFailure(KindQuota)

	// 上卷态应为 Quarantined（最差）
	if state := mgr.GetCredentialState(100); state != StateQuarantined {
		t.Fatalf("credential state should be rolled up to Quarantined, got %s", state)
	}

	// embedding 仍为 Closed
	if b3.State() != StateClosed {
		t.Fatalf("embedding should still be closed, got %s", b3.State())
	}
}

// TestGetModelState 验证按模型查询状态
// W1 缺口① (2026-11-19): 新增的按模型查询接口
func TestGetModelState(t *testing.T) {
	mgr := NewManager()

	// 查询不存在的模型，应返回 Closed
	if state := mgr.GetModelState(1, 100, "nonexistent-model"); state != StateClosed {
		t.Fatalf("nonexistent model should return closed, got %s", state)
	}

	// 创建并熔断一个模型
	b := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b.RecordFailure(KindUpstreamDown)
	b.RecordFailure(KindUpstreamDown)

	// 查询应返回 Open
	if state := mgr.GetModelState(1, 100, "gpt-4"); state != StateOpen {
		t.Fatalf("gpt-4 should be open, got %s", state)
	}

	// 查询其他模型，应返回 Closed
	if state := mgr.GetModelState(1, 100, "gpt-3.5-turbo"); state != StateClosed {
		t.Fatalf("gpt-3.5-turbo should be closed, got %s", state)
	}
}

// TestModelNameTrimming 验证模型名空格修剪
// W1 缺口① (2026-11-19): 确保空格被正确处理
func TestModelNameTrimming(t *testing.T) {
	mgr := NewManager()

	// 带空格的模型名
	b1 := mgr.GetOrCreatePerModel(1, 100, "  gpt-4  ")
	b2 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")

	// 应返回同一个实例（空格已修剪）
	if b1 != b2 {
		t.Fatal("trimmed model names should return the same breaker instance")
	}

	// 验证键格式（无空格）
	if b1.Key() != "1/100/gpt-4" {
		t.Fatalf("expected key '1/100/gpt-4', got '%s'", b1.Key())
	}
}

// TestBackwardCompatibility 验证向后兼容性
// W1 缺口① (2026-11-19): 旧接口应继续工作（测试用）
func TestBackwardCompatibility(t *testing.T) {
	mgr := NewManager()

	// 旧接口仍可用
	b := mgr.GetOrCreatePerModel(1, 100, "")
	if b == nil {
		t.Fatal("GetOrCreate should return a breaker")
	}

	// 验证 legacy 标记
	if b.model != "_legacy_" {
		t.Fatalf("legacy breaker should have model='_legacy_', got '%s'", b.model)
	}

	// 旧接口键格式（providerID/credentialID）
	if b.Key() != "1/100/_legacy_" {
		t.Fatalf("expected legacy key '1/100/_legacy_', got '%s'", b.Key())
	}
}

// TestMultipleCredentialsSameModel 验证不同凭据的同模型互相隔离
//
// ★ P0-a (2026-11-19 审计轮)：语义已翻转——旧设计是 per-provider-model，
// 同 provider 同模型的**所有凭据共享一把 breaker**，于是：
//
//	· 一次失败连坐该 provider 下所有凭据（跨凭据熔断）；
//	· 该实例的 credentialID 取决于「谁先创建」⇒ DB 上卷归错凭据。
//
// 现键为 "providerID/credentialID/model"，同模型不同凭据互不影响，
// 上卷也各自归因。
func TestMultipleCredentialsSameModel(t *testing.T) {
	mgr := NewManager()

	// 不同凭据，同模型
	b1 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b2 := mgr.GetOrCreatePerModel(1, 200, "gpt-4")

	// 必须是**不同**的实例（跨凭据隔离）
	if b1 == b2 {
		t.Fatal("same model on different credentials must NOT share a breaker (cross-credential contagion)")
	}

	// 凭据 100 的 gpt-4 熔断
	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)

	// 凭据 200 的 gpt-4 不受影响
	if b2.State() != StateClosed {
		t.Fatalf("credential 200 gpt-4 should stay closed, got %s", b2.State())
	}

	// 上卷：只归因到真正出错的凭据 100
	if state := mgr.GetCredentialState(100); state != StateOpen {
		t.Fatalf("credential 100 state should be open, got %s", state)
	}
	if state := mgr.GetCredentialState(200); state != StateClosed {
		t.Fatalf("credential 200 state should be closed (no cross-credential attribution), got %s", state)
	}
}
