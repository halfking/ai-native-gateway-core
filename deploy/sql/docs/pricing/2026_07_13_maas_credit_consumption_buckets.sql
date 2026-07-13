-- ============================================================================
-- 2026-07-13 maas_credit_consumption_buckets — hourly credit consumption counter
-- ============================================================================
--
-- Background:
--   admin dashboard 的 "总积分消耗" KPI 卡 (commit f29b896a3) 走的是
--   SELECT SUM(credits_charged) FROM usage_ledger_with_current_month
--   WHERE ts >= now() - INTERVAL 'N day' AND tenant_id=$1
--
--   这是 full-scan + 5-way aggregation: days=1 扫 1 天 ~ 几万/几十万行,
--   days=30 扫一个月分区 + cold cache miss. admin dashboard 5 分钟自动 refresh,
--   多用户并发触发相同 SQL, 在大流量场景下浪费 IO.
--
-- Solution:
--   把 "总积分消耗" 从 N 行 scan + SUM 降级为 ≤ N 小时桶 (≤ 24/168/720/2160 行)
--   的 btree PK scan. ChargeRequest 在同一个 PG tx 里做 atomic upsert:
--
--     INSERT INTO maas_credit_consumption_buckets
--         (tenant_id, bucket_start, credits, request_count)
--     VALUES ($1, date_trunc('hour', now()), $2, 1)
--     ON CONFLICT (tenant_id, bucket_start) DO UPDATE
--         SET credits = mccb.credits + EXCLUDED.credits,
--             request_count = mccb.request_count + 1,
--             updated_at = now()
--
--   dashboard 读路径替换为 subquery:
--
--     SELECT COALESCE(
--         (SELECT SUM(credits) FROM maas_credit_consumption_buckets
--          WHERE tenant_id = $1 AND bucket_start >= now() - ($2 * INTERVAL '1 day')),
--         0
--     )::bigint AS total_credits_charged
--
-- Properties:
--   - Atomic: PG row-level lock, 多 instance 并发无需外部锁.
--   - Idempotent: 重启 / 重 backfill 是覆盖而非累加, 与 usage_ledger.credits_charged
--     (source of truth) 对齐.
--   - Bounded: 90 天 × 24 hour × N tenant 行数有限, 无需 partition.
--   - Failure-safe: chargeTokens 与 credit_ledger 同一 tx; rollback 时 bucket
--     也不写.
--
-- Backfill:
--   cmd/gateway 启动时 Service.BackfillCreditConsumptionBuckets(90) 把过去
--   90 天 usage_ledger.credits_charged GROUP BY tenant, hour() 写入本表.
--   中间窗口若代码未升级, dashboard 走 degraded fallback (返回 0 + hint).

BEGIN;

CREATE TABLE IF NOT EXISTS public.maas_credit_consumption_buckets (
    tenant_id     text             NOT NULL,
    bucket_start  timestamptz      NOT NULL,
    credits       bigint           NOT NULL DEFAULT 0,
    request_count integer          NOT NULL DEFAULT 0,
    updated_at    timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, bucket_start)
);

COMMENT ON TABLE public.maas_credit_consumption_buckets IS
  'Hourly credit consumption counter. Incremented atomically by ChargeRequest in the same tx as credit_ledger; read by admin /api/usage/summary to avoid scanning usage_ledger.';
COMMENT ON COLUMN public.maas_credit_consumption_buckets.bucket_start IS
  'Hour-bucket start (UTC, date_trunc(''hour'', now())). Up to 90 days retained.';
COMMENT ON COLUMN public.maas_credit_consumption_buckets.credits IS
  'Cumulative credits consumed in this hour bucket (positive sum, in credits_per_1m units).';
COMMENT ON COLUMN public.maas_credit_consumption_buckets.request_count IS
  'Number of ChargeRequest calls in this hour bucket.';

CREATE INDEX IF NOT EXISTS idx_mccb_recent
  ON public.maas_credit_consumption_buckets (bucket_start DESC);

-- Composite PK already covers (tenant_id, bucket_start) so no separate
-- tenant_id index needed; queries filtered by tenant_id + bucket_start
-- range will use the PK.

COMMIT;