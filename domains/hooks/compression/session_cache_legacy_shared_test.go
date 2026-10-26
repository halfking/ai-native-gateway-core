package compression

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSessionCacheSharedLegacyMigrationRequiresExactBodyHash(t *testing.T) {
	for _, matches := range []bool{true, false} {
		name := "mismatch"
		if matches {
			name = "exact"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r := newSharedTestRedis(t)
			exact := []byte(`{"model":"m","messages":[{"role":"user","content":"committed"}],"unknown":9007199254740993}`)
			persisted := exact
			if !matches {
				persisted = []byte(`{"messages":[{"role":"user","content":"different"}]}`)
			}
			// Literal v1 namespace is intentional: it is the old metadata-only format.
			require.NoError(t, r.HSet(ctx, "session:sc:shared-tenant:gw_shared001:v1", encodeSessionStateFields(sharedState(exact, "legacy"))...))
			db := &rehydrateDB{row: &LastOutboundRow{OutboundBody: persisted, OutboundMsgCount: 1}}
			reader := NewSessionCache(r, db)
			st, body, err := reader.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
			if !matches {
				require.Error(t, err, "same count is insufficient to match old metadata to L3")
				require.Nil(t, st)
				require.Nil(t, body)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, st)
			require.Equal(t, "legacy", st.SanitizeMapGeneration)
			require.Equal(t, string(exact), string(body))
			// A successful migration must survive without the legacy row/body source.
			require.NoError(t, r.Del(ctx, "session:sc:shared-tenant:gw_shared001:v1"))
			requireSharedPair(t, NewSessionCache(r, nil), exact, "legacy")
		})
	}
}
