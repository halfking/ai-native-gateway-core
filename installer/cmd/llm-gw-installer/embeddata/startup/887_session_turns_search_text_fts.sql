-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，877 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/887_session_turns_search_text_fts.sql
-- Migration:     887
-- Database:      llm_gateway
-- Purpose:       给 session_turns 家族补**全文检索**（tsvector）承重索引 ——
--                表达式 GIN 索引 `USING gin (to_tsvector('simple', search_text))`，
--                配合 admin/logs.go 的 `?q=` 谓词形态决策（词形词 → 全文检索
--                路径；其余 → 既有 trigram 路径）。
--
-- Status:        active
-- Idempotent:    YES（三段全部带 IF NOT EXISTS / 幂等 DO 守卫；建前自愈
--                indisvalid=false 的半成品索引，建后形状校验）
-- Dependencies:  877（本族 search_text 的 GIN trigram 索引，三段式先例）；
--                707（search_text 列本身，父表与 hot 表各一份）；
--                00-prereqs（pg_trgm 扩展，本迁移不新建扩展）
--
-- Background（缺口本体）:
--   S4 停写（storage.request_logs_write_enabled=false，生产 2026-10-13 生效）
--   后新请求只落 session 家族，而 admin/logs.go 的搜索过滤是
--       rl.search_text ILIKE '%' || $n || '%'
--   ——**前导通配符**。877 给本族补了 GIN trigram 索引，让这条谓词不再对
--   每个月分区顺序扫描 search_text（session_turns_2026_09 单分区 6.4GB，
--   search_text 是 TOAST 宽列）。
--
--   ★ 但 trigram 只解决「**子串**」语义。全族**没有任何 tsvector**：
--     grep 全 startup/ 与 schema/：session_turns / session_turns_hot 上
--     零个 `tsvector` 列、零个 `to_tsvector` 表达式索引 ⇒
--       ① 没有 `@@` / `plainto_tsquery` —— 多词查询（"deepseek chat"）只能
--          退化成「连续子串」匹配（ILIKE '%deepseek chat%'），召回明显偏窄；
--       ② 没有排名（rank）—— 搜索结果没有相关性排序；
--       ③ 没有词元化 —— 搜 "session" 命中不了 "sessions"/"session_id" 的
--          词元形态之外的东西。
--     ⇒ GIN **trigram** 索引服务不了 `@@` 谓词（它是子串索引，不是倒排索引），
--       这不是「配置没打开」，是**索引类型不匹配**。
--
-- ★ 为什么用**表达式 GIN 索引**，而不是加一列 `tsvector`:
--   硬约束（已由审计轮确认，不得违反）：session_turns **不能**加生成列
--   （GENERATED ALWAYS AS ... STORED）。
--   原因在 promote_session_turns_hot_to_partition（707 建、615/626 同族）：
--   它的搬行体是「**从 hot 表的目录列派生**列清单」，然后
--       INSERT INTO public.session_turns (<cols>) SELECT <cols> ... ;
--   一旦 hot 表的目录里出现一个生成列，INSERT 就报
--       ERROR: cannot insert into a generated column
--   ⇒ promote 当场失败 ⇒ **热表数据永远排不进月分区**（hot 表越滚越大）。
--   ⇒ 只能走 `USING gin (to_tsvector('simple', search_text))`：
--     表达式索引不占目录列、不进 INSERT 列表 ⇒ promote 不受影响。
--
-- ★ 表达式索引的**代价**：表达式索引只在查询表达式**语法同形**时才会被用上。
--   索引里存的是 `to_tsvector('simple', search_text)`；若查询写成
--       search_text @@ plainto_tsquery('simple', $n)
--   或
--       to_tsvector('simple', rl.search_text) @@ to_tsquery('simple', $n)
--   都**匹配不上**索引表达式 ⇒ planner 不会用它 ⇒ 索引白建。
--   所以查询侧必须逐字写成：
--       to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', $n)
--   写侧：admin/logs_search_predicate.go 的 buildSessionSearchPredicate。
--   ★ 另一个同形要求：text search configuration 用 **'simple'**。
--     'simple' 不做词干还原、不去停用词 ⇒ 索引与查询两侧的切分必然一致；
--     换 'english' 之类带词典的配置，索引侧与查询侧只要有一处漏写
--     configuration 名就会语法不同形 ⇒ 索引失效且**不报错**（静默退化成
--     顺序扫描）。'simple' 也是本仓唯一没有外部词典依赖的选择
--     （部署机上不保证装了 snowball 词典）。
--
-- ★ 为什么走 877 的**三段式 CONCURRENTLY**:
--   session_turns 是**活写**分区父表；PG17 在分区父表上不支持
--   CREATE INDEX CONCURRENTLY（252 真库实测 42809，842/846/862/870/874
--   同一实证，877 同款）。而普通 CREATE INDEX 会级联到每个分区并在
--   **整个构建期持 ShareLock 阻断写入**。本索引是 GIN、覆盖 100% 行、
--   search_text 是 TOAST 宽列 ⇒ 6.4GB 分区属**分钟级**构建 ⇒
--   「秒级 + 部署窗口」的先例不成立。
--   ⇒ 三段式（877 同款，已被 sql/migrations/startup/migration_877_test.go
--     逐段固化）：
--       1) 对每个**已存在**的分区单独 CONCURRENTLY 建（\gexec 生成；
--          全新库无分区时自然为空操作）；
--       2) 父表 **ON ONLY** 建分区索引壳（无扫描，仅短暂元数据锁）；
--       3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
--   之后 ensure_sessions_v2_partitions() 以 CREATE TABLE … PARTITION OF 建的
--   未来月分区会自动继承父表分区索引，无需模板动作。
--
-- ★ 为什么**不**给 v1 族（_request_logs / request_logs）也建这条索引:
--   顶层 OR（ILIKE ‖ FTS）会让 planner 在**每个**参与扫描的关系上要求
--   **两条腿各自都有索引路径**才能走 BitmapOr；缺一条就退回顺序扫描。
--   ⇒ 视图 request_logs_with_current_month 的第三条 UNION 臂（v1 族）没有
--     FTS 索引 ⇒ 那条臂会从「bitmap index scan」退化成「seq scan」。
--   实测依据（2026-10-20 复核轮，252 生产库）：**退役族 _request_logs
--   行数 = 0**（v1 停写且历史已清空）⇒ 那条臂扫的是空表，代价为零。
--   ⇒ 刻意**不**给一个「已清空、且已被排进删除计划」的族再盖一条 GIN 索引
--     （盖了也只会被随族一起删掉，白付构建时间与磁盘）。
--   ⚠ **这条推理是有条件的**，条件写在这里以便将来复核：
--     若 v1 族被重新灌入数据、或历史回填把行放回去，那条臂的顺序扫描
--     就变成真成本 ⇒ 届时必须给 v1 族补同形表达式索引，或把 OR 退回
--     纯 ILIKE。判据见 sql/migrations/startup/session_turns_search_fts_test.go
--     的「v1 族必须为空」那条（它现在只登记这条前提，不假装它是永久事实）。
--
-- 超时：共享 PG 服务端 statement_timeout 可能只有 30s，GIN 构建必然击穿，
--   57014 之后迁移永不记账。本文件走**非事务通道**（首行标记），故用
--   **会话级** SET（每个文件是一次独立 psql 进程，会话级设置不外溢）；
--   **不能用 SET LOCAL**——没有事务块时它无意义。
--   ★ 被 applySQL 的 5 分钟 ctx 掐断会留下 **indisvalid=false 的半成品索引**
--   （CONCURRENTLY 的两阶段扫描被中断）：对象在、名字在、但 planner 不用它，
--   而 `IF NOT EXISTS` 对这种对象**不生效**（它只判存在性）⇒
--   「重跑一次」看起来跑了、实际上什么都没修。⇒ 本文件建**之前**先把同名
--   invalid 索引 DROP 掉，让重跑真正重建（877 同款）。
--   lock_timeout 抢不到锁则整体失败、什么都没发生，稍后重跑即可。
--
-- 回滚：.down.sql 逐段反向（删父壳会级联删各分区子索引；hot 侧独立删），
--   不动数据。
-- ===========================================================================

