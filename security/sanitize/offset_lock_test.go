package sanitize

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOffsetTestMiddleware(t *testing.T, rdb *redis.Client, detector Detector) *SanitizeInputMiddleware {
	t.Helper()
	s, err := NewSanitizer(detector)
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)
	return mw
}

func callOffsetTestMiddleware(mw *SanitizeInputMiddleware, tenant, session string, phone ...string) (int, string, bool) {
	body := `{"model":"m","messages":[{"role":"user","content":"call 13800138000"}]}`
	if len(phone) > 0 {
		body = strings.Replace(body, "13800138000", phone[0], 1)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", session)
	req = req.WithContext(WithAuthenticatedTenant(req.Context(), tenant))
	called := false
	var forwarded string
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		payload, _ := io.ReadAll(r.Body)
		forwarded = string(payload)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code, forwarded, called
}

func TestSanitizeOffsetsAcrossMiddlewareInstances(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	middlewares := []*SanitizeInputMiddleware{
		newOffsetTestMiddleware(t, rdb, NewPatternDetector()),
		newOffsetTestMiddleware(t, rdb, NewPatternDetector()),
	}
	const rounds = 12
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]int, 2)
		for i, mw := range middlewares {
			wg.Add(1)
			go func(i int, mw *SanitizeInputMiddleware) {
				defer wg.Done()
				<-start
				status, forwarded, called := callOffsetTestMiddleware(mw, "tenant-two-instances", "session-two-instances", fmt.Sprintf("138%08d", round*2+i))
				if !called || strings.Contains(forwarded, "13800138000") {
					results[i] = -status
					return
				}
				results[i] = status
			}(i, mw)
		}
		close(start)
		wg.Wait()
		require.Equal(t, []int{http.StatusOK, http.StatusOK}, results)
	}
	key := SanitizeRedisKey(HashTenant("tenant-two-instances"), "session-two-instances")
	values, err := rdb.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	require.Len(t, values, rounds*2)
	offsetKey := SanitizeOffsetRedisKey(HashTenant("tenant-two-instances"), "session-two-instances")
	require.Equal(t, "24", rdb.HGet(context.Background(), offsetKey, "phone").Val())
}

func TestSanitizeOffsetsBusyOrRedisFaultStopsDispatch(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	mw := newOffsetTestMiddleware(t, rdb, NewPatternDetector())
	const tenant, session = "tenant-busy", "session-busy"
	offsetKey := SanitizeOffsetRedisKey(HashTenant(tenant), session)
	lockKey := offsetKey + ":lock"
	require.NoError(t, rdb.Set(context.Background(), lockKey, "other-worker", time.Minute).Err())
	status, _, called := callOffsetTestMiddleware(mw, tenant, session)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.False(t, called)
	require.False(t, mini.Exists(SanitizeRedisKey(HashTenant(tenant), session)))
	require.NoError(t, rdb.Del(context.Background(), lockKey).Err())
	require.NoError(t, rdb.Set(context.Background(), offsetKey, "wrongtype", time.Minute).Err())
	status, _, called = callOffsetTestMiddleware(mw, tenant, session)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.False(t, called)
	require.False(t, mini.Exists(SanitizeRedisKey(HashTenant(tenant), session)))
	require.NoError(t, rdb.Del(context.Background(), offsetKey).Err())
	status, forwarded, called := callOffsetTestMiddleware(mw, tenant, session)
	require.Equal(t, http.StatusOK, status)
	require.True(t, called)
	require.NotContains(t, forwarded, "13800138000")
}

type expireOffsetLeaseDetector struct {
	mini    *miniredis.Miniredis
	entered chan<- struct{}
	resume  <-chan struct{}
}

func (d expireOffsetLeaseDetector) Name() string { return "expire-offset-lease" }
func (d expireOffsetLeaseDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	d.mini.FastForward(20 * time.Second)
	if d.entered != nil {
		close(d.entered)
		<-d.resume
	}
	return NewPatternDetector().Detect(ctx, text)
}

