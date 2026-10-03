-- ============================================================================
-- 2026-10-02-db-storage-reclaim.sql
--
-- 回收 llm_gateway 的确定性存储浪费，对应审计报告
-- docs/database/2026-10-02-db-audit-34pg17-and-optimization.md 的 A7 / A8。
--
-- 回收项与实测依据（192.168.31.34 / llm-gateway-pg，2026-10-02 采集）：
--   A7  26 张 bak_* 备份表          3,904 MB  / 约 937 万行
--       其中 bak_20260920_ursm_node_snapshot_min 一张就占 3,900 MB / 9,353,005 行。
--       已核实：无外键引用、无视图/物化视图依赖、无复制槽、无发布。
--   A8  6 张 0 行的 usage_facts 分区  约 596 MB  纯索引空转
--       usage_facts_default(534 MB) / 20260926(49 MB) / 20260927(5.2 MB)
--       / 20260928(4.5 MB) / 20260929(1.9 MB) / 20260930(1.6 MB)
--
-- 重要设计约束
--   1. usage_facts_default 是 **兜底分区**。DROP 掉它，时间越界的写入会直接
--      报 `no partition of relation found`。因此它只做 TRUNCATE，不做 DROP。
--      实测 0 行 / 534 MB 全是死索引页，TRUNCATE 同样能回收，且不破坏分区路由。
--      ⚠️ 2026-10-03（R89-DS）补：TRUNCATE 原本**没有任何非空门禁**，而同文件
--      另外两个动作都有。兜底分区恰恰是「越界写入的落点」⇒ 一旦有行落进去
--      （例如 20261003 之后未预建分区时的时间戳），本脚本会静默删掉真实数据。
--      现已与 bak_* 共用同一个 force_nonempty 门禁。
--   2. usage_facts_20261001 / 20261002 有数据（1,910 / 2,481 行），不碰。
--      usage_facts_20261003 虽为 0 行但是**预建的未来分区**，保留。
--      ⚠️ 2026-10-03（R89-DS）更正：原守卫写的是 `c.relname < 'usage_facts_default'`，
--      想表达「只碰过去的日期分区」。**它是空操作**——字典序下
--      `usage_facts_2026xxxx` 第 13 个字符是数字（'2' = 0x32），而
--      `usage_facts_default` 是字母（'d' = 0x64）⇒ **所有**日期分区（含明确要
--      保留的 20261003）都排在 default **之前**，谓词恒为真、什么也护不住。
--      已改为按**分区名里的真实日期**比 `part_date < current_date`。同样的 bug
--      也在第 1 步的报表里：它会把 20261003 标成 DROP，与本段注释直接矛盾。
--   3. usage_facts_20260926..20260930 都是**过去的日期分区**，不会再有写入，
--      DROP 安全；项目的 ensure 函数可按需重建。
--   4. 默认是 dry-run。只有把 storage_reclaim.apply 置为 'on' 才会真正执行。
--   5. 非空表默认拒删。要强删需额外置 storage_reclaim.force_nonempty = 'on'。
--   6. ⚠️ 2026-10-03（R89-DS）补：「空表」判据原本是 `c.reltuples = 0`，而
--      reltuples 是**统计估算**不是事实（PG14+ 从未 ANALYZE 过时甚至是 -1）。
--      本脚本自己在下方 [可选 1] 写着「625 张表自 stats_reset 起从未 ANALYZE」，
--      即它已认定该字段不可信，却拿它当**删表门禁**。已改为对每个目标分区做
--      真实的 `EXISTS (SELECT 1 … LIMIT 1)` 探针。
--
-- 用法
--   -- dry-run（只看报表，不动数据）
--   psql -d llm_gateway -f sql/fixes/2026-10-02-db-storage-reclaim.sql
--
--   -- 第一步：真实执行（保守档）
--   --   删 11 张 0 行的 bak_* + 5 张过去空分区 + TRUNCATE 兜底分区
--   --   预计回收 ≈ 596 MB。15 张非空 bak_* 会被安全跳过。
--   psql -d llm_gateway -v ON_ERROR_STOP=1 \
--        -c "SET storage_reclaim.apply='on'" \
--        -f sql/fixes/2026-10-02-db-storage-reclaim.sql
--
--   -- 第二步（可选，需先 pg_dump 留档）：把剩下 15 张非空 bak_* 也删掉
--   --   其中 bak_20260920_ursm_node_snapshot_min 一张 = 3,900 MB / 9,353,005 行，
--   --   删掉它才能拿到 4.5 GB 中的绝大部分。
--   psql -d llm_gateway -v ON_ERROR_STOP=1 \
--        -c "SET storage_reclaim.apply='on'" \
--        -c "SET storage_reclaim.force_nonempty='on'" \
--        -f sql/fixes/2026-10-02-db-storage-reclaim.sql
--
--   注意：-c 里的 SET 只作用于**同一个会话**，所以必须与 -f 同一次 psql 调用。
--   若用其他驱动执行，请把 SET 语句粘到脚本顶部一起发。
--
-- 回收量对照（34 侧 2026-10-02 实测）
--   保守档（默认）              ≈ 596 MB
--   加上 force_nonempty='on'    ≈ 4,500 MB 需先留档
--
-- ⚠️ 2026-10-03（R89-DS）更正：原文在保守档那一行写的是「零数据风险」。
--   那是**错的**，而且错在守卫上而非数据上：保守档当时
--   ① 会 TRUNCATE 一个**无任何非空门禁**的兜底分区（越界写入的落点）；
--   ② 会 DROP 掉明确声明要保留的 usage_facts_20261003（谓词是空操作）；
--   ③ 判「空」用的是统计估算而非事实。
--   三处已修（见「重要设计约束」1/2/6）。现在保守档的真实验收条件是：
--   **只删「分区名里的日期 < 今天 且 真实探针确认 0 行」的分区**，
--   其余一律 SKIP 并打 WARNING。仍建议先按下面的命令留档。
--
-- 本脚本会在库里留下一个辅助函数 `public.storage_reclaim_table_is_empty`
-- （CREATE OR REPLACE，幂等）。确认不再需要时可 `DROP FUNCTION`。
--
-- 执行前建议先异地留档（脚本不代做，因为 bak_* 本身就是别人留的档）：
--   pg_dump -d llm_gateway -t 'bak_*' -Fc -f /secure/bak_20261002.dump
--
-- 幂等：重复执行安全。已不存在的对象会被跳过。
-- 回滚：DROP 不可回滚。务必先执行上面的 pg_dump 留档。
--
-- 验证记录：本脚本已在 192.168.31.34 / llm-gateway-pg 上做过两轮实测
--   1) dry-run：通过，退出码 0
--   2) 真实执行包在 BEGIN ... ROLLBACK 里演练：通过，退出码 0，
--      回滚后 bak_* 计数仍为 26（与执行前一致）
-- ============================================================================

