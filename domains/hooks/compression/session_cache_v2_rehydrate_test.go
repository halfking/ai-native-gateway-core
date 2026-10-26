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

// The complete shared envelope preserves lineage even when a V2 TurnReader
// would return a differently wrapped message array. Redis' acknowledged pair
// is authoritative, so its provider envelope and original hash stay intact.
func TestSessionCache_L2ExactEnvelopeKeepsDeltaLineage(t *testing.T) {
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

// Once Set acknowledges a complete shared pair, a lagging or independently
// newer V2 DB snapshot cannot replace its body or discard its metadata.
func TestSessionCache_L2KeepsAcknowledgedPairOverDifferentDBTurn(t *testing.T) {
	ctx := context.Background()
	redis := newRehydrateRedis()
	envelope := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"old"}]}`)
	writer := NewSessionCache(redis, nil)
	state := &SessionState{SchemaVersion: schemaVersion, LastOutboundHash: sha256Hex(envelope), MsgCount: 1, TokenEstimate: 4, SummaryMarker: "smm_v1:committed"}
	requireNoError(t, writer.Set(ctx, "tenant-1", "session-1", state, envelope))
	reader := NewSessionCache(redis, nil)
	reader.SetTurnReader(&fakeTurnReader{latest: []v2.Message{{Role: "user", Content: "new"}, {Role: "assistant", Content: "different-turn"}}})
	got, body, err := reader.GetOrLoad(ctx, "tenant-1", "session-1")
	requireNoError(t, err)
	if got == nil || got.MsgCount != 1 || got.LastOutboundHash != sha256Hex(envelope) || got.SummaryMarker != state.SummaryMarker || string(body) != string(envelope) {
		t.Fatalf("acknowledged pair replaced by unrelated DB turn: state=%+v body=%s", got, body)
	}
}
