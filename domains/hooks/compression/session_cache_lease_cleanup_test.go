package compression

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type releaseOnlyFailureBackend struct {
	*sharedTestRedis
	failNextRelease atomic.Bool
	releaseAttempts atomic.Int32
	releaseErr      error
}

func (r *releaseOnlyFailureBackend) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if strings.Contains(script, "session-cache-release") && r.failNextRelease.Swap(false) {
		r.releaseAttempts.Add(1)
		return nil, r.releaseErr
	}
	return r.sharedTestRedis.Eval(ctx, script, keys, args...)
}
func newReleaseOnlyFailureBackend(base *sharedTestRedis) *releaseOnlyFailureBackend {
	backend := &releaseOnlyFailureBackend{sharedTestRedis: base, releaseErr: errors.New("injected lease cleanup failure")}
	backend.failNextRelease.Store(true)
	return backend
}

// A confirmed write consumes request-local deltas even if releasing its lease
// fails. CommitFinal must not retry those already acknowledged increments.
func TestSessionCacheSharedAcknowledgedCommitCleanupFailureDoesNotRepeatDeltas(t *testing.T) {
	ctx := context.Background()
	base := newSharedTestRedis(t)
	body := []byte(`{"messages":[{"role":"user","content":"body"}]}`)
	initial := sharedState(body, "")
	initial.StripsApplied = 3
	initial.CompletedTasks = 5
	require.NoError(t, NewSessionCache(base, nil).Set(ctx, "shared-tenant", "gw_shared001", initial, body))
	fault := newReleaseOnlyFailureBackend(base)
	cache := NewSessionCache(fault, nil)
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	res := &PrepareResult{MsgCount: 1, cacheStripAt: 1, cacheStripMsgs: 1, cacheStripTokens: 2, cacheCompletedTasksDelta: 2}
	sc.updateCache(ctx, "shared-tenant", "gw_shared001", nil, body, res, false, false)
	first, gotBody, err := NewSessionCache(base, nil).GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, string(body), string(gotBody))
	require.Equal(t, 4, first.StripsApplied)
	require.Equal(t, 7, first.CompletedTasks)
	require.EqualValues(t, 1, fault.releaseAttempts.Load(), "cleanup failure premise must be present")
	require.Zero(t, res.cacheStripAt, "acknowledged delta must be consumed despite failed cleanup")
	require.Zero(t, res.cacheCompletedTasksDelta)
	base.server.FastForward(time.Minute) // Failed cleanup leaves its bounded lease until expiry.
	require.NoError(t, sc.CommitFinal(ctx, "shared-tenant", "gw_shared001", body, res))
	final, finalBody, err := NewSessionCache(base, nil).GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, string(body), string(finalBody))
	require.Equal(t, 4, final.StripsApplied)
	require.Equal(t, 7, final.CompletedTasks)
}

func TestSessionCacheSharedCleanupFailurePreservesPrimaryFailure(t *testing.T) {
	for _, failure := range []string{"callback", "commit"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			base := newSharedTestRedis(t)
			body := []byte(`{"messages":[{"role":"user","content":"old"}]}`)
			require.NoError(t, NewSessionCache(base, nil).Set(ctx, "shared-tenant", "gw_shared001", sharedState(body, "old"), body))
			before, err := base.HGetAll(ctx, redisKey("shared-tenant", "gw_shared001"))
			require.NoError(t, err)
			ttlBefore := base.server.TTL(redisKey("shared-tenant", "gw_shared001"))
			primary := errors.New("injected primary " + failure + " failure")
			if failure == "commit" {
				base.mu.Lock()
				base.writeErr = primary
				base.mu.Unlock()
			}
			fault := newReleaseOnlyFailureBackend(base)
			var calls int
			err = NewSessionCache(fault, nil).Update(ctx, "shared-tenant", "gw_shared001", func(st *SessionState, _ []byte) (*SessionState, []byte, error) {
				calls++
				st.SanitizeMapGeneration = "unconfirmed"
				if failure == "callback" {
					return st, []byte("new"), primary
				}
				return st, []byte("new"), nil
			})
			require.ErrorIs(t, err, primary)
			require.NotErrorIs(t, err, fault.releaseErr, "cleanup must not change the primary error")
			if failure == "callback" {
				require.Same(t, primary, err, "callback error must propagate unchanged")
			}
			require.Equal(t, 1, calls)
			require.EqualValues(t, 1, fault.releaseAttempts.Load())
			base.mu.Lock()
			base.writeErr = nil
			base.mu.Unlock()
			after, readErr := base.HGetAll(ctx, redisKey("shared-tenant", "gw_shared001"))
			require.NoError(t, readErr)
			require.Equal(t, before, after)
			require.Equal(t, ttlBefore, base.server.TTL(redisKey("shared-tenant", "gw_shared001")))
			requireSharedPair(t, NewSessionCache(base, nil), body, "old")
		})
	}
}
