package compression

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// sharedTestRedis runs the actual Lua against miniredis. Its fault controls
// are synchronized, and HGetAll owns the returned map through go-redis.
// FastForward advances Redis' lease clock without sleeping for the lease TTL.
type sharedTestRedis struct {
	server                    *miniredis.Miniredis
	client                    *redis.Client
	mu                        sync.Mutex
	readErr, writeErr, ttlErr error
	beforeEval                func(context.Context, string, []string)
	afterEval                 func(string, any, error)
	skipACLShim               bool
}

func newSharedTestRedis(t *testing.T) *sharedTestRedis {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return &sharedTestRedis{server: mr, client: client}
}
func (r *sharedTestRedis) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	r.mu.Lock()
	err := r.readErr
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r.client.HGetAll(ctx, key).Result()
}
func (r *sharedTestRedis) HSet(ctx context.Context, key string, args ...any) error {
	r.mu.Lock()
	err := r.writeErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.client.HSet(ctx, key, args...).Err()
}
func (r *sharedTestRedis) Expire(ctx context.Context, key string, ttl time.Duration) error {
	r.mu.Lock()
	err := r.ttlErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.client.Expire(ctx, key, ttl).Err()
}
func (r *sharedTestRedis) Del(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}
func (r *sharedTestRedis) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	r.mu.Lock()
	hook, after, writeErr, ttlErr, skipACL := r.beforeEval, r.afterEval, r.writeErr, r.ttlErr, r.skipACLShim
	r.mu.Unlock()
	if hook != nil {
		hook(ctx, script, keys)
	}
	if strings.Contains(script, "session-cache-commit") {
		if writeErr != nil {
			return nil, writeErr
		}
		if ttlErr != nil {
			return nil, ttlErr
		}
	}
	// miniredis does not implement Redis 7's ACL API. Supply an explicit
	// allow-all test API shim; production uses the unmodified script, and
	// actual denial/rollback guarantees are tested against disposable Redis.
	if !skipACL {
		script = "redis.acl_check_cmd=function(...) return true end\n" + script
	}
	result, err := r.client.Eval(ctx, script, keys, args...).Result()
	if after != nil {
		after(script, result, err)
	}
	return result, err
}
func (r *sharedTestRedis) observeContention() <-chan struct{} {
	contended := make(chan struct{})
	var once sync.Once
	r.mu.Lock()
	r.afterEval = func(script string, result any, err error) {
		if err == nil && strings.Contains(script, "session-cache-acquire") && result == int64(0) {
			once.Do(func() { close(contended) })
		}
	}
	r.mu.Unlock()
	return contended
}

func sharedState(body []byte, generation string) *SessionState {
	return &SessionState{SchemaVersion: schemaVersion, LastOutboundHash: sha256Hex(body), MsgCount: countMessages(body), SanitizeMapGeneration: generation}
}
func requireSharedPair(t *testing.T, c *SessionCache, wantBody []byte, wantGeneration string) *SessionState {
	t.Helper()
	st, body, err := c.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, wantGeneration, st.SanitizeMapGeneration)
	require.Equal(t, string(wantBody), string(body), "must return exact committed body, including provider envelope")
	if len(wantBody) > 0 {
		require.Equal(t, sha256Hex(body), st.LastOutboundHash, "state and body must describe the same commit")
	}
	return st
}

func TestSessionCacheSharedWarmAndColdReadExactPair(t *testing.T) {
	r := newSharedTestRedis(t)
	a, b := NewSessionCache(r, nil), NewSessionCache(r, nil)
	oldBody := []byte(`{"model":"m","messages":[{"role":"user","content":"old"}],"precision":9007199254740993}`)
	newBody := []byte(`{"model":"m","messages":[{"role":"user","content":"new"}],"precision":9007199254740993}`)
	require.NoError(t, a.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(oldBody, "old"), oldBody))
	requireSharedPair(t, b, oldBody, "old")
	require.NoError(t, a.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(newBody, "new"), newBody))
	requireSharedPair(t, b, newBody, "new")
	requireSharedPair(t, NewSessionCache(r, nil), newBody, "new")
}

func TestSessionCacheSharedDoesNotLaunderSameCountOldDBBody(t *testing.T) {
	r := newSharedTestRedis(t)
	oldBody := []byte(`{"messages":[{"role":"user","content":"old database turn"}]}`)
	finalBody := []byte(`{"model":"m","messages":[{"role":"user","content":"new dispatched turn"}],"unknown":{"n":9007199254740993}}`)
	db := &rehydrateDB{row: &LastOutboundRow{OutboundBody: oldBody, OutboundMsgCount: 1}}
	writer := NewSessionCache(r, nil)
	require.NoError(t, writer.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(finalBody, "final"), finalBody))
	reader := NewSessionCache(r, db)
	reader.SetTurnReader(&fakeTurnReader{latest: nil})
	requireSharedPair(t, reader, finalBody, "final")
	require.Zero(t, db.calls, "acknowledged shared pair must not consult lagging L3")
}

