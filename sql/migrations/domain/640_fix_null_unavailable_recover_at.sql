-- Migration: 640_fix_null_unavailable_recover_at
-- Purpose: 清理历史遗留的 NULL unavailable_recover_at 行，使自动恢复机制能够正常工作
-- Issue: ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md 根因#2
-- Date: 2026-09-02

-- ============================================================================
-- 问题背景
-- ============================================================================
-- credential_model_bindings.available=FALSE 但 unavailable_recover_at=NULL 的行
-- 无法被 bg/credential_recovery.go:RecoverExpired() 和 credentialhealth/checker.go
-- 的恢复SQL选中，因为这些SQL都要求:
--   WHERE unavailable_recover_at IS NOT NULL AND unavailable_recover_at <= now()
--
-- 历史根因：
-- 1. 2026-07-22之前，KindAuth 写入 availability_recover_at=NULL
-- 2. 2026-07-27之前，model_probe_broken 不设置 unavailable_recover_at
-- 3. 某些错误路径可能遗漏了该字段的设置
--
-- 本次修复为所有符合条件的行设置合理的兜底恢复时间。

-- ============================================================================
-- 修复策略
-- ============================================================================
-- 1. 对有 unavailable_at 的行：设置为 unavailable_at + 30分钟（标准冷却）
-- 2. 对连 unavailable_at 都没有的行：设置为 now() + 5分钟（快速重试）
-- 3. 排除 manual* 原因（管理员手动标记的行不应自动恢复）
-- 4. 排除 admin_protected=TRUE 的行

BEGIN;

-- This is a forward-only data repair. Record counts in the durable migration
-- audit table; do not use a temporary backup table as a cross-transaction
-- rollback mechanism.
CREATE TABLE IF NOT EXISTS schema_migration_audit (
    migration_id   TEXT PRIMARY KEY,
    applied_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    row_count      BIGINT NOT NULL DEFAULT 0,
    note           TEXT NOT NULL DEFAULT ''
);

WITH candidates AS (
    SELECT count(*)::bigint AS row_count
    FROM credential_model_bindings
    WHERE available = FALSE
      AND unavailable_recover_at IS NULL
      AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
      AND COALESCE(admin_protected, FALSE) = FALSE
),
recorded AS (
    INSERT INTO schema_migration_audit (migration_id, row_count, note)
    SELECT '640_fix_null_unavailable_recover_at', row_count,
           'forward-only repair candidates before update'
    FROM candidates
    ON CONFLICT (migration_id) DO UPDATE
      SET applied_at = EXCLUDED.applied_at,
          row_count = EXCLUDED.row_count,
          note = EXCLUDED.note
    RETURNING migration_id
)
SELECT migration_id FROM recorded;

-- ============================================================================
-- 修复#1: 有 unavailable_at 的行 → unavailable_at + 30分钟
-- ============================================================================
UPDATE credential_model_bindings
SET
    unavailable_recover_at = unavailable_at + INTERVAL '30 minutes',
    updated_at = now()
WHERE available = FALSE
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL
  AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
  AND COALESCE(admin_protected, FALSE) = FALSE;

-- ============================================================================
-- 修复#2: 连 unavailable_at 都没有的行 → now() + 5分钟（快速重试）
-- ============================================================================
UPDATE credential_model_bindings
SET
    unavailable_recover_at = now() + INTERVAL '5 minutes',
    unavailable_at = now(),  -- 同时补齐 unavailable_at 以记录时间点
    updated_at = now()
WHERE available = FALSE
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NULL
  AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
  AND COALESCE(admin_protected, FALSE) = FALSE;

-- ============================================================================
-- 验证结果
-- ============================================================================
DO $$
DECLARE
    remaining_null_count INT;
    fixed_count INT;
BEGIN
    -- 检查剩余的NULL行（应该只有manual和admin_protected）
    SELECT COUNT(*) INTO remaining_null_count
    FROM credential_model_bindings
    WHERE available = FALSE
      AND unavailable_recover_at IS NULL;

    -- 读取修复前记录的候选行数
    SELECT row_count INTO fixed_count
    FROM schema_migration_audit
    WHERE migration_id = '640_fix_null_unavailable_recover_at';

    RAISE NOTICE 'Migration 640: Fixed % rows, remaining NULL rows (manual/protected): %',
                 fixed_count, remaining_null_count;

    -- 断言：剩余NULL行应该都是manual或admin_protected
    IF EXISTS (
        SELECT 1 FROM credential_model_bindings
        WHERE available = FALSE
          AND unavailable_recover_at IS NULL
          AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
          AND COALESCE(admin_protected, FALSE) = FALSE
    ) THEN
        RAISE EXCEPTION 'Migration 640: Found unexpected NULL rows after fix';
    END IF;
END $$;

COMMIT;

-- ==========================================================================
-- No down migration: this repair intentionally changes historical state.
-- If reversal is required, restore from the database backup approved by DBA.
