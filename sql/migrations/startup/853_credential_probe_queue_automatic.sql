-- ===========================================================================
-- File:          sql/migrations/startup/853_credential_probe_queue_automatic.sql
-- Migration:     853
-- Database:      llm_gateway
-- Purpose:       credential_probe_queue.automatic 列收编——deploy 链 V360
--                的列集对账（startup 镜像链从未拿到过的形状）
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS，
--                存量库零行为变化)
-- Dependencies:  489/490_credential_probe_queue_*（队列表本体，2026-10-12
--                补登进本通道）；deploy/sql/migrations/
--                V360__credential_probe_queue_automatic.sql（收编来源）
--
-- Background:
--   bg/probe_queue.go 自 e2d3490e38（2026-08-28）起，enqueue/claim 两条
--   SQL 都按名字引用 credential_probe_queue.automatic（manual/automatic
--   任务的资格门与审计列），但该列只存在于 deploy 链 V360——startup 链
--   的 489/490（343/347/349 的 startup 镜像）从未收录它，全仓 startup/
--   domain 迁移均无此列的 ADD。
--
--   部署链（V359 血统）的库按历史顺序跑过 V360 不受影响；fresh-install
--   链与 host-first 通道的库上 ProbeQueueWorker 的 claim 扫描每秒 42703：
--   column q.automatic does not exist（2026-10-12 本机 host-first 实测，
--   489/490 补登后立即暴露——探针队列从「表不存在 42P01」前进到「列
--   不存在 42703」）。
--
--   本迁移把 V360 的事实的列集收编进 canonical 链（对齐 803/852 的
--   收编先例）：列类型/默认值/部分索引与 V360 逐项一致；存量库零行为
--   变化。
-- ===========================================================================

BEGIN;

ALTER TABLE public.credential_probe_queue
    ADD COLUMN IF NOT EXISTS automatic BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN public.credential_probe_queue.automatic IS
    '853 (deploy-chain V360 收编): TRUE for scheduler/system-generated probes; FALSE for explicit manual probes. Automatic tasks are eligibility-gated at enqueue, claim, and execution.';

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_automatic_claim
    ON public.credential_probe_queue (automatic, priority DESC, next_run_at, id)
    WHERE status = 'ready';

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_name = 'credential_probe_queue' AND column_name = 'automatic'
