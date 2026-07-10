-- 374_license_devices.sql
-- License设备激活记录与离线激活请求表
--
-- 补齐 Phase 1 缺失的设备管理表，支持：
--   1. 设备激活与注销记录
--   2. 心跳更新
--   3. 离线激活请求流程

CREATE TABLE IF NOT EXISTS license_devices (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    instance_id         TEXT NOT NULL,
    hardware_hash       TEXT NOT NULL,
    device_name         TEXT NOT NULL,
    activated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat      TIMESTAMPTZ,
    status              TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'deactivated')),
    deactivated_at      TIMESTAMPTZ,
    deactivate_reason   TEXT,
    UNIQUE (license_id, hardware_hash)
);
CREATE INDEX IF NOT EXISTS idx_ld_license ON license_devices (license_id);
CREATE INDEX IF NOT EXISTS idx_ld_status ON license_devices (status);
CREATE INDEX IF NOT EXISTS idx_ld_hardware ON license_devices (hardware_hash);

CREATE TABLE IF NOT EXISTS offline_activation_requests (
    id                  BIGSERIAL PRIMARY KEY,
    license_key         TEXT NOT NULL,
    hardware_hash       TEXT NOT NULL,
    instance_id         TEXT NOT NULL,
    device_name         TEXT NOT NULL,
    request_id          TEXT NOT NULL UNIQUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at         TIMESTAMPTZ,
    signed_license      JSONB
);
CREATE INDEX IF NOT EXISTS idx_oar_request ON offline_activation_requests (request_id);
CREATE INDEX IF NOT EXISTS idx_oar_license ON offline_activation_requests (license_key);
CREATE INDEX IF NOT EXISTS idx_oar_created ON offline_activation_requests (created_at DESC);
