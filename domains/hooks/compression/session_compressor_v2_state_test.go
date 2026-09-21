package compression

import (
	"context"
	"errors"
	"testing"
)

// stubStateReader implements V2StateReader for tests.
type stubStateReader struct {
	has  bool
	err  error
	meta map[string]any
}

func (s stubStateReader) HasState(_ context.Context, _, _ string) (bool, error) {
	return s.has, s.err
}

func (s stubStateReader) CompressionMetadata(_ context.Context, _, _ string) (map[string]any, error) {
	return s.meta, nil
}

// stubOutboundBuilder implements V2OutboundBuilder for tests.
type stubOutboundBuilder struct {
	body []byte
	err  error
}

func (s stubOutboundBuilder) BuildLatestOutbound(_ context.Context, _, _ string) ([]byte, error) {
	return s.body, s.err
}

func TestLoadV2CompressionState_RestoresRecoveryMetadata(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{CacheV2: stubStateReader{
		meta: map[string]any{
			"summary_marker": "[smm_v1:abc]",
			"cut_marker": map[string]interface{}{
				"created_at": float64(123), "source_msg_count": float64(10),
				"system_msg_count": float64(1), "cut_index": float64(4),
				"strategy": "smart_window_llm", "summary_marker": "[smm_v1:abc]",
			},
			"pre_sanitize_offset_range": []int{1, 5},
			"alignment_map": []map[string]interface{}{{
				"original_index": float64(2), "compressed_index": float64(1), "hash": "abc",
			}},
			"sanitize_map_ref": "session:tenant:session:sanitize",
			"sanitize_message_refs": []map[string]interface{}{{
				"raw_index": float64(2), "sanitized_index": float64(2),
				"raw_hash": "a", "sanitized_hash": "b", "changed": true,
			}},
		},
	}}}
	state := sc.loadV2CompressionState(context.Background(), "tenant", "session")
	if state == nil || !state.HasCutMarker || state.CutIndex != 4 || state.CutPreSanitizeEnd != 5 {
		t.Fatalf("cut metadata not restored: %+v", state)
	}
	if len(state.AlignmentMap) != 1 || len(state.SanitizeMessageRefs) != 1 || state.SanitizeMapRef == "" {
		t.Fatalf("provenance metadata not restored: %+v", state)
	}
}

// TestTryLoadV2State_Success covers the happy path: prior state exists
// and the builder returns a body → (body, true). This is the branch the
// real SessionCacheV2 + OutboundBuilder will hit in production, and it
// was previously untested because the deps used concrete types that
// required a live database.
func TestTryLoadV2State_Success(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{has: true},
		Builder: stubOutboundBuilder{body: []byte(`[{"role":"user","content":"hi"}]`)},
	}}

	body, ok := sc.tryLoadV2State(context.Background(), "t1", "s1")
	if !ok {
		t.Fatal("expected ok=true on success path")
	}
	if string(body) != `[{"role":"user","content":"hi"}]` {
		t.Fatalf("unexpected body: %s", body)
	}
}

// TestTryLoadV2State_NewSession covers a brand-new session: no prior
// state. The contract is ok=true with an empty body (NOT a fallback) so
// the caller treats it as a fresh session rather than dropping to V1.
func TestTryLoadV2State_NewSession(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{has: false},
		Builder: stubOutboundBuilder{}, // should not be called
	}}

	body, ok := sc.tryLoadV2State(context.Background(), "t1", "s1")
	if !ok {
		t.Fatal("expected ok=true for a new session (not a fallback)")
	}
	if body != nil {
		t.Fatalf("expected nil body for new session, got %s", body)
	}
}

// TestTryLoadV2State_CacheErrorFallback verifies a reader error triggers
// a V1 fallback (ok=false) rather than propagating.
func TestTryLoadV2State_CacheErrorFallback(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{err: errors.New("db down")},
		Builder: stubOutboundBuilder{body: []byte("should-not-reach")},
	}}

	body, ok := sc.tryLoadV2State(context.Background(), "t1", "s1")
	if ok {
		t.Fatal("expected ok=false (fallback) when reader errors")
	}
	if body != nil {
		t.Fatalf("expected nil body on fallback, got %s", body)
	}
}

// TestTryLoadV2State_BuilderErrorFallback verifies a builder error after
// state exists also triggers fallback.
func TestTryLoadV2State_BuilderErrorFallback(t *testing.T) {
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{has: true},
		Builder: stubOutboundBuilder{err: errors.New("marshal failed")},
	}}

	body, ok := sc.tryLoadV2State(context.Background(), "t1", "s1")
	if ok {
		t.Fatal("expected ok=false (fallback) when builder errors")
	}
	if body != nil {
		t.Fatalf("expected nil body on fallback, got %s", body)
	}
}
