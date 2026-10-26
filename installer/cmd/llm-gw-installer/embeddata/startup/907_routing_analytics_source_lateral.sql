-- Migration 907: `routing_analytics_source` 换成「会话族两腿 + LATERAL 回定位」
-- ⇒ 消除刷新链里 45 万行 `session_turn_details` 顺序扫。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-24 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【背景：共享库上最大的持续慢查询源】
--   R95 轮归因：`routing_analytics_7d` / `routing_audit_summary_7d` 的 REFRESH
--   累计 3,181,560 ms ≈ 53 分钟 DB 时间（单次最长 74,331 ms）；
--   瓶颈 = `session_turn_details` **45 万行顺序扫**（Buffers shared hit=95,086 ≈ 743 MB）。
--
-- 【★ 根因：为了 3 列把整个宽表 JOIN 进来】
--   旧体：`SELECT 15 列 FROM request_logs_with_current_month`
--     ⇒ 该 canonical 视图是 `session_turns_hot ∪ session_turns`
--       **LEFT JOIN session_turn_details(_hot)** 的 118 列投影
--     ⇒ 规划器只能选 Hash Join ⇒ details 侧必须**扫全部分区**
--   ⇒ 生产 EXPLAIN：`Parallel Hash Right Join` + details 侧
--     `Parallel Seq Scan` × 4（326,497 + 128,517 + 18 + 18）
--   ⇒ 而 source 的 15 列里**只有 3 列**来自 details
--     （client_model / provider_id / auto_profile），其余 12 列来自 session_turns。
--
-- 【★ 修法：details 的 3 列用 LATERAL 回定位】
--   `LEFT JOIN LATERAL (SELECT ... FROM session_turn_details dd
--      WHERE dd.tenant_id = t.tenant_id AND dd.request_id = t.request_id
--        AND dd.partition_date = t.partition_date LIMIT 1) d ON TRUE`
--   ⇒ 走 details 的唯一索引 (tenant_id, request_id, partition_date) 逐行回定位
--   ⇒ **只读窗口内真正需要的行**，不再顺序扫 45 万行。
--
-- 【★ 口径等价（252 生产实测，同一语句 ⇒ 同一快照）】
--   · 源层双向 EXCEPT ALL（1,110,635 行）：only_old=**0** / only_new=**0**
--   · 聚合层（matview 输出口径）双向 EXCEPT ALL：c1_only_old=**0** / c1_only_new=**0**
--   · 逐列类型对拍：15 列列名与类型逐列一致（CREATE OR REPLACE 的列契约）
--   ★ LIMIT 1 的前提：details 的唯一键是 (tenant_id, request_id, partition_date)
--     且 partition_date 是分区键 ⇒ 同一键最多 1 行 ⇒ LEFT JOIN 与
--     LEFT JOIN LATERAL(... LIMIT 1) **产出同样的行集合**。
--
-- 【★ 实测收益（252，3 轮交错 A/B，取中位）】
--   旧体 4,482.8 ms（最坏一轮冷缓存 20,378.6 ms）｜新体 2,270.8 ms
--   ⇒ 中位 **2.0×**，冷缓存轮 **10.6×**
--
-- 【★ 为什么【不】加索引】
--   实测另建的覆盖索引 (tenant_id, request_id, partition_date)
--     INCLUDE (client_model, provider_id, auto_profile)
--   **没有被规划器选中**（它比既有唯一索引更大 ⇒ 估算更贵 ⇒ 选了既有那条 + Heap Fetches）
--   ⇒ ★ 覆盖索引白建 ⇒ 本迁移**零 DDL**（只换视图体）。
--
-- 【★ matview 不需要重建】
--   两个 matview 的**查询体不变**，仍读 `routing_analytics_source`；
--   ★ `CREATE OR REPLACE VIEW` **保留 OID** ⇒ matview 的依赖不动
--   ⇒ ★ 下一次 REFRESH 自动采用新定义（本地真库实测：REFRESH 后内容即新）
--   ⇒ ★⇒ **不需要 DROP/CREATE matview，也不需要重建启动期的 stale-definition gate**；
--     ★ 但 db.go 的 gate 特征串必须**同时接受新旧两形**（否则新形被判陈旧
--     ⇒ 走 CASCADE 重建路径，启动期 minutes 级）。见 `db/db.go` 的 fast-path gate。
--
-- 【幂等】CREATE OR REPLACE VIEW 本身幂等（重复执行结果相同）。
-- 【锁与时长】实测 CREATE OR REPLACE VIEW 耗时 **245 ms**（252 生产）。
-- 【Down】把视图体换回 canonical 形态（`FROM request_logs_with_current_month`）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '2min';

		-- Keep analytics isolated from the frozen request-log wrapper view. The
		-- narrow source has stable types across hot and parent partitions and
		-- explicitly exposes origin_stage for probe filtering.
		-- CREATE OR REPLACE (not DROP+CREATE): the routing matviews depend on
		-- this view, so a plain DROP fails with SQLSTATE 2BP01 whenever this
		-- batch runs on the index-repair path (views current, ukey missing).
		-- 2026-10-13 S3 波 2（停写切换）：源从 _request_logs_hot UNION ALL
		-- _request_logs 换成 canonical 视图 request_logs_with_current_month。
		-- 停写后新流量只落 session_turns(_hot)，裸 v1 双腿会让整套自动路由
		-- 分析（matrix/flow/funnel 端点与 7d 物化视图）随热窗排空静默归零。
		-- 2026-10-24 R96 轮：源再从 canonical 视图换成**会话族两腿 + LATERAL 回定位**
		-- （口径逐行等价见上；收益与不加索引的理由见上）。
		-- 15 列全部在 118 列契约内、列名/类型/次序与旧体逐位一致（CREATE
		-- OR REPLACE 的列契约；auto_profile 仍居末位）。会话臂的
		-- provider_id/auto_profile/client_model 依赖 734 details 特征层，
		-- 覆盖率随 details 行走（已登记）；work_type 会话侧近乎恒空，
		-- effective_work_type 维度对停写后流量退化为 unknown（既有口径）。
		-- ⚠️ 注释里**不许出现反引号**：本段 SQL 在 Go raw string 里，反引号会提前终止字符串。
		CREATE OR REPLACE VIEW routing_analytics_source AS
		SELECT
		  t.ts,
		  t.task_type::text AS task_type,
		  t.model::text AS outbound_model,
		  d.client_model::text AS client_model,
		  t.work_type::text AS work_type,
		  d.provider_id::bigint AS provider_id,
		  (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END)::bigint AS credential_id,
		  t.is_auto_request::boolean AS is_auto_request,
		  t.tenant_id::text AS tenant_id,
		  t.request_id::text AS request_id,
		  t.success::boolean AS success,
		  t.latency_ms::numeric AS latency_ms,
		  t.cost_usd::numeric AS cost_usd,
		  t.origin_stage::text AS origin_stage,
		  d.auto_profile::text AS auto_profile
		FROM public.session_turns_hot t
		LEFT JOIN LATERAL (
		  SELECT dd.client_model, dd.provider_id, dd.auto_profile
		  FROM public.session_turn_details_hot dd
		  WHERE dd.tenant_id = t.tenant_id
		    AND dd.request_id = t.request_id
		    AND dd.partition_date = t.partition_date
		  LIMIT 1
		) d ON TRUE
		UNION ALL
		SELECT
		  t.ts,
		  t.task_type::text AS task_type,
		  t.model::text AS outbound_model,
		  d.client_model::text AS client_model,
		  t.work_type::text AS work_type,
		  d.provider_id::bigint AS provider_id,
		  (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END)::bigint AS credential_id,
		  t.is_auto_request::boolean AS is_auto_request,
		  t.tenant_id::text AS tenant_id,
		  t.request_id::text AS request_id,
		  t.success::boolean AS success,
		  t.latency_ms::numeric AS latency_ms,
		  t.cost_usd::numeric AS cost_usd,
		  t.origin_stage::text AS origin_stage,
		  d.auto_profile::text AS auto_profile
		FROM public.session_turns t
		LEFT JOIN LATERAL (
		  SELECT dd.client_model, dd.provider_id, dd.auto_profile
		  FROM public.session_turn_details dd
		  WHERE dd.tenant_id = t.tenant_id
		    AND dd.request_id = t.request_id
		    AND dd.partition_date = t.partition_date
		  LIMIT 1
		) d ON TRUE;

COMMIT;

-- POST_CONDITION:
--   SELECT pg_get_viewdef('public.routing_analytics_source'::regclass, true);
--   -- 预期：FROM public.session_turns_hot t LEFT JOIN LATERAL (...)
--   --       UNION ALL FROM public.session_turns t LEFT JOIN LATERAL (...)
--   --       （不再是 FROM request_logs_with_current_month）
--   -- 列契约（CREATE OR REPLACE 只能加列，不能改名列/次序/类型）：
--   SELECT attname, format_type(atttypid, atttypmod)
--     FROM pg_attribute
--    WHERE attrelid = 'public.routing_analytics_source'::regclass
--      AND attnum > 0 AND NOT attisdropped
--    ORDER BY attnum;
--   -- 预期 15 列，auto_profile 仍在末位。
