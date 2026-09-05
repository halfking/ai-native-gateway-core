-- 0004_shadow_provider_contract.down.sql
ALTER TABLE shadow_user_providers
    DROP CONSTRAINT IF EXISTS shadow_user_providers_provider_chk;
