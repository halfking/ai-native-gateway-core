package compression

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strconv"
	"strings"
	"testing"
)

func TestSessionCacheSharedRejectsCorruptPairEvenWithWarmL1(t *testing.T) {
	for _, field := range []string{"pair_v", "revision", "state", "state_hash", "body", "body_codec", "body_size", "body_hash"} {
		t.Run(field, func(t *testing.T) {
			r := newSharedTestRedis(t)
			cache := NewSessionCache(r, nil)
			body := []byte("exact body")
			require.NoError(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
			require.NoError(t, r.HSet(context.Background(), redisKey("shared-tenant", "gw_shared001"), field, "corrupt"))
			for _, reader := range []*SessionCache{cache, NewSessionCache(r, nil)} {
				state, got, err := reader.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
				require.Error(t, err)
				require.Nil(t, state)
				require.Nil(t, got)
			}
		})
	}
}
func TestSessionCacheSharedBodyAndStateLimitsAreExplicit(t *testing.T) {
	r := newSharedTestRedis(t)
	cache := NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", "3")
	require.Error(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "large"), body))
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", "128")
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_STATE_BYTES", "32")
	require.Error(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "large"), body))
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_STATE_BYTES", strconv.Itoa(sharedStateLimit))
	requireSharedPair(t, cache, body, "base")
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", "invalid")
	require.Error(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "invalid"), body))
}
func TestSessionCacheSharedGzipDecodedLimitCannotBeBypassed(t *testing.T) {
	r := newSharedTestRedis(t)
	body := []byte(strings.Repeat("x", 4096))
	cache := NewSessionCache(r, nil)
	require.NoError(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	fields, err := r.HGetAll(context.Background(), redisKey("shared-tenant", "gw_shared001"))
	require.NoError(t, err)
	require.Equal(t, "gzip", fields["body_codec"])
	require.Less(t, len(fields["body"]), 128)
	require.NoError(t, r.HSet(context.Background(), redisKey("shared-tenant", "gw_shared001"), "body_size", "64"))
	t.Setenv("LLM_GATEWAY_SESSION_CACHE_MAX_BODY_BYTES", "128")
	_, _, err = cache.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.Error(t, err)
}
func TestSessionCacheSharedInvalidStateSchemaNeverAcknowledged(t *testing.T) {
	r := newSharedTestRedis(t)
	cache := NewSessionCache(r, nil)
	require.Error(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", &SessionState{SchemaVersion: 99}, []byte("body")))
	fields, err := r.HGetAll(context.Background(), redisKey("shared-tenant", "gw_shared001"))
	require.NoError(t, err)
	require.Empty(t, fields)
}
func TestSessionCacheSharedStateHashDoesNotReplaceBodyHashCheck(t *testing.T) {
	r := newSharedTestRedis(t)
	cache := NewSessionCache(r, nil)
	body := []byte("body")
	require.NoError(t, cache.Set(context.Background(), "shared-tenant", "gw_shared001", sharedState(body, "base"), body))
	st := sharedState(body, "base")
	st.LastOutboundHash = "another body's hash"
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, r.HSet(context.Background(), redisKey("shared-tenant", "gw_shared001"), "state", string(raw), "state_hash", sha256Hex(raw)))
	_, _, err = cache.GetOrLoad(context.Background(), "shared-tenant", "gw_shared001")
	require.Error(t, err)
}
