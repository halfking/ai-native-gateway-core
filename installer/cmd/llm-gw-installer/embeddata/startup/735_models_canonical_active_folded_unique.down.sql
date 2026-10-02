-- 735 down: 移除 models_canonical active 折叠名表达式唯一索引。
-- 仅删本迁移创建的索引，不触碰任何数据（与 726.down 同纪律）。
DROP INDEX IF EXISTS uq_models_canonical_active_folded_name;
