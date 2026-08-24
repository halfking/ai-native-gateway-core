package credentialfpslot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeStateCooldownRecoveryOnRead(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})

	mgr := New(Config{Enabled: true, DefaultLimit: 5}, client)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, mgr.RecordNodeFailure(ctx, 100, "gpt-4", "req", "rate_limit"))
	}

	state, err := mgr.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Disabled)

	state.DisabledUntil = time.Now().Add(-1 * time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	raw, err := client.Get(ctx, nodeKey(100, "gpt-4")).Result()
	require.NoError(t, err)
	var stored NodeState
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	assert.True(t, stored.Disabled)
	assert.LessOrEqual(t, stored.DisabledUntil, time.Now().Unix())

	state, err = mgr.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.IsUsable(time.Now()))
	assert.False(t, state.Disabled)
	assert.Equal(t, int64(0), state.FailureCount)
	assert.Len(t, state.SlideWindow, 0)
}

func TestNodeStateCooldownRecoveryPure(t *testing.T) {
	state := &NodeState{
		Disabled:      true,
		DisabledUntil: time.Now().Add(-1 * time.Second).Unix(),
		FailureCount:  3,
		SlideWindow:   []NodeRecord{{Success: false, Timestamp: time.Now().Unix()}},
	}

	assert.True(t, state.IsUsable(time.Now()))
	assert.False(t, state.Disabled)
	assert.Equal(t, int64(0), state.FailureCount)
	assert.Len(t, state.SlideWindow, 0)
}

func newNodeStateTestManager(t *testing.T) (*Manager, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	return New(Config{Enabled: true, DefaultLimit: 5}, client), client
}

func TestGetNodeStateMalformedJSONReturnsError(t *testing.T) {
	mgr, client := newNodeStateTestManager(t)
	ctx := context.Background()

	require.NoError(t, client.Set(ctx, nodeKey(101, "gpt-4"), "{not-json", 0).Err())

	state, err := mgr.GetNodeState(ctx, 101, "gpt-4")
	require.Error(t, err)
	assert.Nil(t, state)
}

func TestGetNodeStateIdentityMismatchReturnsError(t *testing.T) {
	mgr, client := newNodeStateTestManager(t)
	ctx := context.Background()

	// Payload belongs to a different node but sits under (501, gpt-4)'s key.
	misplaced, err := json.Marshal(&NodeState{CredentialID: 999, Model: "elsewhere", SlideWindow: []NodeRecord{}})
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, nodeKey(501, "gpt-4"), misplaced, 0).Err())

	state, err := mgr.GetNodeState(ctx, 501, "gpt-4")
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "identity mismatch")
}

func TestGetNodeStatesBatchFailsOpenOnCorruptAndMismatch(t *testing.T) {
	mgr, client := newNodeStateTestManager(t)
	ctx := context.Background()

	valid, err := json.Marshal(&NodeState{
		CredentialID:  201,
		Model:         "model-a",
		Disabled:      true,
		DisabledUntil: time.Now().Add(time.Hour).Unix(),
		SlideWindow:   []NodeRecord{},
	})
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, nodeKey(201, "model-a"), valid, 0).Err())
	require.NoError(t, client.Set(ctx, nodeKey(202, "model-b"), "{{{", 0).Err())
	misplaced, err := json.Marshal(&NodeState{CredentialID: 999, Model: "elsewhere", SlideWindow: []NodeRecord{}})
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, nodeKey(203, "model-c"), misplaced, 0).Err())
	// (204, model-d) intentionally missing.

	states, err := mgr.GetNodeStatesBatch(ctx, []NodeStateKey{
		{CredentialID: 201, Model: "model-a"},
		{CredentialID: 202, Model: "model-b"},
		{CredentialID: 203, Model: "model-c"},
		{CredentialID: 204, Model: "model-d"},
	})
	require.NoError(t, err)
	require.Len(t, states, 4)

	// The one valid entry keeps its disable state.
	assert.True(t, states[0].Disabled)
	assert.False(t, states[0].IsUsable(time.Now()))

	// Corrupt, identity-mismatched, and missing keys all fail open to a
	// zero (usable) state carrying the requested key's identity.
	for i, k := range []NodeStateKey{{202, "model-b"}, {203, "model-c"}, {204, "model-d"}} {
		assert.False(t, states[i+1].Disabled, "entry %d must fail open", i+1)
		assert.True(t, states[i+1].IsUsable(time.Now()), "entry %d must be usable", i+1)
		assert.Equal(t, k.CredentialID, states[i+1].CredentialID)
		assert.Equal(t, k.Model, states[i+1].Model)
	}
}

func TestRecordNodeOutcomeSelfHealsCorruptState(t *testing.T) {
	mgr, client := newNodeStateTestManager(t)
	ctx := context.Background()

	require.NoError(t, client.Set(ctx, nodeKey(301, "claude-3"), "{corrupt", 0).Err())
	require.NoError(t, mgr.RecordNodeFailure(ctx, 301, "claude-3", "req-1", "timeout"))

	state, err := mgr.GetNodeState(ctx, 301, "claude-3")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 301, state.CredentialID)
	assert.Equal(t, "claude-3", state.Model)
	assert.Equal(t, int64(1), state.FailureCount)
	require.Len(t, state.SlideWindow, 1)
	assert.False(t, state.SlideWindow[0].Success)
	assert.Equal(t, "timeout", state.SlideWindow[0].ErrorKind)

	// The success path self-heals the same way.
	require.NoError(t, client.Set(ctx, nodeKey(302, "gpt-4"), "]]] broken", 0).Err())
	require.NoError(t, mgr.RecordNodeSuccess(ctx, 302, "gpt-4", "req-2"))

	state2, err := mgr.GetNodeState(ctx, 302, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(1), state2.SuccessCount)
	require.Len(t, state2.SlideWindow, 1)
	assert.True(t, state2.SlideWindow[0].Success)
}

func TestRecordNodeOutcomeRewritesWrongTypedFields(t *testing.T) {
	mgr, client := newNodeStateTestManager(t)
	ctx := context.Background()

	// Decodable JSON with wrong field shapes: string counters, non-table
	// window, numeric model. The write path must normalize before encoding
	// so the Go read path never sees a permanently unreadable key.
	payload := `{"credential_id":"401","success_count":"7","slide_window":5,"model":123}`
	require.NoError(t, client.Set(ctx, nodeKey(401, "m"), payload, 0).Err())

	require.NoError(t, mgr.RecordNodeSuccess(ctx, 401, "m", "req-3"))

	state, err := mgr.GetNodeState(ctx, 401, "m")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 401, state.CredentialID)
	assert.Equal(t, "m", state.Model)
	assert.Equal(t, int64(1), state.SuccessCount)
	assert.Equal(t, int64(0), state.FailureCount)
	require.Len(t, state.SlideWindow, 1)
}
