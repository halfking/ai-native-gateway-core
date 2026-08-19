-- 0004_shadow_provider_contract.up.sql
-- ASM 与 llm-gateway-go 共用用户系统；ASM 不得成为独立 shadow provider。
-- 使用 fix-forward migration，避免修改已应用的 0002 migration checksum。

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM shadow_user_providers
        WHERE provider NOT IN ('redclaw', 'memora', 'llm-gateway', 'pocket', 'acc')
    ) THEN
        RAISE EXCEPTION 'cannot add provider contract: unsupported provider rows exist';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'shadow_user_providers'::regclass
          AND conname = 'shadow_user_providers_provider_chk'
    ) THEN
        ALTER TABLE shadow_user_providers
            ADD CONSTRAINT shadow_user_providers_provider_chk
            CHECK (provider IN ('redclaw', 'memora', 'llm-gateway', 'pocket', 'acc'));
    END IF;
END $$;

INSERT INTO identity_migration_ownership (version, name, checksum, applied_by)
VALUES (4, '0004_shadow_provider_contract.up.sql', 'managed-by-identity-migrate', current_user)
ON CONFLICT (version) DO NOTHING;
