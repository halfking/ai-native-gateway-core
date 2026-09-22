-- ===========================================================================
-- File:          sql/migrations/startup/738_view_chain_credits_rate_multiplier.sql
-- Migration:     738
-- Database:      llm_gateway
-- Purpose:       736 (Wave 3 B1) 在 request_logs / request_logs_hot 添加了
--                credits_rate_multiplier 列，但 736 没更新 view 链
--                request_logs_with_current_month*（680/717/734 重建的"冻结
--                体"，显式列列表不会因 ALTER TABLE 自动补列）。
--
--                bg/stats_minute_rollup.go 的 INSERT INTO request_stats_minute
--                引用 r.credits_rate_multiplier，view 链缺列 → 每分钟聚合任务
--                抛 "column r.credits_rate_multiplier does not exist at
--                character 1239"，落库空转（docker logs 见 2026-09-22
--                21:40:08 / 21:41:08 两次同错）。
--
--                本迁移遵循 717 同款「viewdef 捕获 → DROP CASCADE → 按补列后
--                体形重建」惯用法，把 credits_rate_multiplier 列加进 view 链
--                三个 view 的 SELECT 列表（位置对齐 113 列 + 1，total = 114）。
--
--                幂等：每个 view 独立检查列是否已在 viewdef 里；若已含则跳过
--                该 view 的 drop/recreate，避免重复添加报错（708 历史教训：
--                部分 view 已含列的世系下，重跑会 "column … specified more
--                than once"）。114 列契约后续如再扩列，按 696/700/717/734 双
--                形态守卫先例再续。
--
-- Dependencies:  736 (column on request_logs / request_logs_hot)。
-- Idempotent:    是（按 view 独立判断，跳过即 no-op）。
-- Down:          738_view_chain_credits_rate_multiplier.down.sql（重建 736
--                前形态，同样按 view 独立幂等）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  -- 三个 view 的当前 viewdef（pg_get_viewdef 输出是单行文本含 + 行续符）
  v_bot text;
  v_mid text;
  v_top text;
  v_bot_ddl text;
  v_mid_ddl text;
  v_top_ddl text;
  v_bot_has  boolean;
  v_mid_has  boolean;
  v_top_has  boolean;
