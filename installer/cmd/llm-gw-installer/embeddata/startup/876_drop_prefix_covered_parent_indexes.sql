-- 876_drop_prefix_covered_parent_indexes.sql
-- 2026-10-18：删掉三条**声明在分区父表上**、且被同表宽索引逐列覆盖的窄索引。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（§78 的第二段判据在生产上打出来的）
--
-- §78 给 pg17-duplicate-index-check.sh 加了第二段「前缀覆盖」判据，
-- 在全库扫出 17 对「窄索引被宽索引覆盖」，其中最贵的两条是
--   usage_ledger_2026_09_request_id_idx  169 MB
--   usage_ledger_2026_10_request_id_idx   50 MB
-- ★ 但**只删分区上的索引是没有用的**，本文件就是这件事的证据：
--
--   sql/objects/functions/ensure_usage_ledger_partition_timestamp_with_time_zone.sql
--     CREATE TABLE %I PARTITION OF usage_ledger FOR VALUES FROM .. TO ..
--   `CREATE TABLE … PARTITION OF` 会**克隆父表上声明的每一个索引**。
--   而父表 usage_ledger 上同时声明着
--     idx_usage_ledger_part_request_id  (request_id)          ← 窄
--     idx_usage_ledger_part_ts          (ts)                  ← 窄
--     usage_ledger_partitioned_request_id_ts_key (request_id, ts) UNIQUE  ← 覆盖方
--     idx_usage_ledger_recon_cover      (request_id, ts DESC) INCLUDE(5 列) ← 覆盖方
--     usage_ledger_ts_tenant_cache_agg_idx (ts, tenant_id) INCLUDE(7 列)     ← 覆盖方
--   sessions 父表同理：
--     idx_sessions_session_id (session_id) ← sessions_session_id_partition_date_key UNIQUE (session_id, partition_date)
--
--   ⇒ 删掉分区上的窄索引，**下个月建分区时会原样长回来**。
--   生产实测印证：usage_ledger_2026_11（当月还没到，分区已预建）
--   已经带着全部 6 个索引，各 8192 bytes。
--
-- 为什么在**父表**上删：PG 对分区父表执行 DROP INDEX 会**级联到所有分区**，
-- 一条语句同时止血（父表不再声明）与回收（现存分区），不必逐分区点名。
--
-- -----------------------------------------------------------------------
-- 收益（生产只读实测，2026-10-18 19:5x）
--
--   一次性回收（现存分区）
--     idx_usage_ledger_part_request_id → 169 MB + 50 MB        = 219 MB
--     idx_usage_ledger_part_ts         →  49 MB + 13 MB        =  62 MB
--     idx_sessions_session_id          → 9.3 MB + 15 MB        =  24 MB
--                                                          合计 ≈ 305 MiB
--
--   每月复发（这才是重点）
--     usage_ledger_2026_09 一个整月分区（1,416,772 行）就长出
--     request_id_idx 168.6 MiB + ts_idx 49.1 MiB = 217.7 MiB
--     ⇒ 约 243 MiB/月 ≈ **2.9 GB/年**，且随流量上涨。
--     sessions 侧约 25 MiB/月。
--
--   写放大（不是次要收益）
--     usage_ledger 是最热的写表：pg_stat_statements 窗口内
--     INSERT 236,621 次 + UPDATE 150,956 次。索引从 6 条降到 4 条
--     ⇒ 每次写少维护两份 B-tree。
--
-- -----------------------------------------------------------------------
-- 为什么删得掉（证据，不是推理）
--
-- 1. 读方普查：pg_stat_statements 里 query ILIKE '%usage_ledger%'
--    AND query ~* 'request_id' 的语句，**只有一条**读查询——
--    870 修过的月度对账 `SELECT DISTINCT ON (request_id) …`。
--    它现在走的是 idx_usage_ledger_recon_cover 的 Index Only Scan
--    （870 台账实测：129.2s → 8.7s，计划名即该索引）。
--    其余全是 usage_ledger_hot 的 INSERT/UPDATE（写，不靠这三张表的索引）。
--    ⇒ **没有任何读查询依赖这三条窄索引**。
--
-- 2. planner 取舍比 §77 温和：三条覆盖方里有两条是 INCLUDE 覆盖索引，
--    比窄索引**多携带载荷**；一条是 UNIQUE。
--    单列点查 `WHERE request_id = ?` 改走 UNIQUE(request_id, ts)
--    是等价前缀探测；`WHERE session_id = ?` 同理。
--    §77 的取舍（planner 按字节偏好更小索引）在这里不构成回退。
--
-- ⚠ idx_usage_ledger_part_tenant (tenant_id, ts) **刻意保留**：
--   它没有覆盖方（父表上没有任何以 tenant_id 开头的更宽索引）。
--
-- -----------------------------------------------------------------------
-- ★ 守卫：覆盖关系在**本文件里现算**，不写死结论。
--   写死的守卫会随 schema 演化悄悄失效 —— 覆盖方被改名或被删之后，
--   「删掉它反正还有那个」的假设不会有人再检查一次。
--   下面这段从 pg_index 重新证明：存在一个同表索引，其键列的前缀
--   与窄索引**逐列等长**（列/opclass/排序/排序规则全同），
--   且窄索引的 INCLUDE 列也被它覆盖，且它 valid & ready。
--   任一条证明不成立 ⇒ RAISE EXCEPTION，整个迁移回滚，一条索引都不删。
--
--   ★ 反向对照已实测（不是恒真）：把目标换成 idx_usage_ledger_part_tenant
--     （无覆盖方）⇒ proven_covered_by 为 NULL ⇒ 会被拒。
--
-- ★ 复刻 §78 的量具教训：indkey/indoption/indclass/indcollation 是
--   int2vector/oidvector，**下界为 0**，`::int2[]` 不换下界，
--   (col::int2[])[1:n] 会静默取到第 2..n 个元素 ⇒ 比较恒假。
--   所以下面一律用 string_to_array(array_to_string(...)) 重新定基。
--
-- -----------------------------------------------------------------------
-- 锁与回滚
--
-- PG **不支持**在分区父表上 DROP INDEX CONCURRENTLY，
-- 因此本文件走单事务链 + 普通 DROP INDEX：它对父表与每个分区各取一次
-- ACCESS EXCLUSIVE，但只改元数据、不重写数据，正常是毫秒级。
-- lock_timeout=2s —— 抢不到锁就**快速失败**，不排在写流量后面；
-- 失败时事务整体回滚，什么都没发生，稍后重跑即可。建议低峰窗口执行。
--
-- 回滚：down 文件按名在父表重建（**不带 ON ONLY**，否则不会级联到分区），
-- 于是所有分区会被重新构建一次 —— 大分区实测秒级，但它**不是**零成本，
-- 且会重新带回每月约 243 MiB 的复发。回滚前请先读 down 文件头。

