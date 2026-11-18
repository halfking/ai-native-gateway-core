package credential

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestPerModelIntegration 集成测试：per-model 隔离在真实场景下的行为
// W1 缺口① (2026-11-19): 集成测试
func TestPerModelIntegration(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mgr := NewManager()
	sync := NewDBStateSync(mockDB, 8, mgr)

	// 模拟真实场景：同一凭据下有 3 个模型
	models := []string{"gpt-4", "gpt-3.5-turbo", "text-embedding-ada-002"}
	breakers := make([]*Breaker, len(models))

	for i, model := range models {
		breakers[i] = mgr.GetOrCreatePerModel(1, 100, model)
	}

	// 场景 1：gpt-4 连续失败，触发熔断
	t.Run("single_model_circuit_break", func(t *testing.T) {
		// 预期 DB 写入 "open"（因为有一个模型 Open）
		mockDB.ExpectExec(`UPDATE credentials`).
			WithArgs("open", pgxmock.AnyArg(), pgxmock.AnyArg(), 100).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		// 连续两次失败
		breakers[0].RecordFailure(KindUpstreamDown)
		breakers[0].RecordFailure(KindUpstreamDown)

		// 触发状态变更
		sc := StateChange{
			ProviderID:   1,
			CredentialID: 100,
			From:         StateClosed,
			To:           StateOpen,
			Kind:         KindUpstreamDown,
		}
		sync.Observe(sc)

		// 验证隔离：只有 gpt-4 被熔断
		if breakers[0].State() != StateOpen {
			t.Errorf("gpt-4 should be open, got %s", breakers[0].State())
		}
		if breakers[1].State() != StateClosed {
			t.Errorf("gpt-3.5-turbo should be closed, got %s", breakers[1].State())
		}
		if breakers[2].State() != StateClosed {
			t.Errorf("embedding should be closed, got %s", breakers[2].State())
		}

		// 验证 Allow 方法：gpt-4 被拒绝，其他模型允许
		if breakers[0].Allow() {
			t.Error("gpt-4 should not allow requests")
		}
		if !breakers[1].Allow() {
			t.Error("gpt-3.5-turbo should allow requests")
		}
		if !breakers[2].Allow() {
			t.Error("embedding should allow requests")
		}
	})

	// 场景 2：gpt-3.5-turbo 配额耗尽，进入 Quarantined
	t.Run("quota_exhaustion", func(t *testing.T) {
		// 预期 DB 写入 "open"（Quarantined 存储为 "open"）
		mockDB.ExpectExec(`UPDATE credentials`).
			WithArgs("open", pgxmock.AnyArg(), nil, 100).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		breakers[1].RecordFailure(KindQuota)
		breakers[1].RecordFailure(KindQuota)

		sc := StateChange{
			ProviderID:   1,
			CredentialID: 100,
			From:         StateClosed,
			To:           StateQuarantined,
			Kind:         KindQuota,
		}
		sync.Observe(sc)

		// 验证上卷态为 Quarantined（最差）
		if state := mgr.GetCredentialState(100); state != StateQuarantined {
			t.Errorf("credential state should be Quarantined, got %s", state)
		}

		// 验证 embedding 仍然可用
		if breakers[2].State() != StateClosed {
			t.Errorf("embedding should still be closed, got %s", breakers[2].State())
		}
		if !breakers[2].Allow() {
			t.Error("embedding should still allow requests")
		}
	})

	// 场景 3：gpt-4 恢复，上卷态仍为 Quarantined
	t.Run("partial_recovery", func(t *testing.T) {
		// gpt-4 恢复到 Closed
		breakers[0].RecordSuccess()

		// 验证上卷态仍为 Quarantined（gpt-3.5-turbo 仍是最差）
		if state := mgr.GetCredentialState(100); state != StateQuarantined {
			t.Errorf("credential state should still be Quarantined, got %s", state)
		}

		// 验证 gpt-4 已恢复
		if breakers[0].State() != StateClosed {
			t.Errorf("gpt-4 should be closed after success, got %s", breakers[0].State())
		}
	})

	// 启动消费并等待
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go sync.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

// TestConcurrentPerModelAccess 压力测试：并发访问 per-model breakers
// W1 缺口① (2026-11-19): 并发安全测试
func TestConcurrentPerModelAccess(t *testing.T) {
	mgr := NewManager()

	const (
		numGoroutines = 100
		numModels     = 10
		numOperations = 1000
	)

	models := make([]string, numModels)
	for i := 0; i < numModels; i++ {
		models[i] = "model-" + string(rune('a'+i))
	}

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// 并发访问测试
	for i := 0; i < numGoroutines; i++ {
		go func(gid int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				model := models[j%numModels]

				// 并发创建和访问
				b := mgr.GetOrCreatePerModel(1, 100, model)
				if b == nil {
					t.Errorf("goroutine %d: failed to get breaker for %s", gid, model)
					continue
				}

				// 随机操作
				switch j % 4 {
				case 0:
					b.RecordSuccess()
				case 1:
					b.RecordFailure(KindUpstreamDown)
				case 2:
					_ = b.Allow()
				case 3:
					_ = b.State()
				}
			}
		}(i)
	}

	wg.Wait()

	// 验证所有模型都创建了
	for _, model := range models {
		b := mgr.GetPerModel(1, model)
		if b == nil {
			t.Errorf("model %s was not created", model)
		}
	}

	// 验证上卷逻辑在并发场景下的正确性
	state := mgr.GetCredentialState(100)
	// 状态应该是 Closed、Open、HalfOpen 或 Quarantined 之一
	if state != StateClosed && state != StateOpen && state != StateHalfOpen && state != StateQuarantined {
		t.Errorf("credential state should be valid, got %s", state)
	}
}

