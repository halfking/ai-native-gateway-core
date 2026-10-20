-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY / DROP INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，877 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/888_session_family_join_index_gaps.sql
-- Migration:     888
-- Database:      llm_gateway
-- Purpose:       补会话族三处**索引缺口**：
--                  ① session_turns ⨝ session_bodies 的 join 键没有索引承重
--                     （父表与 hot 表的索引都**不含 request_id** ⇒ 每次 join
--                       都要回堆做一次 recheck）；
--                  ② hot 表那条索引的**列序与父表不一致**（父表 tenant_id
--                     打头、hot 表 session_id 打头）⇒ 同一个 join 在两条臂上
--                     走不同的索引路径；
--                  ③ sessions 表缺 (tenant_id, session_id, partition_date DESC)
--                     ⇒ 「按 (tenant_id, session_id) 找会话」在每个月上分区上
--                     只能靠 (session_id, partition_date) 那条 UNIQUE 顶着，
--                     tenant_id 退化成回堆过滤。
--
-- Status:        active
-- Idempotent:    YES（建索引全部 IF NOT EXISTS；删索引全部 IF EXISTS；
--                ATTACH / 校验块均带幂等守卫）
-- Dependencies:  430（session_bodies / sessions 建表）；476（父表那条
--                (tenant_id, session_id, turn_no DESC) 索引，本迁移替换它）；
--                614（hot 表那条 (session_id, turn_no, tenant_id) 索引，
--                本迁移替换它）；877（会话族三段式 CONCURRENTLY 先例）
--
-- -----------------------------------------------------------------------
-- 缺口本体（读方普查 + 索引普查，不是推理）
-- -----------------------------------------------------------------------
-- ① **join 键缺 request_id ⇒ 恒有回堆 recheck**
--    轮次列表 / 详情两处 join（admin/session_turns_v2.go:132-136 与 :304-310）：
--        FROM public.session_turns_with_current_month t
--        LEFT JOIN public.session_bodies_unified b
--          ON b.tenant_id = t.tenant_id AND b.session_id = t.session_id
--         AND b.turn_no = t.turn_no AND b.request_id = t.request_id
--    四个等值条件里，**request_id 是唯一没有索引承重的那一个**：
--      · 父表 session_bodies 的索引：
--          idx_session_bodies_session           (session_id, turn_no DESC)   ← 430
--          idx_session_bodies_tenant_session_turn
--              (tenant_id, session_id, turn_no DESC)                        ← 476
--        两者都**不含 request_id** ⇒ 前三列定位到行之后，第四个条件只能
--        **回堆**再比一次 request_id。
--      · hot 表 session_bodies_hot 的索引：
--          idx_session_bodies_hot_lookup (session_id, turn_no, tenant_id)   ← 614
--        同样不含 request_id，而且**列序与父表不同**（tenant_id 在第三位）。
--
--    ⇒ 把 request_id 放进索引，四列就都能在索引里比完 ⇒ join 的探测侧
--      不再回堆（能否做到 Index Only Scan 还取决于可见性图，但**回堆
--      recheck 这一步**是确定省掉的）。
--
-- ② **两条臂的列序不一致**
--    父表是 (tenant_id, session_id, turn_no DESC)、hot 表是
--    (session_id, turn_no, tenant_id) ⇒ 同一个 join 在 hot 臂上要先用
--    session_id 定位、再回堆过滤 tenant_id。⇒ 本迁移让 hot 表与父表
--    **逐列同序**（tenant_id, session_id, turn_no DESC, request_id）。
--
-- ③ **sessions 缺 (tenant_id, session_id, partition_date DESC)**
--    读方（都带 tenant_id + session_id 两个等值条件）：
--      · domains/session/v2/session_aggregator.go:365 与 :527
--            FROM public.sessions WHERE tenant_id = $1 AND session_id = $2
--      · internal/titlestore/store.go:271（写侧投影最新分区行）
--            ... AND partition_date = (SELECT MAX(partition_date) FROM public.sessions
--                                       WHERE tenant_id = $1 AND session_id = $2)
--      · admin/turns_sessions.go:197（读侧「一会话一行」去重谓词）
--            s.partition_date = (SELECT MAX(x.partition_date) FROM public.sessions x
--                                WHERE x.tenant_id = s.tenant_id AND x.session_id = s.session_id)
--    现存可用索引里，session_id 打头的是那条 UNIQUE
--    sessions_session_id_partition_date_key (session_id, partition_date) ⇒
--    tenant_id 只能当**回堆过滤**。
--
-- ⚠ **关于「这条索引能不能让最新分区查找不再逐月探测」的诚实口径**：
--    **不能。** PostgreSQL 17 **没有** btree skip-scan（PG18 才有），
--    没有 partition_date 常量的谓词就**一定会**逐个月分区探测 —— 这是
--    planner 的能力边界，不是索引形态问题。
--    本索引的真实收益是**让每次探测更便宜**：探测从「session_id 等值 +
--    回堆过滤 tenant_id」变成「tenant_id + session_id 双等值的紧索引定位」，
--    并且给 MAX(partition_date) 子查询一个**按 partition_date 排序**的
--    索引扫描（每个分区内部取第一行，而不是聚合全部匹配行）。
--    ⇒ 想真正停止逐月探测，缺的是**分区键常量**：把
--      partition_date = (SELECT MAX(...))
--    改成两段式（先取 max，再拿它当裁剪常量）—— 那是**代码**改动，
--    不在本次索引迁移的范围内，此处只登记为遗留项。
--
-- -----------------------------------------------------------------------
-- ★ 为什么是「建新的 + 删旧的」，而不是就地改
-- -----------------------------------------------------------------------
--   **PostgreSQL 不支持 ALTER INDEX … ADD COLUMN** —— 索引的列集合在建索引
--   时就固定了（PG 11 之后 ADD COLUMN 支持 INCLUDE，但**不支持把已有列
--   塞进键列**）。⇒ 想加 request_id 只能新建一条索引再删旧的。
--   两条索引的**先后顺序**是刻意的：**先建后删** ⇒ 任一时刻都至少有一条
--   索引在承重（不会出现「两条都没有」的窗口）。
--
--   被替换的两条：
--     · 476 的 idx_session_bodies_tenant_session_turn
--         (tenant_id, session_id, turn_no DESC)
--       → 新索引 (tenant_id, session_id, turn_no DESC, request_id) 是它的
--         **严格超集**（同前缀 + 追加 request_id）⇒ 删它零代价。
--       ⚠ 若该库从未应用 476（本机开发库就是这种形态：它的 schema 来自
--         正典基线快照，基线里没有这条索引）⇒ IF EXISTS 让删语句空转。
--     · 614 的 idx_session_bodies_hot_lookup
--         (session_id, turn_no, tenant_id)
--       → 新索引 (tenant_id, session_id, turn_no DESC, request_id)
--       ⚠ 列序不同，**不是**前缀关系 ⇒ 删它之前必须先确认没有「只按
--         session_id 过滤 hot 表」的读方。已普查：全仓 Go 代码里读
--         session_bodies_hot 的地方**没有**任何 session_id 单列过滤
--         （读面一律走 session_bodies_unified 的四列 join；写面是 INSERT）；
--         且基线自带的 session_bodies_hot_session_id_turn_no_idx
--         (session_id, turn_no DESC) 仍在 ⇒ 单列 session_id 的读面仍有索引。
--
-- ★ 为什么走三段式 CONCURRENTLY（877 同款）
--   session_bodies 与 sessions 都是**活写分区父表**；PG17 在分区父表上
--   不支持 CREATE INDEX CONCURRENTLY（252 真库实测 42809，842/846/862/
--   870/874/877 同一实证）。而普通 CREATE INDEX 会级联到每个分区并在
--   **整个构建期持 ShareLock 阻断写入** ⇒ 本族是会话热路径，不能接受。
--   ⇒ 三段式：逐分区 CONCURRENTLY → 父表 ON ONLY 壳 → ATTACH。
--   session_bodies_hot 不是分区表 ⇒ 单条 CONCURRENTLY 即可。
--
-- ★ 为什么先建后删**不会**踩 876 的「前缀覆盖」坑
--   876 删的是「**被宽索引逐列覆盖**的窄索引」（建了就是纯浪费）。本迁移
--   删的两条都**不是**被新索引覆盖的窄索引：
--     · 476 那条：新索引是它的严格超集（键列前缀相同 + 追加一列）⇒
--       它的**每一个查询用途**都能被新索引以**同样的代价**服务 ⇒ 删它
--       确实零代价（这是「同前缀 + 更宽」，不是 876 那种「窄 ⊂ 宽」）。
--     · 614 那条：列序不同，新索引**服务不了**「只按 session_id 过滤」的
--       读法（tenant_id 变成了首列）⇒ 它的删除依赖上面那条普查结论，
--       而不是「被覆盖」。
--
-- 超时：会话族分区体量可达 GB 级（252 真库 bodies 月分区 6.4GB 量级），
--   btree 构建是**秒到十秒级**（877 的注释：「874 自己的注释就写着『秒到
--   十秒级，随部署窗口应用』」）；但 CONCURRENTLY 仍可能被 applySQL 的
--   5 分钟 ctx 掐断 ⇒ 建之前先自愈 indisvalid=false 的半成品索引，
--   建完当场校验（877 同款）。
--
-- 回滚：.down.sql 逐条反向 —— 删三条新索引、按原样重建两条旧索引
--   （476 的父表索引 / 614 的 hot 表索引），不动数据。
-- ===========================================================================

