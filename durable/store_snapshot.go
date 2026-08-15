package durable

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// Snapshot is the decrypted, versioned request DTO required by RecoveryWorker.
type Snapshot struct {
	TaskID          string
	TenantID        string
	RequestID       string
	RequestHash     string
	Version         int
	EncryptionKeyID string
	Body            []byte
}

// LoadSnapshot reads and decrypts the authoritative request snapshot. AAD is
// rebuilt from persisted tenant/task/request-hash fields; tampering, unknown
// key/version or a missing row fails closed.
func (s *Store) LoadSnapshot(ctx context.Context, taskID string) (*Snapshot, error) {
	if taskID == "" {
		return nil, errors.New("durable: task ID required")
	}
	if s.kr == nil {
		return nil, ErrNoKeyring
	}
	var snap Snapshot
	var ciphertext string
	err := s.db.QueryRow(ctx, `
		SELECT id, tenant_id, request_id, request_hash,
		       snapshot_version, encryption_key_id, request_snapshot_ciphertext
		FROM durable_llm_tasks
		WHERE id = $1`, taskID).Scan(
		&snap.TaskID, &snap.TenantID, &snap.RequestID, &snap.RequestHash,
		&snap.Version, &snap.EncryptionKeyID, &ciphertext)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("durable: snapshot task %s not found", taskID)
		}
		return nil, fmt.Errorf("durable: load snapshot: %w", err)
	}
	body, keyID, err := secret.DecryptWithAAD(ciphertext, s.kr, secret.AADDomainDurableRequest,
		secret.AADBinding{TenantID: snap.TenantID, TaskID: snap.TaskID, RequestHash: snap.RequestHash})
	if err != nil {
		return nil, fmt.Errorf("durable: decrypt snapshot: %w", err)
	}
	if keyID != snap.EncryptionKeyID {
		return nil, fmt.Errorf("durable: snapshot key id mismatch: envelope=%s column=%s", keyID, snap.EncryptionKeyID)
	}
	snap.Body = body
	return &snap, nil
}
