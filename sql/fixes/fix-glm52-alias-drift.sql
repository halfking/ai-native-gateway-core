-- =============================================================================
-- fix-glm52-alias-drift.sql
-- 问题1：glm-5.2 请求被错误路由到 glm-5.1 的根因数据修复脚本
--
-- 根因（已通过代码 + DB 审计确认）：
--   provider/client.go:loadCandidatesDB 的候选匹配条件 (3) alias match：
--     WHERE lower(ma2.raw_name) = lower($1)   -- $1 = 'glm-5.2'
--       AND ma2.canonical_id = mo.canonical_id
--   当生产库 model_aliases 把 'glm-5.2' 这个 raw_name 错误关联到 glm-5.1 的
--   canonical_id (=49) 时，所有 canonical_id=49 的 offer（即 glm-5.1 的 offer）
--   都会被当作 glm-5.2 的候选返回，于是请求 glm-5.2 实际打到 glm-5.1。
--
--   代码层面的模型变体生成（modelname.NormalizeRouteKeyAliases /
--   versionPunctuationCartesian）是正确的：glm-5.2 只会生成
--   ['glm-5.2','glm-5-2'] 等变体，永远不会变成 glm-5.1。
--   所以这是纯数据配置问题，本脚本负责检测与修复。
--
-- 用法：
--   本脚本分为三部分，请按顺序在 psql 中逐段执行并人工确认：
--     第 1 部分：检测（只读 SELECT，安全，先看有多少脏数据）
--     第 2 部分：DRY-RUN 预览修复影响（只读）
--     第 3 部分：修复（DML，需人工确认后取消注释执行）
--
--   执行前请先备份：
--     pg_dump -t model_aliases -t models_canonical -t model_offers > backup_alias_$(date +%Y%m%d).sql
-- =============================================================================

\echo '========================================================'
\echo ' 第 1 部分：检测（只读）'
\echo '========================================================'

-- 1.1 检测 glm-5.2 被 alias 关联到非 glm-5.2 canonical 的情况
--     这是问题1的直接根因数据。
\echo '--- 1.1 glm-5.2 的 alias 是否被关联到错误的 canonical ---'
SELECT
    ma.id            AS alias_id,
    ma.raw_name      AS alias_raw_name,
    ma.canonical_id  AS alias_canonical_id,
    mc.canonical_name AS canonical_name,
    CASE
        WHEN mc.canonical_name ILIKE '%glm-5.2%' OR mc.canonical_name ILIKE '%glm-5-2%'
            THEN 'OK (glm-5.2)'
        WHEN mc.canonical_name ILIKE '%glm-5.1%' OR mc.canonical_name ILIKE '%glm-5-1%'
            THEN 'DRIFT -> glm-5.1  ← 错误!'
        ELSE 'DRIFT -> 其它 canonical  ← 需检查'
    END AS diagnosis
FROM model_aliases ma
JOIN models_canonical mc ON mc.id = ma.canonical_id
WHERE ma.raw_name ILIKE '%glm-5.2%'
   OR ma.raw_name ILIKE '%glm-5-2%'
ORDER BY ma.raw_name;

-- 1.2 当前 glm-5.2 / glm-5.1 各自的 canonical 与 family，确认是否归并到了同一个 canonical
\echo '--- 1.2 glm-5 系列的 canonical 行 ---'
SELECT id, canonical_name, family, status
FROM models_canonical
WHERE canonical_name ILIKE '%glm-5%'
ORDER BY canonical_name;

-- 1.3 检测 model_offers 里 raw_model_name='glm-5.2' 但 canonical_id 指向 glm-5.1 的情况
--     （另一种可能的脏数据形态：offer 行本身 canonical_id 写错）
\echo '--- 1.3 glm-5.2 offer 的 canonical_id 是否指向了 glm-5.1 ---'
SELECT
    mo.credential_id,
    mo.raw_model_name,
    mo.outbound_model_name,
    mo.canonical_id   AS offer_canonical_id,
    mc.canonical_name AS offer_canonical_name,
    CASE
        WHEN mc.canonical_name ILIKE '%glm-5.2%' OR mc.canonical_name ILIKE '%glm-5-2%'
            THEN 'OK'
        ELSE 'DRIFT  ← offer 的 canonical_id 错误'
    END AS diagnosis
FROM model_offers mo
LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
WHERE mo.raw_model_name ILIKE '%glm-5.2%'
   OR mo.outbound_model_name ILIKE '%glm-5.2%'
ORDER BY mo.raw_model_name;

