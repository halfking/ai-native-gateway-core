export PGPAGER=cat
export TERM=dumb

echo "===== 0. 环境基线 ====="
df -h /var/lib/postgresql/data | tail -1
echo "storage backend: $(mount | grep -m1 'on /var/lib/postgresql/data' | cut -d' ' -f5)"
psql -U llm_gateway -d llm_gateway -A -t -c "SELECT 'columnar_tables_before = '||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='c';"

echo ""
echo "===== 1. Citus columnar 压缩比：request_logs 真实数据 20k 行 ====="
echo "（注意：本机数据盘是 virtiofs 网络盘，列存转换很慢，故用小样本）"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -v ON_ERROR_STOP=1 <<'SQL'
SET statement_timeout = '0';
SET lock_timeout = '5s';
SET client_min_messages = warning;
BEGIN;
CREATE SCHEMA IF NOT EXISTS audit_scratch;

CREATE TABLE audit_scratch.rl_row AS
  SELECT * FROM public.request_logs_2026_09 LIMIT 20000;

\echo '-- row baseline built, building columnar copy --'
CREATE TABLE audit_scratch.rl_colar USING columnar AS
  SELECT * FROM public.request_logs_2026_09 LIMIT 20000;

SELECT 'ROW   = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.rl_row'))
UNION ALL
SELECT 'COLAR = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.rl_colar'))
UNION ALL
SELECT 'RATIO = ' || round(pg_total_relation_size('audit_scratch.rl_row')::numeric
                          / nullif(pg_total_relation_size('audit_scratch.rl_colar'),0), 2) || 'x'
UNION ALL
SELECT 'rows = ' || (SELECT count(*) FROM audit_scratch.rl_row);
ROLLBACK;
SQL
echo "test1_exit=$?"

echo ""
echo "===== 2. TOAST pglz vs lz4：outbound_body 真实数据 5000 条 ====="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -v ON_ERROR_STOP=1 <<'SQL'
SET statement_timeout = '0';
SET lock_timeout = '5s';
SET client_min_messages = warning;
BEGIN;
CREATE SCHEMA IF NOT EXISTS audit_scratch;

CREATE TABLE audit_scratch.body_src AS
  SELECT outbound_body FROM public.request_logs_bodies_2026_09
  WHERE outbound_body IS NOT NULL LIMIT 5000;

CREATE TABLE audit_scratch.body_pglz (b jsonb);
CREATE TABLE audit_scratch.body_lz4  (b jsonb);
ALTER TABLE audit_scratch.body_pglz ALTER COLUMN b SET COMPRESSION pglz;
ALTER TABLE audit_scratch.body_lz4  ALTER COLUMN b SET COMPRESSION lz4;

INSERT INTO audit_scratch.body_pglz SELECT outbound_body FROM audit_scratch.body_src;
INSERT INTO audit_scratch.body_lz4  SELECT outbound_body FROM audit_scratch.body_src;

SELECT 'raw jsonb bytes = ' || pg_size_pretty(sum(pg_column_size(outbound_body))::bigint)
       || ' | rows = ' || count(*) FROM audit_scratch.body_src;
SELECT 'PGLZ = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.body_pglz'))
UNION ALL
SELECT 'LZ4  = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.body_lz4'))
UNION ALL
SELECT 'LZ4 SAVES = ' || pg_size_pretty(
         pg_total_relation_size('audit_scratch.body_pglz') - pg_total_relation_size('audit_scratch.body_lz4')) ||
       ' (' || round(100.0 * (pg_total_relation_size('audit_scratch.body_pglz') - pg_total_relation_size('audit_scratch.body_lz4'))
                    / nullif(pg_total_relation_size('audit_scratch.body_pglz'),0), 1) || '%)';
ROLLBACK;
SQL
echo "test2_exit=$?"

echo ""
echo "===== 3. 应用层 zstd 对照：同一批 body 在 PG 层压一次再存 ====="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -v ON_ERROR_STOP=1 <<'SQL'
SET statement_timeout = '0';
SET lock_timeout = '5s';
SET client_min_messages = warning;
BEGIN;
CREATE SCHEMA IF NOT EXISTS audit_scratch;

CREATE TABLE audit_scratch.body_src AS
  SELECT outbound_body FROM public.request_logs_bodies_2026_09
  WHERE outbound_body IS NOT NULL LIMIT 5000;

-- TOAST lz4（PG 层自动压缩）
CREATE TABLE audit_scratch.t_lz4 (b jsonb);
ALTER TABLE audit_scratch.t_lz4 ALTER COLUMN b SET COMPRESSION lz4;
INSERT INTO audit_scratch.t_lz4 SELECT outbound_body FROM audit_scratch.body_src;

-- 应用层 zstd 存成 bytea（模拟 cmd/compression-bench 的做法）
CREATE TABLE audit_scratch.t_appzstd (b bytea);
INSERT INTO audit_scratch.t_appzstd
  SELECT convert_to((SELECT convert_from(compress(outbound_body::text, 'zstd'))) , 'UTF8') FROM audit_scratch.body_src;

SELECT 'TOAST lz4     = ' || pg_size_pretty(pg_total_relation_size('audit_scratch.t_lz4'))
UNION ALL
SELECT 'app-layer zstd= ' || pg_size_pretty(pg_total_relation_size('audit_scratch.t_appzstd'));
ROLLBACK;
SQL
echo "test3_exit=$?"

echo ""
echo "===== 4. 清理确认 ====="
psql -U llm_gateway -d llm_gateway -A -t -c "SELECT 'audit_scratch schema exists = '||(to_regnamespace('audit_scratch') IS NOT NULL)::text;"
psql -U llm_gateway -d llm_gateway -A -t -c "SELECT 'columnar tables after = '||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='c';"
df -h /var/lib/postgresql/data | tail -1
