package session

import (
	"sync"
	"testing"
	"time"
)

// TestAtomicSessionConcurrentUpdates 测试并发更新的线程安全性
func TestAtomicSessionConcurrentUpdates(t *testing.T) {
	session := &Session{
		SessionID:  "test-session",
		TenantID:   "test-tenant",
		TotalTurns: 0,
	}

	atomicSession := NewAtomicSession(session)

	// 并发写入测试
	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				atomicSession.AddTurn(1)
				atomicSession.AddPromptTokens(10)
				atomicSession.AddCompletionTokens(20)
				atomicSession.AddCost(5)
			}
		}()
	}

	wg.Wait()

	// 验证结果
	expectedTurns := int64(goroutines * iterations)
	expectedPromptTokens := int64(goroutines * iterations * 10)
	expectedCompletionTokens := int64(goroutines * iterations * 20)
	expectedCost := int64(goroutines * iterations * 5)

	if got := atomicSession.GetTotalTurns(); got != expectedTurns {
		t.Errorf("TotalTurns = %d, want %d", got, expectedTurns)
	}

	if got := atomicSession.GetTotalPromptTokens(); got != expectedPromptTokens {
		t.Errorf("TotalPromptTokens = %d, want %d", got, expectedPromptTokens)
	}

	if got := atomicSession.GetTotalCompletionTokens(); got != expectedCompletionTokens {
		t.Errorf("TotalCompletionTokens = %d, want %d", got, expectedCompletionTokens)
	}

	if got := atomicSession.GetTotalCost(); got != expectedCost {
		t.Errorf("TotalCost = %d, want %d", got, expectedCost)
	}
}

// TestAtomicSessionBatchUpdate 测试批量更新
func TestAtomicSessionBatchUpdate(t *testing.T) {
	session := &Session{
		SessionID: "test-session",
		TenantID:  "test-tenant",
	}

	atomicSession := NewAtomicSession(session)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				atomicSession.UpdateTokensAndCost(100, 200, 50)
			}
		}()
	}

	wg.Wait()

	// 验证
	expected := int64(goroutines * 100)
	if got := atomicSession.GetTotalTurns(); got != expected {
		t.Errorf("TotalTurns = %d, want %d", got, expected)
	}
}

// TestAtomicSessionCredentialRotation 测试凭证轮转的线程安全性
func TestAtomicSessionCredentialRotation(t *testing.T) {
	session := &Session{
		SessionID: "test-session",
		TenantID:  "test-tenant",
	}

	atomicSession := NewAtomicSession(session)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// 模拟并发的凭证轮转
	for i := 0; i < goroutines; i++ {
		credID := i + 1
		go func(id int) {
			defer wg.Done()
			time.Sleep(time.Millisecond * time.Duration(id%10))
			atomicSession.ResetCredentialCounters(id)
		}(credID)
	}

	wg.Wait()

	// 验证最终状态一致性
	credID := atomicSession.GetCurrentCredentialID()
	if credID <= 0 || credID > goroutines {
		t.Errorf("CurrentCredentialID = %d, want 1-%d", credID, goroutines)
	}
}

// TestAtomicSessionReadWriteConcurrent 测试并发读写
func TestAtomicSessionReadWriteConcurrent(t *testing.T) {
	session := &Session{
		SessionID: "test-session",
		TenantID:  "test-tenant",
	}

	atomicSession := NewAtomicSession(session)

	const writers = 50
	const readers = 50
	const duration = time.Second

	done := make(chan struct{})
	var wg sync.WaitGroup

	// 启动写协程
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					atomicSession.AddTurn(1)
					atomicSession.SetCurrentModel("gpt-4")
					atomicSession.SetLastActive(time.Now())
				}
			}
		}()
	}

	// 启动读协程
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = atomicSession.GetTotalTurns()
					_ = atomicSession.GetCurrentModel()
					_ = atomicSession.GetLastActive()
				}
			}
		}()
	}

	// 运行一段时间
	time.Sleep(duration)
	close(done)
	wg.Wait()

	// 验证数据一致性
	turns := atomicSession.GetTotalTurns()
	if turns < 0 {
		t.Errorf("TotalTurns is negative: %d", turns)
	}
}

// BenchmarkAtomicSessionAddTurn 性能基准测试
func BenchmarkAtomicSessionAddTurn(b *testing.B) {
	session := &Session{SessionID: "bench"}
	atomicSession := NewAtomicSession(session)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			atomicSession.AddTurn(1)
		}
	})
}

// BenchmarkAtomicSessionUpdateTokensAndCost 批量更新性能测试
func BenchmarkAtomicSessionUpdateTokensAndCost(b *testing.B) {
	session := &Session{SessionID: "bench"}
	atomicSession := NewAtomicSession(session)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			atomicSession.UpdateTokensAndCost(100, 200, 50)
		}
	})
}

// BenchmarkAtomicSessionGetTotalTurns 读取性能测试
func BenchmarkAtomicSessionGetTotalTurns(b *testing.B) {
	session := &Session{SessionID: "bench"}
	atomicSession := NewAtomicSession(session)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = atomicSession.GetTotalTurns()
		}
	})
}
