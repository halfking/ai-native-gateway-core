// Package migration owns the L3 k2 migration machinery (slice 11-13) of
// docs/03-design/02-feature-design/会话优化v4/{14,15,16}*.md. It is the
// only package authorized to write to the k2 migration metadata HASH and to
// drive the preflight/copy/cleanup phases. The package does not edit any
// other URSM file; the only symbols it imports from
// domains/ursm/v2/store are the 14 §0 hand-over set in keys_k2.go.
package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Migrator is the top-level driver. It composes Preflight, Copy, Cleanup
// and the MetadataStore so the CLI / a future G4 operator tool can call
// them through a single seam.
type Migrator struct {
	Prefix string
	Owner   string
	LedgerID string
	Mode    Mode
	RollbackDeadline time.Time
	Now      func() time.Time
	RDB      *redis.Client
	Ledger   *Ledger
}

// Defaults fills the unset fields with the values 14 §0 requires. It does
// not write anything; the CLI is responsible for surfacing the resolved
// metadata before --apply (which is not yet wired in this slice).
func (m *Migrator) defaults() {
	if m.Owner == "" {
		m.Owner = "halfking"
	}
	if m.LedgerID == "" {
		m.LedgerID = "ursm-k2-mig-134e6d21-721b-41d4-af0b-adff143107e2"
	}
	if m.Mode == "" {
		m.Mode = ModeDual
	}
	if m.Prefix == "" {
		m.Prefix = "ursm:v2:"
	}
	if m.Now == nil {
		m.Now = func() time.Time { return time.Now().UTC() }
	}
}

// Metadata builds a Metadata snapshot from the current state. Used by both
// the CLI status command and the metadata store publisher.
func (m *Migrator) Metadata() Metadata {
	m.defaults()
	return Metadata{
		Owner:            m.Owner,
		LedgerID:         m.LedgerID,
		Mode:             m.Mode,
		CutoverEpoch:     0,
		StartedAt:        m.Now(),
		UpdatedAt:        m.Now(),
		RollbackDeadline: m.RollbackDeadline,
		Checkpoint:       CheckpointPreflight,
	}
}

// Preflight runs the preflight phase and writes the metadata HASH on success.
// Dry-run is the default: when --apply is not set, the metadata HASH is
// untouched and the ledger file is *also* not written (the CLI is expected
// to gate this externally via the DryRun flag).
type PreflightDriver struct {
	Migrator *Migrator
	DryRun   bool
}

// Run executes the preflight. The returned summary is always populated;
// the ledger is only appended to when DryRun is false.
func (d *PreflightDriver) Run(ctx context.Context) (PreflightSummary, error) {
	if d.Migrator == nil {
		return PreflightSummary{}, fmt.Errorf("migration: nil migrator")
	}
	d.Migrator.defaults()
	if d.Migrator.Ledger == nil {
		return PreflightSummary{}, fmt.Errorf("migration: ledger not configured")
	}
	if d.DryRun {
		return PreflightSummary{}, nil
	}
	pre := &PreflightRunner{
		Prefix:  d.Migrator.Prefix,
		Ledger:  d.Migrator.Ledger,
		Scanner: &RedisScanner{RDB: d.Migrator.RDB},
		Now:     d.Migrator.Now,
	}
	return pre.Preflight(ctx)
}