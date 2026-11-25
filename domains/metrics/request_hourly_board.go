package metrics

import (
	"context"
	"database/sql"
	"time"
)

const RequestBoardHourDaysTable = "public.request_metrics_board_hour_days"

var requestBoardDayLocation, _ = time.LoadLocation("Asia/Shanghai")

func requestBoardCutsDay(start, end time.Time) bool {
	if requestBoardDayLocation == nil {
		return true
	}
	for _, t := range []time.Time{start, end} {
		local := t.In(requestBoardDayLocation)
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, requestBoardDayLocation)
		if !t.Equal(day) {
			return true
		}
	}
	return false
}

// RebuildRequestBoardHours publishes all hours of one clean day under its state row lock.
func RebuildRequestBoardHours(ctx context.Context, tx *sql.Tx, day string) error {
	bucket := boardBucket("occurred_at", "60")
	statements := []string{
		`WITH removed AS (DELETE FROM public.request_metrics_board_hour_masks WHERE day=$1::date)
		DELETE FROM public.request_metrics_board_hour_summary WHERE day=$1::date`,
		`INSERT INTO public.request_metrics_board_hour_summary (day, bucket, tenant_id, traffic_class, models, ` + requestStatsStateColumns + `)
		SELECT $1::date, ` + bucket + ` AS bucket, tenant_id, traffic_class,
			coalesce(array_agg(DISTINCT model) FILTER (WHERE model <> '__unknown__'), '{}'),
			` + requestStatsRawCounters + `, ` + requestStatsDistributionState + `
		FROM (SELECT *, ` + requestModelKey + ` AS model FROM public.request_metrics_projection
		      WHERE ` + requestDailyBounds + `) r
		GROUP BY bucket, tenant_id, traffic_class`,
		`INSERT INTO public.request_metrics_board_hour_masks (day,bucket,tenant_id,traffic_class,word,mask)
		SELECT $1::date, ` + bucket + ` AS bucket, r.tenant_id, r.traffic_class, s.code/64,
			bit_or(1::bigint << (s.code%64)::int)
		FROM public.request_metrics_projection r
		JOIN public.request_metrics_daily_session_codes s USING (tenant_id,session_id)
		WHERE ` + requestDailyBounds + `
		GROUP BY bucket, r.tenant_id, r.traffic_class, s.code/64`,
		`INSERT INTO ` + RequestBoardHourDaysTable + ` (day) VALUES ($1::date)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, day); err != nil {
			return err
		}
	}
	return nil
}

// Only whole UTC hours contained in a published Shanghai day and in the
// half-open query window can replace detail. Whole published days take priority.
const requestBoardHourCoverageSQL = `edge_hours AS MATERIALIZED (
	SELECT p.day,h.bucket
	FROM ` + RequestBoardHourDaysTable + ` p
	JOIN public.request_metrics_daily_state s USING(day)
	JOIN public.request_metrics_board_days b USING(day)
	CROSS JOIN LATERAL generate_series(
		greatest(date_trunc('hour',$1::timestamptz,'UTC'),date_trunc('hour',p.day::timestamp AT TIME ZONE ` + requestDailyZone + `,'UTC')),
		least($2::timestamptz,(p.day+1)::timestamp AT TIME ZONE ` + requestDailyZone + `)-interval '1 hour',
		interval '1 hour'
	) h(bucket)
	WHERE p.day IN (($1::timestamptz AT TIME ZONE ` + requestDailyZone + `)::date,($2::timestamptz AT TIME ZONE ` + requestDailyZone + `)::date)
		AND NOT s.dirty
		AND (SELECT backfill_complete FROM projection)
		AND NOT EXISTS (SELECT FROM clean_days d WHERE d.day=p.day)
		AND h.bucket >= $1 AND h.bucket >= (p.day::timestamp AT TIME ZONE ` + requestDailyZone + `)
),
hour_coverage AS (
	SELECT coalesce(range_agg(tstzrange(bucket,bucket+interval '1 hour','[)')), '{}') AS covered FROM edge_hours
),`

const requestBoardUncoveredHour = `NOT (occurred_at <@ (SELECT covered FROM hour_coverage))`

func requestBoardHourlyTrendSQL(minutes, scope string) string {
	return ` UNION ALL
	SELECT s.bucket, is_total, model, ` + boardMeasureSums + `,
		sum(success) AS success, sum(latency_sum) AS latency_sum, sum(latency_count) AS latency_count
	FROM edge_hours e JOIN public.request_metrics_board_series s
		ON s.day=e.day AND s.bucket>=e.bucket AND s.bucket<e.bucket+interval '1 hour'
	WHERE s.minutes=` + minutes + scope + `
		AND (is_total OR (model <> '__others__' AND model IN (SELECT key FROM top_models)))
	GROUP BY s.bucket,is_total,model`
}
