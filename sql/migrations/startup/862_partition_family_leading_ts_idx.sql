-- 862_partition_family_leading_ts_idx.sql
-- 2026-10-14：给两个分区父表补**无谓词的 (ts) 前导索引**。
--
-- 缺陷（2026-10-14 252 PG SQL 日志审计轮，30.5h 窗实测）：
--   两族分区的全部 ts 索引都是复合前导（(tenant_id, ts)、(CASE credential, ts)）
--   或部分索引（(ts DESC) WHERE discard_events IS NOT NULL、
--   (ts, id) WHERE digest IS NULL）。凡查询只按 ts 窗（无租户/凭据约束），
--   planner 只能整条索引扫（Index Only Scan 全量 + 聚合 / Bitmap 全扫）：
--
--   · db.V1FreezeCutoffSQL 的母表臂 `SELECT max(ts) FROM _request_logs`：
--     2026_10 分区一臂 cost 8442 / 334,757 条目全扫。该冻结点被内联进
--     auto_index_refresher rollup（5min tick × 每实例，实测 mean 7.4s、
--     峰值 29.5s 贴 30s rolconfig 击杀线）、admin/tenants.go 四臂租户统计。
--   · canonical 视图 request_logs_with_current_month 的窗口读臂
--     （licensing collector TPS/model-usage/in-flight 三连、admin 面板族）：
--     session_turns_2026_09 臂 cost 11,917（79.8 万行）。
--     30.5h 窗 ≥1s 慢查 16,695 条中该视图族占 ~7,480 条。
--
-- 改法：在**分区父表**上建普通 (ts) 索引，由 PostgreSQL 级联到现存分区、
--   并自动出现在未来新分区（842/846/858 同款先例）。
--
-- ⚠ 两个设计决定：
--   1) **刻意不带任何 WHERE 谓词**。现存同名观感的部分索引
--      （request_logs_2026_XX_ts_idx / session_turns_*_ts_id_idx）恰恰是
--      本缺陷的成因——谓词让它们对通用窗口查询不可用。判据
--      migration_862_test.go 对此有反向钉子。
--   2) request_logs 腿用 **860 之后的 `_` 前缀名**。862 序号在 860 之后，
--     任何走迁移链的环境执行到这里时改名已完成。对已按 736 先例预应用
--     （旧名、同名索引）的环境，IF NOT EXISTS 按索引名命中，幂等跳过。
--
-- 上线代价：session_turns 是活写分区（2026_09 79.8 万 + 2026_10 12.8 万行），
--   普通 CREATE INDEX 父级级联期间写被阻塞（ShareLock，秒到十秒级、读不受
--   影响，846 同款已实证）。request_logs 族自 S4 停写（storage.
--   request_logs_write_enabled=false）起冻结，零写入风险；该族整体带 860
--   的待删除标志，索引随族 DROP 一起消失，无清理债。
--
-- 回滚：.down.sql 按名删两条父级索引（级联删分区子索引），不动数据。

BEGIN;

-- ⚠ 共享 PG 服务端 statement_timeout=30s（runner.go 与 856 头注均有实测：
--   245 首部署 65.2s、849/855 均在部署链上 57014）。本迁移的
--   session_turns 腿在**活写**分区父表上建无谓词 (ts) 索引并级联到每个
--   分区，252 审计轮实测 idx_session_turns_ts 单条就 94.2s（2026_09
--   79.8 万行 + 2026_10 12.8 万行）——**远超 30s，不加 SET LOCAL 必得
--   57014**。两条 CREATE INDEX 同处本事务，失败整体回滚不留部分状态，
--   但 psql_file 只按退出码记账 ⇒ 该迁移永远不被记为 applied，每次部署
--   重放、每次同样失败（与 50eb3bb88 修掉的 COMMENT 守卫击穿同族的永久
--   卡点形态）。858 同为无 statement_timeout 的建索引迁移，但建在 S4
--   起已停写的 request_logs 族上，代价可忽略，不构成本例。
SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '60s';

CREATE INDEX IF NOT EXISTS idx_session_turns_ts
    ON public.session_turns (ts);

DO $build_request_logs_ts$
BEGIN
  IF to_regclass('public._request_logs') IS NOT NULL THEN
    EXECUTE 'CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON public._request_logs (ts)';
  ELSE
    RAISE NOTICE 'migration 862: public._request_logs not found (skip request_logs leg)';
  END IF;
END
$build_request_logs_ts$;

COMMIT;

-- ⚠ COMMENT 必须与上面的创建腿**同守卫**（r1030 实测）：守卫掉的表如果
--   仍留一条无条件 COMMENT，整条脚本会在 COMMIT 之后以
--   `relation "idx_request_logs_ts" does not exist` 非零退出 —— 索引其实
--   已经建好，但运行器只认退出码，于是该迁移永远不被记为 applied，
--   每次 boot 重放、每次同样失败。凡「可能不存在的对象」都在此按
--   to_regclass 判定后再下 COMMENT。
DO $comment_862_indexes$
BEGIN
  IF to_regclass('public.idx_session_turns_ts') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_ts IS
      '862: session_turns 分区父表无谓词 (ts) 前导索引 —— canonical 视图窗口读臂与任何 ts 窗查询的承重索引；复合前导 (tenant_id, ts) 与部分索引 (ts,id) WHERE digest IS NULL 均不服务无租户约束的窗口形态。';
  END IF;
  IF to_regclass('public.idx_request_logs_ts') IS NOT NULL THEN
    COMMENT ON INDEX idx_request_logs_ts IS
      '862: request_logs 族（860 后 _request_logs）分区父表无谓词 (ts) 前导索引 —— V1FreezeCutoffSQL 母表臂 max(ts) 与 canonical 视图窗口读臂专用；替代全索引扫描。族 DROP 时随之消失。';
  END IF;
END
$comment_862_indexes$;
