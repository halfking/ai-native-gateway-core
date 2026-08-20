package distlock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRedisManager_FollowerIgnoresSpoofedRelease(t *testing.T) {
	m, _, rdb := newRedisManagerForTest(t)
	ctx := context.Background()
	leader, err := m.Acquire(ctx, AcquireOpts{Key: "spoofed-release", TTL: time.Second})
	if err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	defer leader.Release(ctx)
	follower, err := m.Acquire(ctx, AcquireOpts{Key: "spoofed-release", TTL: time.Second})
	if err != nil {
		t.Fatalf("follower Acquire: %v", err)
	}
	defer follower.Release(ctx)

	waitDone := make(chan error, 1)
	go func() { waitDone <- follower.Wait(ctx) }()
	if err := rdb.Publish(ctx, "spoofed-release"+releaseChannelSuffix, "not-the-owner").Err(); err != nil {
		t.Fatalf("Publish spoofed release: %v", err)
	}
	select {
	case err := <-waitDone:
		t.Fatalf("spoofed release woke follower: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	leader.Release(ctx)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("follower Wait after real release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not wake after real release")
	}
}

func TestRedisManager_ConcurrentWaitersShareTerminalResult(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	ctx := context.Background()
	leader, err := m.Acquire(ctx, AcquireOpts{Key: "broadcast-wait", TTL: time.Second})
	if err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	defer leader.Release(ctx)
	follower, err := m.Acquire(ctx, AcquireOpts{Key: "broadcast-wait", TTL: time.Second})
	if err != nil {
		t.Fatalf("follower Acquire: %v", err)
	}
	defer follower.Release(ctx)

	const waiters = 4
	results := make(chan error, waiters)
	var wg sync.WaitGroup
	wg.Add(waiters)
	for range waiters {
		go func() {
			defer wg.Done()
			results <- follower.Wait(ctx)
		}()
	}
	leader.Release(ctx)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Wait: %v", err)
		}
	}
}

func TestRedisManager_CheckDetectsLostLease(t *testing.T) {
	m, mr, _ := newRedisManagerForTest(t)
	ctx := context.Background()
	leader, err := m.Acquire(ctx, AcquireOpts{Key: "lease-check", TTL: time.Second})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	mr.FastForward(2 * time.Second)
	if err := leader.Check(ctx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Check after expiry: want ErrLeaseLost, got %v", err)
	}
}

func TestLocalManager_FollowerHasNoTTL(t *testing.T) {
	m := NewLocalManager()
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "local-no-ttl", TTL: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	defer leader.Release(context.Background())
	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "local-no-ttl", TTL: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("follower Acquire: %v", err)
	}
	defer follower.Release(context.Background())

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := follower.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("local follower Wait: want context deadline, got %v", err)
	}
}
