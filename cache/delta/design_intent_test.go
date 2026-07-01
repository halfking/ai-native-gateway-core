package delta

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/cache/kv"
)

// TestDesignIntent_DeltaStoreIsTransparentReplacement demonstrates the central
// design promise: callers can replace their existing kv.Store with
// NewDeltaStore(inner) without changing any other code. The Store, Lookup,
// Invalidate, Stats methods are drop-in compatible.
func TestDesignIntent_DeltaStoreIsTransparentReplacement(t *testing.T) {
	// Construct a DeltaStore as the type the interface specifies.
	var s kv.Store = NewDeltaStore(kv.NewInMemoryStore())
	ctx := context.Background()

	// Use it exactly like a kv.Store.
	_ = s.Store(ctx, "k1", []byte(`{"hello":"world"}`), time.Minute)
	got, ok, err := s.Lookup(ctx, "k1")
	if err != nil || !ok || string(got) != `{"hello":"world"}` {
		t.Errorf("DeltaStore not transparent: got=%q ok=%v err=%v", got, ok, err)
	}
	_ = s.Invalidate(ctx, "k1")
	if _, ok, _ := s.Lookup(ctx, "k1"); ok {
		t.Errorf("After Invalidate: ok=true, want false")
	}
}

// TestDesignIntent_EncodeApplyRoundTrip demonstrates the AC-2 contract: for
// any chat-completion body that Encode decides to compress, Apply must
// reconstruct the exact bytes. The property is observable in three
// independent ways here to catch any drift in either direction.
func TestDesignIntent_EncodeApplyRoundTrip(t *testing.T) {
	cases := []struct {
		label   string
		parent  string
		newBody string
	}{
		{
			"single_turn_growth",
			`{"messages":[{"role":"user","content":"hi"}]}`,
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello!"}]}`,
		},
		{
			"history_then_rewrite",
			`{"messages":[{"role":"system","content":"You are a translator"},{"role":"user","content":"Hello"},{"role":"assistant","content":"你好"}]}`,
			`{"messages":[{"role":"system","content":"You are a translator"},{"role":"user","content":"Hello"},{"role":"assistant","content":"您好"}]}`,
		},
		{
			"shared_long_prefix",
			strings.Repeat("X", 500),
			strings.Repeat("X", 500) + "additional tail bytes appended here",
		},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			d, saved := Encode([]byte(c.parent), []byte(c.newBody))
			if !saved {
				t.Skipf("Encode did not save (parent not long enough relative to new)")
			}
			recon, err := Apply([]byte(c.parent), d)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(recon) != c.newBody {
				t.Errorf("Round-trip mismatch:\n got=%q\nwant=%q", recon, c.newBody)
			}
		})
	}
}

// TestDesignIntent_ParentChainReconstruction walks the chain k0 → k1 → ... → k9
// and verifies each level reconstructs correctly. This is the AC-3 + AC-5
// combined: DeltaStore satisfies kv.Store AND achieves compression.
func TestDesignIntent_ParentChainReconstruction(t *testing.T) {
	s := NewDeltaStore(kv.NewInMemoryStore())
	ctx := context.Background()

	// Build a 10-turn chain. The bodies share a long system prompt + history
	// prefix; only the last turn changes (this is the canonical chat pattern).
	const sharedPrefix = `{"model":"claude-opus-4","messages":[{"role":"system","content":"You are an expert software engineer. Help the user debug and design production code. Be concise, technical, and precise."}`
	bodies := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		bodies[i] = []byte(sharedPrefix +
			`{"role":"user","content":"Please help me with step ` + intToStr(i+1) + ` of the workflow."}` +
			`{"role":"assistant","content":"Sure, here is the answer for step ` + intToStr(i+1) + `."}]}`)
	}
	if err := s.Store(ctx, "k0", bodies[0], time.Hour); err != nil {
		t.Fatalf("Store k0: %v", err)
	}
	for i := 1; i < 10; i++ {
		s.SetParent("k"+intToStr(i), "k"+intToStr(i-1))
		if err := s.Store(ctx, "k"+intToStr(i), bodies[i], time.Hour); err != nil {
			t.Fatalf("Store k%d: %v", i, err)
		}
	}

	// Each Lookup must reconstruct the exact body.
	for i := 0; i < 10; i++ {
		got, ok, err := s.Lookup(ctx, "k"+intToStr(i))
		if err != nil || !ok {
			t.Fatalf("Lookup k%d: ok=%v, err=%v", i, ok, err)
		}
		if string(got) != string(bodies[i]) {
			t.Errorf("Lookup k%d: bytes mismatch", i)
		}
	}

	// At least 9 of the 10 stores should be delta-compressed (k0 is the root).
	ds := s.DeltaStats()
	if ds.DeltaStores < 9 {
		t.Errorf("DeltaStores=%d, want >= 9 (all but root should compress)", ds.DeltaStores)
	}
	if ds.BytesSaved <= 0 {
		t.Errorf("BytesSaved=%d, want > 0", ds.BytesSaved)
	}
	t.Logf("Compression: %d DeltaStores, %d FullStores, %d BytesSaved (%.1f%% of naive)",
		ds.DeltaStores, ds.FullStores, ds.BytesSaved,
		float64(ds.BytesSaved)/float64(sumLens(bodies))*100)
}

