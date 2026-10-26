-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内
-- 执行）；installer 的 applySQL 据此走非事务通道（runner.go noTransactionMarker，
-- 845/888/898 同款）。无该标记时全新安装会失败于
-- "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
--
-- Migration 902: stats_event_dedup 索引合并 —— pkey 换体为
--                UNIQUE(event_id) INCLUDE(occurred_at)，删除严格冗余的
--                (event_id, occurred_at) 复合索引（845 所建）。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么（★ 全部为 2026-10-24 252 生产只读实测，读数可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【现状】stats_event_dedup（409 MB）有两条索引：
--     stats_event_dedup_pkey (event_id)                                  = 114 MB
--       —— 536 所建；event_writer.go 的
--          INSERT ... ON CONFLICT (event_id) DO UPDATE SET occurred_at=EXCLUDED...
--          的唯一性载体（dedup 不变式：全表每个 event_id 恰一行）。
--     idx_stats_event_dedup_event_id_occurred_at (event_id, occurred_at) = 151 MB
--       —— 845 所建；daily_monthly_rollup.go:198 与 reconciliation.go 三处
--          `JOIN stats_event_dedup d ON d.event_id=f.event_id AND d.occurred_at=f.occurred_at`
--          的承重索引（Index Only Scan，消除逐行 heap 回表）。
--
-- 【冗余的机制证明】pkey(event_id) 已保证全表 event_id 唯一 ⇒ 复合键的第二列
--   occurred_at 从来没有「同 event_id 第二行」可排序 —— 151 MB 那条索引相对
--   pkey 的唯一增值是「把 occurred_at 装进索引元组」（covering）。
--   ⇒ 把 pkey 换成 UNIQUE(event_id) INCLUDE(occurred_at) 后，唯一性判定只看
--   键列（INCLUDE 不参与唯一性、不参与 ON CONFLICT 推断），而 occurred_at
--   仍在索引元组里 ⇒ JOIN 的双列等值仍然 Index Only、不回 heap。
--   ⇒ 两条索引合并成一条：~265 MB → ~151 MB，净省 ~114 MB（252 实测口径；
--   §143.27.7 当年的「净省 151 MB」按旧 pkey 体量估算，同一方向）。
--
-- 【无损论证（三处消费方逐个过）】
--   ① event_writer.go:176 `ON CONFLICT (event_id)` —— 推断只看键列，
--      INCLUDE 列不参与 ⇒ 逐字不改仍工作（本地一次性库实测：upsert 成功、
--      同 event_id 第二行被新 pkey 拒绝 ⇒ 不变式仍强制）。
--   ② rollup / reconciliation 四处双列等值 JOIN —— 本地一次性库实测（300k 行、
--      VACUUM 后）：换体前 Index Only Scan(idx_...)（Index Cond 双列）⇒
--      换体后 Index Only Scan(stats_event_dedup_pkey)（Index Cond event_id +
--      occurred_at 谓词在索引元组上求值）⇒ 两者都零 heap 访问。
--      实验脚本：scripts/local/143102_p5_dedup_pkey_include_local_experiment.sql。
--   ③ 反向查询（只按 occurred_at 过滤、不带 event_id）—— 旧复合索引本来就
--      帮不上（occurred_at 是第二列），无回归面。
--
-- 【幂等设计】换体段包在 DO 块里，先查目录态：pkey 已带 INCLUDE
--   （indnkeyatts=1 AND indnatts=2）⇒ 整段跳过；重跑路径的兜底
--   DROP INDEX CONCURRENTLY IF EXISTS ..._tmp 保证任何路径都不留孤儿
--   （本地实验阶段 4 实测：无守卫的重跑会留下 20 MB 孤儿索引 ⇒ 这条兜底
--   是被实验抓出来后补的）。
--   ★ 2026-10-24 12h 审计轮补了一条【前置于 ①】的清理：只删**无效**的残留
--   tmp 索引（步骤 ⓪）—— 因为 `IF NOT EXISTS` 只看名字、不看 validity ⇒
--   ⇒ 上一次 CONCURRENTLY 构建失败留下的 invalid 索引会让本迁移**永久卡死**
--   ⇒ 详见步骤 ⓪ 的注释。
--
-- 【换体窗口】DROP CONSTRAINT + ADD CONSTRAINT ... USING INDEX 在同一 DO 块
--   （同一事务）内完成，期间持 ACCESS EXCLUSIVE ⇒ 并发写者排队等待、
--   不会撞见「无唯一约束可匹配 ON CONFLICT」的瞬时窗口；lock_timeout=60s
--   抢不到锁快速失败、整体回滚、稍后重跑（882/888 同款）。
--   ADD CONSTRAINT ... USING INDEX 会把 tmp 索引自动改名为 stats_event_dedup_pkey
--   （PG NOTICE 确认）⇒ 索引名对下游监控查询保持不变。
--
-- ⚠ 生产应用需属主授权（252 水位推进走 apply-db-revision-sequence.sh 通道）。
-- ═══════════════════════════════════════════════════════════════════════

