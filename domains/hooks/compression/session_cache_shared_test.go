package compression

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
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
