package preprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Dependency hash inputs (spec R11.5).  All versions/config inputs are
// explicit strings; the hashes are pure functions so that config/algorithm
// changes invalidate only the affected downstream layers:
//
//	Raw        = canonicalizer version + mutation kind + previous revision
//	             chain hash + current delta hash
//	Sanitized  = raw source hash + sanitizer version + policy/config hash +
//	             placeholder schema
//	Compressed = sanitized source hash + compressor/schema version +
//	             compression mode + summary prompt/model version + protocol +
//	             context-window bucket + tools hash + prefix/tool handling
//	             version + model capability profile
type RawDependencies struct {
	CanonicalizerVersion string
	Mutation             MutationKind
	PrevChainHash        [32]byte
	DeltaHash            [32]byte
}

type SanitizedDependencies struct {
	RawSourceHash     [32]byte
	SanitizerVersion  string
	PolicyHash        string
	PlaceholderSchema string
}

type CompressedDependencies struct {
	SanitizedSourceHash    [32]byte
	CompressorVersion      string
	SchemaVersion          string
	CompressionMode        string
	SummaryPromptVersion   string
	SummaryModelVersion    string
	Protocol               string
	ContextWindowBucket    string
	ToolsHash              string
	PrefixHandlingVersion  string
	ToolHandlingVersion    string
	ModelCapabilityProfile string
}

// ArtifactDependencies bundles the three layers' dependency inputs.
type ArtifactDependencies struct {
	Raw        RawDependencies
	Sanitized  SanitizedDependencies
	Compressed CompressedDependencies
}

// DependencyHashes carries the three computed dependency hashes.
type DependencyHashes struct {
	Raw        [32]byte
	Sanitized  [32]byte
	Compressed [32]byte
}

// hashFields computes sha256(domain || 0x00 || field || 0x00 || ...) with
// domain separation so identical field values cannot collide across layers.
func hashFields(domain string, fields ...string) [32]byte {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, f := range fields {
		h.Write([]byte{0})
		h.Write([]byte(f))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Hash computes the raw-layer dependency hash (R11.5).
func (d RawDependencies) Hash() [32]byte {
	return hashFields("artifact-dep-raw-v1",
		d.CanonicalizerVersion,
		string(d.Mutation),
		hex32(d.PrevChainHash),
		hex32(d.DeltaHash),
	)
}

// Hash computes the sanitized-layer dependency hash (R11.5).
func (d SanitizedDependencies) Hash() [32]byte {
	return hashFields("artifact-dep-sanitized-v1",
		hex32(d.RawSourceHash),
		d.SanitizerVersion,
		d.PolicyHash,
		d.PlaceholderSchema,
	)
}

// Hash computes the compressed-layer dependency hash (R11.5).
func (d CompressedDependencies) Hash() [32]byte {
	return hashFields("artifact-dep-compressed-v1",
		hex32(d.SanitizedSourceHash),
		d.CompressorVersion,
		d.SchemaVersion,
		d.CompressionMode,
		d.SummaryPromptVersion,
		d.SummaryModelVersion,
		d.Protocol,
		d.ContextWindowBucket,
		d.ToolsHash,
		d.PrefixHandlingVersion,
		d.ToolHandlingVersion,
		d.ModelCapabilityProfile,
	)
}

// ComputeDependencyHashes fills the derived fields (PrevChainHash from the
// committed revision, RawSourceHash from the raw artifact source hash,
// SanitizedSourceHash from the sanitized artifact source hash) and returns the
// three dependency hashes.  The caller-supplied raw mutation defaults to
// append_delta when empty.
func ComputeDependencyHashes(deps ArtifactDependencies, committed SessionRevision, rawSource, sanitizedSource [32]byte) DependencyHashes {
	raw := deps.Raw
	if raw.Mutation == "" {
		raw.Mutation = MutationAppendDelta
	}
	raw.PrevChainHash = committed.ChainHash
	rawHash := raw.Hash()

	san := deps.Sanitized
	san.RawSourceHash = rawSource
	sanHash := san.Hash()

	comp := deps.Compressed
	comp.SanitizedSourceHash = sanitizedSource
	compHash := comp.Hash()

	return DependencyHashes{Raw: rawHash, Sanitized: sanHash, Compressed: compHash}
}

func hex32(h [32]byte) string {
	return hex.EncodeToString(h[:])
}

// String renders a short prefix form for logs (first 8 bytes only).
func (d DependencyHashes) String() string {
	return fmt.Sprintf("deps(raw=%s,san=%s,cmp=%s)", HashPrefix(d.Raw), HashPrefix(d.Sanitized), HashPrefix(d.Compressed))
}
