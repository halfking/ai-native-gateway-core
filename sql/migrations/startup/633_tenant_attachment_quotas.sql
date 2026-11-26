-- ============================================================
-- 迁移 633: 租户附件配额（tenant_attachment_quotas）
-- 目的：P0 安全修复 —— 防止恶意租户通过无限制上传耗尽存储空间
-- 日期：2026-11-26（P0.2 附件管线轮引入；2026-11-27 审计轮投递收口）
-- 使用方：domains/attachments/quota.go (QuotaManager)
--
-- ★ 投递缺口史：本迁移最初落在 sql/migrations/startup/up/
--   （★ 该目录整体不被任何通道消费 —— 389 同款先例）⇒ 部署与新装都不会建表
--   ⇒ quota.go 的 INSERT/SELECT 恒 42P01。
--   2026-11-27 审计轮搬到 canonical 目录 ＋ 进两条投递通道（同 999b/999c/999d 修法）。
-- ★ 结构：单表 ＋ 单索引，全部 IF NOT EXISTS 幂等；自带 BEGIN/COMMIT
--   ＋ lock_timeout/statement_timeout ＋ schema_migrations 自登记（695-704 定式）。
-- ============================================================

BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.tenant_attachment_quotas (
    tenant_id TEXT PRIMARY KEY,
    current_bytes BIGINT NOT NULL DEFAULT 0,
    files_today INTEGER NOT NULL DEFAULT 0,
    last_reset_date DATE NOT NULL DEFAULT CURRENT_DATE,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 索引：按更新时间查询（监控、清理）
CREATE INDEX IF NOT EXISTS idx_tenant_attachment_quotas_updated_at
    ON public.tenant_attachment_quotas(updated_at);

COMMENT ON TABLE public.tenant_attachment_quotas IS
    'P0 security fix: Per-tenant attachment storage quotas to prevent DoS via unlimited uploads';
COMMENT ON COLUMN public.tenant_attachment_quotas.tenant_id IS
    'Tenant identifier (from auth.keyInfo.TenantID)';
COMMENT ON COLUMN public.tenant_attachment_quotas.current_bytes IS
    'Total attachment storage used by this tenant (bytes)';
COMMENT ON COLUMN public.tenant_attachment_quotas.files_today IS
    'Number of files uploaded today (resets daily)';
COMMENT ON COLUMN public.tenant_attachment_quotas.last_reset_date IS
    'Date when files_today was last reset (for daily limit enforcement)';
COMMENT ON COLUMN public.tenant_attachment_quotas.updated_at IS
    'Last update timestamp (for monitoring and cleanup)';

INSERT INTO public.schema_migrations (version, description)
VALUES ('633', 'tenant attachment quotas: per-tenant storage bytes + daily file count with auto-reset (P0 upload-DoS fix)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
