package delta

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/cache/kv"
)

// TestEdge_RatioOK_TotalZero: ratioOK with total=0 must return false
// (avoids division by zero — also handled by the float conversion, but
// explicit is safer for future readers).
func TestEdge_RatioOK_TotalZero(t *testing.T) {
	if ratioOK(10, 0) {
		t.Errorf("ratioOK(10, 0) = true, want false (would divide by zero)")
	}
	if ratioOK(0, 0) {
		t.Errorf("ratioOK(0, 0) = true, want false")
	}
}

// TestEdge_RatioOK_Boundary: ratioOK exactly at threshold should be true
// (the comparison is >=, not >). This pins the boundary behavior.
func TestEdge_RatioOK_Boundary(t *testing.T) {
	if !ratioOK(30, 100) {
		t.Errorf("ratioOK(30, 100) = false, want true (exactly at threshold)")
	}
	if ratioOK(29, 100) {
		t.Errorf("ratioOK(29, 100) = true, want false (just below threshold)")
	}
}

// TestEdge_SetParent_EmptyParentKey_ClearsEntry: SetParent("c", "") must
// remove the parent for "c" — subsequent Store should fall back to OpFull.
func TestEdge_SetParent_EmptyParentKey_ClearsEntry(t *testing.T) {
	s := makeDeltaStore()
	ctx := context.Background()
	s.SetParent("c", "p")
	s.SetParent("c", "") // clear
	if err := s.Store(ctx, "p", []byte(`{"a":1}`), time.Minute); err != nil {
		t.Fatalf("Store p: %v", err)
	}
	if err := s.Store(ctx, "c", []byte(`{"a":2}`), time.Minute); err != nil {
		t.Fatalf("Store c: %v", err)
	}
	ds := s.DeltaStats()
	if ds.DeltaStores != 0 {
		t.Errorf("DeltaStores=%d, want 0 (parent cleared)", ds.DeltaStores)
	}
}

// TestEdge_Lookup_InnerError: when the inner store returns an error,
// Lookup must propagate it (not swallow to a miss).
func TestEdge_Lookup_InnerError(t *testing.T) {
	inner := &fakeKV{err: errors.New("simulated backend failure")}
	s := NewDeltaStore(inner)
	_, _, err := s.Lookup(context.Background(), "any")
	if err == nil {
		t.Errorf("Lookup on failing inner: err=nil, want error propagation")
	}
	ds := s.DeltaStats()
	if ds.InnerErrors == 0 {
		t.Errorf("InnerErrors=0, want > 0 (error propagated)")
	}
}

// TestEdge_Lookup_CorruptEntry: stored Delta that fails JSON unmarshal
// must be treated as cache miss, not error.
func TestEdge_Lookup_CorruptEntry(t *testing.T) {
	inner := &fakeKV{
		data: map[string][]byte{
			"k1": []byte(`{not valid json`), // malformed
		},
	}
	s := NewDeltaStore(inner)
	_, ok, err := s.Lookup(context.Background(), "k1")
	if err != nil {
		t.Errorf("Lookup on corrupt entry: err=%v, want nil (treat as miss)", err)
	}
	if ok {
		t.Errorf("Lookup on corrupt entry: ok=true, want false")
	}
	ds := s.DeltaStats()
	if ds.CorruptEntries == 0 {
		t.Errorf("CorruptEntries=0, want > 0")
	}
}

// TestEdge_ResolveChain_TooDeep: chain depth > MaxChainDepth must return
// cache miss (not loop forever).
func TestEdge_ResolveChain_TooDeep(t *testing.T) {
	inner := kv.NewInMemoryStore()
	ctx := context.Background()

	rootDelta := Delta{Op: OpFull, Payload: []byte(`root_payload`)}
	rootBytes, _ := json.Marshal(rootDelta)
	if err := inner.Store(ctx, "k0", rootBytes, time.Minute); err != nil {
		t.Fatalf("Store k0: %v", err)
	}
	// Store MaxChainDepth + 5 nested deltas.
	for i := 1; i <= MaxChainDepth+5; i++ {
		key := "k" + intToStr(i)
		parentKey := "k" + intToStr(i-1)
		d := Delta{Op: OpReplaceTail, ParentKey: parentKey, Cutoff: 0, Payload: []byte(`x`)}
		db, _ := json.Marshal(d)
		if err := inner.Store(ctx, key, db, time.Minute); err != nil {
			t.Fatalf("Store %s: %v", key, err)
		}
	}

	s := NewDeltaStore(inner)
	deepest := "k" + intToStr(MaxChainDepth+5)
	_, ok, err := s.Lookup(ctx, deepest)
	if err != nil {
		t.Fatalf("Lookup deepest: err=%v, want nil (cache miss is silent)", err)
	}
	if ok {
		t.Errorf("Lookup deepest (over chain depth): ok=true, want false (chain too deep)")
	}
}

