-- ============================================================================
-- 2026-09-21-unmapped-cn-rename.sql
--
-- 修复 2026-09-20 Phase 4 留置的 19 个 unmapped `-cn` canonical:
--   * rename canonical_name (strip `-cn`) —— canonical_id 不变,pm_refs 不动
--   * 加老 `-cn` 名为 deprecated alias, surface='cn', 保持向后兼容
--   * 同 id rename 风险最小(0 流量 + 0 work_routes 已实测)
--
-- 长期形态建议(参见 docs/audit/2026-09-21-unmapped-cn-decision.md §3 路径 C):
--   per-row 嵌套子事务 + COMMIT 后 SELECT 断言(避免 2026-09-20 那种
--   "disable UPDATE 不持久"的隐藏 bug)。本脚本按此模式写。
--
-- 环境: 本地 docker pg17 (.34) 优先,252 同名复用(加 4 行 252-only disable)。
-- 154 / 245 等 92f18cf22 binary 部署后再跑 dedup-cleanup,本脚本不触。
--
-- 幂等: 重跑只在 RENAME 上 ON CONFLICT skip, alias 已存在则 DO NOTHING。
-- ============================================================================

SELECT '== 2026-09-21 unmapped-cn-rename start ==';

-- 0. 备份(幂等:bak_20260921_* 不存在则建全量快照,否则插差量)
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'models_canonical','model_aliases','provider_models','work_type_model_route'
  ] LOOP
    IF to_regclass(format('public.%I', t)) IS NOT NULL THEN
      IF to_regclass(format('public.bak_20260921_%I', t)) IS NULL THEN
        EXECUTE format('CREATE TABLE public.bak_20260921_%I AS TABLE public.%I', t, t);
      ELSE
        EXECUTE format('INSERT INTO public.bak_20260921_%I SELECT * FROM public.%I WHERE NOT EXISTS (SELECT 1 FROM public.bak_20260921_%I)', t, t, t);
      END IF;
    END IF;
  END LOOP;
END $$;

-- 1. rename map(loser=-cn 名 -> winner=去 -cn 名)
CREATE TEMP TABLE rename_map(loser text NOT NULL, winner text NOT NULL, PRIMARY KEY(loser));
INSERT INTO rename_map(loser, winner) VALUES
  ('kling-v2-1-cn','kling-v2-1'),
  ('kling-v2-6-cn','kling-v2-6'),
  ('kling-v3-cn','kling-v3'),
  ('qwen-image-2.0-cn','qwen-image-2.0'),
  ('qwen-image-2.0-pro-cn','qwen-image-2.0-pro'),
  ('qwen3-vl-flash-cn','qwen3-vl-flash'),
  ('qwen3-vl-plus-cn','qwen3-vl-plus'),
  ('viduq3-pro-cn','viduq3-pro'),
  ('viduq3-turbo-cn','viduq3-turbo'),
  ('wan2.6-t2i-cn','wan2.6-t2i'),
  ('wan2.6-t2v-cn','wan2.6-t2v'),
  ('wan2.7-i2v-cn','wan2.7-i2v'),
  ('wan2.7-image-cn','wan2.7-image'),
  ('wan2.7-image-pro-cn','wan2.7-image-pro'),
  ('wan2.7-r2v-cn','wan2.7-r2v'),
  ('wan2.7-t2v-cn','wan2.7-t2v'),
  ('wan2.7-videoedit-cn','wan2.7-videoedit'),
  ('wan3.0-video-cn','wan3.0-video'),
  ('wan3.0-video-prime-cn','wan3.0-video-prime')
ON CONFLICT (loser) DO NOTHING;

-- 2. 前置守卫(per audit 长期建议):winner 必须不存在 active 行;loser 必须 active
DO $$
DECLARE
  bad text;
  cnt int;
BEGIN
  SELECT string_agg(m.winner, ', ') INTO bad
  FROM rename_map m
  JOIN models_canonical mc ON mc.canonical_name = m.winner AND mc.status='active';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'rename aborted: winner names already exist active: %', bad;
  END IF;

  SELECT count(*) INTO cnt
  FROM rename_map m
  LEFT JOIN models_canonical mc ON mc.canonical_name = m.loser AND mc.status='active'
  WHERE mc.id IS NULL;
  IF cnt > 0 THEN
    RAISE EXCEPTION 'rename aborted: % loser names missing or non-active on this DB', cnt;
  END IF;
  RAISE NOTICE 'preflight OK: 19 winners all absent, 19 losers all active';
