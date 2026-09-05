// Package preprocess is the single owner of the three-level session semantic
// artifacts (raw -> sanitized -> compressed) described by spec FR-11
// (会话优化 v4, R11.1–R11.9).  This file carries the contract types copied
// field-by-field from the spec; do not deviate from the spec shapes.
package preprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// ArtifactKind identifies one semantic layer of the session artifacts.
// The layers form a fixed dependency graph Raw -> Sanitized -> Compressed
// (R11.1); each layer may physically live in L1 (in-process) / L2 (Redis) /
// L3 (PostgreSQL) replicas.
type ArtifactKind string

const (
	// ArtifactRaw is the canonicalized original session/turn delta before
	// sanitization.  It contains sensitive data and MUST be stored encrypted.
	ArtifactRaw ArtifactKind = "raw"
	// ArtifactSanitized is the placeholder-sanitized session derived from Raw.
	ArtifactSanitized ArtifactKind = "sanitized"
	// ArtifactCompressed is the LLM-ready compressed/summarized representation
	// derived from Sanitized, versioned per variant (model/protocol/...).
	ArtifactCompressed ArtifactKind = "compressed"
)

// DownstreamOf returns the layers that depend on kind (exclusive), i.e. the
// layers that must be invalidated when kind changes: raw -> {sanitized,
// compressed}, sanitized -> {compressed}, compressed -> {}.
func (k ArtifactKind) DownstreamOf() []ArtifactKind {
	switch k {
	case ArtifactRaw:
		return []ArtifactKind{ArtifactSanitized, ArtifactCompressed}
	case ArtifactSanitized:
		return []ArtifactKind{ArtifactCompressed}
	default:
		return nil
	}
}

func (k ArtifactKind) Valid() bool {
	return k == ArtifactRaw || k == ArtifactSanitized || k == ArtifactCompressed
}

// ArtifactStatus is the explicit per-artifact lifecycle status (R11.4):
// missing/building/ready/stale/failed/degraded/superseded.
type ArtifactStatus uint8

const (
	StatusMissing ArtifactStatus = iota
	StatusBuilding
	StatusReady
	StatusStale
	StatusFailed
	StatusDegraded
	StatusSuperseded
)

func (s ArtifactStatus) String() string {
	switch s {
	case StatusMissing:
		return "missing"
	case StatusBuilding:
		return "building"
	case StatusReady:
		return "ready"
	case StatusStale:
		return "stale"
	case StatusFailed:
		return "failed"
	case StatusDegraded:
		return "degraded"
	case StatusSuperseded:
		return "superseded"
	default:
		return fmt.Sprintf("artifact_status(%d)", uint8(s))
	}
}

// MutationKind enumerates the turn-level mutation semantics of the raw source
// log (R11.6): append_delta / replace_snapshot / reset / attachment_only.
// A snapshot payload must never be blindly appended as a delta.
type MutationKind string

const (
	MutationAppendDelta     MutationKind = "append_delta"
	MutationReplaceSnapshot MutationKind = "replace_snapshot"
	MutationReset           MutationKind = "reset"
	MutationAttachmentOnly  MutationKind = "attachment_only"
)

func (m MutationKind) Valid() bool {
	switch m {
	case MutationAppendDelta, MutationReplaceSnapshot, MutationReset, MutationAttachmentOnly:
		return true
	default:
		return false
	}
}

// ArtifactFlags are the fast-path bits from spec R11.4.  Flags are a fast path
// only: reuse additionally requires source revision, dependency hash and
// content hash checks (see ReuseCheck).
type ArtifactFlags uint32

const (
	FlagRawReady ArtifactFlags = 1 << iota
	FlagSanitizedReady
	FlagCompressedReady
	FlagRawBuilding
	FlagSanitizedBuilding
	FlagCompressedBuilding
	FlagRawStale
	FlagSanitizedStale
	FlagCompressedStale
	FlagSanitizeDegraded
	FlagBuildLeaseHeld
)

// ReadyFlag returns the ready bit of the given kind.
func ReadyFlag(kind ArtifactKind) ArtifactFlags {
	switch kind {
	case ArtifactRaw:
		return FlagRawReady
	case ArtifactSanitized:
		return FlagSanitizedReady
	case ArtifactCompressed:
		return FlagCompressedReady
	default:
		return 0
	}
}

// StaleFlag returns the stale bit of the given kind.
func StaleFlag(kind ArtifactKind) ArtifactFlags {
	switch kind {
	case ArtifactRaw:
		return FlagRawStale
	case ArtifactSanitized:
		return FlagSanitizedStale
	case ArtifactCompressed:
		return FlagCompressedStale
	default:
		return 0
	}
}

// BuildingFlag returns the building bit of the given kind.
func BuildingFlag(kind ArtifactKind) ArtifactFlags {
	switch kind {
	case ArtifactRaw:
		return FlagRawBuilding
	case ArtifactSanitized:
		return FlagSanitizedBuilding
	case ArtifactCompressed:
		return FlagCompressedBuilding
	default:
		return 0
	}
}

