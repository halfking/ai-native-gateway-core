-- 修复缺失的 request_wal_hot 表
-- 问题：代码写入 request_wal_hot，但 252 数据库可能缺少该表
-- 解决方案：检查并创建 request_wal_hot 表（幂等操作）
--
-- R89-EC（213 号）更正 —— 逐条坐实：
--
--   ① 🔴 【最严重】原第 4 节「迁移 request_wal_default 数据」会造成**视图重复计数**。
--      原写法：
--          INSERT INTO request_wal_hot SELECT * FROM request_wal_default
--          ON CONFLICT (request_id, created_at) DO NOTHING;
--      —— 只**拷贝**、**不从 _default 删除**。
--      而 `sql/migrations/startup/332_request_wal_default_partition.sql:31` 是
--          CREATE TABLE public.request_wal_default PARTITION OF public.request_wal DEFAULT;
--      ⇒ `_default` **就是** `request_wal` 的 DEFAULT 分区
--      ⇒ `SELECT * FROM request_wal` **本来就包含**这些行。
--      而本视图是 `request_wal_hot UNION ALL request_wal`
--      ⇒ 拷贝之后，这批行**各出现两次**。
--      ⚠️ 更糟：332 是 **startup 迁移** ⇒ `_default` 在任何正常库里都存在
--      ⇒ 213 号实测该分支**必然执行**，不是罕见分支。
--      ⚠️ 再糟一层：`_default` 是给 `promote_request_wal_default_batch(interval,integer)`
--      用的**溢写暂存桶**（该函数把它 `INSERT` 到正式月分区后 `DELETE` 掉）。
--      本脚本把行复制进 hot 却把原件留在 `_default` ⇒ 原件稍后还会被**提升**进月分区
--      ⇒ **同一批行在 hot 和月分区里各存一份 ⇒ 永久重复**。
--      ⇒ 改法：**不迁移**。这些行已经通过 `request_wal`（含 DEFAULT 分区）出现在视图里，
--        再拷一份进 hot 只会制造重复。原第 4 节整节移除并写明原因。
--
--   ② 🔴 本文件原先**没有** `\set ON_ERROR_STOP on` ⇒ 中途任何一条语句失败时，
--      psql 只打印错误后**继续往下跑**，而末尾照样打印「修复完成！」
--      —— 与 212 号 `fix-request-wal-hot-primary-key.sql` 同一形态。
--
--   ③ 🟡 两处 `SELECT *`（视图与迁移）都改成**显式列清单**。权威定义
--      （`01-schema.sql` 里的 `request_wal_with_current_month`）**本来就是逐列枚举的**
--      —— 那正是因为 `UNION ALL` 两侧靠**位置**对齐，`SELECT *` 一旦任一侧加列/改序就会
--      静默错位或直接报错。本脚本是那个脆弱写法。
--      （实测两表现各 17 列且完全一致，所以 `SELECT *` 目前**能跑**；这里是消除脆弱性，
--        不是修一个当前正在发生的错。）
--
--   ④ 🟡 `CREATE TABLE request_wal_hot` 的 `WITH (fillfactor=90)` 漏了权威表上的
--      五个 autovacuum 参数（`01-schema.sql`）：
--      `autovacuum_enabled='true', autovacuum_vacuum_scale_factor='0.05',
--       autovacuum_vacuum_threshold='10', autovacuum_analyze_scale_factor='0.02',
--       autovacuum_analyze_threshold='50'`。
--      这是 **hot 表**（8h 保留、高写入），autovacuum 调优正是它最需要的。
--      ⇒ 按权威表补齐。⚠️ 注意 `CREATE TABLE IF NOT EXISTS` 对已存在的表**不会改**这些参数，
--        所以本条只对「表确实缺失、本脚本真的创建」的情况生效。
--
--   ⑤ 🟡 `:99-102` 的 `RAISE WARNING 'request_wal 父表不存在' ; RETURN;`
--      之后**仍会执行**第 117 行的 `CREATE VIEW` ⇒ 报一个更难懂的
--      "relation does not exist"。⇒ 改为让该错误**一路中止**。
--
--   ⑥ 🟡 所有 `pg_class` / `information_schema` 判据都补 `nspname='public'`。
--      原写法只按 `relname` 匹配 ⇒ 别的 schema 里有同名表就会误判
--      （与 210 号 `fix-request-logs-bodies-reattach-partitions.sql`、
--        212 号 `normalize-columnar-historical.sql` 记下的**同一形态**）。
--
--   ⑦ 🟡 第 5 节用 `hot_columns_count < 15` 这个**魔数**判断"结构不完整"，
--      且该 `information_schema` 判据没有 schema 限定。
--      ⇒ 改为与**权威基线**逐列比对（列名集合相等），魔数去掉。
--
-- ⚠️ 保留说明：`request_wal_hot` / `request_wal_bodies` 的**列清单本身是正确的**
--    （213 号逐列核对：17 列 / 4 列，与 `01-schema.sql` 完全一致、顺序也一致），
--    本轮**没有**改动这两份 CREATE 的列定义。

