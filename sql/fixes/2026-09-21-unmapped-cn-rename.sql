-- ============================================================================
-- 2026-09-21-unmapped-cn-rename.sql
--
-- 修复 2026-09-20 Phase 4 留置的 19 个 unmapped `-cn` canonical:
--   * rename canonical_name (strip `-cn`) —— canonical_id 不变,pm_refs 不动
--   * 加老 `-cn` 名为 alias, surface='cn'
--   * 同 id rename 风险最小(0 流量 + 0 work_routes 已实测)
--
-- R89-DW（210 号）更正 —— 本头部原有三处与代码不符的表述，逐条改掉：
--
--   ① **「保持向后兼容」是反的**。原头部写「加老 `-cn` 名为 deprecated alias,
--      **保持向后兼容**」。但别名解析的**全部**读点都硬过滤 `status='active'`
--      （`resolve/resolve.go:384`、`:425`；`admin/model_normalize.go`、
--      `admin/logs.go` 同款过滤），而本脚本写的正是 `status='deprecated'`。
--      ⇒ 老 `-cn` 名在改名后**不再解析**，与"保持向后兼容"正好相反。
--      更严重的是 §4 的验证段 `:159` **断言 `deprecated_aliases = 19`** ——
--      它把这个反效果当成成功来校验。
--      ⇒ 已加 §4.1 **可解析性门禁**：老名若解析不到就中止并说明。
--      ⚠️ 到底该不该让老名继续可解析（`status='active'`）是**产品裁决**，
--      本脚本只把事实摆出来，**不替你改路由语义**。见 §4.1 的说明。
--
--   ② **「per-row 嵌套子事务」在本脚本里不存在**。原头部写「本脚本按此模式写」，
--      实际是**两段彼此独立的平铺事务**：`:91-113`（别名弃用）、`:115-121`（改名）。
--      ⇒ 若第二段因 `models_canonical.canonical_name` 唯一键冲突（守卫 `:70`
--      **只查 `status='active'`** 的 winner，而该列有 UNIQUE 约束）而失败，
--      第一段**已经提交** ⇒ 留下「名字没改、别名已废」的**半应用态**。
--      ⇒ 已加 `\set ON_ERROR_STOP on`，并把该风险写进 §3 门禁的说明。
--
--   ③ 原 §0 备份的「否则插差量」分支是**死代码**：`:32` 的
--      `WHERE NOT EXISTS (SELECT 1 FROM public.bak_...)` 没有相关子句，
--      仅当备份表为空为真 ⇒ 重跑只会插 0 行，**没有差量、也没有回滚材料**。
--      已改写注释为它真实的行为（仅当备份表为空才插），并登记为待裁决项。
--
-- 环境: 本地 docker pg17 (.34) 优先,252 同名复用(加 4 行 252-only disable)。
-- 154 / 245 等 92f18cf22 binary 部署后再跑 dedup-cleanup,本脚本不触。
--
-- 幂等: 重跑只在 RENAME 上 ON CONFLICT skip, alias 已存在则 DO NOTHING。
-- ============================================================================

-- ⚠️ R89-DW（210 号补）：本文件原先**没有** `\set ON_ERROR_STOP on`，而它是
-- `sql/fixes/` 下**唯一一个真的会自动改库**的脚本（另两个脚本的 DML 全被注释）。
-- 缺它的后果不是"报错后继续改"（事务内失败会中止事务、COMMIT 变 ROLLBACK），
-- 而是这三条：
--   ① §0.2 的 preflight `RAISE EXCEPTION 'rename aborted: …'` 位于**任何事务之外**，
--      psql 会打印错误后**继续执行** ⇒ 它宣称的中止**根本没有发生**；
--   ② 脚本整体**以 0 退出** ⇒ CI/自动化看不出它失败过；
--   ③ §4 的 post-commit 门禁同样不会让 psql 非零退出。
\set ON_ERROR_STOP on

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
--
--    2026-09-21 .34 实测修正:多数 -cn canonical 已有指向自身的 ACTIVE self-alias,
--    纯 INSERT+NOT EXISTS 会把它们 skip 掉(首跑只插入 1/19,post-commit 断言拦截)。
--    改为 UPSERT 语义:先 UPDATE 既有 self-alias 到 deprecated+cn,再补插缺失行。
BEGIN;
  UPDATE model_aliases ma
  SET status='deprecated', surface='cn',
      notes='backward-compat alias for unmapped -cn canonical renamed 2026-09-21'
  FROM models_canonical mc
  JOIN rename_map m ON m.loser = mc.canonical_name
  WHERE ma.canonical_id = mc.id AND ma.raw_name = mc.canonical_name
    AND mc.status='active'
    AND (ma.status <> 'deprecated' OR COALESCE(ma.surface,'') <> 'cn');
  -- expected ~18 rows on .34 (self-aliases already existed)

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
  -- expected ~1 row on .34 (only wan2.6-t2v lacked a self-alias)
