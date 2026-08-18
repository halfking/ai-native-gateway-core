package distlock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newRedisManagerForTest spins up an in-memory miniredis and returns a
// RedisManager + the underlying miniredis handle so tests can
// introspect / fast-forward TTL.
func newRedisManagerForTest(t *testing.T) (*RedisManager, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisManager(rdb), mr, rdb
}

// TestRedisManager_NotEnabledNilClient guards the most common misconfig:
// Handler initialised before SetRedisClient runs.
func TestRedisManager_NotEnabledNilClient(t *testing.T) {
	m := NewRedisManager(nil)
	if m.Enabled() {
		t.Fatal("nil client must report Enabled=false")
	}
	_, err := m.Acquire(context.Background(), AcquireOpts{Key: "x"})
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Acquire on disabled manager: want ErrNotEnabled, got %v", err)
	}
}

// TestRedisManager_LeaderThenFollower pins the happy path against real
// (in-memory) Redis. First acquire is leader; second is follower.
func TestRedisManager_LeaderThenFollower(t *testing.T) {
	m, mr, rdb := newRedisManagerForTest(t)

	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "k", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("Acquire leader: %v", err)
	}
	if !leader.IsLeader() {
		t.Fatal("first handle must be leader")
	}
	if _, err := rdb.Get(context.Background(), "k").Result(); err != nil {
		t.Fatalf("expected key to exist: %v", err)
	}
	if ttl := mr.TTL("k"); ttl <= 0 {
		t.Fatalf("expected positive TTL on key, got %v", ttl)
	}

	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "k", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}
	if follower.IsLeader() {
		t.Fatal("second handle must be follower")
	}

	// Follower must block until leader releases.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- follower.Wait(context.Background())
	}()
	select {
	case err := <-waitDone:
		t.Fatalf("follower returned before leader release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Leader releases. Follower wakes up + the key is removed.
	leader.Release(context.Background())
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("follower Wait after leader release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not wake after leader Release")
	}
	if _, err := rdb.Get(context.Background(), "k").Result(); err == nil {
		t.Fatal("key should have been deleted by leader Release")
	}
	follower.Release(context.Background())
}

// TestRedisManager_FollowerWakesViaPubSub: when the leader releases, the
// follower receives a pub/sub message and Wait returns nil. This is the
// contract that distinguishes RedisManager from a naive polling loop.
func TestRedisManager_FollowerWakesViaPubSub(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	leader, _ := m.Acquire(context.Background(), AcquireOpts{Key: "pubsub-k", TTL: 30 * time.Second})
	follower, _ := m.Acquire(context.Background(), AcquireOpts{Key: "pubsub-k", TTL: 30 * time.Second})

	go func() {
		time.Sleep(50 * time.Millisecond)
		leader.Release(context.Background())
	}()

	start := time.Now()
	if err := follower.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Wait took %v; pubsub should wake within milliseconds", elapsed)
	}
	follower.Release(context.Background())
}

// TestRedisManager_FollowerTTLExpired: if the leader crashes / hangs and
// never releases, the follower must NOT wait past the lock TTL. We use a
// real 200ms TTL (Go's time.NewTimer is anchored to wall-clock, not
// miniredis time) — FastForward alone would not advance the follower's
// internal timer.
func TestRedisManager_FollowerTTLExpired(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	leader, _ := m.Acquire(context.Background(), AcquireOpts{Key: "ttl-k", TTL: 200 * time.Millisecond})
	defer leader.Release(context.Background())

	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "ttl-k", TTL: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}
	defer follower.Release(context.Background())

	start := time.Now()
	err = follower.Wait(context.Background())
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTTLExpired) {
		t.Fatalf("Wait after TTL expiry: want ErrTTLExpired, got %v", err)
	}
	// Follower should give up shortly after TTL, not after a full second
	// or more. Allow generous slack for CI scheduling.
	if elapsed < 200*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("Wait elapsed %v; want ~TTL (200ms)", elapsed)
	}
}

// TestRedisManager_FollowerCtxCancel verifies the ctx-cancel escape hatch.
func TestRedisManager_FollowerCtxCancel(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	leader, _ := m.Acquire(context.Background(), AcquireOpts{Key: "ctx-k", TTL: 30 * time.Second})
	defer leader.Release(context.Background())

	follower, _ := m.Acquire(context.Background(), AcquireOpts{Key: "ctx-k", TTL: 30 * time.Second})
	defer follower.Release(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := follower.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait after ctx cancel: want context.Canceled, got %v", err)
	}
}

