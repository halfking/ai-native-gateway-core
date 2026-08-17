package preprocess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-SA-03: appending the terminal assistant turn advances the session
// revision and marks Sanitized/Compressed stale; the next request rebuilds
// the downstream layers on demand.
func TestAppendTerminalMarksDownstreamStale(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	hk := newTestHook(t, store, builder, NoopEventSink{})
	ctx := context.Background()

	// full request lifecycle
	p := mustPrepare(t, hk, prepareInput("t", "s", "req-1", []byte("hello")))
	require.NoError(t, hk.CommitFirstForward(ctx, p, 1))

	revBefore, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)

	require.NoError(t, hk.AppendTerminal(ctx, TerminalTurn{
		TenantID:         "t",
		SessionID:        "s",
		RequestID:        "req-1",
		AssistantDelta:   []byte("hi there"),
		Status:           StatusReady,
		UpstreamResponse: BodyRefHash{Ref: "blob:up-resp", Hash: h("up"), Complete: true},
		ClientResponse:   BodyRefHash{Ref: "blob:cli-resp", Hash: h("cli"), Complete: true},
	}))

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, revBefore.Revision.TurnNo+1, m.Revision.TurnNo, "terminal append advances the revision")
	assert.Equal(t, "req-1", m.Revision.HeadRequestID)
	assert.True(t, m.Flags.Has(FlagSanitizedStale), "sanitized must be stale after the append")
	assert.True(t, m.Flags.Has(FlagCompressedStale), "compressed must be stale after the append")
	assert.False(t, m.Flags.Has(FlagSanitizedReady))
	assert.False(t, m.Flags.Has(FlagCompressedReady))

	// downstream payloads are not deleted
	_, found, err := store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	assert.True(t, found)

	// the next request rebuilds downstream layers
	rawBefore, sanBefore, cmpBefore := builder.counts()
	p2 := mustPrepare(t, hk, prepareInput("t", "s", "req-2", []byte("next question")))
	rawAfter, sanAfter, cmpAfter := builder.counts()
	assert.Equal(t, rawBefore+1, rawAfter, "raw rebuilds for the new turn (new delta => new raw dep hash)")
	assert.Equal(t, sanBefore+1, sanAfter, "sanitized rebuilds after stale")
	assert.Equal(t, cmpBefore+1, cmpAfter, "compressed rebuilds after stale")
	require.NoError(t, hk.CommitFirstForward(ctx, p2, 1))
}

func TestAppendTerminalIdempotent(t *testing.T) {
	store, _ := newTestStore(t, nil)
	hk := newTestHook(t, store, &fakeBuilder{}, NoopEventSink{})
	ctx := context.Background()

	p := mustPrepare(t, hk, prepareInput("t", "s", "req-1", []byte("hello")))
	require.NoError(t, hk.CommitFirstForward(ctx, p, 1))

	turn := TerminalTurn{TenantID: "t", SessionID: "s", RequestID: "req-1", AssistantDelta: []byte("answer")}
	require.NoError(t, hk.AppendTerminal(ctx, turn))
	require.NoError(t, hk.AppendTerminal(ctx, turn), "replaying the same terminal append must be idempotent")

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, 2, m.Revision.TurnNo, "no duplicate terminal turn")
}

func TestAppendTerminalRequiresDelta(t *testing.T) {
	store, _ := newTestStore(t, nil)
	hk := newTestHook(t, store, &fakeBuilder{}, NoopEventSink{})
	ctx := context.Background()
	assert.Error(t, hk.AppendTerminal(ctx, TerminalTurn{TenantID: "t", SessionID: "s", RequestID: "r"}), "missing assistant delta must fail")
	assert.Error(t, hk.AppendTerminal(ctx, TerminalTurn{AssistantDelta: []byte("x")}), "missing ids must fail")
	require.NoError(t, hk.AppendTerminal(ctx, TerminalTurn{TenantID: "t", SessionID: "s", RequestID: "r", AssistantDelta: []byte("answer")}))
}
