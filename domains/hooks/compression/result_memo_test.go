package compression

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeMemoStore is an in-memory memoStore so the memo logic is testable
// without a live Redis. TTL is recorded but not enforced (expiry is Redis' job).
type fakeMemoStore struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration

	getErr error
}

func newFakeMemoStore() *fakeMemoStore {
	return &fakeMemoStore{
		data: map[string][]byte{},
		ttls: map[string]time.Duration{},
	}
}

func (f *fakeMemoStore) get(_ context.Context, key string) ([]byte, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	return v, ok, nil
}

func (f *fakeMemoStore) set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = val
	f.ttls[key] = ttl
	return nil
}

func (f *fakeMemoStore) del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeMemoStore) keyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

func testMemoParts() MemoKeyParts {
	return MemoKeyParts{
		TenantID:      "tenant-a",
		SessionID:     "sess-1",
		Mode:          "smart",
		Protocol:      "openai",
		ContextWindow: 128000,
	}
}

func fullMemoValue() *MemoValue {
	return &MemoValue{
		CompressedBody:       []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Strategy:             "sliding_window_token",
		SummaryMarker:        "[smm_v1:abc]",
		WindowTriggered:      "sliding_window_token",
		MsgCount:             2,
		TokenEst:             120,
		MsgHashes:            json.RawMessage(`[{"index":0,"sha256":"aa"}]`),
		CompressedPrefixHash: "deadbeef",
	}
}

