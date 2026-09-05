package preprocess

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareBuildsAllLayersOnFirstRequest(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	p := mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))

	raw, san, cmp := builder.counts()
	assert.Equal(t, 1, raw, "raw built once")
	assert.Equal(t, 1, san)
	assert.Equal(t, 1, cmp)
	assert.False(t, p.Reused[ArtifactRaw])
	assert.False(t, p.Reused[ArtifactSanitized])
	assert.False(t, p.Reused[ArtifactCompressed])
	assert.Equal(t, 3, len(p.Receipts), "transform receipts from all three layers")

	// provisional revision is computed but NOT committed (UT-SA-07)
	assert.EqualValues(t, 1, p.ProvisionalRevision.TurnNo)
	assert.Equal(t, "req-1", p.ProvisionalRevision.HeadRequestID)
	assert.False(t, p.Committed)

	m, err := store.GetManifest(context.Background(), "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Revision.IsZero(), "Prepare must not advance the persistent revision")
	assert.True(t, m.Flags.Has(FlagRawReady|FlagSanitizedReady|FlagCompressedReady))
	assert.Equal(t, "default", p.CompressedVariant)

	// artifacts are loadable through the store with verified content hashes
	for _, art := range []*SessionArtifact{p.Raw, p.Sanitized, p.Compressed} {
		require.True(t, art.VerifyContentHash())
	}
}

// UT-SA-08 (second half): the second Prepare at the same revision with the
// same dependencies hits all three layers — no repeated sanitize/compress.
func TestPrepareReusesSameRevision(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))
	p2 := mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))

	raw, san, cmp := builder.counts()
	assert.Equal(t, 1, raw)
	assert.Equal(t, 1, san)
	assert.Equal(t, 1, cmp)
	assert.True(t, p2.Reused[ArtifactRaw])
	assert.True(t, p2.Reused[ArtifactSanitized])
	assert.True(t, p2.Reused[ArtifactCompressed])

	// hit events observed for all layers + bytes/tokens saved
	for _, k := range []EventKind{EventRawHit, EventSanitizeHit, EventCompressHit, EventArtifactSaved} {
		assert.GreaterOrEqual(t, sink.countOf(k), 1, "expected %s event", k)
	}
}

// UT-SA-01 (hook level) + UT-SA-04 (精确失效): a dependency change rebuilds
// only the affected layer and its downstream — Raw is untouched.
func TestPrepareRebuildsOnlyAffectedLayers(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	in := prepareInput("t", "s", "req-1", []byte("hello"))
	mustPrepare(t, h, in)

	// sanitizer version change: sanitized payload changes => sanitized and
	// (via sanitized source hash) compressed rebuild; raw reuses.
	builder.sanSuffix = "v2"
	in2 := prepareInput("t", "s", "req-1", []byte("hello"))
	in2.Deps.Sanitized.SanitizerVersion = "san-v2"
	p := mustPrepare(t, h, in2)

	raw, san, cmp := builder.counts()
	assert.Equal(t, 1, raw, "raw must not rebuild when only the sanitizer changed (UT-SA-04)")
	assert.Equal(t, 2, san, "sanitized must rebuild on sanitizer version change")
	assert.Equal(t, 2, cmp, "compressed must rebuild downstream of the changed sanitized output")
	assert.True(t, p.Reused[ArtifactRaw])
	assert.False(t, p.Reused[ArtifactSanitized])
	assert.False(t, p.Reused[ArtifactCompressed])

	// compressor change only affects the compressed layer
	in3 := prepareInput("t", "s", "req-1", []byte("hello"))
	in3.Deps.Sanitized.SanitizerVersion = "san-v2" // keep the rebuilt state
	in3.Deps.Compressed.CompressorVersion = "comp-v2"
	builder.cmpSuffix = "c2"
	p = mustPrepare(t, h, in3)
	raw, san, cmp = builder.counts()
	assert.Equal(t, 1, raw, "raw untouched by compressor change")
	assert.Equal(t, 2, san, "sanitized untouched by compressor change")
	assert.Equal(t, 3, cmp, "only compressed rebuilds on compressor change")
	assert.True(t, p.Reused[ArtifactRaw] && p.Reused[ArtifactSanitized])
}

// UT-SA-02: same-session concurrent first requests build each layer exactly
// once (lease + singleflight); waiters get the winner's result.
func TestPrepareConcurrentBuildsOnce(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	const n = 10
	var wg sync.WaitGroup
	bundles := make([]*PreparedSessionArtifacts, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bundles[i], errs[i] = h.Prepare(context.Background(), prepareInput("t", "s", "req-1", []byte("hello")))
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.NotNil(t, bundles[i])
	}
	raw, san, cmp := builder.counts()
	assert.Equal(t, 1, raw, "concurrent Prepare must build raw exactly once")
	assert.Equal(t, 1, san, "concurrent Prepare must build sanitized exactly once")
	assert.Equal(t, 1, cmp, "concurrent Prepare must build compressed exactly once")

	// exactly one build event per layer (IT-SS-04 contract)
	assert.Equal(t, 1, sink.countOf(EventRawBuild))
	assert.Equal(t, 1, sink.countOf(EventSanitizeBuild))
	assert.Equal(t, 1, sink.countOf(EventCompressBuild))
}