func TestSessionCacheSharedUpdateSerializesCallbacksOnce(t *testing.T) {
	r := newSharedTestRedis(t)
	a, b := NewSessionCache(r, nil), NewSessionCache(r, nil)
	body := []byte(`{"messages":[{"role":"user","content":"body"}]}`)
	require.NoError(t, a.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	enteredA, releaseA, enteredB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	results := make(chan error, 2)
	var callsA, callsB atomic.Int32
	go func() {
		results <- a.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
			callsA.Add(1)
			close(enteredA)
			<-releaseA
			st.StripsApplied++
			return st, b, nil
		})
	}()
	select {
	case <-enteredA:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}
	contended := r.observeContention()
	go func() {
		results <- b.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
			callsB.Add(1)
			close(enteredB)
			st.StripsApplied++
			return st, b, nil
		})
	}()
	select {
	case <-contended:
	case <-time.After(time.Second):
		close(releaseA)
		t.Fatal("B never reached shared lease contention")
	}
	early := false
	select {
	case <-enteredB:
		early = true
	case <-time.After(40 * time.Millisecond):
	}
	close(releaseA)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("serialized updates did not finish")
		}
	}
	require.False(t, early, "second callback must wait until first commit")
	require.EqualValues(t, 1, callsA.Load())
	require.EqualValues(t, 1, callsB.Load())
	st := requireSharedPair(t, NewSessionCache(r, nil), body, "base")
	require.Equal(t, 2, st.StripsApplied)
}

func TestSessionCacheSharedSetReplacesOptionalZeroFields(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	body := []byte(`{"messages":[{"role":"user","content":"body"}]}`)
	st := sharedState(body, "old")
	st.ToolsHash = "tools"
	st.SystemPrompt = "system"
	st.FullSessionHash = "full"
	st.LastStripAt = 1
	st.StripsApplied = 2
	st.CompletedTasks = 3
	st.CompressionMode = "smart"
	st.MessagesAfterStrip = 4
	st.TokensAfterStrip = 5
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", st, body))
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "new"), body))
	got := requireSharedPair(t, NewSessionCache(r, nil), body, "new")
	require.Empty(t, got.ToolsHash)
	require.Empty(t, got.SystemPrompt)
	require.Empty(t, got.FullSessionHash)
	require.Zero(t, got.LastStripAt)
	require.Zero(t, got.StripsApplied)
	require.Zero(t, got.CompletedTasks)
	require.Empty(t, got.CompressionMode)
	require.Zero(t, got.MessagesAfterStrip)
	require.Zero(t, got.TokensAfterStrip)
}

func TestSessionCacheSharedErrorsNeverPublishL1(t *testing.T) {
	for _, op := range []string{"write", "ttl"} {
		t.Run(op, func(t *testing.T) {
			r := newSharedTestRedis(t)
			c := NewSessionCache(r, nil)
			body := []byte(`{"messages":[{"role":"user","content":"old"}]}`)
			require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "old"), body))
			sentinel := errors.New("injected shared " + op + " failure")
			r.mu.Lock()
			if op == "write" {
				r.writeErr = sentinel
			} else {
				r.ttlErr = sentinel
			}
			r.mu.Unlock()
			next := bytes.ReplaceAll(body, []byte("old"), []byte("new"))
			err := c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(next, "new"), next)
			require.ErrorIs(t, err, sentinel, "nil means shared commit was acknowledged")
			r.mu.Lock()
			r.writeErr = nil
			r.ttlErr = nil
			r.mu.Unlock()
			requireSharedPair(t, c, body, "old")
			requireSharedPair(t, NewSessionCache(r, nil), body, "old")
		})
	}
	t.Run("read_does_not_return_old_L1", func(t *testing.T) {
		r := newSharedTestRedis(t)
		c := NewSessionCache(r, nil)
		body := []byte("body")
		require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
		sentinel := errors.New("injected shared read failure")
		r.mu.Lock()
		r.readErr = sentinel
		r.mu.Unlock()
		st, got, err := c.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
		require.ErrorIs(t, err, sentinel)
		require.Nil(t, st)
		require.Nil(t, got)
	})
	t.Run("wrong_type", func(t *testing.T) {
		r := newSharedTestRedis(t)
		r.server.Set(redisKey("shared-tenant", "gw_shared001"), "wrong-type")
		_, _, err := NewSessionCache(r, nil).GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
		require.Error(t, err)
		require.Contains(t, err.Error(), "WRONGTYPE")
	})
}