type slowOffsetDetector struct {
	entered chan<- struct{}
	resume  <-chan struct{}
}

func (d slowOffsetDetector) Name() string { return "slow-offset" }
func (d slowOffsetDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	close(d.entered)
	select {
	case <-d.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return NewPatternDetector().Detect(ctx, text)
}

func TestSanitizeOffsetsSlowDetectorRenewsLease(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	entered := make(chan struct{})
	resume := make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	mw := newOffsetTestMiddleware(t, rdb, slowOffsetDetector{entered: entered, resume: resume})
	mw.offsetLeaseTTL = 120 * time.Millisecond
	result := make(chan int, 1)
	go func() {
		status, _, called := callOffsetTestMiddleware(mw, "tenant-slow", "session-slow")
		if !called {
			result <- -status
			return
		}
		result <- status
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow detector did not start")
	}
	// Advance Redis's clock beyond the original lease while the detector
	// remains active. Each periodic owner-checked renewal must keep it alive.
	lockKey := SanitizeOffsetRedisKey(HashTenant("tenant-slow"), "session-slow") + ":lock"
	for i := 0; i < 4; i++ {
		mini.FastForward(80 * time.Millisecond)
		deadline := time.Now().Add(time.Second)
		for mini.TTL(lockKey) < 80*time.Millisecond && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		require.GreaterOrEqual(t, mini.TTL(lockKey), 80*time.Millisecond, "lease was not renewed after Redis clock advance %d", i)
	}
	close(resume)
	select {
	case status := <-result:
		require.Equal(t, http.StatusOK, status)
	case <-time.After(2 * time.Second):
		t.Fatal("slow sanitizer did not finish")
	}
	values, err := rdb.HGetAll(context.Background(), SanitizeRedisKey(HashTenant("tenant-slow"), "session-slow")).Result()
	require.NoError(t, err)
	require.Equal(t, "13800138000", values["{SENSITIVE:phone:1}"])
}

func TestSanitizeOffsetsExpiredLeaseCannotCommit(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	mw := newOffsetTestMiddleware(t, rdb, expireOffsetLeaseDetector{mini: mini})
	const tenant, session = "tenant-expired", "session-expired"
	status, _, called := callOffsetTestMiddleware(mw, tenant, session)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.False(t, called)
	require.False(t, mini.Exists(SanitizeRedisKey(HashTenant(tenant), session)))
	require.False(t, mini.Exists(SanitizeOffsetRedisKey(HashTenant(tenant), session)))
}

func TestSanitizeOffsetsExpiredWorkerCannotOverwriteNewOwner(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()
	entered := make(chan struct{})
	resume := make(chan struct{})
	stale := newOffsetTestMiddleware(t, rdb, expireOffsetLeaseDetector{mini: mini, entered: entered, resume: resume})
	fresh := newOffsetTestMiddleware(t, rdb, NewPatternDetector())
	const tenant, session = "tenant-takeover", "session-takeover"
	type result struct {
		status int
		called bool
	}
	staleResult := make(chan result, 1)
	go func() {
		status, _, called := callOffsetTestMiddleware(stale, tenant, session, "13800138000")
		staleResult <- result{status: status, called: called}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stale worker did not enter detector")
	}
	status, forwarded, called := callOffsetTestMiddleware(fresh, tenant, session, "13912345678")
	require.Equal(t, http.StatusOK, status)
	require.True(t, called)
	require.NotContains(t, forwarded, "13912345678")
	close(resume)
	old := <-staleResult
	require.Equal(t, http.StatusServiceUnavailable, old.status)
	require.False(t, old.called)
	mapKey := SanitizeRedisKey(HashTenant(tenant), session)
	values, err := rdb.HGetAll(context.Background(), mapKey).Result()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"{SENSITIVE:phone:1}": "13912345678"}, values)
	key := SanitizeOffsetRedisKey(HashTenant(tenant), session)
	require.Equal(t, "1", rdb.HGet(context.Background(), key, "phone").Val())
}

