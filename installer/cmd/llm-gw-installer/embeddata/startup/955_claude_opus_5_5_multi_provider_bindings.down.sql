-- 955_claude_opus_5_5_multi_provider_bindings.down.sql
-- 回滚 claude-opus-5-5 多provider绑定

DO $$ 
DECLARE
    v_canonical_id bigint;
BEGIN
    SELECT id INTO v_canonical_id
    FROM models_canonical
    WHERE canonical_name = 'claude-opus-5-5';
    
    IF v_canonical_id IS NULL THEN
        RAISE NOTICE 'claude-opus-5-5 not found, nothing to rollback';
        RETURN;
    END IF;
    
    -- 删除除了最初的绑定（id=242）之外的所有绑定
    DELETE FROM credential_model_bindings
    WHERE id IN (
        SELECT cmb.id
        FROM credential_model_bindings cmb
        JOIN provider_models pm ON cmb.provider_model_id = pm.id
        WHERE pm.canonical_id = v_canonical_id
          AND cmb.id != 242
    );
    
    -- 删除新增的 provider_models（保留最初的 id=244）
    DELETE FROM provider_models
    WHERE canonical_id = v_canonical_id
      AND id != 244;
    
    RAISE NOTICE 'Rolled back claude-opus-5-5 multi-provider bindings';
    
END $$;
