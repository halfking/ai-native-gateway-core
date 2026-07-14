package stats

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func flushMinuteRows(
	ctx context.Context,
	db *pgxpool.Pool,
	main map[string]*MinuteRow,
	dims map[string]*DimRow,
	drills map[string]*ErrorDrillRow,
) error {
	if len(main) == 0 && len(dims) == 0 && len(drills) == 0 {
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, row := range main {
		if err := upsertMinuteRow(ctx, tx, row); err != nil {
			return err
		}
	}
	for _, row := range dims {
		if err := upsertDimRow(ctx, tx, row); err != nil {
			return err
		}
	}
	for _, row := range drills {
		if err := upsertDrillRow(ctx, tx, row); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func upsertMinuteRow(ctx context.Context, tx pgx.Tx, row *MinuteRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO request_stats_minute (
			bucket, tenant_id, provider_id, canonical_id,
			requests, success_count, failure_count,
			prompt_tokens, completion_tokens, total_tokens,
			credits_charged, cost_usd, latency_ms_sum
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (bucket, tenant_id, provider_id, canonical_id) DO UPDATE SET
			requests = request_stats_minute.requests + EXCLUDED.requests,
			success_count = request_stats_minute.success_count + EXCLUDED.success_count,
			failure_count = request_stats_minute.failure_count + EXCLUDED.failure_count,
			prompt_tokens = request_stats_minute.prompt_tokens + EXCLUDED.prompt_tokens,
			completion_tokens = request_stats_minute.completion_tokens + EXCLUDED.completion_tokens,
			total_tokens = request_stats_minute.total_tokens + EXCLUDED.total_tokens,
			credits_charged = request_stats_minute.credits_charged + EXCLUDED.credits_charged,
			cost_usd = request_stats_minute.cost_usd + EXCLUDED.cost_usd,
			latency_ms_sum = request_stats_minute.latency_ms_sum + EXCLUDED.latency_ms_sum
	`, row.Bucket, row.TenantID, row.ProviderID, row.CanonicalID,
		row.Requests, row.SuccessCount, row.FailureCount,
		row.PromptTokens, row.CompletionTokens, row.TotalTokens,
		row.CreditsCharged, row.CostUSD, row.LatencyMsSum)
	return err
}

func upsertDimRow(ctx context.Context, tx pgx.Tx, row *DimRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO request_stats_dim_minute (
			bucket, tenant_id, dim_type, dim_key,
			requests, success_count, failure_count,
			total_tokens, credits_charged, cost_usd
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (bucket, tenant_id, dim_type, dim_key) DO UPDATE SET
			requests = request_stats_dim_minute.requests + EXCLUDED.requests,
			success_count = request_stats_dim_minute.success_count + EXCLUDED.success_count,
			failure_count = request_stats_dim_minute.failure_count + EXCLUDED.failure_count,
			total_tokens = request_stats_dim_minute.total_tokens + EXCLUDED.total_tokens,
			credits_charged = request_stats_dim_minute.credits_charged + EXCLUDED.credits_charged,
			cost_usd = request_stats_dim_minute.cost_usd + EXCLUDED.cost_usd
	`, row.Bucket, row.TenantID, row.DimType, row.DimKey,
		row.Requests, row.SuccessCount, row.FailureCount,
		row.TotalTokens, row.CreditsCharged, row.CostUSD)
	return err
}

func upsertDrillRow(ctx context.Context, tx pgx.Tx, row *ErrorDrillRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO request_stats_error_drill_minute (
			bucket, tenant_id, error_kind, model_name, provider_id, client_profile, requests
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (bucket, tenant_id, error_kind, model_name, provider_id, client_profile) DO UPDATE SET
			requests = request_stats_error_drill_minute.requests + EXCLUDED.requests
	`, row.Bucket, row.TenantID, row.ErrorKind, row.ModelName, row.ProviderID, row.ClientProfile, row.Requests)
	return err
}

// CleanupOldMinuteStats deletes rollup rows older than retentionDays.
func CleanupOldMinuteStats(ctx context.Context, db *pgxpool.Pool, retentionDays int) error {
	if db == nil || retentionDays < 1 {
		return nil
	}
	cutoff := fmt.Sprintf("%d days", retentionDays)
	for _, table := range []string{
		"request_stats_minute",
		"request_stats_dim_minute",
		"request_stats_error_drill_minute",
	} {
		if _, err := db.Exec(ctx,
			`DELETE FROM `+table+` WHERE bucket < now() - ($1::int * INTERVAL '1 day')`,
			retentionDays,
		); err != nil {
			return fmt.Errorf("cleanup %s: %w", table, err)
		}
	}
	_ = cutoff
	return nil
}
