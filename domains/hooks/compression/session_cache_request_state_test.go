package compression

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoReviewPrepareToolsHashChangePersists(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "3")
	for _, shared := range []bool{false, true} {
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			var backend SessionCacheBackend
			if shared {
				backend = newSharedTestRedis(t)
			}
			cache := NewSessionCache(backend, nil)
			ctx := context.Background()
			old := []byte(`{"messages":[{"role":"user","content":"seed"}],"tools":[{"type":"function","function":{"name":"A"}}]}`)
			changed := []byte(`{"messages":[{"role":"user","content":"seed"},{"role":"user","content":"second"}],"tools":[{"type":"function","function":{"name":"B"}}]}`)
			returned := []byte(`{"messages":[{"role":"user","content":"seed"},{"role":"user","content":"second"},{"role":"user","content":"third"}],"tools":[{"type":"function","function":{"name":"A"}}]}`)
			state := sharedState(old, "")
			var top map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(old, &top))
			state.ToolsHash = sha256Hex(top["tools"])
			require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", state, old))
			sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
			_ = sc.Prepare(ctx, changed, "shared-tenant", "gw_shared001", "openai", 128000, false)
			got, _, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(changed, &top))
			if got.ToolsHash != sha256Hex(top["tools"]) {
				t.Errorf("changed tools B hash not persisted: got=%s want=%s", got.ToolsHash, sha256Hex(top["tools"]))
			}
			res := sc.Prepare(ctx, returned, "shared-tenant", "gw_shared001", "openai", 128000, false)
			require.NotEmpty(t, res.OutboundBody)
			require.NoError(t, json.Unmarshal(res.OutboundBody, &top))
			if len(top["_tools_cached"]) > 0 {
				t.Error("A falsely treated as still cached after upstream changed to B")
			}
		})
	}
}

func TestGoReviewPrepareGuardRejectionClearsCutLineage(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "3")
	for _, shared := range []bool{false, true} {
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			var backend SessionCacheBackend
			if shared {
				backend = newSharedTestRedis(t)
			}
			cache := NewSessionCache(backend, nil)
			unsafe := []byte(`{"messages":[{"role":"user","content":"[smm_v1:old] UncheckedSecret"}]}`)
			safe := []byte(`{"messages":[{"role":"user","content":"safe new request"}]}`)
			ctx := WithGeneratedTextGuard(context.Background(), func(_ context.Context, text string) (string, error) {
				return strings.ReplaceAll(text, "UncheckedSecret", "redacted"), nil
			})
			require.False(t, cachedBodyPassesGuard(ctx, unsafe))
			require.True(t, cachedBodyPassesGuard(ctx, safe))
			state := sharedState(unsafe, "")
			state.HasCutMarker = true
			state.CutIndex = 7
			state.CutStrategy = "old-strategy"
			state.CompressedPrefixHash = "old-prefix"
			state.StripsApplied = 4
			require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", state, unsafe))
			sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
			_ = sc.Prepare(ctx, safe, "shared-tenant", "gw_shared001", "openai", 128000, false)
			got, body, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
			require.NoError(t, err)
			require.Equal(t, string(safe), string(body))
			if got.HasCutMarker || got.CutIndex != 0 || got.CutStrategy != "" {
				t.Errorf("guard-rejected body lineage rebound to safe replacement: hasCut=%v cutIndex=%d strategy=%q", got.HasCutMarker, got.CutIndex, got.CutStrategy)
			}
		})
	}
}

type goReviewSwapAfterRead struct {
	*sharedTestRedis
	once sync.Once
	swap func()
}

func (r *goReviewSwapAfterRead) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	fields, err := r.sharedTestRedis.HGetAll(ctx, key)
	if err == nil && len(fields) > 0 {
		r.once.Do(r.swap)
	}
	return fields, err
}
func TestGoReviewRecoveryUsesOneStateBodyPair(t *testing.T) {
	ctx := context.Background()
	base := newSharedTestRedis(t)
	writer := NewSessionCache(base, nil)
	old := makeBodyAny(makeMsg("system", "sys"), makeMsg("user", smartWindowSummaryPrefix+"summary A"), makeMsg("user", "tail A"))
	fresh := makeBodyAny(makeMsg("system", "sys"), makeMsg("user", smartWindowSummaryPrefix+"summary B"), makeMsg("user", "tail B"))
	markerA := CutMarker{Version: 1, CreatedAt: time.Now().Unix(), SourceMsgCount: 6, SystemMsgCount: 1, CutIndex: 2, SummaryMarker: BuildSummaryMarker("summary A"), Strategy: "smart_window_llm"}
	markerB := markerA
	markerB.CutIndex = 4
	markerB.SummaryMarker = BuildSummaryMarker("summary B")
	stateA := sharedState(old, "")
	stateA.SetCutMarker(markerA)
	stateB := sharedState(fresh, "")
	stateB.SetCutMarker(markerB)
	require.NoError(t, writer.Set(ctx, "shared-tenant", "gw_shared001", stateA, old))
	swapping := &goReviewSwapAfterRead{sharedTestRedis: base, swap: func() { require.NoError(t, writer.Set(ctx, "shared-tenant", "gw_shared001", stateB, fresh)) }}
	rc := NewRecoveryCoordinator(RecoveryDeps{Cache: NewSessionCache(swapping, nil), MaxRetries: 2})
	body := makeBodyAny(makeMsg("system", "sys"), makeMsg("user", strings.Repeat("A ", 2000)), makeMsg("assistant", strings.Repeat("B ", 2000)), makeMsg("user", strings.Repeat("C ", 2000)), makeMsg("assistant", strings.Repeat("D ", 2000)), makeMsg("user", "latest"))
	got := rc.Recover(ctx, body, "openai", 5000, "shared-tenant", "gw_shared001", 0)
	require.Equal(t, "incremental_cache", got.Strategy)
	require.NotNil(t, got.CutMarker)
	if got.CutMarker.SummaryMarker == markerA.SummaryMarker && got.CutMarker.SummaryText != "summary A" {
		t.Errorf("torn pair: cutA=%d/markerA plus summary=%q", got.CutMarker.CutIndex, got.CutMarker.SummaryText)
	}
}

// Both writes must decide inheritance from the pair held by Update. An earlier
// rejected snapshot must neither resurrect unsafe lineage nor erase a newer
// safe writer's metadata.
