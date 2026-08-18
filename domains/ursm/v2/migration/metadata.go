// Package migration implements the L3 k2 migration machinery described in
// docs 14/15/16 (slice 11-13). It owns preflight classification, copy with
// PTTL preservation, and exact-ledger cleanup. The package is self-contained:
// the only URSM symbols it imports are those explicitly handed over in
// 14 §0 (store.NodeKeyCanonical/WindowKeyCanonical/CandidateIndexKeyCanonical
// and the corresponding Parse*Canonical helpers plus Schema/SchemaK2/SchemaLegacy).
package migration

import (
	"encoding/json"
	"fmt"
	"time"
)

// Mode describes the k2 schema mode for the Redis key layout (doc 14 §3).
// It is orthogonal to api.RolloutMode (URSM v2 vs legacy routing authority).
type Mode string

const (
	ModeLegacy    Mode = "legacy"
	ModeDual      Mode = "dual"
	ModeCanonical Mode = "canonical"
)

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
	Owner            string     `json:"owner"`
	LedgerID         string     `json:"ledger_id"`
	Mode             Mode       `json:"mode"`
	CutoverEpoch     int64      `json:"cutover_epoch"`
	StartedAt        time.Time  `json:"started_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	PreflightChecksum string    `json:"preflight_checksum,omitempty"`
	RollbackDeadline time.Time  `json:"rollback_deadline,omitempty"`
	Checkpoint       Checkpoint `json:"checkpoint"`
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
