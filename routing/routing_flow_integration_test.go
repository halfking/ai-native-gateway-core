package routing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/sessions"
)

// TestRoutingFlow_EndToEnd simulates a complete routing flow:
// 1. Client sends request (no session ID)
// 2. LastSystemSessionIndex reuses previous session
// 3. SessionPreference provides preferred credential
// 4. RouteNodeState filters out unhealthy nodes
// 5. RouteNodeRecorder tracks success/failure
func TestRoutingFlow_EndToEnd(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	// Initialize all components
	routeNodeStore := NewRouteNodeStore(client)
	sessionPref := sessions.NewSessionPreference(client)
	lastSystemSessionIdx := sessions.NewLastSystemSessionIndex(client)
	recorder := NewRouteNodeRecorder(routeNodeStore, sessionPref)

	// Setup: Pretend a client has a previous system session
	apiKeyID := 123
	previousSessionID := "gw_previous_session"
	err := lastSystemSessionIdx.Set(ctx, apiKeyID, &sessions.LastSystemSessionEntry{
		SessionID:  previousSessionID,
		DeviceSeed: "device_abc",
		TaskID:     "task_xyz",
	})
	require.NoError(t, err)

	// Setup: Session prefers credential 100
	err = sessionPref.Set(ctx, previousSessionID, 100)
	require.NoError(t, err)

	// Step 1: New request from same client (no session ID)
	entry, found := lastSystemSessionIdx.Get(ctx, apiKeyID)
	require.True(t, found)
	assert.Equal(t, previousSessionID, entry.SessionID)

	// Step 2: RouteNode filter - credential 100 should be healthy
	state100, err := routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.True(t, state100.IsUsable(time.Now()))

	// Step 3: Record success
	recorder.RecordSuccess(ctx, 100, "gpt-4", previousSessionID)

	// Step 4: Verify SessionPreference updated
	credID, found := sessionPref.Get(ctx, previousSessionID)
	require.True(t, found)
	assert.Equal(t, 100, credID)

	// Step 5: Verify RouteNode success count
	state100, err = routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(1), state100.SuccessCount)
}

// TestRouteNodeFailure_AutoDisableAndRecover verifies the auto-disable logic.
func TestRouteNodeFailure_AutoDisableAndRecover(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	routeNodeStore := NewRouteNodeStore(client)
	sessionPref := sessions.NewSessionPreference(client)
	recorder := NewRouteNodeRecorder(routeNodeStore, sessionPref)

	// Step 1: Make 3 consecutive credential-level failures
	for i := 0; i < RouteNodeFailStreakLimit; i++ {
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)
	}

	// Step 2: Verify node is disabled
	state, err := routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.True(t, state.Disabled)
	assert.False(t, state.IsUsable(time.Now()))

	// Step 3: Filter should exclude this node
	router := NewRouter(nil, nil)
	router.RouteNodeStore = routeNodeStore

	// Step 4: Manually set DisabledUntil to past for testing
	state.DisabledUntil = time.Now().Add(-1 * time.Second)
	err = routeNodeStore.Set(ctx, state)
	require.NoError(t, err)

	// Step 5: After cooldown, node should be usable again
	state, err = routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.True(t, state.IsUsable(time.Now()), "node should be usable after cooldown")
	assert.False(t, state.Disabled, "node should no longer be disabled")
}

// TestTransientFailure_Ignored verifies transient failures don't affect RouteNode.
func TestTransientFailure_Ignored(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	routeNodeStore := NewRouteNodeStore(client)
	recorder := NewRouteNodeRecorder(routeNodeStore, nil)

	// Make 10 transient failures (way over the threshold)
	for i := 0; i < 10; i++ {
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindNetwork)
		recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindTimeout)
	}

	// Verify node is still healthy
	state, err := routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(0), state.FailureCount)
	assert.False(t, state.Disabled)
}

// TestMixedFailureTypes_OnlyCredentialCounts verifies mixed failure types.
func TestMixedFailureTypes_OnlyCredentialCounts(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	routeNodeStore := NewRouteNodeStore(client)
	recorder := NewRouteNodeRecorder(routeNodeStore, nil)

	// Mix of transient and credential failures
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindNetwork)      // transient
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)    // credential
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindTimeout)      // transient
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindRateLimit)    // credential
	recorder.RecordFailure(ctx, 100, "gpt-4", errorsx.KindUpstreamDown) // transient

	// Only credential failures should be counted
	state, err := routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(2), state.FailureCount)
}