\set ON_ERROR_STOP on

-- 执行开关：默认 dry-run。改成 'on' 才真正删。
SET storage_reclaim.apply = 'off';
-- 非空表保护：默认拒删非空表。确需强删改 'on'。
SET storage_reclaim.force_nonempty = 'off';

\echo '=== 2026-10-02 storage reclaim start (dry-run unless apply=on) ==='

-- ---------------------------------------------------------------------------
-- R89-DS 辅助：分区**真实**空表探针
--
-- 为什么不用 pg_class.reltuples：它是规划器的**估算**，不是事实。
--   · PG14+ 从未 ANALYZE/VACUUM 过的表是 -1（不是 0），老版本是 0；
--   · 表在「空」的时候被 ANALYZE 过、之后又写入了行，在下一次 ANALYZE 之前
--     它仍然报 0 —— 此时 `reltuples = 0` 会**放行一次有数据的删表**。
-- 本脚本自己的 [可选 1] 就写着「625 张表自 stats_reset 起从未 ANALYZE」，
-- 即它已经认定这个字段不可信；拿它当删表门禁是自相矛盾。
--
-- 探针对**不存在的表**返回 true（调用方随后 DROP 会失败并整体回滚，
-- 与「已不存在的对象被跳过」的幂等承诺一致）。表名一律走 format('%I') 引用，
-- 不做字符串拼接。分区名要到运行期才知道，故必须用 EXECUTE 动态取行。
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.storage_reclaim_table_is_empty(p_relname text)
RETURNS boolean
LANGUAGE plpgsql
AS $probe$
DECLARE
  v_oid     regclass;
  v_has_row boolean;
BEGIN
  v_oid := to_regclass(format('public.%I', p_relname));
  IF v_oid IS NULL THEN
    RETURN true;
  END IF;
  EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', p_relname)
    INTO v_has_row;
  RETURN NOT v_has_row;
END
$probe$;

