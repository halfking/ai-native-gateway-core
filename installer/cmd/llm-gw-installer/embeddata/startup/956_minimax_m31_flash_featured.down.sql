-- 956_minimax_m31_flash_featured.down.sql
-- Rollback: unmark minimax-m3.1-flash as featured
--
-- 只撤销本迁移的标记动作，不动 953 标注的其它模型。
--
-- ★ 注意：回滚只还原「canonical 侧的 featured 标记」；
--   同轮的 bg/model_tier.go raw 变体展开是**代码**改动，不由此迁移回滚，
--   回滚本迁移后那些 raw 变体仍会因 953 标注的 canonical 而留在 featured 集合
--   （这与回滚前的 static 集合口径一致，非本迁移引入）。

BEGIN;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

DO $$
DECLARE
    v_hit INTEGER;
BEGIN
    UPDATE models_canonical
    SET featured = FALSE
    WHERE featured = TRUE
      AND canonical_name = 'minimax-m3.1-flash';

    GET DIAGNOSTICS v_hit = ROW_COUNT;

    RAISE NOTICE 'migration 956 down: minimax-m3.1-flash featured=FALSE (rows updated=%)', v_hit;
END $$;

DELETE FROM public.schema_migrations WHERE version = '956';
COMMIT;