SET lock_timeout = '60s';
SET statement_timeout = '10min';

-- ⓪ 兜底：清掉「上一次 CONCURRENTLY 构建失败」留下的【无效】tmp 索引。
--
-- ★ 为什么必须这一步（★ 2026-10-24 12h 审计轮实测复现，不是推理）：
--   PG 的既定行为 = `CREATE INDEX CONCURRENTLY` 失败后**留下一个 invalid 索引**
--   （pg_index.indisvalid = false / indisready = false）。
--   而步骤 ① 是 `CREATE UNIQUE INDEX CONCURRENTLY **IF NOT EXISTS** <tmp>` ⇒
--   `IF NOT EXISTS` **只按名字判存在、不看 validity** ⇒ ⇒ 遇到这个残留
--   ⇒ 步骤 ① **跳过创建**（只打一条 NOTICE "already exists, skipping"）
--   ⇒ ⇒ 步骤 ② 的形状守卫（要求 indisvalid AND indisready AND indisunique）
--   **必然抛错** ⇒ ⇒ 迁移失败。
--   ⇒ 而失败后每一次重跑都走**完全相同**的路径（① 跳过 → ② 抛错）
--   ⇒ ⇒ **永久卡死**，除非人工 `DROP INDEX`。
--   ⇒ 触发条件不是臆想：statement_timeout=10min 被触发 / 连接中断 / 构建被取消
--   —— 任一都足以留下 invalid 索引。
--   ⇒ 902 是 installer StartupFiles 的**链尾** ⇒ ⇒ 它失败 = 新装链与升级链都断
--   ⇒ ⇒ 不是「一条迁移跳过」而是「安装器起不来」。
--
-- ★ 只删【无效】的：合法的 tmp 索引是步骤 ①→② 的中间产物，删了会让步骤 ①
--   重建（白跑一遍 CONCURRENTLY 构建）。
--
-- ★ 用普通 DROP INDEX（不是 CONCURRENTLY）：本文件是 dbinit:no-transaction，
--   但 DO 块本身**是一个事务块** ⇒ `DROP INDEX CONCURRENTLY` 在事务块内会被
--   PG 拒绝（"cannot run inside a transaction block"）⇒ ⇒ 这里必须用普通
--   DROP；且它只作用在一个 invalid 索引上（没有并发写者依赖它）。
DO $pre$
DECLARE
    v_bad boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
          FROM pg_index ix
          JOIN pg_class i     ON i.oid = ix.indexrelid
          JOIN pg_namespace n ON n.oid = i.relnamespace
         WHERE n.nspname = 'public'
           AND i.relname = 'stats_event_dedup_pkey_include_tmp'
           AND NOT (ix.indisvalid AND ix.indisready)
    ) INTO v_bad;

    IF v_bad THEN
        EXECUTE 'DROP INDEX IF EXISTS stats_event_dedup_pkey_include_tmp';
        RAISE NOTICE '902: dropped INVALID leftover tmp index from a previously failed CONCURRENTLY build';
    END IF;
