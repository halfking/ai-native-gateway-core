-- Migration 910 down: 把 `routing_analytics_source` 视图体换回 **907 的
-- LATERAL 形态**（会话族两腿 + LEFT JOIN LATERAL (... LIMIT 1) 回定位）。
--
-- ⚠ 回退会**恢复**「父表腿 ~2.75 万次逐行探针」的 907 形态
--   （252 实测 907 后 REFRESH 均值 ≈32s（混合窗推导，区间 [32,41]s）、
--   整计划 ~147K buffers）⇒ 回退是**性能回退**，不是（带窗读的）口径回退：
--   窗口内两形行集合逐行一致（910 的窗口内父表腿双向 EXCEPT ALL 实测 0/0
--   @66,838 行）。★ 边界提醒：910 的日界下推对「无 ts 窗的全量读」有
--   口径边界（>8d 旧行 details 三列为 NULL）⇒ 回退到 907 后无窗全量读的
--   details 列会**恢复有值**——当前全仓 13 处消费方全部带窗/存在性探针，
--   回退对它们零口径影响。
--   ★ 不回退到 canonical 形态（那是 907.down 的职责；907 已 applied
--   且文件内容冻结，本 down 逐字采用 907.up 的视图体）。
--
-- ★ 回退同样**不需要重建 matview**：CREATE OR REPLACE VIEW 保留 OID ⇒
--   依赖不动 ⇒ 下一次 REFRESH 自动采用回退后的定义。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '2min';

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
--   -- 预期：两腿都是 LEFT JOIN LATERAL (... LIMIT 1)（= 907.up 形态）。
