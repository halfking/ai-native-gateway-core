package compression

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestReviewInvalidateDoesNotReimportRetiredLegacyState(t *testing.T) {
	for _, withDB := range []bool{false, true} {
		name := "no_db"
		if withDB {
			name = "l3_reload_allowed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r := newSharedTestRedis(t)
			body := []byte(nil)
			var db *rehydrateDB
			if withDB {
				body = []byte(`{"messages":[{"role":"user","content":"same persisted body"}]}`)
				db = &rehydrateDB{row: &LastOutboundRow{OutboundBody: body, OutboundMsgCount: 1}}
			}
			old := sharedState(body, "")
			old.ApprovalStatus = ApprovalStateApproved
			old.AuditScore = 1
			if !withDB {
				old.LastOutboundHash = ""
			}
			require.NoError(t, r.HSet(ctx, legacyRedisKey("shared-tenant", "gw_shared001"), encodeSessionStateFields(old)...))
			cache := NewSessionCache(r, nil)
			if withDB {
				cache = NewSessionCache(r, db)
			}
			fresh := sharedState(body, "")
			fresh.ApprovalStatus = ApprovalStateRejected
			fresh.AuditScore = 9
			require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", fresh, body))
			cache.Invalidate(ctx, "shared-tenant", "gw_shared001")
			state, gotBody, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
			require.NoError(t, err)
			if withDB {
				require.NotNil(t, state)
				require.Equal(t, string(body), string(gotBody))
				require.Empty(t, state.ApprovalStatus, "L3 body reload is allowed; retired v1 approval metadata must not be resurrected")
			} else {
				require.Nil(t, state, "without L3, invalidation must remain a cache miss")
			}
		})
	}
}
func TestReviewInvalidationL3ControlWithoutLegacyMetadata(t *testing.T) {
	ctx := context.Background()
	r := newSharedTestRedis(t)
	body := []byte(`{"messages":[{"role":"user","content":"same persisted body"}]}`)
	db := &rehydrateDB{row: &LastOutboundRow{OutboundBody: body, OutboundMsgCount: 1}}
	cache := NewSessionCache(r, db)
	st := sharedState(body, "")
	st.ApprovalStatus = ApprovalStateRejected
	require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", st, body))
	cache.Invalidate(ctx, "shared-tenant", "gw_shared001")
	got, out, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, string(body), string(out))
	require.Empty(t, got.ApprovalStatus)
	require.Equal(t, 1, db.calls)
}

type invalidateFailureBackend struct {
	*sharedTestRedis
	phase    string
	failure  error
	failures atomic.Int32
}

func (r *invalidateFailureBackend) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if (r.phase == "acquire" && strings.Contains(script, "session-cache-acquire")) || (r.phase != "acquire" && strings.Contains(script, "session-cache-invalidate")) {
		r.failures.Add(1)
		return nil, r.failure
	}
	return r.sharedTestRedis.Eval(ctx, script, keys, args...)
}
func (r *invalidateFailureBackend) Del(ctx context.Context, key string) error {
	if r.phase == "delete" && key == redisKey("shared-tenant", "gw_shared001") {
		r.failures.Add(1)
		return r.failure
	}
	return r.sharedTestRedis.Del(ctx, key)
}
func TestInvalidateFailureKeepsConfirmedPairAndL1(t *testing.T) {
	for _, phase := range []string{"acquire", "delete"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			base := newSharedTestRedis(t)
			old := &SessionState{SchemaVersion: 1, ApprovalStatus: ApprovalStateApproved}
			require.NoError(t, base.HSet(ctx, legacyRedisKey("shared-tenant", "gw_shared001"), encodeSessionStateFields(old)...))
			cache := NewSessionCache(base, nil)
			body := []byte("confirmed body")
			latest := sharedState(body, "")
			latest.ApprovalStatus = ApprovalStateRejected
			require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", latest, body))
			before, err := base.HGetAll(ctx, redisKey("shared-tenant", "gw_shared001"))
			require.NoError(t, err)
			fault := &invalidateFailureBackend{sharedTestRedis: base, phase: phase, failure: errors.New("injected invalidate " + phase + " failure")}
			cache.redis = fault
			cache.Invalidate(ctx, "shared-tenant", "gw_shared001")
			after, err := base.HGetAll(ctx, redisKey("shared-tenant", "gw_shared001"))
			require.NoError(t, err)
			require.Equal(t, before, after, "unconfirmed invalidation must preserve latest shared pair")
			cache.mu.Lock()
			entry := cache.l1[l1Key("shared-tenant", "gw_shared001")]
			cache.mu.Unlock()
			require.NotNil(t, entry, "failed shared removal must not claim local invalidation succeeded")
			require.Positive(t, fault.failures.Load(), "fault must actually be exercised")
		})
	}
}

type pausedInvalidateBackend struct {
	*sharedTestRedis
	marker      string
	entered     chan struct{}
	resume      chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (r *pausedInvalidateBackend) pause(ctx context.Context) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *pausedInvalidateBackend) release() { r.releaseOnce.Do(func() { close(r.resume) }) }
func (r *pausedInvalidateBackend) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if strings.Contains(script, r.marker) {
		if err := r.pause(ctx); err != nil {
			return nil, err
		}
	}
	return r.sharedTestRedis.Eval(ctx, script, keys, args...)
}
func (r *pausedInvalidateBackend) Del(ctx context.Context, key string) error {
	// Pause the old implementation at its unguarded v2 deletion too, so the
	// same lease-expiry regression proves the difference before the fix.
	if r.marker == "session-cache-invalidate" && key == redisKey("shared-tenant", "gw_shared001") {
		if err := r.pause(ctx); err != nil {
			return err
		}
	}
	return r.sharedTestRedis.Del(ctx, key)
}
