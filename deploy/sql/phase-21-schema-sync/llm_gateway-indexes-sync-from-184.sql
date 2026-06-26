-- ============================================================
-- Index sync for database: llm_gateway
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 62
-- ============================================================

\connect llm_gateway

CREATE INDEX credit_ledger_2026_06_ref_type_ref_id_idx ON public.credit_ledger_2026_06 USING btree (ref_type, ref_id);

CREATE INDEX tool_usage_stats_2026_07_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (tenant_id, usage_date);

CREATE INDEX idx_request_logs_default_client_model_trgm ON public.request_logs_default USING gin (client_model public.gin_trgm_ops);

CREATE INDEX idx_asset_rel_src ON public.asset_relationships USING btree (src_kind, src_ref_id);

CREATE INDEX idx_tool_usage_stats_tool_tenant ON public.tool_usage_stats_old USING btree (tool_id, tenant_id, usage_date DESC);

CREATE INDEX tool_usage_stats_2026_06_created_at_idx ON public.tool_usage_stats_2026_06 USING btree (created_at);

CREATE INDEX idx_agents_capabilities ON public.agents USING gin (capabilities jsonb_path_ops);

CREATE INDEX idx_asset_rel_dst ON public.asset_relationships USING btree (dst_kind, dst_ref_id);

CREATE INDEX idx_agent_rel_src ON public.agent_relationships USING btree (src_agent_id);

CREATE INDEX credit_ledger_2026_07_ref_type_ref_id_idx ON public.credit_ledger_2026_07 USING btree (ref_type, ref_id);

CREATE INDEX usage_ledger_2026_08_ts_idx ON public.usage_ledger_2026_08 USING btree (ts);

CREATE INDEX credit_ledger_2026_07_tenant_id_created_at_idx ON public.credit_ledger_2026_07 USING btree (tenant_id, created_at);

CREATE INDEX idx_request_logs_2026_06_client_model_trgm ON public.request_logs_2026_06 USING gin (client_model public.gin_trgm_ops);

CREATE INDEX idx_agent_rel_dst ON public.agent_relationships USING btree (dst_agent_id);

CREATE UNIQUE INDEX request_logs_2026_07_request_id_ts_idx ON public.request_logs_2026_07 USING btree (request_id, ts);

CREATE INDEX idx_agents_kind ON public.agents USING btree (tenant_id, kind);

CREATE INDEX credit_ledger_2026_08_tenant_id_created_at_idx ON public.credit_ledger_2026_08 USING btree (tenant_id, created_at);

CREATE INDEX credit_ledger_2026_08_created_at_idx ON public.credit_ledger_2026_08 USING btree (created_at);

CREATE INDEX idx_assets_tags ON public.assets USING gin (tags jsonb_path_ops);

CREATE INDEX idx_models_canonical_version_rank ON public.models_canonical USING btree (version_rank);

CREATE INDEX usage_ledger_2026_06_request_id_idx ON public.usage_ledger_2026_06 USING btree (request_id);

CREATE INDEX credit_ledger_2026_08_ref_type_ref_id_idx ON public.credit_ledger_2026_08 USING btree (ref_type, ref_id);

CREATE INDEX idx_models_canonical_strengths ON public.models_canonical USING gin (strengths);

CREATE INDEX tool_usage_stats_2026_06_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (tenant_id, usage_date);

CREATE INDEX usage_ledger_2026_08_tenant_id_ts_idx ON public.usage_ledger_2026_08 USING btree (tenant_id, ts);

CREATE INDEX usage_ledger_2026_07_ts_idx ON public.usage_ledger_2026_07 USING btree (ts);

CREATE INDEX idx_models_canonical_released ON public.models_canonical USING btree (released_at DESC NULLS LAST);

CREATE INDEX idx_credit_ledger_tenant_ts ON public.credit_ledger_old USING btree (tenant_id, created_at DESC);

CREATE INDEX idx_wtmr_tier ON public.work_type_model_route USING btree (work_type_key, tier, weight DESC);

CREATE INDEX idx_agents_heartbeat ON public.agents USING btree (last_heartbeat) WHERE (last_heartbeat IS NOT NULL);