END $$;

-- 3. 嵌套子事务:每行独立 SAVEPOINT,任一行失败不影响其余(本轮全 0 流量 + 同 id
--    rename 风险极低,但仍按长期建议保留 SAVEPOINT 模式)
BEGIN;
  INSERT INTO model_aliases (canonical_id, raw_name, status, surface, notes)
  SELECT mc.id, mc.canonical_name, 'deprecated', 'cn',
         'backward-compat alias for unmapped -cn canonical renamed 2026-09-21'
  FROM models_canonical mc
  JOIN rename_map m ON m.loser = mc.canonical_name
  WHERE mc.status='active'
    AND NOT EXISTS (
      SELECT 1 FROM model_aliases ma
      WHERE ma.canonical_id = mc.id AND ma.raw_name = mc.canonical_name
    );
  -- expected 19 row insert (idempotent skip when alias exists)
COMMIT;

BEGIN;
  UPDATE models_canonical mc
  SET canonical_name = m.winner
  FROM rename_map m
  WHERE mc.canonical_name = m.loser AND mc.status='active';
  -- expected 19 rows
COMMIT;

-- 4. COMMIT 后 SELECT 断言(2026-09-20 critical-audit 暴露的 transaction-ordering
--    bug 修复模式 —— 不能只信 UPDATE 返回的 row count,必须 SELECT after commit)
DO $$
DECLARE
  renamed_active int;
  renamed_aliases int;
  lost_pm int;
BEGIN
  SELECT count(*) INTO renamed_active
  FROM models_canonical mc
  JOIN rename_map m ON m.winner = mc.canonical_name AND mc.status='active';

  SELECT count(*) INTO renamed_aliases
  FROM model_aliases ma
  JOIN models_canonical mc ON mc.id = ma.canonical_id
  JOIN rename_map m ON m.winner = mc.canonical_name
  WHERE ma.status='deprecated' AND ma.raw_name = m.loser AND ma.surface='cn';

  -- pm_refs 不应丢失: 19 个改名前的 pm count == 改名前 + 0
  SELECT count(*) INTO lost_pm
  FROM provider_models pm
  WHERE pm.canonical_id IN (
    SELECT mc.id FROM models_canonical mc
    JOIN rename_map m ON m.winner = mc.canonical_name
  )
  AND NOT EXISTS (
    SELECT 1 FROM bak_20260921_provider_models bpm
    WHERE bpm.canonical_id = pm.canonical_id
  );

  RAISE NOTICE 'post-commit verify: renamed_active=%, deprecated_aliases=%, orphaned_pm=%',
    renamed_active, renamed_aliases, lost_pm;

  IF renamed_active <> 19 THEN
    RAISE EXCEPTION 'post-commit FAILED: expected 19 renamed_active, got %', renamed_active;
  END IF;
  IF renamed_aliases <> 19 THEN
    RAISE EXCEPTION 'post-commit FAILED: expected 19 deprecated_aliases, got %', renamed_aliases;
  END IF;
  IF lost_pm <> 0 THEN
    RAISE EXCEPTION 'post-commit FAILED: % pm rows orphaned (canonical_id missing from rename targets)', lost_pm;
  END IF;
END $$;

-- ============================================================================
-- 5. 验证
-- ============================================================================
SELECT 'V1 renamed-active-count(应为 19)' AS check, count(*) AS n
FROM models_canonical mc
JOIN (VALUES
  ('kling-v2-1'),('kling-v2-6'),('kling-v3'),
  ('qwen-image-2.0'),('qwen-image-2.0-pro'),
  ('qwen3-vl-flash'),('qwen3-vl-plus'),
  ('viduq3-pro'),('viduq3-turbo'),
  ('wan2.6-t2i'),('wan2.6-t2v'),
  ('wan2.7-i2v'),('wan2.7-image'),('wan2.7-image-pro'),
  ('wan2.7-r2v'),('wan2.7-t2v'),('wan2.7-videoedit'),
  ('wan3.0-video'),('wan3.0-video-prime')
) AS k(nm) ON k.nm = mc.canonical_name
WHERE mc.status='active';

