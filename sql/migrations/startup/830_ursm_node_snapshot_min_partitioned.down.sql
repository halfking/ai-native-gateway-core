-- 830 down: 回滚 ursm_node_snapshot_min 的分区化（手工执行）
--
-- ★ 与 830 up 一样是**手工迁移**，不在 installer 自动启动序列里。
--
-- 何时需要走这个：
--   830 up 已执行、观察期内出现无法当场修复的问题（写入失败、分区边界错位、
--   某个下游 SQL 未适配分区语义等），且需要立刻回到「非分区表」形态。
--
-- ★★ 硬警告：回滚会**丢掉 830 up 之后写入新表的所有行** ★★
--   830 up 走的是「改名 + 新建空父表」而不是「搬数据」，所以执行 up 之后
--   写入的行全部在新表 `ursm_node_snapshot_min` 里，与 `_legacy` 无交集。
--   本脚本的默认动作是 **RENAME 交换**（新表回到 `_post825` 名字保住其数据，
--   `_legacy` 换回原名），而不是直接丢弃。
--   若你确认新表数据可以丢弃，把下面 `DROP` 那段的注释解开再跑。
--
-- 前置条件（全部满足才允许回滚）：
--   1. 已确认问题无法在分区形态下当场修复（不是某个分区缺了 —— 那应该
--      调 ensure，而不是回滚）；
--   2. 已确认 _legacy 仍在（830 的步骤 3 DROP 还没执行）；若已 DROP，
--      本脚本直接 RAISE —— 那时已经没有可回滚的历史了；
--   3. 已通知运维：回滚后新表数据不再有写入方，直到再次执行 830。

DO $$
DECLARE
    new_rows  BIGINT;
    legacy_ok BOOLEAN;
BEGIN
    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RAISE EXCEPTION
            'public.ursm_node_snapshot_min 不存在 —— 830 up 似乎没执行过，无需回滚。';
    END IF;

    legacy_ok := to_regclass('public.ursm_node_snapshot_min_legacy') IS NOT NULL;
    IF NOT legacy_ok THEN
        RAISE EXCEPTION
            'public.ursm_node_snapshot_min_legacy 不存在 —— 830 的步骤 3 已执行、'
            '历史数据已被 DROP，没有可回滚的目标。回滚会永久丢失 %s。',
            '830 up 之后写入新表的数据';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class
                WHERE oid = 'public.ursm_node_snapshot_min'::regclass
                  AND relkind <> 'p') THEN
        RAISE EXCEPTION 'public.ursm_node_snapshot_min 不是分区父表 —— 状态与预期不符，人工确认。';
    END IF;

    -- 把新表当前行数打出来，供人工决策（回滚会把它挪到 _post825，不丢）。
    SELECT count(*) INTO new_rows FROM public.ursm_node_snapshot_min;
    RAISE NOTICE '830 回滚：分区表当前 % 行，将保留为 ursm_node_snapshot_min_post825。', new_rows;
END $$;

BEGIN;
SET LOCAL lock_timeout = '10s';

-- 1) 分区表挪出原名（连同其全部子分区），保住 up 之后写入的数据。
ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_post825;

-- 1b) ★ 它的 PK 约束**不跟着改名表改名字**，仍叫
--     `ursm_node_snapshot_min_pkey`，于是 canonical 名字被 `_post825` 占着。
--     不让出来的话，重新执行 830 up 时新父表建同名 PK 会报
--     `relation "ursm_node_snapshot_min_pkey" already exists`
--     （本地 PG 17.11 往返实测踩到）⇒ 回滚后再也上不了迁移。
--     条件式执行，容忍「约束已被别处改名 / 本次 up 未走到建 PK」的形态。
DO $$
DECLARE
    cur_name TEXT;
BEGIN
    SELECT conname INTO cur_name
      FROM pg_constraint
     WHERE conrelid = 'public.ursm_node_snapshot_min_post825'::regclass
       AND contype = 'p'
     LIMIT 1;

    IF cur_name = 'ursm_node_snapshot_min_pkey' THEN
        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min_post825
                 RENAME CONSTRAINT ursm_node_snapshot_min_pkey
                 TO ursm_node_snapshot_min_post825_pkey';
    END IF;
