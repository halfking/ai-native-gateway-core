-- ===========================================================================
-- File:          sql/migrations/startup/740_view_chain_client_ip.down.sql
-- Migration:     740 down
-- Purpose:       重建 738 形态视图链（无 client_ip；credits_rate_multiplier
--                保留）。viewdef 捕获 → regexp 剥离 client_ip 行 → 重建，
--                逐 view 独立幂等（已无 client_ip 即跳过）。
-- 依赖：底表 client_ip 列不动（341 的表列不属于本迁移）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
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
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NULL
     OR to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NULL
     OR to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '740 down: view chain incomplete; skipping';
    RETURN;
  END IF;

  v_bot_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_customer_id'
                           AND column_name='client_ip');
  v_mid_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month_without_request_class_due_at'
                           AND column_name='client_ip');
  v_top_has := EXISTS (SELECT 1 FROM information_schema.columns
                         WHERE table_schema='public'
                           AND table_name='request_logs_with_current_month'
                           AND column_name='client_ip');

  IF NOT v_bot_has AND NOT v_mid_has AND NOT v_top_has THEN
    RAISE NOTICE '740 down: view chain already free of client_ip; nothing to do';
    RETURN;
  END IF;

  IF v_top_has THEN
    v_top := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
  END IF;
  IF v_mid_has THEN
    v_mid := pg_get_viewdef('public.request_logs_with_current_month_without_request_class_due_at'::regclass, true);
  END IF;
  IF v_bot_has THEN
    v_bot := pg_get_viewdef('public.request_logs_with_current_month_without_customer_id'::regclass, true);
  END IF;

  IF v_top_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;
  END IF;
  IF v_mid_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
  END IF;
  IF v_bot_has THEN
    DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;
  END IF;

  -- 剥离 up 插入/重建出的 client_ip 列。pg_get_viewdef 归一化输出为尾逗号
  -- 风格（`前列,` 换行 `client_ip 列`），故剥离单位 = 「逗号 + client_ip 行」，
  -- 恰好还原 738 形态的收尾列。
  IF v_bot_has THEN
    v_bot_ddl := regexp_replace(
      v_bot,
      E',[[:space:]]*request_logs_hot\.client_ip',
      '',
      'g'
    );
    v_bot_ddl := regexp_replace(
      v_bot_ddl,
      E',[[:space:]]*request_logs\.client_ip',
      ''
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS ' || v_bot_ddl;
  END IF;

  IF v_mid_has THEN
    v_mid_ddl := regexp_replace(
      v_mid,
      E',[[:space:]]*v\.client_ip',
      ''
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS ' || v_mid_ddl;
  END IF;

  IF v_top_has THEN
    v_top_ddl := regexp_replace(
      v_top,
      E',[[:space:]]*NULL::inet AS client_ip',
      '',
      'g'
    );
    v_top_ddl := regexp_replace(
      v_top_ddl,
      E',[[:space:]]*v\.client_ip',
      ''
    );
    v_top_ddl := regexp_replace(
      v_top_ddl,
      E',[[:space:]]*rl\.client_ip',
      ''
    );
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month AS ' || v_top_ddl;
  END IF;
END $$;

-- 列数对账（738 形态：109/110/114）。
DO $$
DECLARE
  cnt integer;
BEGIN
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month_without_customer_id';
  IF cnt <> 109 THEN
    RAISE EXCEPTION '740 down: bottom view column count = % (expected 109)', cnt;
  END IF;
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month_without_request_class_due_at';
  IF cnt <> 110 THEN
    RAISE EXCEPTION '740 down: middle view column count = % (expected 110)', cnt;
  END IF;
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 114 THEN
    RAISE EXCEPTION '740 down: canonical view column count = % (expected 114)', cnt;
  END IF;
  RAISE NOTICE '740 down: view chain column counts = 109/110/114 OK';
END $$;

DELETE FROM public.schema_migrations WHERE version = '740';

COMMIT;
