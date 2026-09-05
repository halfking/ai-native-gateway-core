-- 662 post-conditions: fold correctness + new identity upsert + replay idempotency.
DO $do$
DECLARE
    total int;
    occ int;
    msg text;
    fseen timestamptz;
    lseen timestamptz;
    idxdef text;
BEGIN
    SELECT count(*) INTO total FROM provider_error_details;
    IF total <> 2 THEN RAISE EXCEPTION 'ASSERT FAIL: expected 2 rows after fold, got %', total; END IF;

    SELECT occurrences, error_message, first_seen_at, last_seen_at
      INTO occ, msg, fseen, lseen
      FROM provider_error_details WHERE tenant_id = 'tenant-a';
    IF occ <> 6 THEN RAISE EXCEPTION 'ASSERT FAIL: folded occurrences = %, want 6 (2+3+1)', occ; END IF;
    IF msg <> 'rate limited (retry after 8s)' THEN RAISE EXCEPTION 'ASSERT FAIL: survivor message = %, want newest wording', msg; END IF;
    IF lseen <> (SELECT MAX(last_seen_at) FROM provider_error_details WHERE tenant_id='tenant-a') THEN
        RAISE EXCEPTION 'ASSERT FAIL: last_seen not max';
    END IF;
    IF fseen <> (SELECT MIN(first_seen_at) FROM provider_error_details WHERE tenant_id='tenant-a') THEN
        RAISE EXCEPTION 'ASSERT FAIL: first_seen not min';
    END IF;

    SELECT tenant_b_count INTO total FROM (SELECT count(*) AS tenant_b_count FROM provider_error_details WHERE tenant_id='tenant-b') x;
    IF total <> 1 THEN RAISE EXCEPTION 'ASSERT FAIL: untouched tenant-b rows = %, want 1', total; END IF;

    idxdef := pg_get_indexdef('idx_provider_error_details_tenant_cred_fingerprint'::regclass);
    IF idxdef LIKE '%error_message%' THEN RAISE EXCEPTION 'ASSERT FAIL: index still keys on message: %', idxdef; END IF;
    RAISE NOTICE 'ASSERT OK: fold=6 occurrences, newest message survivor, min/max seen, tenant-b intact, index=%', idxdef;
END
$do$;

-- Aggregator's new ON CONFLICT target must match the rebuilt index:
-- same logical bucket, different wording -> upsert, not a new row.
INSERT INTO provider_error_details (provider_id, tenant_id, credential_id, model_name, endpoint, error_type, error_code, error_message, occurrences, first_seen_at, last_seen_at, aggregation_bucket)
VALUES (9, 'tenant-a', '42', 'glm-5.2', 'api.example.com', 'rate_limit', '1210', 'brand new wording', 7, NOW(), NOW(), date_trunc('hour', NOW()))
ON CONFLICT (
    (COALESCE(tenant_id, '')), provider_id, (COALESCE(credential_id, '')),
    (COALESCE(model_name, '')),
    (COALESCE(endpoint, '')), error_type, (COALESCE(error_code, '')),
    COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
)
DO UPDATE SET
    occurrences = EXCLUDED.occurrences,
    error_message = EXCLUDED.error_message,
    updated_at = NOW();

DO $do$
DECLARE
    n int; msg text;
BEGIN
    SELECT count(*), max(error_message) INTO n, msg FROM provider_error_details WHERE tenant_id='tenant-a';
    IF n <> 1 OR msg <> 'brand new wording' THEN
        RAISE EXCEPTION 'ASSERT FAIL: post-upsert rows=% message=%', n, msg;
    END IF;
    RAISE NOTICE 'ASSERT OK: new ON CONFLICT target upserts across wording drift (rows=1, message refreshed)';
END
$do$;