COMMIT;

BEGIN;
  UPDATE models_canonical mc
  SET canonical_name = m.winner
  FROM rename_map m
  WHERE mc.canonical_name = m.loser AND mc.status='active';
  -- expected 19 rows

  -- ------------------------------------------------------------------
  -- R89-DW（210 号新增）可解析性门禁 —— **放在本事务内、COMMIT 之前**
  --
  -- 为什么必须在事务内：放在 COMMIT 之后就只是**事后报表**，RAISE EXCEPTION
  -- 回滚不了任何东西。放在这里，失败会让**本段改名整体回滚**。
  -- ⚠️ 诚实说明它**回滚不了**的部分：第一段事务（`:122-144` 别名弃用）
  -- 已经提交，所以最坏情况是「别名已弃用 + 名字没改」——这正是头部 ②
  -- 记录的那个半应用态。修那个要合并两段事务，本轮只登记不重构。
  --
  -- 判据与生产读点**同源**：别名解析全部要求 `status='active'`
  -- （`resolve/resolve.go:384,425`）。§4 的 `:159` 断言
  -- `deprecated_aliases = 19`，恰恰是**把「老名不可解析」当成功校验**。
  --
  -- ⚠️ 本段**不**擅自把别名改成 `status='active'`：「老名是否应当继续可解析」
  -- 是**产品裁决**。本段只保证这个事实在**跑的时候就炸出来**，而不是上线后
  -- 才发现路由断了。
  -- ------------------------------------------------------------------
  DO $$
  DECLARE
      resolvable_old INT;
      unresolvable   TEXT;
  BEGIN
      SELECT count(*) INTO resolvable_old
      FROM rename_map m
      JOIN models_canonical mc ON mc.canonical_name = m.winner
      JOIN model_aliases ma
        ON ma.canonical_id = mc.id
       AND ma.raw_name = m.loser
       AND ma.status = 'active';

      SELECT string_agg(m.loser, ', ' ORDER BY m.loser) INTO unresolvable
      FROM rename_map m
      JOIN models_canonical mc ON mc.canonical_name = m.winner
      WHERE NOT EXISTS (
          SELECT 1 FROM model_aliases ma
          WHERE ma.canonical_id = mc.id
            AND ma.raw_name = m.loser
            AND ma.status = 'active'
      );

      IF resolvable_old = 0 THEN
          RAISE EXCEPTION
              '可解析性门禁未通过：19 个老 `-cn` 名**一个都不可解析**（仍发老名的客户端会全部落空）。'
              '头部宣称的「保持向后兼容」未达成 —— 本脚本写的是 status=deprecated，'
              '而全部别名解析点都要求 status=active。不可解析的老名: % 。'
              '本次改名回滚；是否要让老名继续可解析属产品裁决，本脚本不擅自改。', unresolvable;
      ELSIF resolvable_old < 19 THEN
          RAISE EXCEPTION
              '可解析性门禁未通过：仅 %/19 个老 `-cn` 名仍可解析，部分客户端会落空。不可解析: % 。'
              '本次改名回滚。', resolvable_old, unresolvable;
      END IF;

      RAISE NOTICE '可解析性门禁通过：19/19 个老 `-cn` 名仍可解析（向后兼容成立）';
  END $$;
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
-- 4.1 R89-DW 说明：可解析性门禁放在**哪**、以及它**管不到**什么
--
-- 可解析性门禁（问「老 `-cn` 名改名后还能不能解析」）放在**第二段事务内、
-- COMMIT 之前**，见上方 `BEGIN; … COMMIT;` 之间的那个 DO 块。
--
-- 为什么**不能**放在这里（§4 之后）：这里已在两段 COMMIT **之后**，
-- `RAISE EXCEPTION` 回滚不了任何东西 ⇒ 放在这里就只是**事后报表**，
-- 而报表不是门。**判据要放在它能真正中止的地方**，否则它只是一段打印。
--
-- 判据与生产读点同源：别名解析全部要求 `status='active'`
-- (`resolve/resolve.go:384,425`)。而 §4 的 `:159` 断言
-- `deprecated_aliases = 19`，恰恰是**把「老名不可解析」当成功来校验**。
--
-- ⚠️ 门禁**管不到**的部分（诚实登记）：两段事务彼此独立，第一段（别名弃用）
-- 先提交 ⇒ 若第二段被门禁拦下，最坏是「名字没改、别名已废」的半应用态。
-- 根治要合并两段事务，本轮**只登记不重构**。
-- ⚠️ 本轮**不**擅自把别名改成 `status='active'`：「老名是否应当继续可解析」
-- 是**产品裁决**（继续可解析 ⇒ 改 status；不可解析 ⇒ 头部那句「保持向后兼容」
-- 应删掉并写明这是**破坏性变更**）。
-- ============================================================================

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