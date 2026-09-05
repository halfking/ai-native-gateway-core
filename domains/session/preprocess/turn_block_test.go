package preprocess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-SA-09: the six body classes are recorded by reference+hash; retries of
// the same turn append only attempt/status metadata without duplicating any
// body block.
func TestTurnBlockRetriesAppendMetadataOnly(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	hk := newTestHook(t, store, builder, NoopEventSink{})
	ctx := context.Background()

	p := mustPrepare(t, hk, prepareInput("t", "s", "req-1", []byte("hello")))

	blocks := hk.(*hook).blocks
	block, attempts, ok := blocks.Get("t", "s", "req-1")
	require.True(t, ok)
	assert.Empty(t, attempts)
	assert.Equal(t, StatusBuilding, block.Status)
	assert.NotZero(t, block.CreatedAt)
	assert.True(t, block.CompletedAt.IsZero())

	// integrator records the on-the-wire upstream request once
	upstreamReq := BodyRefHash{Ref: "blob:up-req", Hash: h("upreq"), Bytes: 100, Tokens: 30, Complete: true}
	require.NoError(t, blocks.RecordUpstreamRequest("t", "s", "req-1", upstreamReq))

	// three node retries: only attempt metadata appended
	before, _, _ := blocks.Get("t", "s", "req-1")
	for i, status := range []ArtifactStatus{StatusFailed, StatusFailed, StatusReady} {
		require.NoError(t, blocks.RecordAttempt("t", "s", "req-1", TurnAttemptRecord{
			AttemptID: "att-" + string(rune('0'+i)),
			NodeRef:   "node-a",
			Status:    status,
			ErrorKind: "timeout",
		}))
	}
	block, attempts, ok = blocks.Get("t", "s", "req-1")
	require.True(t, ok)
	require.Len(t, attempts, 3)
	assert.Equal(t, "att-0", attempts[0].AttemptID)
	assert.Equal(t, StatusFailed, attempts[0].Status)

	// none of the six body references changed
	assert.Equal(t, before.RawRequestRef, block.RawRequestRef)
	assert.Equal(t, before.SanitizedRequestRef, block.SanitizedRequestRef)
	assert.Equal(t, before.CompressedRequestRef, block.CompressedRequestRef)
	assert.Equal(t, upstreamReq, block.UpstreamRequestRef)
	assert.Equal(t, before.UpstreamResponseRef, block.UpstreamResponseRef)
	assert.Equal(t, before.ClientResponseRef, block.ClientResponseRef)

	// request-side refs carry the artifact references and content hashes
	assert.Equal(t, ArtifactRef("t", "s", ArtifactRaw, ""), block.RawRequestRef.Ref)
	assert.Equal(t, p.Raw.ContentHash, block.RawRequestRef.Hash)
	assert.Equal(t, ArtifactRef("t", "s", ArtifactCompressed, "default"), block.CompressedRequestRef.Ref)
	assert.Equal(t, p.Compressed.ContentHash, block.CompressedRequestRef.Hash)

	// terminal completes the block with upstream/client response refs
	require.NoError(t, hk.CommitFirstForward(ctx, p, 1))
	require.NoError(t, hk.AppendTerminal(ctx, TerminalTurn{
		TenantID:         "t",
		SessionID:        "s",
		RequestID:        "req-1",
		AssistantDelta:   []byte("answer"),
		UpstreamResponse: BodyRefHash{Ref: "blob:up-resp", Hash: h("uresp"), Complete: true},
		ClientResponse:   BodyRefHash{Ref: "blob:cli-resp", Hash: h("cresp"), Complete: true},
	}))

	block, _, ok = blocks.Get("t", "s", "req-1")
	require.True(t, ok)
	assert.Equal(t, StatusReady, block.Status)
	assert.False(t, block.CompletedAt.IsZero())
	assert.Equal(t, "blob:up-resp", block.UpstreamResponseRef.Ref)
	assert.Equal(t, h("uresp"), block.UpstreamResponseRef.Hash)
	assert.Equal(t, "blob:cli-resp", block.ClientResponseRef.Ref)

	// all six body classes are present and distinct refs
	assert.NotEmpty(t, block.RawRequestRef.Ref)
	assert.NotEmpty(t, block.SanitizedRequestRef.Ref)
	assert.NotEmpty(t, block.CompressedRequestRef.Ref)
	assert.NotEmpty(t, block.UpstreamRequestRef.Ref)
	assert.NotEmpty(t, block.UpstreamResponseRef.Ref)
	assert.NotEmpty(t, block.ClientResponseRef.Ref)
}

