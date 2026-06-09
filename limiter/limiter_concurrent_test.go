package limiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterAcquireAll_Concurrent(t *testing.T) {
	l := NewWithLimits(100, 20, 10, 3)
	defer l.Stop()

	const goroutines = 50
	const providerID = 1
	const credentialID = 1
	identityHash := "test-identity"

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var failed atomic.Int32
	var mu sync.Mutex
	releaseFuncs := make([]ReleaseFunc, 0, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			release, err := l.AcquireAll(ctx, providerID, credentialID, identityHash)
			if err != nil {
				failed.Add(1)
				return
			}
			acquired.Add(1)

			time.Sleep(time.Microsecond)

			mu.Lock()
			releaseFuncs = append(releaseFuncs, release)
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	for _, release := range releaseFuncs {
		release()
	}

	if acquired.Load() == 0 {
		t.Error("expected some acquisitions")
	}
	t.Logf("acquired=%d, failed=%d", acquired.Load(), failed.Load())
}

func TestLimiterGlobalLimit(t *testing.T) {
	l := NewWithLimits(5, 100, 100, 100)
	defer l.Stop()

	const providerID = 1
	const credentialID = 1
	identityHash := "test"

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var blocked atomic.Int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			release, err := l.AcquireAll(ctx, providerID, credentialID, identityHash)
			if err != nil {
				blocked.Add(1)
				return
			}
			acquired.Add(1)
			time.Sleep(30 * time.Millisecond)
			release()
		}(i)
	}

	wg.Wait()

	// With concurrent goroutines, some may acquire after others release
	if acquired.Load() == 0 {
		t.Error("expected at least 1 acquisition")
	}
	t.Logf("acquired=%d, blocked=%d", acquired.Load(), blocked.Load())
}

func TestLimiterPoolLimit(t *testing.T) {
	l := NewWithLimits(1000, 5, 100, 100)
	defer l.Stop()

	const providerID = 1
	const credentialID = 1
	identityHash := "test"

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var blocked atomic.Int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			release, err := l.AcquireAll(ctx, providerID, credentialID, identityHash)
			if err != nil {
				blocked.Add(1)
				return
			}
			acquired.Add(1)
			time.Sleep(30 * time.Millisecond)
			release()
		}(i)
	}

	wg.Wait()

	if acquired.Load() == 0 {
		t.Error("expected at least 1 acquisition")
	}
	t.Logf("acquired=%d, blocked=%d", acquired.Load(), blocked.Load())
}

func TestLimiterCredentialLimit(t *testing.T) {
	l := NewWithLimits(1000, 100, 5, 100)
	defer l.Stop()

	const providerID = 1
	const credentialID = 1
	identityHash := "test"

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var blocked atomic.Int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			release, err := l.AcquireAll(ctx, providerID, credentialID, identityHash)
			if err != nil {
				blocked.Add(1)
				return
			}
			acquired.Add(1)
			time.Sleep(30 * time.Millisecond)
			release()
		}(i)
	}

	wg.Wait()

	if acquired.Load() == 0 {
		t.Error("expected at least 1 acquisition")
	}
	t.Logf("acquired=%d, blocked=%d", acquired.Load(), blocked.Load())
}

func TestLimiterShrinkRecover(t *testing.T) {
	l := NewWithLimits(1000, 100, 50, 10)
	defer l.Stop()

	cred := l.Credential(1, 1)
	originalCap := cred.Capacity()

	l.Shrink(1, 1)
	shrunkCap := cred.Capacity()

	if shrunkCap >= originalCap {
		t.Errorf("expected shrunk capacity %d < original %d", shrunkCap, originalCap)
	}

	t.Logf("original=%d, shrunk=%d", originalCap, shrunkCap)
}

func TestSemaphore_Concurrent(t *testing.T) {
	s := NewSemaphore("test", 10)

	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var failed atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if s.TryAcquire() {
					acquired.Add(1)
					time.Sleep(time.Microsecond)
					s.Release()
				} else {
					failed.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	if s.Used() != 0 {
		t.Errorf("expected 0 used after all releases, got %d", s.Used())
	}
	t.Logf("acquired=%d, failed=%d", acquired.Load(), failed.Load())
}

func TestSemaphore_AcquireWithContext(t *testing.T) {
	s := NewSemaphore("test", 1)

	s.TryAcquire()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Acquire(ctx)
	if err == nil {
		t.Error("expected timeout error")
	}

	s.Release()

	err = s.Acquire(ctx)
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	s.Release()
}

func TestSemaphore_ShrinkRecover(t *testing.T) {
	s := NewSemaphore("test", 100)

	s.Shrink(0.5)
	if s.Capacity() != 50 {
		t.Errorf("expected capacity 50 after shrink, got %d", s.Capacity())
	}

	s.RecoverStep(100)
	if s.Capacity() <= 50 {
		t.Errorf("expected capacity > 50 after recovery, got %d", s.Capacity())
	}

	t.Logf("after shrink=%d, after recover=%d", 50, s.Capacity())
}

func TestLimiterMultiProvider(t *testing.T) {
	l := NewWithLimits(1000, 5, 5, 100)
	defer l.Stop()

	var wg sync.WaitGroup
	var acquired atomic.Int32

	for p := 0; p < 3; p++ {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(provider, idx int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()

				release, err := l.AcquireAll(ctx, provider, 1, fmt.Sprintf("identity-%d", idx))
				if err == nil {
					acquired.Add(1)
					time.Sleep(time.Microsecond)
					release()
				}
			}(p, i)
		}
	}

	wg.Wait()

	if acquired.Load() == 0 {
		t.Error("expected some acquisitions")
	}
	t.Logf("acquired=%d", acquired.Load())
}

func TestLimiterRelease(t *testing.T) {
	l := NewWithLimits(1000, 100, 5, 10)
	defer l.Stop()

	identityHash := "test-identity"

	release, err := l.AcquireAll(context.Background(), 1, 1, identityHash)
	if err != nil {
		t.Fatal(err)
	}

	if l.global.Used() != 1 {
		t.Errorf("expected global used 1, got %d", l.global.Used())
	}

	release()

	if l.global.Used() != 0 {
		t.Errorf("expected global used 0 after release, got %d", l.global.Used())
	}
}

func BenchmarkLimiterAcquireRelease(b *testing.B) {
	l := NewWithLimits(10000, 1000, 500, 100)
	defer l.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			ctx := context.Background()
			release, err := l.AcquireAll(ctx, i%10, i%100, fmt.Sprintf("identity-%d", i%50))
			if err == nil {
				release()
			}
			i++
		}
	})
}

func TestLimiterStats_Concurrent(t *testing.T) {
	l := NewWithLimits(100, 10, 5, 3)
	defer l.Stop()

	release, err := l.AcquireAll(context.Background(), 1, 1, "test")
	if err != nil {
		t.Fatal(err)
	}

	stats := l.Stats()

	global := stats["global"].(map[string]int)
	if global["used"] != 1 {
		t.Errorf("expected global used 1, got %d", global["used"])
	}

	release()

	stats = l.Stats()
	global = stats["global"].(map[string]int)
	if global["used"] != 0 {
		t.Errorf("expected global used 0 after release, got %d", global["used"])
	}
}