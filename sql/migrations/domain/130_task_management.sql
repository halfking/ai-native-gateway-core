-- Migration: 130_task_management.sql
-- Description: 创建任务分组、任务分配和审批路由表
-- Date: 2026-07-01

-- ============================================================================
-- 任务组表
-- ============================================================================
CREATE TABLE IF NOT EXISTS task_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    tenant_id VARCHAR(255) NOT NULL,
    type VARCHAR(50) NOT NULL CHECK (type IN ('project', 'department', 'custom')),
    managers JSONB NOT NULL DEFAULT '[]'::jsonb,     -- 管理员列表 ["user_id1", "user_id2"]
    members JSONB NOT NULL DEFAULT '[]'::jsonb,      -- 成员列表 ["user_id1", "user_id2"]
    rules JSONB NOT NULL DEFAULT '{}'::jsonb,        -- 分组规则
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_task_groups_tenant_id ON task_groups(tenant_id);
CREATE INDEX IF NOT EXISTS idx_task_groups_type ON task_groups(type);
CREATE INDEX IF NOT EXISTS idx_task_groups_created_at ON task_groups(created_at DESC);

-- 注释
COMMENT ON TABLE task_groups IS '任务组表：用于组织和管理审批任务';
COMMENT ON COLUMN task_groups.id IS '任务组唯一标识';
COMMENT ON COLUMN task_groups.name IS '任务组名称';
COMMENT ON COLUMN task_groups.description IS '任务组描述';
COMMENT ON COLUMN task_groups.tenant_id IS '租户ID';
COMMENT ON COLUMN task_groups.type IS '任务组类型：project（项目）、department（部门）、custom（自定义）';
COMMENT ON COLUMN task_groups.managers IS '管理员列表（JSON数组）';
COMMENT ON COLUMN task_groups.members IS '成员列表（JSON数组）';
COMMENT ON COLUMN task_groups.rules IS '分组规则（JSON对象）';

-- ============================================================================
-- 任务分配表
-- ============================================================================
CREATE TABLE IF NOT EXISTS task_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id VARCHAR(255) NOT NULL,                   -- 关联的任务ID（如approval_id）
    task_type VARCHAR(50) NOT NULL CHECK (task_type IN ('approval', 'review', 'monitor', 'alert')),
    group_id UUID REFERENCES task_groups(id) ON DELETE CASCADE,
    assignee_id VARCHAR(255) NOT NULL,               -- 分配给的用户ID
    assignee_name VARCHAR(255),                      -- 分配给的用户名称（冗余字段）
    status VARCHAR(50) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed', 'canceled')),
    priority INT NOT NULL DEFAULT 0,                 -- 优先级（数值越大优先级越高）
    metadata JSONB DEFAULT '{}'::jsonb,              -- 附加元数据
    assigned_at TIMESTAMP NOT NULL DEFAULT NOW(),
    started_at TIMESTAMP,                            -- 开始处理时间
    completed_at TIMESTAMP,                          -- 完成时间
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_task_assignments_task_id ON task_assignments(task_id);
CREATE INDEX IF NOT EXISTS idx_task_assignments_assignee_id ON task_assignments(assignee_id);
CREATE INDEX IF NOT EXISTS idx_task_assignments_group_id ON task_assignments(group_id);
CREATE INDEX IF NOT EXISTS idx_task_assignments_status ON task_assignments(status);
CREATE INDEX IF NOT EXISTS idx_task_assignments_assigned_at ON task_assignments(assigned_at DESC);

-- 注释
COMMENT ON TABLE task_assignments IS '任务分配表：记录任务分配给具体人员的情况';
COMMENT ON COLUMN task_assignments.task_id IS '关联的任务ID';
COMMENT ON COLUMN task_assignments.task_type IS '任务类型：approval（审批）、review（审查）、monitor（监控）、alert（告警）';
COMMENT ON COLUMN task_assignments.group_id IS '所属任务组ID';
COMMENT ON COLUMN task_assignments.assignee_id IS '分配给的用户ID';
COMMENT ON COLUMN task_assignments.status IS '任务状态';
COMMENT ON COLUMN task_assignments.priority IS '优先级（数值越大优先级越高）';