-- 1.4 通用检测：所有"客户端模型主版本号 ≠ canonical 主版本号"的 alias 跨版本关联
--     （把 glm-5.* 这类 family 之外的数字段视为主版本，捕获同类 drift）
\echo '--- 1.4 通用：跨主版本的 alias drift（不限 glm） ---'
WITH parsed AS (
    SELECT
        ma.id, ma.raw_name, ma.canonical_id, mc.canonical_name,
        -- 取 raw_name 最后一个 "x.y" 或 "x-y" 版本段
        lower(regexp_replace(ma.raw_name, '^.*?(\d+)[\.\-](\d+)([^\d].*)?$', '\1.\2', '')) AS raw_ver,
        lower(regexp_replace(mc.canonical_name, '^.*?(\d+)[\.\-](\d+)([^\d].*)?$', '\1.\2', '')) AS can_ver
    FROM model_aliases ma
    JOIN models_canonical mc ON mc.id = ma.canonical_id
    WHERE ma.status = 'active'
      AND mc.canonical_name ~* '\d+[\.\-]\d+'
      AND ma.raw_name     ~* '\d+[\.\-]\d+'
)
SELECT raw_name, canonical_id, canonical_name, raw_ver, can_ver
FROM parsed
WHERE raw_ver <> can_ver
  AND raw_ver <> '' AND can_ver <> ''
ORDER BY raw_name;


\echo '========================================================'
\echo ' 第 2 部分：DRY-RUN 预览修复影响（只读）'
\echo '========================================================'

-- 预览：如果为 glm-5.2 新建独立 canonical，哪些 alias / offer 会迁移过去
\echo '--- 2.1 将被迁移到新 glm-5.2 canonical 的 alias 行 ---'
SELECT id, canonical_id, raw_name
FROM model_aliases
WHERE raw_name ILIKE '%glm-5.2%' OR raw_name ILIKE '%glm-5-2%'
ORDER BY raw_name;

\echo '--- 2.2 将被迁移到新 glm-5.2 canonical 的 offer 行 ---'
SELECT credential_id, raw_model_name, outbound_model_name, canonical_id
FROM model_offers
WHERE raw_model_name ILIKE '%glm-5.2%' OR outbound_model_name ILIKE '%glm-5.2%';


\echo '========================================================'
\echo ' 第 3 部分：修复（DML，请人工确认后取消注释执行）'
\echo '========================================================'
\echo ' 注意：以下语句默认注释。请根据第 1 部分的检测结果确认后，'
\echo ' 取消注释并在事务中执行（BEGIN/ROLLBACK 先验证）。'

-- 修复策略：为 glm-5.2 建立独立的 canonical，把所有 glm-5.2 的 alias 和 offer
-- 从 glm-5.1 的 canonical(49) 迁移到新的 glm-5.2 canonical，彻底切断跨版本匹配。
-- 用 family = 'zhipu-glm' 与 glm-5.1 保持同族（与 discovery/normalize.go 一致）。

-- BEGIN;
--
-- -- 3.1 新建 glm-5.2 独立 canonical（若已存在则取其 id）
-- INSERT INTO models_canonical (canonical_name, family, source, status)
-- VALUES ('glm-5.2', 'zhipu-glm', 'manual_fix', 'active')
-- ON CONFLICT (canonical_name) DO NOTHING;
--
-- -- 3.2 把 glm-5.2 的所有 alias 迁移到新 canonical
-- UPDATE model_aliases AS ma
-- SET canonical_id = (
--         SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.2'
--     ),
--     status = 'active'
-- WHERE (ma.raw_name ILIKE '%glm-5.2%' OR ma.raw_name ILIKE '%glm-5-2%')
--   AND ma.canonical_id <> (SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.2');
--
-- -- 3.3 把 glm-5.2 的 offer 的 canonical_id 修正到新 canonical
-- UPDATE model_offers AS mo
-- SET canonical_id = (
--         SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.2'
--     )
-- WHERE (mo.raw_model_name ILIKE '%glm-5.2%' OR mo.outbound_model_name ILIKE '%glm-5.2%')
--   AND mo.canonical_id <> (SELECT id FROM models_canonical WHERE canonical_name = 'glm-5.2');
--
-- -- 3.4 复核（应全部为 OK）
-- SELECT ma.raw_name, mc.canonical_name FROM model_aliases ma
-- JOIN models_canonical mc ON mc.id = ma.canonical_id
-- WHERE ma.raw_name ILIKE '%glm-5.2%';
--
-- -- 确认无误后：COMMIT;   否则：ROLLBACK;
-- COMMIT;

\echo '修复脚本结束。如执行了第 3 部分，请在网关侧确认 30s 候选缓存已失效（自动）后重新请求 glm-5.2 验证。'