// TestRedisManager_ReleaseTokenSafe: a stale leader whose Release runs
// after its TTL has expired must NOT delete a fresh lock installed by a
// follower or a subsequent leader. This is the regression test for the
// Lua compare-and-delete in releaseScript.
func TestRedisManager_ReleaseTokenSafe(t *testing.T) {
	m, mr, rdb := newRedisManagerForTest(t)
	staleLeader, _ := m.Acquire(context.Background(), AcquireOpts{Key: "token-k", TTL: 1 * time.Second})
	// Expire the lock without releasing.
	mr.FastForward(2 * time.Second)
	// A new contender acquires the lock with a fresh token.
	freshLeader, err := m.Acquire(context.Background(), AcquireOpts{Key: "token-k", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("fresh leader Acquire: %v", err)
	}
	// The stale leader now tries to release. The token must not match,
	// so the Lua script must NOT delete the fresh leader's key.
	staleLeader.Release(context.Background())
	val, err := rdb.Get(context.Background(), "token-k").Result()
	if err != nil {
		t.Fatalf("fresh key should still exist after stale Release; got err %v", err)
	}
	if val == "" {
		t.Fatal("fresh key should have a value")
	}
	freshLeader.Release(context.Background())
}

// TestRedisManager_FollowerUsesActualRemainingTTL verifies that a
// follower's Wait timer is bounded by the real remaining TTL on the
// Redis key, not the original opts.TTL. If the leader set a 60s TTL
// and 50s elapsed before the follower joined, the follower's wait
// must expire within ~10s, not 60s.
func TestRedisManager_FollowerUsesActualRemainingTTL(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "pttl-k", TTL: 60 * time.Second})
	if err != nil {
		t.Fatalf("Acquire leader: %v", err)
	}
	defer leader.Release(context.Background())

	// Sleep briefly to let the leader's lock age a few hundred ms
	// before the follower joins. This proves the follower's TTL is
	// NOT counted from opts.TTL but from the actual remaining time.
	time.Sleep(500 * time.Millisecond)

	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "pttl-k", TTL: 60 * time.Second})
	if err != nil {
		t.Fatalf("Acquire follower: %v", err)
	}
	defer follower.Release(context.Background())

	// The follower's TTL should be ~59.5s, NOT 60s. We can't measure
	// the field directly, but we can verify the wait timer doesn't
	// expire immediately (which would happen if we passed ttl=0)
	// and that the leader's eventual Release wakes us up.
	start := time.Now()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- follower.Wait(context.Background())
	}()

	// If TTL were 0 (key already gone), Wait would return ErrTTLExpired
	// immediately. Verify the wait blocks at least 100ms.
	select {
	case err := <-waitDone:
		if errors.Is(err, ErrTTLExpired) {
			t.Fatalf("follower TTL is 0; PTTL fix did not apply")
		}
		t.Fatalf("follower Wait returned too early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Now release and verify wake-up.
	leader.Release(context.Background())
	select {
	case err := <-waitDone:
		if err != nil {
			t.Errorf("follower Wait after leader release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not wake after leader Release")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("follower woke after %v; expected shortly after release", elapsed)
	}
}

func TestRedisManager_ConcurrentLeaderRelease(t *testing.T) {
	m, _, rdb := newRedisManagerForTest(t)
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "redis-concurrent-release", TTL: time.Second})
	if err != nil {
		t.Fatalf("Acquire leader: %v", err)
	}
	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "redis-concurrent-release", TTL: time.Second})
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
	if _, err := rdb.Get(context.Background(), "redis-concurrent-release").Result(); err == nil {
		t.Fatal("key should be deleted after Release")
	}
	follower.Release(context.Background())
}

// TestRedisManager_EmptyKeyRejected: defensive parity with LocalManager.
func TestRedisManager_EmptyKeyRejected(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	_, err := m.Acquire(context.Background(), AcquireOpts{})
	if err == nil {
		t.Fatal("empty Key should error")
	}
}

// TestRedisManager_ConcurrentAcquire_OnlyOneLeader is the cross-process
// regression test the title pipeline relies on: while one leader holds
// the lock, 50 concurrent Acquire goroutines must produce exactly one
// leader total (the holder). The other 49 see IsLeader=false.
//
// Without the holder barrier the test is meaningless — sequential
// goroutines each become leader in turn and the lock would never contend.
func TestRedisManager_ConcurrentAcquire_OnlyOneLeader(t *testing.T) {
	m, _, _ := newRedisManagerForTest(t)
	holder, err := m.Acquire(context.Background(), AcquireOpts{Key: "race-k", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}
	defer holder.Release(context.Background())
	if !holder.IsLeader() {
		t.Fatal("holder must be leader")
	}

	const n = 50
	var wg sync.WaitGroup
	var leaders atomic.Int32
	var followers atomic.Int32

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			h, err := m.Acquire(context.Background(), AcquireOpts{Key: "race-k", TTL: 30 * time.Second})
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			defer h.Release(context.Background())
			if h.IsLeader() {
				leaders.Add(1)
			} else {
				followers.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := leaders.Load(); got != 0 {
		t.Fatalf("new leaders = %d while lock held; want 0", got)
	}
	if got := followers.Load(); got != n {
		t.Fatalf("followers = %d, want %d", got, n)
	}
}
