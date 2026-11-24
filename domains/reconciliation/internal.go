package reconciliation

var internalColumns = []string{
	"tenant_id", "request_postings", "recorded_debited_requests", "recorded_gross_debit_credits",
	"recorded_net_credits", "consumption_buckets", "boundary_buckets", "bucket_credits", "bucket_requests",
	"unaligned_hour_buckets", "ledger_hours_without_bucket", "bucket_hours_without_debits", "hours_with_bucket_debit_difference",
	"bucket_minus_recorded_debit_credits", "bill_status", "bill_comparison", "bill_period_start", "bill_period_end",
	"bill_total_credits", "bill_total_requests", "bill_amount_cents", "bill_currency", "bucket_minus_bill_credits",
}

// Gross debits and stored buckets are compared per UTC hour before the period
// rollup. Net postings include refunds and adjustments independently.
const internalFacts = `WITH postings AS (` + requestPostings + `),
ledger AS (
	SELECT tenant_id, count(*) AS request_postings,
	       count(DISTINCT request_id) FILTER (WHERE entry_type='consume' AND net_credits > 0) AS recorded_debited_requests,
	       COALESCE(sum(net_credits) FILTER (WHERE entry_type='consume' AND net_credits > 0), 0) AS recorded_gross_debit_credits,
	       sum(net_credits) AS recorded_net_credits
	FROM postings GROUP BY tenant_id
), debit_hours AS (
	SELECT tenant_id, date_trunc('hour', posted_at, 'UTC') AS bucket_start, sum(net_credits) AS debit_credits
	FROM postings
	WHERE entry_type='consume' AND net_credits > 0
	GROUP BY tenant_id, 2
	HAVING date_trunc('hour', posted_at, 'UTC') >= $1
	   AND date_trunc('hour', posted_at, 'UTC') + interval '1 hour' <= $2
), bucket_rows AS (
	SELECT tenant_id, bucket_start, credits, request_count,
	       bucket_start = date_trunc('hour', bucket_start, 'UTC') AS aligned,
	       bucket_start >= $1 AND bucket_start + interval '1 hour' <= $2 AS in_window
	FROM public.maas_credit_consumption_buckets
	WHERE bucket_start >= $1::timestamptz - interval '1 hour' AND bucket_start < $2
	  AND bucket_start + interval '1 hour' > $1 AND ($3 = '' OR tenant_id = $3)
), buckets AS (
	SELECT tenant_id, count(*) FILTER (WHERE in_window AND aligned) AS consumption_buckets,
	       count(*) FILTER (WHERE NOT in_window) AS boundary_buckets,
	       count(*) FILTER (WHERE NOT aligned) AS unaligned_hour_buckets,
	       sum(credits) FILTER (WHERE in_window AND aligned) AS bucket_credits,
	       sum(request_count) FILTER (WHERE in_window AND aligned) AS bucket_requests
	FROM bucket_rows GROUP BY tenant_id
), complete_buckets AS (
	SELECT tenant_id, bucket_start, credits, request_count FROM bucket_rows WHERE in_window AND aligned
), hours_overlapping_unaligned_buckets AS (
	SELECT DISTINCT tenant_id, date_trunc('hour', bucket_start, 'UTC') + overlap.offset_from_hour AS bucket_start
	FROM bucket_rows CROSS JOIN (VALUES (interval '0 hour'), (interval '1 hour')) AS overlap(offset_from_hour)
	WHERE NOT aligned
), hourly AS (
	SELECT tenant_id,
	       count(*) FILTER (WHERE d.bucket_start IS NOT NULL AND b.bucket_start IS NULL) AS ledger_hours_without_bucket,
	       count(*) FILTER (WHERE d.bucket_start IS NULL AND (b.credits <> 0 OR b.request_count <> 0)) AS bucket_hours_without_debits,
	       count(*) FILTER (WHERE b.credits <> d.debit_credits AND u.bucket_start IS NULL) AS hours_with_bucket_debit_difference
	FROM debit_hours d FULL JOIN complete_buckets b USING (tenant_id, bucket_start)
	LEFT JOIN hours_overlapping_unaligned_buckets u USING (tenant_id, bucket_start)
	GROUP BY tenant_id
), month_bill AS (
	SELECT tenant_id, status AS bill_status, period_start AS bill_period_start, period_end AS bill_period_end,
	       total_credits AS bill_total_credits, total_requests AS bill_total_requests,
	       amount_cents AS bill_amount_cents, currency AS bill_currency
	FROM public.maas_monthly_bills
	WHERE $7::boolean AND period_ym = to_char(timezone($4, $1::timestamptz),'YYYY-MM') AND tenant_id = $3
), evidence AS (
	SELECT tenant_id, COALESCE(request_postings, 0) AS request_postings,
	       COALESCE(recorded_debited_requests, 0) AS recorded_debited_requests,
	       COALESCE(recorded_gross_debit_credits, 0) AS recorded_gross_debit_credits,
	       COALESCE(recorded_net_credits, 0) AS recorded_net_credits,
	       COALESCE(consumption_buckets, 0) AS consumption_buckets,
	       COALESCE(boundary_buckets, 0) AS boundary_buckets, bucket_credits, bucket_requests,
	       COALESCE(unaligned_hour_buckets, 0) AS unaligned_hour_buckets,
	       COALESCE(ledger_hours_without_bucket, 0) AS ledger_hours_without_bucket,
	       COALESCE(bucket_hours_without_debits, 0) AS bucket_hours_without_debits,
	       COALESCE(hours_with_bucket_debit_difference, 0) AS hours_with_bucket_debit_difference,
	       date_trunc('hour',$1::timestamptz,'UTC')=$1 AND date_trunc('hour',$2::timestamptz,'UTC')=$2
	         AND COALESCE(boundary_buckets,0)=0 AND COALESCE(unaligned_hour_buckets,0)=0 AS whole_hours,
	       bill_status, bill_period_start, bill_period_end, bill_total_credits, bill_total_requests, bill_amount_cents, bill_currency,
	       CASE WHEN NOT $7::boolean THEN 'not_applicable'
	            WHEN bill_status IS NULL THEN 'missing'
	            WHEN bill_period_start <> $1 OR bill_period_end <> $2 THEN 'window_mismatch'
	            WHEN bill_status NOT IN ('issued','paid') THEN bill_status
	            ELSE 'comparable' END AS bill_comparison
	FROM ledger FULL JOIN buckets USING (tenant_id) LEFT JOIN hourly USING (tenant_id) FULL JOIN month_bill USING (tenant_id)
)
SELECT tenant_id, request_postings, recorded_debited_requests, recorded_gross_debit_credits, recorded_net_credits,
	consumption_buckets, boundary_buckets, bucket_credits, bucket_requests,
	unaligned_hour_buckets, ledger_hours_without_bucket, bucket_hours_without_debits, hours_with_bucket_debit_difference,
	CASE WHEN whole_hours AND request_postings > 0 AND ledger_hours_without_bucket=0 AND bucket_hours_without_debits=0
	     THEN bucket_credits - recorded_gross_debit_credits END AS bucket_minus_recorded_debit_credits,
	bill_status, bill_comparison, bill_period_start, bill_period_end,
	bill_total_credits, bill_total_requests, bill_amount_cents::text AS bill_amount_cents, bill_currency,
	CASE WHEN whole_hours AND bill_comparison='comparable' AND ledger_hours_without_bucket=0
	     THEN bucket_credits - bill_total_credits END AS bucket_minus_bill_credits
FROM evidence`

