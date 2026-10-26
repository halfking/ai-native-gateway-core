package sanitizerules

// SchemaSQL is applied by the existing startup migration runner, never Manager.
// The head row is locked during publication, including first baseline creation.
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS sanitize_rule_versions (
 id BIGSERIAL PRIMARY KEY,
 parent_id BIGINT NOT NULL DEFAULT 0,
 rollback_of BIGINT NOT NULL DEFAULT 0,
 config JSONB NOT NULL,
 actor TEXT NOT NULL,
 comment TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 review_links JSONB NOT NULL DEFAULT '[]'::jsonb
);
CREATE OR REPLACE FUNCTION sanitize_rule_immutable_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'sanitize rule versions are immutable'; END;
$$;
DROP TRIGGER IF EXISTS sanitize_rule_versions_immutable ON sanitize_rule_versions;
CREATE TRIGGER sanitize_rule_versions_immutable BEFORE UPDATE OR DELETE ON sanitize_rule_versions
 FOR EACH ROW EXECUTE FUNCTION sanitize_rule_immutable_version();
CREATE TABLE IF NOT EXISTS sanitize_rule_head (
 singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton),
 version_id BIGINT NOT NULL DEFAULT 0
);
INSERT INTO sanitize_rule_head(singleton,version_id) VALUES(TRUE,0) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS sanitize_rule_events (
 id BIGSERIAL PRIMARY KEY,
 fingerprint TEXT NOT NULL UNIQUE,
 tenant_id TEXT NOT NULL DEFAULT '',
 rule_id TEXT NOT NULL,
 version BIGINT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('input','output','egress')),
 action TEXT NOT NULL CHECK(action IN ('mask','block','log','exempted','pending')),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed_sensitive','false_positive','ignored')),
 sensitive_type TEXT NOT NULL DEFAULT 'custom',
 field_kind TEXT NOT NULL DEFAULT 'text',
 sample_masked TEXT NOT NULL,
 count BIGINT NOT NULL DEFAULT 1,
 first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
 last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
 reviewer TEXT NOT NULL DEFAULT '',
 review_comment TEXT NOT NULL DEFAULT '',
 reviewed_at TIMESTAMPTZ,
 resolved_version BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sanitize_rule_events_tenant_time_idx ON sanitize_rule_events(tenant_id,last_seen DESC);
CREATE INDEX IF NOT EXISTS sanitize_rule_events_seen_idx ON sanitize_rule_events(last_seen DESC,id DESC);
CREATE TABLE IF NOT EXISTS sanitize_rule_instances (
 instance_id TEXT PRIMARY KEY,
 version BIGINT NOT NULL,
 config_digest TEXT NOT NULL DEFAULT '',
 last_seen TIMESTAMPTZ NOT NULL,
 last_refresh TIMESTAMPTZ,
 error TEXT NOT NULL DEFAULT ''
);
`

const RollbackSchemaSQL = `
DROP TABLE IF EXISTS sanitize_rule_instances,sanitize_rule_events,sanitize_rule_head,sanitize_rule_versions CASCADE;
DROP FUNCTION IF EXISTS sanitize_rule_immutable_version();
`