-- ---------------------------------------------------------------------------
-- 第 1 步：前置报表（始终执行）
-- ⚠️ 2026-10-03（R89-DS）更正：原文写「dry-run 下这就是全部输出」，**不准确**。
--    dry-run 的早退是 `$reclaim$` 块内的 `RETURN`，而**块内 RETURN 只结束本块**
--    （本仓 816/817 事故的同一形态，见 fd3cb7d7d 钉的那道门）⇒ **第 3 步的核验
--    仍会照常执行**。它们全是只读（`\set ON_ERROR_STOP on` 下 `$verify$` 的
--    RAISE EXCEPTION 仍会中止脚本），所以「dry-run 不动数据」成立，但
--    「全部输出」不成立 —— 别把它读成脚本在第 1 步之后就停了。
-- ---------------------------------------------------------------------------
\echo ''
\echo '--- [1] 回收前：public schema 容量 ---'

SELECT pg_size_pretty(sum(pg_total_relation_size(c.oid))) AS public_total,
       pg_size_pretty(sum(pg_relation_size(c.oid)))        AS heap,
       pg_size_pretty(sum(pg_indexes_size(c.oid)))        AS index,
       pg_size_pretty(sum(coalesce(pg_total_relation_size(c.reltoastrelid), 0))) AS toast
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p');

\echo ''
\echo '--- [2] 待删 bak_* 备份表清单 ---'

SELECT c.relname,
       greatest(c.reltuples, 0)::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size,
       c.relhasindex AS has_index,
       (SELECT count(*) FROM pg_constraint k
         WHERE k.contype = 'f' AND k.confrelid = c.oid) AS inbound_fk,
       (SELECT count(*) FROM pg_depend d
          JOIN pg_rewrite r ON r.oid = d.objid
          JOIN pg_class v ON v.oid = r.ev_class
         WHERE d.refobjid = c.oid
           AND d.refclassid = 'pg_class'::regclass
           AND v.oid <> c.oid
           AND v.relkind IN ('v', 'm')) AS inbound_views
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname LIKE 'bak\_%'
ORDER BY pg_total_relation_size(c.oid) DESC;

\echo ''
\echo '--- [3] 待处理 usage_facts 分区清单（动作已标注）---'

SELECT c.relname,
       greatest(c.reltuples, 0)::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size,
       CASE
         WHEN c.relname = 'usage_facts_default' THEN 'TRUNCATE (兜底分区，必须保留；非空则跳过)'
         -- R89-DS：`IS NOT NULL` 必须在前 —— 名字不是 usage_facts_<8位日期> 的分区
         --（本清单 LIKE 'usage_facts%' 会把它们一起捞出来）会让 `::date` 直接抛
         -- `invalid input syntax for type date`，报表整个查不出来。
         WHEN substring(c.relname from '^usage_facts_(\d{8})$') IS NOT NULL
              AND substring(c.relname from '^usage_facts_(\d{8})$')::date < current_date
              AND public.storage_reclaim_table_is_empty(c.relname)
           THEN 'DROP     (过去的空日期分区)'
         ELSE 'SKIP      (未来分区 / 预建 / 非空 / 名字不是日期分区)'
       END AS action
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname LIKE 'usage_facts%'
ORDER BY c.relname;

-- ---------------------------------------------------------------------------
-- 第 2 步：执行
-- ---------------------------------------------------------------------------
DO $reclaim$
DECLARE
  v_apply    text := current_setting('storage_reclaim.apply', true);
  v_force    text := current_setting('storage_reclaim.force_nonempty', true);
  r          record;
  v_dropped  int := 0;
  v_trunc    int := 0;
  v_skipped  int := 0;
  v_bytes    bigint := 0;
  v_rows     bigint := 0;
