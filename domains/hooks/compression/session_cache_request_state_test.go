package compression

import (
	"context"
	"encoding/json"
	"fmt"
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
func TestCacheWriteRechecksCurrentGuardPair(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, safeCurrent := range []bool{false, true} {
				name := fmt.Sprintf("shared=%v/final=%v/safe_current=%v", shared, final, safeCurrent)
				t.Run(name, func(t *testing.T) {
					var backend SessionCacheBackend
					if shared {
						backend = newSharedTestRedis(t)
					}
					cache := NewSessionCache(backend, nil)
					ctx := WithGeneratedTextGuard(context.Background(), func(_ context.Context, text string) (string, error) {
						return strings.ReplaceAll(text, "UncheckedSecret", "redacted"), nil
					})
					unsafe := []byte(`{"messages":[{"role":"user","content":"UncheckedSecret"}]}`)
					currentBody := unsafe
					if safeCurrent {
						currentBody = []byte(`{"messages":[{"role":"user","content":"safe current"}]}`)
					}
					current := sharedState(currentBody, "")
					current.HasCutMarker = true
					current.CutIndex = 7
					current.CutStrategy = "current-strategy"
					current.AuditScore = 8
					current.CompletedTasks = 5
					require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", current, currentBody))
					body := []byte(`{"messages":[{"role":"user","content":"safe request"}]}`)
					sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
					res := &PrepareResult{MsgCount: 1}
					if final {
						require.NoError(t, sc.CommitFinal(ctx, "shared-tenant", "gw_shared001", body, res))
					} else {
						sc.updateCache(ctx, "shared-tenant", "gw_shared001", sharedState(unsafe, ""), body, res, false, false)
					}
					got, _, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
					require.NoError(t, err)
					require.NotNil(t, got)
					if safeCurrent {
						require.True(t, got.HasCutMarker)
						require.Equal(t, 7, got.CutIndex)
						require.Equal(t, 8, got.AuditScore)
						require.Equal(t, 5, got.CompletedTasks)
					} else {
						require.False(t, got.HasCutMarker)
						require.Zero(t, got.CutIndex)
						require.Empty(t, got.CutStrategy)
					}
				})
			}
		}
	}
}

func TestToolsHashDeltaSurvivesFinalBodyRestoration(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "3")
	cache := NewSessionCache(newSharedTestRedis(t), nil)
	ctx := context.Background()
	a := []byte(`{"messages":[{"role":"user","content":"seed"}],"tools":[{"type":"function","function":{"name":"A"}}]}`)
	b := []byte(`{"messages":[{"role":"user","content":"seed"}],"tools":[{"type":"function","function":{"name":"B"}}]}`)
	state := sharedState(a, "")
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(a, &top))
	hashA := sha256Hex(top["tools"])
	state.ToolsHash = hashA
	require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", state, a))
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	request := []byte(`{"messages":[{"role":"user","content":"seed"},{"role":"user","content":"next"}],"tools":[{"type":"function","function":{"name":"A"}}]}`)
	res := sc.Prepare(ctx, request, "shared-tenant", "gw_shared001", "openai", 128000, false)
	require.NoError(t, json.Unmarshal(res.OutboundBody, &top))
	require.NotEmpty(t, top["_tools_cached"], "cached-tools premise must be exercised")
	latest := sharedState(b, "")
	require.NoError(t, json.Unmarshal(b, &top))
	latest.ToolsHash = sha256Hex(top["tools"])
	latest.AuditScore = 9
	latest.CompletedTasks = 7
	require.NoError(t, cache.Set(ctx, "shared-tenant", "gw_shared001", latest, b))
	require.NoError(t, sc.CommitFinal(ctx, "shared-tenant", "gw_shared001", a, res))
	got, body, err := cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, hashA, got.ToolsHash)
	require.Equal(t, 9, got.AuditScore)
	require.Equal(t, 7, got.CompletedTasks)
	require.Equal(t, string(a), string(body))
	// Requests with no tools must not overwrite a newer tools decision with a
	// stale snapshot; the replacement concerns only fields this request carries.
	plain := []byte(`{"messages":[{"role":"user","content":"no tools"}]}`)
	require.NoError(t, sc.CommitFinal(ctx, "shared-tenant", "gw_shared001", plain, &PrepareResult{}))
	got, _, err = cache.GetOrLoad(ctx, "shared-tenant", "gw_shared001")
	require.NoError(t, err)
	require.Equal(t, hashA, got.ToolsHash)
}
