-- ===========================================================================
-- File:          db/migrations/014_create_releases_tables.sql
-- Database:      llm_gateway
-- Purpose:       版本管理表（releases, release_files, release_tests）
--
-- Status:        active
-- Idempotent:    YES
--
-- Changelog:
--   2026-07-23  v1.0  Initial creation
-- ===========================================================================
--
-- Execution:
--   psql -h localhost -U postgres -d llm_gateway -f db/migrations/014_create_releases_tables.sql
--
-- Verification:
--   \dt releases
--   SELECT * FROM releases LIMIT 1;
--
-- Rollback:
--   DROP TABLE IF EXISTS release_tests CASCADE;
--   DROP TABLE IF EXISTS release_files CASCADE;
--   DROP TABLE IF EXISTS releases CASCADE;
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- ============================================================================
-- 1. releases 表 - 版本主表
-- ============================================================================

CREATE TABLE IF NOT EXISTS releases (
    id BIGSERIAL PRIMARY KEY,
    
    -- 版本信息
    version VARCHAR(20) NOT NULL,                    -- 如 2.4.7
    full_version VARCHAR(100) NOT NULL UNIQUE,       -- 如 2.4.7-1347-3970dbc1-20260723
    build_seq INTEGER NOT NULL,                      -- 构建序号
    git_sha VARCHAR(40) NOT NULL,                    -- Git commit SHA
    build_date VARCHAR(8) NOT NULL,                  -- 构建日期 YYYYMMDD
    
    -- 发布信息
    release_type VARCHAR(20) NOT NULL DEFAULT 'stable',  -- stable/beta/alpha/rc
    release_date TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    release_notes TEXT,                              -- 发布说明
    changelog_url TEXT,                              -- 变更日志 URL
    
    -- 兼容性
    upgrade_from_versions JSONB DEFAULT '[]'::jsonb, -- 可从哪些版本升级
    breaking_changes JSONB DEFAULT '[]'::jsonb,      -- 重大变更
    
    -- 系统要求
    min_postgres_version VARCHAR(20),
    min_redis_version VARCHAR(20),
    min_go_version VARCHAR(20),
    min_node_version VARCHAR(20),
    
    -- 状态
    status VARCHAR(20) NOT NULL DEFAULT 'draft',     -- draft/published/deprecated
    is_public BOOLEAN NOT NULL DEFAULT FALSE,        -- 是否公开（customer版本）
    is_latest BOOLEAN NOT NULL DEFAULT FALSE,        -- 是否最新版本
    
    -- 统计
    download_count INTEGER NOT NULL DEFAULT 0,
    view_count INTEGER NOT NULL DEFAULT 0,
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(100),
    updated_by VARCHAR(100),
    
    -- 索引
    CONSTRAINT releases_version_check CHECK (version ~ '^\d+\.\d+\.\d+$')
);

CREATE INDEX IF NOT EXISTS idx_releases_version ON releases(version);
CREATE INDEX IF NOT EXISTS idx_releases_status ON releases(status);
CREATE INDEX IF NOT EXISTS idx_releases_release_type ON releases(release_type);
CREATE INDEX IF NOT EXISTS idx_releases_is_public ON releases(is_public);
CREATE INDEX IF NOT EXISTS idx_releases_release_date ON releases(release_date DESC);

COMMENT ON TABLE releases IS '版本发布主表';
COMMENT ON COLUMN releases.version IS '版本号，如 2.4.7';
COMMENT ON COLUMN releases.full_version IS '完整版本号，如 2.4.7-1347-3970dbc1-20260723';
COMMENT ON COLUMN releases.release_type IS '发布类型: stable/beta/alpha/rc';
COMMENT ON COLUMN releases.status IS '状态: draft/published/deprecated';
COMMENT ON COLUMN releases.is_public IS '是否公开（customer版本可见）';

-- ============================================================================
-- 2. release_files 表 - 发布文件
-- ============================================================================

CREATE TABLE IF NOT EXISTS release_files (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联
    release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    
    -- 文件信息
    filename VARCHAR(255) NOT NULL,                  -- 文件名
    file_path TEXT NOT NULL,                         -- Cloudreve 上的路径
    file_size BIGINT NOT NULL,                       -- 文件大小（字节）
    file_hash VARCHAR(64) NOT NULL,                  -- SHA256 哈希
    
    -- 平台信息
    platform VARCHAR(20) NOT NULL,                   -- host/docker
    os VARCHAR(20) NOT NULL,                         -- linux/darwin/windows
    arch VARCHAR(20) NOT NULL,                       -- amd64/arm64
    
    -- 下载信息
    download_url TEXT,                               -- 下载 URL
    share_url TEXT,                                  -- 分享链接
    download_count INTEGER NOT NULL DEFAULT 0,
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 唯一约束
    CONSTRAINT release_files_unique UNIQUE (release_id, filename)
);

