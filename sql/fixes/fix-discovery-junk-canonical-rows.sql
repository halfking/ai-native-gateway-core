-- =============================================================================
-- fix-discovery-junk-canonical-rows.sql
-- (2026-09-11)  垃圾标准行治理 —— psql 伴生分诊脚本
--
-- 背景：
--   2026-09-10 之前，discovery 定时 worker 与手工 provider-refresh 共用的
--   EnsureCanonicalAndAliases 会把 raw 名去掉 "/" 前缀后直接 seed 成标准行：
--       claude/opus-5 → canonical "opus-5"
--       grok/4.6      → canonical "4.6"
--   即使 claude-opus-5 / grok-4.6 已经存在。因此垃圾行的 source 可能是
--   'discovery' 也可能是 'provider_refresh'（两个调用方各自打标）。
--   修复后的匹配逻辑在 modelname/match.go (MatchStandardModels)，不再新增
--   垃圾行，但存量垃圾行仍在 models_canonical，provider_models 里
--   standardized_name（乃至 canonical_id）也引用着这些截断名。
--
-- 本文件只是 psql 分诊（triage）与人工修复模板。
-- 权威的检测 / 打分 / 执行工具是（打分门禁用 Go 的
-- modelname.BestStandardModelMatch，SQL 无法等价复刻，所以下面第 1 部分
-- 的 SQL 检测是"结构性"近似：canonical_name 恰等于某带前缀 raw 名的
-- 去前缀 base）：
--
--   go run ./scripts/govern-junk-canonical              # 诊断（只读）
--   go run ./scripts/govern-junk-canonical -json        # 诊断（JSON）
--   go run ./scripts/govern-junk-canonical -apply       # 执行（单事务）
--
-- 用法（psql，任意可连库环境；先跑只读部分）：
--   docker exec -i <pg容器> psql -U llm_gateway -d llm_gateway \
--     -v ON_ERROR_STOP=1 -f /path/fix-discovery-junk-canonical-rows.sql
-- =============================================================================

\set ON_ERROR_STOP on

\echo '========================================================'
\echo ' 第 1 部分：检测（只读）'
\echo '========================================================'

-- 1.1 疑似垃圾 canonical 行：
--   * status='active' 且 source 属于自动 seed 来源
--   * 没有指向它的"外来"活跃别名（仅自身拼写变体 _/. 的不算 —— 那是旧
--     管线生成垃圾行时一并写出来的，apply 时会被一并重定向）
--   * canonical_name 恰等于某个带前缀 raw 名去掉前缀后的 base
--     （严格的后缀/包含关系与打分判定在 Go 工具里，SQL 只做精确相等近似）
\echo '--- 1.1 疑似垃圾 canonical 行（结构近似检测） ---'
SELECT mc.id,
       mc.canonical_name,
       mc.source,
       count(DISTINCT pm.id) AS pm_refs,
       min(pm.raw_model_name) AS example_raw_name
FROM models_canonical mc
JOIN provider_models pm
  ON pm.raw_model_name LIKE '%/%'
 AND lower(split_part(pm.raw_model_name, '/', 2)) = mc.canonical_name
WHERE mc.status = 'active'
  AND mc.source IN ('discovery', 'provider_refresh', 'auto_discovered')
  AND NOT EXISTS (
        SELECT 1 FROM model_aliases ma
         WHERE ma.canonical_id = mc.id
           AND ma.status = 'active'
           AND replace(replace(lower(ma.raw_name), '_', '-'), '.', '-')
               <> replace(replace(lower(mc.canonical_name), '_', '-'), '.', '-'))
GROUP BY mc.id, mc.canonical_name, mc.source
ORDER BY length(mc.canonical_name), mc.canonical_name;

-- 1.2 建议目标（SQL 近似）：前缀-base 重连后的名字已存在于目录。
--     真实打分（含 typo 容忍 'cluade/opus-5'→'claude-opus-5'、
--     Rule-2 陷阱规避、同分分歧守卫）请以 Go 工具输出为准。
\echo '--- 1.2 建议目标（joined 名精确命中目录；近似） ---'
SELECT mc.id           AS junk_id,
       mc.canonical_name AS junk_name,
       count(DISTINCT pm.id) AS pm_refs,
       min(tgt.id)           AS target_id,
       min(tgt.canonical_name) AS target_name,
       min(split_part(pm.raw_model_name, '/', 1) || '-' || split_part(pm.raw_model_name, '/', 2)) AS joined_form
FROM models_canonical mc
JOIN provider_models pm
  ON pm.raw_model_name LIKE '%/%'
 AND lower(split_part(pm.raw_model_name, '/', 2)) = mc.canonical_name
JOIN models_canonical tgt
  ON tgt.canonical_name = lower(split_part(pm.raw_model_name, '/', 1)) || '-' || split_part(pm.raw_model_name, '/', 2)
 AND tgt.status = 'active'
