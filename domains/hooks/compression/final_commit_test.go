package compression

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCommitFinal_CacheMatchesMockSupplierRequest(t *testing.T) {
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	original := []byte(`{"messages":[{"role":"user","content":"latest"},{"role":"system","content":"stable"}]}`)
	result := sc.Prepare(ctx, original, "tenant-final", "gw_final01", "openai", 0, false)
	result.CompressedPrefixHash = "stale-prefix-hash"

	// Simulate the common handler transforms that run after Prepare.
	finalBody := []byte(`{"messages":[{"role":"system","content":"stable"},{"role":"user","content":"latest"}]}`)
	if err := sc.CommitFinal(ctx, "tenant-final", "gw_final01", finalBody, result); err != nil {
		t.Fatalf("CommitFinal failed: %v", err)
	}

	var supplierBody []byte
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		supplierBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read supplier request failed: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(supplier.Close)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, supplier.URL, bytes.NewReader(finalBody))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mock supplier request failed: %v", err)
	}
	_ = resp.Body.Close()

	cachedState, cachedBody, err := cache.GetOrLoad(ctx, "tenant-final", "gw_final01")
	if err != nil {
		t.Fatalf("GetOrLoad failed: %v", err)
	}
	if !bytes.Equal(cachedBody, supplierBody) {
		t.Fatalf("cache/supplier mismatch:\n cache=%s\n supplier=%s", cachedBody, supplierBody)
	}
	if bytes.Equal(cachedBody, original) {
		t.Fatal("cache retained pre-transform body instead of final supplier body")
	}
	if cachedState.MsgCount != 2 || result.MsgCount != 2 {
		t.Fatalf("final message count mismatch: cache=%d result=%d", cachedState.MsgCount, result.MsgCount)
	}
	if cachedState.LastOutboundHash != sha256Hex(supplierBody) {
		t.Fatalf("final body hash mismatch: got %s", cachedState.LastOutboundHash)
	}
	if len(result.MsgHashes) == 0 {
		t.Fatal("final result message hashes were not recomputed")
	}
	if result.CompressedPrefixHash == "stale-prefix-hash" {
		t.Fatal("final compressed prefix hash was not recomputed")
	}
}

func TestCommitFinal_V2SourceDoesNotWriteV1Cache(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", true)
	rec := &recordingBackend{}
	sc := NewSessionCompressor(SessionCompressorDeps{
		Cache:   NewSessionCache(rec, nil),
		CacheV2: stubStateReader{has: true},
		Builder: stubOutboundBuilder{body: []byte(`[{"role":"user","content":"prior"}]`)},
	})
	clientBody := []byte(`{"messages":[{"role":"user","content":"prior"},{"role":"assistant","content":"reply"}]}`)
	result := sc.Prepare(context.Background(), clientBody, "tenant-v2", "gw_finalv2", "openai", 0, false)
	if err := sc.CommitFinal(context.Background(), "tenant-v2", "gw_finalv2", clientBody, result); err != nil {
		t.Fatalf("CommitFinal failed: %v", err)
	}
	if got := atomic.LoadInt32(&rec.hsets); got != 0 {
		t.Fatalf("V2-sourced final commit wrote V1 cache %d times", got)
	}
}

func TestCommitFinal_RejectsInvalidSessionIDWithoutCacheWrite(t *testing.T) {
	rec := &recordingBackend{}
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: NewSessionCache(rec, nil)})
	err := sc.CommitFinal(
		context.Background(),
		"tenant-invalid",
		"session forbidden",
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		&PrepareResult{},
	)
	if err == nil {
		t.Fatal("invalid session id should fail final cache commit")
	}
	if got := atomic.LoadInt32(&rec.hsets); got != 0 {
		t.Fatalf("invalid session id wrote cache %d times", got)
	}
}

func TestCommitFinal_PreservesExistingSessionState(t *testing.T) {
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	previous := &SessionState{
		SchemaVersion:       schemaVersion,
		StripsApplied:       4,
		MessagesAfterStrip:  12,
		TokensAfterStrip:    640,
		LastStripAt:         123,
		HasCutMarker:        true,
		CutIndex:            7,
		CutStrategy:         "smart",
		ApprovalStatus:      "approved",
		ApprovalID:          "approval-1",
		AuditScore:          9,
		SecurityScore:       8,
		SensitiveDetected:   true,
		OptimizationApplied: "strip_tools",
		AlignmentMap: []AlignmentInfo{
			{OriginalIndex: 0, CompressedIndex: 0, Hash: "old"},
		},
	}
	if err := cache.Set(ctx, "tenant-state", "gw_state01", previous, []byte(`{"messages":[]}`)); err != nil {
		t.Fatal(err)
	}

	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	clientBody := []byte(`{"messages":[{"role":"user","content":"new"}]}`)
	result := sc.Prepare(ctx, clientBody, "tenant-state", "gw_state01", "openai", 0, false)
	if err := sc.CommitFinal(ctx, "tenant-state", "gw_state01", clientBody, result); err != nil {
		t.Fatalf("CommitFinal failed: %v", err)
	}

	got, gotBody, err := cache.GetOrLoad(ctx, "tenant-state", "gw_state01")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, clientBody) {
		t.Fatalf("final body mismatch: got %s want %s", gotBody, clientBody)
	}
	if got.StripsApplied != previous.StripsApplied || got.MessagesAfterStrip != previous.MessagesAfterStrip ||
		got.TokensAfterStrip != previous.TokensAfterStrip || got.LastStripAt != previous.LastStripAt {
		t.Fatalf("strip state lost: got %+v", got)
	}
	if !got.HasCutMarker || got.CutIndex != previous.CutIndex || got.CutStrategy != previous.CutStrategy {
		t.Fatalf("cut marker state lost: got %+v", got)
	}
	if got.ApprovalStatus != previous.ApprovalStatus || got.ApprovalID != previous.ApprovalID ||
		got.AuditScore != previous.AuditScore || got.SecurityScore != previous.SecurityScore ||
		!got.SensitiveDetected || got.OptimizationApplied != previous.OptimizationApplied {
		t.Fatalf("governance state lost: got %+v", got)
	}
	if len(got.AlignmentMap) != 1 || got.AlignmentMap[0].Hash != "old" {
		t.Fatalf("previous alignment map lost: %+v", got.AlignmentMap)
	}
}
