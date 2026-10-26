-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，718 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/877_session_turns_search_text_trgm.sql
-- Migration:     877
-- Database:      llm_gateway
-- Purpose:       给 session_turns 分区父表补 search_text 的 GIN trigram 索引（并删一条
--                640 留下的、对恒 NULL 列的部分索引），
--                让 S4 停写后 /api/logs?q= 的 `search_text ILIKE '%…%'` 谓词
--                不再对全部月分区顺序扫描
--
-- Status:        active
-- Idempotent:    YES（三段全部带 IF NOT EXISTS / 幂等 DO 守卫）
-- Dependencies:  430（session_turns 家族）；874（同族部分索引先例）；
--                00-prereqs（pg_trgm 扩展，本迁移不新建扩展）
--
-- Background（缺陷本体）:
--   S4 停写（storage.request_logs_write_enabled=false，生产 2026-10-13 生效）
--   后新请求只落 session 家族，而 `request_logs_with_current_month`
--   （816/824 重建）的前两条 UNION ALL 臂正是 session_turns_hot / session_turns。
--   admin/logs.go:540 的搜索过滤是
--       rl.search_text ILIKE '%' || $n || '%'
--   ——**前导通配符**，btree 完全无法服务。session 家族全族没有任何
--   search_text 索引（本轮前 grep 全 startup/ 与 schema/：只有 request_logs 族
--   经 ensure_request_logs_partition 建过 gin_trgm_ops），于是这条谓词退化为
--   对每个月分区顺序扫描 search_text。session_turns_2026_09 单分区 6.4GB，
--   search_text 是 TOAST 宽列，扫它的代价远高于扫窄列。
--
--   对照（同一能力的既有形态，见 sql/schema/01-schema.sql:1933-1942）：
--   request_logs 族每个分区由 ensure_request_logs_partition 建
--       idx_%s_search_trgm  USING gin (search_text gin_trgm_ops)
--   那是 **v1 族的既有能力**，session 族从未迁移过它。本迁移补齐。
--
-- 为什么只索引 search_text、**不**给 model / client_model 建 trigram:
--   - session_turns 上**根本没有 client_model 列**（全 startup/ grep：
--     client_model 只存在于 request_logs / session_summaries.client_models /
--     session_audit，对 session_turns 零命中；turn_writer.go 的 INSERT 列清单
--     里也没有）。给一个不存在的列建索引无从谈起。
--   - model 等值过滤（admin/logs.go:567 的 ?model=）走的是 **精确匹配 =**，
--     不是 ILIKE（2026-08-29 刻意从 '%…%' 改回精确匹配）。btree 已经服务得了，
--     GIN trigram 在这里是纯写放大。
--
-- ★ 为什么走 727/729 的**三段式 CONCURRENTLY**、而不是父表上普通 CREATE INDEX
--   （本条是本轮审计推翻的第一版设计，务必保留）:
--   session_turns 是**活写**分区父表。父表上的普通 CREATE INDEX 会级联到每个
--   分区并在**整个构建期持 ShareLock 阻断写入**（读照常）。对 874 那种
--   「部分索引、约 1/9 行、btree、秒级」的索引，这个代价可以接受——874 自己的
--   注释就写着「秒到十秒级，随部署窗口应用」。但本索引是
--   **GIN trigram、覆盖 100% 行、且 search_text 是 TOAST 宽列**：对每个值都要
--   抽 trigram，6.4GB 分区属**分钟级**构建。把一个只在「秒级 + 部署窗口」前提下
--   才成立的取舍，直接套到一个分钟级的活写表上，是拿 874 的先例给 877 背书——
--   机制相同，代价差一个数量级，前提不成立。
--
--   PG17 在分区父表上不支持 CREATE INDEX CONCURRENTLY（252 真库实测 42809，
--   842/846/862/870/874 同一实证）。因此采用 727/729 已落地并被门
--   （sql/migrations/startup/migration_727_test.go 逐段校验）固化的标准三段式：
--     1) 对每个**已存在**的分区单独 CONCURRENTLY 建（\gexec 生成；全新库无分区
--        时自然为空操作，不阻塞任何写入）；
--     2) 父表 **ON ONLY** 建分区索引壳（无扫描，仅短暂元数据锁）；
--     3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
--   之后 ensure_sessions_v2_partitions() 以 CREATE TABLE … PARTITION OF 建的
--   未来月分区会自动继承父表分区索引，无需模板动作。
--
--   ⚠ 一条必须写下来的实测事实，它纠正了一个流传的错误说法：
--   **「父表 CREATE INDEX 只挂父表、分区上没有」是错的。**
--   在一次性 PostgreSQL 17.11 上实测：
--   ① DDL 文本里**不写** ON ONLY 时，pg_get_indexdef 对**父表**那条依然渲染出
--      `ON ONLY` 字样——那是渲染层对分区父表索引的固定表述，**不能**用它判断
--      有没有级联；
--   ② 同一条 CREATE INDEX 执行后，两个月分区都拿到了自己的 trigram 索引
--      （自动改名 st_2026_09_search_text_idx 等），indisvalid=t；
--   ③ 之后 ATTACH 的新分区同样自动继承。
--   ⇒ 验收必须按**分区级 + 定义匹配**查 pg_index，不能按父表定义里的 ON ONLY
--      文字判断，也不能按索引名 grep（PG 按分区名+列名自动改名）。
--
-- 超时：共享 PG 服务端 statement_timeout=30s，GIN trigram 构建必然击穿，
--   57014 之后迁移永不记账、成为永久卡点（874 同因）。本文件走非事务通道，
--   故用**会话级** SET（每个文件是一次独立 psql 进程，会话级设置不外溢）；
--   **不能用 SET LOCAL**——没有事务块时它无意义。
--   lock_timeout 抢不到锁则整体失败、什么都没发生，稍后重跑即可。
--
-- 回滚：.down.sql 逐段反向（删父壳会级联删各分区子索引；hot 侧独立删），
--   并按 640 原样重建被删掉的 upstream_protocol 索引。不动数据。
--
-- 本迁移同时**删掉一条死索引**（640 建的
-- idx_session_turns_hot_upstream_protocol），取舍与代价口径见文件末该段。
-- ===========================================================================

