-- ============================================================================
-- cleanup-canonical.sql  (2026-09-20)
-- 模型标准名称(models_canonical)去重与垃圾清理 —— .34 pg17 与 252 通用
-- 事务化 + 幂等(重跑只影响未处理行)。历史日志表(request_logs/session_turns/
-- usage_ledger/auto_route_selections/route_decisions/stats_*)一律不动,只重定向
-- live 引用表。
-- ============================================================================

SELECT '== cleanup-canonical 2026-09-20 start ==';

-- 0. 备份(首次运行建表并留全量快照;重跑不再覆盖)
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'models_canonical','model_aliases','provider_models','work_type_model_route',
    'model_offers','local_models','model_task_index','task_model_affinity',
    'model_fingerprints','model_iq_runs','provider_scores','api_key_model_cost',
    'model_credit_rates','sticky_sessions','pricing_plans','prompt_injection_llm_engines',
    'credential_model_index_hot','credential_model_index_2026_10',
    'tenant_model_policies','ursm_node_snapshot_min',
    -- R49 审计（2026-09-20）补登记：以下三表对 models_canonical 是
    -- ON DELETE CASCADE 外键（342 迁移），DELETE loser 会无声级联删数据
    -- 且无备份可回滚 —— 必须先备份、重定向后再删。
    'model_capability_profiles','model_capability_profile_audit','model_substitution_overrides',
    -- R50 审计（2026-09-21）补登记：§2.3 会重定向但此前不进备份的两张表
    -- ——重定向写坏时无备份可回滚，违背本脚本"必须先备份"自身原则。
    'role_task_llm_mapping','tenant_model_policies_active'
  ]
  LOOP
    IF to_regclass(format('public.%I', t)) IS NOT NULL THEN
      IF to_regclass(format('public.bak_20260920_%I', t)) IS NULL THEN
        EXECUTE format('CREATE TABLE public.bak_20260920_%I AS TABLE public.%I', t, t);
      ELSE
        EXECUTE format('INSERT INTO public.bak_20260920_%I SELECT * FROM public.%I WHERE NOT EXISTS (SELECT 1 FROM public.bak_20260920_%I)', t, t, t);
      END IF;
    END IF;
  END LOOP;
END $$;

BEGIN;

-- 1. 归并映射(loser_name -> winner_name)
CREATE TEMP TABLE merge_map(loser text NOT NULL, winner text NOT NULL, PRIMARY KEY(loser));
INSERT INTO merge_map (loser, winner) VALUES
  -- 23 组 dot/dash 双写(保留侧: 活跃引用多/官方拼写/路由在用)
  ('claude-haiku-4.5','claude-haiku-4-5'),
  ('claude-opus-4.5','claude-opus-4-5'),
  ('claude-opus-4.6','claude-opus-4-6'),
  ('claude-opus-4.7','claude-opus-4-7'),
  ('claude-opus-4.8','claude-opus-4-8'),
  ('claude-sonnet-4.5','claude-sonnet-4-5'),
  ('claude-sonnet-4.6','claude-sonnet-4-6'),
  ('claude-sonnet-4.8','claude-sonnet-4-8'),
  ('deepseek-v3.1','deepseek-v3-1'),
  ('deepseek-v3.1-terminus','deepseek-v3-1-terminus'),
  ('deepseek-v3-2','deepseek-v3.2'),
  ('deepseek-v4-1-flash','deepseek-v4.1-flash'),
  ('doubao-1.5-ui-tars','doubao-1-5-ui-tars'),
  ('doubao-seed-2-0-code','doubao-seed-2.0-code'),
  ('doubao-seed-2.0-lite','doubao-seed-2-0-lite'),
  ('doubao-seed-2.0-mini','doubao-seed-2-0-mini'),
  ('doubao-seed-2.0-pro','doubao-seed-2-0-pro'),
  ('glm-4-5-air','glm-4.5-air'),
  ('glm-4-7','glm-4.7'),
  ('glm-4.9b-chat','glm-4-9b-chat'),
  ('glm-5-2','glm-5.2'),
  ('glm-5-3-flash','glm-5.3-flash'),
  ('qwen3-5-122b-a10b','qwen3.5-122b-a10b'),
  -- 截断垃圾名 -> 全名
  ('opus-5','claude-opus-5'),
  ('sonnet-5','claude-sonnet-5'),
  ('v4_flash','deepseek-v4-flash'),
  ('v4-flash','deepseek-v4-flash'),
  ('v4-pro','deepseek-v4-pro'),
  ('3.1-pro','gemini-3.1-pro'),
  ('3.8-flash','gemini-3.8-flash'),
  ('k3','kimi-k3'),
  ('5.6-luna','gpt-5.6-luna'),
  ('5.6-sol','gpt-5.6-sol'),
  ('5.6-terra','gpt-5.6-terra'),
  ('6-astra','gpt-6-astra'),
  ('5.3','glm-5.3'),
  ('5.3-flash','glm-5.3-flash'),
  ('4.6','grok-4.6'),
  ('seed-2.0-code','doubao-seed-2.0-code'),
  ('seed-2.0-lite','doubao-seed-2-0-lite'),
  ('seed-2.0-mini','doubao-seed-2-0-mini'),
  ('seed-2-1-turbo','doubao-seed-2-1-turbo'),
  ('mistral-nemo','open-mistral-nemo'),
  -- 供应商全限定名(Bedrock 风格)
  ('global.anthropic.claude-sonnet-4-5-20250929-v1:0','claude-sonnet-4-5')
