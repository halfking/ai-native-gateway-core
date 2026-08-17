package preprocess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-SA-05: the four mutation kinds each carry their own semantics; a
// snapshot payload appended as a delta is rejected.
func TestMutationSemanticsAppendDelta(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut := SessionMutation{
		TenantID: "t", SessionID: "s", RequestID: "r1",
		TurnNo: 1, Mutation: MutationAppendDelta,
		DeltaHash: h("d1"), DependencyHash: h("dep"),
	}
	rev, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rev.TurnNo, "append_delta advances the turn")
	assert.Equal(t, "r1", rev.HeadRequestID)
	assert.Equal(t, AdvanceChainHash(SessionRevision{}.ChainHash, h("d1")), rev.ChainHash)
}

func TestMutationSemanticsSnapshotAsDeltaRejected(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut := SessionMutation{
		TenantID: "t", SessionID: "s", RequestID: "r1",
		TurnNo: 1, Mutation: MutationAppendDelta,
		DeltaHash: h("snapshot"), Snapshot: true, // full snapshot pushed through the delta path
	}
	_, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSnapshotAsDelta, "snapshot appended as delta must be rejected (UT-SA-05)")

	// direct validation agrees
	assert.ErrorIs(t, mut.Validate(), ErrSnapshotAsDelta)
}

func TestMutationSemanticsReplaceSnapshot(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	// turn 1 via delta
	mut1 := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep")}
	rev1, err := store.AppendRawTurn(ctx, mut1, SessionRevision{})
	require.NoError(t, err)

	// replace at the same turn with a snapshot
	snap := SessionMutation{
		TenantID: "t", SessionID: "s", RequestID: "r2",
		TurnNo: 1, Mutation: MutationReplaceSnapshot,
		DeltaHash: h("snap"), Snapshot: true, DependencyHash: h("dep2"),
	}
	rev2, err := store.AppendRawTurn(ctx, snap, rev1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rev2.TurnNo, "replace_snapshot stays on the same turn")
	assert.Equal(t, "r2", rev2.HeadRequestID)
	assert.Equal(t, AdvanceChainHash(rev1.ChainHash, h("snap")), rev2.ChainHash)

	// replace_snapshot without the snapshot flag is invalid
	bad := snap
	bad.Snapshot = false
	_, err = store.AppendRawTurn(ctx, bad, rev2)
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestMutationSemanticsReset(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut1 := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep")}
	rev1, err := store.AppendRawTurn(ctx, mut1, SessionRevision{})
	require.NoError(t, err)
	require.EqualValues(t, 1, rev1.TurnNo)

	// reset starts a fresh chain from the zero chain hash
	reset := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r9", TurnNo: 0, Mutation: MutationReset, DeltaHash: h("reset"), DependencyHash: h("dep")}
	rev2, err := store.AppendRawTurn(ctx, reset, rev1)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rev2.TurnNo)
	assert.Equal(t, AdvanceChainHash(SessionRevision{}.ChainHash, h("reset")), rev2.ChainHash, "reset must re-anchor the chain")
}

func TestMutationSemanticsAttachmentOnly(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut1 := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep")}
	rev1, err := store.AppendRawTurn(ctx, mut1, SessionRevision{})
	require.NoError(t, err)

	att := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1-file", TurnNo: 1, Mutation: MutationAttachmentOnly, DeltaHash: h("file1")}
	rev2, err := store.AppendRawTurn(ctx, att, rev1)
	require.NoError(t, err)
	assert.EqualValues(t, rev1.TurnNo, rev2.TurnNo, "attachment_only must not create a conversational turn")
	assert.Equal(t, "r1-file", rev2.HeadRequestID)
	assert.Equal(t, AdvanceChainHash(rev1.ChainHash, h("file1")), rev2.ChainHash, "chain hash still advances with the attachment")

	// attachment_only with a snapshot payload is invalid
	bad := att
	bad.Snapshot = true
	assert.ErrorIs(t, bad.Validate(), ErrInvalidMutation)
}

func TestMutationDeltaHashMustMatchPayload(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut := SessionMutation{
		TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1,
		Mutation:  MutationAppendDelta,
		Delta:     []byte("actual bytes"),
		DeltaHash: h("declared-but-wrong"),
	}
	_, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestMutationUnknownKindRejected(t *testing.T) {
	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", Mutation: MutationKind("corrupt")}
	assert.ErrorIs(t, mut.Validate(), ErrInvalidMutation)
}

func TestAppendTurnNumberMismatchRejected(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 7, Mutation: MutationAppendDelta, DeltaHash: h("d")}
	_, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.ErrorIs(t, err, ErrInvalidMutation, "append_delta must follow the expected revision's turn")
}

// UT-SA-06 (store level): revision CAS conflict + idempotent replay of the
// same request id.
func TestAppendRawTurnConflictAndIdempotentReplay(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep1")}
	rev1, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)

	// conflicting writer moves the revision ahead first
	other := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r2", TurnNo: 2, Mutation: MutationAppendDelta, DeltaHash: h("d2"), DependencyHash: h("dep2")}
	rev2, err := store.AppendRawTurn(ctx, other, rev1)
	require.NoError(t, err)
	require.EqualValues(t, 2, rev2.TurnNo)

	// replaying r1 with its original (now stale) base must conflict, not overwrite
	_, err = store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.ErrorIs(t, err, ErrRevisionConflict)

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.Equal(t, "r2", m.Revision.HeadRequestID, "the newer revision must not be overwritten")

	// replaying r2 (the current head) is idempotent: no second turn
	revAgain, err := store.AppendRawTurn(ctx, other, rev1)
	require.NoError(t, err, "same-request replay must be idempotent")
	assert.Equal(t, rev2, revAgain, "replay returns the already-committed revision")

	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.EqualValues(t, 2, m.Revision.TurnNo, "idempotent replay must not append a second turn")
}