const internalTotals = `SELECT count(*) AS tenants_with_evidence,
	COALESCE(sum(recorded_gross_debit_credits), 0) AS recorded_gross_debit_credits,
	COALESCE(sum(recorded_net_credits), 0) AS recorded_net_credits,
	count(*) FILTER (WHERE consumption_buckets=0) AS tenants_without_complete_hour_buckets,
	count(*) FILTER (WHERE boundary_buckets>0) AS tenants_with_boundary_buckets,
	count(*) FILTER (WHERE unaligned_hour_buckets>0 OR ledger_hours_without_bucket>0 OR bucket_hours_without_debits>0) AS tenants_with_hourly_bucket_gaps,
	count(*) FILTER (WHERE bill_comparison='missing') AS tenants_without_bill,
	count(*) FILTER (WHERE bill_comparison='window_mismatch') AS tenants_with_bill_window_mismatch,
	count(bucket_minus_recorded_debit_credits) AS tenants_with_bucket_debit_comparison,
	count(*) FILTER (WHERE bucket_minus_recorded_debit_credits<>0 OR hours_with_bucket_debit_difference>0) AS tenants_with_bucket_debit_difference,
	count(bucket_minus_bill_credits) AS tenants_with_bucket_bill_comparison,
	count(*) FILTER (WHERE bucket_minus_bill_credits<>0) AS tenants_with_bucket_bill_difference
FROM facts`
