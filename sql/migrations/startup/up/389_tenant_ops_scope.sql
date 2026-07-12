-- Scope operational records by tenant. Existing platform records remain default.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';
CREATE INDEX IF NOT EXISTS idx_licenses_tenant_created ON licenses (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_releases_tenant_published ON releases (tenant_id, published_at, build_seq DESC);
