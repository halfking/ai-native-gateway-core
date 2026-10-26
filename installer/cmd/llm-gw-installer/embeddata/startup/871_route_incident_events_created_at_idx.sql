-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY，
-- PostgreSQL 不允许在事务块内执行它。installer 的 applySQL 据此对本文件走
-- 非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker）。
-- 若漏掉该标记，全新安装会失败于
--   "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
--
-- 871_route_incident_events_created_at_idx.sql
-- 2026-10-18：route_incident_events 的 created_at 首列索引。
--
-- 缺陷（生产实测，172.16.2.210/llm_gateway，共用库）：
--   route_incident_events 170 万行 / 1,004 MB（堆 580 MB + 索引 422 MB），
--   **四个索引全部把 created_at 放在末位或根本不含**，没有任何索引以它打头。
--
-- 为什么这是硬伤（EXPLAIN 实测，非推测）：
--   1) 30 天留存的删除计划：
--        Index Scan using idx_route_incident_events_type_created
--          Index Cond: (created_at < (now() - '30 days'))
--        条件建在**非首列**上 ⇒ PG 只能扫完整个 93 MB 索引再逐条过滤，
--        不是范围 seek；估算要删 404,440 行（cost=95532）。
--   2) 应用侧「取最近 50 条事件」：
--        Index Scan … (created_at > now()-'1 day')  +  Sort(created_at DESC)
--        同样是全索引扫描 + 对 170 万行排序（cost=24131）——
--        created_at 排不进索引有序性，这个计划在结构上无法优化。
--   ⇒ 「按时间留存」这条需求在现有结构下**第一条语句就会失败**：
--     服务端 statement_timeout = 30s，而该表冷缓存实测
--     count(*) 27.9s / max(created_at) 25.7s —— 正好卡在超时线上。
--
-- 为什么必须是 btree 而不是 BRIN（本地基准实测，两选一后推翻 BRIN）：
--   基准克隆 1,683,550 行（与生产行数差 0.9%），EXPLAIN (ANALYZE, BUFFERS)：
--     范围扫描（created_at < now()-30d）
--       无索引 : Parallel Seq Scan                      33,781 buffers  183.6ms
--       BRIN   : Parallel Bitmap Heap Scan               6,437 buffers  157.7ms
--       btree  : Parallel Index Only Scan (Fetches 0)      816 buffers   20.2ms
--     取最近 50 条（ORDER BY created_at DESC LIMIT 50）
--       无索引 : Seq Scan + top-N heapsort              33,859 buffers  172.8ms
--       BRIN   : **完全没有被使用**（仍是 Seq Scan + 排序）122.7ms
--       btree  : Index Scan Backward                         53 buffers    0.29ms
--   ⇒ BRIN 48 kB 只解决一半；btree 36 MB（占表 3.6%）两个维度同时可用。
--     原因：**BRIN 不提供有序性**，而 top-N 排序只要 28 kB 内存，
--     规划器天然选排序 ⇒ BRIN 在 ORDER BY … LIMIT 这类查询上是零收益。
--
-- 为什么用 CONCURRENTLY：
--   该表是**非分区普通表**（PG 支持在普通表上 CONCURRENTLY；不支持的是分区父表），
--   且网关持续写 failure_observed ⇒ 普通 CREATE INDEX 的 SHARE 锁会阻塞写入。
--   实测规模 580 MB heap，建索引是分钟级而非秒级，不能拿停写换。
--
-- ★ 为什么必须 SET lock_timeout（§44 的教训）：
--   无上限的锁等待会把 CREATE INDEX 排到写流量后面，期间它卡着队列，
--   后面所有要 AccessShareLock 的语句全被堵住 —— 2026-10-18 实测
--   一次迁移这样把 14 个网关会话堵了约 1.5 分钟。
--   lock_timeout=2s 的语义是「抢不到锁就快速失败」，不制造队列。
--
-- ⚠️ 非事务通道下**不能用 SET LOCAL**（没有事务块，它会告警且不生效），
--    必须用会话级 SET，并在结束时 RESET。
--
-- ★ 幂等：CONCURRENTLY 支持 IF NOT EXISTS。已存在同名索引时本文件是空操作；
--   但「同名且形状不对」必须报错而不是默默接受，故末尾有形状校验块。
--
-- 验收（按定义匹配，不要按名字猜）：
--   SELECT ic.relname, pg_size_pretty(pg_relation_size(i.indexrelid)),
--          i.indisvalid AND i.indisready AS valid
--   FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
--   WHERE i.indrelid = 'public.route_incident_events'::regclass
--     AND (SELECT array_agg(a.attname ORDER BY k.ord)
--          FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
--          JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum)[1]
--         = 'created_at';
-- 期望 1 行、valid=t、约 36 MB。真正的收益判据是「取最近 N 条」计划
-- 变成 Index Scan Backward，且范围扫描的 buffers 掉到千级。

SET lock_timeout = '2s';
-- statement_timeout 必须放开：索引构建本身很久，而 lock_timeout 只管锁等待，
-- 它才是「不要堵住写流量」的那一个。
SET statement_timeout = '0';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_route_incident_events_created_at
    ON public.route_incident_events (created_at);

RESET lock_timeout;
RESET statement_timeout;

-- 形状校验：同名但形状不对 ⇒ 明确报错，绝不默默接受一个错的索引。
-- （DO 块在自动提交模式下是独立事务，允许；与上面的 CREATE INDEX CONCURRENTLY
--   不是同一条语句，故不构成「在事务块内跑 CONCURRENTLY」的问题。）
DO $guard_871_index$
DECLARE
  v_idx regclass := to_regclass('public.idx_route_incident_events_created_at');
BEGIN
  IF v_idx IS NULL THEN
    RAISE EXCEPTION '871: idx_route_incident_events_created_at 缺失，索引创建未成功';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = v_idx AND indisvalid AND indisready
  ) THEN
    RAISE EXCEPTION '871: idx_route_incident_events_created_at 无效（CONCURRENTLY 构建可能失败）；'
                    '请先 DROP INDEX CONCURRENTLY 重建后再重跑本迁移';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_am am ON am.oid = c.relam
    WHERE i.indexrelid = v_idx
      AND i.indrelid = 'public.route_incident_events'::regclass
      AND am.amname = 'btree'
      AND NOT i.indisunique
      AND i.indexprs IS NULL
      AND i.indnatts = 1 AND i.indnkeyatts = 1
      AND pg_get_indexdef(i.indexrelid, 1, true) = 'created_at'
  ) THEN
    RAISE EXCEPTION '871: idx_route_incident_events_created_at 定义不符 —— '
                    '本迁移要的是单列 btree(created_at)，请确认同名索引的形状';
  END IF;
END
$guard_871_index$;

COMMENT ON INDEX public.idx_route_incident_events_created_at IS
  '871: route_incident_events 的 created_at 首列索引。建它的理由不是查询慢，而是「无法安全地按时间留存」——原四个索引全把 created_at 放末位，范围条件只能全索引扫描，实测冷缓存 25.7s vs statement_timeout 30s。本地基准：btree 让范围扫描 33,781→816 buffers、取最近 50 条 33,859→53 buffers（Index Scan Backward）。BRIN 只有 48 kB 但不提供有序性，对 ORDER BY … LIMIT 零收益，故未采用。必须 CONCURRENTLY（网关持续写该表），并配 lock_timeout=2s 快速失败以免排队堵住写流量。';