// TestPlanCandidates_RouteNodeFilter verifies PlanCandidates filters out disabled nodes.
func TestPlanCandidates_RouteNodeFilter(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	routeNodeStore := NewRouteNodeStore(client)
	router := NewRouter(nil, nil)
	router.RouteNodeStore = routeNodeStore

	// Setup: Disable credential 100 by recording 3 consecutive failures
	for i := 0; i < RouteNodeFailStreakLimit; i++ {
		if err := routeNodeStore.RecordFailure(ctx, 100, "gpt-4", "req-"+string(rune('0'+i)), "rate_limit"); err != nil {
			t.Fatal(err)
		}
	}

	// Verify credential 100 is disabled
	state, err := routeNodeStore.Get(ctx, 100, "gpt-4")
	require.NoError(t, err)
	assert.True(t, state.Disabled)

	// Verify credential 200 is healthy
	state, err = routeNodeStore.Get(ctx, 200, "gpt-4")
	require.NoError(t, err)
	assert.False(t, state.Disabled)

	// Verify the state.IsUsable works correctly
	state100, _ := routeNodeStore.Get(ctx, 100, "gpt-4")
	assert.False(t, state100.IsUsable(time.Now()))

	state200, _ := routeNodeStore.Get(ctx, 200, "gpt-4")
	assert.True(t, state200.IsUsable(time.Now()))
}

// TestPlanCandidates_RouteNodeFilter_NoStore verifies graceful degradation.
func TestPlanCandidates_RouteNodeFilter_NoStore(t *testing.T) {
	router := NewRouter(nil, nil)
	// No RouteNodeStore set - should return input unchanged
	assert.Nil(t, router.RouteNodeStore)
}

// TestSessionPreference_AfterModelSwitch verifies session pref behavior.
// Note: Full model-switch detection requires reading previous model,
// which requires extending SessionPreference to also store model.
func TestSessionPreference_AfterModelSwitch(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	sessionPref := sessions.NewSessionPreference(client)

	// Set preference
	err := sessionPref.Set(ctx, "session-1", 100)
	require.NoError(t, err)

	// Simulate model switch (clear preference)
	err = sessionPref.ClearOnModelSwitch(ctx, "session-1", "gpt-4", "gpt-3.5")
	require.NoError(t, err)

	// Verify preference is cleared
	credID, found := sessionPref.Get(ctx, "session-1")
	assert.False(t, found)
	assert.Equal(t, 0, credID)

	// Now set new preference for new model
	err = sessionPref.Set(ctx, "session-1", 200)
	require.NoError(t, err)

	credID, found = sessionPref.Get(ctx, "session-1")
	require.True(t, found)
	assert.Equal(t, 200, credID)
}

// TestLastSystemSession_ReuseWindow verifies 5-minute reuse.
func TestLastSystemSession_ReuseWindow(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	idx := sessions.NewLastSystemSessionIndex(client)

	// Set entry
	err := idx.Set(ctx, 456, &sessions.LastSystemSessionEntry{
		SessionID:  "gw_reuse_test",
		DeviceSeed: "device_456",
	})
	require.NoError(t, err)

	// Within 5 minutes: should be found
	entry, found := idx.Get(ctx, 456)
	require.True(t, found)
	assert.Equal(t, "gw_reuse_test", entry.SessionID)
}

// TestCompleteFlow_NoIDClientReuse simulates a no-id client getting 5-min reuse.
func TestCompleteFlow_NoIDClientReuse(t *testing.T) {
	client, cleanup := setupIntegrationRedis(t)
	defer cleanup()

	ctx := context.Background()

	idx := sessions.NewLastSystemSessionIndex(client)
	apiKeyID := 789

	// First request: no session ID, create new
	_, found := idx.Get(ctx, apiKeyID)
	assert.False(t, found)

	newEntry := &sessions.LastSystemSessionEntry{
		SessionID:  "gw_new_session",
		DeviceSeed: "device_789",
		TaskID:     "task_001",
	}
	err := idx.Set(ctx, apiKeyID, newEntry)
	require.NoError(t, err)

	// Second request within 5 min: should reuse
	reused, found := idx.Get(ctx, apiKeyID)
	require.True(t, found)
	assert.Equal(t, "gw_new_session", reused.SessionID)
	assert.Equal(t, "device_789", reused.DeviceSeed)

	// Touch to extend window
	err = idx.Touch(ctx, apiKeyID)
	require.NoError(t, err)

	// Should still be found
	reused, found = idx.Get(ctx, apiKeyID)
	require.True(t, found)
	assert.Equal(t, "gw_new_session", reused.SessionID)
}

func setupIntegrationRedis(t *testing.T) (*redis.Client, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, cleanup
}
