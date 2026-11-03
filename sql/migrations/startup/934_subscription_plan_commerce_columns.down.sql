-- Down for 934: drop commerce columns on subscription_plans / topup_packages.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';
ALTER TABLE public.subscription_plans
    DROP COLUMN IF EXISTS billing_period,
    DROP COLUMN IF EXISTS quota_credits,
    DROP COLUMN IF EXISTS overage_price_cents,
    DROP COLUMN IF EXISTS overage_allowed,
    DROP COLUMN IF EXISTS effective_from,
    DROP COLUMN IF EXISTS effective_to,
    DROP COLUMN IF EXISTS auto_renew,
    DROP COLUMN IF EXISTS concurrency_limit,
    DROP COLUMN IF EXISTS sla;
ALTER TABLE public.topup_packages
    DROP COLUMN IF EXISTS bonus_credits,
    DROP COLUMN IF EXISTS effective_from,
    DROP COLUMN IF EXISTS effective_to,
    DROP COLUMN IF EXISTS first_time_only;
DELETE FROM public.schema_migrations WHERE version = '934';
COMMIT;
