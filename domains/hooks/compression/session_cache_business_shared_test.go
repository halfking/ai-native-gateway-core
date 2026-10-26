package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
)

type pausedSharedRead struct {
	*sharedTestRedis
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (r *pausedSharedRead) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	fields, err := r.sharedTestRedis.HGetAll(ctx, key)
	if err == nil && len(fields) > 0 {
		r.once.Do(func() {
			close(r.entered)
			select {
			case <-r.resume:
			case <-ctx.Done():
			}
		})
	}
	return fields, err
}

// Pause the business operation after it has read old state. A competing audit
// must either wait for its lease, or finish first and be retained by a reload.
// Reading old state and later replacing it with Set loses the audit score.
func verifySharedBusinessRMW(t *testing.T, action func(*SessionCache) ([]byte, error)) {
	t.Helper()
	r := newSharedTestRedis(t)
	initial := []byte(`{"messages":[{"role":"user","content":"initial"}]}`)
	state := sharedState(initial, "")
	state.AuditScore = 1
	require.NoError(t, NewSessionCache(r, nil).Set(context.Background(), "shared-tenant", "gw_shared001", state, initial))
	paused := &pausedSharedRead{sharedTestRedis: r, entered: make(chan struct{}), resume: make(chan struct{})}
	type result struct {
		body []byte
		err  error
	}
	actionDone := make(chan result, 1)
	go func() { body, err := action(NewSessionCache(paused, nil)); actionDone <- result{body, err} }()
	select {
	case <-paused.entered:
	case early := <-actionDone:
		t.Fatalf("business operation returned before read: %v", early.err)
	case <-time.After(time.Second):
		t.Fatal("business operation did not read state")
	}
	contended := r.observeContention()
	auditDone := make(chan error, 1)
	go func() {
		auditDone <- NewSessionCache(r, nil).Update(context.Background(), "shared-tenant", "gw_shared001", func(s *SessionState, b []byte) (*SessionState, []byte, error) { s.AuditScore = 9; return s, b, nil })
	}()
	auditFinished := false
	select {
	case err := <-auditDone:
		require.NoError(t, err)
		auditFinished = true
	case <-contended:
	case <-time.After(time.Second):
		close(paused.resume)
		t.Fatal("audit neither completed nor reached lease contention")
	}
	close(paused.resume)
	var committed result
	select {
	case committed = <-actionDone:
		require.NoError(t, committed.err)
	case <-time.After(2 * time.Second):
		t.Fatal("business operation did not finish")
	}
	if !auditFinished {
		select {
		case err := <-auditDone:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("competing audit did not finish")
		}
	}
	got, body, err := NewSessionCache(r, nil).GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 9, got.AuditScore, "business Get+Set overwrote a concurrent audit")
	require.Equal(t, string(committed.body), string(body))
	require.Equal(t, sha256Hex(body), got.LastOutboundHash)
}

func TestSessionCacheSharedCommitFinalPreservesConcurrentAudit(t *testing.T) {
	verifySharedBusinessRMW(t, func(cache *SessionCache) ([]byte, error) {
		final := []byte(`{"messages":[{"role":"user","content":"final"}],"exact":9007199254740993}`)
		sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
		return final, sc.CommitFinal(context.Background(), "shared-tenant", "gw_shared001", final, &PrepareResult{})
	})
}
func TestSessionCacheSharedRecoveryPreservesConcurrentAudit(t *testing.T) {
	verifySharedBusinessRMW(t, func(cache *SessionCache) ([]byte, error) {
		rc := NewRecoveryCoordinator(RecoveryDeps{Cache: cache, MaxRetries: 2})
		// attempt=1 bypasses the incremental lookup, pausing only the final RMW.
		res := rc.Recover(context.Background(), makeRecoveryTestBody(), "openai", 5000, "shared-tenant", "gw_shared001", 1)
		if !res.ShouldRetry {
			return nil, fmt.Errorf("recovery did not produce a retry: %s", res.Reason)
		}
		return res.NewBody, nil
	})
}

func TestSessionCacheSharedPrepareDoesNotRestoreDiscardedSanitizeLineage(t *testing.T) {
	r := newSharedTestRedis(t)
	cache := NewSessionCache(r, nil)
	body := []byte(`{"messages":[{"role":"user","content":"body"}]}`)
	stale := sharedState(body, "old")
	stale.HasCutMarker = true
	stale.CutIndex = 7
	stale.SanitizeMapRef = "old-map"
	require.NoError(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", stale, body))
	ctx := WithSanitizeInfo(context.Background(), SanitizeInfo{MapGeneration: "new"})
	NewSessionCompressor(SessionCompressorDeps{Cache: cache}).updateCache(ctx, "shared-tenant", "gw_shared001", nil, body, &PrepareResult{MsgCount: 1}, false, false)
	got, _, err := cache.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, "new", got.SanitizeMapGeneration)
	require.False(t, got.HasCutMarker)
	require.Zero(t, got.CutIndex)
	require.Empty(t, got.SanitizeMapRef)
}

func TestSessionCacheSharedPrepareStripCounterCommitsOnce(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "4")
	r := newSharedTestRedis(t)
	cache := NewSessionCache(r, nil)
	ctx := context.Background()
	seed := []byte(`{"messages":[{"role":"user","content":"seed"}]}`)
	initial := sharedState(seed, "")
	initial.StripsApplied = 3
	require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", initial, seed))
	messages := []map[string]any{{"role": "user", "content": "seed"}}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("call_%d", i)
		messages = append(messages, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": id, "content": strings.Repeat("large result ", 1000)})
	}
	messages = append(messages, map[string]any{"role": "user", "content": strings.Repeat("latest context ", 3000)})
	body, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	_, strip := StripToolInfo(body, "openai")
	require.True(t, strip.DidStrip, "tool strip premise must be present")
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	res := sc.Prepare(ctx, body, "shared-tenant", "gw_shared001", "openai", 5000, false)
	require.NotEmpty(t, res.WindowTriggered, "compression window must fire")
	got, final, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, 4, got.StripsApplied)
	require.NoError(t, sc.CommitFinal(ctx, "shared-tenant", "gw_shared001", final, res))
	got, _, err = cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, 4, got.StripsApplied, "final commit must not count Prepare's strip twice")
}
