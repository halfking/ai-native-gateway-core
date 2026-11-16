-- 965_auth_id_mapping_canonical_channel.sql
-- 2026-11-15（12h 修订审计轮 P1 收口）：Casdoor 集成的身份映射表收编
-- startup 正典通道。
--
-- ── 为什么存在本文件 ─────────────────────────────────────────────────────
--   phase3（cee143c84）把 auth_id_mapping DDL 放在顶层 migrations/036_
--   add_auth_id_mapping.sql——无投递通道（745/752 同款病，752 头注释有
--   先例登记）：文件不被 deploy pending-scanner 扫描、不在 installer
--   StartupFiles 清单、不在 boot ensure 自愈链，唯一执行方式是文档里
--   的手工 psql \i（CASDOOR_CONFIGURATION.md:189）⇒ 除手工跑过的库外
--   全部环境缺表 ⇒ 启用 casdoor provider 首登即 42P01（auth_id_mapping
--   不存在），login 500。
--
--   且 036 与 sql/migrations/startup/036_fp_slot_limit.sql **撞号异文件**
--   （顶层序号是七个月前的老段；本通道已推进到 964）。历史先例：顶层
--   db/migrations/036_fp_slot_limit 同样撞号，2026-09-27 收编 752 时
--   一并删除（752 头注释自记）。
--
-- ── 本文件相对顶层 036 的差异 ────────────────────────────────────────────
--   1. 纯幂等化（startup 通道每次部署无脑重放是安全默认——deploy-seamless
--      引擎注释原话）：
--      · CREATE INDEX → IF NOT EXISTS（顶层版裸 CREATE INDEX，重放即炸
--        42P11 duplicate index）；
--      · CREATE TRIGGER → 先 DROP IF EXISTS 再建（顶层版裸建，重放即炸
--        42710 duplicate trigger）；
--      · backfill INSERT 保留 ON CONFLICT DO NOTHING（顶层版已幂等）。
--   2. 删除 DOWN 段与验证注释段（startup 通道文件是前向幂等 DDL，
--      DOWN 走独立的 .down.sql；验证段属文档不属可执行文件）。
--   3. 表名/列/约束与顶层 036 完全一致——已在 252 等环境手工建过表的
--      环境重放本文件时全部语句 no-op，不破坏存量。
--   4. 顶层 migrations/036_add_auth_id_mapping.sql 删除（撞号异文件混淆
--      一并消除，752 先例）；.handoff/CASDOOR_CONFIGURATION.md 的手工
--      psql 步骤改指本通道（startup 通道自动投递，无需手工）。

-- ============================================================================
-- UP（幂等，重放安全）
-- ============================================================================

-- ★ 2026-11-16 12h 修订审计轮补正：本文件此前**既无事务块也无 lock_timeout**
--   ⇒ ① `SET LOCAL` 写在事务块外会只发一条 WARNING、**值不生效**（空文守卫）；
--   ② ADD/CREATE INDEX（ACCESS EXCLUSIVE）可能**无限期等锁**堵住全库
--      （installer 的 startup_ddl_lock_guard_gate_test.go 门 >870 号迁移）。
--   ⇒ 现在补 BEGIN/COMMIT + lock_timeout + statement_timeout。
--   ★ 966 从未被任何环境应用过（投递通道此前就是断的，见审计档 P1-1）⇒
--   改字节不撞 R189「已应用迁移字节冻结」。

BEGIN;

SET LOCAL lock_timeout = '5s';
-- ★ 超时预算（2026-11-17 补测；门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）：
--   本文件含 5 条**非并发 CREATE INDEX** ⇒ 命中「全表扫描类 DDL」判据 ⇒
--   预算必须按目标环境最大表实测后抬够（761 先例：实测 18.9s ⇒ 抬 10min；
--   914 反例：拍 120s ⇒ 245 部署中止）。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17 / arm64 Docker，
--        2026-11-17 06:5x，psql \timing）：同结构 TEMP 表灌 **100,000 行**
--        （211 上该表真实行数 = 24 ⇒ 4000 倍余量）后建同形索引：
--     (external_id, provider) 129.034 ms ｜ (internal_id) 55.325 ms
--     (username, provider, tenant_id) 137.386 ms ｜ (tenant_id) 92.023 ms
--     (provider) 39.088 ms   ⇒ 最慢单条 **137.386 ms**
--   抬到 10min 的理由（不是拍脑袋）：上面是**空载**共享库上的单次测量，
--   迁移窗口内网关仍在写、ensure 链与索引维护叠加（761 的 18.9s 正是在
--   这种争用下越过了角色级上限）⇒ 按 761 同一惯例留足余量。
--   SET LOCAL 只对本事务生效，不污染集群全局。
SET LOCAL statement_timeout = '10min';

-- External identity mapping table
-- Maps external provider identities (Casdoor UUIDs, LDAP DNs, etc.) to internal user IDs
CREATE TABLE IF NOT EXISTS auth_id_mapping (
    internal_id SERIAL PRIMARY KEY,
    external_id TEXT NOT NULL,           -- External provider's user ID (e.g., Casdoor UUID)
    provider TEXT NOT NULL,              -- Provider name: 'native', 'casdoor', 'ldap', 'oidc'
    tenant_id TEXT NOT NULL,             -- Tenant scope for multi-tenancy
    username TEXT NOT NULL,              -- Username in the external provider
    display_name TEXT,                   -- Cached display name from provider
    email TEXT,                          -- Cached email from provider
    metadata JSONB,                      -- Provider-specific metadata (extensible)
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    last_sync_at TIMESTAMPTZ,           -- Last synchronization with external provider

    -- Unique constraints: one mapping per provider+external_id+tenant combination
    CONSTRAINT auth_id_mapping_provider_external_tenant_key UNIQUE(provider, external_id, tenant_id),
    -- Unique username per provider+tenant (prevents duplicate usernames)
    CONSTRAINT auth_id_mapping_provider_username_tenant_key UNIQUE(provider, username, tenant_id),

    -- Validation: provider must be one of the supported types
    CONSTRAINT auth_id_mapping_provider_check CHECK (provider IN ('native', 'casdoor', 'ldap', 'oidc', 'saml'))
);