\set ON_ERROR_STOP on

BEGIN;

-- ============================================================
-- 1. 检查并创建 request_wal_hot 表
-- ============================================================

DO $$
DECLARE
  table_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal_hot' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO table_exists;
  
  IF table_exists THEN
    RAISE NOTICE 'request_wal_hot 表已存在，跳过创建';
  ELSE
    RAISE NOTICE 'request_wal_hot 表不存在，开始创建...';
  END IF;
END $$;

-- 213 号：补齐权威表上的 autovacuum 参数（见头部 ④）。
-- ⚠️ IF NOT EXISTS 对已存在的表不会改这些参数，本条只在本脚本真的创建时生效。
CREATE TABLE IF NOT EXISTS request_wal_hot (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb,
    -- 39 轮（2026-10-03 12h 审计）：主键从 (request_id, created_at) 对齐到
    -- 权威定义 (request_id)（sql/schema/01-schema.sql:21925 ALTER 形式）。
    -- 运行时 request_logger.go upsertInitial 的 ON CONFLICT (request_id)
    -- 只接受 request_id 单列唯一约束；按旧定义建出的表会让每次写入都以
    -- "no unique or exclusion constraint matching the ON CONFLICT
    -- specification" 失败。213 号已登记此分叉，本行收口。
    CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id)
) WITH (fillfactor=90,
        autovacuum_enabled=true,
        autovacuum_vacuum_scale_factor=0.05,
        autovacuum_vacuum_threshold=10,
        autovacuum_analyze_scale_factor=0.02,
        autovacuum_analyze_threshold=50);

DO $$ BEGIN RAISE NOTICE '✓ request_wal_hot 表已就绪'; END $$;

-- ============================================================
-- 2. 检查并创建 request_wal_bodies 表
-- ============================================================

DO $$
DECLARE
  table_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal_bodies' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO table_exists;
  
  IF table_exists THEN
    RAISE NOTICE 'request_wal_bodies 表已存在，跳过创建';
  ELSE
    RAISE NOTICE 'request_wal_bodies 表不存在，开始创建...';
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)
);

DO $$ BEGIN RAISE NOTICE '✓ request_wal_bodies 表已就绪'; END $$;

-- ============================================================
-- 3. 检查并创建 request_wal_with_current_month 视图
-- ============================================================

DO $$
DECLARE
  view_exists boolean;
  parent_table_exists boolean;
BEGIN
  -- 检查父表
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO parent_table_exists;
  
  IF NOT parent_table_exists THEN
    -- 213 号 ⑤：原来这里只是 RAISE WARNING + RETURN，而下面的 CREATE VIEW 照跑
    -- ⇒ 报一个更难懂的「relation does not exist」。改为一路中止。
    RAISE EXCEPTION 'public.request_wal 父表不存在，无法创建 request_wal_with_current_month 视图；'
                    '请先跑完 request_wal 的建表/分区迁移。';
  END IF;

  -- 检查视图（213 号 ⑥：补 schema 限定）
  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE viewname = 'request_wal_with_current_month'
      AND schemaname = 'public'
  ) INTO view_exists;

  IF view_exists THEN
    RAISE NOTICE 'request_wal_with_current_month 视图已存在，将重建';
    DROP VIEW request_wal_with_current_month;
  ELSE
    RAISE NOTICE '创建 request_wal_with_current_month 视图...';
  END IF;
