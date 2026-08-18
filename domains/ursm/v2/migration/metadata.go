// Package migration implements the L3 k2 migration machinery described in
// docs 14/15/16 (slice 11-13). It owns preflight classification, copy with
// PTTL preservation, and exact-ledger cleanup. The package is self-contained:
// the only URSM symbols it imports are those explicitly handed over in
// 14 §0 (store.NodeKeyCanonical/WindowKeyCanonical/CandidateIndexKeyCanonical
// and the corresponding Parse*Canonical helpers plus Schema/SchemaK2/SchemaLegacy).
package migration

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Mode describes the k2 schema mode for the Redis key layout (doc 14 §3).
// It is orthogonal to api.RolloutMode (URSM v2 vs legacy routing authority).
type Mode string

const (
	ModeLegacy    Mode = "legacy"
	ModeDual      Mode = "dual"
	ModeCanonical Mode = "canonical"
)

// Valid returns true iff Mode is one of the frozen values above.
func (m Mode) Valid() bool {
	switch m {
	case ModeLegacy, ModeDual, ModeCanonical:
		return true
	}
	return false
}

// Checkpoint values for the metadata state machine (doc 14 §4 / 15 §4).
type Checkpoint string

const (
	CheckpointPreflight Checkpoint = "preflight"
	CheckpointCopy      Checkpoint = "copy"
	CheckpointCoverage  Checkpoint = "coverage"
	CheckpointDual      Checkpoint = "dual"
	CheckpointObserve   Checkpoint = "observe"
	CheckpointCleanup   Checkpoint = "cleanup"
	CheckpointDone      Checkpoint = "done"
	CheckpointRollback  Checkpoint = "rollback"
)

// Valid returns true iff Checkpoint is one of the frozen values above.
func (c Checkpoint) Valid() bool {
	switch c {
	case CheckpointPreflight, CheckpointCopy, CheckpointCoverage,
		CheckpointDual, CheckpointObserve, CheckpointCleanup,
		CheckpointDone, CheckpointRollback:
		return true
	}
	return false
}

