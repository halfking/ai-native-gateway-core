package metrics

import (
	"context"
	"time"
)

const sessionFilterOptionsV2SQL = `
WITH projection AS (
	SELECT backfill_complete,updated_at,
		(SELECT min(enqueued_at) FROM public.request_metrics_pending
		 WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3='' OR tenant_id=$3)) AS pending_since
	FROM public.request_metrics_projection_state WHERE singleton
), candidates AS (
	SELECT DISTINCT nullif(` + requestModelKey + `,'__unknown__') AS model,nullif(provider_code,'') AS provider
	FROM public.request_metrics_projection
	WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3='' OR tenant_id=$3)
	  AND ($4='' OR owner_user=$4) AND source_fact_id IS NOT NULL AND traffic_class='business'
	  AND (SELECT backfill_complete FROM projection)
)
SELECT backfill_complete,updated_at,pending_since,jsonb_build_object(
	'models',coalesce((SELECT jsonb_agg(DISTINCT model COLLATE "C" ORDER BY model COLLATE "C")
		FILTER (WHERE model IS NOT NULL) FROM candidates),'[]'),
	'providers',coalesce((SELECT jsonb_agg(DISTINCT provider COLLATE "C" ORDER BY provider COLLATE "C")
		FILTER (WHERE provider IS NOT NULL) FROM candidates),'[]'))
FROM projection`

func ReadSessionFilterOptions(ctx context.Context, db requestStatsDB, start, end time.Time, tenant, owner string) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	err := db.QueryRow(ctx, sessionFilterOptionsV2SQL, start, end, tenant, owner).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
