package requestjourney

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
	TenantID        string
	RequestID       string
	SnapshotVersion int64
	// ProjectionBaseSeq is fixed on the first claim and reused for every retry.
	// It keeps generated JourneyEvent sequences stable across lease reclaim.
	ProjectionBaseSeq int64
	Owner             string
	ClaimUntil        time.Time
	Claimed           bool
	AlreadyCompleted  bool
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

// SetClockForTest controls lease timestamps in focused receipt tests.
func (s *JournalSnapshotReceiptStore) SetClockForTest(clock func() time.Time) {
	if s != nil && clock != nil {
		s.clock = clock
	}
}

// SnapshotPayloadHash returns the canonical SHA-256 hash used to detect a
// conflicting replay of the same (tenant, request, version) identity. Only
// immutable snapshot content participates; caller authorization metadata is
// deliberately excluded from the idempotency payload.
func SnapshotPayloadHash(tenantID, requestID string, entries any, truncated bool, truncatedCount int, version int64) (string, error) {
	payload, err := json.Marshal(struct {
		TenantID       string `json:"tenant_id"`
		RequestID      string `json:"request_id"`
		Entries        any    `json:"entries"`
		Truncated      bool   `json:"truncated"`
		TruncatedCount int    `json:"truncated_count"`
		Version        int64  `json:"version"`
	}{tenantID, requestID, entries, truncated, truncatedCount, version})
	if err != nil {
		return "", fmt.Errorf("marshal journal snapshot payload: %w", err)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// Claim is a legacy compat shim for adapters that have no projection history
// yet. It is retained only so older callers do not crash on signature change.
//
// Deprecated: production adapters must call ClaimWithProjectionBase with the
// first-attempt base derived from their event recorder so concurrent retries
// that disagree on the base are surfaced as ErrSnapshotReceiptLeaseLost
// instead of silently overwriting each other's lease. Callers that rely on
// the default-zero base effectively run without any base-pinning and accept
// the historical risk that two concurrent Claim calls cannot be told apart.
func (s *JournalSnapshotReceiptStore) Claim(ctx context.Context, tenantID, requestID string, version int64, payloadHash string) (JournalSnapshotReceiptClaim, error) {
	return s.ClaimWithProjectionBase(ctx, tenantID, requestID, version, payloadHash, 0)
}

// ClaimWithProjectionBase fixes the event-sequence base on the first claim and
// returns that same base on lease reclaim. The immutable base makes retry event
// identities stable even after partial projection or a failed Complete call.
//
// Concurrency contract:
//   - On INSERT path the base is stored as-is.
//   - On reclaim UPDATE the WHERE clause re-checks projection_base_seq matches
//     the value supplied by the caller (allowing the historical first-claim
//     zero to overwrite only zero). A mismatched base is treated as a lease
//     lost scenario so two concurrent retries cannot silently overwrite one
//     another when they disagree on the projection history.
func (s *JournalSnapshotReceiptStore) ClaimWithProjectionBase(ctx context.Context, tenantID, requestID string, version int64, payloadHash string, projectionBaseSeq int64) (JournalSnapshotReceiptClaim, error) {
	if s == nil {
		return JournalSnapshotReceiptClaim{TenantID: tenantID, RequestID: requestID, SnapshotVersion: version, ProjectionBaseSeq: projectionBaseSeq}, ErrSnapshotReceiptInvalid
	}
	claim := JournalSnapshotReceiptClaim{TenantID: tenantID, RequestID: requestID, SnapshotVersion: version, ProjectionBaseSeq: projectionBaseSeq, Owner: s.owner}
	if s.db == nil || tenantID == "" || requestID == "" || version <= 0 || payloadHash == "" || projectionBaseSeq < 0 {
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
			tenant_id, request_id, snapshot_version, payload_hash, projection_base_seq,
			status, claim_owner, claim_until, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,'processing',$6,$7,$8,$8)
		ON CONFLICT (tenant_id, request_id, snapshot_version) DO NOTHING`,
		tenantID, requestID, version, payloadHash, projectionBaseSeq, s.owner, claim.ClaimUntil, now)
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

	var existingHash, status string
	var owner pgtype.Text
	var existingBase int64
	var until pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until
		FROM journal_snapshot_receipts
		WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3
		FOR UPDATE`, tenantID, requestID, version).Scan(&existingHash, &existingBase, &status, &owner, &until); err != nil {
		return claim, fmt.Errorf("load journal snapshot receipt: %w", err)
	}
	if existingHash != payloadHash {
		return claim, ErrSnapshotReceiptConflict
	}
	if status == "completed" {
		return JournalSnapshotReceiptClaim{TenantID: tenantID, RequestID: requestID, SnapshotVersion: version, ProjectionBaseSeq: existingBase, AlreadyCompleted: true}, nil
	}
	// Any unexpired processing lease is owned by another delivery attempt.
	// Do not treat equal owner strings as safe: hostname/configured owners can
	// be shared by multiple local processes.
	if until.Valid && until.Time.After(now) {
		return claim, nil
	}
// Reclaim guards (P1-4 contract): callers must pass the same base the
	// existing row stores, unless this is the historical first-claim sentinel
	// where both stored and supplied are zero. The both-zero sentinel is the
	// documented exception that lets adapters with no projection history yet
	// still issue a reclaim.
	//
	// IMPORTANT concurrency caveat (2026-08-31, audit P1-4 followup):
	// the both-zero path is NOT concurrency-safe across processes. Two
	// gateway replicas each passing projection_base_seq=0 for the same
	// (tenant, request, version) tuple will see the same stored row, the
	// same in-store guard, and one will quietly reclaim the other's lease.
	// Production safety relies on the dispatch adapter serialising per-owner
	// delivery before calling ClaimWithProjectionBase (see
	// cmd/gateway/main_dispatch_observation.go a.mu). Do not delete the
	// both-zero exception without first guaranteeing that *every* production
	// caller derives a strictly positive base; the legacy Claim shim and the
	// recorder-fresh path at cmd/gateway/main_dispatch_observation.go:186
	// rely on it.
	if existingBase != projectionBaseSeq && !(existingBase == 0 && projectionBaseSeq == 0) {
		return claim, ErrSnapshotReceiptLeaseLost
	}
	tag, err = tx.Exec(ctx, `
		UPDATE journal_snapshot_receipts
		SET status='processing', claim_owner=$4, claim_until=$5, updated_at=$6
		WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3
		  AND projection_base_seq = $7`,
		tenantID, requestID, version, s.owner, claim.ClaimUntil, now, existingBase)
	if err != nil {
		return claim, fmt.Errorf("reclaim journal snapshot receipt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return claim, ErrSnapshotReceiptLeaseLost
	}
	claim.ProjectionBaseSeq = existingBase
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
