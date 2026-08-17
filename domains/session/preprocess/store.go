package preprocess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Package-level sentinel errors.
var (
	// ErrRevisionConflict is returned by AppendRawTurn (and surfaced by
	// PutCAS as (false, nil)) when the manifest revision moved ahead of the
	// caller's expectation.  Callers must reload/rebase, never overwrite the
	// newer revision (R11.6 step 5).
	ErrRevisionConflict = errors.New("preprocess: session revision conflict, reload/rebase required")

	// ErrSnapshotAsDelta rejects appending a snapshot payload as a delta
	// (R11.6, UT-SA-05).
	ErrSnapshotAsDelta = errors.New("preprocess: snapshot payload cannot be appended as delta")

	// ErrInvalidMutation rejects structurally invalid mutations.
	ErrInvalidMutation = errors.New("preprocess: invalid session mutation")

	// ErrRawKeyRequired is returned when a raw artifact is written without a
	// configured AES-GCM key (raw must be envelope-encrypted, R11.7).
	ErrRawKeyRequired = errors.New("preprocess: raw artifact requires an AES-256-GCM key")

	// ErrArtifactNotFound is never returned by Get (it returns found=false);
	// kept for integrators that prefer sentinel-based probing of helpers.
	ErrArtifactNotFound = errors.New("preprocess: artifact not found")

	// ErrBuildLeaseTimeout is returned when waiting for another builder's
	// lease exceeded the configured wait budget.
	ErrBuildLeaseTimeout = errors.New("preprocess: timed out waiting for build lease")

	// ErrArtifactCorrupted is returned when an encrypted/tampered payload
	// fails to decrypt (AES-GCM integrity failure).
	ErrArtifactCorrupted = errors.New("preprocess: artifact payload integrity check failed")
)

// Lease is a renewable-free build lease acquired via AcquireBuildLease.
// A lease is per (tenant, session, kind, dependency hash); releasing it lets
// another builder proceed.  Leases expire server-side (SET NX PX).
type Lease interface {
	Token() string
	Kind() ArtifactKind
	Release(ctx context.Context) error
}

// SessionArtifactStore is the physical storage contract for session semantic
// artifacts (spec R11.7, signature verbatim).  Implementations must be
// tenant-scoped and must never return raw payloads through admin paths.
type SessionArtifactStore interface {
	// GetManifest returns the current manifest.  A session without any state
	// returns a non-nil empty manifest (zero revision, all layers missing),
	// not an error.
	GetManifest(ctx context.Context, tenantID, sessionID string) (*ArtifactManifest, error)

	// Get loads one artifact payload (decrypted/decompressed).  found=false
	// on miss; integrity failures return an error instead of bad payload.
	Get(ctx context.Context, tenantID, sessionID string, kind ArtifactKind, variant string) (*SessionArtifact, bool, error)

	// PutCAS stores an artifact and updates the manifest meta for its kind
	// only when the manifest revision still equals expected.  Returns false
	// (no error) on revision mismatch; expected zero revision creates the
	// manifest when absent.
	PutCAS(ctx context.Context, a *SessionArtifact, expected SessionRevision) (bool, error)

	// AppendRawTurn applies one raw-source mutation under revision CAS,
	// advances the session revision and marks downstream layers (sanitized,
	// compressed) stale without deleting their payloads.  Replaying the same
	// request (same head request id at the target turn) is idempotent: it
	// returns the current revision without appending a second turn.
	AppendRawTurn(ctx context.Context, mutation SessionMutation, expected SessionRevision) (SessionRevision, error)

	// InvalidateFrom marks kind and all downstream layers stale in the
	// manifest (flags + meta.status only; payloads are kept).
	InvalidateFrom(ctx context.Context, tenantID, sessionID string, kind ArtifactKind) error

	// AcquireBuildLease attempts to take the build lease for (session, kind,
	// dependency hash).  ok=false means another builder holds it; wait and
	// re-check the manifest instead of building.
	AcquireBuildLease(ctx context.Context, tenantID, sessionID string, kind ArtifactKind, depHash [32]byte) (Lease, bool, error)
}

// StoreOptions configures the Redis-backed store.
type StoreOptions struct {
	// Client is the Redis client (go-redis v9).  Required.
	Client redis.UniversalClient

	// AESKey is the 32-byte AES-256-GCM key for raw payload envelope
	// encryption.  Required (raw artifacts must never be stored plaintext).
	AESKey [32]byte

	// Now is the clock used for GeneratedAt timestamps; defaults to time.Now.
	Now func() time.Time

	// Per-layer TTLs; zero falls back to DefaultTTL (2h, R11.7).  Each layer
	// is independently configurable (hot-configurable at the integrator).
	RawTTL        time.Duration
	SanitizedTTL  time.Duration
	CompressedTTL time.Duration
	ManifestTTL   time.Duration

	// LeaseTTL is the build-lease expiry; defaults to 30s.
	LeaseTTL time.Duration

	// L1BudgetBytes bounds the in-process L1 payload cache; defaults to 8MiB.
	// Zero or negative disables L1.
	L1BudgetBytes int64

	// CompressSanitized/CompressCompressed enable gzip at rest for those
	// layers (R11.7: sanitized/compressed 可压缩保存).
	CompressSanitized  bool
	CompressCompressed bool

	// KeyVersion is the key namespace version; defaults to "v1".
	KeyVersion string
}

// DefaultTTL is the default artifact TTL (spec §5: 2h per layer).
const DefaultTTL = 2 * time.Hour

const (
	defaultLeaseTTL    = 30 * time.Second
	defaultL1Budget    = 8 << 20
	defaultKeyVersion  = "v1"
	defaultLeaseTokenB = 16
)

func (o *StoreOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *StoreOptions) ttlFor(kind ArtifactKind) time.Duration {
	switch kind {
	case ArtifactRaw:
		if o.RawTTL > 0 {
			return o.RawTTL
		}
	case ArtifactSanitized:
		if o.SanitizedTTL > 0 {
			return o.SanitizedTTL
		}
	case ArtifactCompressed:
		if o.CompressedTTL > 0 {
			return o.CompressedTTL
		}
	}
	if o.ManifestTTL > 0 {
		return o.ManifestTTL
	}
	return DefaultTTL
}

func (o *StoreOptions) manifestTTL() time.Duration {
	if o.ManifestTTL > 0 {
		return o.ManifestTTL
	}
	return DefaultTTL
}

func (o *StoreOptions) leaseTTL() time.Duration {
	if o.LeaseTTL > 0 {
		return o.LeaseTTL
	}
	return defaultLeaseTTL
}

func (o *StoreOptions) keyVersion() string {
	if o.KeyVersion != "" {
		return o.KeyVersion
	}
	return defaultKeyVersion
}

// hash16 returns the first 16 hex chars (8 bytes) of sha256(s), used for
// tenant-scoped key derivation (R11.7).
func hash16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// ArtifactRef returns the canonical reference string (Redis key form) for one
// artifact.  Bodies are referenced, never inlined, in manifests/blocks/events.
func ArtifactRef(tenantID, sessionID string, kind ArtifactKind, variant string) string {
	return fmt.Sprintf("session:artifact:%s:%s:%s:%s:%s",
		defaultKeyVersion, hash16(tenantID), hash16(sessionID), kind, variantOrDefault(variant))
}

func variantOrDefault(variant string) string {
	if variant == "" {
		return "default"
	}
	return variant
}