func TestSessionCacheSharedCallbackErrorAndNilLeavePair(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	sentinel := errors.New("callback rejected")
	err := c.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
		st.SanitizeMapGeneration = "rejected"
		return st, []byte("bad"), sentinel
	})
	require.ErrorIs(t, err, sentinel)
	requireSharedPair(t, NewSessionCache(r, nil), body, "base")
	require.NoError(t, c.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
		st.SanitizeMapGeneration = "nil"
		return nil, []byte("bad"), nil
	}))
	requireSharedPair(t, NewSessionCache(r, nil), body, "base")
}

func TestSessionCacheSharedCancellationBeforeAndAfterCallback(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			r := newSharedTestRedis(t)
			c := NewSessionCache(r, nil)
			body := []byte("body")
			require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if when == "before" {
				cancel()
			}
			var calls int
			err := c.Update(ctx, "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
				calls++
				cancel()
				st.SanitizeMapGeneration = "cancelled"
				return st, b, nil
			})
			require.ErrorIs(t, err, context.Canceled)
			if when == "before" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			requireSharedPair(t, c, body, "base")
			requireSharedPair(t, NewSessionCache(r, nil), body, "base")
		})
	}
}

func TestSessionCacheSharedExpiredOwnerCannotCommitOrReleaseSuccessor(t *testing.T) {
	r := newSharedTestRedis(t)
	a, b, c := NewSessionCache(r, nil), NewSessionCache(r, nil), NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, a.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	enteredA, enteredB, releaseA, releaseB := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	var callsA, callsB atomic.Int32
	go func() {
		doneA <- a.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
			callsA.Add(1)
			close(enteredA)
			<-releaseA
			st.SanitizeMapGeneration = "expired-A"
			return st, b, nil
		})
	}()
	select {
	case <-enteredA:
	case <-time.After(time.Second):
		t.Fatal("A callback did not start")
	}
	r.server.FastForward(time.Minute)
	go func() {
		doneB <- b.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
			callsB.Add(1)
			close(enteredB)
			<-releaseB
			st.SanitizeMapGeneration = "successor-B"
			return st, b, nil
		})
	}()
	select {
	case <-enteredB:
	case <-time.After(time.Second):
		close(releaseA)
		close(releaseB)
		t.Fatal("B did not acquire expired lease")
	}
	close(releaseA)
	var errA error
	select {
	case errA = <-doneA:
	case <-time.After(time.Second):
		close(releaseB)
		t.Fatal("expired owner did not return")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	var callsC int
	errC := c.Update(waitCtx, "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) { callsC++; return st, b, nil })
	cancel()
	close(releaseB)
	var errB error
	select {
	case errB = <-doneB:
	case <-time.After(time.Second):
		t.Fatal("successor did not finish")
	}
	require.Error(t, errA, "lost lease must reject A commit")
	require.ErrorIs(t, errC, context.DeadlineExceeded, "A cleanup must not delete B's lease")
	require.Zero(t, callsC)
	require.NoError(t, errB)
	require.EqualValues(t, 1, callsA.Load())
	require.EqualValues(t, 1, callsB.Load())
	requireSharedPair(t, NewSessionCache(r, nil), body, "successor-B")
}

func TestSessionCacheSharedCancelWhileWaitingDoesNotCallCallback(t *testing.T) {
	r := newSharedTestRedis(t)
	a, b := NewSessionCache(r, nil), NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, a.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.Update(context.Background(), "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) {
			close(entered)
			<-release
			return st, b, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owner did not enter callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	var calls int
	err := b.Update(ctx, "shared-tenant", "gw_shared001", func(st *SessionState, b []byte) (*SessionState, []byte, error) { calls++; return st, b, nil })
	close(release)
	select {
	case ownerErr := <-done:
		require.NoError(t, ownerErr)
	case <-time.After(time.Second):
		t.Fatal("owner did not finish")
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, calls)
}

func TestSessionCacheSharedSetEmptyBodyReplacesHistory(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "old"), body))
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", &SessionState{SchemaVersion: schemaVersion, SanitizeMapGeneration: "empty"}, nil))
	requireSharedPair(t, NewSessionCache(r, nil), nil, "empty")
}

