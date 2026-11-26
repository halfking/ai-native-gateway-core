-- Migration 633 Rollback: Drop Tenant Attachment Quotas
-- P0 安全修复 (2026-11-26)
--
-- WARNING: This will delete all quota tracking data!

DROP INDEX IF EXISTS idx_tenant_attachment_quotas_updated_at;
DROP TABLE IF EXISTS tenant_attachment_quotas;