SET statement_timeout = '10min';
SET lock_timeout = '60s';

-- ── 第 0 段：自愈被 applySQL 的 5 分钟 ctx 掐断的残留 ────────────────
-- applySQL 的 ctx 是 5 分钟（exec.CommandContext，runner.go）。CONCURRENTLY
-- 是两阶段扫描，被掐断会留下 **indisvalid=false 的半成品索引**：对象在、名字在、
-- 但 planner 不会用它。IF NOT EXISTS 对这种对象**不生效**（它只判存在性），
-- 于是「重跑一次」看起来跑了、实际上什么都没修——这正是最坏形态。
-- 因此建之前先把同名 invalid 索引 DROP 掉，让重跑真正重建。
DO $pre_877_heal$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT ci.relname
    FROM pg_index i
    JOIN pg_class ci ON ci.oid = i.indexrelid
    JOIN pg_class ct ON ct.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = ct.relnamespace
    WHERE n.nspname = 'public'
      AND ct.relname LIKE 'session_turns%'
      AND NOT i.indisvalid
      AND ci.relname LIKE '%search_trgm'
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', r.relname);
    RAISE NOTICE '877: dropped leftover invalid index public.%', r.relname;
  END LOOP;
END
$pre_877_heal$;

-- ── 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ──────────
-- 全新库尚无月分区时本段自然为空操作，不阻塞任何写入。
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I USING gin (search_text gin_trgm_ops)',
  c.relname || '_search_trgm', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
)