ON CONFLICT (loser) DO NOTHING;

-- :batch / :free / :thinking 变体 -> 基名(基名已存在; 若基名本身是被归并的
-- 败者拼写, 则解析到其胜者; 不含 4 个待改名行)
WITH cand AS (
  SELECT mc.canonical_name AS loser,
         COALESCE((SELECT s.winner FROM merge_map s WHERE s.loser = split_part(mc.canonical_name, ':', 1)),
                  split_part(mc.canonical_name, ':', 1)) AS winner
  FROM models_canonical mc
  WHERE mc.canonical_name ~ ':'
    AND mc.canonical_name NOT IN ('dots-3-note-preview:free','lfm-2.5-2.6b:free','north-mini-code:free','qwen-plus-2025-07-28:thinking','global.anthropic.claude-sonnet-4-5-20250929-v1:0')
)
INSERT INTO merge_map (loser, winner)
SELECT DISTINCT c.loser, c.winner FROM cand c
WHERE EXISTS (SELECT 1 FROM models_canonical b WHERE b.canonical_name = c.winner)
ON CONFLICT (loser) DO NOTHING;

-- 前置断言: 所有 winner 都必须存在且自身不在 loser 列表
DO $$
DECLARE bad text;
BEGIN
  SELECT string_agg(m.winner, ', ') INTO bad
  FROM merge_map m
  WHERE NOT EXISTS (SELECT 1 FROM models_canonical mc WHERE mc.canonical_name = m.winner)
     OR m.winner IN (SELECT loser FROM merge_map);
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'merge_map winner missing or self-referential: %', bad;
  END IF;
END $$;

-- 2. live 引用表重定向 canonical_id -> winner
-- 2.1 model_aliases: 先删与 winner 重复 raw_name 的 loser 别名,再重定向
DELETE FROM model_aliases a
USING merge_map m
WHERE a.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)
  AND EXISTS (SELECT 1 FROM model_aliases w
              WHERE w.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner)
                AND w.raw_name = a.raw_name);

-- '4.6' 残留歧义别名按 raw 前缀分流(grok% -> grok-4.6, glm% -> glm-4.6)
-- 先清与目标侧重复 raw_name 的行(UPDATE 不走 ON CONFLICT)
DELETE FROM model_aliases a
USING models_canonical j, model_aliases w, models_canonical g
WHERE j.canonical_name = '4.6'
  AND a.canonical_id = j.id
  AND g.canonical_name IN ('grok-4.6','glm-4.6')
  AND w.canonical_id = g.id
  AND w.raw_name = a.raw_name
  AND EXISTS (SELECT 1 FROM merge_map WHERE loser = '4.6');

