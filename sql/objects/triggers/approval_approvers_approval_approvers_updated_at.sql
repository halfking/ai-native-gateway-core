--
-- Name: approval_approvers approval_approvers_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

-- =============================================================================
-- DEFERRED FUNCTIONS — Created here (after all tables) to satisfy dependency
-- ordering that pg_dump --schema-only does not respect. See note above.
-- =============================================================================

-- recent_success_rate — moved from earlier in the dump.
--
-- ★ 2026-10-20（迁移 890，v1 族最终删除）：本函数原先读 `request_logs_hot`
--   ⇒ 它被 provider/client.go 的候选查询以 `CROSS JOIN LATERAL` 调用 ⇒
--   42P01 ⇒ **选不出任何上游凭据 ⇒ 100% 流量失败**。
--   而且 `CREATE OR REPLACE FUNCTION` 对 SQL 函数体**不在创建时解析** ⇒
--   建得成功、首次调用才炸（症状看起来像「所有上游都挂了」，而不像迁移不匹配）。
--
--   现在读 `public.session_turns_hot`（会话族的"热窗"表）。
--
--   权威来源：db/db.go 的 recentSuccessRateEnsureSQL（运行时安装）与
--   sql/migrations/startup/890_drop_request_logs_v1_family.sql（迁移里同一段）。
--   迁移与运行时两者的**逐字等价**由
--   sql/migrations/startup/migration_890_v1_family_drop_test.go 守着。
--

		CREATE OR REPLACE FUNCTION recent_success_rate(p_credential_id BIGINT,
		                                    p_raw_model     TEXT,
		                                    p_sample_n      INT DEFAULT 50,
		                                    p_window_hours  INT DEFAULT 3)
		RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
		LANGUAGE sql
		STABLE
		AS $$
			    WITH recent AS (
			        SELECT success, ts
				    FROM public.session_turns_hot
				    WHERE credential_id ~ '^[0-9]+$'
				      AND credential_id::bigint = p_credential_id
				      AND lower(COALESCE(model, canonical_model)) = lower(p_raw_model)
				      AND ts > NOW() - (p_window_hours || ' hours')::interval
				      AND COALESCE(task_type, '') <> 'probe_triggered'
				      AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
				      AND request_id NOT LIKE 'probe-%'
			    )

		    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
		           COUNT(*)::int
		    FROM (
		        SELECT success FROM recent ORDER BY ts DESC LIMIT p_sample_n
		    ) newest_samples;
		$$;
	
