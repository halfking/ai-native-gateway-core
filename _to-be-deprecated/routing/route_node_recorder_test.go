package routing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/_to-be-deprecated/sessions"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func setupRecorderTest(t *testing.T) (*RouteNodeRecorder, *credentialfpslot.Manager, *sessions.SessionPreference, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	fpSlots := credentialfpslot.New(credentialfpslot.Config{Enabled: true, DefaultLimit: 5}, client)
	sessionPref := sessions.NewSessionPreference(client)
	recorder := NewRouteNodeRecorder(fpSlots, sessionPref)

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return recorder, fpSlots, sessionPref, cleanup
}

func TestRouteNodeRecorder_RecordSuccess(t *testing.T) {
	recorder, fpSlots, sessionPref, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record success
	recorder.RecordSuccess(ctx, 100, "gpt-4", "session-1")

	// Verify RouteNode was updated
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, int64(1), state.SuccessCount)

	// Verify SessionPreference was set
	val, found := sessionPref.Get(ctx, "session-1")
	require.True(t, found)
	assert.Equal(t, 100, val.CredentialID)
	assert.Equal(t, "gpt-4", val.Model)
}

func TestRouteNodeRecorder_RecordSuccess_NoSession(t *testing.T) {
	recorder, fpSlots, _, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record success without session ID
	recorder.RecordSuccess(ctx, 100, "gpt-4", "")

	// Verify RouteNode was still updated
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, int64(1), state.SuccessCount)
}

func TestRouteNodeRecorder_RecordFailure_CredentialLevel(t *testing.T) {
	recorder, fpSlots, _, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record credential-level failure (rate-limit)
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)

	// Verify RouteNode was updated
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, int64(1), state.FailureCount)
}

func TestRouteNodeRecorder_RecordFailure_Transient(t *testing.T) {
	recorder, fpSlots, _, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record transient failures (should NOT update RouteNode)
	transientKinds := []errorsx.ErrorKind{
		errorsx.KindNetwork,
		errorsx.KindTimeout,
		errorsx.KindUpstreamDown,
		errorsx.KindCanceled,
		errorsx.KindContextLength,
	}

	for _, kind := range transientKinds {
		recorder.RecordFailure(ctx, 100, "gpt-4", kind)
	}

	// Verify RouteNode was NOT updated
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	if state != nil {
		assert.Equal(t, int64(0), state.FailureCount, "transient failures should not count")
	}
}

func TestRouteNodeRecorder_RecordFailure_AutoDisable(t *testing.T) {
	recorder, fpSlots, _, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record 3 consecutive credential-level failures
	for i := 0; i < 3; i++ {
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)
	}

	// Verify node is disabled
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Disabled)
	assert.False(t, state.IsUsable(time.Unix(state.LastFailureAt, 0)))
}

func TestRouteNodeRecorder_RecordFatal(t *testing.T) {
	recorder, fpSlots, _, cleanup := setupRecorderTest(t)
	defer cleanup()

	ctx := context.Background()

	// Record fatal error
	recorder.RecordFatal(ctx, 100, "gpt-4", errorsx.KindAuth)

	// Verify RouteNode was updated
	state, err := fpSlots.GetNodeState(ctx, 100, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, int64(1), state.FailureCount)
}

func TestRouteNodeRecorder_NilSafe(t *testing.T) {
	// Nil recorder should not panic
	var recorder *RouteNodeRecorder
	ctx := context.Background()

	assert.NotPanics(t, func() {
		recorder.RecordSuccess(ctx, 100, "gpt-4", "session-1")
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)
		recorder.RecordFatal(ctx, 100, "gpt-4", errorsx.KindAuth)
	})
}

func TestRouteNodeRecorder_NilFpSlots(t *testing.T) {
	// Recorder with nil fpSlots should not panic
	recorder := NewRouteNodeRecorder(nil, nil)
	ctx := context.Background()

	assert.NotPanics(t, func() {
		recorder.RecordSuccess(ctx, 100, "gpt-4", "session-1")
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)
	})
}

func TestIsTransientFailure(t *testing.T) {
	tests := []struct {
		name     string
		kind     errorsx.ErrorKind
		expected bool
	}{
		{"network transient", errorsx.KindNetwork, true},
		{"timeout transient", errorsx.KindTimeout, true},
		{"upstream_down transient", errorsx.KindUpstreamDown, true},
		{"canceled transient", errorsx.KindCanceled, true},
		{"context_length transient", errorsx.KindContextLength, true},
		{"rate_limit NOT transient", errorsx.KindRateLimit, false},
		{"auth NOT transient", errorsx.KindAuth, false},
		{"quota NOT transient", errorsx.KindQuota, false},
		{"empty NOT transient", errorsx.ErrorKind(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTransientFailure(tt.kind)
			assert.Equal(t, tt.expected, result)
		})
	}
}