-- Indexes for performance（幂等：IF NOT EXISTS）
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_external ON auth_id_mapping(external_id, provider);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_internal ON auth_id_mapping(internal_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_username ON auth_id_mapping(username, provider, tenant_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_tenant ON auth_id_mapping(tenant_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_provider ON auth_id_mapping(provider);

-- Auto-update timestamp trigger（幂等：先 DROP 再建）
CREATE OR REPLACE FUNCTION update_auth_id_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS auth_id_mapping_updated_at ON auth_id_mapping;
CREATE TRIGGER auth_id_mapping_updated_at
    BEFORE UPDATE ON auth_id_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_auth_id_mapping_timestamp();

-- Comments for documentation（幂等：COMMENT 本身可重放）
COMMENT ON TABLE auth_id_mapping IS 'Maps external provider identities to internal user IDs for multi-provider authentication';
COMMENT ON COLUMN auth_id_mapping.internal_id IS 'Internal sequential user ID (stable across providers)';
COMMENT ON COLUMN auth_id_mapping.external_id IS 'External provider user ID (Casdoor UUID, LDAP DN, etc.)';
COMMENT ON COLUMN auth_id_mapping.provider IS 'Authentication provider name';
COMMENT ON COLUMN auth_id_mapping.tenant_id IS 'Tenant/organization identifier';
COMMENT ON COLUMN auth_id_mapping.username IS 'Username in the external provider';
COMMENT ON COLUMN auth_id_mapping.metadata IS 'Provider-specific metadata (JSON)';
COMMENT ON COLUMN auth_id_mapping.last_sync_at IS 'Last synchronization timestamp with external provider';

-- Backfill existing native users (optional, for reporting/analytics)
-- This creates mappings for all existing PostgreSQL users with provider='native'
INSERT INTO auth_id_mapping (
    internal_id,
    external_id,
    provider,
    tenant_id,
    username,
    display_name,
    email,
    created_at,
    last_sync_at
)
SELECT
    id,
    'native:' || id::text,  -- External ID format: native:123
    'native',
    tenant_id,
    username,
    display_name,
    email,
    created_at,
    last_login_at  -- Use last_login_at as initial sync timestamp
FROM users
WHERE EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')
ON CONFLICT (provider, external_id, tenant_id) DO NOTHING;

-- ★ 2026-11-16 12h 修订审计轮 P2 根修：backfill 之后**推进序列**。
--
--   上面的 backfill **显式插入 internal_id**（= users.id）⇒ 不触发 nextval
--   ⇒ 序列仍停在 1 ⇒ 后续 id_mapper.MapOrCreate 取号撞上已回填行
--   ⇒ **PK 冲突 23505** ⇒ 登录 500（存量库跑完本迁移后必现）。
--   ⇒ 且重放时若 backfill 的显式 id 撞上「序列已发出去」的号，整份迁移失败。
--   同族先例：534/566/609/622 迁移都有 setval。
--
--   口径：setval(seq, MAX(internal_id), is_called=true) ⇒ 下一次 nextval = MAX+1。
--   ★ 表为空时用 is_called=false + 值 1 ⇒ 下一次 nextval = 1（不跳号）。
--   ★ 用 DO $$ 包裹且先判序列是否存在：手工建过表的库（早于 SERIAL 定义的
--     变体）没有该序列 ⇒ 跳过而不是让整份迁移失败。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'auth_id_mapping_internal_id_seq'
          AND n.nspname = 'public'
    ) THEN
        PERFORM setval(
            'public.auth_id_mapping_internal_id_seq',
            GREATEST(COALESCE((SELECT MAX(internal_id) FROM public.auth_id_mapping), 0), 1),
            EXISTS (SELECT 1 FROM public.auth_id_mapping)
        );
    END IF;
END $$;

-- ── 记账（≥952 自登记纪律，门 TestMigrationLedgerSelfRegistration）────────
--   ★ 2026-11-17 补：本文件的 **down** 一直带着
--     `DELETE FROM public.schema_migrations WHERE version = '966'`，
--     而 up 侧漏了配对的 INSERT ⇒ 记账链断层（950/951/957/958 同款）：
--     211 实测（acc-pg17 / llm_gateway）表里 auth_id_mapping **对象在场**，
--     但 schema_migrations 里 **没有 '966' 这一行** ⇒ 人读台账少记一笔、
--     审计按台账判「未应用」会得出与事实相反的结论。
--   ★ 幂等：ON CONFLICT DO UPDATE ⇒ 重放安全（211 上内容变更后由
--     apply-db-revision-sequence.sh 的指纹通道自动重放一次，补上这笔账）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('966', 'auth_id_mapping: canonical-channel identity mapping table (external_id/provider/username/tenant_id) with updated_at trigger and internal_id sequence alignment; Casdoor provider identity mapping')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