func (f ArtifactFlags) Has(b ArtifactFlags) bool { return f&b == b }

// SessionRevision identifies one committed point of the session source log.
type SessionRevision struct {
	TurnNo        int32
	HeadRequestID string
	ChainHash     [32]byte
}

func (r SessionRevision) IsZero() bool {
	return r.TurnNo == 0 && r.HeadRequestID == "" && r.ChainHash == SessionRevision{}.ChainHash
}

func (r SessionRevision) Equal(other SessionRevision) bool {
	return r.TurnNo == other.TurnNo && r.HeadRequestID == other.HeadRequestID && r.ChainHash == other.ChainHash
}

func (r SessionRevision) String() string {
	return fmt.Sprintf("rev(turn=%d,head=%s,chain=%s)", r.TurnNo, r.HeadRequestID, HashPrefix(r.ChainHash))
}

// AdvanceChainHash derives the next chain hash: sha256(domain || prev || delta).
// It is the single chain-derivation rule used by the hook and the store.
func AdvanceChainHash(prev [32]byte, deltaHash [32]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("session-artifact-chain-v1"))
	h.Write([]byte{0})
	h.Write(prev[:])
	h.Write([]byte{0})
	h.Write(deltaHash[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ArtifactMeta is the per-layer reuse metadata (spec R11.4, verbatim).
type ArtifactMeta struct {
	Status         ArtifactStatus
	SourceHash     [32]byte
	DependencyHash [32]byte
	ContentHash    [32]byte
	SchemaVersion  uint16
	TransformVer   uint16
	GeneratedAt    int64
	FailureCode    uint16
}

// ArtifactManifest is the per-session verifiable manifest (spec R11.4,
// verbatim).  It stores only the chain head (current revision) plus per-layer
// ready/stale state; full sessions are rebuilt from TurnArtifactBlock chains.
type ArtifactManifest struct {
	TenantID, SessionID string
	Revision            SessionRevision
	Flags               ArtifactFlags
	Raw, Sanitized      ArtifactMeta
	CompressedVariants  map[string]ArtifactMeta
}

// NewArtifactManifest returns an empty manifest (zero revision, all missing).
func NewArtifactManifest(tenantID, sessionID string) *ArtifactManifest {
	return &ArtifactManifest{
		TenantID:           tenantID,
		SessionID:          sessionID,
		CompressedVariants: map[string]ArtifactMeta{},
	}
}

func (m *ArtifactManifest) ensureVariants() {
	if m.CompressedVariants == nil {
		m.CompressedVariants = map[string]ArtifactMeta{}
	}
}

// MetaFor returns the metadata of the given layer; variant is only used for
// the compressed kind (pass "" elsewhere).
func (m *ArtifactManifest) MetaFor(kind ArtifactKind, variant string) ArtifactMeta {
	switch kind {
	case ArtifactRaw:
		return m.Raw
	case ArtifactSanitized:
		return m.Sanitized
	case ArtifactCompressed:
		m.ensureVariants()
		return m.CompressedVariants[variant]
	default:
		return ArtifactMeta{}
	}
}

// SetMeta overwrites the metadata of the given layer.
func (m *ArtifactManifest) SetMeta(kind ArtifactKind, variant string, meta ArtifactMeta) {
	switch kind {
	case ArtifactRaw:
		m.Raw = meta
	case ArtifactSanitized:
		m.Sanitized = meta
	case ArtifactCompressed:
		m.ensureVariants()
		m.CompressedVariants[variant] = meta
	}
}

// SetReady marks the kind ready (clears stale/building bits for that kind).
func (m *ArtifactManifest) SetReady(kind ArtifactKind) {
	m.Flags &^= StaleFlag(kind) | BuildingFlag(kind)
	m.Flags |= ReadyFlag(kind)
}

// MarkStale marks the kind stale (clears ready/building bits for that kind).
func (m *ArtifactManifest) MarkStale(kind ArtifactKind) {
	m.Flags &^= ReadyFlag(kind) | BuildingFlag(kind)
	m.Flags |= StaleFlag(kind)
}

// MarkBuilding marks the kind building (keeps stale semantics aside).
func (m *ArtifactManifest) MarkBuilding(kind ArtifactKind) {
	m.Flags &^= ReadyFlag(kind)
	m.Flags |= BuildingFlag(kind)
}

// BodyRefHash is a reference+hash pair for one of the six body classes of a
// turn (spec R11.3, verbatim).  Bodies are stored by reference, never copied
// into events.
type BodyRefHash struct {
	Ref      string // Redis/PG/encrypted blob reference; 不在事件中放正文
	Hash     [32]byte
	Bytes    int32
	Tokens   int32
	Complete bool
}

// TransformReceipt records one transformation step (spec R11.3, verbatim).
type TransformReceipt struct {
	Name       string // canonicalize/sanitize/compress/paramguard/disguise/restore
	Version    uint16
	InputHash  [32]byte
	OutputHash [32]byte
	Status     uint8 // applied/skipped/degraded/failed
}

// Transform step statuses (TransformReceipt.Status).
const (
	TransformApplied  uint8 = iota // 0
	TransformSkipped               // 1
	TransformDegraded              // 2
	TransformFailed                // 3
)

func TransformStatusString(s uint8) string {
	switch s {
	case TransformApplied:
		return "applied"
	case TransformSkipped:
		return "skipped"
	case TransformDegraded:
		return "degraded"
	case TransformFailed:
		return "failed"
	default:
		return fmt.Sprintf("transform_status(%d)", s)
	}
}

// TurnArtifactBlock is the immutable per-turn block (spec R11.3, verbatim:
// fields copied one-by-one, including the paired BodyRefHash field names).
type TurnArtifactBlock struct {
	TenantID, SessionID string
	TurnNo              int32
	RequestID           string
	AttemptID           string // 首次/最终 attempt 关联，不在每次重试复制正文
	SourceRevision      SessionRevision
	Mutation            MutationKind // append_delta/replace_snapshot/reset/attachment_only

	RawRequestRef, RawRequestHash             BodyRefHash
	SanitizedRequestRef, SanitizedRequestHash BodyRefHash
	CompressedRequestRef, CompressedHash      BodyRefHash
	UpstreamRequestRef, UpstreamRequestHash   BodyRefHash
	UpstreamResponseRef, UpstreamResponseHash BodyRefHash
	ClientResponseRef, ClientResponseHash     BodyRefHash

	TransformChain         []TransformReceipt // 顺序、版本、输入/输出 hash、状态
	Status                 ArtifactStatus
	CreatedAt, CompletedAt time.Time
}

// SessionArtifact is the stored payload unit exchanged through
// SessionArtifactStore.  Payload is always plaintext in memory; the store
// applies envelope encryption (raw) / optional gzip (sanitized, compressed)
// at rest.
type SessionArtifact struct {
	TenantID       string
	SessionID      string
	Kind           ArtifactKind
	Variant        string
	Revision       SessionRevision // revision at which the artifact was generated
	SourceHash     [32]byte
	DependencyHash [32]byte
	ContentHash    [32]byte
	SchemaVersion  uint16
	TransformVer   uint16
	GeneratedAt    int64
	FailureCode    uint16
	Tokens         int32
	Payload        []byte
}

// VerifyContentHash reports whether sha256(Payload) == ContentHash.
func (a *SessionArtifact) VerifyContentHash() bool {
	if a == nil || a.Payload == nil {
		return false
	}
	return HashBytes(a.Payload) == a.ContentHash
}

// Clone returns a deep copy of the artifact (payload included).
func (a *SessionArtifact) Clone() *SessionArtifact {
	if a == nil {
		return nil
	}
	c := *a
	c.Payload = cloneBytes(a.Payload)
	return &c
}

// SessionMutation describes one turn-level mutation of the raw source log
// (R11.6).  Snapshot marks payloads that are full snapshots rather than
// deltas; appending a snapshot as a delta is rejected (UT-SA-05).
type SessionMutation struct {
	TenantID, SessionID string
	RequestID           string
	TurnNo              int32
	Mutation            MutationKind
	DeltaHash           [32]byte
	Delta               []byte   // optional payload; stored encrypted for raw
	Snapshot            bool     // payload is a full snapshot, not a delta
	DependencyHash      [32]byte // raw-layer dependency hash recorded in manifest meta
	SchemaVersion       uint16
}

// Validate enforces the mutation semantics of R11.6.
func (m SessionMutation) Validate() error {
	if m.TenantID == "" || m.SessionID == "" {
		return fmt.Errorf("%w: tenant/session required", ErrInvalidMutation)
	}
	if !m.Mutation.Valid() {
		return fmt.Errorf("%w: unknown mutation kind %q", ErrInvalidMutation, m.Mutation)
	}
	switch m.Mutation {
	case MutationAppendDelta:
		if m.Snapshot {
			return ErrSnapshotAsDelta
		}
	case MutationReplaceSnapshot:
		if !m.Snapshot {
			return fmt.Errorf("%w: replace_snapshot must carry a snapshot payload", ErrInvalidMutation)
		}
	case MutationAttachmentOnly:
		if m.Snapshot {
			return fmt.Errorf("%w: attachment_only cannot be a snapshot", ErrInvalidMutation)
		}
	case MutationReset:
		// reset may carry either a fresh snapshot or nothing
	}
	return nil
}

// HashBytes returns sha256(b).
func HashBytes(b []byte) [32]byte {
	return sha256.Sum256(b)
}

// HashPrefix returns the first 8 bytes of h as 16 hex chars.  This is the only
// hash form allowed in events (R11.9: no full hashes, no bodies, no maps).
func HashPrefix(h [32]byte) string {
	return hex.EncodeToString(h[:8])
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