CREATE INDEX IF NOT EXISTS idx_release_files_release_id ON release_files(release_id);
CREATE INDEX IF NOT EXISTS idx_release_files_platform ON release_files(platform, os, arch);

COMMENT ON TABLE release_files IS '版本发布文件表';
COMMENT ON COLUMN release_files.platform IS '平台: host（主机安装）/docker（容器部署）';
COMMENT ON COLUMN release_files.file_hash IS 'SHA256 哈希值';

-- ============================================================================
-- 3. release_tests 表 - 测试记录
-- ============================================================================

CREATE TABLE IF NOT EXISTS release_tests (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联
    release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    
    -- 测试信息
    test_type VARCHAR(50) NOT NULL,                  -- unit/integration/e2e/docker/245
    test_name VARCHAR(255) NOT NULL,
    test_status VARCHAR(20) NOT NULL,                -- passed/failed/skipped
    
    -- 测试结果
    test_output TEXT,                                -- 测试输出
    test_duration INTEGER,                           -- 测试时长（秒）
    error_message TEXT,                              -- 错误信息
    
    -- 环境信息
    test_environment VARCHAR(50),                    -- local/ci/245/docker
    tester VARCHAR(100),                             -- 测试者
    
    -- 元数据
    tested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- 索引
    CONSTRAINT release_tests_status_check CHECK (test_status IN ('passed', 'failed', 'skipped'))
);

CREATE INDEX IF NOT EXISTS idx_release_tests_release_id ON release_tests(release_id);
CREATE INDEX IF NOT EXISTS idx_release_tests_status ON release_tests(test_status);
CREATE INDEX IF NOT EXISTS idx_release_tests_type ON release_tests(test_type);

COMMENT ON TABLE release_tests IS '版本测试记录表';
COMMENT ON COLUMN release_tests.test_type IS '测试类型: unit/integration/e2e/docker/245';
COMMENT ON COLUMN release_tests.test_status IS '测试状态: passed/failed/skipped';

-- ============================================================================
-- 4. 触发器 - 自动更新 updated_at
-- ============================================================================

CREATE OR REPLACE FUNCTION update_releases_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_releases_updated_at ON releases;
CREATE TRIGGER trigger_releases_updated_at
    BEFORE UPDATE ON releases
    FOR EACH ROW
    EXECUTE FUNCTION update_releases_updated_at();

DROP TRIGGER IF EXISTS trigger_release_files_updated_at ON release_files;
CREATE TRIGGER trigger_release_files_updated_at
    BEFORE UPDATE ON release_files
    FOR EACH ROW
    EXECUTE FUNCTION update_releases_updated_at();

-- ============================================================================
-- 5. 视图 - 发布概览
-- ============================================================================

CREATE OR REPLACE VIEW v_releases_overview AS
SELECT 
    r.id,
    r.version,
    r.full_version,
    r.release_type,
    r.release_date,
    r.status,
    r.is_public,
    r.is_latest,
    r.download_count AS total_downloads,
    COUNT(DISTINCT rf.id) AS file_count,
    SUM(rf.file_size) AS total_size,
    COUNT(DISTINCT rt.id) FILTER (WHERE rt.test_status = 'passed') AS passed_tests,
    COUNT(DISTINCT rt.id) FILTER (WHERE rt.test_status = 'failed') AS failed_tests,
    COUNT(DISTINCT rt.id) AS total_tests
FROM releases r
LEFT JOIN release_files rf ON rf.release_id = r.id
LEFT JOIN release_tests rt ON rt.release_id = r.id
GROUP BY r.id, r.version, r.full_version, r.release_type, r.release_date, 
         r.status, r.is_public, r.is_latest, r.download_count;

COMMENT ON VIEW v_releases_overview IS '版本发布概览视图';

-- ============================================================================
-- 6. 示例数据（可选）
-- ============================================================================

-- 插入示例版本
INSERT INTO releases (
    version, full_version, build_seq, git_sha, build_date,
    release_type, release_notes, status, is_public, is_latest,
    min_postgres_version, min_redis_version
) VALUES (
    '2.4.7', '2.4.7-1347-3970dbc1-20260723', 1347, '3970dbc1', '20260723',
    'stable', '修复若干bug，优化性能', 'published', true, true,
    '14', '6'
) ON CONFLICT (full_version) DO NOTHING;

COMMIT;

-- 验证
SELECT 'Tables created successfully' AS status;
SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename LIKE 'release%';

