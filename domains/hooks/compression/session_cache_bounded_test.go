package compression

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/require"
)

func TestL1UpdateEnforcesExistingAggregateByteBudget(t *testing.T) {
	previous := settings.Global
	t.Cleanup(func() { settings.Global = previous })
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: map[string][]byte{"cache.session_l1_max_bytes": []byte("16777216")}})
	for _, spec := range settings.CompressionSpecs() {
		if spec.Key == "cache.session_l1_max_bytes" {
			registry.MustRegisterSpec(spec)
		}
	}
	settings.Global = registry
	cache := NewSessionCache(nil, nil)
	body := []byte(strings.Repeat("x", 1<<20))
	for i := 0; i < 14; i++ {
		cache.setL1("key"+strings.Repeat("x", i), stateOf("state"), body)
	}
	cache.setL1("key", stateOf("updated"), []byte(strings.Repeat("y", 4<<20)))
	require.LessOrEqual(t, cache.curBytes, 16<<20, "update returned before enforcing the byte budget")
	require.Equal(t, "updated", cache.l1["key"].state.SystemPrompt)
}

func TestStrictLocalReadRejectsBeforeCopyingLegacyBody(t *testing.T) {
	cache := NewSessionCache(nil, nil)
	cache.setL1(l1Key("t", "s"), stateOf("legacy"), []byte(strings.Repeat("x", (1<<20)+1)))
	_, _, err := cache.getLocal(strictCacheContext(), "t", "s")
	require.Error(t, err)
}

type boundedReadProbe struct{ *sharedTestRedis }

func (r boundedReadProbe) HGetAll(context.Context, string) (map[string]string, error) {
	return nil, errors.New("unbounded HGETALL must not execute")
}

func (r boundedReadProbe) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	if strings.Contains(script, "session-cache-bounded-read") {
		script = `local previous=redis.call
redis.call=function(command,...)
 if command == 'HMGET' then previous('INCR','test:bulk-fetches') end
 return previous(command,...)
end
` + script
	}
	return r.sharedTestRedis.Eval(ctx, script, keys, args...)
}

func strictCacheContext() context.Context {
	return contextbudget.WithSpec(context.Background(), contextbudget.Spec{ContextWindow: 1000000})
}

func TestSharedStrictReadRejectsBeforeFetchingLargeValues(t *testing.T) {
	for _, field := range []string{"state", "body", "body_size", "revision"} {
		t.Run(field, func(t *testing.T) {
			base := newSharedTestRedis(t)
			limit := 256 << 10
			if field == "body" {
				limit = 1 << 20
			}
			require.NoError(t, base.HSet(context.Background(), redisKey("t", "s"), field, strings.Repeat("x", limit+1)))
			_, _, _, err := NewSessionCache(boundedReadProbe{base}, nil).readShared(strictCacheContext(), "t", "s")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "HGETALL")
			require.False(t, base.server.Exists("test:bulk-fetches"), "oversized bytes were fetched before checking their lengths")
		})
	}
}

func TestSharedStrictReadPreservesExactSafePair(t *testing.T) {
	base := newSharedTestRedis(t)
	body := []byte(`{"messages":[{"role":"user","content":"safe"}]}`)
	require.NoError(t, NewSessionCache(base, nil).Set(context.Background(), "t", "s", sharedState(body, "g"), body))
	state, got, _, err := NewSessionCache(boundedReadProbe{base}, nil).readShared(strictCacheContext(), "t", "s")
	require.NoError(t, err)
	require.Equal(t, body, got)
	require.Equal(t, "g", state.SanitizeMapGeneration)
	bulk, err := base.server.Get("test:bulk-fetches")
	require.NoError(t, err)
	require.Equal(t, "1", bulk)
}

func TestSharedStrictRejectsDeclaredInflationBeforeFetch(t *testing.T) {
	base := newSharedTestRedis(t)
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}]}`)
	require.NoError(t, NewSessionCache(base, nil).Set(context.Background(), "t", "s", sharedState(body, "g"), body))
	_, _, _, err := NewSessionCache(boundedReadProbe{base}, nil).readShared(strictCacheContext(), "t", "s")
	require.Error(t, err)
	require.False(t, base.server.Exists("test:bulk-fetches"))
}

func TestSharedStrictLegacyCannotFallThroughToUnboundedDB(t *testing.T) {
	base := newSharedTestRedis(t)
	require.NoError(t, base.HSet(context.Background(), legacyRedisKey("t", "s"), "sys", strings.Repeat("x", (256<<10)+1)))
	db := &rehydrateDB{}
	_, _, err := NewSessionCache(boundedReadProbe{base}, db).loadSharedLocked(strictCacheContext(), "t", "s", "token")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "HGETALL")
	require.Zero(t, db.calls)
	require.False(t, base.server.Exists("test:bulk-fetches"))
}
