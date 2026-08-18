package migration

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Checkpoint is the durable migration phase recorded in Redis metadata and
// the PG ledger. A failure transition records CheckpointRollback before the
// schema returns to legacy (doc 14 §6.3).
type Checkpoint string

const (
	CheckpointPreflight Checkpoint = "preflight"
	CheckpointCopy      Checkpoint = "copy"
	CheckpointCoverage  Checkpoint = "coverage"
	CheckpointObserve   Checkpoint = "observe"
	CheckpointCleanup   Checkpoint = "cleanup"
	CheckpointRollback  Checkpoint = "rollback"
)

// Metadata is the runtime state mandated by doc 14 §3. The full per-key
// audit ledger belongs in PostgreSQL; this hash lets every gateway enforce
// a single owner/ledger identity and monotonic cutover transitions.
type Metadata struct {
	Owner             string
	LedgerID          string
	Mode              store.KeySchemaMode
	CutoverEpoch      int64
	StartedAt         string
	UpdatedAt         string
	PreflightChecksum string
	RollbackDeadline  string
	Checkpoint        Checkpoint
}

//go:embed metadata_advance.lua
var metadataAdvanceSrc string

var metadataAdvanceScript = redis.NewScript(metadataAdvanceSrc)

type MetadataStore struct {
	rdb    *redis.Client
	prefix string
}

func NewMetadata(rdb *redis.Client, prefix string) *MetadataStore {
	return &MetadataStore{rdb: rdb, prefix: prefix}
}

func (m *MetadataStore) key() string { return m.prefix + "meta:migration" }

// Initialize atomically establishes the immutable owner/ledger identity.
// Re-initializing with the same identity is idempotent; a different identity
// is rejected because ledger IDs are non-reusable (doc 14 §3).
func (m *MetadataStore) Initialize(ctx context.Context, in Metadata) error {
	if m == nil || m.rdb == nil {
		return fmt.Errorf("ursm.v2: migration metadata requires redis")
	}
	if in.Owner == "" || in.LedgerID == "" || in.Checkpoint == "" {
		return fmt.Errorf("ursm.v2: migration metadata requires owner, ledger_id and checkpoint")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if in.StartedAt == "" {
		in.StartedAt = now
	}
	in.UpdatedAt = now
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
		"mode":               in.Mode.String(),
		"cutover_epoch":      strconv.FormatInt(in.CutoverEpoch, 10),
		"started_at":         in.StartedAt,
		"updated_at":         in.UpdatedAt,
		"preflight_checksum": in.PreflightChecksum,
		"rollback_deadline":  in.RollbackDeadline,
		"checkpoint":         string(in.Checkpoint),
	}).Err(); err != nil {
		return fmt.Errorf("ursm.v2: complete migration metadata initialize: %w", err)
	}
	return nil
}

func (m *MetadataStore) Read(ctx context.Context) (Metadata, error) {
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
	return Metadata{
		Owner:             values["owner"],
		LedgerID:          values["ledger_id"],
		Mode:              mode,
		CutoverEpoch:      epoch,
		StartedAt:         values["started_at"],
		UpdatedAt:         values["updated_at"],
		PreflightChecksum: values["preflight_checksum"],
		RollbackDeadline:  values["rollback_deadline"],
		Checkpoint:        Checkpoint(values["checkpoint"]),
	}, nil
}

// Advance uses the metadata hash's epoch as a fencing token. A stale caller
// cannot overwrite a newer cutover/checkpoint; successful advances increment
// the epoch exactly once.
func (m *MetadataStore) Advance(ctx context.Context, expectedEpoch int64, checkpoint Checkpoint, mode store.KeySchemaMode) error {
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