SET statement_timeout = '30min';
SET lock_timeout = '60s';

-- ── 第 0 段：自愈被 applySQL 的 5 分钟 ctx 掐断的残留 ────────────────
-- CONCURRENTLY 是两阶段扫描，被掐断会留下 indisvalid=false 的半成品索引：
-- 对象在、名字在、但 planner 不会用它。IF NOT EXISTS 对这种对象不生效，
-- 于是「重跑一次」看起来跑了、实际上什么都没修 —— 这正是最坏形态。
-- 因此建之前先把同名 invalid 索引 DROP 掉，让重跑真正重建。
DO $pre_887_fts_heal$
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
      AND ci.relname LIKE '%search_fts'
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', r.relname);
    RAISE NOTICE '887: dropped leftover invalid index public.%', r.relname;
  END LOOP;
END
$pre_887_fts_heal$;

-- ── 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ──────────
-- 全新库尚无月分区时本段自然为空操作，不阻塞任何写入。
--
-- ★ 表达式必须与写侧**逐字同形**：`to_tsvector('simple', search_text)`。
--   写查询时若写成 `to_tsvector('simple'::regconfig, search_text)` 之外的
--   任何形态（例如少了 configuration、或用了 to_tsvector(text) 单参版），
--   索引就匹配不上 ⇒ planner 静默不用它。
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I USING gin (to_tsvector(''simple'', search_text))',
  c.relname || '_search_fts', c.relname)
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
--     （887 头部 :72-73 自己写的就是这条验收要求：不能按索引名 grep）。
AND NOT EXISTS (
  SELECT 1
  FROM pg_inherits ci
  JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
  WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_fts')
    AND mi.indrelid = c.oid
)
ORDER BY c.relname
\gexec

