package reconciliation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequestLinks shares orchestration correlation across gateway replicas using the same database.
type RequestLinks struct{ pool *pgxpool.Pool }

func NewRequestLinks(pool *pgxpool.Pool) *RequestLinks { return &RequestLinks{pool: pool} }

// Register creates a one-use, short-lived capability for a single internal dispatch.
// The database retains the digest and link evidence, never the capability itself.
func (r *RequestLinks) Register(ctx context.Context, tenant string, keyID int, parentID, bodySHA string) (string, error) {
	if tenant == "" || keyID <= 0 || parentID == "" || len(bodySHA) != 64 {
		return "", errors.New("request link requires a verified key, parent receipt and body fingerprint")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	digest := sha256.Sum256(token[:])
	err := writeReconciliationEvidence(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO public.request_reconciliation_links
			(token_digest, tenant_id, api_key_id, parent_request_id, body_sha256) VALUES ($1,$2,$3,$4,$5)`,
			digest[:], tenant, keyID, parentID, bodySHA)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// Claim associates a new server-issued child ID with the registered dispatch.
func (r *RequestLinks) Claim(ctx context.Context, token, tenant string, keyID int, childID, bodySHA string) (bool, error) {
	if len(token) != 64 || tenant == "" || keyID <= 0 || childID == "" || len(bodySHA) != 64 {
		return false, nil
	}
	raw, err := hex.DecodeString(token)
	if err != nil {
		return false, nil
	}
	digest := sha256.Sum256(raw)
	var matched bool
	err = writeReconciliationEvidence(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE public.request_reconciliation_links
			SET child_request_id = $4, claimed_at = clock_timestamp()
			WHERE token_digest = $1 AND tenant_id = $2 AND api_key_id = $3 AND body_sha256 = $5
			  AND child_request_id IS NULL AND parent_request_id <> $4
			  AND registered_at > clock_timestamp() - interval '5 minutes'`,
			digest[:], tenant, keyID, childID, bodySHA)
		matched = tag.RowsAffected() == 1
		return err
	})
	return matched && err == nil, err
}