SET statement_timeout = '30min';
SET lock_timeout = '60s';

-- ── 第 0 段：自愈被 applySQL 的 5 分钟 ctx 掐断的残留 ────────────────
-- CONCURRENTLY 被掐断会留下 indisvalid=false 的半成品索引：对象在、名字在、
-- 但 planner 不用它，而 IF NOT EXISTS 对这种对象不生效 ⇒ 重跑必须先 DROP。
DO $pre_888_heal$
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
      AND ct.relname IN ('session_bodies', 'session_bodies_hot', 'sessions')
      AND NOT i.indisvalid
      AND (ci.relname LIKE '%tenant_session_turn_request%'
           OR ci.relname LIKE '%sessions_tenant_session_partition_date%')
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', r.relname);
    RAISE NOTICE '888: dropped leftover invalid index public.%', r.relname;
  END LOOP;
END
$pre_888_heal$;

-- ── 第 1 段：session_bodies 父表（分区）三段式 ───────────────────────
-- 1a. 逐分区 CONCURRENTLY
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I USING btree (tenant_id, session_id, turn_no DESC, request_id)',
  c.relname || '_tenant_session_turn_request_idx', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_bodies'::regclass
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
  WHERE ci.inhparent = to_regclass('public.idx_session_bodies_tenant_session_turn_request')
    AND mi.indrelid = c.oid
)
ORDER BY c.relname
\gexec