END
$pre$;

-- ① 建 INCLUDE 体的临时唯一索引（CONCURRENTLY：该表是活写表，event_writer
--    每请求一次 upsert；普通 CREATE INDEX 会在构建期持 ShareLock 阻断写入）。
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stats_event_dedup_pkey_include_tmp
    ON public.stats_event_dedup (event_id) INCLUDE (occurred_at);

-- ② 换体（同一事务内完成，写者无感）：
--    仅当「pkey 尚未带 INCLUDE」时执行；tmp 索引必须有效且形状正确才准换。
DO $swap$
DECLARE
    v_pkey_name text;
BEGIN
    PERFORM set_config('lock_timeout', '60s', true);

    IF EXISTS (
        SELECT 1
          FROM pg_index ix
          JOIN pg_class t     ON t.oid = ix.indrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE n.nspname = 'public'
           AND t.relname = 'stats_event_dedup'
           AND ix.indisprimary
           AND ix.indnkeyatts = 1      -- 键列只有 event_id
           AND ix.indnatts  = 2        -- 且 occurred_at 已是 INCLUDE 列
    ) THEN
        RAISE NOTICE '902: stats_event_dedup pkey already carries INCLUDE(occurred_at) — swap skipped (idempotent)';
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_index ix
          JOIN pg_class i     ON i.oid = ix.indexrelid
          JOIN pg_namespace n ON n.oid = i.relnamespace
         WHERE n.nspname = 'public'
           AND i.relname = 'stats_event_dedup_pkey_include_tmp'
           AND ix.indisvalid
           AND ix.indisready
           AND ix.indisunique
           AND ix.indnkeyatts = 1
           AND ix.indnatts  = 2
    ) THEN
        RAISE EXCEPTION '902: tmp index stats_event_dedup_pkey_include_tmp missing/invalid/wrong-shape — refusing to swap pkey';
    END IF;

    SELECT c.conname INTO v_pkey_name
      FROM pg_constraint c
     WHERE c.conrelid = 'public.stats_event_dedup'::regclass
       AND c.contype  = 'p';
    IF v_pkey_name IS NULL THEN
        RAISE EXCEPTION '902: stats_event_dedup has no primary key constraint — refusing to swap (unexpected state)';
    END IF;

    EXECUTE format('ALTER TABLE public.stats_event_dedup DROP CONSTRAINT %I', v_pkey_name);
    ALTER TABLE public.stats_event_dedup
        ADD CONSTRAINT stats_event_dedup_pkey PRIMARY KEY
        USING INDEX stats_event_dedup_pkey_include_tmp;
    -- ADD CONSTRAINT ... USING INDEX 会把 tmp 索引改名为 stats_event_dedup_pkey。
END
$swap$;

-- ③ 删除被合并的 845 复合索引（151 MB @ 252）。
DROP INDEX CONCURRENTLY IF EXISTS idx_stats_event_dedup_event_id_occurred_at;

-- ④ 兜底：重跑 / 已应用后再跑的任何路径都不留孤儿 tmp 索引
--    （本地实验阶段 4 实测：无此兜底时重跑留下 ~20 MB 孤儿）。
DROP INDEX CONCURRENTLY IF EXISTS stats_event_dedup_pkey_include_tmp;

COMMENT ON INDEX stats_event_dedup_pkey IS
    '902: pkey 换体 UNIQUE(event_id) INCLUDE(occurred_at) —— 唯一性判定只看 event_id（INCLUDE 不参与），occurred_at 装进索引元组使 rollup/reconciliation 的双列等值 JOIN 仍 Index Only；合并并取代 845 的 (event_id, occurred_at) 复合索引（252 实测省 ~114 MB）。';