END $$;

-- 213 号 ③：改为**显式列清单**，与权威定义（01-schema.sql 里的同名视图）一致。
-- `UNION ALL` 两侧靠**位置**对齐，`SELECT *` 一旦任一侧加列/改序就会静默错位或直接报错；
-- 权威定义本来就是逐列枚举的。
CREATE VIEW request_wal_with_current_month AS
SELECT request_wal_hot.request_id,
       request_wal_hot.tenant_id,
       request_wal_hot.gw_session_id,
       request_wal_hot.status,
       request_wal_hot.stage,
       request_wal_hot.client_model,
       request_wal_hot.upstream_provider_id,
       request_wal_hot.upstream_credential_id,
       request_wal_hot.completion_tokens,
       request_wal_hot.prompt_tokens,
       request_wal_hot.created_at,
       request_wal_hot.completed_at,
       request_wal_hot.upstream_request_at,
       request_wal_hot.upstream_response_at,
       request_wal_hot.error,
       request_wal_hot.compression_strategy,
       request_wal_hot.compression_meta
FROM public.request_wal_hot
UNION ALL
SELECT request_wal.request_id,
       request_wal.tenant_id,
       request_wal.gw_session_id,
       request_wal.status,
       request_wal.stage,
       request_wal.client_model,
       request_wal.upstream_provider_id,
       request_wal.upstream_credential_id,
       request_wal.completion_tokens,
       request_wal.prompt_tokens,
       request_wal.created_at,
       request_wal.completed_at,
       request_wal.upstream_request_at,
       request_wal.upstream_response_at,
       request_wal.error,
       request_wal.compression_strategy,
       request_wal.compression_meta
FROM public.request_wal;

COMMENT ON VIEW request_wal_with_current_month IS
'Optimized query VIEW using hot table architecture.
- request_wal_hot: independent hot table (0-7 days)
- request_wal: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.';

DO $$ BEGIN RAISE NOTICE '✓ request_wal_with_current_month 视图已就绪'; END $$;

-- ==============================================================
-- 4. 【213 号整节移除】原「迁移 request_wal_default 数据」已删除
--
-- 原写法只把 _default 的行 **拷贝** 进 request_wal_hot、**不删原件**：
--     INSERT INTO request_wal_hot SELECT * FROM request_wal_default
--     ON CONFLICT (request_id, created_at) DO NOTHING;
--
-- 而 `request_wal_default` 是 `request_wal` 的 **DEFAULT 分区**
-- （`migrations/startup/332_request_wal_default_partition.sql:31`），
-- 所以 `SELECT * FROM request_wal` **本来就已经包含这些行**。
-- 视图 = `request_wal_hot UNION ALL request_wal` ⇒ 拷完之后每行**出现两次**。
--
-- 之所以说它**必然触发**而不是罕见分支：332 是 **startup 迁移**，
-- 任何正常库里 `_default` 都存在。
--
-- 而且原件留在 `_default` 还会被 `promote_request_wal_default_batch()`
-- 提升进正式月分区 ⇒ 同一批行最终在 hot 和月分区**各存一份 ⇒ 永久重复**。
--
-- ⇒ 这些行**已经**通过 `request_wal`（含 DEFAULT 分区）出现在视图里，
--    不需要也不应该再拷一份。
--    若确实要把 _default 里的历史行**移走**（而非复制），那属于
--    `promote_request_wal_default_batch` 的职责，不在本脚本范围内。
-- ==============================================================

DO $$
DECLARE
  default_rows bigint;
BEGIN
  IF to_regclass('public.request_wal_default') IS NULL THEN
    RAISE NOTICE 'request_wal_default 不存在（正常，无需处理）';
    RETURN;
  END IF;
  EXECUTE 'SELECT count(*) FROM public.request_wal_default' INTO default_rows;
  RAISE NOTICE
    'request_wal_default 是 request_wal 的 DEFAULT 分区，其 % 行**已**通过父表出现在'
    'request_wal_with_current_month 视图中；213 号起本脚本**不再**把它们复制进 hot'
    '（复制会造成视图重复计数，且原件稍后会被 promote_…batch() 提升，造成永久重复）。',
    default_rows;
