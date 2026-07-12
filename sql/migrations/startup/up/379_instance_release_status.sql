-- 379_instance_release_status.sql
-- 为 gateway_instances 添加 current_version 字段（如果不存在）
-- 同时确保 instance_release_status 表的完整性

-- 为 gateway_instances 添加 current_version 字段（与 version 字段独立）
-- version: 启动时上报的版本（实例当前运行的版本）
-- current_version: 升级后的目标版本（升级成功后更新）
ALTER TABLE gateway_instances ADD COLUMN IF NOT EXISTS current_version TEXT;

-- 初始化 current_version（如果为空，则设置为 version）
UPDATE gateway_instances SET current_version = version WHERE current_version IS NULL;

-- 确保 instance_release_status 表存在（376_autoupdate.sql 已创建，此处为幂等性）
CREATE TABLE IF NOT EXISTS instance_release_status (
    id BIGSERIAL PRIMARY KEY,
    instance_id TEXT NOT NULL UNIQUE,
    release_id BIGINT,
    from_version TEXT,
    to_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'rolled_back', 'failed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    duration_ms INT,
    error TEXT,
    retry_count INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 如果 376 创建的表没有 from_version、duration_ms，则添加
ALTER TABLE instance_release_status ADD COLUMN IF NOT EXISTS from_version TEXT;
ALTER TABLE instance_release_status ADD COLUMN IF NOT EXISTS duration_ms INT;

-- 如果 376 创建的表缺少 id 主键（只有 instance_id PRIMARY KEY），则需要调整
-- 检查是否存在 id 列，如果不存在则添加
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns 
                   WHERE table_name = 'instance_release_status' AND column_name = 'id') THEN
        -- 添加 id 列
        ALTER TABLE instance_release_status ADD COLUMN id BIGSERIAL;
        -- 删除旧的主键约束
        ALTER TABLE instance_release_status DROP CONSTRAINT IF EXISTS instance_release_status_pkey;
        -- 设置新的主键
        ALTER TABLE instance_release_status ADD PRIMARY KEY (id);
        -- 确保 instance_id 唯一
        CREATE UNIQUE INDEX IF NOT EXISTS idx_irs_instance_id_unique ON instance_release_status (instance_id);
    END IF;
END $$;

-- 创建索引（如果不存在）
CREATE INDEX IF NOT EXISTS idx_irs_instance ON instance_release_status (instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_irs_status ON instance_release_status (status);
CREATE INDEX IF NOT EXISTS idx_irs_release ON instance_release_status (release_id) WHERE release_id IS NOT NULL;

-- 为 upgrade_logs 表添加外键引用（如果不存在）
-- 确保 instance_id 引用 gateway_instances.instance_id
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.table_constraints 
                   WHERE constraint_name = 'fk_upgrade_logs_instance' 
                   AND table_name = 'upgrade_logs') THEN
        ALTER TABLE upgrade_logs 
        ADD CONSTRAINT fk_upgrade_logs_instance 
        FOREIGN KEY (instance_id) REFERENCES gateway_instances(instance_id) ON DELETE CASCADE;
    END IF;
END $$;
