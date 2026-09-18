-- 726: 修复 credential_model_index_hot 唯一索引缺失导致的 auto route rollup 全失败
--      （154 生产事故审计，2026-09-18，SQLSTATE 42P10）
--
-- 症状（154 生产 build 2138，2026-09-18 07:35 CST 部署后）：
--   gateway 日志每逢 5 分钟 ticker 及 credentials / credential_model_bindings
--   NOTIFY 触发即重复：
--     WARN auto route listener: refresh failed
--     error="rollup credential_model_index: insert: ERROR: there is no unique
--      or exclusion constraint matching the ON CONFLICT specification
--      (SQLSTATE 42P10)"
--   连带后果：credential_model_index_hot 无任何新写入；8h retention 的
--   promote_credential_model_index_hot_to_partition 把存量搬入 parent 后
--   DELETE，hot 表归零；credential_model_index_with_current_month 的最新
--   桶冻结在 2026-09-17 14:45+08。autoroute 的 refreshIndexSQL 取
--   per-pair MAX(bucket) 且无新鲜度下限，因此 auto 路由未中断、但决策
--   指标停止进化（本任务"kaixuan/auto 是否生效"审计的根因）。
--
-- 根因：
--   (bucket, credential_id, raw_model) 三列上历史上先后存在三套函数等价的
--   唯一索引：
--     - credential_model_index_hot_unique_key                        (347)
--     - idx_credential_model_index_hot_unique                        (354)
--     - credential_model_index_hot_bucket_credential_id_raw_model_idx
--   718（R37 冗余索引清理）按"三留一"设计 drop 前两者、保留
--   idx_credential_model_index_hot_unique。其前置核实②（drop 后无 Go
--   ensure 链复活）与③（保留侧存在）在审计本地库成立，但在 154/245
--   共享 PG17 上保留侧并不存在（354 的 IF NOT EXISTS 未在该库生效，或
--   该索引此前已被清理；该库 schema_migrations.version 还混有 'V359'
--   等非数字值，存在账本修复痕迹）。718 应用后 hot 表唯一索引归零，
--   bg/auto_index_refresher.go rollupCredentialModelIndexONCONFLICT 的
--     ON CONFLICT (bucket, credential_id, raw_model) DO UPDATE
--   每次执行必然 42P10（deleteSQL 先行成功、insertSQL 失败）。
--
-- 修复：
--   1) 防御性去重：若某环境曾在无唯一索引窗口内产生过重复行，先收敛再建
--      索引（否则 CREATE UNIQUE INDEX 直接失败）。保留每组冲突键中 ctid
--      最小的行；不比较其余列——下一个成功的 rollup 会经 ON CONFLICT
--      DO UPDATE 用最新指标覆盖。
--   2) 以 718 选定的保留名 idx_credential_model_index_hot_unique 重建唯一
--      索引。IF NOT EXISTS 保证幂等：新装库（baseline→354 已建同名索引）
--      上为 no-op；347 名字的索引在新装链路会被 718 drop，与本迁移无关。
--
-- 幂等性：可重复执行；对索引健在的库是纯 no-op。
-- 回滚：见 .down.sql——会重新打开 42P10 缺口，仅用于彻底撤销本迁移。
--
-- 只读核实记录（154 → 共享 PG17，2026-09-18）：
--   pg_indexes: credential_model_index_hot 仅剩 canonical_id / credential_id /
--               updated_at 三个非唯一索引；
--   credential_model_index_hot 0 行；credential_model_index 64286 行、
--   MAX(bucket)=2026-09-17 14:45+08；model_task_index 唯一约束健在（无需修）。

BEGIN;

DELETE FROM credential_model_index_hot a
USING credential_model_index_hot b
WHERE a.bucket        = b.bucket
  AND a.credential_id = b.credential_id
  AND a.raw_model     = b.raw_model
  AND a.ctid          > b.ctid;

CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_model_index_hot_unique
    ON credential_model_index_hot (bucket, credential_id, raw_model);

COMMIT;
