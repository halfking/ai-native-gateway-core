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
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS)
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
--       idx_%s_client_model_trgm USING gin (client_model gin_trgm_ops)
--   那是 **v1 族的既有能力**，session 族从未迁移过它。本迁移补齐。
--
-- 为什么只索引 search_text、**不**给 model / client_model 建 trigram:
--   - session_turns 上**根本没有 client_model 列**（全 startup/ grep：
--     client_model 只存在于 request_logs / session_summaries.client_models /
--     session_audit，对 session_turns 零命中；turn_writer.go 的 INSERT 列清单
--     里也没有）。给一个不存在的列建索引无从谈起，审计里"顺带考虑
--     client_model"的前提在本族不成立。
--   - model 等值过滤（admin/logs.go:567 的 ?model=）走的是 **精确匹配 =**，
--     不是 ILIKE（2026-08-29 刻意从 '%…%' 改回精确匹配）。btree 已经服务得了，
--     GIN trigram 在这里是纯写放大。
--   - 全仓唯一的 search_text 子串谓词就是上面那一条 admin/logs.go:540，
--     本索引是它的唯一服务对象，不是"以防万一"的铺开。
--
-- 为什么建在**父表**而不是逐分区（870/874 同款取舍）:
--   普通 CREATE INDEX 在分区父表上会**级联**到现存分区，且未来
--   CREATE TABLE … PARTITION OF 新月分区自动继承。
--   ⚠ 这里有一条必须写下来的实测事实，它纠正了一个流传的错误说法：
--   **「父表 CREATE INDEX 只挂父表、分区上没有」是错的。**
--   本轮在一次性 PostgreSQL 17.11 上实测（见文件末「实测记录」）：
--   ① DDL 文本里**不写** ON ONLY 时，pg_get_indexdef 对**父表**那条依然渲染
--      出 `ON ONLY` 字样——那是 pg_dump/渲染层对分区父表索引的固定表述，
--      **不能**用它来判断索引有没有级联；
--   ② 同一条 CREATE INDEX 执行后，两个月分区都拿到了自己的 trigram 索引
--      （自动改名 st_2026_09_search_text_idx 等），indisvalid=t；
--   ③ 之后 ATTACH 的新分区同样自动继承。
--   ⇒ 验收必须按**分区级 + 定义匹配**查 pg_index，不能按父表定义里的 ON ONLY
--      文字判断，也不能按索引名 grep（PG 按列名自动改名）。
--
-- 上线代价: session_turns 是活写分区父表，父表上不支持 CREATE INDEX
--   CONCURRENTLY（842/846/862/870/874 同一实测），普通 CREATE INDEX 级联期间
--   写被 ShareLock 阻塞、读照常。GIN trigram 构建需对全部 search_text 值
--   抽 trigram，6.4GB 分区属分钟级；共享 PG 服务端 statement_timeout=30s，
--   必须在本文件内 SET LOCAL 放宽，否则 57014 击穿后迁移永不记账、成为永久
--   卡点（874 同因）。lock_timeout=60s 抢不到锁则整体回滚、什么都没发生，
--   稍后重跑即可。
--
-- 回滚：.down.sql 按名删两条父级/hot 级索引（删父表索引级联删各分区子索引），
-- 并按 640 原样重建它删掉的那条 upstream_protocol 索引。不动数据。
--
-- 本迁移同时**删掉一条死索引**（640 建的
-- idx_session_turns_hot_upstream_protocol），取舍与代价口径见文件末该段。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '60s';

CREATE INDEX IF NOT EXISTS idx_session_turns_search_trgm
    ON public.session_turns USING gin (search_text gin_trgm_ops);

-- 同一谓词的 hot 侧：view 的第一条 UNION ALL 臂就是 session_turns_hot，
-- 不建则热数据仍然顺序扫描。
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_search_trgm
    ON public.session_turns_hot USING gin (search_text gin_trgm_ops);

-- ── 附带：删掉 640 建的 idx_session_turns_hot_upstream_protocol ──────────
-- 640 建了两条同名形态的部分索引（client_protocol / upstream_protocol）。
-- 其中 **upstream_protocol 这条是死的**，两条独立的理由：
--   ① 写入侧：turn_writer.go 的 INSERT 列清单里只有 client_protocol
--      （§9.208 明确记着它被追加在参数表末尾），**没有 upstream_protocol**，
--      全仓也无第二个写入方 ⇒ 该列在 session_turns_hot 上恒为 NULL，
--      部分索引的谓词 `upstream_protocol IS NOT NULL` **永不成立**。
--   ② 读取侧：全仓 grep `upstream_protocol`，除本索引自身与 640 的列定义
--      外，唯一命中是 domains/hooks/observability/telemetry/client.go:2692 ——
--      那条 UPDATE 打的是 **_request_logs_hot**（v1 表），与本族无关。
--      ⇒ 没有任何查询按 session_turns.upstream_protocol 过滤。
--
-- 为什么选「删索引」而不是「补写入」：补写入要跨
-- ProcessedRequest → v2.ProcessedRequest（dual_writer.convertToV2Request）
-- → TurnRecord 三层新增字段，而 **上游语义源**（egressProtocol）目前只在
-- streaming/handler.go:7694 流向 telemetry 的 RequestLogEntry，
-- session 家族的写入点拿不到它。为一个零读者的列铺三层字段，
-- 代价远大于收益；而删掉一条空索引零风险、可随时重建。
--
-- ⚠ 代价的诚实口径：这条**部分**索引因谓词永不成立而**恒为 0 条目**
--   （一次性 PG 17.11 实测：10 万行全 NULL 的表上该索引 8192 bytes），
--   所以它的写放大只是「每行求一次值谓词」，**不是**同族全量索引那种量级。
--   删它的收益是清理一个**误导性的对象**（读方普查会把它算成"有索引可用"
--   的假象），而不是显著省资源。这一点必须写明，避免下一个读注释的人
--   以为这条索引曾很贵。
-- ⚠ 只删 upstream_protocol 那条，**保留 client_protocol 那条**：
--   client_protocol 有写入方（§9.208 已接）与读取方（640 的注释说明
--   它是协议转换诊断的入口），是活的。
DROP INDEX IF EXISTS public.idx_session_turns_hot_upstream_protocol;

COMMIT;

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
-- ===========================================================================

-- COMMENT 与创建腿同守卫（874 同款：守卫掉的对象留无条件 COMMENT 会在
-- COMMIT 后以 relation does not exist 非零退出，迁移永不记账）。
DO $comment_877_index$
BEGIN
  IF to_regclass('public.idx_session_turns_search_trgm') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_search_trgm IS
      '877: session_turns 分区父表 search_text GIN trigram 索引 —— S4 停写后 admin/logs.go:540 的 search_text ILIKE ''%…%'' 谓词的承重索引；前导通配符使 btree 无法服务，无它则该谓词对每个 6.4GB 级月分区顺序扫描。验收须按分区级 pg_index 查（父表 pg_get_indexdef 恒渲染 ON ONLY，不代表未级联）。';
  END IF;
  IF to_regclass('public.idx_session_turns_hot_search_trgm') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_turns_hot_search_trgm IS
      '877: session_turns_hot 的 search_text GIN trigram 索引 —— 同一条 ILIKE ''%…%'' 谓词在 view 第一条 UNION ALL 臂（hot）上的承重索引；不建则热数据仍顺序扫描。';
  END IF;
END
$comment_877_index$;