UPDATE model_aliases a
SET canonical_id = g.id
FROM (VALUES ('grok%','grok-4.6'), ('glm%','glm-4.6')) AS v(pat, tgt)
JOIN models_canonical g ON g.canonical_name = v.tgt
JOIN models_canonical j ON j.canonical_name = '4.6'
WHERE a.canonical_id = j.id AND a.raw_name LIKE v.pat
  AND EXISTS (SELECT 1 FROM merge_map WHERE loser = '4.6');

UPDATE model_aliases a
SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner)
FROM merge_map m
WHERE a.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser);

-- 2.2 provider_models: '4.6'/'4-bit' 特例 + 通用重定向
UPDATE provider_models p
SET canonical_id = g.id
FROM (VALUES ('grok/4.6','grok-4.6'), ('glm-4.6','glm-4.6'), ('grok-4.6','grok-4.6')) AS v(raw, tgt)
JOIN models_canonical g ON g.canonical_name = v.tgt
JOIN models_canonical j ON j.canonical_name = '4.6'
WHERE p.canonical_id = j.id AND p.raw_model_name = v.raw
  AND EXISTS (SELECT 1 FROM merge_map WHERE loser = '4.6');

-- 4-bit: .34 上有一条本地 mlx 量化模型路径,重定向到其真实模型(存在则指认,否则置 NULL=未匹配)
UPDATE provider_models p
SET canonical_id = COALESCE((SELECT id FROM models_canonical WHERE canonical_name = 'qwen3.8-27b-uncensored'), NULL)
FROM models_canonical j
WHERE p.canonical_id = j.id AND j.canonical_name = '4-bit'
  AND p.raw_model_name LIKE '%qwen3.8-27b-uncensored%';

UPDATE provider_models p
SET canonical_id = NULL
FROM models_canonical j
WHERE p.canonical_id = j.id AND j.canonical_name = '4-bit';

UPDATE provider_models p
SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner),
    standardized_name = m.winner
FROM merge_map m
WHERE p.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser);

-- 2.3 其余 live 表(存在才更新)
DO $$
BEGIN
  IF to_regclass('public.model_offers') IS NOT NULL THEN
    EXECUTE 'UPDATE model_offers x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.local_models') IS NOT NULL THEN
    EXECUTE 'UPDATE local_models x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_task_index') IS NOT NULL THEN
    EXECUTE 'UPDATE model_task_index x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.task_model_affinity') IS NOT NULL THEN
    EXECUTE 'UPDATE task_model_affinity x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_fingerprints') IS NOT NULL THEN
    EXECUTE 'UPDATE model_fingerprints x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_iq_runs') IS NOT NULL THEN
    EXECUTE 'UPDATE model_iq_runs x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.provider_scores') IS NOT NULL THEN
    EXECUTE 'UPDATE provider_scores x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.api_key_model_cost') IS NOT NULL THEN
    EXECUTE 'UPDATE api_key_model_cost x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_credit_rates') IS NOT NULL THEN
    EXECUTE 'UPDATE model_credit_rates x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.sticky_sessions') IS NOT NULL THEN
    EXECUTE 'UPDATE sticky_sessions x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.pricing_plans') IS NOT NULL THEN
    EXECUTE 'UPDATE pricing_plans x SET model_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.model_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.prompt_injection_llm_engines') IS NOT NULL THEN
    EXECUTE 'UPDATE prompt_injection_llm_engines x SET model_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.model_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.credential_model_index_hot') IS NOT NULL THEN
    EXECUTE 'UPDATE credential_model_index_hot x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.credential_model_index_2026_10') IS NOT NULL THEN
    EXECUTE 'UPDATE credential_model_index_2026_10 x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.tenant_model_policies') IS NOT NULL THEN
    EXECUTE 'UPDATE tenant_model_policies t SET canonical_name = m.winner FROM merge_map m WHERE lower(t.canonical_name) = lower(m.loser)';
  END IF;
  IF to_regclass('public.tenant_model_policies_active') IS NOT NULL AND (SELECT relkind FROM pg_class WHERE oid = 'public.tenant_model_policies_active'::regclass) = 'r' THEN
    EXECUTE 'UPDATE tenant_model_policies_active t SET canonical_name = m.winner FROM merge_map m WHERE lower(t.canonical_name) = lower(m.loser)';
  END IF;
  IF to_regclass('public.ursm_node_snapshot_min') IS NOT NULL THEN
    EXECUTE 'UPDATE ursm_node_snapshot_min u SET canonical_name = m.winner FROM merge_map m WHERE lower(u.canonical_name) = lower(m.loser)';
  END IF;
  -- R49 审计（2026-09-20）：FK ON DELETE CASCADE 三表必须先重定向——
  -- 否则 §7 DELETE loser 时静默级联删掉能力画像/替换策略且无备份。
  IF to_regclass('public.model_capability_profiles') IS NOT NULL THEN
    EXECUTE 'UPDATE model_capability_profiles x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_capability_profile_audit') IS NOT NULL THEN
    EXECUTE 'UPDATE model_capability_profile_audit x SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  IF to_regclass('public.model_substitution_overrides') IS NOT NULL THEN
    EXECUTE 'UPDATE model_substitution_overrides x SET requested_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.requested_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
    EXECUTE 'UPDATE model_substitution_overrides x SET candidate_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner) FROM merge_map m WHERE x.candidate_canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.loser)';
  END IF;
  -- R48 新表（730）按名引用 canonical：后续 admin/API 写入 loser 拼写的行
  -- 会悬空（promoteFirstPresent 静默让位），一并重定向。
  IF to_regclass('public.role_task_llm_mapping') IS NOT NULL THEN
    EXECUTE 'UPDATE role_task_llm_mapping r SET llm_canonical_name = m.winner FROM merge_map m WHERE lower(r.llm_canonical_name) = lower(m.loser)';
  END IF;
