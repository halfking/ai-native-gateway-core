-- ============================================================
-- 迁移 1004: 三级模型过滤（租户 allow/deny 双模 ＋ 组织/用户级策略表）
-- 目的：
--   ① tenant_model_policies 增加 mode 列（deny=既有黑名单语义、
--      allow=白名单语义），缺省 deny ＝ 既有行为逐字不变
--   ② org_model_policies / user_model_policies 两张策略表（照 024 范式）
-- 语义：
--   deny 模式：canonical_name 在表内 ⇒ 拒绝（既有行为）
--   allow 模式：canonical_name 不在表内 ⇒ 拒绝（空表 = 无白名单 = 全放行，
--     与「表空 = 不启用过滤」的全局口径一致）
-- 依赖：024（tenant_model_policies）、1003（organizations/users.org_id）
-- 日期：2026-11-27
-- 影响：tenant_model_policies(mode)、org_model_policies(新)、
--       user_model_policies(新)
-- ============================================================

-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go）：
--   含 CREATE TABLE ＋ ALTER TABLE ＋ CREATE INDEX ⇒ 5s lock_timeout。
-- ★ 实测（2026-11-27 三级配额制轮，PG17 本机一次性容器 casdoor-e2e-pg:13.3.0）：
--   全新基线（00/01/02＋1003 后）单件首跑墙钟 <0.1s（空表建表/加列/触发器/索引，
--   全为元数据级操作）、幂等重放 9 条 NOTICE 全 skip 0.05s、down 0.03s
--   ⇒ 与 10min 预算差五个数量级，无 914 型「拍小了中止部署」风险。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ──────────────────────────────────────────────────────────
-- Part 1: tenant_model_policies.mode（allow/deny 双模扩展）
-- ──────────────────────────────────────────────────────────
-- 缺省 deny ＝ 既有黑名单语义 ⇒ 存量行为零变化。
ALTER TABLE tenant_model_policies
    ADD COLUMN IF NOT EXISTS mode VARCHAR(8) NOT NULL DEFAULT 'deny';

-- view 同步带出 mode 列（Checker reload 读 view）。
-- ★ CREATE OR REPLACE VIEW 只能在尾部追加列（列序不可变）⇒ mode 追加在
--   尾部；表列序（ALTER TABLE ADD COLUMN 也在表尾）一致。
CREATE OR REPLACE VIEW tenant_model_policies_active AS
    SELECT id, tenant_id, canonical_name, reason, created_by,
           created_at, updated_at, mode
    FROM tenant_model_policies
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN tenant_model_policies.mode IS
  '策略模式：deny=黑名单（命中即拒，既有语义）| allow=白名单（不在名单即拒；空名单=全放行）';

-- ──────────────────────────────────────────────────────────
-- Part 2: org_model_policies（组织级模型策略）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS org_model_policies (
    id              BIGSERIAL PRIMARY KEY,
    org_id          INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    mode            VARCHAR(8) NOT NULL DEFAULT 'deny',
    canonical_name  TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    created_by      VARCHAR(128) NOT NULL DEFAULT '',
    deleted_at      TIMESTAMPTZ,
    deleted_by      VARCHAR(128),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, canonical_name),
    CHECK (canonical_name <> ''),
    CHECK (mode IN ('allow', 'deny'))
);

CREATE INDEX IF NOT EXISTS idx_omp_org_active
    ON org_model_policies (org_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_omp_canonical
    ON org_model_policies (canonical_name);

COMMENT ON COLUMN org_model_policies.mode IS
  '策略模式：deny=黑名单 | allow=白名单（空名单=全放行）';

-- ──────────────────────────────────────────────────────────
-- Part 3: user_model_policies（用户级模型策略）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_model_policies (
    id              BIGSERIAL PRIMARY KEY,
    user_id         INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode            VARCHAR(8) NOT NULL DEFAULT 'deny',
    canonical_name  TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    created_by      VARCHAR(128) NOT NULL DEFAULT '',
    deleted_at      TIMESTAMPTZ,
    deleted_by      VARCHAR(128),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, canonical_name),
    CHECK (canonical_name <> ''),
    CHECK (mode IN ('allow', 'deny'))
);

