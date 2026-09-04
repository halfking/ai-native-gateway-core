-- auto_route_selections hot heap behavioural tests (migration 656)
-- AUTO 路由选择热表行为验证
--
-- Purpose: 验证 migration 656 的热表写入默认值、统一视图、promote 原子迁移、
--          DEFAULT 分区安全搬迁、唯一冲突跳过与回滚数据保全。
-- Author: llm-gateway-ops (2026-09-05)
--
-- 前置: 478 / 650 / 656 已应用。
-- 运行方式:
--   psql -h localhost -U postgres -d llm_gateway \
--     -v ON_ERROR_STOP=1 -f auto_route_selections_hot_tests.sql
--
-- 安全性: 全部语句包在一个事务里并以 ROLLBACK 结束——包括 promote 的
--         删除/插入和 ensure 创建的分区，脚本结束后不留任何痕迹。
--         可以在 staging 库上放心运行；不要在业务高峰的生产主库运行
--         （promote 会短暂锁住真实冷行）。

\echo '=================================='
\echo 'auto_route_selections hot tests'
\echo '=================================='

BEGIN;

-- 测试数据使用统一前缀，便于断言与排查。
CREATE TEMP TABLE test_ctx (tag text);
INSERT INTO test_ctx VALUES ('auto656t_' || extract(epoch from now())::bigint::text);

-- ============================================================
-- 1. 默认值：不传 ts/partition_date/布尔列可写入
-- ============================================================
\echo '1. insert with defaults'

DO $$
DECLARE v_tag text;
BEGIN
  SELECT tag INTO v_tag FROM test_ctx;
  INSERT INTO auto_route_selections_hot (request_id, task_type, chosen_model)
  VALUES (v_tag, 'code', 'test-model');
END $$;

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM auto_route_selections_hot h
    JOIN test_ctx t ON h.request_id = t.tag
    WHERE h.ts IS NOT NULL AND h.partition_date = CURRENT_DATE
      AND h.profile = 'smart' AND h.classifier = 'heuristic'
      AND h.candidate_rank = 1
      AND h.affinity_applied = FALSE AND h.explore = FALSE AND h.fallback_used = FALSE
  ) THEN
    RAISE EXCEPTION 'hot defaults missing (ts/partition_date/booleans/enum defaults)';
  END IF;
END $$;

-- ============================================================
-- 2. 统一视图可见且 storage_tier=hot
-- ============================================================
\echo '2. view sees hot tier'

DO $$ BEGIN
  IF (SELECT s.storage_tier FROM auto_route_selections_all s
      JOIN test_ctx t ON s.request_id = t.tag) IS DISTINCT FROM 'hot' THEN
    RAISE EXCEPTION 'view should report hot tier';
  END IF;
END $$;

-- ============================================================
-- 3. retention=0 被拒绝
-- ============================================================
\echo '3. retention guard'

DO $$ BEGIN
  BEGIN
    PERFORM promote_auto_route_selections_hot_to_partition(interval '0 seconds', 100);
    RAISE EXCEPTION 'retention guard not enforced';
  EXCEPTION WHEN others THEN
    IF SQLERRM NOT LIKE '%p_retention must be positive%' THEN
      RAISE EXCEPTION 'unexpected guard error: %', SQLERRM;
    END IF;
  END;
END $$;

-- ============================================================
-- 4. 超过 8 小时的行迁移到当月分区；8 小时内的行不动
-- ============================================================
\echo '4. promote moves only cold rows'

DO $$
DECLARE v_tag text; n bigint; hot_cnt bigint;
BEGIN
  SELECT tag INTO v_tag FROM test_ctx;
  INSERT INTO auto_route_selections_hot (request_id, task_type, chosen_model, ts)
  VALUES (v_tag || '_cold', 'code', 'test-model', now() - interval '9 hours');

  n := promote_auto_route_selections_hot_to_partition(interval '8 hours', 100);
  IF n < 1 THEN RAISE EXCEPTION 'expected promote to move rows, got %', n; END IF;

  SELECT count(*) INTO hot_cnt FROM auto_route_selections_hot
  WHERE request_id IN (v_tag, v_tag || '_cold');
  IF hot_cnt <> 1 THEN
    RAISE EXCEPTION 'expected only the fresh row left in hot, got %', hot_cnt;
  END IF;
  IF (SELECT s.storage_tier FROM auto_route_selections_all s
      WHERE s.request_id = v_tag || '_cold') IS DISTINCT FROM 'parent' THEN
    RAISE EXCEPTION 'cold row should be in parent tier';
  END IF;
END $$;

