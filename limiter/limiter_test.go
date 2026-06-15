package limiter

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewSemaphore(t *testing.T) {
	s := NewSemaphore("test", 10)
	if s.Capacity() != 10 {
		t.Fatalf("expected capacity 10, got %d", s.Capacity())
	}
	if s.Used() != 0 {
		t.Fatalf("expected used 0, got %d", s.Used())
	}
	if s.Available() != 10 {
		t.Fatalf("expected available 10, got %d", s.Available())
	}
}

func TestSemaphoreTryAcquireAndRelease(t *testing.T) {
	s := NewSemaphore("test", 2)

	if !s.TryAcquire() {
		t.Fatal("should acquire")
	}
	if !s.TryAcquire() {
		t.Fatal("should acquire")
	}
	if s.TryAcquire() {
		t.Fatal("should not acquire beyond capacity")
	}

	s.Release()
	if !s.TryAcquire() {
		t.Fatal("should acquire after release")
	}
}

func TestSemaphoreAcquireWithContext(t *testing.T) {
	s := NewSemaphore("test", 1)
	s.TryAcquire() // use the only slot

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Acquire(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestSemaphoreTryAcquireWithTimeout(t *testing.T) {
	s := NewSemaphore("test", 1)
	s.TryAcquire()

	if s.TryAcquireWithTimeout(50 * time.Millisecond) {
		t.Fatal("should not acquire within timeout")
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Release()
	}()

	if !s.TryAcquireWithTimeout(100 * time.Millisecond) {
		t.Fatal("should acquire after release")
	}
}

func TestSemaphoreShrink(t *testing.T) {
	s := NewSemaphore("test", 100)
	s.Shrink(0.7)
	if s.Capacity() != 70 {
		t.Fatalf("expected capacity 70 after shrink 0.7, got %d", s.Capacity())
	}

	// Shrink again
	s.Shrink(0.5)
	if s.Capacity() != 35 {
		t.Fatalf("expected capacity 35 after shrink 0.5, got %d", s.Capacity())
	}

	// Shrink to minimum
	s.Shrink(0.01)
	if s.Capacity() < 1 {
		t.Fatal("capacity should not go below 1")
	}
}

func TestSemaphoreRecoverStep(t *testing.T) {
	s := NewSemaphore("test", 100)
	s.Shrink(0.5) // 50

	s.RecoverStep(100)
	if s.Capacity() != 75 { // 50 + ceil(50*0.5)
		t.Fatalf("expected capacity 75 after recovery step, got %d", s.Capacity())
	}

	s.RecoverStep(100)
	if s.Capacity() != 88 { // 75 + ceil(25*0.5) ≈ 88
		t.Fatalf("expected capacity ~88, got %d", s.Capacity())
	}

	// Recover to full
	for s.Capacity() < 100 {
		s.RecoverStep(100)
	}
	if s.Capacity() != 100 {
		t.Fatalf("expected full recovery to 100, got %d", s.Capacity())
	}
}

func TestLimiterNew(t *testing.T) {
	l := New()
	defer l.Stop()

	if l.Global().Capacity() != DefaultGlobalLimit {
		t.Fatalf("expected global limit %d, got %d", DefaultGlobalLimit, l.Global().Capacity())
	}
}

func TestLimiterPoolSemaphore(t *testing.T) {
	l := New()
	defer l.Stop()

	s1 := l.Pool(1)
	s2 := l.Pool(1)
	if s1 != s2 {
		t.Fatal("Pool should return same instance for same provider")
	}

	s3 := l.Pool(2)
	if s1 == s3 {
		t.Fatal("Pool should return different instance for different provider")
	}
}

func TestLimiterCredentialSemaphore(t *testing.T) {
	l := New()
	defer l.Stop()

	s1 := l.Credential(1, 1)
	s2 := l.Credential(1, 1)
	if s1 != s2 {
		t.Fatal("Credential should return same instance")
	}

	s3 := l.Credential(1, 2)
	if s1 == s3 {
		t.Fatal("Credential should return different instance for different credential")
	}
}

func TestLimiterIdentitySemaphore(t *testing.T) {
	l := New()
	defer l.Stop()

	s1 := l.Identity(1, 1, "hash1")
	s2 := l.Identity(1, 1, "hash1")
	if s1 != s2 {
		t.Fatal("Identity should return same instance")
	}

	s3 := l.Identity(1, 1, "hash2")
	if s1 == s3 {
		t.Fatal("Identity should return different instance for different hash")
	}
}

func TestLimiterAcquireAllAndRelease(t *testing.T) {
	l := NewWithLimits(5, 3, 2, 1)
	defer l.Stop()

	ctx := context.Background()

	// Acquire all five layers
	release1, err := l.AcquireAll(ctx, 1, 1, "hash-a", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.Global().Used() != 1 {
		t.Fatalf("expected 1 global used, got %d", l.Global().Used())
	}

	// Acquire another — identity limit is 1, so identity layer should note it
	release2, err := l.AcquireAll(ctx, 1, 1, "hash-b", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if l.Global().Used() != 2 {
		t.Fatalf("expected 2 global used, got %d", l.Global().Used())
	}

	// Release both
	release1()
	release2()

	if l.Global().Used() != 0 {
		t.Fatalf("expected 0 global used after release, got %d", l.Global().Used())
	}
}

func TestLimiterAcquireBlocks(t *testing.T) {
	l := NewWithLimits(1, 1, 1, 1)
	defer l.Stop()

	ctx := context.Background()

	// Use the only global slot
	release1, err := l.AcquireAll(ctx, 1, 1, "hash-a", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second acquire should block, use timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = l.AcquireAll(timeoutCtx, 1, 1, "hash-b", 0, 0)
	if err == nil {
		t.Fatal("expected error when global limit reached")
	}

	release1()
}

func TestLimiterShrink(t *testing.T) {
	l := New()
	defer l.Stop()

	s := l.Credential(1, 1)
	originalCap := s.Capacity()
	l.Shrink(1, 1)

	newCap := s.Capacity()
	if newCap >= originalCap {
		t.Fatalf("expected capacity decrease, was %d, now %d", originalCap, newCap)
	}
}

func TestLimiterConcurrentAccess(t *testing.T) {
	l := New()
	defer l.Stop()

	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			release, err := l.AcquireAll(ctx, 1, 1, "hash-x", 0, 0)
			if err != nil {
				t.Errorf("goroutine %d: acquire error: %v", id, err)
				return
			}
			time.Sleep(10 * time.Millisecond)
			release()
		}(i)
	}

	wg.Wait()
}

func TestLimiterStats(t *testing.T) {
	l := New()
	defer l.Stop()

	l.Pool(1)
	l.Credential(1, 1)

	stats := l.Stats()

	global, ok := stats["global"].(map[string]int)
	if !ok {
		t.Fatal("expected global stats")
	}
	if global["capacity"] != DefaultGlobalLimit {
		t.Fatalf("expected global capacity %d, got %d", DefaultGlobalLimit, global["capacity"])
	}

	pools, ok := stats["pools"].([]map[string]any)
	if !ok || len(pools) != 1 {
		t.Fatalf("expected 1 pool entry, got %d", len(pools))
	}

	creds, ok := stats["credentials"].([]map[string]any)
	if !ok || len(creds) != 1 {
		t.Fatalf("expected 1 credential entry, got %d", len(creds))
	}
}

func TestLimiterRecoveryLoop(t *testing.T) {
	l := NewWithLimits(100, 10, 10, 5)
	defer l.Stop()

	s := l.Credential(1, 1)
	s.Shrink(0.5) // 5

	// Wait for recovery cycle
	time.Sleep(100 * time.Millisecond)

	// Recovery happens every 5 minutes naturally, but we can test the step directly
	l.recoveryStep()

	if s.Capacity() <= 5 {
		t.Fatalf("expected recovery after step, got capacity %d", s.Capacity())
	}
}