END $$;

-- ============================================================
-- 5. 验证（213 号重写）
--
-- 原来是 `hot_columns_count < 15` 这个**魔数**，而且那条
-- `information_schema.columns WHERE table_name=…` **没有 schema 限定**
-- （别的 schema 里有同名表就会数到别的列数）。
--
-- ⇒ 改为与**权威基线逐列比对**：列名集合必须与 01-schema.sql 里的
--   request_wal_hot 完全一致。魔数去掉。
-- ⚠️ 判据取「列名集合相等」而不是「列数相等」：列数相等但顺序/名字不同，
--   对 `INSERT … ON CONFLICT (request_id, created_at)` 与视图的**位置对齐**同样致命。
-- ============================================================

DO $$
DECLARE
  hot_count bigint;
  bodies_count bigint;
  view_exists boolean;
  actual_cols text;
  expected_cols text;
  missing_cols text;
  extra_cols text;
BEGIN
  SELECT count(*) INTO hot_count FROM public.request_wal_hot;

  -- 213 号：补 schema 限定
  SELECT EXISTS (
    SELECT 1 FROM pg_views
    WHERE viewname = 'request_wal_with_current_month'
      AND schemaname = 'public'
  ) INTO view_exists;

  IF NOT view_exists THEN
    RAISE EXCEPTION 'request_wal_with_current_month 视图创建失败';
  END IF;

  -- 权威列清单（来自 01-schema.sql，本轮逐列核对过，共 17 列）
  expected_cols :=
    'request_id,tenant_id,gw_session_id,status,stage,client_model,' ||
    'upstream_provider_id,upstream_credential_id,completion_tokens,prompt_tokens,' ||
    'created_at,completed_at,upstream_request_at,upstream_response_at,error,' ||
    'compression_strategy,compression_meta';

  SELECT string_agg(column_name, ',' ORDER BY ordinal_position) INTO actual_cols
  FROM information_schema.columns
  WHERE table_schema = 'public' AND table_name = 'request_wal_hot';

  IF actual_cols IS NULL THEN
    RAISE EXCEPTION 'public.request_wal_hot 不存在';
  END IF;

  SELECT string_agg(e, ',' ORDER BY e) INTO missing_cols
  FROM unnest(string_to_array(expected_cols, ',')) AS e
  WHERE e <> ALL (string_to_array(actual_cols, ','));

  SELECT string_agg(a, ',' ORDER BY a) INTO extra_cols
  FROM unnest(string_to_array(actual_cols, ',')) AS a
  WHERE a <> ALL (string_to_array(expected_cols, ','));

  IF missing_cols IS NOT NULL OR extra_cols IS NOT NULL THEN
    RAISE EXCEPTION
      'request_wal_hot 列集合与权威基线不一致 —— 缺: [%] 多: [%]。'
      '这会让 INSERT … ON CONFLICT (request_id, created_at) 与视图的位置对齐失效。',
      coalesce(missing_cols, ''), coalesce(extra_cols, '');
  END IF;

  SELECT count(*) INTO bodies_count FROM public.request_wal_bodies;

  RAISE NOTICE '========================================';
  RAISE NOTICE '✓ 所有验证通过';
  RAISE NOTICE '  - request_wal_hot: % 行, 列集合与权威基线一致', hot_count;
  RAISE NOTICE '  - request_wal_bodies: % 行', bodies_count;
  RAISE NOTICE '  - request_wal_with_current_month: 视图已创建';
  RAISE NOTICE '========================================';
END $$;

COMMIT;

-- 使用说明
\echo ''
\echo '修复完成！'
\echo ''
\echo '接下来的步骤：'
\echo '1. 在154服务器上重启 llm-gateway 服务'
\echo '2. 发送测试请求到 llm.kxpms.cn'
\echo '3. 验证数据是否正确写入 252 的 request_wal_hot 表'
\echo ''
\echo '验证命令：'
\echo 'SELECT COUNT(*), MAX(created_at) FROM request_wal_hot;'
\echo ''