BEGIN;

SET LOCAL statement_timeout = '5min';
SET LOCAL lock_timeout = '2s';

DO $guard_876_coverage$
DECLARE
  v_uncovered text;
  v_droppable  text;
BEGIN
  WITH targets(narrow) AS (
    VALUES
      ('public.idx_usage_ledger_part_request_id'::text),
      ('public.idx_usage_ledger_part_ts'::text),
      ('public.idx_sessions_session_id'::text)
  ),
  proven AS (
    SELECT t.narrow
    FROM targets t
    WHERE to_regclass(t.narrow) IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM pg_index ni
        JOIN pg_class nic ON nic.oid = ni.indexrelid
        JOIN pg_am namb ON namb.oid = nic.relam
        CROSS JOIN LATERAL (
          SELECT
            string_to_array(array_to_string(ni.indkey::int2[], ','), ',')::int2[] AS keys,
            string_to_array(array_to_string(ni.indoption::int2[], ','), ',')::int2[] AS opts,
            string_to_array(array_to_string(ni.indclass::oid[], ','), ',')::oid[] AS opl,
            string_to_array(array_to_string(ni.indcollation::oid[], ','), ',')::oid[] AS colls,
            ni.indnkeyatts,
            (string_to_array(array_to_string(ni.indkey::int2[], ','), ',')::int2[])
              [ni.indnkeyatts+1:ni.indnatts] AS incs
        ) n
        JOIN pg_index ci
          ON ci.indrelid = ni.indrelid
         AND ci.indexrelid <> ni.indexrelid
        JOIN pg_class cic ON cic.oid = ci.indexrelid
        JOIN pg_am camb ON camb.oid = cic.relam
        CROSS JOIN LATERAL (
          SELECT
            string_to_array(array_to_string(ci.indkey::int2[], ','), ',')::int2[] AS keys,
            string_to_array(array_to_string(ci.indoption::int2[], ','), ',')::int2[] AS opts,
            string_to_array(array_to_string(ci.indclass::oid[], ','), ',')::oid[] AS opl,
            string_to_array(array_to_string(ci.indcollation::oid[], ','), ',')::oid[] AS colls,
            ci.indnkeyatts,
            (string_to_array(array_to_string(ci.indkey::int2[], ','), ',')::int2[])
              [ci.indnkeyatts+1:ci.indnatts] AS incs
        ) c
        WHERE ni.indexrelid = to_regclass(t.narrow)
          AND namb.amname = 'btree' AND camb.amname = 'btree'
          -- 窄索引自身不能有任何独立语义
          AND ni.indpred IS NULL AND ni.indexprs IS NULL AND NOT ni.indisunique
          AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conindid = ni.indexrelid)
          -- 覆盖方必须是可用的普通 btree
          AND ci.indpred IS NULL AND ci.indexprs IS NULL
          AND ci.indisvalid AND ci.indisready
          -- 严格更长，否则与「逐字相同」重复
          AND c.indnkeyatts > n.indnkeyatts
          AND (c.keys)[1:n.indnkeyatts]  = n.keys
          AND (c.opts)[1:n.indnkeyatts]  = n.opts
          AND (c.opl)[1:n.indnkeyatts]   = n.opl
          AND (c.colls)[1:n.indnkeyatts] = n.colls
          AND (n.keys || COALESCE(n.incs, '{}'::int2[]))
              <@ (c.keys || COALESCE(c.incs, '{}'::int2[]))
      )
  ),
  present AS (
    -- ★ 与 present_not_proven 分开算：NOTICE 要报的是**实际存在**的那些，
    --   早先写成「两个变量都从 present_not_proven 聚合」，于是覆盖一旦成立
    --   v_droppable 就是 NULL，NOTICE 会说「全部已不存在」——
    --   而三条索引明明都在，下一秒就要被 DROP。
    --   迁移输出撒谎比不输出更糟：操作者会据此以为无事发生。
    SELECT t.narrow
    FROM targets t
    WHERE to_regclass(t.narrow) IS NOT NULL
  ),
  present_not_proven AS (
    SELECT t.narrow
    FROM present t
    WHERE NOT EXISTS (SELECT 1 FROM proven p WHERE p.narrow = t.narrow)
  )
  SELECT (SELECT string_agg(narrow, ', ') FROM present_not_proven),
         (SELECT string_agg(narrow, ', ') FROM present)
    INTO v_uncovered, v_droppable;

  IF v_uncovered IS NOT NULL THEN
    RAISE EXCEPTION
      '876: 以下目标索引未被证明覆盖（可能覆盖方已改名/失效，或它已不是纯前缀），拒绝删除: %',
      v_uncovered;
  END IF;

  RAISE NOTICE '876: 覆盖证明通过，实际存在并将删除: %', coalesce(v_droppable, '(无，三条都已不存在)');
