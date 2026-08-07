package compression

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// recordingBackend counts HSet calls so a test can assert whether the V1
// cache was written. Other SessionCacheBackend methods are no-ops.
type recordingBackend struct {
	hsets int32
}

func (r *recordingBackend) HSet(_ context.Context, _ string, _ ...any) error {
	atomic.AddInt32(&r.hsets, 1)
	return nil
}
func (r *recordingBackend) HGetAll(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *recordingBackend) Expire(_ context.Context, _ string, _ time.Duration) error { return nil }
func (r *recordingBackend) Del(_ context.Context, _ string) error                     { return nil }

// TestPrepare_V2Path_DoesNotPoisonV1Cache guards the fix for V2→V1 cache
// poisoning. When a request is served from the V2 read path, updateCache
// must NOT write back into the V1 cache, otherwise the V2-sourced session
// gets a corrupted V1 entry (LastCompressedAt=now, no SummaryMarker) that
// would mislead any later V1 fallback for the same session.
//
// We wire a recordingBackend into the V1 SessionCache and assert that a
// V2-served Prepare triggers zero HSet calls, while a non-V2 (new-session)
// Prepare does write.
func TestPrepare_V2Path_DoesNotPoisonV1Cache(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", true)

	rec := &recordingBackend{}
	v1Cache := NewSessionCache(rec, nil) // nil DB → GetOrLoad returns empty

	// --- V2-served request ---
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{has: true},
		Builder: stubOutboundBuilder{body: []byte(`[{"role":"user","content":"prev"}]`)},
		Cache:   v1Cache,
	}}
	clientBody := []byte(`{"messages":[` +
		`{"role":"user","content":"prev"},` +
		`{"role":"assistant","content":"reply"}` +
		`]}`)
	sc.Prepare(context.Background(), clientBody, "t1", "gw_nopoison01", "openai", 0, false)

	if got := atomic.LoadInt32(&rec.hsets); got != 0 {
		t.Fatalf("V2 path must not write V1 cache, got %d HSet calls", got)
	}

	// --- Control: with the flag OFF, the same request goes through the V1
	// path and SHOULD write the V1 cache. This proves the recordingBackend
	// is actually wired and that the zero-HSet assertion above is meaningful
	// (not just a broken harness).
	t.Run("control_flag_off_writes_v1", func(t *testing.T) {
		withPlatformFlag(t, "sessions_v2_compression_read", false)
		rec2 := &recordingBackend{}
		v1Cache2 := NewSessionCache(rec2, nil)
		sc2 := &SessionCompressor{deps: SessionCompressorDeps{
			CacheV2: stubStateReader{has: true}, // ignored: flag off
			Builder: stubOutboundBuilder{},
			Cache:   v1Cache2,
		}}
		fresh := []byte(`{"messages":[{"role":"user","content":"first"}]}`)
		sc2.Prepare(context.Background(), fresh, "t1", "gw_nopoison02", "openai", 0, false)
		if got := atomic.LoadInt32(&rec2.hsets); got == 0 {
			t.Fatal("V1 path (flag off) should write V1 cache (control assertion)")
		}
	})
}
