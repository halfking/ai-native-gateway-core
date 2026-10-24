-- Migration 907 down: 把 `routing_analytics_source` 视图体换回 **canonical 形态**
-- （`FROM request_logs_with_current_month`）。
--
-- ⚠ 回退会**恢复**「details 侧顺序扫 45 万行」的旧形态
--   （252 实测中位 4,482.8 ms，最坏一轮 20,378.6 ms）⇒ 回退是**性能回退**，
--   不是口径回退：两形的行集合逐行一致（907 的 EXCEPT ALL 实测 only_old=0/only_new=0）。
--
-- ★ 回退同样**不需要重建 matview**：CREATE OR REPLACE VIEW 保留 OID ⇒
--   依赖不动 ⇒ 下一次 REFRESH 自动采用回退后的定义。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '2min';

		CREATE OR REPLACE VIEW routing_analytics_source AS
		SELECT
		  ts,
		  task_type::text AS task_type,
		  outbound_model::text AS outbound_model,
		  client_model::text AS client_model,
		  work_type::text AS work_type,
		  provider_id::bigint AS provider_id,
		  credential_id::bigint AS credential_id,
		  is_auto_request::boolean AS is_auto_request,
		  tenant_id::text AS tenant_id,
		  request_id::text AS request_id,
		  success::boolean AS success,
		  latency_ms::numeric AS latency_ms,
		  cost_usd::numeric AS cost_usd,
		  origin_stage::text AS origin_stage,
		  auto_profile::text AS auto_profile
		FROM request_logs_with_current_month;

COMMIT;

-- POST_CONDITION:
--   SELECT pg_get_viewdef('public.routing_analytics_source'::regclass, true);
--   -- 预期：FROM request_logs_with_current_month
