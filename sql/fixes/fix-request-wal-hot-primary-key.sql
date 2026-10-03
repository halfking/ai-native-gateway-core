-- 修复 request_wal_hot 和 request_logs_hot 表缺少 (request_id, <时间列>) 唯一键的问题
--
-- R89-EA（212 号）更正 —— 本脚本原头部写「表缺少主键的问题 / 所有请求日志写入失败」。
-- **与仓库权威基线不符**：`sql/schema/01-schema.sql:21827` 与 `:21907` 明确写着
--     ADD CONSTRAINT request_logs_hot_pkey PRIMARY KEY (request_id);
--     ADD CONSTRAINT request_wal_hot_pkey  PRIMARY KEY (request_id);
-- ⇒ 两张表**都已有主键**，只是**在 `request_id` 单列上**，不在 `(request_id, ts/created_at)` 上。
-- ⇒ 原来的存在性检查按**约束名**查（`conname='request_wal_hot_pkey'`）⇒ 在任何
--    与基线一致的库里都会**命中并直接跳过** ⇒ **整份脚本是 no-op**，
--    而头部宣称它修了一个「所有请求日志写入失败」的问题。
--
-- ⚠️ 更值得警惕的是它的**失效方向**：检查按**名字**而不是按**形状**问「有没有主键」。
--    一旦主键存在但名字不同（例如被某个迁移重命名过），它会**再加一个主键** ⇒
--    PG 直接报 `multiple primary keys for table` ⇒ 整段失败。
--    这与 210 号 R89-DV 的「查错了系统目录」、以及 playbook §129 的
--    「问『有没有任意一个』还是『有没有那一个』」是同一族。
--
-- 真正缺的是 **`ON CONFLICT (request_id, ts)` 所需的那一组列**：
-- `ON CONFLICT (cols)` 只认**恰好**建在这些列上的唯一索引/约束；
-- 只有 `(request_id)` 的主键**不满足** `(request_id, ts)`。
-- ⇒ **这个缺口由同目录的 `fix-request-logs-hot-unique-constraint.sql` 负责**
--   （R89-DV，210 号：改查 `pg_index`，并要求列清单恰好相等）。
-- ⇒ **本脚本不再自己建索引/主键**，只负责：
--      ① 把两张表主键的真实形状**报出来**（而不是按名字猜）；
--      ② 显式指明 `(request_id, ts)` 那一组列由哪个脚本负责，避免重复建索引。
--      这样两张脚本**不会互相看不见**，也不会在同一张表上建出两份同列唯一索引。
--
-- 影响：所有请求日志写入失败 —— **该说法只在缺少 (request_id, ts) 唯一键时成立**；
--       按基线，主键并不缺。

\set ON_ERROR_STOP on

BEGIN;

-- ============================================================
-- 1+2. 报告两张表主键的**真实形状**（按 pg_constraint 的实际列，不按名字猜）
-- ============================================================
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT t.tbl, c.conname, c.contype,
           (SELECT string_agg(a.attname, ', ' ORDER BY k.ord)
              FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
              JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) AS cols
    FROM (VALUES ('request_wal_hot'), ('request_logs_hot')) AS t(tbl)
    LEFT JOIN pg_class cl ON cl.relname = t.tbl
    LEFT JOIN pg_constraint c ON c.conrelid = cl.oid AND c.contype IN ('p','u')
  LOOP
    IF r.conname IS NULL THEN
      RAISE EXCEPTION '%.212：%s 上找不到任何主键/唯一约束；本脚本的「只报告」策略'
                      '遇到这种情况会中止，请人工确认后再加。', '  ', r.tbl;
    END IF;
    RAISE NOTICE '% : % % (%)', r.tbl, r.contype, r.conname, r.cols;
  END LOOP;

  RAISE NOTICE
    '注意：`ON CONFLICT (request_id, <时间列>)` 认的是**恰好**建在那两列上的唯一索引/约束；'
    '上面主键只在 (request_id) 上，**不满足**该推断。'
    '该缺口由 fix-request-logs-hot-unique-constraint.sql 负责（改查 pg_index）。';
END $$;

-- ============================================================
-- 3. 212 号更正：原「验证」段按**约束名**复查并打印「已创建」
--    —— 但本脚本 212 号之后**不再创建任何约束**（见头部 ②），
--    打印「已创建」会是在报告一件**没有发生的事**。
--    ⇒ 改为真正要问的问题：**(request_id, <时间列>) 那一组列上到底有没有唯一索引？**
--      这是 `ON CONFLICT` 能不能工作的**唯一**判据。
-- ============================================================

