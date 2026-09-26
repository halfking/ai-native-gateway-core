-- 750: usage_facts 按日分区函数 + 当日/次日预建（R68 24h 审计轮，2026-09-26）
--
-- 背景：usage_facts 是 PARTITION BY RANGE (occurred_at) 的分区父表（537），
-- 但仅 DEFAULT 单分区。749（R67）仅补 occurred_at 前导索引；rollup
-- 五查询（domains/reportrollup/rollup.go providerModelDaySQL/
-- totalDaySQL/internalTenantDaySQL/internalModelDaySQL/
-- internalPersonDaySQL）+ stats 对账（domains/stats/reconciliation.go）
-- 全是纯 occurred_at 范围条件——无按日分区函数时 partition pruning
-- 不可用，新一日数据仍写 DEFAULT 分区，索引扫描退化为 DEFAULT 全表。
-- 共享 PG（252）30s statement_timeout 必杀成批 rollup/对账。
--
-- 修法：ensure_usage_facts_daily_partition(p_date DATE) 函数，幂等建
-- 当日分区。DEFAULT 分区保留作历史 catch-all（新一日数据走日分区，
-- 越界数据落 DEFAULT；partition pruning 对 WHERE 范围查询仅扫命中
-- 分区）。Date 签名（与 ensure_sessions_v2_partitions 同款），由
-- PartitionManager 24h tick 接入 ensureSpecs 用 Asia/Shanghai 日历
-- 派生当日/次日（避免 UTC 会话在 +08 0-8h 把次日误建为今日）。
--
-- 2026-09-26 修订（当日修订审计 P1）：初版直接 CREATE TABLE ... PARTITION OF。
-- PG 对「父表挂 DEFAULT 分区时新建具体分区」会先校验 DEFAULT 内无落入
-- 新分区边界的行——有则报
--   ERROR: updated partition constraint for default partition
--         "usage_facts_default" would be violated by some row
-- 存量数据环境（252 共享 PG 的 telemetry 持续写入 DEFAULT）首启必踩：
-- db.Open → ApplyMigrations → ensureUsageFactsDailyPartition 失败 → 进程
-- 进入 no-DB 模式并触发部署自动回滚；PartitionManager tick 在「跨午夜停机
-- 后首日行先落 DEFAULT」时同样失败，且该日永远补不上分区（行持续累积，
-- 约束校验持续失败）。scratch 空库四通道实证未覆盖「DEFAULT 已有当日行」
-- 路径。改为搬移后挂接（move-then-attach），全部步骤同一事务，失败整体
-- 回滚不留半成品：
--   ① pg_advisory_xact_lock 串行化多实例/多入口并发 ensure（boot ensure、
--      installer、升级通道、24h tick 在 252 共享库上可能并发）；
--   ② 短路：分区已挂接（pg_inherits）则直接返回；
--   ③ CREATE TABLE ... (LIKE usage_facts INCLUDING DEFAULTS INCLUDING INDEXES)
--      先建独立表：拷贝默认值（fact_id 共享 bigserial 序列）+ 父表 6 个
--      分区索引（PK + 537 四索引 + 749 occurred_at 前导），ATTACH 时索引
--      匹配为纯元数据挂接，避免父表 ACCESS EXCLUSIVE 下现场重建索引；
--   ④ LOCK TABLE usage_facts_default IN ACCESS EXCLUSIVE MODE：挡住并发
--      telemetry 向 DEFAULT 路由的新写入（其余分区写入不受影响），持锁
--      段仅「删当日行 + 挂接元数据」，时长被当日行数界定；
--   ⑤ DELETE ... WHERE occurred_at ∈ [start, end) RETURNING → INSERT 进
--      新表：把会违反 DEFAULT 约束校验的当日行搬走（历史上该日行数有界，
--      通常一个部署日内几千~几十万行）；
--   ⑥ ALTER TABLE ... ATTACH PARTITION：此刻 DEFAULT 内已无界内行；
--      ATTACH 对 DEFAULT 的校验扫描仍会发生（PG 无法凭空证明），扫描在
--      boot ensure 的 5min 迁移期 statement_timeout 钉扎内完成；这是每日
--      一次（boot/tick 时点、低峰）的代价，换 partition pruning 常态收益。
--
-- TTL：旧 DEFAULT 分区历史数据归属由 owner 拍板（依《存储策略 v2.1》
-- 表 5 推算 R68 §四 登记的 TTL 区间 7-90 天），本迁移仅建日分区函数
-- 不删除任何历史 partition。

CREATE OR REPLACE FUNCTION ensure_usage_facts_daily_partition(p_date DATE)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    pname    TEXT := format('usage_facts_%s', to_char(p_date, 'YYYYMMDD'));
    start_ts TIMESTAMPTZ := p_date::timestamptz;
    end_ts   TIMESTAMPTZ := (p_date + INTERVAL '1 day')::timestamptz;
BEGIN
    -- 幂等短路：该日分区已挂接则无事可做。
    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.usage_facts'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 串行化并发 ensure（同日不同入口 / 多实例共享库）。锁随事务释放。
    PERFORM pg_advisory_xact_lock(
        hashtext('ensure_usage_facts_daily_partition:' || pname));

    -- 拿到锁后复查：等锁期间别的入口可能已建好。
    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.usage_facts'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 独立表承接当日行：INCLUDING INDEXES 让 ATTACH 的分区索引挂接走
    -- 元数据匹配（父表 6 个分区索引：PK + 4 个 537 二级 + 749 前导）。
    EXECUTE format(
        'CREATE TABLE %I (LIKE usage_facts INCLUDING DEFAULTS INCLUDING INDEXES)',
        pname);

    -- 挡住并发写入落进 DEFAULT 的搬移窗口；持锁段 = 搬当日行 + 挂接。
    EXECUTE 'LOCK TABLE usage_facts_default IN ACCESS EXCLUSIVE MODE';

    -- 把 DEFAULT 内落入本日边界的行搬进新表（否则 ATTACH 的约束校验必炸）。
    EXECUTE format(
        'WITH moved AS (
             DELETE FROM usage_facts_default
             WHERE occurred_at >= %L AND occurred_at < %L
             RETURNING *
         )
         INSERT INTO %I SELECT * FROM moved',
        start_ts, end_ts, pname);

    EXECUTE format(
        'ALTER TABLE usage_facts ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
        pname, start_ts, end_ts);
END $$;

-- 首次迁移预建当日 + 次日；后续由 PartitionManager 24h tick 接管。
-- 调用期间分区父表已挂 DEFAULT 分区（537）；PG 允许 DEFAULT 与具体
-- RANGE 分区共存——新一日数据走日分区，越界数据落 DEFAULT，partition
-- pruning 对 WHERE 范围查询仅扫命中分区。存当日行的存量库由函数内
-- 搬移步骤兜底（见上方 2026-09-26 修订注记）。
SELECT ensure_usage_facts_daily_partition(current_date);
SELECT ensure_usage_facts_daily_partition(current_date + 1);
