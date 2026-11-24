package credit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackfillPostedConsumption fills absent UTC hours from retained request
// debits. The ledger and existing buckets share the statement snapshot.
// A concurrent live charge can win the insert; its bucket is left intact.
func BackfillPostedConsumption(ctx context.Context, pool *pgxpool.Pool, lookbackDays int) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// This is a system-wide operation. Fail if the connection cannot read
	// the complete ledger under its current database role.
	if _, err := tx.Exec(ctx, `SET LOCAL row_security = off`); err != nil {
		return 0, err
	}
	result, err := tx.Exec(ctx, backfillPostedConsumptionSQL, min(90, max(1, lookbackDays)))
	if err != nil {
		return 0, fmt.Errorf("backfill posted credit consumption: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const backfillPostedConsumptionSQL = `WITH posted AS (
	SELECT DISTINCT ON (tenant_id, id) tenant_id, id, ref_id, amount,
	       date_trunc('hour', created_at, 'UTC') AS bucket_start
	FROM public.credit_ledger_with_current_month
	WHERE created_at >= date_trunc('hour', now(), 'UTC') - ($1::int * interval '24 hours')
	  AND tenant_id NOT IN ('', 'default') AND ref_type = 'request'
	  AND entry_type = 'consume' AND amount < 0
	ORDER BY tenant_id, id, created_at DESC
)
INSERT INTO public.maas_credit_consumption_buckets (tenant_id, bucket_start, credits, request_count, updated_at)
SELECT p.tenant_id, p.bucket_start, SUM(-p.amount::numeric)::bigint, count(DISTINCT p.ref_id)::int, now()
FROM posted p
WHERE NOT EXISTS (
	SELECT 1 FROM public.maas_credit_consumption_buckets b
	WHERE b.tenant_id = p.tenant_id
	  AND b.bucket_start > p.bucket_start - interval '1 hour'
	  AND b.bucket_start < p.bucket_start + interval '1 hour'
)
GROUP BY p.tenant_id, p.bucket_start
HAVING count(NULLIF(p.ref_id, '')) = count(*)
ON CONFLICT (tenant_id, bucket_start) DO NOTHING`
