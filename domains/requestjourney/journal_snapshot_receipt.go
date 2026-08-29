package requestjourney

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const journalSnapshotReceiptLease = time.Minute

var (
	ErrSnapshotReceiptConflict  = errors.New("request journey journal snapshot receipt payload conflict")
	ErrSnapshotReceiptLeaseLost = errors.New("request journey journal snapshot receipt lease lost")
	ErrSnapshotReceiptInvalid   = errors.New("request journey journal snapshot receipt identity is invalid")
)

// JournalSnapshotReceiptClaim describes the durable ownership acquired for one
// snapshot version. A completed receipt is returned as AlreadyCompleted rather
// than claimed, allowing retries to be safely acknowledged without replaying
// the diagnostic projection.
type JournalSnapshotReceiptClaim struct {
	TenantID         string
	RequestID        string
	SnapshotVersion  int64
	Owner            string
	ClaimUntil       time.Time
	Claimed          bool
	AlreadyCompleted bool
}

// JournalSnapshotReceiptStore persists the idempotency boundary for terminal
// journal snapshots. The store intentionally contains only integrity metadata;
// request and response bodies remain in their existing stores.
type JournalSnapshotReceiptStore struct {
	db    observationOutboxDB
	owner string
	clock func() time.Time
}

func NewPostgresJournalSnapshotReceiptStore(db observationOutboxDB, owner string) *JournalSnapshotReceiptStore {
	if db == nil {
		return nil
	}
	if owner == "" {
		owner = "request-journey-snapshot"
	}
	return &JournalSnapshotReceiptStore{db: db, owner: owner, clock: time.Now}
}

// SnapshotPayloadHash returns the canonical SHA-256 hash used to detect a
// conflicting replay of the same (tenant, request, version) identity.
func SnapshotPayloadHash(snapshot any) (string, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal journal snapshot payload: %w", err)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// Claim atomically inserts or inspects a receipt. A pending/processing receipt
// held by another live owner is not stolen; an expired claim may be reclaimed.
func (s *JournalSnapshotReceiptStore) Claim(ctx context.Context, tenantID, requestID string, version int64, payloadHash string) (JournalSnapshotReceiptClaim, error) {
	claim := JournalSnapshotReceiptClaim{TenantID: tenantID, RequestID: requestID, SnapshotVersion: version, Owner: s.owner}
	if s == nil || s.db == nil || tenantID == "" || requestID == "" || version <= 0 || payloadHash == "" {
		return claim, ErrSnapshotReceiptInvalid
	}
	now := s.clock()
	claim.ClaimUntil = now.Add(journalSnapshotReceiptLease)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return claim, fmt.Errorf("begin journal snapshot receipt claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationOutboxBypass(ctx, tx); err != nil {
		return claim, fmt.Errorf("set journal snapshot receipt RLS bypass: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO journal_snapshot_receipts (
			tenant_id, request_id, snapshot_version, payload_hash,
			status, claim_owner, claim_until, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'processing',$5,$6,$7,$7)
		ON CONFLICT (tenant_id, request_id, snapshot_version) DO NOTHING`,
		tenantID, requestID, version, payloadHash, s.owner, claim.ClaimUntil, now)
	if err != nil {
		return claim, fmt.Errorf("insert journal snapshot receipt: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if err := tx.Commit(ctx); err != nil {
			return claim, fmt.Errorf("commit journal snapshot receipt claim: %w", err)
		}
		claim.Claimed = true
		return claim, nil
	}

	var existingHash, status, owner string
	var until *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT payload_hash, status, claim_owner, claim_until
		FROM journal_snapshot_receipts
		WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3
		FOR UPDATE`, tenantID, requestID, version).Scan(&existingHash, &status, &owner, &until); err != nil {
		return claim, fmt.Errorf("load journal snapshot receipt: %w", err)
	}
	if existingHash != payloadHash {
		return claim, ErrSnapshotReceiptConflict
	}
	if status == "completed" {
		return JournalSnapshotReceiptClaim{TenantID: tenantID, RequestID: requestID, SnapshotVersion: version, AlreadyCompleted: true}, nil
	}
	if until != nil && until.After(now) && owner != "" && owner != s.owner {
		return claim, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE journal_snapshot_receipts
		SET status='processing', claim_owner=$4, claim_until=$5, updated_at=$6
		WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3`,
		tenantID, requestID, version, s.owner, claim.ClaimUntil, now); err != nil {
		return claim, fmt.Errorf("reclaim journal snapshot receipt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return claim, fmt.Errorf("commit journal snapshot receipt reclaim: %w", err)
	}
	claim.Claimed = true
	return claim, nil
}

// Complete marks a claimed receipt durable. The owner and unexpired lease fence
// the write so a stale worker cannot acknowledge a receipt reclaimed by retry.
func (s *JournalSnapshotReceiptStore) Complete(ctx context.Context, claim JournalSnapshotReceiptClaim) error {
	if s == nil || s.db == nil || claim.TenantID == "" || claim.RequestID == "" || claim.SnapshotVersion <= 0 || claim.Owner == "" {
		return ErrSnapshotReceiptLeaseLost
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin journal snapshot receipt completion: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := setObservationOutboxBypass(ctx, tx); err != nil {
		return fmt.Errorf("set journal snapshot receipt RLS bypass: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journal_snapshot_receipts
		SET status='completed', claim_owner=NULL, claim_until=NULL, updated_at=now()
		WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3
		  AND claim_owner=$4 AND status='processing' AND claim_until > now()`,
		claim.TenantID, claim.RequestID, claim.SnapshotVersion, claim.Owner)
	if err != nil {
		return fmt.Errorf("complete journal snapshot receipt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrSnapshotReceiptLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit journal snapshot receipt completion: %w", err)
	}
	return nil
}