// UT-SA-02 (cross-process half): when another builder holds the lease, the
// waiter polls the manifest, then reuses the winner's artifact without
// building.
func TestPrepareWaitsForLeaseHolderThenReuses(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	in := prepareInput("t", "s", "req-1", []byte("hello"))
	// Compute the exact raw dep hash the hook will use.
	rawDeps := in.Deps.Raw
	rawDeps.Mutation = MutationAppendDelta
	rawDeps.PrevChainHash = SessionRevision{}.ChainHash
	rawDeps.DeltaHash = HashBytes(in.Delta)
	depHash := rawDeps.Hash()

	// hold the raw build lease as a foreign builder
	lease, ok, err := store.AcquireBuildLease(context.Background(), "t", "s", ArtifactRaw, depHash)
	require.NoError(t, err)
	require.True(t, ok)

	done := make(chan error, 1)
	go func() {
		_, err := h.Prepare(context.Background(), in)
		done <- err
	}()

	// give the waiter a moment to enter the wait loop, then publish the
	// winner's artifact and release the lease
	time.Sleep(60 * time.Millisecond)
	winner := &SessionArtifact{
		TenantID: "t", SessionID: "s", Kind: ArtifactRaw, Variant: "default",
		DependencyHash: depHash,
		Payload:        []byte("foreign-builder-raw"),
	}
	ok, err = store.PutCAS(context.Background(), winner, SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, lease.Release(context.Background()))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Prepare did not finish after lease release")
	}

	raw, _, _ := builder.counts()
	assert.Equal(t, 0, raw, "the lease waiter must not build raw itself")
	assert.GreaterOrEqual(t, sink.countOf(EventLeaseWait), 1, "lease wait must be observable")
}

// A permanently held lease surfaces as ErrBuildLeaseTimeout, not a hang.
func TestPrepareLeaseTimeout(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	h, err := NewHook(HookConfig{
		Store:             store,
		Builder:           builder,
		Events:            NoopEventSink{},
		Now:               time.Now,
		LeaseWaitTimeout:  150 * time.Millisecond,
		LeasePollInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	in := prepareInput("t", "s", "req-1", []byte("hello"))
	rawDeps := in.Deps.Raw
	rawDeps.Mutation = MutationAppendDelta
	rawDeps.PrevChainHash = SessionRevision{}.ChainHash
	rawDeps.DeltaHash = HashBytes(in.Delta)

	_, ok, err := store.AcquireBuildLease(context.Background(), "t", "s", ArtifactRaw, rawDeps.Hash())
	require.NoError(t, err)
	require.True(t, ok)

	_, err = h.Prepare(context.Background(), in)
	require.ErrorIs(t, err, ErrBuildLeaseTimeout)
}

func TestPrepareVariantMissEvent(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	in := prepareInput("t", "s", "req-1", []byte("hello"))
	in.Variant = "claude-3"
	mustPrepare(t, h, in)
	assert.Equal(t, 1, sink.countOf(EventCompressVariantMiss), "first use of a variant is a variant miss")

	// same variant again: present in the manifest now
	sink2 := &capturingSink{}
	h2 := newTestHook(t, store, builder, sink2)
	in2 := prepareInput("t", "s", "req-1", []byte("hello"))
	in2.Variant = "claude-3"
	mustPrepare(t, h2, in2)
	assert.Equal(t, 0, sink2.countOf(EventCompressVariantMiss))
}

func TestPrepareSanitizeDegradedEvent(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{sanDegrade: true}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	p := mustPrepare(t, h, prepareInput("t", "s", "req-1", []byte("hello")))
	require.NotNil(t, p.Sanitized)
	assert.NotZero(t, p.Sanitized.FailureCode, "degraded sanitize must carry an explicit failure code")
	assert.Equal(t, 1, sink.countOf(EventSanitizeDegraded))
}

func TestPrepareInvalidInput(t *testing.T) {
	store, _ := newTestStore(t, nil)
	h := newTestHook(t, store, &fakeBuilder{}, NoopEventSink{})

	_, err := h.Prepare(context.Background(), SessionPrepareInput{TenantID: "t", SessionID: "s", Delta: []byte("x")})
	assert.Error(t, err, "missing request id must fail")

	_, err = h.Prepare(context.Background(), SessionPrepareInput{TenantID: "t", SessionID: "s", RequestID: "r"})
	assert.Error(t, err, "missing delta must fail")
}

// UT-SA-14: events carry only hash prefixes and counters — no bodies, no
// sensitive mappings, no full 64-hex hashes.
func TestEventsLeakNothing(t *testing.T) {
	store, _ := newTestStore(t, nil)
	builder := &fakeBuilder{sanSuffix: "SECRET-SANITIZER-MAP-PLACEHOLDER"}
	sink := &capturingSink{}
	h := newTestHook(t, store, builder, sink)

	in := prepareInput("t", "s", "req-1", []byte("SECRET-RAW-PII-BODY"))
	mustPrepare(t, h, in)
	// second round to produce hit events too
	mustPrepare(t, h, in)

	events := sink.snapshot()
	require.NotEmpty(t, events)
	for _, e := range events {
		blob, err := json.Marshal(e)
		require.NoError(t, err)
		s := string(blob)
		assert.NotContains(t, s, "SECRET-RAW-PII-BODY", "events must not contain raw bodies")
		assert.NotContains(t, s, "SECRET-SANITIZER-MAP-PLACEHOLDER", "events must not contain sanitizer mappings")
		assert.NotContains(t, s, "raw|", "events must not contain artifact payloads")
		assert.NotContains(t, s, "san|", "events must not contain artifact payloads")
		assert.NotContains(t, s, "cmp|", "events must not contain artifact payloads")
		// no long hex strings (full 32-byte hashes would be 64 hex chars)
		for _, field := range strings.Split(s, ",") {
			for _, tok := range strings.Split(field, `"`) {
				tok = strings.Trim(tok, ` ":`)
				if len(tok) == 64 && isAllHex(tok) {
					t.Fatalf("event contains a full 64-hex hash %q — only 8-byte prefixes allowed", tok)
				}
			}
		}
	}
}

func isAllHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
