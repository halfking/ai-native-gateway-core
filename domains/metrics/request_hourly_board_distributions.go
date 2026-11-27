package metrics

import (
	"context"
	"database/sql"
)

const RequestBoardHourDistributionDaysTable = "public.request_metrics_board_hour_distribution_days"

const (
	boardPieInputColumns      = "dimension, key, requests, tokens, credits, cost_usd"
	boardProviderInputColumns = "provider_id, provider_code, provider_name, request_count, success_count, prompt_tokens, completion_tokens, total_tokens, credits, total_cost_usd"
	boardKeyInputColumns      = "word, mask"
)

// Both grains use the same tenant, traffic and measure definitions.
func boardDistributionStatements(hourly bool) []string {
	prefix := "public.request_metrics_board_"
	tenantColumns := "coalesce(tenant_id,'') AS tenant_id, grouping(tenant_id)=1 AS all_tenants, "
	tenantGrouping := "GROUPING SETS ((tenant_id), ())"
	var bucketColumn, bucketValue string
	if hourly {
		prefix += "hour_"
		bucketColumn = "bucket, "
		bucketValue = boardBucket("occurred_at", "60") + " AS bucket, "
		// Global edge reads merge these same tenant rows.
		tenantColumns = "tenant_id, false AS all_tenants, "
		tenantGrouping = "tenant_id"
	}
	return []string{
		`WITH pies AS (DELETE FROM ` + prefix + `pies WHERE day=$1::date),
		      providers AS (DELETE FROM ` + prefix + `providers WHERE day=$1::date)
		 DELETE FROM ` + prefix + `keys WHERE day=$1::date`,
		`WITH inputs AS MATERIALIZED (
			SELECT ` + bucketValue + `tenant_id, traffic_class, status, total_tokens, credits_charged, cost_amount, cost_currency,
				` + boardModel + ` AS model, ` + boardPieKeys + `
			FROM public.request_metrics_projection WHERE ` + requestDailyBounds + `
		), groups AS (
			SELECT ` + bucketColumn + tenantColumns + `traffic_class, ` + boardPieGrouping + `, ` + boardMeasures + `
			FROM inputs GROUP BY ` + bucketColumn + `traffic_class, ` + tenantGrouping + `, GROUPING SETS (` + boardPieSets + `)
		)
		INSERT INTO ` + prefix + `pies (day, ` + bucketColumn + `tenant_id, all_tenants, traffic_class, dimension, key, requests, tokens, credits, cost_usd)
		SELECT $1::date, ` + bucketColumn + `tenant_id, all_tenants, traffic_class, dimension, key, requests, tokens, credits, cost_usd
		FROM groups WHERE key IS NOT NULL`,
		`INSERT INTO ` + prefix + `providers (
			day, ` + bucketColumn + `tenant_id, all_tenants, traffic_class, provider_id, provider_code, provider_name, request_count,
			success_count, prompt_tokens, completion_tokens, total_tokens, credits, total_cost_usd)
		SELECT $1::date, ` + bucketValue + tenantColumns + `traffic_class, provider_id, ` + boardProviderMeasures + `
		FROM public.request_metrics_projection
		WHERE ` + requestDailyBounds + ` AND provider_id IS NOT NULL
		GROUP BY ` + bucketColumn + `traffic_class, provider_id, ` + tenantGrouping,
		`INSERT INTO ` + prefix + `keys (day, ` + bucketColumn + `tenant_id, all_tenants, traffic_class, word, mask)
		SELECT $1::date, ` + bucketValue + tenantColumns + `traffic_class, api_key_id >> 6, bit_or(1::bigint << (api_key_id & 63)::int)
		FROM public.request_metrics_projection
		WHERE ` + requestDailyBounds + ` AND api_key_id IS NOT NULL
		GROUP BY ` + bucketColumn + `traffic_class, api_key_id >> 6, ` + tenantGrouping,
	}
}

// RebuildRequestBoardHourDistributions publishes one clean day under its state row lock.
func RebuildRequestBoardHourDistributions(ctx context.Context, tx *sql.Tx, day string) error {
	for _, statement := range boardDistributionStatements(true) {
		if _, err := tx.ExecContext(ctx, statement, day); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO `+RequestBoardHourDistributionDaysTable+` (day) VALUES ($1::date)`, day)
	return err
}

const requestBoardHourDistributionCoverageSQL = `distribution_hours AS MATERIALIZED (
	SELECT h.day,h.bucket FROM edge_hours h JOIN ` + RequestBoardHourDistributionDaysTable + ` p USING(day)
), distribution_coverage AS (
	SELECT coalesce(range_agg(tstzrange(bucket,bucket+interval '1 hour','[)')), '{}') AS covered
	FROM distribution_hours
),`

func requestBoardHourDistributionRows(table, columns, scope string) string {
	return " UNION ALL SELECT " + columns + " FROM " + table + " JOIN distribution_hours USING(day,bucket) WHERE true" + scope
}