-- 1b. 父表 ON ONLY 壳（无扫描，仅元数据锁）
CREATE INDEX IF NOT EXISTS idx_session_bodies_tenant_session_turn_request
  ON ONLY public.session_bodies USING btree (tenant_id, session_id, turn_no DESC, request_id);

-- 1c. ATTACH 已建好的子索引（幂等守卫，877 同款）
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.session_bodies'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_tenant_session_turn_request_idx')) IS NOT NULL
       -- ★ R76 订正：同 877 —— 判据改成「该分区在父索引下有没有成员（名字不限）」。
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
         WHERE ci.inhparent = to_regclass('public.idx_session_bodies_tenant_session_turn_request')
           AND mi.indrelid = to_regclass(format('public.%I', part))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_bodies_tenant_session_turn_request ATTACH PARTITION public.%I',
                     part || '_tenant_session_turn_request_idx');
    END IF;
  END LOOP;
END $$;

-- ── 第 2 段：session_bodies_hot（普通表）一条 CONCURRENTLY ───────────
-- hot 表不是分区表 ⇒ PG 支持 CREATE INDEX CONCURRENTLY。
-- 列序与父表**逐列相同**（这是本迁移要修的缺口②）。
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.session_bodies_hot USING btree (tenant_id, session_id, turn_no DESC, request_id)',
  'idx_session_bodies_hot_tenant_session_turn_request')
