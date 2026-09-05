package preprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-SA-01 (part 1): the fast flags exist exactly at the spec R11.4 bit
// positions and are only a fast path, never sufficient for reuse.
func TestArtifactFlagBitPositions(t *testing.T) {
	assert.EqualValues(t, 1<<0, FlagRawReady, "FlagRawReady must be bit0")
	assert.EqualValues(t, 1<<1, FlagSanitizedReady)
	assert.EqualValues(t, 1<<2, FlagCompressedReady)
	assert.EqualValues(t, 1<<3, FlagRawBuilding)
	assert.EqualValues(t, 1<<4, FlagSanitizedBuilding)
	assert.EqualValues(t, 1<<5, FlagCompressedBuilding)
	assert.EqualValues(t, 1<<6, FlagRawStale)
	assert.EqualValues(t, 1<<7, FlagSanitizedStale)
	assert.EqualValues(t, 1<<8, FlagCompressedStale)
	assert.EqualValues(t, 1<<9, FlagSanitizeDegraded)
	assert.EqualValues(t, 1<<10, FlagBuildLeaseHeld)

	// ready/stale/building helpers agree with the bits
	assert.Equal(t, FlagRawReady, ReadyFlag(ArtifactRaw))
	assert.Equal(t, FlagSanitizedStale, StaleFlag(ArtifactSanitized))
	assert.Equal(t, FlagCompressedBuilding, BuildingFlag(ArtifactCompressed))
}

func TestArtifactStatusStrings(t *testing.T) {
	assert.Equal(t, "missing", StatusMissing.String())
	assert.Equal(t, "building", StatusBuilding.String())
	assert.Equal(t, "ready", StatusReady.String())
	assert.Equal(t, "stale", StatusStale.String())
	assert.Equal(t, "failed", StatusFailed.String())
	assert.Equal(t, "degraded", StatusDegraded.String())
	assert.Equal(t, "superseded", StatusSuperseded.String())
}

func TestMutationKindStrings(t *testing.T) {
	assert.Equal(t, "append_delta", string(MutationAppendDelta))
	assert.Equal(t, "replace_snapshot", string(MutationReplaceSnapshot))
	assert.Equal(t, "reset", string(MutationReset))
	assert.Equal(t, "attachment_only", string(MutationAttachmentOnly))
}

// UT-SA-01: 复用四条件 — ready + source revision + dependency hash + content
// hash must ALL hold; each counterexample must force a rebuild.
func TestReuseCheckFourConditions(t *testing.T) {
	payload := []byte("session payload")
	contentHash := HashBytes(payload)
	depHash := HashBytes([]byte("dep"))
	rev := SessionRevision{TurnNo: 3, HeadRequestID: "req-1", ChainHash: HashBytes([]byte("chain"))}

	newReady := func() *ArtifactManifest {
		m := NewArtifactManifest("t1", "s1")
		m.Revision = rev
		m.Raw = ArtifactMeta{Status: StatusReady, DependencyHash: depHash, ContentHash: contentHash}
		m.SetReady(ArtifactRaw)
		return m
	}

	t.Run("all conditions hold", func(t *testing.T) {
		reason, ok := ReuseCheck{Manifest: newReady(), Kind: ArtifactRaw, Meta: newReady().Raw, CurrentRev: rev, DepHash: depHash, Payload: payload}.Evaluate()
		require.True(t, ok, "expected reuse, got reason %q", reason)
		assert.Equal(t, ReuseOK, reason)
	})

	t.Run("ready bit clear", func(t *testing.T) {
		m := newReady()
		m.Flags &^= FlagRawReady
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: depHash}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseNotReady, reason)
	})

	t.Run("stale bit set", func(t *testing.T) {
		m := newReady()
		m.Flags |= FlagRawStale
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: depHash}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseStale, reason)
	})

	t.Run("meta status not ready", func(t *testing.T) {
		m := newReady()
		m.Raw.Status = StatusStale // flag bits still ready: flags alone are not authoritative
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: depHash}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseStatusNotReady, reason)
	})

	t.Run("source revision moved", func(t *testing.T) {
		m := newReady()
		m.Revision.TurnNo = 4 // another request committed while we were looking
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: depHash}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseRevisionMoved, reason)
	})

	t.Run("dependency hash mismatch", func(t *testing.T) {
		m := newReady()
		other := HashBytes([]byte("dep-v2"))
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: other}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseDepHash, reason)
	})

	t.Run("content hash mismatch", func(t *testing.T) {
		m := newReady()
		reason, ok := ReuseCheck{Manifest: m, Kind: ArtifactRaw, Meta: m.Raw, CurrentRev: rev, DepHash: depHash, Payload: []byte("tampered")}.Evaluate()
		assert.False(t, ok)
		assert.Equal(t, ReuseContentHash, reason)
	})

	t.Run("nil manifest never reusable", func(t *testing.T) {
		_, ok := ReuseCheck{Kind: ArtifactRaw, CurrentRev: rev, DepHash: depHash}.Evaluate()
		assert.False(t, ok)
	})
}

func TestSessionRevisionHelpers(t *testing.T) {
	zero := SessionRevision{}
	assert.True(t, zero.IsZero())
	r := SessionRevision{TurnNo: 1, HeadRequestID: "r", ChainHash: HashBytes([]byte("x"))}
	assert.False(t, r.Equal(zero))
	assert.True(t, r.Equal(SessionRevision{TurnNo: 1, HeadRequestID: "r", ChainHash: r.ChainHash}))
}

func TestManifestFlagTransitions(t *testing.T) {
	m := NewArtifactManifest("t", "s")
	m.SetReady(ArtifactRaw)
	assert.True(t, m.Flags.Has(FlagRawReady))
	m.MarkStale(ArtifactRaw)
	assert.False(t, m.Flags.Has(FlagRawReady))
	assert.True(t, m.Flags.Has(FlagRawStale))
	m.MarkBuilding(ArtifactSanitized)
	assert.True(t, m.Flags.Has(FlagSanitizedBuilding))
	assert.False(t, m.Flags.Has(FlagSanitizedReady))
}