BEGIN
  -- ── 0. 守卫：view 链或列缺一即 no-op ─────────────────────────────────
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '738: view chain incomplete (680-incident shape); skipping — db.ensure rebuilds at startup';
    RETURN;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public'
      AND table_name='request_logs_hot'
      AND column_name='credits_rate_multiplier'
  ) THEN
    RAISE NOTICE '738: request_logs_hot.credits_rate_multiplier missing (736 not applied); skipping';
    RETURN;
  END IF;

  -- 每个 view 独立判断（按 viewdef 字符串 grep 即可；pg_get_viewdef 的列名
  -- 展开是稳定锚点）。
  v_bot_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_customer_id'
                           AND column_name='credits_rate_multiplier');
  v_mid_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_request_class_due_at'
                           AND column_name='credits_rate_multiplier');
  v_top_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month'
                           AND column_name='credits_rate_multiplier');

  IF v_bot_has AND v_mid_has AND v_top_has THEN
    RAISE NOTICE '738: view chain already exposes credits_rate_multiplier on all 3 views; nothing to do';
    RETURN;
  END IF;

  -- ── 1. 仅捕获需要重建的 viewdef ────────────────────────────────────
  IF NOT v_bot_has THEN
    v_bot := pg_get_viewdef('public.request_logs_with_current_month_without_customer_id'::regclass, true);
  END IF;
  IF NOT v_mid_has THEN
    v_mid := pg_get_viewdef('public.request_logs_with_current_month_without_request_class_due_at'::regclass, true);
  END IF;
  IF NOT v_top_has THEN
    v_top := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
  END IF;

  -- ── 2. 自顶向下 drop（CASCADE 在 view→view 链路里不保证传递：逐层显式
  --       drop 最稳；同时仅 drop 需要重建的 view）───────────────────────
  IF NOT v_top_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;
  END IF;
  IF NOT v_mid_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
  END IF;
  IF NOT v_bot_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;
  END IF;

  -- ── 3. 重建底层 view（hot∪parent，hot/parent 都补 credits_rate_multiplier）
  --    用 [[:space:]]* 容忍 pg_get_viewdef 输出的任意行续符 / 缩进，避免硬编码空格数。
  IF NOT v_bot_has THEN
    v_bot_ddl := regexp_replace(
      v_bot,
      E'\n[[:space:]]+FROM request_logs_hot\n',
      E'\n   , request_logs_hot.credits_rate_multiplier\n   FROM request_logs_hot\n'
    );
    v_bot_ddl := regexp_replace(
      v_bot_ddl,
      E'\n[[:space:]]+FROM request_logs;',
      E'\n   , request_logs.credits_rate_multiplier\n   FROM request_logs;'
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS ' || v_bot_ddl;
  END IF;

  -- ── 4. 重建中层 view（m.customer_id 末列后追加 v.credits_rate_multiplier）──
  IF NOT v_mid_has THEN
    v_mid_ddl := regexp_replace(
      v_mid,
      E'\n[[:space:]]+m\.customer_id\n[[:space:]]+FROM request_logs_with_current_month_without_customer_id v',
      E'\n    m.customer_id\n   , v.credits_rate_multiplier\n   FROM request_logs_with_current_month_without_customer_id v'
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS ' || v_mid_ddl;
  END IF;

  -- ── 5. 重建顶层 view（三个 SELECT 分支都补 credits_rate_multiplier）────
  IF NOT v_top_has THEN
    --    5a) session_turns_hot 分支
    v_top_ddl := regexp_replace(
      v_top,
      E'\n[[:space:]]+t\.raw_model_name\n[[:space:]]+FROM session_turns_hot t',
      E'\n     t.raw_model_name\n   , NULL::double precision AS credits_rate_multiplier\n    FROM session_turns_hot t'
    );
    --    5b) session_turns 分支
    v_top_ddl := regexp_replace(
      v_top_ddl,
      E'\n[[:space:]]+t\.raw_model_name\n[[:space:]]+FROM session_turns t',
      E'\n     t.raw_model_name\n   , NULL::double precision AS credits_rate_multiplier\n    FROM session_turns t'
    );
    --    5c-1) v1 兜底分支 — 内 subquery 加 v.credits_rate_multiplier（让 rl 拿到 114 列）
    v_top_ddl := regexp_replace(
      v_top_ddl,
      E'\n[[:space:]]+source\.raw_model_name\n[[:space:]]+FROM request_logs_with_current_month_without_request_class_due_at v',
      E'\n             source.raw_model_name\n   , v.credits_rate_multiplier\n            FROM request_logs_with_current_month_without_request_class_due_at v'
    );
    --    5c-2) v1 兜底分支 — 外 SELECT 加 rl.credits_rate_multiplier（让 UNION 列对齐）
    v_top_ddl := regexp_replace(
      v_top_ddl,
      E'\n[[:space:]]+rl\.raw_model_name\n[[:space:]]+FROM[[:space:]]*\\(',
      E'\n     rl.raw_model_name\n   , rl.credits_rate_multiplier\n     FROM ('
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month AS ' || v_top_ddl;
  END IF;
END $$;

-- 列数对账（v_top 重建后列数应为 114）
DO $$
DECLARE
  cnt integer;
BEGIN
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 114 THEN
    RAISE WARNING '738: request_logs_with_current_month column count = % (expected 114); view chain may be in a partial state — investigate', cnt;
  ELSE
    RAISE NOTICE '738: request_logs_with_current_month column count = 114 OK';
  END IF;
END $$;

-- Ledger self-registration（710/734 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('738', 'view chain exposes credits_rate_multiplier (Wave 3 B1 follow-up: 736 added the column on base tables but the 680/717/734 frozen view bodies were not refreshed; bg/stats_minute_rollup INSERT relied on the missing column)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