\gexec

-- ── 第 3 段：sessions 父表（分区）三段式 ────────────────────────────
-- 3a. 逐分区 CONCURRENTLY
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I USING btree (tenant_id, session_id, partition_date DESC)',
  c.relname || '_tenant_session_partition_date_idx', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.sessions'::regclass
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
  WHERE ci.inhparent = to_regclass('public.idx_sessions_tenant_session_partition_date')
    AND mi.indrelid = c.oid
)
ORDER BY c.relname
\gexec

-- 3b. 父表 ON ONLY 壳
CREATE INDEX IF NOT EXISTS idx_sessions_tenant_session_partition_date
  ON ONLY public.sessions USING btree (tenant_id, session_id, partition_date DESC);

-- 3c. ATTACH（幂等守卫）
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.sessions'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_tenant_session_partition_date_idx')) IS NOT NULL
       -- ★ R76 订正：同 877 —— 判据改成「该分区在父索引下有没有成员（名字不限）」。
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
         WHERE ci.inhparent = to_regclass('public.idx_sessions_tenant_session_partition_date')
           AND mi.indrelid = to_regclass(format('public.%I', part))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_sessions_tenant_session_partition_date ATTACH PARTITION public.%I',
                     part || '_tenant_session_partition_date_idx');
    END IF;
  END LOOP;
END $$;

-- ── 第 4 段：删掉被替换的两条旧索引（**先建后删** ⇒ 无空窗）────────
-- ④1. 476 的父表索引：新索引是它的严格超集 ⇒ 零代价。
DROP INDEX IF EXISTS public.idx_session_bodies_tenant_session_turn;

-- ④2. 614 的 hot 表索引：列序不同 ⇒ 依赖「无 session_id 单列读方」的
--     普查结论（见文件头）。hot 表是**活写**表 ⇒ 用 CONCURRENTLY 删，
--     避免 ACCESS EXCLUSIVE 窗口。
DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_bodies_hot_lookup;

