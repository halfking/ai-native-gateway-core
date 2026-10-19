-- ===========================================================================
-- File:          sql/migrations/startup/878_supplier_errors_partition_indexes.sql
-- Migration:     878
-- Database:      llm_gateway
-- Purpose:       给 supplier_errors 分区父表补齐 5 条 btree 索引（形状照抄
--                supplier_errors_hot 上已被同一批查询实测用上的那 5 条），
--                让 supplier_errors_unified 的历史侧不再对每个 54MB 月分区顺序扫描
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS × 5；父表索引对现存与未来分区级联)
-- Dependencies:  703（supplier_errors 家族与 promote 路径）；无新扩展
--
-- Background（缺陷本体，211 acc-pg17 只读实测 2026-10-19）:
--   supplier_errors_unified 是 admin 侧错误分析的唯一读入口
--   （view = supplier_errors_hot UNION ALL supplier_errors），而它两臂的索引
--   配置**完全不对称**：
--     supplier_errors_hot       5 条 btree（见下 5 条同名后缀的 _hot 形态）
--     supplier_errors_2026_09   0 条
--     supplier_errors_2026_10   0 条   ← 54MB / 75,329 行
--     supplier_errors_2026_11   0 条   （未来月分区，已预建）
--   ⇒ 走 view 的每一条查询，热侧走索引、历史侧对月分区顺序扫描。
--
--   实测证据（同实例 EXPLAIN ANALYZE，非估算）：
--     · admin/vendor_credential_error_handlers.go:337 的形态
--       （credential_id 等值 + 30 天窗 + ORDER BY occurred_at DESC LIMIT 200）
--         Seq Scan on supplier_errors_2026_10  actual rows=21,639
--         Rows Removed by Filter: 53,690
--         Buffers: shared hit=1 read=6,876    Execution Time: 60.213 ms
--       —— 同一查询的 hot 侧是 Index Scan using idx_supplier_errors_hot_occurred_at
--     · admin/errors_trend.go:266 的形态（1 小时窗 GROUP BY supplier）
--         Seq Scan on supplier_errors_2026_10  actual rows=0
--         Rows Removed by Filter: 75,329
--         Buffers: shared hit=35 read=6,842   Execution Time: 13.113 ms
--       —— 区间只覆盖到 hot，却仍把整张月分区读了 75,329 行。
--     · 累计代价：pg_stat_user_tables.supplier_errors_2026_10
--         seq_scan=1,615  seq_tup_read=69,873,160  idx_scan=（空）
--         n_tup_ins=75,329  n_tup_upd=0  n_tup_del=0
--       即读:写 ≈ 927:1，而读侧一条索引都没有。
--
-- 为什么照抄 hot 的 5 条形状，而不是重新设计：
--   这 5 条索引服务的就是同一批查询在热侧的另一半臂
--   （hot 侧的 Index Scan / Index Only Scan 是 EXPLAIN 实测出来的，不是推测）。
--   重新设计等于在没有证据的地方引入第二套口径。
--
-- ★ 为什么必须建在**父表**上（876 同款教训）：
--   ensure_supplier_errors_partition 用 CREATE TABLE … PARTITION OF 建月分区，
--   会克隆父表上声明的每一个索引。只在分区实例上建索引，下个月会长不回来。
--   建在父表上一条语句同时覆盖现存与未来分区。
--   （验收口径同 877：按**分区级 pg_index** 查，既不按父表 pg_get_indexdef 的
--   `ON ONLY` 文字判，也不按索引名判 —— 分区级索引会被 PG 自动改名。）
--
-- 写放大口径（诚实说明）：
--   本表月增约 7.5 万行（2.5k/天），5 条 btree 的维护成本在这个写入量上不可见；
--   换来的读侧收益是 69,873,160 行顺序扫描的直接消失。
--   ⇒ 与本仓「写放大」主线不冲突：这里读:写 ≈ 927:1，属索引的收益区间。
--   ★ 但**不要**据此推广到 usage_ledger / session_turns 等热写表 ——
--     那几张的读写比与本表完全不同，判据不通用。
--
-- ⚠ 锁：分区父表不支持 CREATE INDEX CONCURRENTLY（877/874 同款取舍）。
--   单事务链 + 普通 CREATE INDEX + lock_timeout=60s（抢不到锁快速失败、
--   整体回滚、稍后重跑）+ statement_timeout=10min。
--   54MB 级分区建 5 条索引实测在分钟级以内。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

