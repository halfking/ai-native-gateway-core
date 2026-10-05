-- ===========================================================================
-- File:          sql/migrations/startup/828_supplier_errors_unified_tracked.sql
-- Migration:     828
-- Database:      llm_gateway
-- Purpose:        让 `supplier_errors_unified` 变成**可从仓库复现**、且在
--                Citus columnar 布局上**每一列都能投影**的视图。
--
-- 立项依据（本机库实测，非推断）：
--
-- 1. **这个视图在仓库里根本不存在。**
--    实测：`sql/schema/01-schema.sql`、`deploy/sql/schemas/baseline/01-schema.sql`、
--    `installer/.../embeddata/01-schema.sql` 三份 schema 快照里 **0 处**；
--    `sql/migrations/startup/**` 与 `installer/.../embeddata/startup/**` 里 **0 处**。
--    唯一定义处是 `deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql`，
--    而 `schema_migrations` 里 **V371 从未被记录**（本机最高只到 V359）。
--    ⇒ 视图真实存在于本机库，却**不可从仓库复现**：一个全新安装/重建的库不会有它，
--      而 admin 的三个读端都查它（`admin/errors_trend.go`、`admin/provider_credential.go`、
--      `admin/handler.go`）。本迁移把它补进**受追踪的 startup 链**。
--
-- 2. **第 1 列 `source` 在 columnar 上不可投影。**
--    `supplier_errors_2026_09` 是 Citus `columnar` 分区，于是
--    `SELECT source FROM supplier_errors_unified` 抛
--      cache lookup failed for attribute source of relation <oid>
--    （oid = 该列存分区）。审计 §9.197.5 的逐列扫描：8 视图 147 列中只有这 1 列失败。
--
-- 3. **触发条件不是「合成常量列」，是「输出列不是基表列的直接 Var」。**
--    真库事务内实测四种变体（CREATE VIEW + ROLLBACK，不留痕）：
--      · `CASE WHEN <rel>.id IS NOT NULL THEN 'hot' … END::text AS source`  → 仍失败
--      · `tenant_id AS source`（纯改名，源列真实存在）                          → 仍失败
--      · 两条腿各包一层 CTE                                                    → 换成
--        `invalid perminfoindex 0 in RTE with relid 0`（集合算子 + 列存，另一种故障）
--      · **删掉 `source` 列**                                                  → 正常
--    `SELECT *` 能过只是因为 planner 把用不到的视图列裁掉了，不是列没问题。
--
-- ⇒ 本迁移**删除 `source` 列**。契约 21 列 → 20 列。
--    消费方影响：**零**。已核生产 Go 全部读点（`admin/errors_trend.go:239`、
--    `admin/provider_credential.go`、`admin/handler.go`、`deploy/sql/verify/supplier_errors_pg_test.go`）
--    都不选 `source`——现网真实形状实测正常，它一直是**潜伏**故障。
--    代价：调用方**无法再从视图本身区分 hot 与 historical 行**。
--    ⚠ 决策记录：审计 §9.200 / 决策表 D30-d（属主已拍板选此方案）。
--
-- 为什么必须 `DROP VIEW` 而不是 `CREATE OR REPLACE VIEW`：
--   CREATE OR REPLACE **不能删列、不能改列名/列序**；删列只能先 DROP。
--   本视图实测**无任何依赖视图**（pg_depend 对 r.ev_class <> r.oid 查询 0 行），
--   故不加 CASCADE：万一有本轮没查到的依赖，让它**失败**并回滚，
--   而不是悄悄级联删掉别人的东西。
--
-- 幂等：先 DROP IF EXISTS 再 CREATE，可安全重放；
--       在未部署 V371 的全新环境上，本迁移等价于「首次创建」。
--
-- 锁：CREATE VIEW 对基表只取 ACCESS SHARE；DROP VIEW 只锁视图自身。
--     不会阻塞 promote / 写入路径。
-- ===========================================================================
BEGIN;

DROP VIEW IF EXISTS public.supplier_errors_unified;

CREATE VIEW public.supplier_errors_unified AS
SELECT
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors_hot
UNION ALL
SELECT
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors;

-- 与 V371 原定义保持一致：读端以调用者权限访问（RLS 生效）。
ALTER VIEW public.supplier_errors_unified SET (security_invoker = true);

COMMENT ON VIEW public.supplier_errors_unified IS
    'supplier_errors 热面 ∪ 月度分区（20 列）。第 1 列 `source` 已于 828 移除：'
    '它在 Citus columnar 分区上不可投影（cache lookup failed for attribute），'
    '且改为基表列表达式无效——触发条件是「输出列不是基表列的直接 Var」。'
    '需要区分来源的调用方请改用 supplier_errors_hot 直查。';

COMMIT;