-- ★ R76 订正：分区在父索引下**已有成员**时，本段跳过该分区。
--   PG 的硬约束（一次性 PG 17 实测）：一个分区在**同一个父索引**下只能有
--   一个成员索引，且**与成员定义是否同义无关** —— 同义成员也报
--   "Another index is already attached for partition"。
--   而 PG 在分区加入父表、或父分区索引存在后再建分区时，会**自动**给分区
--   建一个匹配索引并自动命名（形如 <partition>_<column>_idx，如
--   session_turns_2026_10_search_text_idx）—— 那才是**生产上的真实形态**
--   （252 实测：父索引 idx_session_turns_search_trgm 的 6 个成员全是自动名，
--   一个 _search_trgm 都没有）。
--   本迁移按固定后缀建的名字与它**同义但不同名**：在已有成员的情况下再建
--   ⇒ 立刻变成「重复索引 + 永远挂不上去的孤儿」，并让下一段的 ATTACH 撞槽位
--   ⇒ 迁移失败 ⇒ 官方门禁 abort（R73 实测 exit=2，5 条未登记失败里 3 条是它）。
--   ⇒ 这里按「分区级 + 是否已挂父成员」判定，而不是按索引名
--     （877 头部 :72-73 自己写的就是这条验收要求：不能按索引名 grep）。
AND NOT EXISTS (
  SELECT 1
  FROM pg_inherits ci
  JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
  WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_trgm')
    AND mi.indrelid = c.oid
)
ORDER BY c.relname
\gexec

-- ── 第 2 段：热侧 session_turns_hot 独立建 ─────────────────────────────
-- session_turns_hot 是独立 hot 表（dual-write 架构），父表索引**不级联**到它，
-- 必须单独建；漏掉这条腿会让 view 的第一条 UNION ALL 臂仍然顺序扫描
-- ——727 的门就是专门守这一腿的。
-- 同为 CONCURRENTLY，所以也放进 \gexec：**本文件里所有 CONCURRENTLY 语句都必须
-- 独立自动提交**，一旦和别的语句混在一次 Exec 里就会落进隐式事务块而报 25001。
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.session_turns_hot USING gin (search_text gin_trgm_ops)',
  'idx_session_turns_hot_search_trgm')
\gexec

-- ── 第 3 段：父表分区索引壳（ON ONLY，无扫描，仅元数据锁）────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_search_trgm
  ON ONLY public.session_turns USING gin (search_text gin_trgm_ops);

-- ── 第 4 段：ATTACH 已建好的子索引（幂等守卫，727/729 同款）────────────
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.session_turns'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_search_trgm')) IS NOT NULL
       -- ★ R76 订正：判据从「**我建的那个子索引**挂没挂」改成
       --   「**该分区**在父索引下有没有成员（名字不限）」。
       --   旧判据在「该分区已有 PG 自动命名的成员」时会认为「没挂」⇒ 去 ATTACH
       --   ⇒ 撞 PG 的「一个分区一个成员」硬约束 ⇒ 迁移失败（官方门禁 abort）。
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
         WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_trgm')
           AND mi.indrelid = to_regclass(format('public.%I', part))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_search_trgm ATTACH PARTITION public.%I',
                     part || '_search_trgm');
    END IF;
  END LOOP;
END $$;

-- ── 形状校验：建完必须每个分区子索引都 valid ──────────────────────────
-- CONCURRENTLY 被 ctx 掐断会留下 indisvalid=false 的半成品：对象在、planner
-- 不用。第 0 段只在**下次**运行自愈；这一次必须当场炸出来，否则它会静默地
-- 留在库里，直到某天有人发现"索引明明在、EXPLAIN 却还在 Seq Scan"。
-- 这里显式报错，让 applySQL 当次失败、迁移不记账。
DO $verify_877_valid$
DECLARE
  bad text;