DO $$
DECLARE
  r record;
  tbl text;
  timecol text;
  ok boolean;
  detail text;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['request_wal_hot', 'request_logs_hot'] LOOP
    timecol := CASE WHEN tbl = 'request_wal_hot' THEN 'created_at' ELSE 'ts' END;

    SELECT coalesce(string_agg(c.conname, ', '), '(无)') INTO detail
    FROM pg_class cl
    JOIN pg_constraint c ON c.conrelid = cl.oid AND c.contype IN ('p','u')
    JOIN pg_attribute a1 ON a1.attrelid = cl.oid AND a1.attname = 'request_id'
    JOIN pg_attribute a2 ON a2.attrelid = cl.oid AND a2.attname = timecol
    WHERE cl.relname = tbl
      AND c.conkey = ARRAY[a1.attnum, a2.attnum]::smallint[];

    ok := (detail <> '(无)');

    IF ok THEN
      RAISE NOTICE '✓ % 上已有恰好 (request_id, %) 的唯一约束/主键：%',
                   tbl, timecol, detail;
    ELSE
      -- 212 号：这是**真门禁**（原来这里不存在，只有一个"按名字复查"的摆设）。
      RAISE EXCEPTION
        '✗ % 上**没有**恰好建在 (request_id, %) 上的唯一索引/约束。'
        ' ⇒ `ON CONFLICT (request_id, %)` 在这张表上会直接报错'
        '（there is no unique or exclusion constraint matching the ON CONFLICT specification）。'
        ' 本脚本不会自己补（避免与同目录 fix-request-logs-hot-unique-constraint.sql '
        ' 建出两份同列唯一索引）—— 请先跑那一份。',
        tbl, timecol, timecol;
    END IF;
  END LOOP;
END $$;

-- ============================================================
-- 4. 端到端验证：用真实的 `INSERT … ON CONFLICT` 探一次
--    ⚠️ 212 号：这一段**原本就存在且写得不错**（自插 + 同表 DELETE 清理），
--    但因为前面全是 no-op，它从未真正证明过任何事。
--    212 号之后它是**真门禁**：若 (request_id, <时间列>) 上没有唯一索引，
--    这条 INSERT 会直接抛 `there is no unique or exclusion constraint matching
--    the ON CONFLICT specification` ⇒ 事务回滚 ⇒ 脚本中止。
--    （`\set ON_ERROR_STOP on` 已在文件头设置，所以不会再"打印完错误继续跑"。）
-- ============================================================

DO $$
DECLARE
  test_request_id text;
  test_ts timestamptz;
BEGIN
  test_request_id := 'test_' || extract(epoch from now())::text;
  test_ts := NOW();

  -- 测试 request_wal_hot INSERT with ON CONFLICT
  INSERT INTO request_wal_hot (
    request_id, tenant_id, status, stage, client_model, created_at
  ) VALUES (
    test_request_id, 'test_tenant', 'pending', 0, 'test-model', test_ts
  ) ON CONFLICT (request_id, created_at) DO NOTHING;

  RAISE NOTICE '✓ request_wal_hot INSERT with ON CONFLICT 测试通过';

  -- 测试 request_logs_hot INSERT with ON CONFLICT
  INSERT INTO request_logs_hot (
    request_id, ts, tenant_id, success
  ) VALUES (
    test_request_id, test_ts, 'test_tenant', true
  ) ON CONFLICT (request_id, ts) DO NOTHING;

  RAISE NOTICE '✓ request_logs_hot INSERT with ON CONFLICT 测试通过';

  -- 清理测试数据（**同表**删除 —— 与 209 号 §124 同一纪律）
  DELETE FROM request_wal_hot WHERE request_id = test_request_id;
  DELETE FROM request_logs_hot WHERE request_id = test_request_id;

  RAISE NOTICE '✓ 测试数据已清理';
END $$;

COMMIT;

-- ============================================================
-- 使用说明
-- ============================================================

\echo ''
\echo '检查完成（212 号：本脚本**不再创建**任何主键/索引）'
\echo ''
\echo '两件事请注意：'
\echo '1. request_wal_hot / request_logs_hot 的主键按 01-schema.sql 本来就存在，'
\echo '   只是建在 (request_id) 单列上。'
\echo '2. 若脚本以错误中止（ON CONFLICT 缺唯一索引），先跑：'
\echo '     psql -d llm_gateway -f sql/fixes/fix-request-logs-hot-unique-constraint.sql'
\echo '   它负责补 (request_id, ts) 那一组列上的唯一索引。'
\echo ''
\echo '若上面都通过，重启 llm-gateway 服务后可用下列语句观察写入：'
\echo '   -- 检查 request_wal_hot'
\echo '   SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL ''5 minutes'';'
\echo '   -- 检查 request_logs_hot'
\echo '   SELECT COUNT(*), MAX(ts) FROM request_logs_hot WHERE ts > NOW() - INTERVAL ''5 minutes'';'
\echo ''