-- ── 第 2 段：热侧 session_turns_hot 独立建 ─────────────────────────────
-- session_turns_hot 是独立 hot 表（dual-write 架构），父表索引**不级联**到它，
-- 必须单独建；漏掉这条腿会让 view 的第一条 UNION ALL 臂（hot）仍然顺序扫描
-- ——877 的门就是专门守这一腿的（877:130-139）。
-- 同为 CONCURRENTLY，所以也放进 \gexec：**本文件里所有 CONCURRENTLY 语句
-- 都必须独立自动提交**，一旦和别的语句混在一次 Exec 里就会落进隐式事务块
-- 而报 25001。
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.session_turns_hot USING gin (to_tsvector(''simple'', search_text))',
  'idx_session_turns_hot_search_fts')
\gexec

-- ── 第 3 段：父表分区索引壳（ON ONLY，无扫描，仅元数据锁）────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_search_fts
  ON ONLY public.session_turns USING gin (to_tsvector('simple', search_text));

-- ── 第 4 段：ATTACH 已建好的子索引（幂等守卫，877 同款）────────────
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
    IF to_regclass(format('public.%I', part || '_search_fts')) IS NOT NULL
       -- ★ R76 订正：同 877 —— 判据改成「该分区在父索引下有没有成员（名字不限）」。
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
         WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_fts')
           AND mi.indrelid = to_regclass(format('public.%I', part))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_search_fts ATTACH PARTITION public.%I',
                     part || '_search_fts');
    END IF;
  END LOOP;
END $$;

-- ── 形状校验：建完必须每个分区子索引都 valid 且已挂在父壳下 ──────────
-- CONCURRENTLY 被 ctx 掐断会留下 indisvalid=false 的半成品：对象在、planner
-- 不用。第 0 段只在**下次**运行自愈；这一次必须当场炸出来，否则它会静默地
-- 留在库里，直到某天有人发现"索引明明在、EXPLAIN 却还在 Seq Scan"。
DO $verify_887_fts_valid$
DECLARE
  bad text;
  unattached text;