END $$;

-- 2.4 work_type_model_route: 拼写切换 + 清除垃圾路由(4-bit)
UPDATE work_type_model_route w
SET canonical_name = m.winner
FROM merge_map m
WHERE lower(w.canonical_name) = lower(m.loser);

DELETE FROM work_type_model_route WHERE lower(canonical_name) IN ('4-bit','test','4_bit');

-- 3. 元数据回填: winner 缺失的字段从 loser 补
UPDATE models_canonical w
SET family         = COALESCE(NULLIF(w.family,''), l.family),
    display_name   = COALESCE(NULLIF(w.display_name,''), l.display_name),
    context_window = COALESCE(w.context_window, l.context_window),
    released_at    = COALESCE(w.released_at, l.released_at),
    input_price_cny  = CASE WHEN COALESCE(w.input_price_cny,0)=0 THEN l.input_price_cny ELSE w.input_price_cny END,
    output_price_cny = CASE WHEN COALESCE(w.output_price_cny,0)=0 THEN l.output_price_cny ELSE w.output_price_cny END
FROM merge_map m
JOIN models_canonical l ON l.canonical_name = m.loser
WHERE w.canonical_name = m.winner;

-- 4. 为归并的 loser 拼写补 alias(保持旧拼写可解析),垃圾删除项除外
INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT (SELECT id FROM models_canonical WHERE canonical_name = m.winner), m.loser, 'active',
       'merged from duplicate canonical on 2026-09-20', now(), now()
FROM merge_map m
WHERE m.loser NOT IN ('4.6','4-bit','test','global.anthropic.claude-sonnet-4-5-20250929-v1:0')
  AND NOT EXISTS (SELECT 1 FROM model_aliases a
                  WHERE a.canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = m.winner)
                    AND a.raw_name = m.loser)
ON CONFLICT DO NOTHING;

-- 5. 删除垃圾别名(DELETE_ONLY 行下挂的别名)
DELETE FROM model_aliases a
USING models_canonical j
WHERE a.canonical_id = j.id
  AND (j.canonical_name IN ('4-bit','test','glm-5.6-sol')
       OR j.canonical_name ~ ' '
       OR j.canonical_name LIKE 'kx-%');