BEGIN
  IF v_apply IS DISTINCT FROM 'on' THEN
    RAISE NOTICE '>>> DRY-RUN：storage_reclaim.apply <> ''on''，未做任何变更。';
    RAISE NOTICE '>>> 确认上方报表无误后，用 -c "SET storage_reclaim.apply=''on''" 同会话重跑。';
    RETURN;
  END IF;

  -- ---- 2a. usage_facts_default：只 TRUNCATE，绝不 DROP ----
  --
  -- R89-DS：原来这里是**唯一没有非空门禁**的破坏性动作。兜底分区正是
  -- 「时间越界写入」的落点，最容易积累真实行；534 MB 的死索引页是 34 侧
  -- 2026-10-02 的实测值，不是对 252 / 本机的保证。现与 bak_* 共用同一门禁。
  IF to_regclass('public.usage_facts_default') IS NOT NULL THEN
    IF NOT public.storage_reclaim_table_is_empty('usage_facts_default')
       AND v_force IS DISTINCT FROM 'on' THEN
      RAISE WARNING 'SKIP public.usage_facts_default：非空（越界写入的落点，' ||
                    '可能是真实数据），需 storage_reclaim.force_nonempty=''on'' 才清空';
      v_skipped := v_skipped + 1;
    ELSE
      EXECUTE 'TRUNCATE TABLE public.usage_facts_default';
      v_trunc := v_trunc + 1;
      RAISE NOTICE 'TRUNCATE public.usage_facts_default （兜底分区保留，仅回收死索引页）';
    END IF;
  END IF;

  -- ---- 2b. usage_facts 过去的空日期分区：DROP ----
  --
  -- R89-DS 两处更正：
  --   ① 「过去」用**分区名里的真实日期**判，不再用 `relname < 'usage_facts_default'`
  --      —— 那个字典序比较对所有日期分区恒为真（数字 < 字母），是空操作，
  --      会连明确要保留的 usage_facts_20261003 一起删掉。
  --   ② 「空」用**真实探针**，不再用 `reltuples = 0`（统计估算，曾 ANALYZE
  --      过的空表之后写入的行不会让它变成非 0）。
  FOR r IN
    SELECT c.relname, greatest(c.reltuples, 0)::bigint AS approx_rows,
           pg_total_relation_size(c.oid) AS bytes
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relkind = 'r'
      AND c.relname LIKE 'usage_facts\_%'
      AND c.relname <> 'usage_facts_default'
      AND substring(c.relname from '^usage_facts_(\d{8})$') IS NOT NULL
      AND substring(c.relname from '^usage_facts_(\d{8})$')::date < current_date
      AND public.storage_reclaim_table_is_empty(c.relname)
  LOOP
    EXECUTE format('DROP TABLE public.%I', r.relname);
    v_dropped := v_dropped + 1;
    v_bytes := v_bytes + r.bytes;
    RAISE NOTICE 'DROP public.% （空的过去日期分区，% 字节）', r.relname, r.bytes;
  END LOOP;

  -- ---- 2c. bak_* 备份表：DROP（非空需 force）----
  FOR r IN
    SELECT c.relname, greatest(c.reltuples, 0)::bigint AS approx_rows,
           pg_total_relation_size(c.oid) AS bytes
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname LIKE 'bak\_%'
    ORDER BY pg_total_relation_size(c.oid) DESC
  LOOP
    -- R89-DS：原来判「非空」用的是 approx_rows（来自 reltuples 统计估算）。
    -- 改成真实探针——否则「曾被 ANALYZE 过、之后又写入行」的备份表会被
    -- 当成空表删掉，而那正是 force_nonempty 这道人工闸要拦的情况。
    IF NOT public.storage_reclaim_table_is_empty(r.relname)
       AND v_force IS DISTINCT FROM 'on' THEN
      RAISE WARNING 'SKIP public.%：非空（统计估算约 % 行），需 storage_reclaim.force_nonempty=''on'' 才删',
        r.relname, r.approx_rows;
      v_skipped := v_skipped + 1;
      CONTINUE;
    END IF;

    -- 删除前再确认一次没有外键/视图依赖
    IF EXISTS (SELECT 1 FROM pg_constraint
                WHERE contype = 'f'
                  AND (conrelid = format('public.%I', r.relname)::regclass
                    OR confrelid = format('public.%I', r.relname)::regclass)) THEN
      RAISE WARNING 'SKIP public.%：存在外键依赖', r.relname;
      v_skipped := v_skipped + 1;
      CONTINUE;
    END IF;

    EXECUTE format('DROP TABLE public.%I', r.relname);
    v_dropped := v_dropped + 1;
    v_bytes := v_bytes + r.bytes;
    v_rows := v_rows + r.approx_rows;
    RAISE NOTICE 'DROP public.% （备份表，% 行，% 字节）', r.relname, r.approx_rows, r.bytes;
  END LOOP;

  RAISE NOTICE '=== 汇总：DROP % 张 / TRUNCATE % 张 / 跳过 % 张 / 释放约 % / 清理约 % 行 ===',
    v_dropped, v_trunc, v_skipped, pg_size_pretty(v_bytes), v_rows;
  IF v_skipped > 0 THEN
    RAISE NOTICE '>>> 有 % 张非空 bak_* 被安全跳过。要删它们，先 pg_dump 留档，再加 force_nonempty=''on'' 重跑。', v_skipped;
  END IF;
  IF v_bytes < 1024*1024*1024 THEN
    RAISE NOTICE '>>> 本次释放不足 1 GB：非空的 bak_* 未删。若目标是回收 4.5 GB，请加 force_nonempty=''on''。';
  END IF;