WHERE mc.status = 'active'
  AND mc.source IN ('discovery', 'provider_refresh', 'auto_discovered')
GROUP BY mc.id, mc.canonical_name
ORDER BY mc.canonical_name;

\echo '========================================================'
\echo ' 第 2 部分：引用与别名明细（只读）'
\echo '========================================================'

\echo '--- 2.1 引用疑似行的 provider_models（canonical_id 或 standardized_name） ---'
SELECT pm.id, pm.provider_id, pm.raw_model_name,
       pm.canonical_id, pm.standardized_name, pm.available
FROM provider_models pm
JOIN models_canonical mc ON mc.id = pm.canonical_id
WHERE mc.status = 'active'
  AND mc.source IN ('discovery', 'provider_refresh', 'auto_discovered')
  AND EXISTS (
        SELECT 1 FROM provider_models pm2
         JOIN models_canonical mc2 ON mc2.id = pm2.canonical_id
         WHERE pm2.raw_model_name LIKE '%/%'
           AND lower(split_part(pm2.raw_model_name, '/', 2)) = mc2.canonical_name
           AND mc2.id = mc.id)
ORDER BY mc.id, pm.id;

\echo '--- 2.2 指向疑似行的活跃别名（含"外来"别名 —— 这些行 Go 工具会列为'
\echo '     withheld-foreign-alias，说明有客户端名正在路由到垃圾行，需人工决策） ---'
SELECT mc.canonical_name AS junk_name, ma.raw_name, ma.status, ma.canonical_id
FROM model_aliases ma
JOIN models_canonical mc ON mc.id = ma.canonical_id
WHERE ma.status = 'active'
  AND mc.status = 'active'
  AND mc.source IN ('discovery', 'provider_refresh', 'auto_discovered')
  AND EXISTS (
        SELECT 1 FROM provider_models pm
         WHERE pm.raw_model_name LIKE '%/%'
           AND lower(split_part(pm.raw_model_name, '/', 2)) = mc.canonical_name)
ORDER BY mc.canonical_name, ma.raw_name;

\echo '========================================================'
\echo ' 第 3 部分：修复（DML 模板，默认全部注释；优先用 Go 工具 -apply）'
\echo '========================================================'
-- 执行前先备份：
--   docker exec <pg容器> pg_dump -U llm_gateway -d llm_gateway \
--     -t models_canonical -t model_aliases -t provider_models \
--     --data-only --inserts > /root/backup_junk_canonical_$(date +%Y%m%d_%H%M).sql
--
-- 对每个（junk_id, target_id）替换尖括号占位后逐块确认。单事务：
--
-- BEGIN;
--
-- -- 3.1 重定向 provider_models（引用计数归零是 3.4 的前提）
-- UPDATE provider_models
--    SET canonical_id      = <TARGET_ID>,
--        standardized_name = (SELECT canonical_name FROM models_canonical WHERE id = <TARGET_ID>),
--        updated_at        = now()
--  WHERE canonical_id = <JUNK_ID>
--     OR (canonical_id IS NULL
--         AND standardized_name = (SELECT canonical_name FROM models_canonical WHERE id = <JUNK_ID>));
--
-- -- 3.2 别名保路由：垃圾名（及其变体）指向正确标准行
-- INSERT INTO model_aliases (canonical_id, raw_name, status)
-- SELECT <TARGET_ID>, canonical_name, 'active'
--   FROM models_canonical WHERE id = <JUNK_ID>
-- ON CONFLICT (canonical_id, raw_name) DO UPDATE
--   SET status = 'active', updated_at = now();
--
-- -- 3.3 弃用所有仍指向垃圾行的活跃别名
-- UPDATE model_aliases
--    SET status = 'deprecated', updated_at = now()
--  WHERE canonical_id = <JUNK_ID> AND status = 'active';
--
-- -- 3.4 垃圾行置 deprecated（绝不 DELETE；确认引用已清零）
-- UPDATE models_canonical
--    SET status = 'deprecated', updated_at = now()
--  WHERE id = <JUNK_ID> AND status = 'active'
--    AND NOT EXISTS (SELECT 1 FROM provider_models WHERE canonical_id = <JUNK_ID>);
--
-- 确认无误：COMMIT;   否则：ROLLBACK;
--
-- 复核（应返回 0 行）：
--   SELECT id, canonical_name FROM models_canonical
--    WHERE status = 'active'
--      AND canonical_name IN ( SELECT ... 垃圾名单 ... );
--
-- 注意：request_logs.canonical_model 等历史数据保持原样（仅分析用途，
-- 不影响路由）。apply 后 30-120s 候选缓存自动失效（resolve/resolve.go）。
\echo '第 3 部分为注释模板，未执行任何 DML。'