-- ============================================================================
-- 审批路由表
-- ============================================================================
CREATE TABLE IF NOT EXISTS approval_routing (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id VARCHAR(255) NOT NULL,
    risk_level VARCHAR(50) NOT NULL CHECK (risk_level IN ('low', 'medium', 'high', 'critical')),
    group_id UUID REFERENCES task_groups(id) ON DELETE CASCADE,
    priority INT NOT NULL DEFAULT 0,                 -- 优先级（同一租户下多个路由规则时使用）
    enabled BOOLEAN NOT NULL DEFAULT true,           -- 是否启用
    conditions JSONB DEFAULT '{}'::jsonb,            -- 路由条件（JSON对象）
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, risk_level, group_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_approval_routing_tenant_id ON approval_routing(tenant_id);
CREATE INDEX IF NOT EXISTS idx_approval_routing_risk_level ON approval_routing(risk_level);
CREATE INDEX IF NOT EXISTS idx_approval_routing_enabled ON approval_routing(enabled);

-- 注释
COMMENT ON TABLE approval_routing IS '审批路由表：根据租户和风险级别将审批任务路由到对应的任务组';
COMMENT ON COLUMN approval_routing.tenant_id IS '租户ID';
COMMENT ON COLUMN approval_routing.risk_level IS '风险级别';
COMMENT ON COLUMN approval_routing.group_id IS '路由到的任务组ID';
COMMENT ON COLUMN approval_routing.priority IS '优先级（数值越大优先级越高）';
COMMENT ON COLUMN approval_routing.enabled IS '是否启用此路由规则';
COMMENT ON COLUMN approval_routing.conditions IS '附加路由条件（JSON对象）';

-- ============================================================================
-- 触发器：自动更新 updated_at
-- ============================================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_task_groups_updated_at
    BEFORE UPDATE ON task_groups
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_task_assignments_updated_at
    BEFORE UPDATE ON task_assignments
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_approval_routing_updated_at
    BEFORE UPDATE ON approval_routing
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- 行级安全策略（RLS）
-- ============================================================================
-- 启用 RLS
ALTER TABLE task_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_routing ENABLE ROW LEVEL SECURITY;

-- task_groups RLS 策略
CREATE POLICY task_groups_tenant_isolation ON task_groups
    USING (
        tenant_id = COALESCE(
            NULLIF(current_setting('app.current_tenant', true), ''),
            'default'
        )
        OR current_setting('app.current_role', true) = 'super_admin'
    );

-- task_assignments RLS 策略
CREATE POLICY task_assignments_tenant_isolation ON task_assignments
    USING (
        EXISTS (
            SELECT 1 FROM task_groups
            WHERE task_groups.id = task_assignments.group_id
            AND task_groups.tenant_id = COALESCE(
                NULLIF(current_setting('app.current_tenant', true), ''),
                'default'
            )
        )
        OR current_setting('app.current_role', true) = 'super_admin'
    );

-- approval_routing RLS 策略
CREATE POLICY approval_routing_tenant_isolation ON approval_routing
    USING (
        tenant_id = COALESCE(
            NULLIF(current_setting('app.current_tenant', true), ''),
            'default'
        )
        OR current_setting('app.current_role', true) = 'super_admin'
    );

-- ============================================================================
-- 示例数据（可选）
-- ============================================================================
-- 创建默认任务组
INSERT INTO task_groups (name, description, tenant_id, type, managers, members, rules)
VALUES (
    'Default Approval Group',
    '默认审批组，用于处理所有未匹配到特定规则的审批任务',
    'default',
    'custom',
    '["admin"]'::jsonb,
    '["admin"]'::jsonb,
    '{}'::jsonb
) ON CONFLICT DO NOTHING;

-- 创建默认路由规则
INSERT INTO approval_routing (tenant_id, risk_level, group_id, priority, enabled)
SELECT 
    'default',
    risk_level,
    (SELECT id FROM task_groups WHERE name = 'Default Approval Group' LIMIT 1),
    0,
    true
FROM (VALUES ('low'), ('medium'), ('high'), ('critical')) AS t(risk_level)
ON CONFLICT DO NOTHING;