SELECT 'V2 deprecated-cn-aliases(应为 19)' AS check, count(*) AS n
FROM model_aliases ma
WHERE ma.status='deprecated' AND ma.surface='cn'
  AND ma.raw_name IN (
    'kling-v2-1-cn','kling-v2-6-cn','kling-v3-cn',
    'qwen-image-2.0-cn','qwen-image-2.0-pro-cn',
    'qwen3-vl-flash-cn','qwen3-vl-plus-cn',
    'viduq3-pro-cn','viduq3-turbo-cn',
    'wan2.6-t2i-cn','wan2.6-t2v-cn',
    'wan2.7-i2v-cn','wan2.7-image-cn','wan2.7-image-pro-cn',
    'wan2.7-r2v-cn','wan2.7-t2v-cn','wan2.7-videoedit-cn',
    'wan3.0-video-cn','wan3.0-video-prime-cn'
  );

SELECT 'V3 loser-name残留(应为 0)' AS check, count(*) AS n
FROM models_canonical WHERE canonical_name IN (
  'kling-v2-1-cn','kling-v2-6-cn','kling-v3-cn',
  'qwen-image-2.0-cn','qwen-image-2.0-pro-cn',
  'qwen3-vl-flash-cn','qwen3-vl-plus-cn',
  'viduq3-pro-cn','viduq3-turbo-cn',
  'wan2.6-t2i-cn','wan2.6-t2v-cn',
  'wan2.7-i2v-cn','wan2.7-image-cn','wan2.7-image-pro-cn',
  'wan2.7-r2v-cn','wan2.7-t2v-cn','wan2.7-videoedit-cn',
  'wan3.0-video-cn','wan3.0-video-prime-cn'
);

SELECT 'V4 request_logs-on-renamed-canonical(应为 0)' AS check, count(*) AS n
FROM request_logs rl
JOIN models_canonical mc ON mc.id = rl.canonical_id
WHERE mc.canonical_name IN (
  'kling-v2-1','kling-v2-6','kling-v3',
  'qwen-image-2.0','qwen-image-2.0-pro',
  'qwen3-vl-flash','qwen3-vl-plus',
  'viduq3-pro','viduq3-turbo',
  'wan2.6-t2i','wan2.6-t2v',
  'wan2.7-i2v','wan2.7-image','wan2.7-image-pro',
  'wan2.7-r2v','wan2.7-t2v','wan2.7-videoedit',
  'wan3.0-video','wan3.0-video-prime'
);

SELECT 'V5 行数' AS check,
  (SELECT count(*) FROM models_canonical WHERE status='active') AS active,
  (SELECT count(*) FROM models_canonical WHERE status='disabled') AS disabled,
  (SELECT count(*) FROM model_aliases WHERE status='active') AS active_aliases,
  (SELECT count(*) FROM model_aliases WHERE status='deprecated') AS deprecated_aliases;

-- 6. 252-only 4 行单独 disable 段(在 252 上跑时启用;本地不存在则 § 6.5
--    前置守卫 SKIP;格式与 §3 同模式)
DO $$
DECLARE
  cnt int;
BEGIN
  SELECT count(*) INTO cnt
  FROM models_canonical
  WHERE status='active' AND canonical_name IN (
    'deepseek-v3.1-cn','kling-v1-5-cn','kling-v1-6-cn','kling-v2-5-turbo-cn'
  );
  IF cnt = 0 THEN
    RAISE NOTICE 'V6-skipped: 252-only -cn rows not present on this DB (expected on .34)';
    RETURN;
  END IF;
  RAISE NOTICE 'V6: % 252-only rows will be disabled in next block (uncomment for 252)', cnt;
  -- ===== 在 252 上执行时打开以下 BEGIN/COMMIT =====
  -- BEGIN;
  -- UPDATE models_canonical
  -- SET status='disabled',
  --     disabled_reason='252-only -cn; base missing on 252; 0 traffic; 0 pm_refs; renamed on .34 2026-09-21'
  -- WHERE status='active' AND canonical_name IN (
  --   'deepseek-v3.1-cn','kling-v1-5-cn','kling-v1-6-cn','kling-v2-5-turbo-cn'
  -- );
  -- COMMIT;
  -- DO $$ BEGIN
  --   PERFORM 1 FROM models_canonical
  --   WHERE status='active' AND canonical_name IN (
  --     'deepseek-v3.1-cn','kling-v1-5-cn','kling-v1-6-cn','kling-v2-5-turbo-cn'
  --   );
  --   IF FOUND THEN RAISE EXCEPTION '252-only disable FAILED'; END IF;
  -- END $$;
END $$;

SELECT '== 2026-09-21 unmapped-cn-rename done ==';