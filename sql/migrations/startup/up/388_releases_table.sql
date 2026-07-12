-- Migration: 388_releases_table
-- Description: Create releases and related tables for autoupdate feature
-- FIXED: Use TEXT types to match 376_autoupdate.sql

-- Create releases table
CREATE TABLE IF NOT EXISTS releases (
    id BIGSERIAL PRIMARY KEY,
    version TEXT NOT NULL UNIQUE,
    build_seq INT NOT NULL,
    channel TEXT NOT NULL DEFAULT 'stable',
    title TEXT NOT NULL,
    description TEXT,
    changelog TEXT,
    image_tag TEXT NOT NULL,
    image_digest TEXT,
    min_version TEXT,
    mandatory BOOLEAN NOT NULL DEFAULT false,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    CONSTRAINT releases_channel_check CHECK (channel IN ('stable', 'beta', 'canary'))
);

CREATE INDEX IF NOT EXISTS idx_releases_channel_published ON releases (channel, published_at, build_seq DESC) WHERE published_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_releases_build_seq ON releases (build_seq DESC);

-- Create gray_release_rules table
CREATE TABLE IF NOT EXISTS gray_release_rules (
    id BIGSERIAL PRIMARY KEY,
    release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    percent INT NOT NULL CHECK (percent >= 0 AND percent <= 100),
    selectors JSONB,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gray_release_rules_phase_check CHECK (phase IN ('canary', 'batch_1', 'batch_2', 'batch_3', 'full')),
    CONSTRAINT gray_release_rules_status_check CHECK (status IN ('active', 'paused', 'completed'))
);

CREATE INDEX IF NOT EXISTS idx_gray_release_rules_release_id ON gray_release_rules (release_id);

-- Create upgrade_logs table
CREATE TABLE IF NOT EXISTS upgrade_logs (
    id BIGSERIAL PRIMARY KEY,
    instance_id TEXT NOT NULL,
    old_version TEXT,
    new_version TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    error_message TEXT,
    duration_ms INT,
    retry_count INT NOT NULL DEFAULT 0,
    CONSTRAINT upgrade_logs_status_check CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'failed', 'rolled_back'))
);

CREATE INDEX IF NOT EXISTS idx_upgrade_logs_instance_id ON upgrade_logs (instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_upgrade_logs_status ON upgrade_logs (status, started_at DESC);

-- Create instance_release_status table
CREATE TABLE IF NOT EXISTS instance_release_status (
    release_id BIGINT NOT NULL,
    instance_id TEXT NOT NULL PRIMARY KEY,
    status TEXT NOT NULL,
    version TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    error TEXT,
    retry_count INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT instance_release_status_status_check CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'failed', 'rolled_back'))
);

CREATE INDEX IF NOT EXISTS idx_instance_release_status_release_id ON instance_release_status (release_id);
CREATE INDEX IF NOT EXISTS idx_instance_release_status_status ON instance_release_status (status);

-- Add current_version and build_seq to gateway_instances if not exists
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'gateway_instances' AND column_name = 'current_version'
    ) THEN
        ALTER TABLE gateway_instances ADD COLUMN current_version TEXT;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'gateway_instances' AND column_name = 'build_seq'
    ) THEN
        ALTER TABLE gateway_instances ADD COLUMN build_seq INT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_gateway_instances_version ON gateway_instances (current_version, build_seq);

COMMENT ON TABLE releases IS 'Release versions catalog';
COMMENT ON TABLE gray_release_rules IS 'Gray release rules for phased rollout';
COMMENT ON TABLE upgrade_logs IS 'Upgrade execution logs';
COMMENT ON TABLE instance_release_status IS 'Current upgrade status per instance';