-- ① 时间窗轴：errors_trend / supplier_error_stats_aggregator 的
--    「occurred_at 区间」谓词，以及所有 ORDER BY occurred_at DESC。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_occurred_at
    ON public.supplier_errors USING btree (occurred_at DESC);

-- ② 供应商 × 凭据 × 时间：errors_trend.go:270/306/315/325 的
--    「supplier 等值 + 时间窗 + COUNT(DISTINCT credential_id)」。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_supplier_credential
    ON public.supplier_errors USING btree (supplier, credential_id, occurred_at DESC);

-- ③ 凭据 × 时间：vendor_credential_error_handlers.go:232/337 的主谓词。
--    ★ 不把 tenant_id 放进键里：那两处的租户条件写成
--      `($2 = '' OR tenant_id = $2)` —— **OR 形态**，
--      带 OR 的参数化谓词无法作为索引键使用，放进去只是白付写成本。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_credential_occurred
    ON public.supplier_errors USING btree (credential_id, occurred_at DESC);

-- ④ 租户 × 时间：RLS 隔离形态下的下钻。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_tenant
    ON public.supplier_errors USING btree (tenant_id, occurred_at DESC);

-- ⑤ 错误类型 × 时间：按错误类型下钻。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_error_type
    ON public.supplier_errors USING btree (error_type, occurred_at DESC);

COMMIT;

-- ===========================================================================
-- 实测记录（acc-pg17 = 211 的自建 PG 实例，PostgreSQL 17；**非** 172.16.2.210）
-- ⚠ 多副本纪律：本条只对 211 的 acc-pg17 成立。生产 172.16.2.210 上
--   supplier_errors_2026_09 是**列存分区**（见 admin/columnar_surface_servible_realdb_test.go:35
--   的报错引用），列存上的 btree 行为与验收口径都不同，不得据本条推断生产。
-- ===========================================================================

-- COMMENT 与创建腿同守卫（874/877 同款：守卫掉的对象留无条件 COMMENT 会在
-- COMMIT 后以 relation does not exist 非零退出，迁移永不记账）。
DO $comment_878_index$
BEGIN
  IF to_regclass('public.idx_supplier_errors_part_occurred_at') IS NOT NULL THEN
    COMMENT ON INDEX idx_supplier_errors_part_occurred_at IS
      '878: supplier_errors 月分区的时间窗轴索引 —— supplier_errors_unified 的历史侧臂；无它则 1 小时窗查询也要顺序扫描 54MB 分区读满 75,329 行（211 实测）。形状与 idx_supplier_errors_hot_occurred_at 一致。';
  END IF;
  IF to_regclass('public.idx_supplier_errors_part_supplier_credential') IS NOT NULL THEN
    COMMENT ON INDEX idx_supplier_errors_part_supplier_credential IS
      '878: supplier_errors 月分区的 (supplier, credential_id, occurred_at DESC) 索引 —— 服务 admin/errors_trend.go 的供应商/凭据维度聚合；与 hot 侧同名后缀索引对称。';
  END IF;
  IF to_regclass('public.idx_supplier_errors_part_credential_occurred') IS NOT NULL THEN
    COMMENT ON INDEX idx_supplier_errors_part_credential_occurred IS
      '878: supplier_errors 月分区的 (credential_id, occurred_at DESC) 索引 —— vendor_credential_error_handlers.go 的凭据错误列表/聚合主谓词；刻意不含 tenant_id（调用点是 OR 形态参数化租户条件，索引键用不上）。';
  END IF;
  IF to_regclass('public.idx_supplier_errors_part_tenant') IS NOT NULL THEN
    COMMENT ON INDEX idx_supplier_errors_part_tenant IS
      '878: supplier_errors 月分区的 (tenant_id, occurred_at DESC) 索引 —— RLS 隔离后的租户维度下钻。';
  END IF;
  IF to_regclass('public.idx_supplier_errors_part_error_type') IS NOT NULL THEN
    COMMENT ON INDEX idx_supplier_errors_part_error_type IS
      '878: supplier_errors 月分区的 (error_type, occurred_at DESC) 索引 —— 按错误类型下钻。';
  END IF;
END
$comment_878_index$;