// TestEdge_Store_StatsAfterError: if inner.Store fails, the counters must
// not be incremented.
func TestEdge_Store_StatsAfterError(t *testing.T) {
	inner := &fakeKV{storeErr: errors.New("disk full")}
	s := NewDeltaStore(inner)
	err := s.Store(context.Background(), "k", []byte(`x`), time.Minute)
	if err == nil {
		t.Fatalf("Store on failing inner: err=nil, want error")
	}
	ds := s.DeltaStats()
	if ds.FullStores != 0 || ds.DeltaStores != 0 {
		t.Errorf("Stats after failed Store: FullStores=%d DeltaStores=%d, want 0/0", ds.FullStores, ds.DeltaStores)
	}
	if ds.InnerErrors == 0 {
		t.Errorf("InnerErrors=0, want > 0")
	}
}

// TestEdge_Invalidate_InnerError: Invalidate must propagate inner errors.
func TestEdge_Invalidate_InnerError(t *testing.T) {
	inner := &fakeKV{invalidateErr: errors.New("network down")}
	s := NewDeltaStore(inner)
	err := s.Invalidate(context.Background(), "k")
	if err == nil {
		t.Errorf("Invalidate on failing inner: err=nil, want error propagation")
	}
}

// TestEdge_Encode_ParentLongerThanNew: when new is shorter than parent
// (e.g. conversation truncated), the algorithm should not crash and
// Apply must round-trip correctly.
func TestEdge_Encode_ParentLongerThanNew(t *testing.T) {
	parent := []byte(`{"messages":[m1,m2,m3,m4,m5]}`)
	newB := []byte(`{"messages":[m1,m2]`)
	d, _ := Encode(parent, newB)
	recon, err := Apply(parent, d)
	if err != nil {
		t.Fatalf("Apply parent>new: %v", err)
	}
	if !bytesEqual(recon, newB) {
		t.Errorf("Apply parent>new: got %q, want %q", recon, newB)
	}
}

// TestEdge_StatsCompressionRatio_NegativeBytesSaved: should not panic.
func TestEdge_StatsCompressionRatio_NegativeBytesSaved(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CompressionRatio panicked on edge input: %v", r)
		}
	}()
	s := Stats{BytesSaved: -100, FullStores: 1}
	_ = s.CompressionRatio(100)
}

// TestEdge_Lookup_EmptyKey: empty key must return ErrEmptyKey.
func TestEdge_Lookup_EmptyKey(t *testing.T) {
	s := makeDeltaStore()
	_, _, err := s.Lookup(context.Background(), "")
	if !errors.Is(err, kv.ErrEmptyKey) {
		t.Errorf("Lookup empty key: err=%v, want kv.ErrEmptyKey", err)
	}
}

// --- fakeKV: minimal kv.Store double for error-injection tests ---

type fakeKV struct {
	data          map[string][]byte
	err           error
	storeErr      error
	invalidateErr error
}

func (f *fakeKV) Lookup(_ context.Context, key string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	v, ok := f.data[key]
	return v, ok, nil
}

func (f *fakeKV) Store(_ context.Context, key string, payload []byte, _ time.Duration) error {
	if f.storeErr != nil {
		return f.storeErr
	}
	if f.data == nil {
		f.data = make(map[string][]byte)
	}
	stored := make([]byte, len(payload))
	copy(stored, payload)
	f.data[key] = stored
	return nil
}

func (f *fakeKV) Invalidate(_ context.Context, _ string) error {
	if f.invalidateErr != nil {
		return f.invalidateErr
	}
	return nil
}

func (f *fakeKV) Stats() kv.Stats { return kv.Stats{} }