END $$;

-- 2) 历史表换回原名 ⇒ 写入方（writer.go 的 INSERT + ON CONFLICT）无感恢复。
--    ★ 它的 PK 仍是 (snapshot_ts, tenant_id, credential_id, raw_model_name)，
--      writer 的 ON CONFLICT 继续合法。
ALTER TABLE public.ursm_node_snapshot_min_legacy RENAME TO ursm_node_snapshot_min;

-- 2b) 把 PK 约束名也换回 canonical。
--    ★ 不换的后果（本地 PG 17.11 往返实测）：表名回来了但约束还叫
--      `ursm_node_snapshot_min_legacy_pkey`，再执行 830 up 时它的
--      「让出 canonical 名」那一步会报 does not exist ⇒ 无法重新上迁移。
--      回滚必须把名字一起还原，才算真的回到 up 之前的状态。
DO $$
DECLARE
    cur_name TEXT;
BEGIN
    SELECT conname INTO cur_name
      FROM pg_constraint
     WHERE conrelid = 'public.ursm_node_snapshot_min'::regclass
       AND contype = 'p'
     LIMIT 1;

    IF cur_name = 'ursm_node_snapshot_min_legacy_pkey' THEN
        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min
                 RENAME CONSTRAINT ursm_node_snapshot_min_legacy_pkey
                 TO ursm_node_snapshot_min_pkey';
    END IF;
END $$;

-- 2c) ledger 只在**真的回滚了**时才删（817 同款守卫，2026-10-05 补）。
--    '830' 的台账行由 db.ensureURSMNodeSnapshotMinDailyPartition 在 ensure
--    成功后写入（up 是手工迁移，本身不写台账）。本 down 改名父表之后，这行
--    applied 记录若无人清理，账本就继续声称「830 已应用」，而表其实是回滚后
--    的普通表——下次重装 830 或评估回滚风险时被误导。也不能无条件 DELETE：
--    回滚未收敛时删台账等于声称「830 没跑过」。这里按 817 的模式守一层：
--    父表仍是分区表（relkind='p'）＝回滚没有收敛 ⇒ 保留 ledger 行并 NOTICE。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class
                WHERE oid = 'public.ursm_node_snapshot_min'::regclass
                  AND relkind = 'p') THEN
        RAISE NOTICE '830 down: parent still partitioned; keeping the ledger row (rollback did not converge)';
        RETURN;
    END IF;
    DELETE FROM public.schema_migrations WHERE version = '830';
    RAISE NOTICE '830 down: ledger row for 830 removed';
END $$;

COMMIT;

-- 3) ensure 函数可以留着（幂等、无人调用时无害），但分区表 _post825 还在占空间。
--    人工确认不再需要回滚材料后再删：
-- DROP TABLE public.ursm_node_snapshot_min_post825;
-- DROP FUNCTION IF EXISTS public.ensure_ursm_node_snapshot_min_daily_partition(DATE);

-- 4) 关于 ensure 函数与 boot ensure 的相互作用（2026-10-04 修正）：
--    写这个 down 脚本时发现，boot ensure 若只判断「ensure 函数是否存在」，
--    就会在这个回滚方向上出洞：父表已回到普通表、函数却还在 ⇒ ensure 对
--    非分区父表执行 CREATE TABLE ... PARTITION OF ⇒ 报错冒到 db.Open ⇒
--    进程进 no-DB 模式。
--    **该洞已修**：db.ensureURSMNodeSnapshotMinDailyPartition 现在先判父表
--    relkind='p'，非分区就整个跳过（这同时覆盖了「830 未执行」和「830 已回滚」
--    两种正常态）。门：bg/partition_825_contract_test.go 的
--    Test825BootEnsureIsWired，变异 M35。
--    ⇒ 所以执行回滚后**不必**为了 boot 安全而删函数；删它只是为了不留
--      误导性的死对象，以及避免 bg/partition_manager.go 的 24h tick
--      每轮都记一条 error。
