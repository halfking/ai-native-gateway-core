-- ===========================================================================
-- File:          sql/migrations/startup/874_session_turns_client_request_id_idx.sql
-- Migration:     874
-- Database:      llm_gateway
-- Purpose:       给 session_turns 分区父表补 (client_request_id) 部分索引，
--                修复 S4 停写后 /api/logs/:id 的 client_request_id 兜底腿
--                （getLog 腿 2 / unified_detail.loadRequestLogMeta 兜底臂）
--                对 session 家族全分区的顺序扫描
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS)
-- Dependencies:  430（session_turns 家族）；862（分区父表索引级联先例）
--
-- Background（2026-10-18 154 生产实证）:
--   S4 停写（storage.request_logs_write_enabled=false，生产 2026-10-13 生效）
--   后新请求只落 session_turns 家族。admin/logs.go getLog 旧的单条
--   `(rl.request_id = $1 OR rl.client_request_id = $1)` 谓词在 734 视图上
--   让 planner 放弃 idx_session_turns(_hot)_request —— session 家族没有任何
--   client_request_id 索引，OR 无法拆成 BitmapOr，只能对全部月分区顺序扫描
--   （154 实测 session_turns_2026_09 79.8 万 + 2026_10 20.3 万 ≈ 100 万行 /
--   3.8GB，45s 都跑不完），/api/logs/:id 30s ctx 超时 500，request-detail
--   页核心数据全空。代码侧已把 OR 拆成两段顺序查询（同
--   unified_detail.loadRequestLogMeta 2026-08-27 优化同构）；本迁移补齐
--   client_request_id 等值谓词所需的索引，让拆分后的腿 2（以及 unified
--   的同型兜底臂）在 session 家族上同样走索引。
--
--   为什么是**部分索引**（WHERE client_request_id IS NOT NULL）：实测
--   2026_10 分区仅 10.9% 行带该列值（回显型客户端才写）。部分索引只含
--   这些行（~11 万条目），构建秒级；等值查询 client_request_id = $1 蕴含
--   NOT NULL，planner 可直接使用。
--
--   为什么建在父表而不是逐分区：普通 CREATE INDEX 级联到现存分区，且
--   未来 PARTITION OF 新月分区自动继承（862/846 先例）；逐分区建会漏
--   未来分区。
--
-- 上线代价：session_turns 是活写分区父表，普通 CREATE INDEX（父表上
--   CONCURRENTLY 被 PG 拒绝，842/846/862 实证）级联期间写被 ShareLock
--   阻塞、读照常。部分索引只扫非 NULL 行，252 同表 862 的全量 (ts) 索引
--   实测 94.2s，本索引规模 ~1/9，秒到十秒级，随部署窗口应用。共享 PG
--   服务端 statement_timeout=30s（849/855/856/862 均为此补 SET LOCAL），
--   必须放宽，否则 57014 击穿后迁移永不记账，成为永久卡点。
--
-- 回滚：.down.sql 按名删父级索引（级联删分区子索引），不动数据。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '60s';

CREATE INDEX IF NOT EXISTS idx_session_turns_client_request_id
    ON public.session_turns (client_request_id)
    WHERE client_request_id IS NOT NULL;

COMMIT;

-- COMMENT 与创建腿同守卫（r1030 实测：守卫掉的对象留无条件 COMMENT 会在
-- COMMIT 后以 relation does not exist 非零退出，迁移永不记账）。
DO $comment_874_index$
BEGIN
  IF to_regclass('public.idx_session_turns_client_request_id') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_client_request_id IS
      '874: session_turns 分区父表 (client_request_id) 部分索引 —— S4 停写后 getLog 腿 2 / unified_detail 兜底臂的 client_request_id 等值谓词承重索引；无它则 OR/等值查询对全部月分区顺序扫描（154 实测 100 万行/3.8GB >30s）。';
  END IF;
END
$comment_874_index$;
