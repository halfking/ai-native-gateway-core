package preprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func h(s string) [32]byte { return HashBytes([]byte(s)) }

// R11.5: raw dependency hash covers canonicalizer version + mutation kind +
// previous revision chain hash + current delta hash — nothing else.
func TestRawDependencyHashInputs(t *testing.T) {
	base := RawDependencies{
		CanonicalizerVersion: "canon-v1",
		Mutation:             MutationAppendDelta,
		PrevChainHash:        h("chain"),
		DeltaHash:            h("delta"),
	}
	want := base.Hash()

	// deterministic
	assert.Equal(t, want, base.Hash())

	// every input matters
	changed := base
	changed.CanonicalizerVersion = "canon-v2"
	assert.NotEqual(t, want, changed.Hash(), "canonicalizer version must change raw dep hash")

	changed = base
	changed.Mutation = MutationReplaceSnapshot
	assert.NotEqual(t, want, changed.Hash(), "mutation kind must change raw dep hash")

	changed = base
	changed.PrevChainHash = h("chain2")
	assert.NotEqual(t, want, changed.Hash(), "previous chain hash must change raw dep hash")

	changed = base
	changed.DeltaHash = h("delta2")
	assert.NotEqual(t, want, changed.Hash(), "delta hash must change raw dep hash")
}

func TestSanitizedDependencyHashInputs(t *testing.T) {
	base := SanitizedDependencies{
		RawSourceHash:     h("raw"),
		SanitizerVersion:  "san-v1",
		PolicyHash:        "policy-1",
		PlaceholderSchema: "ph-v1",
	}
	want := base.Hash()
	assert.Equal(t, want, base.Hash())

	for _, mutate := range []func(*SanitizedDependencies){
		func(d *SanitizedDependencies) { d.RawSourceHash = h("raw2") },
		func(d *SanitizedDependencies) { d.SanitizerVersion = "san-v2" },
		func(d *SanitizedDependencies) { d.PolicyHash = "policy-2" },
		func(d *SanitizedDependencies) { d.PlaceholderSchema = "ph-v2" },
	} {
		changed := base
		mutate(&changed)
		assert.NotEqual(t, want, changed.Hash())
	}
}

// R11.5: compressed dependency hash covers all 12 inputs.
func TestCompressedDependencyHashInputs(t *testing.T) {
	base := CompressedDependencies{
		SanitizedSourceHash:    h("san"),
		CompressorVersion:      "comp-v1",
		SchemaVersion:          "schema-v1",
		CompressionMode:        "mode-a",
		SummaryPromptVersion:   "sp-v1",
		SummaryModelVersion:    "sm-v1",
		Protocol:               "openai",
		ContextWindowBucket:    "128k",
		ToolsHash:              "tools-1",
		PrefixHandlingVersion:  "prefix-v1",
		ToolHandlingVersion:    "tool-v1",
		ModelCapabilityProfile: "cap-a",
	}
	want := base.Hash()
	assert.Equal(t, want, base.Hash())

	mutations := []func(*CompressedDependencies){
		func(d *CompressedDependencies) { d.SanitizedSourceHash = h("san2") },
		func(d *CompressedDependencies) { d.CompressorVersion = "comp-v2" },
		func(d *CompressedDependencies) { d.SchemaVersion = "schema-v2" },
		func(d *CompressedDependencies) { d.CompressionMode = "mode-b" },
		func(d *CompressedDependencies) { d.SummaryPromptVersion = "sp-v2" },
		func(d *CompressedDependencies) { d.SummaryModelVersion = "sm-v2" },
		func(d *CompressedDependencies) { d.Protocol = "anthropic" },
		func(d *CompressedDependencies) { d.ContextWindowBucket = "200k" },
		func(d *CompressedDependencies) { d.ToolsHash = "tools-2" },
		func(d *CompressedDependencies) { d.PrefixHandlingVersion = "prefix-v2" },
		func(d *CompressedDependencies) { d.ToolHandlingVersion = "tool-v2" },
		func(d *CompressedDependencies) { d.ModelCapabilityProfile = "cap-b" },
	}
	for i, mutate := range mutations {
		changed := base
		mutate(&changed)
		assert.NotEqual(t, want, changed.Hash(), "compressed input #%d must change the hash", i)
	}
}

