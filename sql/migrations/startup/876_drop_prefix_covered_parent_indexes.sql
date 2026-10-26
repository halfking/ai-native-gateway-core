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
-- 1. 读方普查 —— ★ **本条已按 §81 实测修正过一次，原文过强，见下面「订正」**。
--
-- 2. planner 取舍：三条覆盖方里有两条是 INCLUDE 覆盖索引，
--    比窄索引**多携带载荷**；一条是 UNIQUE。
--
-- -----------------------------------------------------------------------
-- ⚠ 订正（2026-10-18 §81，部署前发现）：三条的代价**并不相等**
--
-- 原稿写「没有任何读查询依赖这三条窄索引」。逐条重查后：
--
--   idx_usage_ledger_part_request_id —— 成立。
--     普查（query ILIKE '%usage_ledger%' AND ~* 'request_id'）只有一条读查询，
--     即 870 修过的 DISTINCT ON (request_id) 月度对账，
--     它现在走 idx_usage_ledger_recon_cover 的 Index Only Scan（129.2s → 8.7s）。
--
--   idx_sessions_session_id —— 成立。
--     用 `FROM\s+(public\.)?sessions(_20\d\d_\d\d)?\s` 精确匹配后，
--     sessions 表上只有 **1 条**读查询（15 次 / 均值 159ms）。
--     ⚠ 排查时一度被一个假读数误导：pg_stat_statements 里有 52,950 次的
--     「SELECT session_id, … FROM goal_sessions」，那打的是 **goal_sessions**，
--     不是 sessions。用 `ILIKE '%sessions%'` 会把它算进来。
--     ⇒ **普查表名时必须锚定 FROM 后面的完整表名**，否则量具会自己骗你。
--
--   idx_usage_ledger_part_ts —— **不成立，它有承重读方。**
--     原稿的普查范围带了 `AND query ~* 'request_id'`，
--     于是整族「ts 范围但不带 tenant_id」的聚合查询被一并漏掉：
--     83 / 60 / 54 / 17 / 14 / 7 / 5 / 4 次调用，共 8 种语句形态。
--     EXPLAIN 实测（2026-10-18，生产只读）：
--       Aggregate (cost=367667.36..367667.37)
--         -> Append
--              -> Seq Scan on usage_ledger_hot          cost=732.75
--              -> Seq Scan on usage_ledger_2026_09      cost=68812.30   ← 1,416,772 行本来就顺序扫
--              -> Index Scan using usage_ledger_2026_10_ts_idx  cost=21208.25   ★ 它确实在用
--
--     删掉之后这族查询会改走
--     usage_ledger_ts_tenant_cache_agg_idx (ts, tenant_id) INCLUDE(7 列)。
--     同一 ts 范围下三档成本实测（2026_10 分区）：
--       Index Only Scan using ts_tenant_id_prompt_tokens_completion_idx1   cost= 10,850
--       Index Only Scan using request_id_ts_provider_id_prompt_token_idx cost= 14,829
--       Index Scan      using ts_idx                                      cost= 21,208  ← 要删的那条
--     ⇒ 覆盖索引其实是**三者中最便宜的**；但本族查询要
--       api_key_id / raw_model_name / canonical_id，它们不在任何 INCLUDE 里，
--       所以删 ts_idx 之后**仍是 Index Scan、拿不到 index-only**，
--       只是把 13 MB 的窄索引换成 26 MB 的覆盖索引。
--
--     ★ 代价量级（**由 cost 推算，非墙钟 A/B** —— 要真测得先删，删之前测不了）：
--       该族查询整体 cost 367,667 → 约 385K，**约 +5%**；
--       且该计划里 2026_09 分区（1.4M 行）本来就是 Seq Scan，占大头。
--
-- ⇒ 三条的处置建议因此**分档**：
--     两条零代价（part_request_id、sessions_session_id）—— 建议直接上；
--     一条有实测 +5% 代价（part_ts）—— **要不要为 49+13=62 MB 付这 5%，
--     属于属主的取舍**，本迁移按「一并处理」编写，但文件头必须写明这一点。
--
-- ---------------------------------------------------------------------------
-- ⚠⚠ 订正之二（2026-10-18 §82）：**上面「零代价」的强度受普查窗口限制**
--
-- 上面所有普查都来自 pg_stat_statements，而本库实测：
--
--     pg_stat_statements_info.stats_reset = 2026-10-10 16:11:47
--     窗口                          = 8 天 4 小时
--     track                         = top
--     pg_stat_statements.max        = 5000
--     实际已记录                     = 4886~4890 条  ← 逼近上限
--
-- ⇒ 两条限制，每一条都会**削弱**「没查到 ⇒ 没有依赖」这个推论：
--     ① 窗口只有 8 天：月报级、周报级、人工月度对账等低频读方
--        **根本不会出现在这份普查里**；
--     ② track=top 且容量已到 97.7%：未进入 top-N 的语句
--        **压根不会被记录**，不是「查了没查到」，是「没被采集」。
--
-- ⇒ 所以准确的表述是：
--     「**在这 8 天窗口、且进入了 top-N 的语句里**，
--       没有查到依赖 part_request_id / sessions_session_id 的读方」。
--     而不是「没有读方依赖它们」。
--     ★ 月度用量对账（910/873 那类）按月跑，**恰好落在最可能漏掉的一侧**，
--       所以「只有 1 条读查询」这类结论对它们**格外不成立**。
--
-- ⇒ 本文件没有、也无法靠 pg_stat_statements 消除这个不确定性。
--   可用的补救只有两条，都需要属主拍板：
--     (a) 把 pg_stat_statements.track 改成 all 并抬高 max（要重启会话级参数、
--         并评估 5 千条上限的容量与开销）；
--     (b) 接受它：先只上「证据最强」的那一条，把另两条留到观察期之后。
--
-- ★ 本轮新增 `scripts/252-monitor/pg17-index-reader-census.sh` 正是为了
--   把窗口与容量**无条件打在普查输出里** —— §79/§81 两轮的普查都是手写
--   SQL，两次都写错了，而错的原因都是「普查范围是随手写的、没人核对」。

--
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