BEGIN
  SELECT string_agg(ci.relname, ', ' ORDER BY ci.relname) INTO bad
  FROM pg_index i
  JOIN pg_class ci ON ci.oid = i.indexrelid
  JOIN pg_class ct ON ct.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = ct.relnamespace
  WHERE n.nspname = 'public'
    AND ct.relname LIKE 'session_turns%'
    AND ci.relname LIKE '%search_fts'
    AND NOT i.indisvalid;
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '887: 全文检索索引构建未完成（indisvalid=false，通常是 applySQL 的 5 分钟 ctx 掐断了 CREATE INDEX CONCURRENTLY 的第二阶段扫描）: % 。下一次重跑本迁移会自动 DROP 并重建；若仍超时，请在会话外手工执行本文件。', bad;
  END IF;

  -- 每个已建好的分区子索引都必须挂在父壳下（否则 planner 不会用父索引
  -- 去服务分区上的查询 —— 父壳只是「壳」，真正承重的是挂在下面的子索引）。
  SELECT string_agg(c.relname, ', ' ORDER BY c.relname) INTO unattached
  FROM pg_class c
  WHERE c.oid IN (
    SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
  )
    AND to_regclass(format('public.%I', c.relname || '_search_fts')) IS NOT NULL
    -- ★ R76 订正：与 ATTACH 段同一个判据（该分区在父索引下有无成员，名字不限）
    --   ⇒ 只有「本迁移建的子索引存在、而该分区在父索引下一个成员都没有」
    --   才算缺陷。旧判据会把「PG 自动命名的成员已承重」误判成未挂载。
    AND NOT EXISTS (
      SELECT 1 FROM pg_inherits ci
      JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
      WHERE ci.inhparent = to_regclass('public.idx_session_turns_search_fts')
        AND mi.indrelid = c.oid
    );
  IF unattached IS NOT NULL THEN
    RAISE EXCEPTION '887: 分区全文检索索引已建但未挂到父索引下: % 。ATTACH 段没跑成；请重跑本迁移。', unattached;
  END IF;
END
$verify_887_fts_valid$;

-- ── 索引注释（契约的唯一可读者：\d+ / pg_get_indexdef）────────────────
-- COMMENT 与创建腿同守卫（守卫掉的对象留无条件 COMMENT 会在语句末尾以
-- relation does not exist 非零退出，迁移永不记账）。
DO $comment_887_fts_index$BEGIN
  IF to_regclass('public.idx_session_turns_search_fts') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_search_fts IS
      '887: session_turns 分区父表的**全文检索**表达式 GIN 索引 —— `USING gin (to_tsvector(''simple'', search_text))`。承重谓词：admin/logs.go 的 `?q=` 在「词形词」输入时发出的 `to_tsvector(''simple'', rl.search_text) @@ plainto_tsquery(''simple'', $n)`（与本表达式逐字同形 ⇒ 才会被 planner 用上；写成裸 `search_text @@ ...` 或换 configuration 都会静默失配）。**不是**列存、**不是**生成列：加 tsvector 生成列会让 promote_session_turns_hot_to_partition 的 `INSERT INTO public.session_turns (<目录派生列>)` 报 cannot insert into a generated column ⇒ 热表永远排不进月分区（877 同族三段式 CONCURRENTLY，父表不支持 CONCURRENTLY）。验收须按分区级 pg_index 查（父表 pg_get_indexdef 恒渲染 ON ONLY，不代表未级联）。';
  END IF;
  IF to_regclass('public.idx_session_turns_hot_search_fts') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_hot_search_fts IS
      '887: session_turns_hot 的全文检索表达式 GIN 索引 —— 同一条 `@@` 谓词在 view 第一条 UNION ALL 臂（hot）上的承重索引。父表索引不级联到 hot 表（dual-write 架构），故必须单独建。';
  END IF;
END
$comment_887_fts_index$;
