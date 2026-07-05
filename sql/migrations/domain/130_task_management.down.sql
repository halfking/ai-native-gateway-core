-- Migration Rollback: 130_task_management.sql
-- Description: 回滚任务分组、任务分配和审批路由表
-- Date: 2026-07-01

-- ============================================================================
-- 删除 RLS 策略
-- ============================================================================
DROP POLICY IF EXISTS task_groups_tenant_isolation ON task_groups;
DROP POLICY IF EXISTS task_assignments_tenant_isolation ON task_assignments;
DROP POLICY IF EXISTS approval_routing_tenant_isolation ON approval_routing;

-- ============================================================================
-- 删除触发器
-- ============================================================================
DROP TRIGGER IF EXISTS update_task_groups_updated_at ON task_groups;
DROP TRIGGER IF EXISTS update_task_assignments_updated_at ON task_assignments;
DROP TRIGGER IF EXISTS update_approval_routing_updated_at ON approval_routing;

-- ============================================================================
-- 删除表
-- ============================================================================
DROP TABLE IF EXISTS task_assignments;
DROP TABLE IF EXISTS approval_routing;
DROP TABLE IF EXISTS task_groups;

-- ============================================================================
-- 删除函数（如果没有其他表使用）
-- ============================================================================
-- 注意：只有在确认没有其他表使用此函数时才删除
-- DROP FUNCTION IF EXISTS update_updated_at_column();
