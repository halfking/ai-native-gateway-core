-- 凭据状态管理模块数据库迁移
-- 创建凭据+模型状态监控节点注册表和状态日志表

-- 1. 状态监控节点注册表
CREATE TABLE IF NOT EXISTS credential_state_nodes (
    id SERIAL PRIMARY KEY,
    credential_id INT NOT NULL,
    raw_model_name TEXT NOT NULL,
    
    -- 节点状态
    node_status TEXT NOT NULL DEFAULT 'active', -- active, disabled
    
    -- 监控配置
    probe_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    probe_interval_seconds INT NOT NULL DEFAULT 3600,
    last_probe_at TIMESTAMPTZ,
    next_probe_at TIMESTAMPTZ,
    
    -- 创建/更新信息
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by TEXT, -- 'system' or 'admin:<user_id>'
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ,
    disabled_by TEXT,
    
    -- 约束
    UNIQUE (credential_id, raw_model_name),
    FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE CASCADE
);

CREATE INDEX idx_csn_credential_model ON credential_state_nodes(credential_id, raw_model_name);
CREATE INDEX idx_csn_active_probe ON credential_state_nodes(next_probe_at) 
    WHERE node_status = 'active' AND probe_enabled = TRUE;

COMMENT ON TABLE credential_state_nodes IS '凭据+模型状态监控节点注册表';
COMMENT ON COLUMN credential_state_nodes.node_status IS '节点状态：active=活跃监控, disabled=已禁用';
COMMENT ON COLUMN credential_state_nodes.created_by IS '创建者：system 或 admin:<user_id>';

-- 2. 状态日志表（用于存储实时状态更新，不影响主表）
CREATE TABLE IF NOT EXISTS credential_state_log (
    credential_id INT NOT NULL,
    raw_model_name TEXT NOT NULL,
    
    -- 状态字段
    available BOOLEAN,
    health_status TEXT,
    latency_ms INT,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_error TEXT,
    recover_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 主键
    PRIMARY KEY (credential_id, raw_model_name),
    FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE CASCADE
);

CREATE INDEX idx_csl_updated_at ON credential_state_log(updated_at);

COMMENT ON TABLE credential_state_log IS '凭据+模型状态日志表（实时状态更新）';

-- 3. 初始化现有节点（为所有活跃的凭据+模型绑定创建监控节点）
INSERT INTO credential_state_nodes 
    (credential_id, raw_model_name, node_status, probe_enabled, created_by)
SELECT DISTINCT 
    cmb.credential_id,
    pm.raw_model_name,
    'active',
    TRUE,
    'migration_035'
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c ON c.id = cmb.credential_id
WHERE c.lifecycle_status = 'active'
  AND cmb.available = TRUE
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;
