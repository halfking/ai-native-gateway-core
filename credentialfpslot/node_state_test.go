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