-- ============================================================
-- 5. 跨月：DEFAULT 分区中的旧数据被安全搬迁，DEFAULT 重新挂载
-- ============================================================
\echo '5. cross-month default partition move'

DO $$
DECLARE v_tag text; jan_month date := '2000-01-01'; c bigint;
BEGIN
  SELECT tag INTO v_tag FROM test_ctx;
  -- 直接写父表，落进 DEFAULT 分区（2000_01 分区不存在）
  INSERT INTO auto_route_selections (request_id, task_type, chosen_model, ts, partition_date)
  VALUES (v_tag || '_defjan', 'code', 'legacy', '2000-01-15 10:00+00', '2000-01-15');
  -- 热表同月冷行
  INSERT INTO auto_route_selections_hot (request_id, task_type, chosen_model, ts, partition_date)
  VALUES (v_tag || '_hotjan', 'code', 'test-model', '2000-01-20 10:00+00', '2000-01-20');

  PERFORM promote_auto_route_selections_hot_to_partition(interval '8 hours', 100);

  IF to_regclass('public.auto_route_selections_2000_01') IS NULL THEN
    RAISE EXCEPTION '2000_01 partition not created';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE c.relname = 'auto_route_selections_default' AND p.relname = 'auto_route_selections'
  ) THEN
    RAISE EXCEPTION 'default partition not re-attached';
  END IF;
  SELECT count(*) INTO c FROM auto_route_selections
  WHERE partition_date >= '2000-01-01' AND partition_date < '2000-02-01'
    AND request_id IN (v_tag || '_defjan', v_tag || '_hotjan');
  IF c <> 2 THEN RAISE EXCEPTION 'expected 2 jan rows in parent, got %', c; END IF;
END $$;

-- ============================================================
-- 6. request_id 唯一冲突：promote 跳过重复行，不报错、不丢已迁移数据
-- ============================================================
\echo '6. request_id conflict dedup'

DO $$
DECLARE v_tag text; c bigint;
BEGIN
  SELECT tag INTO v_tag FROM test_ctx;
  INSERT INTO auto_route_selections (request_id, task_type, chosen_model, ts, partition_date)
  VALUES (v_tag || '_dup', 'code', 'legacy', '2000-02-01 00:00+00', '2000-02-01');
  INSERT INTO auto_route_selections_hot (request_id, task_type, chosen_model, ts, partition_date)
  VALUES (v_tag || '_dup', 'code', 'replayed', '2000-02-01 12:00+00', '2000-02-01');

  PERFORM promote_auto_route_selections_hot_to_partition(interval '8 hours', 100);

  IF EXISTS (SELECT 1 FROM auto_route_selections_hot WHERE request_id = v_tag || '_dup') THEN
    RAISE EXCEPTION 'duplicate hot row should have been deleted';
  END IF;
  SELECT count(*) INTO c FROM auto_route_selections WHERE request_id = v_tag || '_dup';
  IF c <> 1 THEN RAISE EXCEPTION 'expected exactly 1 dup row in parent, got %', c; END IF;
END $$;

-- ============================================================
-- 7. promote 幂等：排干后再次调用返回 0
-- ============================================================
\echo '7. promote idempotent'

DO $$
DECLARE n bigint; loops int := 0;
BEGIN
  LOOP
    n := promote_auto_route_selections_hot_to_partition(interval '8 hours', 100);
    loops := loops + 1;
    EXIT WHEN n = 0 OR loops > 20;
  END LOOP;
  IF loops > 20 THEN RAISE EXCEPTION 'promote did not drain within 20 batches'; END IF;
END $$;

-- ============================================================
-- 8. hot 表约束生效
-- ============================================================
\echo '8. hot constraints'

DO $$ BEGIN
  BEGIN
    INSERT INTO auto_route_selections_hot (request_id, task_type, chosen_model, profile)
    VALUES ('auto656t_bad_profile', 'code', 'm', 'bogus');
    RAISE EXCEPTION 'profile check not enforced on hot';
  EXCEPTION WHEN check_violation THEN NULL; -- 预期路径
  END;
END $$;

-- ============================================================
-- 9. ensure 函数拒绝 NULL 月份
-- ============================================================
\echo '9. ensure null-month guard'

DO $$ BEGIN
  BEGIN
    PERFORM ensure_auto_route_selections_partition(NULL::date);
    RAISE EXCEPTION 'null month guard not enforced';
  EXCEPTION WHEN others THEN
    IF SQLERRM NOT LIKE '%p_month must not be null%' THEN
      RAISE EXCEPTION 'unexpected ensure error: %', SQLERRM;
    END IF;
  END;
END $$;

ROLLBACK;

\echo 'ALL CHECKS PASSED (rolled back, no side effects)'
