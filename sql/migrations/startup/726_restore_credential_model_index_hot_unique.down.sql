-- 726 down: 撤销本迁移重建的唯一索引。
--
-- 警告：执行后 credential_model_index_hot 将不再有任何
-- (bucket, credential_id, raw_model) 唯一索引，
-- bg.AutoIndexRefresher 的 ON CONFLICT rollup 会回到 SQLSTATE 42P10
-- 全失败状态（即 726 之前的 154 生产事故态）。仅应在彻底回滚 726 时
-- 执行，且执行后必须尽快以替代约束恢复 rollup。
--
-- 语义说明：在 354 索引健在的库上，726 本身是 no-op，本 down 仍会删除
-- 354 创建的同名索引（DROP IF EXISTS 无从区分创建者）；如需恢复，重跑
-- 354 或 726 即可（均为 IF NOT EXISTS，幂等）。

DROP INDEX IF EXISTS idx_credential_model_index_hot_unique;