// TestResultMemo_RoundTrip: a stored value comes back with every field intact,
// so a hit can replay the PrepareResult verbatim.
func TestResultMemo_RoundTrip(t *testing.T) {
	store := newFakeMemoStore()
	memo := newResultMemoWithStore(store, time.Minute)
	ctx := context.Background()
	parts := testMemoParts()
	body := []byte(`{"messages":[{"role":"user","content":"long conversation"}]}`)

	got, err := memo.Get(ctx, parts, body)
	if err != nil {
		t.Fatalf("Get on empty memo: %v", err)
	}
	if got != nil {
		t.Fatalf("expected miss on empty memo, got %+v", got)
	}

	want := fullMemoValue()
	if err := memo.Set(ctx, parts, body, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err = memo.Get(ctx, parts, body)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got == nil {
		t.Fatal("expected memo hit after Set")
	}
	if string(got.CompressedBody) != string(want.CompressedBody) {
		t.Errorf("CompressedBody = %q, want %q", got.CompressedBody, want.CompressedBody)
	}
	if got.Strategy != want.Strategy {
		t.Errorf("Strategy = %q, want %q", got.Strategy, want.Strategy)
	}
	if got.SummaryMarker != want.SummaryMarker {
		t.Errorf("SummaryMarker = %q, want %q", got.SummaryMarker, want.SummaryMarker)
	}
	if got.WindowTriggered != want.WindowTriggered {
		t.Errorf("WindowTriggered = %q, want %q", got.WindowTriggered, want.WindowTriggered)
	}
	if got.MsgCount != want.MsgCount || got.TokenEst != want.TokenEst {
		t.Errorf("MsgCount/TokenEst = %d/%d, want %d/%d",
			got.MsgCount, got.TokenEst, want.MsgCount, want.TokenEst)
	}
	if string(got.MsgHashes) != string(want.MsgHashes) {
		t.Errorf("MsgHashes = %s, want %s", got.MsgHashes, want.MsgHashes)
	}
	if got.CompressedPrefixHash != want.CompressedPrefixHash {
		t.Errorf("CompressedPrefixHash = %q, want %q", got.CompressedPrefixHash, want.CompressedPrefixHash)
	}
	if got.CachedAt.IsZero() {
		t.Error("CachedAt should be stamped by Set")
	}
}

// TestResultMemo_KeyIsolation: every key dimension must isolate entries.
// A leak here is the omniroute principalId incident (cross-principal serve).
func TestResultMemo_KeyIsolation(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"same body"}]}`)
	base := testMemoParts()

	variants := map[string]MemoKeyParts{
		"different tenant":         {TenantID: "tenant-b", SessionID: base.SessionID, Mode: base.Mode, Protocol: base.Protocol, ContextWindow: base.ContextWindow},
		"different session":        {TenantID: base.TenantID, SessionID: "sess-2", Mode: base.Mode, Protocol: base.Protocol, ContextWindow: base.ContextWindow},
		"different mode":           {TenantID: base.TenantID, SessionID: base.SessionID, Mode: "aggressive", Protocol: base.Protocol, ContextWindow: base.ContextWindow},
		"different protocol":       {TenantID: base.TenantID, SessionID: base.SessionID, Mode: base.Mode, Protocol: "anthropic-messages", ContextWindow: base.ContextWindow},
		"different context window": {TenantID: base.TenantID, SessionID: base.SessionID, Mode: base.Mode, Protocol: base.Protocol, ContextWindow: 8192},
	}

	for name, other := range variants {
		t.Run(name, func(t *testing.T) {
			store := newFakeMemoStore()
			memo := newResultMemoWithStore(store, time.Minute)
			ctx := context.Background()

			if err := memo.Set(ctx, base, body, fullMemoValue()); err != nil {
				t.Fatalf("Set: %v", err)
			}
			got, err := memo.Get(ctx, other, body)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != nil {
				t.Fatalf("memo leaked across %s", name)
			}
		})
	}
}

// TestResultMemo_BodySensitivity: a different body must not hit.
func TestResultMemo_BodySensitivity(t *testing.T) {
	store := newFakeMemoStore()
	memo := newResultMemoWithStore(store, time.Minute)
	ctx := context.Background()
	parts := testMemoParts()

	if err := memo.Set(ctx, parts, []byte(`{"messages":[{"role":"user","content":"a"}]}`), fullMemoValue()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := memo.Get(ctx, parts, []byte(`{"messages":[{"role":"user","content":"b"}]}`))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("different body must miss")
	}
}

// TestResultMemo_RefusesIncompleteEntries: entries that cannot be replayed
// faithfully are never written.
func TestResultMemo_RefusesIncompleteEntries(t *testing.T) {
	ctx := context.Background()
	parts := testMemoParts()
	body := []byte(`{"messages":[]}`)

	cases := map[string]*MemoValue{
		"no body":     {Strategy: "mechanical_trim"},
		"no strategy": {CompressedBody: []byte(`{"messages":[]}`)},
		"nil value":   nil,
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			store := newFakeMemoStore()
			memo := newResultMemoWithStore(store, time.Minute)
			if err := memo.Set(ctx, parts, body, val); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if store.keyCount() != 0 {
				t.Fatalf("incomplete entry was stored (%d keys)", store.keyCount())
			}
		})
	}
}

// TestResultMemo_MalformedEntryIsMiss: a corrupt payload must not be replayed.
func TestResultMemo_MalformedEntryIsMiss(t *testing.T) {
	store := newFakeMemoStore()
	memo := newResultMemoWithStore(store, time.Minute)
	ctx := context.Background()
	parts := testMemoParts()
	body := []byte(`{"messages":[]}`)

	// Write an entry that unmarshals but lacks a replayable body/strategy.
	_ = store.set(ctx, memoKey(parts, body), []byte(`{"msg_count":3}`), time.Minute)

	got, err := memo.Get(ctx, parts, body)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("entry without body/strategy must be treated as a miss")
	}
}

// TestResultMemo_GetErrorSurfaces: backend errors are returned (Prepare treats
// them as a miss and recomputes).
func TestResultMemo_GetErrorSurfaces(t *testing.T) {
	store := newFakeMemoStore()
	store.getErr = errors.New("redis down")
	memo := newResultMemoWithStore(store, time.Minute)

	got, err := memo.Get(context.Background(), testMemoParts(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
	if got != nil {
		t.Fatalf("expected nil value on error, got %+v", got)
	}
}

// TestResultMemo_RequiresTenantAndSession: without both dimensions the memo
// refuses to operate rather than falling back to a broader key.
func TestResultMemo_RequiresTenantAndSession(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"messages":[]}`)

	for name, parts := range map[string]MemoKeyParts{
		"missing tenant":  {SessionID: "sess-1", Mode: "smart", Protocol: "openai"},
		"missing session": {TenantID: "tenant-a", Mode: "smart", Protocol: "openai"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeMemoStore()
			memo := newResultMemoWithStore(store, time.Minute)

			if err := memo.Set(ctx, parts, body, fullMemoValue()); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if store.keyCount() != 0 {
				t.Fatalf("stored entry without full principal (%d keys)", store.keyCount())
			}
			got, err := memo.Get(ctx, parts, body)
			if err != nil || got != nil {
				t.Fatalf("Get = (%+v, %v), want (nil, nil)", got, err)
			}
		})
	}
}

