package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F04 (V3 持久化协议能力, 2026-09-30) — durable protocol-capability layer on
// top of the (credential, model) node state.
//
// The three-way contract these tests pin:
//   - an explicit negative verdict survives and is readable (short-circuit);
//   - an explicit positive verdict is readable and does NOT short-circuit;
//   - ABSENCE is neither, and absence is what an unprobed node looks like —
//     conflating it with "unsupported" would break every unprobed credential.

func newCapabilityTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	return New(Config{Enabled: true, DefaultLimit: 5}, client), mr
}

// TestF04_DurableCapabilityBlocksRepeatedNativeAttempt is the core F04 test:
// after the probe writes SupportsResponses=false, every later read must see
// the durable verdict — no upstream call, no redis re-detection. Then TTL
// expiry must return the node to "unknown" (re-probe), NOT to "supported" and
// NOT to a permanent block.
func TestF04_DurableCapabilityBlocksRepeatedNativeAttempt(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()

	// 100 sequential reads must all report the durable negative verdict.
	require.NoError(t, mgr.SetSupportsResponses(ctx, 42, "gpt-5.6-terra", false))
	for i := 0; i < 100; i++ {
		supported, known, err := mgr.GetSupportsResponses(ctx, 42, "gpt-5.6-terra", nil)
		require.NoError(t, err, "read %d", i)
		require.True(t, known, "read %d: verdict must be known", i)
		require.False(t, supported, "read %d: verdict must be unsupported", i)
	}

	// TTL expiry (F04 §3.3: expired NodeState == "unknown capability", which
	// must fall back to live detection rather than keep short-circuiting).
	mr.FastForward(nodeStateTTLSec * 2 * time.Second)

	supported, known, err := mgr.GetSupportsResponses(ctx, 42, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	assert.False(t, known, "expired node state must read as unknown, not unsupported")
	assert.False(t, supported)

	// A successful re-probe flips it back to supported (F04 §3.4).
	require.NoError(t, mgr.SetSupportsResponses(ctx, 42, "gpt-5.6-terra", true))
	supported, known, err = mgr.GetSupportsResponses(ctx, 42, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	require.True(t, known)
	assert.True(t, supported, "successful re-probe must flip the verdict back")
}

// TestF04_CapabilityExpiryIsIndependentFromRefreshingNodeHealth ensures a
// steady stream of healthy requests cannot keep an old protocol verdict alive
// by extending the shared NodeState key TTL.
func TestF04_CapabilityExpiryIsIndependentFromRefreshingNodeHealth(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()

	require.NoError(t, mgr.SetSupportsResponses(ctx, 43, "gpt-5.6-terra", false))
	initialState, err := mgr.GetNodeState(ctx, 43, "gpt-5.6-terra")
	require.NoError(t, err)
	require.NotNil(t, initialState)
	initialExpiry := initialState.CapabilityExpiresAt
	require.Positive(t, initialExpiry)
	mr.FastForward(30 * time.Minute)
	require.NoError(t, mgr.RecordNodeSuccess(ctx, 43, "gpt-5.6-terra", "healthy-request"))

	state, err := mgr.GetNodeState(ctx, 43, "gpt-5.6-terra")
	require.NoError(t, err)
	require.NotNil(t, state, "the health write should have refreshed and preserved the shared node-state key")
	require.Equal(t, initialExpiry, state.CapabilityExpiresAt, "health writes must not refresh capability expiry")
	now, err := mgr.redisNow(ctx)
	require.NoError(t, err)
	state.CapabilityExpiresAt = now - 1 // simulate elapsed capability TTL while health state remains live
	require.NoError(t, mgr.SetNodeState(ctx, state))
	require.True(t, mr.Exists(nodeKey(43, "gpt-5.6-terra")), "node health key must remain live after the capability expiry")

	supported, known, err := mgr.GetSupportsResponses(ctx, 43, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	assert.False(t, known, "capability verdict must expire on its own TTL despite a still-live node health key")
	assert.False(t, supported)
}

func TestF04_ResetNodeHealthPreservesCapabilityVerdict(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()
	const credentialID = 45
	const model = "gpt-5.6-terra"

	require.NoError(t, mgr.SetSupportsResponses(ctx, credentialID, model, false))
	state, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, state)
	expiry := state.CapabilityExpiresAt
	state.Disabled = true
	state.DisabledUntil = time.Now().Add(time.Minute).Unix()
	state.DisabledReason = "rate_limit"
	state.FailureCount = 3
	state.SuccessCount = 2
	state.DisableCount = 1
	state.SlideWindow = []NodeRecord{{Timestamp: time.Now().Unix(), ErrorKind: "rate_limit"}}
	require.NoError(t, mgr.SetNodeState(ctx, state))

	require.NoError(t, mgr.ResetNodeHealthState(ctx, credentialID, model))
	reset, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, reset)
	assert.False(t, reset.Disabled)
	assert.Zero(t, reset.DisabledUntil)
	assert.Empty(t, reset.DisabledReason)
	assert.Zero(t, reset.FailureCount)
	assert.Zero(t, reset.SuccessCount)
	assert.Zero(t, reset.DisableCount)
	assert.Empty(t, reset.SlideWindow)
	assert.Equal(t, expiry, reset.CapabilityExpiresAt)

	supported, known, err := mgr.GetSupportsResponses(ctx, credentialID, model, nil)
	require.NoError(t, err)
	assert.True(t, known, "health reset must preserve independent protocol capability evidence")
	assert.False(t, supported, "the known unsupported verdict must remain unsupported")
}

func TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()
	const credentialID = 46
	const model = "gpt-5.6-terra"
	baseTime := time.Unix(1_800_000_000, 0)
	mr.SetTime(baseTime)

	require.NoError(t, mgr.SetSupportsResponses(ctx, credentialID, model, false))
	state, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, state)
	state.CapabilityExpiresAt = baseTime.Add(time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	hook := &advanceTimeAfterGetHook{server: mr, at: baseTime.Add(2 * time.Second)}
	mgr.client.AddHook(hook)
	supported, known, err := mgr.GetSupportsResponses(ctx, credentialID, model, nil)
	require.NoError(t, err)
	assert.True(t, hook.advanced, "test must advance Redis time after the NodeState GET")
	assert.False(t, known, "Redis TIME sampled after the GET must detect expiry crossed between commands")
	assert.False(t, supported)
}