-- 6. 改名(仅存 :free/:thinking 且基名不存在的 4 行 —— 层级后缀非模型身份)
UPDATE models_canonical SET canonical_name = 'dots-3-note-preview',      updated_at = now() WHERE canonical_name = 'dots-3-note-preview:free';
UPDATE models_canonical SET canonical_name = 'lfm-2.5-2.6b',             updated_at = now() WHERE canonical_name = 'lfm-2.5-2.6b:free';
UPDATE models_canonical SET canonical_name = 'north-mini-code',          updated_at = now() WHERE canonical_name = 'north-mini-code:free';
UPDATE models_canonical SET canonical_name = 'qwen-plus-2025-07-28',     updated_at = now() WHERE canonical_name = 'qwen-plus-2025-07-28:thinking';

-- 6.5 R50 审计（2026-09-21）：§7 前置断言（真门禁）。
-- 旧 V8 在 COMMIT 之后跑，是"事后核对器"而非门禁：①非 0 不回滚任何东西；
-- ②342 迁移的三表 FK 全是 ON DELETE CASCADE，"重定向漏 → §7 级联删 →
-- 行消失"的故障形态恰好也产出 V8=0（悬挂引用随父行一起没了）。因此把
-- 悬挂检查挪到 DELETE 之前，非 0 直接 RAISE EXCEPTION 回滚整个事务；
-- 342 未应用的库（252/本地实测如此）三表不存在则跳过对应臂。
-- 兜底 truth：重定向按构造完备（§2.3 匹配所有指向 loser 的行）+
-- bak_20260920_* 备份；末尾 V8 仅作事后留痕核对。
DO $$
DECLARE
    dangling INT;