// Metadata mirrors doc 14 §3 exactly. The JSON form is the canonical
// representation on disk and in the Redis HASH (one field per row).
type Metadata struct {
	Owner             string     `json:"owner"`
	LedgerID          string     `json:"ledger_id"`
	Mode              Mode       `json:"mode"`
	CutoverEpoch      int64      `json:"cutover_epoch"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	PreflightChecksum string     `json:"preflight_checksum,omitempty"`
	RollbackDeadline  time.Time  `json:"rollback_deadline,omitempty"`
	Checkpoint        Checkpoint `json:"checkpoint"`
}

// Validate enforces doc 14 §0/§3 invariants. Owner/ledger_id non-empty, mode
// and checkpoint are valid, cutover_epoch is non-negative, timestamps are
// non-zero. Used at every entry point to fail-closed on misconfigured runs.
func (m Metadata) Validate() error {
	if m.Owner == "" {
		return fmt.Errorf("migration: owner is required")
	}
	if m.LedgerID == "" {
		return fmt.Errorf("migration: ledger_id is required")
	}
	if !m.Mode.Valid() {
		return fmt.Errorf("migration: invalid mode %q", m.Mode)
	}
	if !m.Checkpoint.Valid() {
		return fmt.Errorf("migration: invalid checkpoint %q", m.Checkpoint)
	}
	if m.CutoverEpoch < 0 {
		return fmt.Errorf("migration: cutover_epoch must be non-negative")
	}
	if m.StartedAt.IsZero() {
		return fmt.Errorf("migration: started_at is required")
	}
	if m.UpdatedAt.IsZero() {
		return fmt.Errorf("migration: updated_at is required")
	}
	return nil
}

// MarshalJSON keeps the time format stable (RFC3339Nano) regardless of the
// caller's locale so checksums and Redis HASH fields stay deterministic.
func (m Metadata) MarshalJSON() ([]byte, error) {
	type wire struct {
		Owner             string `json:"owner"`
		LedgerID          string `json:"ledger_id"`
		Mode              Mode   `json:"mode"`
		CutoverEpoch      int64  `json:"cutover_epoch"`
		StartedAt         string `json:"started_at"`
		UpdatedAt         string `json:"updated_at"`
		PreflightChecksum string `json:"preflight_checksum,omitempty"`
		RollbackDeadline  string `json:"rollback_deadline,omitempty"`
		Checkpoint        string `json:"checkpoint"`
	}
	w := wire{
		Owner:             m.Owner,
		LedgerID:          m.LedgerID,
		Mode:              m.Mode,
		CutoverEpoch:      m.CutoverEpoch,
		StartedAt:         m.StartedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         m.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PreflightChecksum: m.PreflightChecksum,
		Checkpoint:        string(m.Checkpoint),
	}
	if !m.RollbackDeadline.IsZero() {
		w.RollbackDeadline = m.RollbackDeadline.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(w)
}

// ============================================================================
// Runtime Redis metadata store retained from the feature branch. It owns the
// `<prefix>meta:migration` HASH and exposes CAS advance / Initialize via
// metadata_advance.lua. The wire format mirrors Metadata above, with the
// schema mode serialised through store.KeySchemaMode (rather than the Mode
// alias) so the durable HASH stays aligned with store's parse path.
// ============================================================================

// MetadataStore is the runtime Redis metadata HASH wrapper for the migration
// state machine (doc 14 §3). The full per-key audit ledger belongs in
// PostgreSQL; this hash lets every gateway enforce a single owner/ledger
// identity and monotonic cutover transitions.
type MetadataLuaStore struct {
	rdb    *redis.Client
	prefix string
}

// NewMetadataLua builds a MetadataLuaStore bound to prefix.
func NewMetadataLua(rdb *redis.Client, prefix string) *MetadataLuaStore {
	return &MetadataLuaStore{rdb: rdb, prefix: prefix}
}

// storeKeySchemaModeFromMode bridges the public Mode string alias (used by
// the free-function Preflight wrapper and cmd) to the underlying
// store.KeySchemaMode int the LUA-CAS Advance path expects.
func storeKeySchemaModeFromMode(m Mode) (out store.KeySchemaMode) {
	switch m {
	case ModeDual:
		return store.KeySchemaModeDual
	case ModeCanonical:
		return store.KeySchemaModeCanonical
	default:
		return store.KeySchemaModeLegacy
	}
}

// isLedgerID returns true for a one-shot migration ledger_id. Two shapes
// are accepted (doc 14 §0):
//
//   - UUIDv4-style: 36 chars, hyphens at 8/13/18/23, hex elsewhere
//     (e.g. "134e6d21-721b-41d4-af0b-adff143107e2")
//   - operator-named: lowercase letters / digits / dashes / dots /
//     underscores, 4..64 chars; this is the production shape used by the
//     k2 CLI defaults (e.g. "ursm-v2-k2-20260818-001") and what an
//     operator passes via --ledger-id on the dry-run / apply boundary.
func isLedgerID(s string) bool {
	if l := len(s); l < 4 || l > 64 {
		return false
	}
	if len(s) == 36 {
		for i, r := range s {
			switch i {
			case 8, 13, 18, 23:
				if r != '-' {
					return false
				}
			default:
				if !isHex(r) {
					return false
				}
			}
		}
		return true
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_':
		default:
			return false
		}
	}
	return true
}

func isHex(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'f':
		return true
	case r >= 'A' && r <= 'F':
		return true
	}
	return false
}

// keyKind values exported via aliases so callers (cmd + tests) can refer to
// them without importing the unexported type from classify.go.
const (
	KindNode         = "node"
	KindWindow       = "window"
	KindIndex        = "index"
	KindBinding      = "binding"
	KindCredential   = "credential"
	KindProvider     = "provider"
	KindDedup        = "request_dedup"
	KindUnrecognised = "unrecognised"
)

func (m *MetadataLuaStore) key() string { return m.prefix + "meta:migration" }

// Initialize atomically establishes the immutable owner/ledger identity.
// Re-initializing with the same identity is idempotent; a different identity
// is rejected because ledger IDs are non-reusable (doc 14 §3).
func (m *MetadataLuaStore) Initialize(ctx context.Context, in Metadata) error {
	if m == nil || m.rdb == nil {
		return fmt.Errorf("ursm.v2: migration metadata requires redis")
	}
	if in.Owner == "" || in.LedgerID == "" || in.Checkpoint == "" {
		return fmt.Errorf("ursm.v2: migration metadata requires owner, ledger_id and checkpoint")
	}
	if in.StartedAt.IsZero() {
		in.StartedAt = time.Now().UTC()
	}
	in.UpdatedAt = time.Now().UTC()
	existing, err := m.Read(ctx)
	if err != nil && err != redis.Nil {
		return err
	}
	if err == nil {
		if existing.Owner != in.Owner || existing.LedgerID != in.LedgerID {
			return fmt.Errorf("ursm.v2: migration metadata identity already belongs to owner=%q ledger_id=%q", existing.Owner, existing.LedgerID)
		}
		return nil
	}
	schemaMode, _ := store.ParseKeySchemaMode(string(in.Mode))
	ok, err := m.rdb.HSetNX(ctx, m.key(), "owner", in.Owner).Result()
	if err != nil {
		return fmt.Errorf("ursm.v2: initialize migration metadata: %w", err)
	}
	if !ok {
		// A concurrent initializer won. Re-read to enforce its identity.
		return m.Initialize(ctx, in)
	}
	if err := m.rdb.HSet(ctx, m.key(), map[string]string{
		"ledger_id":          in.LedgerID,
		"mode":               schemaMode.String(),
		"cutover_epoch":      strconv.FormatInt(in.CutoverEpoch, 10),
		"started_at":         in.StartedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":         in.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"preflight_checksum": in.PreflightChecksum,
		"rollback_deadline":  rollbackDeadlineWire(in.RollbackDeadline),
		"checkpoint":         string(in.Checkpoint),
	}).Err(); err != nil {
		return fmt.Errorf("ursm.v2: complete migration metadata initialize: %w", err)
	}
	return nil
}

// Read returns the current metadata HASH, or redis.Nil when it has not been
// initialized.
func (m *MetadataLuaStore) Read(ctx context.Context) (Metadata, error) {
	if m == nil || m.rdb == nil {
		return Metadata{}, fmt.Errorf("ursm.v2: migration metadata requires redis")
	}
	values, err := m.rdb.HGetAll(ctx, m.key()).Result()
	if err != nil {
		return Metadata{}, err
	}
	if len(values) == 0 {
		return Metadata{}, redis.Nil
	}
	mode, err := store.ParseKeySchemaMode(values["mode"])
	if err != nil {
		return Metadata{}, fmt.Errorf("ursm.v2: invalid stored migration mode: %w", err)
	}
	epoch, err := strconv.ParseInt(values["cutover_epoch"], 10, 64)
	if err != nil || epoch < 0 {
		return Metadata{}, fmt.Errorf("ursm.v2: invalid stored cutover epoch %q", values["cutover_epoch"])
	}
	startedAt, err := time.Parse(time.RFC3339Nano, values["started_at"])
	if err != nil {
		return Metadata{}, fmt.Errorf("ursm.v2: invalid stored started_at: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, values["updated_at"])
	if err != nil {
		return Metadata{}, fmt.Errorf("ursm.v2: invalid stored updated_at: %w", err)
	}
	rollback := time.Time{}
	if raw := values["rollback_deadline"]; raw != "" {
		rollback, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Metadata{}, fmt.Errorf("ursm.v2: invalid stored rollback_deadline: %w", err)
		}
	}
	return Metadata{
		Owner:             values["owner"],
		LedgerID:          values["ledger_id"],
		Mode:              Mode(mode.String()),
		CutoverEpoch:      epoch,
		StartedAt:         startedAt,
		UpdatedAt:         updatedAt,
		PreflightChecksum: values["preflight_checksum"],
		RollbackDeadline:  rollback,
		Checkpoint:        Checkpoint(values["checkpoint"]),
	}, nil
}

// Advance uses the metadata hash's epoch as a fencing token. A stale caller
// cannot overwrite a newer cutover/checkpoint; successful advances increment
// the epoch exactly once.
func (m *MetadataLuaStore) Advance(ctx context.Context, expectedEpoch int64, checkpoint Checkpoint, mode store.KeySchemaMode) error {
	if m == nil || m.rdb == nil {
		return fmt.Errorf("ursm.v2: migration metadata requires redis")
	}
	if checkpoint == "" {
		return fmt.Errorf("ursm.v2: migration checkpoint is required")
	}
	result, err := metadataAdvanceScript.Run(ctx, m.rdb, []string{m.key()},
		strconv.FormatInt(expectedEpoch, 10), string(checkpoint), mode.String(), time.Now().UTC().Format(time.RFC3339Nano)).Text()
	if err != nil {
		return fmt.Errorf("ursm.v2: advance migration metadata: %w", err)
	}
	if result != "advanced" {
		return fmt.Errorf("ursm.v2: migration metadata epoch superseded")
	}
	return nil
}

//go:embed metadata_advance.lua
var metadataAdvanceSrc string

var metadataAdvanceScript = redis.NewScript(metadataAdvanceSrc)

func rollbackDeadlineWire(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