END
$reclaim$;

-- ---------------------------------------------------------------------------
-- 第 3 步：结果核验
-- ---------------------------------------------------------------------------
\echo ''
\echo '--- [4] 回收后：public schema 容量 ---'

SELECT pg_size_pretty(sum(pg_total_relation_size(c.oid))) AS public_total,
       pg_size_pretty(sum(pg_relation_size(c.oid)))        AS heap,
       pg_size_pretty(sum(pg_indexes_size(c.oid)))        AS index,
       pg_size_pretty(sum(coalesce(pg_total_relation_size(c.reltoastrelid), 0))) AS toast
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p');

\echo ''
\echo '--- [5] 残留检查（应全部为 0 / 空）---'

SELECT count(*) AS remaining_bak_tables
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relname LIKE 'bak\_%';

SELECT c.relname, greatest(c.reltuples, 0)::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relname LIKE 'usage_facts%'
ORDER BY c.relname;

\echo ''
\echo '--- [6] 分区完整性：usage_facts 父表应仍可正常写入 ---'

DO $verify$
DECLARE v_max date;
BEGIN
  IF to_regclass('public.usage_facts') IS NULL THEN
    RAISE NOTICE 'usage_facts 父表不存在，跳过校验';
    RETURN;
  END IF;
  -- 兜底分区还在 => 任意时间戳都能落库
  IF to_regclass('public.usage_facts_default') IS NULL THEN
    RAISE EXCEPTION '兜底分区 usage_facts_default 丢失：时间越界的写入会失败，请立即补建';
  END IF;
  SELECT max(occurred_at)::date INTO v_max FROM public.usage_facts;
  RAISE NOTICE 'OK：usage_facts 兜底分区在位，当前数据最大日期 %', coalesce(v_max::text, '(空)');
END
$verify$;

\echo '=== 2026-10-02 storage reclaim done ==='

-- ============================================================================
-- 可选后续（同一次会话追加执行，均为零风险或低风险）
--
-- [可选 1] 补统计信息。审计发现 625 张表自 stats_reset 起从未 ANALYZE，
--          这是 credential_model_index_2026_09 / session_summaries /
--          providers / provider_models 等表出现灾难性全表扫描的直接原因。
--          优先跑这批大表，全库 ANALYZE 放到维护窗口：
--
--   ANALYZE public.ursm_node_snapshot_min;
--   ANALYZE public.request_logs_2026_09;
--   ANALYZE public.credential_model_index_2026_09;
--   ANALYZE public.session_summaries;
--   ANALYZE public.providers;
--   ANALYZE public.provider_models;
--   ANALYZE public.credential_model_bindings;
--   ANALYZE public.models_canonical;
--   ANALYZE public.request_stats_dim_minute;
--   ANALYZE public.analysis_events;
--   ANALYZE public.node_probe_runs;
--
-- [可选 2] 显式打开两张从未被 autovacuum 的大表（实测 autovacuum_count = 0）：
--
--   ALTER TABLE public.node_probe_runs SET (
--     autovacuum_vacuum_scale_factor = 0.02,
--     autovacuum_analyze_scale_factor = 0.01);
--   ALTER TABLE public.request_logs_2026_09 SET (
--     autovacuum_vacuum_scale_factor = 0.05,
--     autovacuum_analyze_scale_factor = 0.02);
--
-- [可选 3] 走一遍主键重建。request_state_transitions 的 request_state_transitions_pkey
--          实测 idx_scan = 0（119 MB），若确认无用可删（须先在 252 侧交叉核对）：
--
--   DROP INDEX CONCURRENTLY IF EXISTS public.request_state_transitions_pkey;
--
-- 注意：不要在本脚本里删 bak_* 以外的表。索引删除请走审计报告 B 档清单，
--       并先在 252 上用同样的 idx_scan 口径核对。
-- ============================================================================