type advanceTimeAfterGetHook struct {
	server   *miniredis.Miniredis
	at       time.Time
	advanced bool
}

func (h *advanceTimeAfterGetHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *advanceTimeAfterGetHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && cmd.Name() == "get" && !h.advanced {
			h.server.SetTime(h.at)
			h.advanced = true
		}
		return err
	}
}

func (h *advanceTimeAfterGetHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestF04_LegacyCapabilityWithoutExpiryReadsAsUnknown(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()
	require.NoError(t, mgr.SetSupportsResponses(ctx, 44, "gpt-5.6-terra", false))

	state, err := mgr.GetNodeState(ctx, 44, "gpt-5.6-terra")
	require.NoError(t, err)
	require.NotNil(t, state)
	state.CapabilityExpiresAt = 0 // matches pre-migration Redis payload shape
	require.NoError(t, mgr.SetNodeState(ctx, state))

	supported, known, err := mgr.GetSupportsResponses(ctx, 44, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	assert.False(t, known, "legacy capability without expiry must re-enter live detection")
	assert.False(t, supported)
}

// TestF04_CapabilityRecoveryFlipsBackToSupported covers the §3.4 reverse
// recovery in isolation: a mislabel must not block native Responses for a
// full TTL.
func TestF04_CapabilityRecoveryFlipsBackToSupported(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()

	require.NoError(t, mgr.SetSupportsResponses(ctx, 7, "gpt-5.6-terra", false))
	_, known, err := mgr.GetSupportsResponses(ctx, 7, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	require.True(t, known)

	require.NoError(t, mgr.SetSupportsResponses(ctx, 7, "gpt-5.6-terra", true))
	supported, known, err := mgr.GetSupportsResponses(ctx, 7, "gpt-5.6-terra", nil)
	require.NoError(t, err)
	require.True(t, known)
	assert.True(t, supported, "capability recovery must flip the verdict back to supported")
}

// TestF04_DefaultNilCapabilityIsNotShortCircuit is the negative test that
// matters most: a node state that exists (has health data) but carries NO
// capability verdict must read as unknown. If nil were treated as false, every
// freshly-created node would be permanently short-circuited to Chat.
func TestF04_DefaultNilCapabilityIsNotShortCircuit(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()

	// A node with real health state but no capability verdict.
	require.NoError(t, mgr.RecordNodeFailure(ctx, 9, "gpt-4", "req-1", "rate_limit"))

	state, err := mgr.GetNodeState(ctx, 9, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, state.Capabilities.SupportsResponsesKnown(),
		"a node without a verdict must report unknown")
	assert.False(t, state.Capabilities.ResponsesUnsupported())

	supported, known, err := mgr.GetSupportsResponses(ctx, 9, "gpt-4", nil)
	require.NoError(t, err)
	assert.False(t, known, "no verdict on record must be unknown, not unsupported")
	assert.False(t, supported)
}

// TestF04_CapabilityWritePreservesHealthState guards the concurrency
// requirement: the capability writer runs its own Lua script, so it must NOT
// clobber the health fields the outcome script owns (and vice versa). A Go
// read-modify-write would lose one of the two.
func TestF04_CapabilityWritePreservesHealthState(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()

	// Health writes first, then the capability verdict.
	require.NoError(t, mgr.RecordNodeFailure(ctx, 11, "gpt-4", "req-1", "rate_limit"))
	require.NoError(t, mgr.RecordNodeFailure(ctx, 11, "gpt-4", "req-2", "rate_limit"))
	require.NoError(t, mgr.SetSupportsResponses(ctx, 11, "gpt-4", false))

	state, err := mgr.GetNodeState(ctx, 11, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, int64(2), state.FailureCount, "capability write must not reset failure_count")
	assert.Len(t, state.SlideWindow, 2, "capability write must not clear the sliding window")
	assert.True(t, state.Capabilities.ResponsesUnsupported(), "verdict must be readable")

	// Now the outcome writer must not drop the capability.
	require.NoError(t, mgr.RecordNodeSuccess(ctx, 11, "gpt-4", "req-3"))
	supported, known, err := mgr.GetSupportsResponses(ctx, 11, "gpt-4", nil)
	require.NoError(t, err)
	require.True(t, known, "an outcome write must not erase the capability verdict")
	assert.False(t, supported)

	state, err = mgr.GetNodeState(ctx, 11, "gpt-4")
	require.NoError(t, err)
	assert.NotZero(t, state.SuccessCount, "outcome write must still record its own field")
}

// TestF04_CapabilityIsPerCredentialAndModel pins the blast radius: a verdict
// written for one model must not short-circuit a sibling model on the same
// credential, nor the same model on another credential.
func TestF04_CapabilityIsPerCredentialAndModel(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()

	require.NoError(t, mgr.SetSupportsResponses(ctx, 1, "model-a", false))

	// Sibling model, same credential: untouched.
	_, known, err := mgr.GetSupportsResponses(ctx, 1, "model-b", nil)
	require.NoError(t, err)
	assert.False(t, known, "sibling model must remain unknown")

	// Same model, different credential: untouched.
	_, known, err = mgr.GetSupportsResponses(ctx, 2, "model-a", nil)
	require.NoError(t, err)
	assert.False(t, known, "other credential must remain unknown")
}

// TestF04_DisabledRedisIsNoOp pins the lite-mode contract: with Redis off, the
// capability write is a no-op and reads report unknown, so the request path
// keeps using live detection instead of a fabricated verdict.
func TestF04_DisabledRedisIsNoOp(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	mgr := New(Config{Enabled: false, DefaultLimit: 5}, client)
	ctx := context.Background()

	require.NoError(t, mgr.SetSupportsResponses(ctx, 5, "gpt-4", false))

	_, known, err := mgr.GetSupportsResponses(ctx, 5, "gpt-4", nil)
	require.NoError(t, err)
	assert.False(t, known, "lite mode must never fabricate a capability verdict")
}

// TestF04_EmptyCapabilitiesNeverCorruptNodeState is a regression guard for a
// trap that only shows up across the Go↔Lua boundary.
//
// A value-typed `Capabilities NodeCapabilities` with `json:",omitempty"` is
// NEVER omitted (omitempty does not apply to structs), so every Go-written
// node state carried `"capabilities":{}`. Redis Lua's cjson cannot tell an
// empty object from an empty array and re-encoded it as `[]`, which then fails
// to unmarshal into a Go struct — every subsequent node-state read for that
// key errored out. Keeping Capabilities a *pointer makes the zero value
// disappear from the payload, so cjson never has to guess.
func TestF04_EmptyCapabilitiesNeverCorruptNodeState(t *testing.T) {
	mgr, _ := newCapabilityTestManager(t)
	ctx := context.Background()

	// A node state written from Go with no capability verdict at all.
	require.NoError(t, mgr.SetNodeState(ctx, &NodeState{
		CredentialID: 77,
		Model:        "gpt-4",
		SlideWindow:  []NodeRecord{},
	}))

	// An outcome write round-trips the same key through cjson.
	require.NoError(t, mgr.RecordNodeFailure(ctx, 77, "gpt-4", "req-1", "rate_limit"))

	// Before the pointer fix this failed with
	// "cannot unmarshal array into Go struct field NodeState.capabilities".
	state, err := mgr.GetNodeState(ctx, 77, "gpt-4")
	require.NoError(t, err, "node state must survive a Lua round-trip without capability data")
	require.NotNil(t, state)
	assert.Equal(t, 77, state.CredentialID)
	assert.Equal(t, int64(1), state.FailureCount, "outcome write must still apply")
	assert.False(t, state.Capabilities.SupportsResponsesKnown())
}