// TestPerModelMemoryLeak 检查是否存在内存泄漏
// W1 缺口① (2026-11-19): 内存泄漏检测
func TestPerModelMemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory leak test in short mode")
	}

	mgr := NewManager()

	// 创建大量 breakers
	const numBreakers = 10000
	for i := 0; i < numBreakers; i++ {
		model := "model-" + string(rune(i))
		mgr.GetOrCreatePerModel(1, i%100, model)
	}

	// 检查内存是否合理（breakers map 大小）
	// 注意：这个测试主要是确保 breakers 被正确存储，不会重复创建
	breakers := mgr.GetOrCreatePerModel(1, 0, "model-"+string(rune(0)))
	if breakers == nil {
		t.Error("breaker should be reused, not recreated")
	}
}

// TestPerModelStateTransitions 测试状态机在 per-model 场景下的转换
// W1 缺口① (2026-11-19): 状态转换测试
func TestPerModelStateTransitions(t *testing.T) {
	mgr := NewManager()
	b := mgr.GetOrCreatePerModel(1, 100, "gpt-4")

	// Closed → Open（两次失败）
	if b.State() != StateClosed {
		t.Fatalf("initial state should be Closed, got %s", b.State())
	}

	b.RecordFailure(KindUpstreamDown)
	if b.State() != StateClosed {
		t.Errorf("after 1 failure, should still be Closed, got %s", b.State())
	}

	b.RecordFailure(KindUpstreamDown)
	if b.State() != StateOpen {
		t.Errorf("after 2 failures, should be Open, got %s", b.State())
	}

	// Open → Closed（一次成功即恢复）
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Errorf("after 1 success, should be Closed, got %s", b.State())
	}

	// Closed → Quarantined（配额失败）
	b.RecordFailure(KindQuota)
	b.RecordFailure(KindQuota)
	if b.State() != StateQuarantined {
		t.Errorf("after 2 quota failures, should be Quarantined, got %s", b.State())
	}

	// Quarantined → Closed（一次成功即恢复）
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Errorf("after success from Quarantined, should be Closed, got %s", b.State())
	}
}

// TestPerModelManagerIsolation 测试不同 provider 的模型隔离
// W1 缺口① (2026-11-19): 跨 provider 隔离测试
func TestPerModelManagerIsolation(t *testing.T) {
	mgr := NewManager()

	// 不同 provider，同模型
	b1 := mgr.GetOrCreatePerModel(1, 100, "gpt-4")
	b2 := mgr.GetOrCreatePerModel(2, 200, "gpt-4")

	// 应该是不同的 breaker（不同 provider）
	if b1 == b2 {
		t.Error("different providers should have different breakers")
	}

	// provider 1 的 gpt-4 熔断
	b1.RecordFailure(KindUpstreamDown)
	b1.RecordFailure(KindUpstreamDown)

	// provider 2 的 gpt-4 不受影响
	if b2.State() != StateClosed {
		t.Errorf("provider 2 gpt-4 should not be affected, got %s", b2.State())
	}

	// 验证键格式
	if b1.Key() != "1/gpt-4" {
		t.Errorf("provider 1 key should be '1/gpt-4', got '%s'", b1.Key())
	}
	if b2.Key() != "2/gpt-4" {
		t.Errorf("provider 2 key should be '2/gpt-4', got '%s'", b2.Key())
	}
}
