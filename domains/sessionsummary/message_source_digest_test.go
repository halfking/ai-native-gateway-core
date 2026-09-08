package sessionsummary

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// digestEnvelopeJSON builds the JSONB shape session_turns.digest has on disk:
// a sessiondigest envelope produced by the write path (session_writer_v2 →
// sessiondigest.Build → Marshal).
func digestEnvelopeJSON(t *testing.T, userContent, assistantContent string) []byte {
	t.Helper()
	request := []map[string]any{{"role": "user", "content": userContent}}
	var response any
	if assistantContent != "" {
		response = []map[string]any{{"role": "assistant", "content": assistantContent}}
	}
	envelope := sessiondigest.Build(request, response,
		map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		map[string]any{}, time.Unix(1000, 0))
	if envelope == nil {
		t.Fatal("sessiondigest.Build returned nil envelope")
	}
	raw, err := sessiondigest.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

// TestExpandDigestTurn_UserAndAssistant is the B#4 core contract: a turn's
// persisted digest expands to BOTH the user prompt and the assistant reply,
// in that order, carrying the turn's request/model/ts metadata.
func TestExpandDigestTurn_UserAndAssistant(t *testing.T) {
	row := digestTurnRow{
		RequestID: "req-1",
		Model:     "glm-4.6",
		Ts:        time.Unix(1000, 0),
		Digest:    digestEnvelopeJSON(t, "帮我看看这个报错", "这是空指针，建议在第 42 行判空"),
	}
	msgs := expandDigestTurn(row)
	if len(msgs) != 2 {
		t.Fatalf("expandDigestTurn produced %d messages, want 2 (user+assistant): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "帮我看看这个报错" {
		t.Errorf("msgs[0] = %+v, want user prompt", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "这是空指针，建议在第 42 行判空" {
		t.Errorf("msgs[1] = %+v, want assistant reply", msgs[1])
	}
	for i, m := range msgs {
		if m.RequestID != "req-1" || m.Model != "glm-4.6" || !m.Timestamp.Equal(row.Ts) {
			t.Errorf("msgs[%d] metadata not carried: %+v", i, m)
		}
	}
}

// TestExpandDigestTurn_EmptySideSkipped: a turn whose digest lacks one side
// (e.g. failed request with no response body) still contributes the other.
func TestExpandDigestTurn_EmptySideSkipped(t *testing.T) {
	onlyUser := expandDigestTurn(digestTurnRow{
		RequestID: "req-2",
		Digest:    digestEnvelopeJSON(t, "只有提问", ""),
	})
	if len(onlyUser) != 1 || onlyUser[0].Role != "user" {
		t.Fatalf("want single user message, got %+v", onlyUser)
	}

	onlyAssistant := expandDigestTurn(digestTurnRow{
		RequestID: "req-3",
		Digest:    digestEnvelopeJSON(t, "", "只有回复"),
	})
	if len(onlyAssistant) != 1 || onlyAssistant[0].Role != "assistant" {
		t.Fatalf("want single assistant message, got %+v", onlyAssistant)
	}
}

// TestExpandDigestTurn_SkipsUnusable confirms turns with a NULL / malformed /
// unsupported-version digest contribute nothing (mirroring how V1/V2 sources
// skip turns without a usable request_delta), instead of emitting empty or
// poisoned rows.
func TestExpandDigestTurn_SkipsUnusable(t *testing.T) {
	cases := map[string][]byte{
		"nil column":       nil,
		"empty column":     {},
		"json null":        []byte("null"),
		"malformed json":   []byte("{not-json"),
		"future version":   []byte(`{"schema_version":999,"algorithm_version":"warp-v9","generated_at":"2026-01-01T00:00:00Z","payload":{"user_input":"x","assistant_output":"y"}}`),
		"both sides empty": digestEnvelopeJSON(t, "", ""),
	}
	for name, raw := range cases {
		if got := expandDigestTurn(digestTurnRow{RequestID: "req-x", Digest: raw}); len(got) != 0 {
			t.Errorf("%s: expected no messages, got %+v", name, got)
		}
	}
}

// TestExpandDigestTurns_PreservesTurnOrder: rows arrive ts-ascending from the
// query; expansion must interleave user/assistant per turn in the same order
// (u1,a1,u2,a2,...) — the summary prompt relies on chronological order.
func TestExpandDigestTurns_PreservesTurnOrder(t *testing.T) {
	rows := []digestTurnRow{
		{RequestID: "req-1", Ts: time.Unix(1, 0), Digest: digestEnvelopeJSON(t, "问1", "答1")},
		{RequestID: "req-2", Ts: time.Unix(2, 0), Digest: digestEnvelopeJSON(t, "问2", "")}, // assistant empty
		{RequestID: "req-3", Ts: time.Unix(3, 0), Digest: nil},                             // skipped entirely
		{RequestID: "req-4", Ts: time.Unix(4, 0), Digest: digestEnvelopeJSON(t, "问4", "答4")},
	}
	got := expandDigestTurns(rows)
	wantRoles := []string{"user", "assistant", "user", "user", "assistant"}
	wantContents := []string{"问1", "答1", "问2", "问4", "答4"}
	if len(got) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i := range wantRoles {
		if got[i].Role != wantRoles[i] || got[i].Content != wantContents[i] {
			t.Errorf("msgs[%d] = (%s,%q), want (%s,%q)", i, got[i].Role, got[i].Content, wantRoles[i], wantContents[i])
		}
	}
	if !got[0].Timestamp.Before(got[2].Timestamp) {
		t.Errorf("timestamps not ascending: %v then %v", got[0].Timestamp, got[2].Timestamp)
	}
}

// TestExpandDigestTurn_TruncatesRunesSafely exercises the defensive cap on a
// drifted envelope whose payload exceeds the persisted 260-rune ceiling
// (sessiondigest.Build compacts at write time; this guards legacy/future
// rows). The cut must be rune-based — byte slicing would shred multi-byte CJK
// into invalid UTF-8 (the 2026-09-08 b1f9da6ca bug class).
func TestExpandDigestTurn_TruncatesRunesSafely(t *testing.T) {
	longUser := strings.Repeat("问", 400)
	longAssistant := strings.Repeat("答", 400)
	envelope := &sessiondigest.Envelope{
		SchemaVersion:    sessiondigest.SchemaVersion,
		AlgorithmVersion: sessiondigest.AlgorithmVersion,
		GeneratedAt:      time.Unix(1000, 0).UTC(),
		Source:           sessiondigest.Source,
		Payload: sessiondigest.Digest{
			UserInput:       longUser,
			AssistantOutput: longAssistant,
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal drifted envelope: %v", err)
	}
	msgs := expandDigestTurn(digestTurnRow{RequestID: "req-long", Digest: raw})
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	for i, m := range msgs {
		runes := []rune(m.Content)
		if len(runes) != perTurnDigestMaxRunes {
			t.Errorf("msgs[%d] length = %d runes, want cap %d", i, len(runes), perTurnDigestMaxRunes)
		}
		if !utf8.ValidString(m.Content) {
			t.Errorf("msgs[%d] content is not valid UTF-8 (rune-unsafe cut)", i)
		}
	}
	if msgs[0].Content != strings.Repeat("问", perTurnDigestMaxRunes) {
		t.Errorf("user content truncated mid-content: %q...", msgs[0].Content[:20])
	}
}

// TestPerTurnDigestSourceFetchQuery (pgxmock) locks the SQL contract: single
// view, session/tenant/since filters, ascending ts, LIMIT 20 — the same turn
// window as the V1/V2 sources — and digest-column scanning.
func TestPerTurnDigestSourceFetchQuery(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	ts := time.Unix(2000, 0)
	rows := pgxmock.NewRows([]string{"request_id", "model", "ts", "digest"}).
		AddRow("req-1", "glm-4.6", ts, digestEnvelopeJSON(t, "问1", "答1")).
		AddRow("req-2", "", ts.Add(time.Second), nil)
	mock.ExpectQuery(`session_turns_with_current_month`).
		WithArgs("sess-1", "tenant-1", ts.Add(-time.Minute)).
		WillReturnRows(rows)

	src := &perTurnDigestSource{pool: mock}
	turns, err := src.fetchDigestTurns(context.Background(), "tenant-1", "sess-1", ts.Add(-time.Minute))
	if err != nil {
		t.Fatalf("fetchDigestTurns: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations not met: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].RequestID != "req-1" || turns[0].Model != "glm-4.6" || !turns[0].Ts.Equal(ts) {
		t.Errorf("turns[0] = %+v", turns[0])
	}
	if turns[1].Model != "" { // COALESCE(model,'')
		t.Errorf("turns[1].Model = %q, want empty string", turns[1].Model)
	}
	if len(turns[1].Digest) != 0 {
		t.Errorf("turns[1].Digest = %q, want empty (NULL scanned)", turns[1].Digest)
	}

	msgs := expandDigestTurns(turns)
	if len(msgs) != 2 { // req-1: user+assistant; req-2 (NULL digest): skipped
		t.Fatalf("expandDigestTurns produced %d messages, want 2", len(msgs))
	}
}

// TestPerTurnDigestSource_NilPoolIsSafe matches the other sources'
// nil-safety contract: error, not panic.
func TestPerTurnDigestSource_NilPoolIsSafe(t *testing.T) {
	src := &perTurnDigestSource{pool: nil}
	if _, err := src.GetSessionMessages(context.Background(), "t", "s"); err == nil {
		t.Fatal("GetSessionMessages with nil pool: expected error")
	}
	if _, err := src.GetMessagesSince(context.Background(), "t", "s", time.Time{}); err == nil {
		t.Fatal("GetMessagesSince with nil pool: expected error")
	}
}

// fakeGateSource records which underlying source the gate delegated to.
type fakeGateSource struct {
	tag string
	err error
}

func (f *fakeGateSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []SessionMessage{{RequestID: f.tag, Role: "user", Content: f.tag}}, nil
}

func (f *fakeGateSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []SessionMessage{{RequestID: f.tag, Role: "assistant", Content: f.tag}}, nil
}

// TestGatedPerTurnDigestSource_Delegation is the gate contract:
//   - flag off → every read is delegated verbatim to the fallback (the V2
//     bodies source in production), byte-identical to the pre-B#4 behavior;
//   - flag on → the digest semantics;
//   - the flag is re-read per call, so toggling hot-reloads.
func TestGatedPerTurnDigestSource_Delegation(t *testing.T) {
	digest := &fakeGateSource{tag: "digest"}
	fallback := &fakeGateSource{tag: "v2"}
	on := false
	gate := &gatedPerTurnDigestSource{digest: digest, fallback: fallback, enabled: func() bool { return on }}

	// Off: both reads hit the fallback.
	got, err := gate.GetSessionMessages(context.Background(), "t", "s")
	if err != nil || len(got) != 1 || got[0].RequestID != "v2" {
		t.Fatalf("flag off GetSessionMessages = (%+v,%v), want fallback", got, err)
	}
	got, err = gate.GetMessagesSince(context.Background(), "t", "s", time.Time{})
	if err != nil || len(got) != 1 || got[0].RequestID != "v2" {
		t.Fatalf("flag off GetMessagesSince = (%+v,%v), want fallback", got, err)
	}

	// Flip the flag between calls (no rewire): now both reads hit digest.
	on = true
	got, err = gate.GetSessionMessages(context.Background(), "t", "s")
	if err != nil || len(got) != 1 || got[0].RequestID != "digest" {
		t.Fatalf("flag on GetSessionMessages = (%+v,%v), want digest", got, err)
	}
	got, err = gate.GetMessagesSince(context.Background(), "t", "s", time.Time{})
	if err != nil || len(got) != 1 || got[0].RequestID != "digest" {
		t.Fatalf("flag on GetMessagesSince = (%+v,%v), want digest", got, err)
	}
}

// TestGatedPerTurnDigestSource_PropagatesErrors: source errors must reach the
// caller regardless of gate state (summary generation fails loudly).
func TestGatedPerTurnDigestSource_PropagatesErrors(t *testing.T) {
	sentinel := errors.New("source unavailable")
	for _, on := range []bool{false, true} {
		gate := &gatedPerTurnDigestSource{
			digest:   &fakeGateSource{err: sentinel},
			fallback: &fakeGateSource{err: sentinel},
			enabled:  func() bool { return on },
		}
		if _, err := gate.GetSessionMessages(context.Background(), "t", "s"); !errors.Is(err, sentinel) {
			t.Errorf("on=%v GetSessionMessages err = %v, want %v", on, err, sentinel)
		}
		if _, err := gate.GetMessagesSince(context.Background(), "t", "s", time.Time{}); !errors.Is(err, sentinel) {
			t.Errorf("on=%v GetMessagesSince err = %v, want %v", on, err, sentinel)
		}
	}
}

// TestDefaultPerTurnDigestEnabled_DefaultsOff pins the safe default: with no
// settings registry initialized (settings.Global nil), the gate reads false —
// the per-turn digest layer must never activate silently.
func TestDefaultPerTurnDigestEnabled_DefaultsOff(t *testing.T) {
	saved := settings.Global
	settings.Global = nil
	defer func() { settings.Global = saved }()

	if defaultPerTurnDigestEnabled() {
		t.Fatal("defaultPerTurnDigestEnabled = true with nil registry, want default false")
	}
}

// TestNewPerTurnDigestSource_Wiring checks the production constructor wires
// the gate to the digest source + V2 fallback with the real settings reader.
func TestNewPerTurnDigestSource_Wiring(t *testing.T) {
	src := NewPerTurnDigestSource(nil)
	gate, ok := src.(*gatedPerTurnDigestSource)
	if !ok {
		t.Fatalf("NewPerTurnDigestSource returned %T, want *gatedPerTurnDigestSource", src)
	}
	if _, ok := gate.digest.(*perTurnDigestSource); !ok {
		t.Errorf("gate.digest = %T, want *perTurnDigestSource", gate.digest)
	}
	if _, ok := gate.fallback.(*v2SessionBodiesSource); !ok {
		t.Errorf("gate.fallback = %T, want *v2SessionBodiesSource", gate.fallback)
	}
	if gate.enabled == nil {
		t.Error("gate.enabled not wired")
	}
}