BEGIN
  SELECT string_agg(ci.relname, ', ' ORDER BY ci.relname) INTO bad
  FROM pg_index i
  JOIN pg_class ci ON ci.oid = i.indexrelid
  JOIN pg_class ct ON ct.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = ct.relnamespace
  WHERE n.nspname = 'public'
    AND ct.relname LIKE 'session_turns%'
    AND ci.relname LIKE '%search_trgm'
    AND NOT i.indisvalid;
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '877: trigram 索引构建未完成（indisvalid=false，通常是 applySQL 的 5 分钟 ctx 掐断了 CREATE INDEX CONCURRENTLY 的第二阶段扫描）: % 。下一次重跑本迁移会自动 DROP 并重建；若仍超时，请在会话外手工执行本文件。', bad;
  END IF;
END
$verify_877_valid$;

-- ── 附带：删掉 640 建的 idx_session_turns_hot_upstream_protocol ──────────
-- 640 建了两条同名形态的部分索引（client_protocol / upstream_protocol）。
-- 其中 **upstream_protocol 这条是死的**，两条独立的理由：
--   ① 写入侧：turn_writer.go 的 INSERT 列清单里只有 client_protocol，
--      **没有 upstream_protocol**，全仓也无第二个写入方 ⇒ 该列在
--      session_turns_hot 上恒为 NULL，部分索引的谓词
--      `upstream_protocol IS NOT NULL` **永不成立**。
--   ② 读取侧：全仓 grep `upstream_protocol`，除本索引自身与 640 的列定义
--      外，唯一命中是 domains/hooks/observability/telemetry/client.go:2692 ——
--      那条 UPDATE 打的是 **_request_logs_hot**（v1 表），与本族无关。
--      ⇒ 没有任何查询按 session_turns.upstream_protocol 过滤。
--
-- 为什么选「删索引」而不是「补写入」：补写入要跨
-- ProcessedRequest → v2.ProcessedRequest（dual_writer.convertToV2Request）
-- → TurnRecord 三层新增字段，而 **上游语义源**（egressProtocol）目前只在
-- streaming/handler.go 流向 telemetry 的 RequestLogEntry，session 家族的
-- 写入点拿不到它。为一个零读者的列铺三层字段，代价远大于收益；
-- 而删掉一条空索引零风险、可随时重建（.down.sql 原样重建）。
--
-- ⚠ 代价的诚实口径：这条**部分**索引因谓词永不成立而**恒为 0 条目**
--   （一次性 PG 17.11 实测：10 万行全 NULL 的表上该索引 8192 bytes），
--   所以它的写放大只是「每行求一次值谓词」，**不是**同族全量索引那种量级。
--   删它的收益是清理一个**误导性的对象**（读方普查会把它算成"有索引可用"
--   的假象），而不是显著省资源。必须写明，避免下一个读注释的人以为它曾很贵。
-- ⚠ 只删 upstream_protocol 那条，**保留 client_protocol 那条**：
--   client_protocol 有写入方与读取方，是活的。
--
-- 本文件走非事务通道，故 DROP 不会与上面的建索引原子回滚：中途失败会留下
-- 「索引建了一半」的中间态。全部语句幂等（IF NOT EXISTS / 守卫 DO），
-- 重跑即可收敛，不存在需要人工清理的破坏性中间态。
DROP INDEX IF EXISTS public.idx_session_turns_hot_upstream_protocol;

