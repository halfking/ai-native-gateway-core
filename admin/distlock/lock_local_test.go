package distlock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLocalManager_AcquireBecomesLeader pins the happy path: first
// Acquire on a key returns a leader handle; the protected operation runs
// exactly once; the deferred Release unblocks any waiter.
func TestLocalManager_AcquireBecomesLeader(t *testing.T) {
	m := NewLocalManager()
	h, err := m.Acquire(context.Background(), AcquireOpts{Key: "k1"})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !h.IsLeader() {
		t.Fatal("expected leader=true on first Acquire")
	}
	if err := h.Wait(context.Background()); err != nil {
		t.Fatalf("leader Wait should be no-op; got %v", err)
	}
	h.Release(context.Background())
}

// TestLocalManager_FollowerWaitsUntilLeaderReleases guards the core
// promise of the module: a second Acquire on the same key does NOT run
// the protected operation while the leader holds the lock.
func TestLocalManager_FollowerWaitsUntilLeaderReleases(t *testing.T) {
	m := NewLocalManager()
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "k2"})
	if err != nil {
		t.Fatalf("Acquire leader: %v", err)
	}
	if !leader.IsLeader() {
		t.Fatal("first handle must be leader")
	}

	// Follower acquires while leader holds the lock.
	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "k2"})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}
	if follower.IsLeader() {
		t.Fatal("second handle must be follower")
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- follower.Wait(context.Background())
	}()

	// Confirm follower is still waiting after a short delay.
	select {
	case <-waitDone:
		t.Fatal("follower returned before leader released")
	case <-time.After(50 * time.Millisecond):
	}

	// Now release the leader; follower must wake up.
	leader.Release(context.Background())
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("follower Wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not wake after leader Release")
	}

	follower.Release(context.Background())

	// After both released, a fresh Acquire on the same key becomes leader again.
	next, err := m.Acquire(context.Background(), AcquireOpts{Key: "k2"})
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if !next.IsLeader() {
		t.Fatal("post-release Acquire must be leader")
	}
	next.Release(context.Background())
}

func TestLocalManager_ConcurrentLeaderRelease(t *testing.T) {
	m := NewLocalManager()
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "concurrent-release"})
	if err != nil {
		t.Fatalf("Acquire leader: %v", err)
	}
	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "concurrent-release"})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- follower.Wait(context.Background()) }()

	const releasers = 16
	var wg sync.WaitGroup
	wg.Add(releasers)
	for release := 0; release < releasers; release++ {
		go func() {
			defer wg.Done()
			leader.Release(context.Background())
		}()
	}
	wg.Wait()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("follower Wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not wake after concurrent Release")
	}
	follower.Release(context.Background())
}

// TestLocalManager_DifferentKeysIndependent: locks on different keys do
// not contend with each other.
func TestLocalManager_DifferentKeysIndependent(t *testing.T) {
	m := NewLocalManager()
	a, _ := m.Acquire(context.Background(), AcquireOpts{Key: "alpha"})
	b, _ := m.Acquire(context.Background(), AcquireOpts{Key: "beta"})
	if !a.IsLeader() || !b.IsLeader() {
		t.Fatalf("both should be leaders on different keys; got a=%v b=%v", a.IsLeader(), b.IsLeader())
	}
	a.Release(context.Background())
	b.Release(context.Background())
}

// TestLocalManager_FollowerCancelReturnsCtxErr: a follower whose context
// is cancelled while waiting must return ctx.Err(), not block forever.
func TestLocalManager_FollowerCancelReturnsCtxErr(t *testing.T) {
	m := NewLocalManager()
	leader, _ := m.Acquire(context.Background(), AcquireOpts{Key: "cancel-me"})
	defer leader.Release(context.Background())

	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "cancel-me"})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := follower.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("follower Wait after ctx cancel: want context.Canceled, got %v", err)
	}
	follower.Release(context.Background())
}

// TestLocalManager_ProtectedWorkRunsOnce is the regression test for the
// original bug: when N goroutines race to acquire the same key WHILE a
// leader is still holding the lock, the protected operation runs exactly
// once. (Without the holder barrier the test is meaningless — sequential
// goroutines each become leader in turn and the lock would never contend.)
func TestLocalManager_ProtectedWorkRunsOnce(t *testing.T) {
	m := NewLocalManager()
	holder, err := m.Acquire(context.Background(), AcquireOpts{Key: "single-key"})
	if err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}
	defer holder.Release(context.Background())
	if !holder.IsLeader() {
		t.Fatal("holder must be leader")
	}

	const n = 5
	var execCount atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			h, err := m.Acquire(context.Background(), AcquireOpts{Key: "single-key"})
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			defer h.Release(context.Background())
			if !h.IsLeader() {
				return // skip — this is the desired behaviour
			}
			execCount.Add(1)
		}()
	}
	wg.Wait()
	if got := execCount.Load(); got != 0 {
		t.Fatalf("protected work ran %d times while leader held lock; want 0", got)
	}
}

// TestLocalManager_EmptyKeyRejected ensures defensive parity with
// RedisManager.
func TestLocalManager_EmptyKeyRejected(t *testing.T) {
	m := NewLocalManager()
	_, err := m.Acquire(context.Background(), AcquireOpts{})
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("empty Key: want ErrNotEnabled, got %v", err)
	}
}

// TestLocalManager_Enabled: LocalManager is always usable.
func TestLocalManager_Enabled(t *testing.T) {
	m := NewLocalManager()
	if !m.Enabled() {
		t.Fatal("LocalManager.Enabled must be true")
	}
}

// TestHandle_NilSafeMethods: a nil Handle must not panic on any of the
// public methods, so defer h.Release(...) in caller code is safe.
func TestHandle_NilSafeMethods(t *testing.T) {
	var h *Handle
	if h.IsLeader() {
		t.Fatal("nil Handle.IsLeader must be false")
	}
	// Wait and Release on nil must not panic.
	if err := h.Wait(context.Background()); err != nil {
		t.Fatalf("nil Wait: %v", err)
	}
	h.Release(context.Background())
}
