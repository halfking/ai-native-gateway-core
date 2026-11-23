-- ===========================================================================
-- File:          sql/migrations/startup/918_models_canonical_minimax_m31_seed.down.sql
-- Migration:     918（回滚）
-- Purpose:       只退本迁移引入的那一行 + 退自己的台账行
--
-- ★ ★ 「只退自己引入的」的具体口径（★ 本仓纪律）：
--   本迁移写的行 source='seed-standard-rollout'；★ 而发现服务同步进来的行
--   source='discovery' ⇒ ⇒ ★ **不是本迁移写的就不删**（★ 生产上那行是
--   discovery 同步进来的 ⇒ ★ 本 down 在生产上**删不掉任何东西** ⇒ ★ 正确）。
--
-- ★ 注意：退掉这一行 ⇒ 956 的前提随之消失 ⇒ ⇒ ★ 若在 956 **之前**回滚到本迁移
--   之后、956 之前，则 956 会再次 abort（★ 这是回滚方向的正常语义：
--   ★ 回滚必须严格按版本倒序 ⇒ 956 会先被退掉）。
-- ===========================================================================

BEGIN;

DELETE FROM public.models_canonical
 WHERE canonical_name = 'minimax-m3.1-flash'
   AND source = 'seed-standard-rollout';

-- 台账对偶（★ 只退自己那一行）
DELETE FROM public.schema_migrations WHERE version = '918';

COMMIT;