-- ===========================================================================
-- 实测记录（一次性 PostgreSQL 17.11，非生产）
-- ---------------------------------------------------------------------------
-- 夹具：分区父表 st + 两个月分区 + 120,000 行 search_text（needle 命中
-- ~0.2%），tenant_id 等值谓词取 admin/logs.go 的真实形态。
--
-- 建索引前：
--   Append
--     ->  Seq Scan on st_2026_09  Filter: (search_text ~~* '%needle%' …)
--     ->  Seq Scan on st_2026_10  Filter: (search_text ~~* '%needle%' …)
--
-- 建索引后：
--   Append
--     ->  Bitmap Heap Scan on st_2026_09
--           Recheck Cond: (search_text ~~* '%needle%'::text)
--           ->  Bitmap Index Scan on st_2026_09_search_text_idx
--                 Index Cond: (search_text ~~* '%needle%'::text)
--     ->  Seq Scan on st_2026_10   （该分区无 needle 行，planner 判定顺序扫更便宜）
--
-- 变异（DROP INDEX 后复跑）：
--   Append
--     ->  Seq Scan on st_2026_09  Filter: …  Rows Removed by Filter: 120000
--     ->  Seq Scan on st_2026_10  Filter: …
--
-- ⚠ 夹具纪律：needle 必须**有选择性**。第一版夹具让每行都含
--   'deepseek'，planner 选 Seq Scan 是**正确**的（匹配 100% 的谓词用索引
--   必然更慢），差点被读成「索引不可用」。定这类判据前先量命中率。
--
-- ⚠ 应用通道纪律：本文件含 \gexec（psql 元命令），**不能**用 pgx/lib/pq
--   直接 Exec —— 必须走 psql，与 installer 的 applySQL 同通道。
--   判据侧同样如此（见 migration_877_integration_test.go 的 psqlApply）。
-- ===========================================================================

-- ── 订正 706 的列注释（列注释是 introspect \d sessions 的读者唯一能看到的契约）──
-- 706_session_family_s1a.sql:461 至今写着「会话耗时；关闭时 closed_at-created_at（706）」，
-- 但 706 的**唯一**写入方 CloseSession 全仓零调用方，该语义在生产上从未兑现。
-- 877 起本列改为**每轮刷新的已流逝时长**（EXCLUDED.updated_at - 存量 created_at）。
-- 不改 706 本体（已 applied+verified 且 SHA 登记台账，改它会重写已应用历史），
-- 只在这里用 COMMENT ON COLUMN 把对外契约订正到与代码一致。
DO $comment_877_duration$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'sessions' AND column_name = 'duration_ms'
  ) THEN
    COMMENT ON COLUMN public.sessions.duration_ms IS
      '877 订正：每轮刷新的**已流逝时长** = updated_at - created_at（首写固化的 created_at）。
       706 原注释「关闭时 closed_at - created_at」承诺的语义从未兑现 —— 其唯一写入方
       CloseSession 无任何生产调用方，该列此前恒为 NULL。按新语义，进行中的会话
       已有真实且有用的「已流逝多久」，且随轮次单调逼近真实总时长。';
  END IF;
END
$comment_877_duration$;

-- COMMENT 与创建腿同守卫（守卫掉的对象留无条件 COMMENT 会在语句末尾以
-- relation does not exist 非零退出，迁移永不记账）。
DO $comment_877_index$BEGIN
  IF to_regclass('public.idx_session_turns_search_trgm') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_search_trgm IS
      '877: session_turns 分区父表 search_text GIN trigram 索引 —— S4 停写后 admin/logs.go:540 的 search_text ILIKE ''%…%'' 谓词的承重索引；前导通配符使 btree 无法服务，无它则该谓词对每个 6.4GB 级月分区顺序扫描。走 727/729 三段式 CONCURRENTLY（父表不支持 CONCURRENTLY），构建期不阻断活写。验收须按分区级 pg_index 查（父表 pg_get_indexdef 恒渲染 ON ONLY，不代表未级联）。';
  END IF;
  IF to_regclass('public.idx_session_turns_hot_search_trgm') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_hot_search_trgm IS
      '877: session_turns_hot 的 search_text GIN trigram 索引 —— 同一条 ILIKE ''%…%'' 谓词在 view 第一条 UNION ALL 臂（hot）上的承重索引。父表索引不级联到 hot 表（dual-write 架构），故必须单独建。';
  END IF;
END
$comment_877_index$;