// TestDesignIntent_OnDiskFormat pins the wire format. The DeltaStore encodes
// stored entries as JSON. If the format changes (renamed fields, removed
// fields), existing cached entries become unreadable. This test guards
// against accidental breakage.
func TestDesignIntent_OnDiskFormat(t *testing.T) {
	d := Delta{
		Op:        OpAppend,
		ParentKey: "abc123",
		Payload:   []byte(`[+1 turn]`),
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Field names are part of the public wire format.
	mustContain := []string{`"op":"append"`, `"parent_key":"abc123"`, `"payload":"`}
	for _, want := range mustContain {
		if !strings.Contains(string(b), want) {
			t.Errorf("Wire format missing %q: %s", want, b)
		}
	}
	// Cutoff is omitempty and zero — must NOT appear.
	if strings.Contains(string(b), `"cutoff"`) {
		t.Errorf("Wire format includes cutoff when 0 (omitempty broken): %s", b)
	}
}

// TestDesignIntent_GracefulDegradation documents the failure modes and their
// observable behavior. The cache is an optimization — every failure must
// degrade to a miss or an explicit error, never a panic or corrupted read.
func TestDesignIntent_GracefulDegradation(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(s *DeltaStore)
		action func(s *DeltaStore) (ok bool)
	}{
		{
			"missing key → miss",
			func(s *DeltaStore) {},
			func(s *DeltaStore) bool {
				_, ok, _ := s.Lookup(context.Background(), "nope")
				return !ok
			},
		},
		{
			"empty key → error",
			func(s *DeltaStore) {},
			func(s *DeltaStore) bool {
				_, _, err := s.Lookup(context.Background(), "")
				return err != nil
			},
		},
		{
			"empty payload Store → error",
			func(s *DeltaStore) {},
			func(s *DeltaStore) bool {
				err := s.Store(context.Background(), "k", nil, time.Minute)
				return err != nil
			},
		},
		{
			"parent evicted before child Store → child falls back to OpFull",
			func(s *DeltaStore) {
				_ = s.Store(context.Background(), "p", []byte(`{"x":1}`), time.Minute)
				s.SetParent("c", "p")
				_ = s.Invalidate(context.Background(), "p")
			},
			func(s *DeltaStore) bool {
				err := s.Store(context.Background(), "c", []byte(`{"x":2}`), time.Minute)
				ds := s.DeltaStats()
				return err == nil && ds.DeltaStores == 0
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewDeltaStore(kv.NewInMemoryStore())
			c.setup(s)
			if !c.action(s) {
				t.Errorf("Degradation case %q: unexpected behavior", c.name)
			}
		})
	}
}

// sumLens returns the total byte length of all bodies — used for compression
// ratio reporting in TestDesignIntent_ParentChainReconstruction.
func sumLens(bodies [][]byte) int {
	total := 0
	for _, b := range bodies {
		total += len(b)
	}
	return total
}
