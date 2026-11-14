package compression

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCacheSharedLargeBodySurvivesWarmL1(t *testing.T) {
	r := newSharedTestRedis(t)
	writer, reader := NewSessionCache(r, nil), NewSessionCache(r, nil)
	body := []byte(strings.Repeat("x", (1<<20)+1))
	require.NoError(t, writer.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "large"), body))
	for _, cache := range []*SessionCache{writer, reader, reader, writer} {
		state, got, err := cache.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
		require.NoError(t, err)
		require.NotNil(t, state)
		require.Equal(t, "large", state.SanitizeMapGeneration)
		require.True(t, bytes.Equal(body, got), "complete body required: got %d bytes, want %d", len(got), len(body))
		require.Equal(t, sha256Hex(got), state.LastOutboundHash)
		cache.mu.Lock()
		entry := cache.l1[l1Key("shared-tenant", "gw_shared001")]
		oversized, retained := entry.oversized, len(entry.body)
		cache.mu.Unlock()
		require.True(t, oversized)
		require.Zero(t, retained, "complete Redis reads must not retain large L1 bodies")
	}
}