// Hash domain separation: identical field values across layers must not
// collide.
func TestDependencyHashDomainSeparation(t *testing.T) {
	raw := RawDependencies{CanonicalizerVersion: "a", Mutation: MutationAppendDelta, PrevChainHash: h("x"), DeltaHash: h("x")}
	san := SanitizedDependencies{RawSourceHash: h("x"), SanitizerVersion: "a", PolicyHash: "a", PlaceholderSchema: "a"}
	assert.NotEqual(t, raw.Hash(), san.Hash())
}

func TestComputeDependencyHashesFillsDerivedFields(t *testing.T) {
	deps := defaultDeps()
	committed := SessionRevision{TurnNo: 2, HeadRequestID: "req-2", ChainHash: h("chain-2")}
	rawSource := h("raw-src")
	sanSource := h("san-src")

	got := ComputeDependencyHashes(deps, committed, rawSource, sanSource)

	// Recompute manually: the derived fields must be filled in exactly.
	wantRaw := RawDependencies{CanonicalizerVersion: deps.Raw.CanonicalizerVersion, Mutation: MutationAppendDelta, PrevChainHash: committed.ChainHash, DeltaHash: deps.Raw.DeltaHash}
	assert.Equal(t, wantRaw.Hash(), got.Raw)

	wantSan := SanitizedDependencies{RawSourceHash: rawSource, SanitizerVersion: deps.Sanitized.SanitizerVersion, PolicyHash: deps.Sanitized.PolicyHash, PlaceholderSchema: deps.Sanitized.PlaceholderSchema}
	assert.Equal(t, wantSan.Hash(), got.Sanitized)

	wantCmp := deps.Compressed
	wantCmp.SanitizedSourceHash = sanSource
	assert.Equal(t, wantCmp.Hash(), got.Compressed)
}

func TestAdvanceChainHash(t *testing.T) {
	prev := h("chain")
	next := AdvanceChainHash(prev, h("delta"))
	assert.Equal(t, next, AdvanceChainHash(prev, h("delta")), "chain advance must be deterministic")
	assert.NotEqual(t, next, AdvanceChainHash(prev, h("delta2")))
	assert.NotEqual(t, next, prev)
	// zero previous is valid (first turn)
	assert.NotEqual(t, AdvanceChainHash(SessionRevision{}.ChainHash, h("d")), AdvanceChainHash(h("other"), h("d")))
}

func TestManifestMetaForVariants(t *testing.T) {
	m := NewArtifactManifest("t", "s")
	meta := ArtifactMeta{Status: StatusReady, DependencyHash: h("d")}
	m.SetMeta(ArtifactCompressed, "gpt4o", meta)
	m.SetMeta(ArtifactCompressed, "claude", ArtifactMeta{Status: StatusStale})

	assert.Equal(t, meta, m.MetaFor(ArtifactCompressed, "gpt4o"))
	assert.Equal(t, StatusStale, m.MetaFor(ArtifactCompressed, "claude").Status)
	assert.Equal(t, StatusMissing, m.MetaFor(ArtifactCompressed, "unknown").Status)
	assert.Equal(t, ArtifactMeta{}, m.MetaFor(ArtifactRaw, "ignored-variant"))
	assert.Equal(t, 2, len(m.CompressedVariants))
}

func TestHashPrefixTruncation(t *testing.T) {
	full := h("anything")
	p := HashPrefix(full)
	assert.Len(t, p, 16, "prefix must be first 8 bytes as 16 hex chars")
	// prefix must match the start of the full hex form
	assert.Equal(t, p, hexOf(full)[:16])
}

func hexOf(h [32]byte) string {
	return hex32(h)
}