END
$guard_876_coverage$;

-- DROP INDEX 作用于分区父表会级联到所有分区（PG 11+），
-- 同时完成两件事：父表不再声明（止血）+ 现存分区的实例被回收。
DROP INDEX IF EXISTS public.idx_usage_ledger_part_request_id;
DROP INDEX IF EXISTS public.idx_usage_ledger_part_ts;
DROP INDEX IF EXISTS public.idx_sessions_session_id;

COMMIT;

-- ---------------------------------------------------------------------------
-- 怎么验收（★ 别按父表大小判成败，也别按索引名 grep）
--
-- 父表上的索引显示 0 bytes 是分区父表的**正常渲染**；而 PG 给分区索引
-- 自动改名（`idx_usage_ledger_part_ts` → `usage_ledger_2026_09_ts_idx`），
-- 按名字 grep 会得出「没删」的假结论。用下面这条，按**列定义**判定：
--
--   SELECT i.indrelid::regclass::text AS partition,
--          pg_size_pretty(pg_relation_size(i.indexrelid)) AS size
--   FROM pg_index i
--   WHERE i.indrelid IN ('public.usage_ledger'::regclass, 'public.sessions'::regclass)
--     AND i.indnkeyatts = 1
--     AND pg_get_indexdef(i.indexrelid, 1, true) IN ('request_id', 'ts', 'session_id')
--   ORDER BY 1;
--
-- 期望：**0 行**。若仍有行，说明分区侧没级联掉，重跑本文件即可（幂等）。
--
-- 反向验收（确认覆盖方还在，这是删之前最该看的那一眼）：
--
--   SELECT ic.relname, pg_get_indexdef(i.indexrelid)
--   FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
--   WHERE i.indrelid IN ('public.usage_ledger'::regclass, 'public.sessions'::regclass)
--     AND i.indisvalid AND i.indisready
--     AND i.indnkeyatts > 1
--   ORDER BY 1;
--
-- 期望：usage_ledger 剩 4 条（part_tenant / recon_cover / request_id_ts_key /
-- ts_tenant_cache_agg_idx），sessions 剩若干条且含
-- sessions_session_id_partition_date_key。
-- ---------------------------------------------------------------------------
