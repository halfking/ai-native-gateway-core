-- ===========================================================================
-- File:          sql/migrations/startup/738_view_chain_credits_rate_multiplier.down.sql
-- Migration:     738 (down)
-- Database:      llm_gateway
-- Purpose:       反向：把 738 加进 view 链的 credits_rate_multiplier 列摘掉，
--                重建 736 前的 113 列契约形态。
--
--                注意：738 加列前 bg/stats_minute_rollup INSERT 已经依赖
--                r.credits_rate_multiplier 列；回滚会让 stats 分钟聚合再次
--                报 "column … does not exist"。这是 736 + view 链契约修复的
--                配套锁，必须 738 与 736 同步回滚才一致。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v_bot  text;
  v_mid  text;
  v_top  text;
  v_bot_ddl text;
  v_mid_ddl text;
  v_top_ddl text;
BEGIN
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '738.down: view chain incomplete; nothing to do';
    RETURN;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public'
      AND table_name='request_logs_with_current_month'
      AND column_name='credits_rate_multiplier'
  ) THEN
    RAISE NOTICE '738.down: credits_rate_multiplier not exposed by view chain; nothing to do';
    RETURN;
  END IF;

  v_bot := pg_get_viewdef('public.request_logs_with_current_month_without_customer_id'::regclass, true);
  v_mid := pg_get_viewdef('public.request_logs_with_current_month_without_request_class_due_at'::regclass, true);
  v_top := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);

  DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;
  DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
  DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;

  -- 底层：去掉 credits_rate_multiplier 列（hot 分支无 ;）
  --    需要吃掉前导逗号（origin_actor, 后那个逗号）以避免 orphan ','。
  v_bot_ddl := regexp_replace(
    v_bot,
    E',[[:space:]]*\n[[:space:]]+request_logs_hot\.credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM request_logs_hot\n',
    E'\n   FROM request_logs_hot\n'
  );
  v_bot_ddl := regexp_replace(
    v_bot_ddl,
    E',[[:space:]]*\n[[:space:]]+request_logs\.credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM request_logs;',
    E'\n   FROM request_logs;'
  );
  EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS ' || v_bot_ddl;

  -- 中层：去掉 v.credits_rate_multiplier 整行（前面逗号也吃掉以避免 orphan ','）
  v_mid_ddl := regexp_replace(
    v_mid,
    E',?[[:space:]]*\n[[:space:]]*v\.credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM request_logs_with_current_month_without_customer_id v',
    E'\n   FROM request_logs_with_current_month_without_customer_id v'
  );
  EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS ' || v_mid_ddl;

  -- 顶层：去掉三个分支的 credits_rate_multiplier
  v_top_ddl := regexp_replace(
    v_top,
    E',?[[:space:]]*\n[[:space:]]*NULL::double precision AS credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM session_turns_hot t',
    E'\n    FROM session_turns_hot t'
  );
  v_top_ddl := regexp_replace(
    v_top_ddl,
    E',?[[:space:]]*\n[[:space:]]*NULL::double precision AS credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM session_turns t',
    E'\n    FROM session_turns t'
  );
  v_top_ddl := regexp_replace(
    v_top_ddl,
    E',?[[:space:]]*\n[[:space:]]*v\.credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM request_logs_with_current_month_without_request_class_due_at v',
    E'\n            FROM request_logs_with_current_month_without_request_class_due_at v'
  );
  v_top_ddl := regexp_replace(
    v_top_ddl,
    E',?[[:space:]]*\n[[:space:]]*rl\.credits_rate_multiplier[^[:alnum:]]*\n[[:space:]]+FROM \\(',
    E'\n     FROM ('
  );
  EXECUTE 'CREATE VIEW public.request_logs_with_current_month AS ' || v_top_ddl;

  PERFORM 1;
END $$;

COMMIT;