func TestSanitizeOffsetsNoRedisKeepsRequestLocalMapping(t *testing.T) {
	mw := newOffsetTestMiddleware(t, nil, NewPatternDetector())
	status, forwarded, called := callOffsetTestMiddleware(mw, "tenant-local", "session-local")
	require.Equal(t, http.StatusOK, status)
	require.True(t, called)
	require.Contains(t, forwarded, "{SENSITIVE:phone:1}")
	require.NotContains(t, forwarded, "13800138000")
}

// blockingHolderDetector blocks inside Detect until it is released, so the
// caller owns the per-session offset lock for as long as the test wants.
// The once guard keeps it safe if Detect is ever called more than once.
type blockingHolderDetector struct {
	entered chan<- struct{}
	resume  <-chan struct{}
	once    sync.Once
}

func (d *blockingHolderDetector) Name() string { return "blocking-holder" }

func (d *blockingHolderDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	d.once.Do(func() {
		if d.entered != nil {
			close(d.entered)
		}
	})
	select {
	case <-d.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return NewPatternDetector().Detect(ctx, text)
}

// TestSanitizeOffsetsSlowHolderDoesNotFailWaiter pins the contract that a
// holder which is merely slow must not be mistaken for a stuck lock.
//
// Why this gate exists (2026-10-20, audit): the waiter used to give up after
// 3 attempts × 50 ms ≈ 100 ms while the lease was 15 s. The holder's critical
// section (load offsets → detect → allocate → commit) routinely exceeds
// 100 ms on a loaded host, so the waiter failed closed with 503 on a healthy
// system. That is the flake behind TestSanitizeOffsetsAcrossMiddlewareInstances.
//
// The gate is deliberately **deterministic**: it makes the holder slow *by
// construction* (300 ms, three times the old wait window) instead of relying
// on a load-dependent flake that reproduces roughly once every 12
// whole-package runs. A gate that only reds under load cannot be used to
// validate a fix.
//
// The complementary contract — a lock that is genuinely held by someone else
// still fails closed — stays pinned by
// TestSanitizeOffsetsBusyOrRedisFaultStopsDispatch.
func TestSanitizeOffsetsSlowHolderDoesNotFailWaiter(t *testing.T) {
	mini, err := miniredis.Run()
	require.NoError(t, err)
	defer mini.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rdb.Close()

	entered := make(chan struct{})
	resume := make(chan struct{})
	holder := newOffsetTestMiddleware(t, rdb, &blockingHolderDetector{entered: entered, resume: resume})
	waiter := newOffsetTestMiddleware(t, rdb, NewPatternDetector())

	const tenant, session = "tenant-slow-holder", "session-slow-holder"
	holderDone := make(chan int, 1)
	go func() {
		status, _, called := callOffsetTestMiddleware(holder, tenant, session, "13800138000")
		if !called {
			holderDone <- -status
			return
		}
		holderDone <- status
	}()

	// Wait until the holder is inside Detect: at that point it owns the
	// per-session offset lock, so the waiter below really contends.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("holder did not enter the detector")
	}

	waiterDone := make(chan int, 1)
	go func() {
		status, _, called := callOffsetTestMiddleware(waiter, tenant, session, "13912345678")
		if !called {
			waiterDone <- -status
			return
		}
		waiterDone <- status
	}()

	// Hold the lock for three times the old wait window.
	time.Sleep(300 * time.Millisecond)
	close(resume)

	require.Equal(t, http.StatusOK, <-holderDone)
	select {
	case status := <-waiterDone:
		require.Equal(t, http.StatusOK, status,
			"a live holder that is merely slow must not make the waiter fail closed")
	case <-time.After(10 * time.Second):
		t.Fatal("waiter did not finish")
	}
}