-- ── 形状校验：三条新索引必须 valid，且分区子索引必须挂在父壳下 ──────
DO $verify_888_valid$
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
    AND (ci.relname LIKE '%tenant_session_turn_request%'
         OR ci.relname LIKE '%sessions_tenant_session_partition_date%')
    AND NOT i.indisvalid;
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '888: 索引构建未完成（indisvalid=false，通常是 applySQL 的 5 分钟 ctx 掐断了 CREATE INDEX CONCURRENTLY 的第二阶段扫描）: % 。下一次重跑本迁移会自动 DROP 并重建。', bad;
  END IF;

  SELECT string_agg(c.relname, ', ' ORDER BY c.relname) INTO unattached
  FROM pg_class c
  WHERE c.oid IN (
    SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_bodies'::regclass
  )
    AND to_regclass(format('public.%I', c.relname || '_tenant_session_turn_request_idx')) IS NOT NULL
    -- ★ R76 订正：与 ATTACH 段同一个判据（该分区在父索引下有无成员，名字不限）。
    AND NOT EXISTS (
      SELECT 1 FROM pg_inherits ci
      JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
      WHERE ci.inhparent = to_regclass('public.idx_session_bodies_tenant_session_turn_request')
        AND mi.indrelid = c.oid
    );
  IF unattached IS NOT NULL THEN
    RAISE EXCEPTION '888: session_bodies 的分区子索引已建但未挂到父索引下: % 。ATTACH 段没跑成；请重跑本迁移。', unattached;
  END IF;

  SELECT string_agg(c.relname, ', ' ORDER BY c.relname) INTO unattached
  FROM pg_class c
  WHERE c.oid IN (
    SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.sessions'::regclass
  )
    AND to_regclass(format('public.%I', c.relname || '_tenant_session_partition_date_idx')) IS NOT NULL
    -- ★ R76 订正：与 ATTACH 段同一个判据（该分区在父索引下有无成员，名字不限）。
    AND NOT EXISTS (
      SELECT 1 FROM pg_inherits ci
      JOIN pg_index mi ON mi.indexrelid = ci.inhrelid
      WHERE ci.inhparent = to_regclass('public.idx_sessions_tenant_session_partition_date')
        AND mi.indrelid = c.oid
    );
  IF unattached IS NOT NULL THEN
    RAISE EXCEPTION '888: sessions 的分区子索引已建但未挂到父索引下: % 。ATTACH 段没跑成；请重跑本迁移。', unattached;
  END IF;
END
$verify_888_valid$;

-- ── 索引注释（契约的唯一可读者：\d+ / pg_get_indexdef）────────────────
DO $comment_888_index$BEGIN
  IF to_regclass('public.idx_session_bodies_tenant_session_turn_request') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_bodies_tenant_session_turn_request IS
      '888: session_bodies 分区父表的 join 承重索引 —— btree (tenant_id, session_id, turn_no DESC, request_id)。承重谓词：admin/session_turns_v2.go 的轮次列表/详情 join（b.tenant_id=t.tenant_id AND b.session_id=t.session_id AND b.turn_no=t.turn_no AND b.request_id=t.request_id）—— 四列全在索引里 ⇒ request_id 不必回堆 recheck。**替换** 476 的 (tenant_id, session_id, turn_no DESC)（本索引是它的严格超集，删它零代价）。PostgreSQL 不支持 ALTER INDEX ADD COLUMN ⇒ 只能建新 + 删旧。';
  END IF;
  IF to_regclass('public.idx_session_bodies_hot_tenant_session_turn_request') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_bodies_hot_tenant_session_turn_request IS
      '888: session_bodies_hot 的 join 承重索引 —— btree (tenant_id, session_id, turn_no DESC, request_id)，**与父表逐列同序**（888 修的缺口②：原来 hot 臂是 (session_id, turn_no, tenant_id)，与父表列序不同 ⇒ 同一个 join 在两条臂上走不同路径）。**替换** 614 的 (session_id, turn_no, tenant_id)。';
  END IF;
  IF to_regclass('public.idx_sessions_tenant_session_partition_date') IS NOT NULL THEN
    COMMENT ON INDEX idx_sessions_tenant_session_partition_date IS
      '888: sessions 分区父表的按会话定位索引 —— btree (tenant_id, session_id, partition_date DESC)。承重谓词：domains/session/v2/session_aggregator.go:365/:527（WHERE tenant_id=$1 AND session_id=$2）、internal/titlestore/store.go:271 与 admin/turns_sessions.go:197（最新分区行的 MAX(partition_date) 定位）。⚠ 诚实口径：**它不会让查询停止逐月探测分区**（PG17 没有 btree skip-scan，没有 partition_date 常量就一定逐月探测）；它让每次探测从「session_id 等值 + 回堆过滤 tenant_id」变成「tenant_id + session_id 双等值的紧索引定位」，并给 MAX(partition_date) 子查询一个按 partition_date 排序的索引扫描。';
  END IF;
END
$comment_888_index$;
