-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），
-- installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go
-- noTransactionMarker，877/822/908 同款）。无该标记时全新安装会失败于
--   "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/909_session_summaries_search_trgm_indexes.sql
-- Migration:     909
-- Database:      llm_gateway
-- Purpose:       给 session_summaries 的三个 LIKE/ILIKE 查询列补 GIN trigram 索引，
--                让 admin 的会话搜索（catalogSearchSQL）不再对全表顺序扫描
--
-- Status:        active
-- Idempotent:    YES（第 0 段自愈 indisvalid=false 的半成品 + CREATE INDEX CONCURRENTLY
--                IF NOT EXISTS + 第 4 段形状校验）
-- Dependencies:  00-prereqs（pg_trgm 扩展，本迁移不新建扩展）；
--                655（session_summaries 建表与 title/primary_model 列）
--
-- Background（缺陷本体，252 生产只读实测，2026-10-27 R154 轮）：
--   admin/session_catalog_usage.go 的 catalogSearchSQL 用 likeContains(q)
--   （★ 生成 `%q%`，★ **中缀匹配**）对 session_summaries 的 **三列**做 OR 的 ILIKE：
--       WHERE session_key IS NOT NULL
--         AND btrim(session_key) <> ''
--         AND (session_key ILIKE $1 ESCAPE '\'
--           OR COALESCE(title, '') ILIKE $1 ESCAPE '\'
--           OR COALESCE(primary_model, '') ILIKE $1 ESCAPE '\')
--   ★ 252 实测（868,711 行）：
--     · EXPLAIN（不带 ANALYZE）⇒ **Parallel Seq Scan on session_summaries**
--     · EXPLAIN ANALYZE ⇒ **Execution Time = 40,415.968 ms（★ 40.4 秒）**
--       （Planning Time 仅 16.679 ms ⇒ ★ **不是规划问题，是执行问题**）
--     · Rows Removed by Filter = 289,543 × 3 workers
--     · Buffers: shared hit=34,435 read=17,177
--   ⇒ ★★ 而 mergeCatalogSearch 的 ctx 超时是 **2 秒**
--     （admin/session_catalog_usage.go：`context.WithTimeout(ctx, 2*time.Second)`）
--     ⇒ ⇒ ★★★ **它每次都超时** ⇒ ⇒ **静默降级成「没搜到」**
--     （slog.Warn("catalog search failed") ⇒ ★ 用户看不到「超时」，★ 只看到「空结果」）
--
-- 为什么必须是**三列**各一个索引（★ 不能只给主列建）：
--   · `session_summaries` 上 session_key 只有**等值**索引（pkey / unique(tenant_id, session_key)）
--     ⇒ ★ `%q%` 中缀匹配用不上 ⇒ ★ **前导通配符是 btree 的死区**；
--   · `title` / `primary_model` **一个索引都没有**（★ 252 生产 pg_indexes 实测 = 0）；
--   · ★★ 三列 OR ⇒ ★★ 规划器必须能把**每一列**变成可索引条件 ⇒ ★★ 放弃全部索引 ⇒ Seq Scan。
--   ⇒ ⇒ **只给 session_key 建 trigram 索引不解决问题**（OR 的另外两支仍不可索引）
--   ⇒ ⇒ 三列各一个 GIN trigram ⇒ 规划器可以走 **BitmapOr**。
--   ★ 决定性对照（252 生产，同一张表、同一条件）：
--       只有 session_key 一列 ILIKE ⇒ Parallel Index Only Scan using session_summaries_pkey
--       三列 OR                  ⇒ **Parallel Seq Scan**
--
-- ★★ 为什么本迁移的索引表达式必须是**裸列**，而 Go 侧必须**同步去掉 COALESCE**：
--   ★ PG 的表达式索引匹配是**逐字**的：`CREATE INDEX … USING gin (title gin_trgm_ops)`
--     **不能**服务 `COALESCE(title, '') ILIKE $1`（表达式不等 ⇒ 索引不参与 ⇒ 退化成 Seq Scan）；
--     反过来 `CREATE INDEX … USING gin ((COALESCE(title,'')) gin_trgm_ops)` 能服务它，
--     ★ **但那是一个「包着 COALESCE 的索引」** ⇒ ★ **任何人把查询顺手简化成 `title ILIKE`
--     就会静默失去索引** ⇒ ★★ 症状是「SQL 照跑、结果照对、只是慢 40 倍」。
--   ⇒ ⇒ **正解 = 索引裸列 + Go 侧裸列**，★ 两者逐字对齐。
--   ⇒ ★★ **口径等价（可证，不靠实测）**：COALESCE(x,'') 与 x 在 WHERE 谓词里**逐行等价**，
--     前提是**模式串永不为空**：
--       · likeContains(q) 返回 "%" + escape(q) + "%"，而 q 非空 ⇒ 模式串**至少含 1 个非空字符**
--         ⇒ `'' ILIKE 模式串` **恒为假**（`%X%` 至少要 1 个字符）；
--       · `x IS NULL` ⇒ `x ILIKE 模式串` = NULL ⇒ WHERE 判为不通过；
--         `COALESCE(x,'') ILIKE 模式串` = `'' ILIKE 模式串` = 假 ⇒ **同样不通过**；
--       · `x IS NOT NULL` ⇒ 两边逐字相同。
--     ⇒ **两边的「通过/不通过」集合完全一致** ⇒ ★ 行集不变 ⇒ ★ 响应形状不变。
--   ⇒ ★ q 非空由**调用方**保证：mergeCatalogSearch 开头 `strings.TrimSpace(q) == ""` 即返回。
--
-- ★★ 写放大口径（★ 为什么三条索引在**热写表**上仍可接受）：
--   · session_summaries 的每次请求都会触发 trg_update_session_summary 做一次 UPDATE
--     （累加 request_count / token / cost、刷新 last_request_at）；
--   · ★ PG 对 UPDATE 逐索引检查「被索引列的值是否真的变了」⇒ **没变就不重写索引项**；
--   · ★ 本次 UPDATE **不触碰** session_key（主键）、title（由摘要器单独写）、
--     primary_model（由摘要器单独写）⇒ ⇒ ★★ **三条索引的写放大≈ 0**；
--   · ★ 对照：若给 `last_request_at` 这类**每次都变**的列建 GIN，写放大才是真的。
--   ⇒ ⇒ **只给「低频写」的列建 trigram 索引** ⇒ ★ 收益极大、代价近零。
--
-- ★ 为什么走 CONCURRENTLY（★ 822 同款先例）：
--   session_summaries 是**热写表** ⇒ 普通 CREATE INDEX 会在整个构建期持 ShareLock 阻断写入
--   ⇒ ⇒ 用 CONCURRENTLY（两阶段扫描，不阻断在途读写）。
--   ★ session_summaries 是**普通表**（不是分区父表）⇒ ★ **不需要** 877 那种
--     「父表 ON ONLY 壳 + 逐分区 ATTACH」的三段式（★ PG17 在分区父表上不支持
--     CREATE INDEX CONCURRENTLY，★ 252 真库实测 42809；★ 本表无此约束）。
--
-- ★ 超时与锁（★ 877/822/908 同款）：
--   · 共享 PG 服务端 statement_timeout=30s ⇒ GIN trigram 构建必然击穿 ⇒ 57014 之后
--     迁移永不记账、成为永久卡点 ⇒ ★ 本文件走非事务通道 ⇒ 用**会话级** SET
--     （每个文件是一次独立 psql 进程，会话级设置不外溢）⇒ ★ **不能用 SET LOCAL**
--     （没有事务块时它无意义）。
--   · lock_timeout=60s ⇒ 抢不到锁则整体失败、什么都没发生，稍后重跑即可。
--
-- 回滚：.down.sql 逐条 DROP（★ 同为 CONCURRENTLY，同样走非事务通道）。
-- ===========================================================================

SET statement_timeout = '10min';
SET lock_timeout = '60s';

-- ── 第 0 段：自愈被 applySQL 的 5 分钟 ctx 掐断的残留 ─────────────────
-- ★ applySQL 的 ctx 是 5 分钟（exec.CommandContext，runner.go）。CONCURRENTLY
--   是两阶段扫描，被掐断会留下 **indisvalid=false 的半成品索引**：对象在、名字在、
--   但 planner 不会用它。IF NOT EXISTS 对这种对象**不生效**（它只判存在性），
--   于是「重跑一次」看起来跑了、实际上什么都没修 —— ★★ 这正是最坏形态。
-- ⇒ 因此建之前先把同名 invalid 索引 DROP 掉，让重跑真正重建。
-- ★ 877 同款做法（对 session_turns 的 search_trgm）。★ 这里是**普通表**，
--   所以 DROP 落在自己的一次事务里即可（不需要 CONCURRENTLY）。
DO $pre_909_heal$
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
      AND ct.relname = 'session_summaries'
      AND NOT i.indisvalid
      AND ci.relname IN (
        'idx_session_summaries_session_key_trgm',
        'idx_session_summaries_title_trgm',
        'idx_session_summaries_primary_model_trgm'
      )
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', r.relname);
    RAISE NOTICE '909: dropped leftover invalid index public.%', r.relname;
  END LOOP;
END
$pre_909_heal$;

-- ── 第 1 段：三条 GIN trigram 索引（★ 顺序固定：主键 → title → model）────
-- ★ 每条都必须是 CONCURRENTLY ⇒ ★ 本文件里所有 CONCURRENTLY 语句都必须**独立自动提交**
--   （一旦和别的语句混在一次 Exec 里就会落进隐式事务块而报 25001）。
-- ★ IF NOT EXISTS ⇒ 幂等（重跑跳过，不报错）。

-- ★ 第一条：session_key。★ 它是主键、★ **从不随请求变化** ⇒ 写放大≈0。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_session_key_trgm
    ON public.session_summaries USING gin (session_key gin_trgm_ops);

-- ★ 第二条：title。★ 零索引 ⇒ 补上。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_title_trgm
    ON public.session_summaries USING gin (title gin_trgm_ops);

-- ★ 第三条：primary_model。★ 零索引 ⇒ 补上。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_primary_model_trgm
    ON public.session_summaries USING gin (primary_model gin_trgm_ops);

-- ── 第 2 段：形状校验（★ 建完必须三条都 valid）──────────────────────────
-- CONCURRENTLY 被 ctx 掐断会留下 indisvalid=false 的半成品：对象在、planner 不用。
-- 第 0 段只在**下次**运行自愈；这一次必须当场炸出来，否则它会静默地留在库里，
-- 直到某天有人发现「索引明明在、EXPLAIN 却还在 Seq Scan」。
-- ⇒ 这里显式报错，让 applySQL 当次失败、迁移不记账。
DO $verify_909_valid$
DECLARE
  bad text;
BEGIN
  SELECT string_agg(ci.relname, ', ' ORDER BY ci.relname) INTO bad
  FROM pg_index i
  JOIN pg_class ci ON ci.oid = i.indexrelid
  JOIN pg_class ct ON ct.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = ct.relnamespace
  WHERE n.nspname = 'public'
    AND ct.relname = 'session_summaries'
    AND ci.relname IN (
      'idx_session_summaries_session_key_trgm',
      'idx_session_summaries_title_trgm',
      'idx_session_summaries_primary_model_trgm'
    )
    AND NOT i.indisvalid;
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '909: trigram 索引构建未完成（indisvalid=false，通常是 applySQL 的 5 分钟 ctx 掐断了 CREATE INDEX CONCURRENTLY 的第二阶段扫描）: % 。下一次重跑本迁移会自动 DROP 并重建；若仍超时，请在会话外手工执行本文件。', bad;
  END IF;
END
$verify_909_valid$;

-- ── 第 3 段：对外契约（列注释是读者唯一能看到的契约）──────────────────────
-- ★ COMMENT 与创建腿同守卫（守卫掉的对象留无条件 COMMENT 会在语句末尾以
--   relation does not exist 非零退出，迁移永不记账）。
DO $comment_909_index$
BEGIN
  IF to_regclass('public.idx_session_summaries_session_key_trgm') IS NOT NULL THEN
    COMMENT ON INDEX public.idx_session_summaries_session_key_trgm IS
      '909: session_summaries.session_key 的 GIN trigram 索引 —— admin 会话搜索（catalogSearchSQL）的三列 OR ILIKE 之一支；前导通配符 %q% 是 btree 的死区，无它则该支不可索引、整条 OR 退化为全表 Seq Scan（252 生产实测 40.4 秒）。session_key 从不随请求变化 ⇒ 写放大≈0。';
  END IF;
  IF to_regclass('public.idx_session_summaries_title_trgm') IS NOT NULL THEN
    COMMENT ON INDEX public.idx_session_summaries_title_trgm IS
      '909: session_summaries.title 的 GIN trigram 索引 —— 同一条三列 OR ILIKE 的第二支；★ 本列此前**零索引**（252 生产 pg_indexes 实测 = 0）⇒ 补上它才可能走 BitmapOr。title 由摘要器单独写、请求路径不触碰 ⇒ 写放大≈0。';
  END IF;
  IF to_regclass('public.idx_session_summaries_primary_model_trgm') IS NOT NULL THEN
    COMMENT ON INDEX public.idx_session_summaries_primary_model_trgm IS
      '909: session_summaries.primary_model 的 GIN trigram 索引 —— 同一条三列 OR ILIKE 的第三支；★ 本列此前**零索引**（252 生产 pg_indexes 实测 = 0）⇒ 三列齐备，规划器才可能走 BitmapOr 而不是 Seq Scan。';
  END IF;
END
$comment_909_index$;

RESET statement_timeout;
RESET lock_timeout;
