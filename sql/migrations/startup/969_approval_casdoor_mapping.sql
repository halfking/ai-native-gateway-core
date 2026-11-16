-- ===========================================================================
-- File:          sql/migrations/startup/969_approval_casdoor_mapping.sql
-- Migration:     969
-- Purpose:       approval_casdoor_mapping 表——llm-gateway 请求ID ↔ Casdoor 任务ID
--                的本地映射（双层审批 L2 的持久化面）。
--
-- ── 背景（★ 与 968 同型病：DDL 落在死通道 ⇒ 表在活库中不存在）──────────────
--
-- plugin-architecture 落地线（3f7f0dd5c → 240026a95）的
-- internal/approval/store.go（PgxApprovalStore）以
-- `approval_casdoor_mapping` 表为**写面**：
--   Save   → INSERT INTO approval_casdoor_mapping (...)
--   Get    → SELECT request_id, session_id, tenant_id, status, provider,
--                   casdoor_task_id, approved_by, approved_at, reason,
--                   created_at, expires_at
--   Update → UPDATE approval_casdoor_mapping SET status = ...
--
-- 而该表的 DDL 被写在了 db/migrations/202611150001_approval_casdoor_mapping.sql
--   ——★ db/migrations/README.md 明令：「这是**旧仓库时代的迁移残留**，
--     不是活通道」「新的 schema 变更一律走 sql/migrations/startup/ 编号通道
--     （并按五点同步纪律登记 installer），不要落到这里」「不要手工对真实库
--     执行本目录任何文件」。
--   ⇒ 该表在全新安装 / 211 升级序列中**永远不会被建**。
--
-- ★ 后果（P1）：
--   Casdoor 审批真正启用时，HandleWebhook / Save 的**第一个 INSERT 就报
--   42P01（relation "approval_casdoor_mapping" does not exist）**
--   ⇒ 审批映射无法落库 ⇒ webhook 裁决无法回写 ⇒ 审批闭环断在存储层。
--
-- ── 修法 ────────────────────────────────────────────────────────────────
--
--   收编进 startup 正典通道（969），并按五点同步纪律登记
--   （sql/tsv/runner.go/embeddata+//go:embed/db-changelog/211 通道+probe）。
--
-- ── 列形状与代码对齐 ────────────────────────────────────────────────────
--
--   ★ 本文件的列集合 = internal/approval/store.go 的读写列集合**逐字段一致**
--     （store.go:48-51 的 SELECT 列表12 列）⇒ 读面 Scan 实参数 = 列数。
--   ★ status 的 CHECK 取值域 = store.go 的 mapNativeStatus 映射目标域
--     （pending/approved/rejected/cancelled/expired/timeout）；
--     provider = native / casdoor。
--
-- ── 幂等性与回放 ────────────────────────────────────────────────────────
--
--   · CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS ⇒ 重放无副作用。
--   · RLS 策略 / 触发器用 DO $$ 包裹 + IF NOT EXISTS 判据 ⇒ 幂等。
--   · 本文件不含不可事务化语句 ⇒ 可单事务执行，无需逐文件事务豁免（标记语义见
--     installer/internal/dbinit 的 requiresNoTransaction）。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.approval_casdoor_mapping (
    request_id      VARCHAR(100) PRIMARY KEY,
    session_id      VARCHAR(100) NOT NULL,
    tenant_id       VARCHAR(100) NOT NULL,
    status          VARCHAR(20)  NOT NULL
 CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired', 'timeout')),
    provider        VARCHAR(20)  NOT NULL
                    CHECK (provider IN ('native', 'casdoor')),
    casdoor_task_id INTEGER,
    approved_by     VARCHAR(200),
    approved_at     TIMESTAMPTZ,
    reason          TEXT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ  NOT NULL,
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_casdoor_mapping_session ON public.approval_casdoor_mapping(session_id);
CREATE INDEX IF NOT EXISTS idx_casdoor_mapping_status  ON public.approval_casdoor_mapping(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_casdoor_mapping_tenant  ON public.approval_casdoor_mapping(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_casdoor_mapping_casdoor_task
    ON public.approval_casdoor_mapping(casdoor_task_id) WHERE casdoor_task_id IS NOT NULL;

-- ── RLS：租户隔离（沿用既有模式：current_setting('app.current_tenant')）──
--   ★ DO $$ 包裹 + IF NOT EXISTS 判据（CREATE POLICY 没有 IF NOT EXISTS 语法）。
ALTER TABLE public.approval_casdoor_mapping ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename  = 'approval_casdoor_mapping'
          AND policyname = 'approval_casdoor_mapping_tenant_isolation'
    ) THEN
        CREATE POLICY approval_casdoor_mapping_tenant_isolation
            ON public.approval_casdoor_mapping
            USING (tenant_id = current_setting('app.current_tenant', true));
    END IF;
END
$$;

-- ── updated_at 触发器（幂等：函数用 CREATE OR REPLACE，触发器用 DO 判据）──
CREATE OR REPLACE FUNCTION public.update_approval_casdoor_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger t
        JOIN pg_class c ON c.oid = t.tgrelid
        WHERE c.relname = 'approval_casdoor_mapping'
          AND t.tgname = 'trigger_update_approval_casdoor_mapping_timestamp'
          AND NOT t.tgisinternal
    ) THEN
        CREATE TRIGGER trigger_update_approval_casdoor_mapping_timestamp
            BEFORE UPDATE ON public.approval_casdoor_mapping
            FOR EACH ROW
            EXECUTE FUNCTION public.update_approval_casdoor_mapping_timestamp();
    END IF;
END
$$;

COMMENT ON TABLE public.approval_casdoor_mapping IS
 'Maps llm-gateway approval requests to Casdoor tasks for dual-layer approval architecture';
COMMENT ON COLUMN public.approval_casdoor_mapping.request_id IS 'llm-gateway request ID (unique)';
COMMENT ON COLUMN public.approval_casdoor_mapping.casdoor_task_id IS
 'Casdoor platform task ID (null for native approvals)';
COMMENT ON COLUMN public.approval_casdoor_mapping.provider IS
 'Approval provider: native (L1 fast) or casdoor (L2 complex)';

COMMIT;
