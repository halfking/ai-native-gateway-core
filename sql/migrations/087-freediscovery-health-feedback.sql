-- Migration 087: FreeDiscovery template health feedback & auto-disable
-- 创建时间: 2026-09-15
-- 用途: 自动禁用连续扫描失败（≥3次）的模板，防止浪费上游 API 配额

BEGIN;

-- ============================================================================
-- 1. 添加健康反馈列
-- ============================================================================

ALTER TABLE public.provider_templates
    ADD COLUMN IF NOT EXISTS consecutive_scan_failures INT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_scan_failure_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ;

-- ============================================================================
-- 2. 索引优化
-- ============================================================================

-- 查询已自动禁用的模板（运维监控）
CREATE INDEX IF NOT EXISTS idx_provider_templates_health
    ON public.provider_templates(consecutive_scan_failures)
    WHERE enabled = FALSE AND auto_disabled_at IS NOT NULL;

-- ============================================================================
-- 3. 注释
-- ============================================================================

COMMENT ON COLUMN provider_templates.consecutive_scan_failures IS
    'Health feedback: consecutive scan failure count; resets to 0 on success, increments on failure, auto-disables at ≥3';

COMMENT ON COLUMN provider_templates.last_scan_failure_at IS
    'Health feedback: timestamp of the most recent scan failure';

COMMENT ON COLUMN provider_templates.auto_disabled_at IS
    'Health feedback: timestamp when template was auto-disabled due to consecutive failures; NULL when manually enabled/disabled';

-- ============================================================================
-- 4. 安全性
-- ============================================================================

-- 默认值 0 → 现有模板不受影响
-- 成功扫描重置计数器 → 瞬态故障不会累积
-- 操作员手动启用时清空 auto_disabled_at → 允许重试

COMMIT;
