-- ===========================================================================
-- File:          sql/migrations/startup/815_request_logs_view_stage_band_cff.down.sql
-- Migration:     815 down
-- Purpose:       剥掉 815 追加的 origin_stage / token_band /
--                client_forwarded_for 三列，还原 740 的 115 列形态。
--
-- 机制与 740.down 同款：viewdef 捕获 → regexp 剥离 → 重建。
--
-- ── 剥离模式必须按 pg_get_viewdef 的**实际渲染形态**写，不能照抄 815 up
--    里的文本。实测（真库归一化输出）三处差异，每一处都会让按 up 文本写的
--    pattern 一条都剥不掉、然后 42601 在重建时炸：
--      1. 会话分支的 `t.origin_stage AS origin_stage` 被 PG 省略成
--         `t.origin_stage`——别名与列名相同时不保留 AS。
--      2. v1 分支外层投影渲染成 `rl.<col>`，不是 up 文本里的裸名。
--      3. lateral 两臂里这三列后面都还跟着 system_fingerprint/raw_model_name，
--         所以它们是「列名 + 尾逗号」；会话分支与 v1 外层则是「前导逗号 +
--         列名」。锚点用错方向会剥出列数相等但错列的 SQL。
--
-- 只动顶层 view：815 的三列只出现在顶层（会话分支 t.<col> 直映 + v1 分支
-- lateral 展开），中层/底层包装链从未携带它们，因此无需级联重建。
--
-- 级联面（真库实测）：DROP ... CASCADE 会带走两个依赖视图
-- v_model_health_dashboard 与 v_probe_system_health。二者由
-- db.ensureProbeHealthDashboardViews 在启动期自愈重建（740.down 同款取舍），
-- 但**在线回滚**（不经重启）期间它们是不存在的——回滚前先确认这一点。
--
-- 注意：down 之后 admin/compression_stats.go:212 的 token_band 读端会重新
-- 42703 —— 那正是 815 上线前的状态，属预期回退。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
  v_top       text;
  v_top_ddl   text;
  v_top_has   boolean;
BEGIN
  IF to_regclass('public.request_logs_with_current_month') IS NULL THEN
    RAISE NOTICE '815 down: canonical view missing; nothing to do';
    RETURN;
  END IF;

  v_top_has := (SELECT count(*) = 3 FROM information_schema.columns
                 WHERE table_schema='public'
                   AND table_name='request_logs_with_current_month'
                   AND column_name IN ('origin_stage','token_band','client_forwarded_for'));
  IF NOT v_top_has THEN
    RAISE NOTICE '815 down: canonical view already free of the three columns; nothing to do';
    RETURN;
  END IF;

  v_top := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);

  -- CREATE OR REPLACE 不能删列（只能追加），故先 DROP 顶层再建回 740 形态。
  DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;

  v_top_ddl := v_top;

  -- 会话分支两条腿（前导逗号锚点）。
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*t\.origin_stage', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*t\.token_band', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*t\.client_forwarded_for', '', 'g');
  -- v1 分支外层按名归一化投影（前导逗号锚点）。
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*rl\.origin_stage', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*rl\.token_band', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*rl\.client_forwarded_for', '', 'g');
  -- v1 分支内层 lateral 追加（前导逗号锚点）。
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*source\.origin_stage', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*source\.token_band', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E',[[:space:]]*source\.client_forwarded_for', '', 'g');
  -- lateral 选列两臂（尾逗号锚点，见头注释第 3 条）。
  v_top_ddl := regexp_replace(v_top_ddl, E'h\.origin_stage,[[:space:]]*', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E'h\.token_band,[[:space:]]*', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E'h\.client_forwarded_for,[[:space:]]*', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E'p\.origin_stage,[[:space:]]*', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E'p\.token_band,[[:space:]]*', '', 'g');
  v_top_ddl := regexp_replace(v_top_ddl, E'p\.client_forwarded_for,[[:space:]]*', '', 'g');

  -- 半剥离视图是最坏结果：列数可能仍相等、查询仍 200、只是 v1 分臂三列恒
  -- NULL。先断言再重建。
  IF v_top_ddl ~ '(origin_stage|token_band|client_forwarded_for)' THEN
    RAISE EXCEPTION '815 down: strip left a residual reference to one of the three columns — refusing to recreate a half-stripped view';
  END IF;

  EXECUTE 'CREATE VIEW public.request_logs_with_current_month AS ' || v_top_ddl;
END $$;

-- 列数对账（740 形态：顶层 115 列；中层/底层本迁移未动）。
DO $$
DECLARE
  cnt integer;
BEGIN
  SELECT count(*) INTO cnt
    FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_with_current_month';
  IF cnt <> 115 THEN
    RAISE EXCEPTION '815 down: request_logs_with_current_month column count = % (expected 115); strip did not converge', cnt;
  END IF;
  IF (SELECT count(*) FROM information_schema.columns
       WHERE table_schema='public' AND table_name='request_logs_with_current_month'
         AND column_name IN ('origin_stage','token_band','client_forwarded_for')) <> 0 THEN
    RAISE EXCEPTION '815 down: one of the three columns survived the strip';
  END IF;
  -- 三列在物理表上必须仍在（down 只动视图，不动数据源），否则说明 up/down
  -- 链某一侧误删了源列。
  IF (SELECT count(*) FROM information_schema.columns
       WHERE table_schema='public' AND table_name='request_logs'
         AND column_name IN ('origin_stage','token_band','client_forwarded_for')) <> 3 THEN
    RAISE EXCEPTION '815 down: physical request_logs lost one of the three source columns';
  END IF;
  RAISE NOTICE '815 down: canonical view back to 115 columns OK';
END $$;

-- schema_migrations 行按 append-only 惯例保留。多数派是这一支：710 / 734 / 738
-- 都保留。**740 是例外**（740.down 末行 DELETE FROM schema_migrations WHERE
-- version='740'），所以本行原先引用的「710/740 惯例」把先例说反了。
--
-- 操作后果（不是理论）：本表是 PRIMARY KEY(version)，而 up 文件末尾会
-- INSERT 自己那一行。down 之后**朴素重跑 up 会在那个 INSERT 处主键冲突、
-- 整笔事务回滚，视图停在 115 列**。响亮地失败可以接受，但「回滚后再前滚」
-- 必须先 `DELETE FROM schema_migrations WHERE version='815'`。
-- 该顺序已被 db.TestRequestLogsViewV2EnsureMatchesMigration 钉住
-- （down → 清 bookkeeping → up → 要求 viewdef 逐字节回到 down 前那一份）。

COMMIT;