BEGIN
    dangling := 0;
    IF to_regclass('public.model_capability_profiles') IS NOT NULL THEN
      dangling := dangling + (SELECT count(*) FROM model_capability_profiles p
        LEFT JOIN models_canonical mc ON mc.id = p.canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.model_capability_profile_audit') IS NOT NULL THEN
      dangling := dangling + (SELECT count(*) FROM model_capability_profile_audit a
        LEFT JOIN models_canonical mc ON mc.id = a.canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.model_substitution_overrides') IS NOT NULL THEN
      dangling := dangling + (SELECT count(*) FROM model_substitution_overrides s
        LEFT JOIN models_canonical mc ON mc.id = s.requested_canonical_id WHERE mc.id IS NULL);
      dangling := dangling + (SELECT count(*) FROM model_substitution_overrides s
        LEFT JOIN models_canonical mc ON mc.id = s.candidate_canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.role_task_llm_mapping') IS NOT NULL THEN
      dangling := dangling + (SELECT count(*) FROM role_task_llm_mapping r
        WHERE NOT EXISTS (SELECT 1 FROM models_canonical mc WHERE lower(mc.canonical_name) = lower(r.llm_canonical_name)));
    END IF;
    IF dangling > 0 THEN
      RAISE EXCEPTION 'cleanup §6.5: % dangling canonical references before §7 DELETE — §2.3 redirect missed a table/column; aborting (transaction rolled back)', dangling;
    END IF;
END $$;

-- 7. 删除: 归并 loser + 垃圾行
DELETE FROM models_canonical mc
USING merge_map m
WHERE mc.canonical_name = m.loser;

DELETE FROM models_canonical
WHERE canonical_name IN ('4-bit','test','glm-5.6-sol')
   OR canonical_name ~ ' '
   OR canonical_name LIKE 'kx-%';

COMMIT;

-- ============================================================================
-- 验证
-- ============================================================================
SELECT 'V1 归一化重复组残留(应为 0)' AS check, count(*) AS n FROM (
  SELECT replace(replace(lower(canonical_name),'.','-'),'_','-') AS nn
  FROM models_canonical GROUP BY 1 HAVING count(*) > 1
) t;

SELECT 'V2 垃圾名残留(空格/冒号/截断/量化,应为 0)' AS check, count(*) AS n
FROM models_canonical
WHERE canonical_name ~ ' ' OR canonical_name ~ ':' OR canonical_name LIKE 'kx-%'
   OR canonical_name IN ('4-bit','test','glm-5.6-sol','opus-5','sonnet-5','v4-flash','v4_flash','v4-pro','3.1-pro','3.8-flash','k3','5.6-luna','5.6-sol','5.6-terra','6-astra','5.3','5.3-flash','4.6','seed-2.0-code','seed-2.0-lite','seed-2.0-mini','seed-2-1-turbo','mistral-nemo');

SELECT 'V3 别名悬挂引用(应为 0)' AS check, count(*) AS n
FROM model_aliases a LEFT JOIN models_canonical mc ON mc.id = a.canonical_id
WHERE a.canonical_id IS NOT NULL AND mc.id IS NULL;

SELECT 'V4 provider_models 悬挂引用(应为 0)' AS check, count(*) AS n
FROM provider_models p LEFT JOIN models_canonical mc ON mc.id = p.canonical_id
WHERE p.canonical_id IS NOT NULL AND mc.id IS NULL;

SELECT 'V5 关键模型唯一性(每行应为 1)' AS check, k.nm, count(mc.id) AS c
FROM (VALUES ('glm-5.3'),('glm-5.3-flash'),('claude-sonnet-5'),('claude-opus-5'),('claude-opus-4-8'),('claude-fable-5'),('grok-4.6'),('deepseek-v4-flash'),('deepseek-v4-pro'),('kimi-k3'),('minimax-m3'),('gpt-5.6-sol')) AS k(nm)
LEFT JOIN models_canonical mc ON replace(replace(lower(mc.canonical_name),'.','-'),'_','-') = replace(replace(lower(k.nm),'.','-'),'_','-')
GROUP BY k.nm;

SELECT 'V6 路由表悬挂拼写(应为 0)' AS check, count(*) AS n
FROM work_type_model_route w
WHERE NOT EXISTS (SELECT 1 FROM models_canonical mc WHERE lower(mc.canonical_name) = lower(w.canonical_name));

SELECT 'V7 行数' AS check,
  (SELECT count(*) FROM models_canonical) AS canonical,
  (SELECT count(*) FROM model_aliases) AS aliases,
  (SELECT count(*) FROM provider_models) AS pm;

-- R50 修订（2026-09-21）：真门禁是 §6.5 的前置断言（COMMIT 前 RAISE
-- EXCEPTION 回滚）；本段降级为事后留痕核对，且各臂带 to_regclass 守卫
-- ——342 未应用的库三表不存在，此前裸查直接报 relation does not exist。
DO $$
DECLARE
    n INT := 0;
BEGIN
    IF to_regclass('public.model_capability_profiles') IS NULL
       AND to_regclass('public.role_task_llm_mapping') IS NULL THEN
        RAISE NOTICE 'V8-skipped: 342 cascade tables not present on this DB';
        RETURN;
    END IF;
    IF to_regclass('public.model_capability_profiles') IS NOT NULL THEN
        n := n + (SELECT count(*) FROM model_capability_profiles p
            LEFT JOIN models_canonical mc ON mc.id = p.canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.model_capability_profile_audit') IS NOT NULL THEN
        n := n + (SELECT count(*) FROM model_capability_profile_audit a
            LEFT JOIN models_canonical mc ON mc.id = a.canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.model_substitution_overrides') IS NOT NULL THEN
        n := n + (SELECT count(*) FROM model_substitution_overrides s
            LEFT JOIN models_canonical mc ON mc.id = s.requested_canonical_id WHERE mc.id IS NULL);
        n := n + (SELECT count(*) FROM model_substitution_overrides s
            LEFT JOIN models_canonical mc ON mc.id = s.candidate_canonical_id WHERE mc.id IS NULL);
    END IF;
    IF to_regclass('public.role_task_llm_mapping') IS NOT NULL THEN
        n := n + (SELECT count(*) FROM role_task_llm_mapping r
            WHERE NOT EXISTS (SELECT 1 FROM models_canonical mc WHERE lower(mc.canonical_name) = lower(r.llm_canonical_name)));
    END IF;
    RAISE NOTICE 'V8 CASCADE 表悬挂引用(应为 0): %', n;
END $$;