// UT-SA-09 (part 2): TransformReceipt records per-step input/output hashes
// and applied/skipped/degraded/failed statuses.
func TestTurnBlockTransformReceipts(t *testing.T) {
	log := NewTurnBlockLog(0)
	receipts := []TransformReceipt{
		{Name: "canonicalize", Version: 1, InputHash: h("in1"), OutputHash: h("out1"), Status: TransformApplied},
		{Name: "sanitize", Version: 2, InputHash: h("out1"), OutputHash: h("out2"), Status: TransformSkipped},
		{Name: "sanitize", Version: 2, InputHash: h("out1"), OutputHash: h("out2d"), Status: TransformDegraded},
		{Name: "restore", Version: 1, InputHash: h("out2"), OutputHash: h("out3"), Status: TransformFailed},
	}
	require.NoError(t, log.AppendBlock(TurnArtifactBlock{
		TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1,
		Mutation: MutationAppendDelta, Status: StatusBuilding,
		TransformChain: receipts,
	}))
	block, _, ok := log.Get("t", "s", "r1")
	require.True(t, ok)
	require.Len(t, block.TransformChain, 4)

	assert.Equal(t, "applied", TransformStatusString(block.TransformChain[0].Status))
	assert.Equal(t, "skipped", TransformStatusString(block.TransformChain[1].Status))
	assert.Equal(t, "degraded", TransformStatusString(block.TransformChain[2].Status))
	assert.Equal(t, "failed", TransformStatusString(block.TransformChain[3].Status))
	// chained hashes line up
	assert.Equal(t, receipts[0].OutputHash, receipts[1].InputHash)

	// mutating the returned copy must not touch the stored chain
	block.TransformChain[0].Name = "tampered"
	again, _, _ := log.Get("t", "s", "r1")
	assert.Equal(t, "canonicalize", again.TransformChain[0].Name)
}

func TestTurnBlockLogBoundedAndOrdered(t *testing.T) {
	log := NewTurnBlockLog(3)
	for i := 0; i < 5; i++ {
		require.NoError(t, log.AppendBlock(TurnArtifactBlock{
			TenantID: "t", SessionID: "s", RequestID: "r" + string(rune('0'+i)),
			TurnNo: int32(i), Mutation: MutationAppendDelta, Status: StatusReady,
		}))
	}
	assert.Equal(t, 3, log.Len(), "capacity bounded FIFO")

	blocks := log.Blocks("t", "s")
	require.Len(t, blocks, 3)
	assert.Equal(t, "r2", blocks[0].RequestID, "oldest evicted, ordered by turn")
	assert.Equal(t, "r4", blocks[2].RequestID)

	// other sessions invisible
	assert.Empty(t, log.Blocks("t", "other"))
}

func TestTurnBlockAppendIdempotentPerRequest(t *testing.T) {
	log := NewTurnBlockLog(0)
	b := TurnArtifactBlock{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta}
	require.NoError(t, log.AppendBlock(b))
	require.NoError(t, log.AppendBlock(b), "re-preparing the same request must not duplicate the block")
	assert.Equal(t, 1, log.Len())
}

func TestTurnBlockValidation(t *testing.T) {
	log := NewTurnBlockLog(0)
	assert.Error(t, log.AppendBlock(TurnArtifactBlock{TenantID: "t", SessionID: "s", Mutation: MutationAppendDelta}), "request id required")
	assert.Error(t, log.AppendBlock(TurnArtifactBlock{TenantID: "t", SessionID: "s", RequestID: "r", Mutation: MutationKind("bad")}), "mutation kind must be valid")
	assert.Error(t, log.RecordAttempt("t", "s", "missing", TurnAttemptRecord{AttemptID: "a"}), "attempt on unknown block fails")
}
