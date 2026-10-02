-- pg17-index-bloat.sh 判据回归夹具（构造地面真值）
--
-- 用途：证明 pg17-index-bloat.sh 的判据**能看见**零散 churn 型膨胀，
--       而不只是「跑通不报错」。
--
-- 背景（2026-10-03 实证）：本项目原先只用 pgstatindex.deleted_pages 判膨胀。
-- 本夹具证明该单信号有真实盲区 ——
--   30 万行删 90% 后，deleted_pages = 0（没有一页被完全清空），
--   但 REINDEX 把索引从 2.79MB 压到 0.30MB，回收 89%（9.4×）。
-- 区分二者的列是 avg_leaf_density（33.94 → 92.52）。
-- ⇒ 判据必须是 deleted_pages OR avg_leaf_density 两个信号取或。
--
-- 运行方式（一次性临时库，不碰业务库）：
--   psql -U postgres -d postgres -c "CREATE DATABASE pgbloat_selftest"
--   podman exec -e PGOPTIONS="-c statement_timeout=0" -i pg-252-pg17 \
--     psql -U postgres -d pgbloat_selftest < pg17-index-bloat-selftest.sql
--   podman exec pg-252-pg17 psql -U postgres -d pgbloat_selftest \
--     -c "CREATE EXTENSION pgstattuple;"   -- 必须先装，否则探针会 exit 2
--
-- 期望结果（判据验收点）：
--   rebuilt  (已 REINDEX, 密度 ~90)  -> 判 ok
--   bloated   (未 REINDEX, 密度 ~34)  -> 判 CANDIDATE
--   且两者的 deleted_pages 均为 0 —— 这是本夹具存在的理由。

\set ON_ERROR_STOP on
\pset pager off

DROP TABLE IF EXISTS selftest_av_off;
DROP TABLE IF EXISTS selftest_av_on;

-- 同一份数据造两份索引，只让「是否重建过」这一个变量不同。
-- autovacuum_enabled 两边都保持默认：实测关掉它也不改变 deleted_pages，
-- 所以变量不是 autovacuum，别再往那个方向排查。
CREATE TABLE selftest_av_off (id bigint PRIMARY KEY, pad text);
CREATE TABLE selftest_av_on  (id bigint PRIMARY KEY, pad text);
INSERT INTO selftest_av_off SELECT g, repeat('x',200) FROM generate_series(1,300000) g;
INSERT INTO selftest_av_on  SELECT g, repeat('x',200) FROM generate_series(1,300000) g;
CREATE INDEX selftest_av_off_pad ON selftest_av_off (pad);
CREATE INDEX selftest_av_on_pad  ON selftest_av_on  (pad);
VACUUM (ANALYZE) selftest_av_off;
VACUUM (ANALYZE) selftest_av_on;

-- 删掉 90%，制造「每页都还剩几条存活、但没有一页全空」的状态
DELETE FROM selftest_av_off WHERE id % 10 <> 0;
DELETE FROM selftest_av_on  WHERE id % 10 <> 0;

\echo '=== 重建前：两者 deleted_pages 应同为 0，密度应显著不同 ==='
SELECT 'bloated(未重建)' AS which, s.deleted_pages, s.avg_leaf_density,
       round(s.index_size/1024.0/1024, 2) AS index_mb
  FROM pgstatindex('selftest_av_on_pad') s;

REINDEX INDEX CONCURRENTLY selftest_av_off_pad;

\echo '=== 重建后：密度回到 ~90，体积降到 ~1/9 ==='
SELECT 'rebuilt(已重建)' AS which, s.deleted_pages, s.avg_leaf_density,
       round(s.index_size/1024.0/1024, 2) AS index_mb
  FROM pgstatindex('selftest_av_off_pad') s
UNION ALL
SELECT 'bloated(对照,未动)', s.deleted_pages, s.avg_leaf_density,
       round(s.index_size/1024.0/1024, 2)
  FROM pgstatindex('selftest_av_on_pad') s;