// TestResultMemo_DisabledIsNoOp: a nil memo and a memo with no backend are both
// safe to call, so Prepare needs no nil checks.
func TestResultMemo_DisabledIsNoOp(t *testing.T) {
	ctx := context.Background()
	parts := testMemoParts()
	body := []byte(`{"messages":[]}`)

	var nilMemo *ResultMemo
	if nilMemo.enabled() {
		t.Fatal("nil memo must not be enabled")
	}
	if got, err := nilMemo.Get(ctx, parts, body); got != nil || err != nil {
		t.Fatalf("nil memo Get = (%+v, %v)", got, err)
	}
	if err := nilMemo.Set(ctx, parts, body, fullMemoValue()); err != nil {
		t.Fatalf("nil memo Set: %v", err)
	}
	if err := nilMemo.Delete(ctx, parts, body); err != nil {
		t.Fatalf("nil memo Delete: %v", err)
	}

	noBackend := NewResultMemo(nil, time.Minute)
	if noBackend.enabled() {
		t.Fatal("memo with nil redis client must not be enabled")
	}
	if got, err := noBackend.Get(ctx, parts, body); got != nil || err != nil {
		t.Fatalf("disabled memo Get = (%+v, %v)", got, err)
	}
}

// TestResultMemo_DefaultTTL: a non-positive TTL falls back to the default.
func TestResultMemo_DefaultTTL(t *testing.T) {
	store := newFakeMemoStore()
	memo := newResultMemoWithStore(store, 0)
	ctx := context.Background()

	if err := memo.Set(ctx, testMemoParts(), []byte(`{"messages":[]}`), fullMemoValue()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.ttls) == 0 {
		t.Fatal("no entry stored")
	}
	for _, ttl := range store.ttls {
		if ttl != DefaultMemoTTL {
			t.Fatalf("ttl = %v, want %v", ttl, DefaultMemoTTL)
		}
	}
}

// TestResultMemo_Delete evicts the entry.
func TestResultMemo_Delete(t *testing.T) {
	store := newFakeMemoStore()
	memo := newResultMemoWithStore(store, time.Minute)
	ctx := context.Background()
	parts := testMemoParts()
	body := []byte(`{"messages":[]}`)

	if err := memo.Set(ctx, parts, body, fullMemoValue()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := memo.Delete(ctx, parts, body); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := memo.Get(ctx, parts, body); got != nil {
		t.Fatal("entry survived Delete")
	}
}

// TestMemoStorable: only the expensive post-window strategies are memoised.
func TestMemoStorable(t *testing.T) {
	for _, s := range []string{"mechanical_trim", "sliding_window_token", "sliding_window_msg_count"} {
		if !memoStorable(s) {
			t.Errorf("memoStorable(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "delta_append", "strip"} {
		if memoStorable(s) {
			t.Errorf("memoStorable(%q) = true, want false", s)
		}
	}
}
