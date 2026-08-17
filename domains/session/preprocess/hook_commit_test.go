package preprocess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-SA-08: only AttemptNo == 1 commits the provisional revision; retries
// reuse the same bundle and repeated commits are idempotent.
func TestCommitFirstForwardOnlyOnFirstAttempt(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)
	ctx := context.Background()

	p := mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))

	// attempt 2 (a node retry) must NOT commit
	require.NoError(t, h.CommitFirstForward(ctx, p, 2))
	assert.False(t, p.Committed)
	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Revision.IsZero(), "attempt != 1 must not advance the revision")

	// attempt 1 commits
	require.NoError(t, h.CommitFirstForward(ctx, p, 1))
	assert.True(t, p.Committed)
	assert.Equal(t, p.ProvisionalRevision, p.CommittedRevision)

	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.Equal(t, p.ProvisionalRevision, m.Revision, "commit makes the provisional revision definitive")
	assert.True(t, m.Flags.Has(FlagSanitizedStale|FlagCompressedStale), "downstream flagged stale by the append")

	// repeat commits are idempotent (no second turn)
	require.NoError(t, h.CommitFirstForward(ctx, p, 1))
	require.NoError(t, h.CommitFirstForward(ctx, p, 3))
	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, 1, m.Revision.TurnNo)
}

// Same bundle reused across retries (R11.2: 重试/换节点复用同一 Prepared
// bundle，禁止重复脱敏/压缩): a node switch re-enters CommitFirstForward with
// AttemptNo > 1 against the SAME bundle; builders ran exactly once and no
// second turn appears.
func TestRetryReusesPreparedBundle(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)
	ctx := context.Background()

	in := prepareInput("t", "s", "req-1", []byte("hello"))
	p := mustPrepare(t, h, in)
	require.NoError(t, h.CommitFirstForward(ctx, p, 1))

	// attempts 2..N on other nodes reuse the bundle directly
	for attempt := 2; attempt <= 5; attempt++ {
		require.NoError(t, h.CommitFirstForward(ctx, p, attempt))
	}
	raw, san, cmp := builder.counts()
	assert.Equal(t, 1, raw, "retries must not re-run the raw builder")
	assert.Equal(t, 1, san, "retries must not re-run the sanitizer")
	assert.Equal(t, 1, cmp, "retries must not re-run the compressor")

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, 1, m.Revision.TurnNo, "no duplicate turn for the same request")

	// a re-prepare BEFORE any commit (dedupe hit / cancel path) also hits the
	// cache: sanitize and compress do not run twice at the same revision
	p2 := mustPrepare(t, h, prepareInput("t", "s", "req-dup", []byte("hello-dup")))
	_ = p2
	raw, san, cmp = builder.counts()
	assert.Equal(t, 2, raw) // different delta => new raw artifacts (R11.5)
	assert.Equal(t, 2, san)
	assert.Equal(t, 2, cmp)
	_ = sink
}

// UT-SA-07: a request that never reaches CommitFirstForward (dedupe hit,
// pre-reject, early client cancel) leaves the session revision untouched.
func TestNoForwardDoesNotAdvanceRevision(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	h := newTestHook(t, store, builder, NoopEventSink{})
	ctx := context.Background()

	// dedupe hit path: Prepare runs but the request is dropped before forward
	_ = mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))

	// client-cancel path: Prepare runs, commit with a non-first attempt
	p := mustPrepare(t, h, prepareInput("t", "s", "req-2", []byte("hello-2")))
	require.NoError(t, h.CommitFirstForward(ctx, p, 2))

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Revision.IsZero(), "provisional revisions must never be committed without a real first forward")
	assert.Equal(t, "", m.Revision.HeadRequestID)
}

// UT-SA-06 (hook level): CAS conflict triggers reload/rebase — the newer
// revision is never overwritten; the same request id replays idempotently.
func TestCommitConflictRebasesWithoutOverwrite(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	hk := newTestHook(t, store, builder, NoopEventSink{})
	ctx := context.Background()

	// request 1 prepares against the empty revision
	p1 := mustPrepare(t, hk, prepareInput("t", "s", "req-1", []byte("hello")))

	// another request commits first (races ahead)
	other := SessionMutation{
		TenantID: "t", SessionID: "s", RequestID: "req-0", TurnNo: 1,
		Mutation: MutationAppendDelta, DeltaHash: h("other-delta"), DependencyHash: h("other-dep"),
	}
	otherRev, err := store.AppendRawTurn(ctx, other, SessionRevision{})
	require.NoError(t, err)

	// req-1's commit now conflicts: it must rebase on top, not overwrite
	require.NoError(t, hk.CommitFirstForward(ctx, p1, 1))

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, otherRev.TurnNo+1, m.Revision.TurnNo, "rebase appends after the newer revision")
	assert.Equal(t, "req-1", m.Revision.HeadRequestID)
	assert.Equal(t, AdvanceChainHash(otherRev.ChainHash, p1.DeltaHash), m.Revision.ChainHash)

	// replaying our own commit with the original (now stale) bundle is
	// idempotent: the conflict path recognizes the already-committed head
	p1.Committed = false // simulate a lost commit response / resend
	require.NoError(t, hk.CommitFirstForward(ctx, p1, 1))
	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, 2, m.Revision.TurnNo, "no second turn on idempotent replay")
}

func TestCommitNilBundleRejected(t *testing.T) {
	store, _ := newTestStore(t, nil)
	h := newTestHook(t, store, &fakeBuilder{}, NoopEventSink{})
	assert.Error(t, h.CommitFirstForward(context.Background(), nil, 1))
}
