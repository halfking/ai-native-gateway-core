-- ===========================================================================
-- File:          961_market_scoring_runs.sql
-- Migration:     961
-- Purpose:       「运营体系 A-17」M2-T4 告警面地基：每日评分 job（bg/market_scoring.go，
--                定稿 M2-T3）的**运行台账**。price_confidence_degraded 告警的
--                「C3 非 trusted 绑定数环比上升」需要**两天的读数** ——
--                supplier_market_position 是 upsert 活表（computed_at 只留最后一次），
--                没有历史 ⇒ 环比无源。本表每次评分一行（ON CONFLICT (run_date)
--                DO UPDATE —— 幂等重放收敛），健康检查读「最近两行」做环比。
--
--                ★ 写入方只有 bg.MarketScoringWorker（单事务内与评分同写）；
--                  多实例共库时由评分事务的 advisory lock 串行化。
--                ★ 空表不报错：健康检查对「没有上一行可比」沉默（没有基线
--                  就没有环比），与 pricing_plan_stale 的「空表≠故障」同纪律。
--
-- Down: 961_market_scoring_runs.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.market_scoring_runs (
    run_date          date PRIMARY KEY,
    bindings          int NOT NULL DEFAULT 0,
    trusted_count     int NOT NULL DEFAULT 0,
    non_trusted_count int NOT NULL DEFAULT 0,
    fx_folded_rows    int NOT NULL DEFAULT 0,
    fx_missing_folds  int NOT NULL DEFAULT 0,
    vendor_absent     int NOT NULL DEFAULT 0,
    unknown_currency  int NOT NULL DEFAULT 0,
    detail            jsonb NOT NULL DEFAULT '{}'::jsonb,
    computed_at       timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.market_scoring_runs IS
    'Daily scoring run ledger (961, M2-T3 job bg/market_scoring.go). One row per local run date; idempotent replay converges via ON CONFLICT (run_date). Feeds the price_confidence_degraded health check (day-over-day non-trusted comparison); no history in supplier_market_position itself (upsert, computed_at only keeps the last run). Empty table = job never ran, not a fault.';
COMMENT ON COLUMN public.market_scoring_runs.non_trusted_count IS
    'C3 gate <> trusted 的绑定数（suspect_symmetric/empty/unknown_currency 合计）；price_confidence_degraded 的环比分子。';
COMMENT ON COLUMN public.market_scoring_runs.detail IS
    '扩展位（本轮空对象）：未来评分口径变化时的诊断载荷。';

-- ★ ≥952 自登记纪律（TestMigrationLedgerSelfRegistration 门）：本迁移必须
--   给 schema_migrations 写自己的记账行 —— 950/951/957/958 四处断层（960
--   回填）之后，自登记是约定不是可选项。
INSERT INTO public.schema_migrations (version, description)
VALUES ('961', 'market_scoring_runs daily ledger: feeds price_confidence_degraded day-over-day comparison (M2-T4 alert base)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
