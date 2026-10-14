package compression

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// fakeTurnReader stubs TurnReaderSource for the L2→L3 rehydrate path.
type fakeTurnReader struct {
	latest []v2.Message
}

func (f *fakeTurnReader) LoadLatestOutbound(context.Context, string, string) ([]v2.Message, error) {
	return f.latest, nil
}

func (f *fakeTurnReader) LoadChain(context.Context, string, string, int) ([]v2.Message, error) {
	return f.latest, nil
}

// TestSessionCache_L2RehydrateFromV2KeepsDeltaLineage pins the V1-fallback
// rehydrate contract: when the body comes back from the V2 session_bodies
// reader (a message array re-wrapped as {"messages":[...]}) while the Redis
// state still carries the hash of the full outbound envelope
// ({"model":...,"messages":[...]}), the state+body pair handed to the delta
// builder must stay lineage-consistent. Before the fix, the envelope hash was
// left untouched, BuildOutboundMessages' lineage guard rejected the pairing,
// and every L1-evicted session silently reset to a full client resend.
func TestSessionCache_L2RehydrateFromV2KeepsDeltaLineage(t *testing.T) {
	ctx := context.Background()
	redis := newRehydrateRedis()

	// What CommitFinal persisted: full outbound envelope, hash over it.
	envelope := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"there"}]}`)
	writer := NewSessionCache(redis, nil)
	state := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: sha256Hex(envelope),
		MsgCount:         2,
		TokenEstimate:    9,
	}
	if err := writer.Set(ctx, "tenant-1", "session-1", state, envelope); err != nil {
		t.Fatal(err)
	}

	// What the V2 reader returns: the message array stored in session_bodies.
	reader := NewSessionCache(redis, nil)
	reader.SetTurnReader(&fakeTurnReader{latest: []v2.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "there"},
	}})

	gotState, gotBody, err := reader.GetOrLoad(ctx, "tenant-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotState == nil || len(gotBody) == 0 {
		t.Fatalf("expected rehydrated state+body, got state=%v bodyLen=%d", gotState, len(gotBody))
	}
	if gotState.LastOutboundHash != sha256Hex(gotBody) {
		t.Fatalf("rehydrated pair is not self-consistent: state hash %s != body hash %s",
			gotState.LastOutboundHash, sha256Hex(gotBody))
	}

	// Same-layer consumer: the delta builder must treat the pair as this
	// session's lineage, not as a brand-new session.
	clientBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"there"},{"role":"user","content":"next"}]}`)
	res, err := BuildOutboundMessages(clientBody, gotState, gotBody, "openai-chat")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsNewSess {
		t.Fatal("lineage guard rejected the V2-rehydrated pair: session reset to full resend")
	}
	if res.MsgCount != 3 {
		t.Fatalf("merged outbound message count = %d, want 3 (prior 2 + delta 1)", res.MsgCount)
	}
}

// TestSessionCache_L2CountMismatchDropsStaleRedisMetadata pins the
// genuinely-different-turns contract. When the V2 body's message count
// disagrees with the Redis state, that body must never be bound to the stale
// metadata (no laundering) — but the reader must not collapse to an empty
// body either: an empty body makes diff.go's lineage guard reset the session
// to a full client resend and caches an incoherent pair in L1. The
// self-consistent L3 pair replaces both wholesale, Redis-only fields from the
// abandoned turn included.
//
// History: the 2026-10-02 integration audit
// (TestStorageAuditObserveV2MetadataBodyMismatch, storage_audit_integration_
// test.go) specified this contract first but never ran — no CI entry wires
// SESSION_AUDIT_REDIS_ADDR — so r1031 pinned the opposite ("body stays
// empty") without knowing. First-ever execution of the audit suite on
// 2026-10-14 exposed the contradiction; this test now pins the corrected
// contract at the same layer as the unit suite.
func TestSessionCache_L2CountMismatchDropsStaleRedisMetadata(t *testing.T) {
	ctx := context.Background()
	redis := newRehydrateRedis()

	envelope := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"old"}]}`)
	writer := NewSessionCache(redis, nil)
	state := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: sha256Hex(envelope),
		MsgCount:         1,
		TokenEstimate:    4,
		SummaryMarker:    "smm_v1:stale",
	}
	if err := writer.Set(ctx, "tenant-1", "session-1", state, envelope); err != nil {
		t.Fatal(err)
	}

	reader := NewSessionCache(redis, nil)
	reader.SetTurnReader(&fakeTurnReader{latest: []v2.Message{
		{Role: "user", Content: "new"},
		{Role: "assistant", Content: "different-turn"},
	}})

	gotState, gotBody, err := reader.GetOrLoad(ctx, "tenant-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBody) == 0 {
		t.Fatal("stale Redis metadata collapsed the rehydrate to an empty body: lineage would reset to full resend")
	}
	if gotState == nil || gotState.LastOutboundHash != sha256Hex(gotBody) {
		t.Fatalf("adopted pair is not self-consistent: state=%v bodyLen=%d", gotState, len(gotBody))
	}
	if gotState.MsgCount != 2 {
		t.Fatalf("adopted state msg count = %d, want 2 (the L3 pair's count)", gotState.MsgCount)
	}
	// The stale metadata is dropped wholesale, not merged: Redis-only fields
	// from the abandoned turn must not survive onto the adopted pair.
	if gotState.SummaryMarker != "" {
		t.Fatalf("stale summary marker %q laundered onto the adopted L3 pair", gotState.SummaryMarker)
	}

	// Same-layer consumer: the adopted pair carries this session's lineage.
	clientBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"new"},{"role":"assistant","content":"different-turn"},{"role":"user","content":"next"}]}`)
	res, err := BuildOutboundMessages(clientBody, gotState, gotBody, "openai-chat")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsNewSess {
		t.Fatal("lineage guard rejected the adopted L3 pair: session reset to full resend")
	}
	if res.MsgCount != 3 {
		t.Fatalf("merged outbound message count = %d, want 3 (prior 2 + delta 1)", res.MsgCount)
	}
}