CREATE INDEX IF NOT EXISTS idx_ump_user_active
    ON user_model_policies (user_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_ump_canonical
    ON user_model_policies (canonical_name);

COMMENT ON COLUMN user_model_policies.mode IS
  '策略模式：deny=黑名单 | allow=白名单（空名单=全放行）';

-- ──────────────────────────────────────────────────────────
-- Part 4: active 视图（org/user 级，Checker 读 view 不读表）
-- ──────────────────────────────────────────────────────────
CREATE OR REPLACE VIEW org_model_policies_active AS
    SELECT id, org_id, mode, canonical_name, reason, created_by,
           created_at, updated_at
    FROM org_model_policies
    WHERE deleted_at IS NULL;

CREATE OR REPLACE VIEW user_model_policies_active AS
    SELECT id, user_id, mode, canonical_name, reason, created_by,
           created_at, updated_at
    FROM user_model_policies
    WHERE deleted_at IS NULL;

-- ──────────────────────────────────────────────────────────
-- Part 5: 审计表 + 触发器（照 024 范式，三级各一套）
-- ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS org_model_policies_audit (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    action          TEXT NOT NULL CHECK (action IN ('insert','update','delete','undelete')),
    policy_id       BIGINT,
    org_id          INT,
    canonical_name  TEXT,
    mode            TEXT,
    reason          TEXT,
    actor           TEXT
);

CREATE TABLE IF NOT EXISTS user_model_policies_audit (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    action          TEXT NOT NULL CHECK (action IN ('insert','update','delete','undelete')),
    policy_id       BIGINT,
    user_id         INT,
    canonical_name  TEXT,
    mode            TEXT,
    reason          TEXT,
    actor           TEXT
);

CREATE OR REPLACE FUNCTION org_model_policies_audit_fn()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT := COALESCE(
        NULLIF(current_setting('app.current_admin', true), ''),
        'system'
    );
BEGIN
    IF (TG_OP = 'INSERT') THEN
        INSERT INTO org_model_policies_audit
            (action, policy_id, org_id, canonical_name, mode, reason, actor)
        VALUES
            ('insert', NEW.id, NEW.org_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
            IF NEW.deleted_at IS NULL THEN
                INSERT INTO org_model_policies_audit
                    (action, policy_id, org_id, canonical_name, mode, reason, actor)
                VALUES
                    ('undelete', NEW.id, NEW.org_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
            ELSE
                INSERT INTO org_model_policies_audit
                    (action, policy_id, org_id, canonical_name, mode, reason, actor)
                VALUES
                    ('delete', NEW.id, NEW.org_id, NEW.canonical_name, OLD.mode, OLD.reason, v_actor);
            END IF;
        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
              OR NEW.mode IS DISTINCT FROM OLD.mode
        THEN
            INSERT INTO org_model_policies_audit
                (action, policy_id, org_id, canonical_name, mode, reason, actor)
            VALUES
                ('update', NEW.id, NEW.org_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO org_model_policies_audit
            (action, policy_id, org_id, canonical_name, mode, reason, actor)
        VALUES
            ('delete', OLD.id, OLD.org_id, OLD.canonical_name, OLD.mode, OLD.reason, v_actor);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS org_model_policies_audit_trg ON org_model_policies;
CREATE TRIGGER org_model_policies_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON org_model_policies
    FOR EACH ROW EXECUTE FUNCTION org_model_policies_audit_fn();

CREATE OR REPLACE FUNCTION user_model_policies_audit_fn()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT := COALESCE(
        NULLIF(current_setting('app.current_admin', true), ''),
        'system'
    );
BEGIN
    IF (TG_OP = 'INSERT') THEN
        INSERT INTO user_model_policies_audit
            (action, policy_id, user_id, canonical_name, mode, reason, actor)
        VALUES
            ('insert', NEW.id, NEW.user_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
            IF NEW.deleted_at IS NULL THEN
                INSERT INTO user_model_policies_audit
                    (action, policy_id, user_id, canonical_name, mode, reason, actor)
            VALUES
                    ('undelete', NEW.id, NEW.user_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
            ELSE
                INSERT INTO user_model_policies_audit
                    (action, policy_id, user_id, canonical_name, mode, reason, actor)
                VALUES
                    ('delete', NEW.id, NEW.user_id, NEW.canonical_name, OLD.mode, OLD.reason, v_actor);
            END IF;
        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
              OR NEW.mode IS DISTINCT FROM OLD.mode
        THEN
            INSERT INTO user_model_policies_audit
                (action, policy_id, user_id, canonical_name, mode, reason, actor)
            VALUES
                ('update', NEW.id, NEW.user_id, NEW.canonical_name, NEW.mode, NEW.reason, v_actor);
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO user_model_policies_audit
            (action, policy_id, user_id, canonical_name, mode, reason, actor)
        VALUES
            ('delete', OLD.id, OLD.user_id, OLD.canonical_name, OLD.mode, OLD.reason, v_actor);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_model_policies_audit_trg ON user_model_policies;
CREATE TRIGGER user_model_policies_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON user_model_policies
    FOR EACH ROW EXECUTE FUNCTION user_model_policies_audit_fn();

COMMIT;