func TestSessionCacheSharedUpdateCacheUsesCurrentAuditState(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	body := []byte(`{"messages":[{"role":"user","content":"body"}]}`)
	stale := sharedState(body, "")
	stale.AuditScore = 1
	current := sharedState(body, "")
	current.AuditScore = 9
	current.ApprovalStatus = ApprovalStatePending
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", current, body))
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: c})
	sc.updateCache(context.Background(), "shared-tenant", "gw_shared001", stale, body, &PrepareResult{MsgCount: 1}, false, false)
	got, _, err := c.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, 9, got.AuditScore, "Prepare's stale state must not overwrite concurrent audit")
	require.Equal(t, ApprovalStatePending, got.ApprovalStatus)
}

func TestSessionCacheSharedBodyAboveStale32MiBLimit(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	// The handler's actual maxBodySize is 128 MiB; its old error text says 32.
	// This highly compressible payload makes the corrected boundary inexpensive.
	body := bytes.Repeat([]byte("x"), 33<<20)
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "large"), body))
	st, got, err := NewSessionCache(r, nil).GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.NotNil(t, st)
	if !bytes.Equal(body, got) {
		t.Fatalf("large shared body mismatch: got %d bytes, want %d", len(got), len(body))
	}
	require.Equal(t, sha256Hex(body), st.LastOutboundHash)
}

type blockingSharedDB struct {
	entered chan struct{}
	resume  chan struct{}
	row     *LastOutboundRow
}

func (d *blockingSharedDB) LastOutboundForSession(ctx context.Context, _, _ string) (*LastOutboundRow, error) {
	close(d.entered)
	select {
	case <-d.resume:
		return d.row, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestSessionCacheSharedBackfillCannotOverwriteConcurrentSet(t *testing.T) {
	r := newSharedTestRedis(t)
	oldBody, newBody := []byte("old-database-body"), []byte("new-committed-body")
	db := &blockingSharedDB{entered: make(chan struct{}), resume: make(chan struct{}), row: &LastOutboundRow{OutboundBody: oldBody}}
	reader, writer := NewSessionCache(r, db), NewSessionCache(r, nil)
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := reader.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
		readDone <- err
	}()
	select {
	case <-db.entered:
	case <-time.After(time.Second):
		t.Fatal("backfill did not load DB")
	}
	contended := r.observeContention()
	go func() {
		writeDone <- writer.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(newBody, "new"), newBody)
	}()
	// A correct writer waits on the DB reader's lease; the old writer commits
	// immediately, and the old backfill then overwrites that acknowledged write.
	completed := false
	select {
	case err := <-writeDone:
		require.NoError(t, err)
		completed = true
	case <-contended:
	case <-time.After(time.Second):
		close(db.resume)
		t.Fatal("writer neither completed nor contended")
	}
	close(db.resume)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("backfill did not finish")
	}
	if !completed {
		select {
		case err := <-writeDone:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("writer did not finish")
		}
	}
	requireSharedPair(t, NewSessionCache(r, nil), newBody, "new")
}

func TestSessionCacheSharedInvalidStateEncodingDoesNotPublish(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	invalid := sharedState(body, "invalid")
	invalid.CompressionQuality.OverallScore = math.NaN()
	require.Error(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", invalid, body))
	requireSharedPair(t, c, body, "base")
}

type legacyOnlySharedBackend struct{ r *sharedTestRedis }

func (b legacyOnlySharedBackend) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return b.r.HGetAll(ctx, key)
}
func (b legacyOnlySharedBackend) HSet(ctx context.Context, key string, args ...any) error {
	return b.r.HSet(ctx, key, args...)
}
func (b legacyOnlySharedBackend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return b.r.Expire(ctx, key, ttl)
}
func (b legacyOnlySharedBackend) Del(ctx context.Context, key string) error { return b.r.Del(ctx, key) }
func TestSessionCacheSharedBackendWithoutAtomicSupportFailsExplicitly(t *testing.T) {
	r := newSharedTestRedis(t)
	c := NewSessionCache(legacyOnlySharedBackend{r}, nil)
	body := []byte("body")
	require.Error(t, c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "bad"), body), "configured Redis cannot silently fall back to local writes")
}

func TestSessionCacheSharedMissingRedisACLCapabilityFailsExplicitly(t *testing.T) {
	r := newSharedTestRedis(t)
	r.skipACLShim = true
	c := NewSessionCache(r, nil)
	err := c.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState([]byte("body"), "base"), []byte("body"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires Redis 7.0")
	require.Empty(t, r.server.Keys(), "unsupported Redis must fail before lease or payload writes")
}