CREATE UNIQUE INDEX request_logs_2026_08_request_id_ts_idx ON public.request_logs_2026_08 USING btree (request_id, ts);

CREATE INDEX idx_armor_judgments_tenant_time ON public.armor_judgments USING btree (tenant_id, created_at DESC);

CREATE INDEX usage_ledger_2026_07_request_id_idx ON public.usage_ledger_2026_07 USING btree (request_id);

CREATE UNIQUE INDEX request_logs_default_request_id_ts_idx ON public.request_logs_default USING btree (request_id, ts);

CREATE INDEX tool_usage_stats_2026_08_created_at_idx ON public.tool_usage_stats_2026_08 USING btree (created_at);

CREATE INDEX usage_ledger_2026_07_tenant_id_ts_idx ON public.usage_ledger_2026_07 USING btree (tenant_id, ts);

CREATE INDEX idx_agents_tenant ON public.agents USING btree (tenant_id);

CREATE INDEX idx_tool_usage_stats_date ON public.tool_usage_stats_old USING btree (usage_date DESC);

CREATE INDEX idx_assets_tenant_kind ON public.assets USING btree (tenant_id, kind);

CREATE INDEX tool_usage_stats_2026_07_created_at_idx ON public.tool_usage_stats_2026_07 USING btree (created_at);

CREATE INDEX idx_tool_usage_stats_tenant_id ON public.tool_usage_stats_old USING btree (tenant_id);

CREATE INDEX idx_request_logs_2026_07_client_model_trgm ON public.request_logs_2026_07 USING gin (client_model public.gin_trgm_ops);

CREATE INDEX credit_ledger_2026_06_created_at_idx ON public.credit_ledger_2026_06 USING btree (created_at);

CREATE INDEX idx_armor_judgments_request ON public.armor_judgments USING btree (request_id);

CREATE UNIQUE INDEX request_logs_2026_06_request_id_ts_idx ON public.request_logs_2026_06 USING btree (request_id, ts);

CREATE INDEX credit_ledger_2026_07_created_at_idx ON public.credit_ledger_2026_07 USING btree (created_at);

CREATE INDEX tool_usage_stats_2026_07_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (usage_date);

CREATE INDEX usage_ledger_2026_06_ts_idx ON public.usage_ledger_2026_06 USING btree (ts);

CREATE INDEX tool_usage_stats_2026_06_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (usage_date);

CREATE INDEX idx_tenant_tool_policies_enabled ON public.tenant_tool_policies USING btree (enabled);

CREATE INDEX tool_usage_stats_2026_08_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (tenant_id, usage_date);

CREATE INDEX tool_usage_stats_2026_08_tool_id_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (tool_id, usage_date);

CREATE INDEX usage_ledger_2026_06_tenant_id_ts_idx ON public.usage_ledger_2026_06 USING btree (tenant_id, ts);

CREATE INDEX usage_ledger_2026_08_request_id_idx ON public.usage_ledger_2026_08 USING btree (request_id);

CREATE INDEX credit_ledger_2026_06_tenant_id_created_at_idx ON public.credit_ledger_2026_06 USING btree (tenant_id, created_at);

CREATE INDEX idx_armor_judgments_stats ON public.armor_judgments USING btree (check_type, decision);

CREATE INDEX idx_call_history_errors ON public.credential_model_call_history USING btree (credential_id, raw_model, window_start DESC) WHERE ((error_rate_limit_count > 0) OR (error_concurrent_count > 0));

CREATE INDEX idx_tool_usage_stats_tool_id ON public.tool_usage_stats_old USING btree (tool_id);

CREATE INDEX idx_request_logs_2026_08_client_model_trgm ON public.request_logs_2026_08 USING gin (client_model public.gin_trgm_ops);

CREATE INDEX tool_usage_stats_2026_06_tool_id_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (tool_id, usage_date);

CREATE INDEX tool_usage_stats_2026_07_tool_id_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (tool_id, usage_date);

CREATE INDEX tool_usage_stats_2026_08_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (usage_date);

