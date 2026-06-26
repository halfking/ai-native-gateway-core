--
-- PostgreSQL database dump
--

\restrict DecnQ5LcXZiA4Ym22zU9MfCPapcDGQ1BOMhox7mKLCvW69xTtslsAuWX5d1okz2

-- Dumped from database version 15.3 (Debian 15.3-1.pgdg120+1)
-- Dumped by pg_dump version 15.18 (Homebrew)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

DROP POLICY IF EXISTS tenant_isolation_users ON public.users;
DROP POLICY IF EXISTS tenant_isolation_tool_usage_stats ON public.tool_usage_stats_old;
DROP POLICY IF EXISTS tenant_isolation_tool_usage_stats ON public.tool_usage_stats;
DROP POLICY IF EXISTS tenant_isolation_tool_registry ON public.tool_registry;
DROP POLICY IF EXISTS tenant_isolation_tool_call_events ON public.tool_call_events;
DROP POLICY IF EXISTS tenant_isolation_tmp_audit ON public.tenant_model_policies_audit;
DROP POLICY IF EXISTS tenant_isolation_tmp ON public.tenant_model_policies;
DROP POLICY IF EXISTS tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies;
DROP POLICY IF EXISTS tenant_isolation_tenant_subscriptions ON public.tenant_subscriptions;
DROP POLICY IF EXISTS tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv;
DROP POLICY IF EXISTS tenant_isolation_tenant_credit_wallets ON public.tenant_credit_wallets;
DROP POLICY IF EXISTS tenant_isolation_settings_audit ON public.settings_audit;
DROP POLICY IF EXISTS tenant_isolation_request_logs ON public.request_logs;
DROP POLICY IF EXISTS tenant_isolation_credit_ledger ON public.credit_ledger_old;
DROP POLICY IF EXISTS tenant_isolation_billing_orders ON public.billing_orders;
DROP POLICY IF EXISTS tenant_isolation_assets ON public.assets;
DROP POLICY IF EXISTS tenant_isolation_asset_relationships ON public.asset_relationships;
DROP POLICY IF EXISTS tenant_isolation_armor_judgments ON public.armor_judgments;
DROP POLICY IF EXISTS tenant_isolation_agents ON public.agents;
DROP POLICY IF EXISTS tenant_isolation_agent_relationships ON public.agent_relationships;
ALTER TABLE IF EXISTS ONLY public.asset_relationships DROP CONSTRAINT IF EXISTS fk_asset_rel_src;
ALTER TABLE IF EXISTS ONLY public.asset_relationships DROP CONSTRAINT IF EXISTS fk_asset_rel_dst;
ALTER TABLE IF EXISTS ONLY public.agent_relationships DROP CONSTRAINT IF EXISTS fk_agent_rel_src;
ALTER TABLE IF EXISTS ONLY public.agent_relationships DROP CONSTRAINT IF EXISTS fk_agent_rel_dst;
DROP TRIGGER IF EXISTS trigger_provider_settings_updated_at ON public.provider_settings;
DROP TRIGGER IF EXISTS trg_update_api_key_model_cost ON public.request_logs;
DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON public.credentials;
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_notify_auto_route_apikeys ON public.api_keys;
DROP TRIGGER IF EXISTS trg_key_applications_updated_at ON public.key_applications;
DROP TRIGGER IF EXISTS trg_check_credential_dates ON public.credentials;
DROP TRIGGER IF EXISTS trg_auto_fp_slot_limit_insert ON public.credentials;
DROP TRIGGER IF EXISTS tenant_model_policies_audit_trg ON public.tenant_model_policies;
DROP TRIGGER IF EXISTS routing_overrides_audit_trg ON public.routing_overrides;
DROP TRIGGER IF EXISTS model_offers_update ON public.model_offers;
DROP TRIGGER IF EXISTS model_offers_insert ON public.model_offers;
DROP TRIGGER IF EXISTS model_offers_delete ON public.model_offers;
DROP TRIGGER IF EXISTS cmb_protect_manual_disable ON public.credential_model_bindings;
DROP INDEX IF EXISTS public.idx_wtmr_work_type;
DROP INDEX IF EXISTS public.idx_wtmr_tier;
DROP INDEX IF EXISTS public.idx_work_type_config_l1;
DROP INDEX IF EXISTS public.idx_work_type_config_category;
DROP INDEX IF EXISTS public.idx_wal_tenant_created;
DROP INDEX IF EXISTS public.idx_wal_status_stage;
DROP INDEX IF EXISTS public.idx_wal_session;
DROP INDEX IF EXISTS public.idx_users_username;
DROP INDEX IF EXISTS public.idx_users_tenant;
DROP INDEX IF EXISTS public.idx_usage_ledger_part_ts;
DROP INDEX IF EXISTS public.idx_usage_ledger_part_tenant;
DROP INDEX IF EXISTS public.idx_usage_ledger_part_request_id;
DROP INDEX IF EXISTS public.idx_tuning_signals_task_ts;
DROP INDEX IF EXISTS public.idx_tuning_signals_strategy_ts;
DROP INDEX IF EXISTS public.idx_tuning_signals_strategy_task;
DROP INDEX IF EXISTS public.idx_tuning_signals_session;
DROP INDEX IF EXISTS public.idx_tuning_signals_lowq;
DROP INDEX IF EXISTS public.idx_tuning_signals_daily_task_ts;
DROP INDEX IF EXISTS public.idx_tuning_signals_daily_pk;
DROP INDEX IF EXISTS public.idx_tuning_signals_5m_task_ts;
DROP INDEX IF EXISTS public.idx_tuning_signals_5m_pk;
DROP INDEX IF EXISTS public.idx_tuning_proposals_status;
DROP INDEX IF EXISTS public.idx_tuning_proposals_created;
DROP INDEX IF EXISTS public.idx_tuning_proposals_cat;
DROP INDEX IF EXISTS public.idx_tool_usage_stats_tool_tenant;
DROP INDEX IF EXISTS public.idx_tool_usage_stats_tool_id;
DROP INDEX IF EXISTS public.idx_tool_usage_stats_tenant_id;
DROP INDEX IF EXISTS public.idx_tool_usage_stats_date;
DROP INDEX IF EXISTS public.idx_tool_stats_part_tool;
DROP INDEX IF EXISTS public.idx_tool_stats_part_tenant;
DROP INDEX IF EXISTS public.idx_tool_stats_part_date;
DROP INDEX IF EXISTS public.idx_tool_stats_part_created;
DROP INDEX IF EXISTS public.idx_tool_registry_unique_version;
DROP INDEX IF EXISTS public.idx_tool_registry_tenant_tool;
DROP INDEX IF EXISTS public.idx_tool_registry_name;
DROP INDEX IF EXISTS public.idx_tool_registry_deprecation;
DROP INDEX IF EXISTS public.idx_tool_registry_category;
DROP INDEX IF EXISTS public.idx_tool_categories_order;
DROP INDEX IF EXISTS public.idx_tmp_tenant_active;
DROP INDEX IF EXISTS public.idx_tmp_canonical;
DROP INDEX IF EXISTS public.idx_tmp_audit_ts;
DROP INDEX IF EXISTS public.idx_tmp_audit_tenant_ts;
DROP INDEX IF EXISTS public.idx_tenants_status;
DROP INDEX IF EXISTS public.idx_tenants_name;
DROP INDEX IF EXISTS public.idx_tenant_tool_policies_tenant;
DROP INDEX IF EXISTS public.idx_tenant_tool_policies_enabled;
DROP INDEX IF EXISTS public.idx_tenant_subscriptions_tenant;
DROP INDEX IF EXISTS public.idx_tenant_settings_kv_tenant;
DROP INDEX IF EXISTS public.idx_tenant_settings_kv_category;
DROP INDEX IF EXISTS public.idx_settings_kv_updated;
DROP INDEX IF EXISTS public.idx_settings_kv_scope;
DROP INDEX IF EXISTS public.idx_settings_kv_category;
DROP INDEX IF EXISTS public.idx_settings_audit_tenant_time;
DROP INDEX IF EXISTS public.idx_settings_audit_operator;
DROP INDEX IF EXISTS public.idx_settings_audit_key_time;
DROP INDEX IF EXISTS public.idx_settings_audit_created;
DROP INDEX IF EXISTS public.idx_session_titles_generated_at;
DROP INDEX IF EXISTS public.idx_session_memora_extraction_at;
DROP INDEX IF EXISTS public.idx_routing_overrides_unique;
DROP INDEX IF EXISTS public.idx_routing_overrides_task_profile;
DROP INDEX IF EXISTS public.idx_routing_overrides_expires;
DROP INDEX IF EXISTS public.idx_routing_overrides_audit_ts;
DROP INDEX IF EXISTS public.idx_routing_overrides_audit_override_ts;
DROP INDEX IF EXISTS public.idx_routing_overrides_audit_actor_ts;
DROP INDEX IF EXISTS public.idx_request_logs_work_type;
DROP INDEX IF EXISTS public.idx_request_logs_upstream_finish_reason;
DROP INDEX IF EXISTS public.idx_request_logs_tool_calls;
DROP INDEX IF EXISTS public.idx_request_logs_tenant_task_ts;
DROP INDEX IF EXISTS public.idx_request_logs_status_ts;
DROP INDEX IF EXISTS public.idx_request_logs_session_outbound;
DROP INDEX IF EXISTS public.idx_request_logs_request_id_ts_unique;
DROP INDEX IF EXISTS public.idx_request_logs_request_id_unique;
DROP INDEX IF EXISTS public.idx_request_logs_quality_flags;
DROP INDEX IF EXISTS public.idx_request_logs_provider_tool_calls;
DROP INDEX IF EXISTS public.idx_request_logs_provider_quality;
DROP INDEX IF EXISTS public.idx_request_logs_parent_ts;
DROP INDEX IF EXISTS public.idx_request_logs_outbound_msg_count;
DROP INDEX IF EXISTS public.idx_request_logs_gw_task_ts;
DROP INDEX IF EXISTS public.idx_request_logs_gw_session_ts;
DROP INDEX IF EXISTS public.idx_request_logs_credits_charged;
DROP INDEX IF EXISTS public.idx_request_logs_client_model_trgm;
DROP INDEX IF EXISTS public.idx_provider_settings_provider;
DROP INDEX IF EXISTS public.idx_provider_settings_key;
DROP INDEX IF EXISTS public.idx_provider_quality_rollup_bucket;
DROP INDEX IF EXISTS public.idx_passive_probe_reviewing;
DROP INDEX IF EXISTS public.idx_mps_due;
DROP INDEX IF EXISTS public.idx_models_canonical_version_rank;
DROP INDEX IF EXISTS public.idx_models_canonical_strengths;
DROP INDEX IF EXISTS public.idx_models_canonical_released;
DROP INDEX IF EXISTS public.idx_model_probe_state_retry;
DROP INDEX IF EXISTS public.idx_credit_ledger_tenant_ts;
DROP INDEX IF EXISTS public.idx_credentials_auto_limit;
DROP INDEX IF EXISTS public.idx_cmb_unavailable_recover_at;
DROP INDEX IF EXISTS public.idx_call_history_model_time;
DROP INDEX IF EXISTS public.idx_call_history_errors;
DROP INDEX IF EXISTS public.idx_call_history_cred_time;
DROP INDEX IF EXISTS public.idx_billing_orders_tenant;
DROP INDEX IF EXISTS public.idx_billing_orders_status;
DROP INDEX IF EXISTS public.idx_assets_tenant_kind;
DROP INDEX IF EXISTS public.idx_assets_tags;
DROP INDEX IF EXISTS public.idx_asset_rel_src;
DROP INDEX IF EXISTS public.idx_asset_rel_dst;
DROP INDEX IF EXISTS public.idx_armor_judgments_tenant_time;
DROP INDEX IF EXISTS public.idx_armor_judgments_stats;
DROP INDEX IF EXISTS public.idx_armor_judgments_request;
DROP INDEX IF EXISTS public.idx_applications_tenant_code;
DROP INDEX IF EXISTS public.idx_agents_tenant;
DROP INDEX IF EXISTS public.idx_agents_kind;
DROP INDEX IF EXISTS public.idx_agents_heartbeat;
DROP INDEX IF EXISTS public.idx_agents_capabilities;
DROP INDEX IF EXISTS public.idx_agent_rel_src;
DROP INDEX IF EXISTS public.idx_agent_rel_dst;
DROP INDEX IF EXISTS public.idx_credit_ledger_part_tenant;
DROP INDEX IF EXISTS public.idx_credit_ledger_part_ref;
DROP INDEX IF EXISTS public.idx_credit_ledger_part_created;
ALTER TABLE IF EXISTS ONLY public.work_type_model_route DROP CONSTRAINT IF EXISTS work_type_model_route_work_type_key_canonical_name_key;
ALTER TABLE IF EXISTS ONLY public.work_type_model_route DROP CONSTRAINT IF EXISTS work_type_model_route_pkey;
ALTER TABLE IF EXISTS ONLY public.work_type_config DROP CONSTRAINT IF EXISTS work_type_config_pkey;
ALTER TABLE IF EXISTS ONLY public.users DROP CONSTRAINT IF EXISTS users_username_key;
ALTER TABLE IF EXISTS ONLY public.users DROP CONSTRAINT IF EXISTS users_pkey;
ALTER TABLE IF EXISTS ONLY public.usage_ledger_old DROP CONSTRAINT IF EXISTS usage_ledger_pkey;
ALTER TABLE IF EXISTS ONLY public.usage_ledger_2026_08 DROP CONSTRAINT IF EXISTS usage_ledger_2026_08_request_id_ts_key;
ALTER TABLE IF EXISTS ONLY public.usage_ledger_2026_07 DROP CONSTRAINT IF EXISTS usage_ledger_2026_07_request_id_ts_key;
ALTER TABLE IF EXISTS ONLY public.usage_ledger_2026_06 DROP CONSTRAINT IF EXISTS usage_ledger_2026_06_request_id_ts_key;
ALTER TABLE IF EXISTS ONLY public.usage_ledger DROP CONSTRAINT IF EXISTS usage_ledger_partitioned_request_id_ts_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_old DROP CONSTRAINT IF EXISTS uk_tool_usage_stats;
ALTER TABLE IF EXISTS ONLY public.tenant_tool_policies DROP CONSTRAINT IF EXISTS uk_tenant_tool_policy;
ALTER TABLE IF EXISTS ONLY public.topup_packages DROP CONSTRAINT IF EXISTS topup_packages_code_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_08 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_08 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_08_pkey;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_07 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_07 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_07_pkey;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_06 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_06_tool_id_tenant_id_usage_date_creat_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats DROP CONSTRAINT IF EXISTS tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats_2026_06 DROP CONSTRAINT IF EXISTS tool_usage_stats_2026_06_pkey;
ALTER TABLE IF EXISTS ONLY public.tool_usage_stats DROP CONSTRAINT IF EXISTS tool_usage_stats_partitioned_pkey;
ALTER TABLE IF EXISTS ONLY public.tool_registry DROP CONSTRAINT IF EXISTS tool_registry_tool_name_key;
ALTER TABLE IF EXISTS ONLY public.tenants DROP CONSTRAINT IF EXISTS tenants_pkey;
ALTER TABLE IF EXISTS ONLY public.tenant_model_policies DROP CONSTRAINT IF EXISTS tenant_model_policies_tenant_id_canonical_name_key;
ALTER TABLE IF EXISTS ONLY public.system_identity_pool DROP CONSTRAINT IF EXISTS system_identity_pool_pkey;
ALTER TABLE IF EXISTS ONLY public.subscription_plans DROP CONSTRAINT IF EXISTS subscription_plans_code_key;
ALTER TABLE IF EXISTS ONLY public.request_wal_2026_07 DROP CONSTRAINT IF EXISTS request_wal_2026_07_pkey;
ALTER TABLE IF EXISTS ONLY public.request_wal_2026_06 DROP CONSTRAINT IF EXISTS request_wal_2026_06_pkey;
ALTER TABLE IF EXISTS ONLY public.request_wal DROP CONSTRAINT IF EXISTS request_wal_pkey;
ALTER TABLE IF EXISTS ONLY public.providers DROP CONSTRAINT IF EXISTS providers_tenant_id_code_key;
ALTER TABLE IF EXISTS ONLY public.providers DROP CONSTRAINT IF EXISTS providers_pkey;
ALTER TABLE IF EXISTS ONLY public.provider_settings DROP CONSTRAINT IF EXISTS provider_settings_unique_key;
ALTER TABLE IF EXISTS ONLY public.provider_quality_rollup DROP CONSTRAINT IF EXISTS provider_quality_rollup_pkey;
ALTER TABLE IF EXISTS ONLY public.provider_models DROP CONSTRAINT IF EXISTS provider_models_unique_provider_model;
ALTER TABLE IF EXISTS ONLY public.provider_header_profiles DROP CONSTRAINT IF EXISTS provider_header_profiles_profile_code_key;
ALTER TABLE IF EXISTS ONLY public.assets DROP CONSTRAINT IF EXISTS pk_assets;
ALTER TABLE IF EXISTS ONLY public.asset_relationships DROP CONSTRAINT IF EXISTS pk_asset_relationships;
ALTER TABLE IF EXISTS ONLY public.agent_relationships DROP CONSTRAINT IF EXISTS pk_agent_relationships;
ALTER TABLE IF EXISTS ONLY public.passive_probe_state DROP CONSTRAINT IF EXISTS passive_probe_state_pkey;
ALTER TABLE IF EXISTS ONLY public.models_canonical DROP CONSTRAINT IF EXISTS models_canonical_canonical_name_key;
ALTER TABLE IF EXISTS ONLY public.model_task_index DROP CONSTRAINT IF EXISTS model_task_index_bucket_canonical_task_key;
ALTER TABLE IF EXISTS ONLY public.model_probe_state DROP CONSTRAINT IF EXISTS model_probe_state_pkey;
ALTER TABLE IF EXISTS ONLY public.model_offers_legacy DROP CONSTRAINT IF EXISTS model_offers_credential_id_raw_model_name_key;
ALTER TABLE IF EXISTS ONLY public.model_fingerprints DROP CONSTRAINT IF EXISTS model_fingerprints_credential_id_canonical_id_key;
ALTER TABLE IF EXISTS ONLY public.maas_settings DROP CONSTRAINT IF EXISTS maas_settings_pkey;
ALTER TABLE IF EXISTS ONLY public.local_runtimes DROP CONSTRAINT IF EXISTS local_runtimes_host_code_runtime_type_base_url_key;
ALTER TABLE IF EXISTS ONLY public.local_models DROP CONSTRAINT IF EXISTS local_models_runtime_id_raw_name_key;
ALTER TABLE IF EXISTS ONLY public.credit_ledger_2026_08 DROP CONSTRAINT IF EXISTS credit_ledger_2026_08_pkey;
ALTER TABLE IF EXISTS ONLY public.credit_ledger_2026_07 DROP CONSTRAINT IF EXISTS credit_ledger_2026_07_pkey;
ALTER TABLE IF EXISTS ONLY public.credit_ledger_2026_06 DROP CONSTRAINT IF EXISTS credit_ledger_2026_06_pkey;
ALTER TABLE IF EXISTS ONLY public.credit_ledger DROP CONSTRAINT IF EXISTS credit_ledger_partitioned_pkey;
ALTER TABLE IF EXISTS ONLY public.credentials DROP CONSTRAINT IF EXISTS credentials_unique_provider_label;
ALTER TABLE IF EXISTS ONLY public.credentials DROP CONSTRAINT IF EXISTS credentials_pkey;
ALTER TABLE IF EXISTS ONLY public.credential_quotas DROP CONSTRAINT IF EXISTS credential_quotas_credential_id_quota_name_key;
ALTER TABLE IF EXISTS ONLY public.credential_quota_usage DROP CONSTRAINT IF EXISTS credential_quota_usage_quota_id_window_started_at_key;
ALTER TABLE IF EXISTS ONLY public.credential_model_index DROP CONSTRAINT IF EXISTS credential_model_index_bucket_cred_model_key;
ALTER TABLE IF EXISTS ONLY public.credential_model_call_history DROP CONSTRAINT IF EXISTS credential_model_call_history_pkey;
ALTER TABLE IF EXISTS ONLY public.credential_capabilities DROP CONSTRAINT IF EXISTS credential_capabilities_credential_id_capability_key;
ALTER TABLE IF EXISTS ONLY public.credential_model_bindings DROP CONSTRAINT IF EXISTS cmb_unique_credential_model;
ALTER TABLE IF EXISTS ONLY public.billing_orders DROP CONSTRAINT IF EXISTS billing_orders_order_no_key;
ALTER TABLE IF EXISTS ONLY public.armor_judgments DROP CONSTRAINT IF EXISTS armor_judgments_pkey;
ALTER TABLE IF EXISTS ONLY public.applications DROP CONSTRAINT IF EXISTS applications_tenant_id_code_key;
ALTER TABLE IF EXISTS ONLY public.applications DROP CONSTRAINT IF EXISTS applications_pkey;
ALTER TABLE IF EXISTS ONLY public.api_keys DROP CONSTRAINT IF EXISTS api_keys_pkey;
ALTER TABLE IF EXISTS ONLY public.api_keys DROP CONSTRAINT IF EXISTS api_keys_key_hash_key;
ALTER TABLE IF EXISTS ONLY public.agents DROP CONSTRAINT IF EXISTS agents_pkey;
ALTER TABLE IF EXISTS public.work_type_model_route ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.users ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tuning_signals ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tuning_proposals ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.topup_packages ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tool_usage_stats_old ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tool_usage_stats ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tool_registry ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.token_audit_events ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tenant_tool_policies ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tenant_subscriptions ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tenant_model_policies_audit ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.tenant_model_policies ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.subscription_plans ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.settings_audit ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.security_audit_log ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.routing_overrides_audit ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.routing_overrides ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.routing_audit_log ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.route_decisions ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.providers ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.provider_settings ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.provider_scores ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.provider_models ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.provider_header_profiles ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.pricing_refresh_log ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.pricing_plans ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.ops_model_offers_backup ALTER COLUMN backup_id DROP DEFAULT;
ALTER TABLE IF EXISTS public.models_canonical ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_reconcile_log ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_offers_legacy ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_lifecycle_jobs ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_fingerprints ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_discovery_runs ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.model_aliases ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.local_runtimes ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.local_models ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credit_ledger_old ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credit_ledger ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credentials ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credential_quotas ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credential_quota_usage ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credential_model_bindings ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credential_health_checks ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.credential_capabilities ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.billing_orders ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.background_tasks ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.auto_tune_audit ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.armor_judgments ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.applications ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.api_keys ALTER COLUMN id DROP DEFAULT;
ALTER TABLE IF EXISTS public.agents ALTER COLUMN id DROP DEFAULT;
DROP SEQUENCE IF EXISTS public.work_type_model_route_id_seq;
DROP TABLE IF EXISTS public.work_type_model_route;
DROP TABLE IF EXISTS public.work_type_config;
DROP VIEW IF EXISTS public.v_routable_credential_models;
DROP VIEW IF EXISTS public.v_idle_credential_slots;
DROP VIEW IF EXISTS public.v_fp_slot_policy;
DROP SEQUENCE IF EXISTS public.users_id_seq;
DROP TABLE IF EXISTS public.users;
DROP TABLE IF EXISTS public.usage_minute;
DROP TABLE IF EXISTS public.usage_ledger_old;
DROP TABLE IF EXISTS public.usage_ledger_2026_08;
DROP TABLE IF EXISTS public.usage_ledger_2026_07;
DROP TABLE IF EXISTS public.usage_ledger_2026_06;
DROP TABLE IF EXISTS public.usage_ledger;
DROP SEQUENCE IF EXISTS public.tuning_signals_id_seq;
DROP MATERIALIZED VIEW IF EXISTS public.tuning_signals_daily;
DROP MATERIALIZED VIEW IF EXISTS public.tuning_signals_5m;
DROP TABLE IF EXISTS public.tuning_signals;
DROP SEQUENCE IF EXISTS public.tuning_proposals_id_seq;
DROP TABLE IF EXISTS public.tuning_proposals;
DROP TABLE IF EXISTS public.tuning_params;
DROP SEQUENCE IF EXISTS public.topup_packages_id_seq;
DROP TABLE IF EXISTS public.topup_packages;
DROP SEQUENCE IF EXISTS public.tool_usage_stats_id_seq;
DROP TABLE IF EXISTS public.tool_usage_stats_old;
DROP TABLE IF EXISTS public.tool_usage_stats_2026_08;
DROP TABLE IF EXISTS public.tool_usage_stats_2026_07;
DROP TABLE IF EXISTS public.tool_usage_stats_2026_06;
DROP SEQUENCE IF EXISTS public.tool_usage_stats_partitioned_id_seq;
DROP TABLE IF EXISTS public.tool_usage_stats;
DROP SEQUENCE IF EXISTS public.tool_registry_id_seq;
DROP TABLE IF EXISTS public.tool_registry;
DROP TABLE IF EXISTS public.tool_categories;
DROP TABLE IF EXISTS public.tool_call_events;
DROP SEQUENCE IF EXISTS public.token_audit_events_id_seq;
DROP TABLE IF EXISTS public.token_audit_events;
DROP TABLE IF EXISTS public.test_columnar_new;
DROP TABLE IF EXISTS public.tenants;
DROP SEQUENCE IF EXISTS public.tenant_tool_policies_id_seq;
DROP TABLE IF EXISTS public.tenant_tool_policies;
DROP SEQUENCE IF EXISTS public.tenant_subscriptions_id_seq;
DROP TABLE IF EXISTS public.tenant_subscriptions;
DROP TABLE IF EXISTS public.tenant_settings_kv;
DROP SEQUENCE IF EXISTS public.tenant_model_policies_id_seq;
DROP SEQUENCE IF EXISTS public.tenant_model_policies_audit_id_seq;
DROP TABLE IF EXISTS public.tenant_model_policies_audit;
DROP VIEW IF EXISTS public.tenant_model_policies_active;
DROP TABLE IF EXISTS public.tenant_model_policies;
DROP TABLE IF EXISTS public.tenant_credit_wallets;
DROP TABLE IF EXISTS public.system_identity_pool;
DROP SEQUENCE IF EXISTS public.subscription_plans_id_seq;
DROP TABLE IF EXISTS public.subscription_plans;
DROP TABLE IF EXISTS public.sticky_sessions;
DROP TABLE IF EXISTS public.settings_kv;
DROP SEQUENCE IF EXISTS public.settings_audit_id_seq;
DROP TABLE IF EXISTS public.settings_audit;
DROP TABLE IF EXISTS public.session_titles;
DROP TABLE IF EXISTS public.session_memora_extraction_log;
DROP SEQUENCE IF EXISTS public.security_audit_log_id_seq;
DROP TABLE IF EXISTS public.security_audit_log;
DROP TABLE IF EXISTS public.schema_migrations;
DROP TABLE IF EXISTS public.schema_migration_audit;
DROP TABLE IF EXISTS public.routing_policy;
DROP SEQUENCE IF EXISTS public.routing_overrides_id_seq;
DROP SEQUENCE IF EXISTS public.routing_overrides_audit_id_seq;
DROP TABLE IF EXISTS public.routing_overrides_audit;
DROP TABLE IF EXISTS public.routing_overrides;
DROP TABLE IF EXISTS public.routing_decision_log;
DROP SEQUENCE IF EXISTS public.routing_audit_log_id_seq;
DROP TABLE IF EXISTS public.routing_audit_log;
DROP SEQUENCE IF EXISTS public.route_decisions_id_seq;
DROP TABLE IF EXISTS public.route_decisions;
DROP TABLE IF EXISTS public.request_wal_bodies;
DROP TABLE IF EXISTS public.request_wal_2026_07;
DROP TABLE IF EXISTS public.request_wal_2026_06;
DROP TABLE IF EXISTS public.request_wal;
DROP TABLE IF EXISTS public.request_logs_default;
DROP VIEW IF EXISTS public.request_logs_all;
DROP TABLE IF EXISTS public.request_logs_archive;
DROP TABLE IF EXISTS public.request_logs_2026_08;
DROP TABLE IF EXISTS public.request_logs_2026_07;
DROP TABLE IF EXISTS public.request_logs_2026_06;
DROP TABLE IF EXISTS public.request_envelope;
DROP SEQUENCE IF EXISTS public.providers_id_seq;
DROP TABLE IF EXISTS public.providers;
DROP SEQUENCE IF EXISTS public.provider_settings_id_seq;
DROP TABLE IF EXISTS public.provider_settings;
DROP SEQUENCE IF EXISTS public.provider_scores_id_seq;
DROP TABLE IF EXISTS public.provider_scores;
DROP TABLE IF EXISTS public.provider_quality_rollup;
DROP SEQUENCE IF EXISTS public.provider_models_id_seq;
DROP SEQUENCE IF EXISTS public.provider_header_profiles_id_seq;
DROP TABLE IF EXISTS public.provider_header_profiles;
DROP TABLE IF EXISTS public.provider_events;
DROP TABLE IF EXISTS public.provider_catalog;
DROP SEQUENCE IF EXISTS public.pricing_refresh_log_id_seq;
DROP TABLE IF EXISTS public.pricing_refresh_log;
DROP SEQUENCE IF EXISTS public.pricing_plans_id_seq;
DROP TABLE IF EXISTS public.pricing_plans;
DROP TABLE IF EXISTS public.price_change_events;
DROP TABLE IF EXISTS public.passive_probe_state;
DROP SEQUENCE IF EXISTS public.ops_model_offers_backup_backup_id_seq;
DROP TABLE IF EXISTS public.ops_model_offers_backup;
DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;
DROP TABLE IF EXISTS public.models_canonical;
DROP TABLE IF EXISTS public.model_task_index;
DROP SEQUENCE IF EXISTS public.model_reconcile_log_id_seq;
DROP TABLE IF EXISTS public.model_reconcile_log;
DROP TABLE IF EXISTS public.model_probe_state;
DROP TABLE IF EXISTS public.model_probe_runs;
DROP SEQUENCE IF EXISTS public.model_offers_id_seq;
DROP TABLE IF EXISTS public.model_offers_legacy;
DROP VIEW IF EXISTS public.model_offers;
DROP TABLE IF EXISTS public.provider_models;
DROP TABLE IF EXISTS public.model_offer_events;
DROP SEQUENCE IF EXISTS public.model_lifecycle_jobs_id_seq;
DROP TABLE IF EXISTS public.model_lifecycle_jobs;
DROP SEQUENCE IF EXISTS public.model_fingerprints_id_seq;
DROP TABLE IF EXISTS public.model_fingerprints;
DROP TABLE IF EXISTS public.model_families;
DROP SEQUENCE IF EXISTS public.model_discovery_runs_id_seq;
DROP TABLE IF EXISTS public.model_discovery_runs;
DROP TABLE IF EXISTS public.model_credit_rates;
DROP VIEW IF EXISTS public.model_cost_per_task_view;
DROP SEQUENCE IF EXISTS public.model_aliases_id_seq;
DROP TABLE IF EXISTS public.model_aliases;
DROP TABLE IF EXISTS public.maas_settings;
DROP SEQUENCE IF EXISTS public.local_runtimes_id_seq;
DROP TABLE IF EXISTS public.local_runtimes;
DROP SEQUENCE IF EXISTS public.local_models_id_seq;
DROP TABLE IF EXISTS public.local_models;
DROP TABLE IF EXISTS public.key_rpm_daily;
DROP TABLE IF EXISTS public.key_applications;
DROP TABLE IF EXISTS public.internal_service_keys;
DROP VIEW IF EXISTS public.customer_cost_view;
DROP TABLE IF EXISTS public.request_logs;
DROP SEQUENCE IF EXISTS public.request_logs_id_seq;
DROP SEQUENCE IF EXISTS public.credit_ledger_id_seq;
DROP TABLE IF EXISTS public.credit_ledger_old;
DROP TABLE IF EXISTS public.credit_ledger_2026_08;
DROP TABLE IF EXISTS public.credit_ledger_2026_07;
DROP TABLE IF EXISTS public.credit_ledger_2026_06;
DROP SEQUENCE IF EXISTS public.credit_ledger_partitioned_id_seq;
DROP TABLE IF EXISTS public.credit_ledger;
DROP SEQUENCE IF EXISTS public.credentials_id_seq;
DROP TABLE IF EXISTS public.credentials;
DROP SEQUENCE IF EXISTS public.credential_quotas_id_seq;
DROP TABLE IF EXISTS public.credential_quotas;
DROP SEQUENCE IF EXISTS public.credential_quota_usage_id_seq;
DROP TABLE IF EXISTS public.credential_quota_usage;
DROP TABLE IF EXISTS public.credential_probe_model_log;
DROP TABLE IF EXISTS public.credential_model_weekly_peak;
DROP TABLE IF EXISTS public.credential_model_stats_1m;
DROP TABLE IF EXISTS public.credential_model_peak_1m;
DROP TABLE IF EXISTS public.credential_model_index;
DROP TABLE IF EXISTS public.credential_model_call_history;
DROP SEQUENCE IF EXISTS public.credential_model_bindings_id_seq;
DROP TABLE IF EXISTS public.credential_model_bindings;
DROP SEQUENCE IF EXISTS public.credential_health_checks_id_seq;
DROP TABLE IF EXISTS public.credential_health_checks;
DROP SEQUENCE IF EXISTS public.credential_capabilities_id_seq;
DROP TABLE IF EXISTS public.credential_capabilities;
DROP TABLE IF EXISTS public.candidate_failure_logs;
DROP SEQUENCE IF EXISTS public.billing_orders_id_seq;
DROP TABLE IF EXISTS public.billing_orders;
DROP SEQUENCE IF EXISTS public.background_tasks_id_seq;
DROP TABLE IF EXISTS public.background_tasks;
DROP SEQUENCE IF EXISTS public.auto_tune_audit_id_seq;
DROP TABLE IF EXISTS public.auto_tune_audit;
DROP TABLE IF EXISTS public.assets;
DROP TABLE IF EXISTS public.asset_relationships;
DROP SEQUENCE IF EXISTS public.armor_judgments_id_seq;
DROP TABLE IF EXISTS public.armor_judgments;
DROP SEQUENCE IF EXISTS public.applications_id_seq;
DROP TABLE IF EXISTS public.applications;
DROP SEQUENCE IF EXISTS public.api_keys_id_seq;
DROP TABLE IF EXISTS public.api_keys;
DROP TABLE IF EXISTS public.api_key_model_cost;
DROP TABLE IF EXISTS public.api_key_auto_profile;
DROP SEQUENCE IF EXISTS public.agents_id_seq;
DROP TABLE IF EXISTS public.agents;
DROP TABLE IF EXISTS public.agent_relationships;
DROP FUNCTION IF EXISTS public.update_provider_settings_updated_at();
DROP FUNCTION IF EXISTS public.update_api_key_model_cost();
DROP FUNCTION IF EXISTS public.trg_cmb_protect_manual_disable();
DROP FUNCTION IF EXISTS public.tenant_model_policies_audit_fn();
DROP FUNCTION IF EXISTS public.routing_overrides_audit_fn();
DROP FUNCTION IF EXISTS public.recent_success_rate(p_credential_id bigint, p_raw_model text, p_sample_n integer, p_window_hours integer);
DROP FUNCTION IF EXISTS public.notify_auto_route_refresh();
DROP FUNCTION IF EXISTS public.model_probe_reclaim_idle_slots(reclaim_after_seconds integer);
DROP FUNCTION IF EXISTS public.model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone);
DROP FUNCTION IF EXISTS public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone);
DROP FUNCTION IF EXISTS public.model_probe_backoff(consecutive_failures integer);
DROP FUNCTION IF EXISTS public.model_offers_update_trigger();
DROP FUNCTION IF EXISTS public.model_offers_insert_trigger();
DROP FUNCTION IF EXISTS public.model_offers_delete_trigger();
DROP FUNCTION IF EXISTS public.key_applications_set_updated_at();
DROP FUNCTION IF EXISTS public.get_current_tenant();
DROP FUNCTION IF EXISTS public.ensure_request_logs_partition(target_ts timestamp with time zone);
DROP FUNCTION IF EXISTS public.create_next_month_partitions();
DROP FUNCTION IF EXISTS public.check_credential_dates();
DROP FUNCTION IF EXISTS public.auto_set_fp_slot_limit();
DROP FUNCTION IF EXISTS public.archive_request_logs(archive_month date);
DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS btree_gist;
DROP EXTENSION IF EXISTS citus_columnar;
--
-- Name: citus_columnar; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog;


--
-- Name: EXTENSION citus_columnar; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION citus_columnar IS 'Citus Columnar extension';


--
-- Name: btree_gist; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public;


--
-- Name: EXTENSION btree_gist; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION btree_gist IS 'support for indexing common datatypes in GiST';


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: archive_request_logs(date); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.archive_request_logs(archive_month date) RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    partition_existed boolean := false;
    col_list    text;
BEGIN
    -- Check if source partition exists
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    partition_existed := true;

    -- Create archive partition if not exists (with columnar storage)
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
        RAISE NOTICE 'Created archive partition: %', dst_part;
    END IF;

    -- Build explicit column list: only columns that exist in BOTH source and archive
    -- (archive's ordinal order; intersection of source and archive columns)
    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_logs_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_logs_archive', src_part;
    END IF;

    -- Migrate data using explicit column list (safe even if column orders differ)
    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;
    RAISE NOTICE 'Migrated % rows from % to % (column-aware)', row_count, src_part, dst_part;

    -- Drop source partition (releases space)
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);
    RAISE NOTICE 'Dropped source partition: %', src_part;

    RETURN QUERY SELECT 'success'::text, row_count, partition_existed;
END;
$$;


--
-- Name: FUNCTION archive_request_logs(archive_month date); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.archive_request_logs(archive_month date) IS 'Archive one month of request_logs into request_logs_archive (columnar). Column-aware: uses explicit column list, robust against column-order differences between source and target partitions. Added 2026-06-26 to fix INSERT ... SELECT * bug (local-discovery).';


--
-- Name: auto_set_fp_slot_limit(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.auto_set_fp_slot_limit() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- Auto-fill fp_slot_limit from concurrency_limit if not explicitly set
    IF NEW.fp_slot_limit IS NULL THEN
        IF NEW.concurrency_limit IS NOT NULL AND NEW.concurrency_limit > 0 THEN
            NEW.fp_slot_limit := GREATEST(1, NEW.concurrency_limit / 4);
        ELSE
            NEW.fp_slot_limit := 20;  -- 2026-06-24: 5→20, matches DefaultDefaultLimit
        END IF;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: check_credential_dates(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_credential_dates() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.effective_at IS NOT NULL AND NEW.expires_at IS NOT NULL THEN
        IF NEW.expires_at <= NEW.effective_at THEN
            RAISE EXCEPTION 'expires_at must be greater than effective_at';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: create_next_month_partitions(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_next_month_partitions() RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_month_start date;
    next_month_end date;
    month_suffix text;
    result text := '';
BEGIN
    next_month_start := date_trunc('month', now() + interval '1 month');
    next_month_end := date_trunc('month', now() + interval '2 months');
    month_suffix := to_char(next_month_start, 'YYYY_MM');
    
    -- usage_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS usage_ledger_%s
        PARTITION OF usage_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'usage_ledger_' || month_suffix || ', ';
    
    -- credit_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS credit_ledger_%s
        PARTITION OF credit_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'credit_ledger_' || month_suffix || ', ';
    
    -- tool_usage_stats
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS tool_usage_stats_%s
        PARTITION OF tool_usage_stats
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'tool_usage_stats_' || month_suffix || ', ';
    
    -- request_logs
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS request_logs_%s
        PARTITION OF request_logs
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'request_logs_' || month_suffix;
    
    RETURN '✅ Created partitions for ' || month_suffix || ': ' || result;
END;
$$;


--
-- Name: FUNCTION create_next_month_partitions(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.create_next_month_partitions() IS '自动创建下个月的所有 telemetry 表分区（heap 存储）。每月28日执行。';


--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;


--
-- Name: get_current_tenant(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_current_tenant() RETURNS text
    LANGUAGE sql STABLE
    AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;


--
-- Name: key_applications_set_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.key_applications_set_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;


--
-- Name: model_offers_delete_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_delete_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE credential_model_bindings SET
        available = FALSE,
        unavailable_reason = 'deleted',
        admin_protected = FALSE,
        updated_at = now()
    WHERE id = OLD.id;
    RETURN OLD;
END;
$$;


--
-- Name: model_offers_insert_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_insert_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO provider_models (provider_id, raw_model_name, canonical_id, outbound_model_name, available, last_seen_at)
    VALUES (
        (SELECT provider_id FROM credentials WHERE id = NEW.credential_id),
        NEW.raw_model_name,
        NEW.canonical_id,
        NEW.outbound_model_name,
        COALESCE(NEW.available, TRUE),
        COALESCE(NEW.last_seen_at, now())
    )
    ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
        canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id),
        outbound_model_name = COALESCE(EXCLUDED.outbound_model_name, provider_models.outbound_model_name),
        last_seen_at = COALESCE(EXCLUDED.last_seen_at, provider_models.last_seen_at),
        available = TRUE,
        updated_at = now()
    RETURNING id INTO NEW.id;

    INSERT INTO credential_model_bindings (
        credential_id, provider_model_id, available,
        routing_tier, weight, manual_priority,
        success_rate, p95_latency_ms, active_sessions, consecutive_failures,
        unit_price_in_per_1m, unit_price_out_per_1m,
        cache_read_price_per_1m, cache_write_price_per_1m,
        currency, billing_mode, pricing_source, pricing_updated_at,
        admin_protected
    ) VALUES (
        NEW.credential_id, NEW.id, COALESCE(NEW.available, TRUE),
        COALESCE(NEW.routing_tier, 2), COALESCE(NEW.weight, 100), COALESCE(NEW.manual_priority, 99),
        COALESCE(NEW.success_rate, 0.9), COALESCE(NEW.p95_latency_ms, 0),
        COALESCE(NEW.active_sessions, 0), COALESCE(NEW.consecutive_failures, 0),
        COALESCE(NEW.unit_price_in_per_1m, 0), COALESCE(NEW.unit_price_out_per_1m, 0),
        COALESCE(NEW.cache_read_price_per_1m, 0), COALESCE(NEW.cache_write_price_per_1m, 0),
        COALESCE(NEW.currency, 'USD'), COALESCE(NEW.billing_mode, 'token'),
        NEW.pricing_source, NEW.pricing_updated_at,
        COALESCE(NEW.admin_protected, FALSE)
    )
    ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
        routing_tier = COALESCE(EXCLUDED.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(EXCLUDED.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(EXCLUDED.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(EXCLUDED.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(EXCLUDED.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(EXCLUDED.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(EXCLUDED.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(EXCLUDED.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(EXCLUDED.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(EXCLUDED.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(EXCLUDED.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(EXCLUDED.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(EXCLUDED.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(EXCLUDED.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(EXCLUDED.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        updated_at = now();

    RETURN NEW;
END;
$$;


--
-- Name: model_offers_update_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_update_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_pm_id BIGINT;
BEGIN
    SELECT provider_model_id INTO v_pm_id
    FROM credential_model_bindings WHERE id = OLD.id;

    IF v_pm_id IS NOT NULL THEN
        UPDATE provider_models SET
            canonical_id = COALESCE(NEW.canonical_id, provider_models.canonical_id),
            standardized_name = COALESCE(NEW.standardized_name, provider_models.standardized_name),
            outbound_model_name = COALESCE(NEW.outbound_model_name, provider_models.outbound_model_name),
            last_seen_at = COALESCE(NEW.last_seen_at, provider_models.last_seen_at),
            updated_at = now()
        WHERE id = v_pm_id;
    END IF;

    UPDATE credential_model_bindings SET
        available = COALESCE(NEW.available, credential_model_bindings.available),
        unavailable_reason = CASE
            WHEN NEW.unavailable_reason IS NOT NULL THEN NEW.unavailable_reason
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_reason
        END,
        unavailable_at = CASE
            WHEN NEW.unavailable_at IS NOT NULL THEN NEW.unavailable_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_at
        END,
        admin_protected = CASE
            WHEN NEW.admin_protected IS NOT NULL THEN NEW.admin_protected
            ELSE credential_model_bindings.admin_protected
        END,
        routing_tier = COALESCE(NEW.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(NEW.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(NEW.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(NEW.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(NEW.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(NEW.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(NEW.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(NEW.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(NEW.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(NEW.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(NEW.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(NEW.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(NEW.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(NEW.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(NEW.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;


--
-- Name: model_probe_backoff(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff(consecutive_failures integer) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
		    SELECT CASE
			WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
			WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
			WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
			ELSE                                  INTERVAL '15 minutes'
		    END;
		$$;


--
-- Name: model_probe_backoff_v2(integer, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
    WITH age AS (
        SELECT EXTRACT(EPOCH FROM (NOW() - COALESCE(last_attempt_at, NOW() - INTERVAL '1 hour'))) AS secs
    )
    SELECT CASE
        -- 0 failures → healthy_confirmed watchdog (every 2h)
        WHEN consecutive_failures <= 0 THEN INTERVAL '2 hours'

        -- 3+ failures → still recovering toward broken_confirmed
        WHEN consecutive_failures >= 3 THEN INTERVAL '60 minutes'

        -- 1 failure: ramp up frequency when fresh, taper when stale
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <   300 THEN INTERVAL '1 minute'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '3 minutes'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '10 minutes'
        WHEN consecutive_failures = 1                              THEN INTERVAL '30 minutes'

        -- 2 failures: same pattern but with longer floor
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <   300 THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '5 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '15 minutes'
        WHEN consecutive_failures = 2                              THEN INTERVAL '45 minutes'

        -- 4+ failures: very rare, treat like 3+
        ELSE INTERVAL '60 minutes'
    END;
$$;


--
-- Name: FUNCTION model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone) IS 'Adaptive backoff: 0 fails = 2h watchdog; 1 fail ramps 1m→30m as the failure ages; 2 fails ramps 2m→45m; 3+ fails = 60m recovering.';


--
-- Name: model_probe_passive_boost(bigint, text, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    recent_count INTEGER;
    new_retry TIMESTAMPTZ;
BEGIN
    SELECT COUNT(*) INTO recent_count
    FROM candidate_failure_logs
    WHERE credential_id = p_credential_id
      AND raw_model_name = p_raw_model_name
      AND ts > p_now - INTERVAL '5 minutes';

    IF recent_count >= 3 THEN
        new_retry := p_now + INTERVAL '30 seconds';
    ELSIF recent_count >= 2 THEN
        new_retry := p_now + INTERVAL '1 minute';
    ELSE
        RETURN;
    END IF;

    UPDATE model_probe_state mps
    SET next_retry_at = LEAST(COALESCE(mps.next_retry_at, new_retry), new_retry)
    WHERE mps.credential_id = p_credential_id
      AND mps.raw_model_name = p_raw_model_name
      AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed';
END;
$$;


--
-- Name: FUNCTION model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone) IS 'When a (cred, model) sees 2+ failures in 5 min via passive signals, pull next_retry_at forward to 30s–1m so the next cycle probes sooner.';


--
-- Name: model_probe_reclaim_idle_slots(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_reclaim_idle_slots(reclaim_after_seconds integer) RETURNS TABLE(deleted_slots integer, deleted_pins integer)
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_deleted_slots INTEGER := 0;
    v_deleted_pins  INTEGER := 0;
    v_cutoff        TIMESTAMPTZ := NOW() - make_interval(secs => reclaim_after_seconds);
    rec             RECORD;
BEGIN
    -- Iterate over currently-occupied slots whose holder has been idle
    -- (no recent traffic on the holder identity) for longer than the
    -- cutoff. We use Redis-side expiration timestamps via the slot key
    -- TTL as the activity signal: a slot's TTL is refreshed on every
    -- Release(). If the TTL is below the cutoff, the holder has been
    -- idle since the last refresh.
    --
    -- We don't have direct access to Redis from plpgsql, so this SQL
    -- function targets the model_probe_state table (which mirrors the
    -- Redis slot via the runner's recordRun writes).
    --
    -- The Go goroutine in credentialfpslot handles the actual Redis
    -- DEL via the same Lua script used by ResetSlots. This SQL function
    -- is a companion for ops tooling and consistency checks.
    FOR rec IN
        SELECT credential_id, raw_model_name
        FROM model_probe_state
        WHERE last_attempt_at < v_cutoff
          AND state <> 'broken_confirmed'
    LOOP
        UPDATE model_probe_state
        SET state = 'unknown',
            consecutive_successes = 0,
            consecutive_failures = 0,
            next_retry_at = NOW() + INTERVAL '2 hours',
            -- do NOT change last_attempt_at — we want it to remain the
            -- "last activity" anchor for future audit queries.
            last_state_change_at = NOW()
        WHERE credential_id = rec.credential_id
          AND raw_model_name = rec.raw_model_name;
        v_deleted_slots := v_deleted_slots + 1;
    END LOOP;

    RETURN QUERY SELECT v_deleted_slots, v_deleted_pins;
END;
$$;


--
-- Name: FUNCTION model_probe_reclaim_idle_slots(reclaim_after_seconds integer); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.model_probe_reclaim_idle_slots(reclaim_after_seconds integer) IS 'Mark model_probe_state rows as unknown if last_attempt_at is older than reclaim_after_seconds. Companion to credentialfpslot.reclaimIdleSlots which does the actual Redis key cleanup.';


--
-- Name: notify_auto_route_refresh(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_auto_route_refresh() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    entity_id text := '';
BEGIN
    IF TG_TABLE_NAME = 'credential_model_bindings' THEN
        entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::text;
    ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys') THEN
        entity_id := COALESCE(NEW.id, OLD.id)::text;
    END IF;

    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP || entity_id);
    RETURN COALESCE(NEW, OLD);
END;
$$;


--
-- Name: recent_success_rate(bigint, text, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recent_success_rate(p_credential_id bigint, p_raw_model text, p_sample_n integer DEFAULT 50, p_window_hours integer DEFAULT 3) RETURNS TABLE(rate double precision, samples integer)
    LANGUAGE sql STABLE
    AS $$
		    WITH recent AS (
		        SELECT success
		        FROM request_logs
		        WHERE credential_id = p_credential_id
		          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
		          AND ts > NOW() - (p_window_hours || ' hours')::interval
		        ORDER BY ts DESC
		        LIMIT p_sample_n
		    )
		    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
		           COUNT(*)::int
		    FROM recent;
		$$;


--
-- Name: routing_overrides_audit_fn(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.routing_overrides_audit_fn() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('insert', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		             NEW.model_chosen, NEW.reason, NEW.expires_at, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at
		           OR NEW.reason IS DISTINCT FROM OLD.reason
		           OR NEW.model_chosen IS DISTINCT FROM OLD.model_chosen
		        THEN
		            INSERT INTO routing_overrides_audit
		                (action, override_id, task_type, profile, mode,
		                 model_chosen, reason, expires_at, old_expires_at,
		                 actor)
		            VALUES
		                ('update', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		                 NEW.model_chosen, NEW.reason, NEW.expires_at,
		                 OLD.expires_at, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('delete', OLD.id, OLD.task_type, OLD.profile, OLD.mode,
		             OLD.model_chosen, OLD.reason, OLD.expires_at, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$;


--
-- Name: tenant_model_policies_audit_fn(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.tenant_model_policies_audit_fn() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('insert', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
		            IF NEW.deleted_at IS NULL THEN
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('undelete', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		            ELSE
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('delete', NEW.id, NEW.tenant_id, NEW.canonical_name, OLD.reason, v_actor);
		            END IF;
		        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
		              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
		        THEN
		            INSERT INTO tenant_model_policies_audit
		                (action, policy_id, tenant_id, canonical_name, reason, actor)
		            VALUES
		                ('update', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('delete', OLD.id, OLD.tenant_id, OLD.canonical_name, OLD.reason, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$;


--
-- Name: trg_cmb_protect_manual_disable(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.trg_cmb_protect_manual_disable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.unavailable_reason = 'manual' THEN
        -- Admin explicit re-enable (toggleModelOfferState available=true)
        IF (NEW.available = TRUE AND NEW.unavailable_reason IS NULL)
           OR current_setting('llmgw.admin_override', true) = '1' THEN
            RETURN NEW;
        END IF;

        IF NEW.unavailable_reason IS DISTINCT FROM 'manual' THEN
            NEW.unavailable_reason := 'manual';
        END IF;
        IF NEW.available = TRUE THEN
            NEW.available := FALSE;
        END IF;
        IF NEW.unavailable_at IS NULL THEN
            NEW.unavailable_at := OLD.unavailable_at;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: update_api_key_model_cost(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_api_key_model_cost() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    bucket_ts TIMESTAMPTZ;
    key_id INT;
    limit_val INT;
BEGIN
    -- 计算 5min bucket（向下取整）
    bucket_ts := date_trunc('hour', NEW.ts)
                  + (FLOOR(EXTRACT(minute FROM NEW.ts) / 5) * INTERVAL '5 minutes');
    key_id := NEW.api_key_id;
    IF key_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- 查找 api_key 的 rate_limit_rpm（作为该 key 的并发近似上限）
    -- 注意：api_keys 表没有 concurrency_limit 列（已在 realtime-trigger SQL 中确认）。
    -- 用 rate_limit_rpm / 10 作为近似（假设平均请求耗时 6 秒）。
    SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
    FROM api_keys WHERE id = key_id;

    -- 增量更新（注意：不在这里累加 active_concurrent，因为 AFTER INSERT 只能加不能减。
    -- active_concurrent 由 customer_cost_view 通过 JOIN request_logs 实时计算）
    INSERT INTO api_key_model_cost (
        bucket, api_key_id, canonical_id, raw_model, billing_mode,
        requests_total, requests_success,
        tokens_input, tokens_output, cost_usd,
        active_concurrent, concurrency_limit, pressure_ratio,
        last_request_at, updated_at
    ) VALUES (
        bucket_ts, key_id, NEW.canonical_id, COALESCE(NEW.outbound_model, NEW.client_model),
        'token',
        1, CASE WHEN NEW.success THEN 1 ELSE 0 END,
        COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        COALESCE(NEW.cost_usd, 0),
        1, limit_val,
        CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
        NEW.ts, NOW()
    )
    ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
        requests_total    = api_key_model_cost.requests_total + 1,
        requests_success  = api_key_model_cost.requests_success + CASE WHEN NEW.success THEN 1 ELSE 0 END,
        tokens_input      = api_key_model_cost.tokens_input + COALESCE(NEW.prompt_tokens, 0),
        tokens_output     = api_key_model_cost.tokens_output + COALESCE(NEW.completion_tokens, 0),
        cost_usd          = api_key_model_cost.cost_usd + COALESCE(NEW.cost_usd, 0),
        -- active_concurrent 在 trigger 中不更新（只在视图层动态计算）
        concurrency_limit = EXCLUDED.concurrency_limit,
        pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                  THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                  ELSE 0 END,
        last_request_at   = NEW.ts,
        updated_at        = NOW();

    RETURN NEW;
END;
$$;


--
-- Name: update_provider_settings_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_provider_settings_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: agent_relationships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_relationships (
    src_agent_id bigint NOT NULL,
    dst_agent_id bigint NOT NULL,
    rel text NOT NULL,
    weight double precision DEFAULT 1.0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_agent_rel CHECK ((rel = ANY (ARRAY['calls'::text, 'delegates'::text, 'depends_on'::text, 'similar_to'::text]))),
    CONSTRAINT chk_agent_rel_no_self CHECK ((src_agent_id <> dst_agent_id))
);


--
-- Name: agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agents (
    id bigint NOT NULL,
    tenant_id text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    endpoint text NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    version text DEFAULT '0.0.0'::text NOT NULL,
    auth_scheme text,
    last_heartbeat timestamp with time zone,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT chk_agents_auth CHECK (((auth_scheme IS NULL) OR (auth_scheme = ANY (ARRAY['bearer'::text, 'api_key'::text, 'mtls'::text, 'none'::text])))),
    CONSTRAINT chk_agents_kind CHECK ((kind = ANY (ARRAY['openclaw'::text, 'brandmind-go'::text, 'crm-go'::text, 'custom'::text]))),
    CONSTRAINT chk_agents_status CHECK ((status = ANY (ARRAY['healthy'::text, 'degraded'::text, 'down'::text, 'unknown'::text])))
);


--
-- Name: agents_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.agents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: agents_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.agents_id_seq OWNED BY public.agents.id;


--
-- Name: api_key_auto_profile; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_key_auto_profile (
    api_key_id integer NOT NULL,
    profile text DEFAULT 'smart'::text NOT NULL,
    first_chosen_at timestamp with time zone DEFAULT now(),
    last_used_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT api_key_auto_profile_profile_check CHECK ((profile = ANY (ARRAY['smart'::text, 'speed_first'::text, 'cost_first'::text])))
);


--
-- Name: TABLE api_key_auto_profile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.api_key_auto_profile IS 'Auto route: per-API-Key profile preference (sticky 30min)';


--
-- Name: api_key_model_cost; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_key_model_cost (
    bucket timestamp with time zone NOT NULL,
    api_key_id integer NOT NULL,
    canonical_id integer,
    raw_model text NOT NULL,
    billing_mode text,
    requests_total integer DEFAULT 0 NOT NULL,
    requests_success integer DEFAULT 0 NOT NULL,
    tokens_input bigint DEFAULT 0 NOT NULL,
    tokens_output bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
    active_concurrent integer DEFAULT 0 NOT NULL,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    last_request_at timestamp with time zone,
    last_decision_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE api_key_model_cost; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.api_key_model_cost IS 'Auto route: per-API-Key per-model 5min rolled-up cost + concurrency + score';


--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_keys (
    id bigint NOT NULL,
    application_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    key_hash text NOT NULL,
    key_prefix text NOT NULL,
    owner_user text,
    data_sensitivity text DEFAULT 'internal'::text NOT NULL,
    default_end_user_id text,
    budget_usd numeric(14,6),
    rate_limit_rpm integer,
    enabled boolean DEFAULT true NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    status character varying(16) DEFAULT 'active'::character varying NOT NULL,
    key_ciphertext text,
    is_system boolean DEFAULT false NOT NULL,
    rate_limit_concurrent integer,
    rate_limit_tpm integer,
    key_tier character varying(16) DEFAULT 'default'::character varying NOT NULL,
    key_ciphertext_kid text,
    throttled_at timestamp with time zone,
    throttled_reason text,
    ewma_rpm_baseline numeric(10,3),
    ewma_updated_at timestamp with time zone,
    reveal_count integer DEFAULT 0 NOT NULL,
    last_revealed_at timestamp with time zone,
    last_revealed_by text,
    remark text,
    key_alias text,
    total_requests bigint DEFAULT 0 NOT NULL,
    total_prompt_tokens bigint DEFAULT 0 NOT NULL,
    total_completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    total_cost_usd numeric(14,8) DEFAULT 0 NOT NULL,
    last_request_at timestamp with time zone,
    default_client_profile text,
    CONSTRAINT api_keys_data_sensitivity_check CHECK ((data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text]))),
    CONSTRAINT api_keys_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('pending'::character varying)::text, ('disabled'::character varying)::text, ('throttled'::character varying)::text, ('revoked'::character varying)::text])))
);


--
-- Name: COLUMN api_keys.status; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.status IS 'active | pending | disabled | throttled (auto-frozen) | revoked (permanent ban)';


--
-- Name: COLUMN api_keys.is_system; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.is_system IS 'System key - should not be disabled (e.g., admin login key)';


--
-- Name: COLUMN api_keys.rate_limit_concurrent; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.rate_limit_concurrent IS 'Per-key concurrent request cap (NULL = use tier default)';


--
-- Name: COLUMN api_keys.rate_limit_tpm; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.rate_limit_tpm IS 'Tokens per minute cap (NULL = no limit)';


--
-- Name: COLUMN api_keys.key_tier; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_tier IS 'system | production | default | applicant';


--
-- Name: COLUMN api_keys.key_ciphertext_kid; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_ciphertext_kid IS 'kid that was used when key_ciphertext was last written (v1 AES-GCM envelope)';


--
-- Name: COLUMN api_keys.throttled_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.throttled_at IS 'Timestamp when the key was auto-throttled by anomaly detection';


--
-- Name: COLUMN api_keys.ewma_rpm_baseline; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.ewma_rpm_baseline IS 'Rolling EWMA baseline RPM for anomaly detection (7-day window)';


--
-- Name: COLUMN api_keys.remark; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.remark IS 'Reason for key creation (system-created keys must explain why)';


--
-- Name: COLUMN api_keys.key_alias; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_alias IS 'Optional human-readable alias for the key';


--
-- Name: COLUMN api_keys.total_requests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_requests IS 'Cumulative count of requests authenticated by this key';


--
-- Name: COLUMN api_keys.total_prompt_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_prompt_tokens IS 'Cumulative prompt token count';


--
-- Name: COLUMN api_keys.total_completion_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_completion_tokens IS 'Cumulative completion token count';


--
-- Name: COLUMN api_keys.total_cost_usd; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_cost_usd IS 'Cumulative cost in USD';


--
-- Name: COLUMN api_keys.last_request_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.last_request_at IS 'When this key last made a request (denormalized from usage_ledger)';


--
-- Name: api_keys_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.api_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: api_keys_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;


--
-- Name: applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.applications (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    owner_user text,
    data_sensitivity text DEFAULT 'internal'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_client_profile text,
    allowed_models_json jsonb,
    CONSTRAINT applications_data_sensitivity_check CHECK ((data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text])))
);


--
-- Name: applications_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.applications_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: applications_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.applications_id_seq OWNED BY public.applications.id;


--
-- Name: armor_judgments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.armor_judgments (
    id bigint NOT NULL,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    check_type text NOT NULL,
    decision text NOT NULL,
    source text NOT NULL,
    pattern_ids text[],
    judge_model text,
    score real,
    threshold real,
    mode text DEFAULT 'observe'::text NOT NULL,
    latency_ms integer DEFAULT 0 NOT NULL,
    prompt_sha256 text,
    snippet text,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_armor_check CHECK ((check_type = ANY (ARRAY['prompt_inject'::text, 'pii'::text, 'hallucination'::text]))),
    CONSTRAINT chk_armor_decision CHECK ((decision = ANY (ARRAY['safe'::text, 'warn'::text, 'block'::text]))),
    CONSTRAINT chk_armor_mode CHECK ((mode = ANY (ARRAY['observe'::text, 'enforce'::text]))),
    CONSTRAINT chk_armor_source CHECK ((source = ANY (ARRAY['pattern'::text, 'judge'::text])))
);


--
-- Name: armor_judgments_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.armor_judgments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: armor_judgments_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.armor_judgments_id_seq OWNED BY public.armor_judgments.id;


--
-- Name: asset_relationships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_relationships (
    src_kind text NOT NULL,
    src_ref_id bigint NOT NULL,
    dst_kind text NOT NULL,
    dst_ref_id bigint NOT NULL,
    rel text NOT NULL,
    weight double precision DEFAULT 1.0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_asset_rel_type CHECK ((rel = ANY (ARRAY['depends_on'::text, 'calls'::text, 'similar_to'::text])))
);


--
-- Name: assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.assets (
    kind text NOT NULL,
    ref_id bigint NOT NULL,
    tenant_id text NOT NULL,
    name text NOT NULL,
    owner text,
    team text,
    cost_center text,
    tags jsonb DEFAULT '{}'::jsonb NOT NULL,
    health_state text DEFAULT 'unknown'::text NOT NULL,
    version text DEFAULT '0.0.0'::text NOT NULL,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT chk_assets_health CHECK ((health_state = ANY (ARRAY['healthy'::text, 'degraded'::text, 'down'::text, 'unknown'::text]))),
    CONSTRAINT chk_assets_kind CHECK ((kind = ANY (ARRAY['llm_endpoint'::text, 'mcp_server'::text, 'agent'::text])))
);


--
-- Name: auto_tune_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auto_tune_audit (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    action text NOT NULL,
    old_limit integer,
    new_limit integer,
    reason text,
    peak_concurrent integer,
    p95_concurrent numeric(8,2),
    week_start timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_by text
);


--
-- Name: TABLE auto_tune_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auto_tune_audit IS 'Audit log for concurrency limit auto-tune actions (24h preview + auto-apply)';


--
-- Name: auto_tune_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.auto_tune_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: auto_tune_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.auto_tune_audit_id_seq OWNED BY public.auto_tune_audit.id;


--
-- Name: background_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_tasks (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    task_type text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    status text DEFAULT 'running'::text NOT NULL,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    result_json jsonb,
    error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone
);


--
-- Name: background_tasks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.background_tasks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: background_tasks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.background_tasks_id_seq OWNED BY public.background_tasks.id;


--
-- Name: billing_orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.billing_orders (
    id bigint NOT NULL,
    order_no character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    order_type character varying(16) NOT NULL,
    status character varying(16) DEFAULT 'pending'::character varying NOT NULL,
    amount_cents integer NOT NULL,
    credits bigint NOT NULL,
    plan_id integer,
    package_id integer,
    payment_channel character varying(16) DEFAULT 'alipay'::character varying NOT NULL,
    qr_payload text DEFAULT ''::text NOT NULL,
    qr_url text DEFAULT ''::text NOT NULL,
    paid_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT billing_orders_order_type_check CHECK (((order_type)::text = ANY (ARRAY[('subscribe'::character varying)::text, ('topup'::character varying)::text]))),
    CONSTRAINT billing_orders_payment_channel_check CHECK (((payment_channel)::text = ANY (ARRAY[('alipay'::character varying)::text, ('wechat'::character varying)::text, ('manual'::character varying)::text]))),
    CONSTRAINT billing_orders_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('paid'::character varying)::text, ('cancelled'::character varying)::text, ('expired'::character varying)::text])))
);


--
-- Name: billing_orders_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.billing_orders_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: billing_orders_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.billing_orders_id_seq OWNED BY public.billing_orders.id;


SET default_table_access_method = columnar;

--
-- Name: candidate_failure_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.candidate_failure_logs (
    id bigint,
    request_id text,
    ts timestamp with time zone,
    tenant_id text,
    credential_id integer,
    provider_id integer,
    raw_model_name text,
    attempt_index integer,
    error_kind text,
    error_message text,
    upstream_status_code integer,
    upstream_response_body text,
    upstream_response_preview text,
    latency_ms integer,
    retryable boolean,
    context jsonb,
    per_attempt_latency_ms integer
);


SET default_table_access_method = heap;

--
-- Name: credential_capabilities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_capabilities (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    capability text NOT NULL,
    supported boolean DEFAULT false NOT NULL,
    last_tested_at timestamp with time zone,
    evidence_json jsonb,
    CONSTRAINT credential_capabilities_capability_check CHECK ((capability = ANY (ARRAY['tool_use'::text, 'vision'::text, 'streaming'::text, 'prompt_caching'::text, 'structured_output'::text, 'long_context'::text, 'json_mode'::text, 'batch'::text])))
);


--
-- Name: credential_capabilities_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_capabilities_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_capabilities_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_capabilities_id_seq OWNED BY public.credential_capabilities.id;


--
-- Name: credential_health_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_health_checks (
    id bigint NOT NULL,
    run_id bigint,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    provider_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    models_ok boolean DEFAULT false NOT NULL,
    probe_ok boolean DEFAULT false NOT NULL,
    health_status text NOT NULL,
    warning_code text,
    classification_reason text,
    models_failure_reason text,
    models_http_status integer,
    probe_http_status integer,
    models_latency_ms integer,
    probe_latency_ms integer,
    probe_model text,
    models_error text,
    probe_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_credential_health_checks_models_failure_reason CHECK (((models_failure_reason IS NULL) OR (models_failure_reason = ANY (ARRAY['request_failed'::text, 'empty_models'::text, 'invalid_payload'::text, 'not_supported'::text])))),
    CONSTRAINT chk_credential_health_checks_status CHECK ((health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'warning'::text, 'unreachable'::text])))
);


--
-- Name: credential_health_checks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_health_checks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_health_checks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_health_checks_id_seq OWNED BY public.credential_health_checks.id;


--
-- Name: credential_model_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_bindings (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_model_id bigint NOT NULL,
    routing_tier smallint DEFAULT 2,
    weight smallint DEFAULT 100,
    manual_priority smallint DEFAULT 99,
    success_rate numeric,
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    consecutive_failures integer DEFAULT 0,
    unit_price_in_per_1m numeric,
    unit_price_out_per_1m numeric,
    cache_read_price_per_1m numeric,
    cache_write_price_per_1m numeric,
    currency text DEFAULT 'USD'::text,
    billing_mode text DEFAULT 'per_token'::text,
    pricing_source text,
    pricing_updated_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    available boolean DEFAULT true NOT NULL,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    plan_meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    admin_protected boolean DEFAULT false NOT NULL,
    unavailable_recover_at timestamp with time zone
);


--
-- Name: TABLE credential_model_bindings; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_bindings IS 'Many-to-many: which credential can access which model, with routing/pricing attrs';


--
-- Name: COLUMN credential_model_bindings.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_bindings.billing_mode IS 'Billing mode: token (PAYG per-1M) | token_plan (prepaid credits/package) | code_plan (subscription, monthly fee + bundle) | free (rate=0) | per_token/per_request/monthly (legacy aliases)';


--
-- Name: COLUMN credential_model_bindings.plan_meta; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_bindings.plan_meta IS 'Subscription/plan metadata: {monthly_cny, included_tokens, tier, validity_days, modality, etc.}. Mirrors pricing_plans.plan_json at offer level.';


--
-- Name: credential_model_bindings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_model_bindings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_model_bindings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_model_bindings_id_seq OWNED BY public.credential_model_bindings.id;


--
-- Name: credential_model_call_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_call_history (
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    window_start timestamp with time zone NOT NULL,
    total_calls integer DEFAULT 0 NOT NULL,
    success_calls integer DEFAULT 0 NOT NULL,
    failed_calls integer DEFAULT 0 NOT NULL,
    avg_latency_ms numeric(8,2),
    p95_latency_ms integer,
    p99_latency_ms integer,
    error_rate_limit_count integer DEFAULT 0 NOT NULL,
    error_quota_count integer DEFAULT 0 NOT NULL,
    error_concurrent_count integer DEFAULT 0 NOT NULL,
    error_network_count integer DEFAULT 0 NOT NULL,
    error_auth_count integer DEFAULT 0 NOT NULL,
    error_other_count integer DEFAULT 0 NOT NULL,
    avg_concurrent numeric(5,2),
    peak_concurrent integer,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE credential_model_call_history; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_call_history IS 'Aggregated call history per (credential, model) in 1-minute windows. Used for intelligent availability tracking, continuous failure detection, and concurrency auto-tuning.';


--
-- Name: COLUMN credential_model_call_history.error_rate_limit_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_call_history.error_rate_limit_count IS '429 rate limit errors - triggers concurrency reduction';


--
-- Name: COLUMN credential_model_call_history.error_concurrent_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_call_history.error_concurrent_count IS '503 concurrent overload errors - triggers concurrency reduction';


--
-- Name: COLUMN credential_model_call_history.avg_concurrent; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_call_history.avg_concurrent IS 'Average concurrent requests in this window - used for auto-scaleup';


--
-- Name: COLUMN credential_model_call_history.peak_concurrent; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_call_history.peak_concurrent IS 'Peak concurrent requests in this window - used for capacity planning';


--
-- Name: credential_model_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_index (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    canonical_id integer,
    billing_mode text,
    unit_price_in_per_1m numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window integer,
    success_rate numeric(5,4),
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE credential_model_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_index IS 'Auto route: per-credential 5min rolled-up live score with 3 profile precomputed';


--
-- Name: credential_model_peak_1m; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_peak_1m (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    peak_concurrent integer DEFAULT 0 NOT NULL,
    avg_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL
);


--
-- Name: TABLE credential_model_peak_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_peak_1m IS 'Per-minute peak concurrency per credential-model pair (used by auto-tune)';


--
-- Name: credential_model_stats_1m; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_stats_1m (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model text DEFAULT ''::text NOT NULL,
    requests integer DEFAULT 0 NOT NULL,
    successes integer DEFAULT 0 NOT NULL,
    failures integer DEFAULT 0 NOT NULL,
    latency_p50_ms integer,
    latency_p95_ms integer,
    latency_p99_ms integer,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(14,8) DEFAULT 0 NOT NULL,
    error_counts jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: TABLE credential_model_stats_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_stats_1m IS 'Per-minute aggregated routing stats, used for sliding window queries';


--
-- Name: credential_model_weekly_peak; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_weekly_peak (
    week_start timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    peak_concurrent integer DEFAULT 0 NOT NULL,
    peak_concurrent_5min integer DEFAULT 0 NOT NULL,
    p95_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    avg_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    total_requests bigint DEFAULT 0 NOT NULL,
    sample_days integer DEFAULT 0 NOT NULL,
    current_limit integer DEFAULT 0 NOT NULL,
    suggested_limit integer,
    suggestion_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE credential_model_weekly_peak; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_weekly_peak IS 'Weekly aggregated peak concurrency for auto-tune suggestions';


SET default_table_access_method = columnar;

--
-- Name: credential_probe_model_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probe_model_log (
    id bigint,
    tenant_id text,
    credential_id bigint,
    source text,
    old_model text,
    new_model text,
    actor text,
    reason text,
    created_at timestamp with time zone
);


SET default_table_access_method = heap;

--
-- Name: credential_quota_usage; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quota_usage (
    id bigint NOT NULL,
    quota_id bigint NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    window_ends_at timestamp with time zone NOT NULL,
    used_total_tokens bigint DEFAULT 0 NOT NULL,
    used_input_tokens bigint DEFAULT 0 NOT NULL,
    used_output_tokens bigint DEFAULT 0 NOT NULL,
    used_requests bigint DEFAULT 0 NOT NULL,
    used_cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    last_event_at timestamp with time zone,
    exhausted boolean DEFAULT false NOT NULL
);


--
-- Name: credential_quota_usage_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_quota_usage_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_quota_usage_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_quota_usage_id_seq OWNED BY public.credential_quota_usage.id;


--
-- Name: credential_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quotas (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    quota_name text NOT NULL,
    window_type text NOT NULL,
    starts_at timestamp with time zone,
    ends_at timestamp with time zone,
    period text,
    cron_expr text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    reset_anchor_local time without time zone,
    rolling_seconds integer,
    cap_total_tokens bigint,
    cap_input_tokens bigint,
    cap_output_tokens bigint,
    cap_requests bigint,
    cap_cost_usd numeric(14,6),
    unlimited_in_window boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    priority integer DEFAULT 100 NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT credential_quotas_window_type_check CHECK ((window_type = ANY (ARRAY['fixed'::text, 'recurring'::text, 'rolling'::text])))
);


--
-- Name: credential_quotas_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_quotas_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_quotas_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_quotas_id_seq OWNED BY public.credential_quotas.id;


--
-- Name: credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credentials (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    label text NOT NULL,
    secret_ciphertext bytea,
    secret_kid text,
    trust_level text DEFAULT 'trusted'::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    concurrency_limit integer,
    effective_concurrency integer,
    balance_usd numeric(14,6),
    pricing_distrust boolean DEFAULT false NOT NULL,
    relay_overhead_ms integer,
    active_plan_id bigint,
    plan_consumed_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    api_models_ok boolean,
    api_models_last_checked_at timestamp with time zone,
    api_models_error text,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    circuit_state text DEFAULT 'closed'::text,
    circuit_opened_at timestamp with time zone,
    consecutive_failures integer DEFAULT 0,
    cooling_until timestamp with time zone,
    circuit_open_count_window integer DEFAULT 0,
    circuit_window_started_at timestamp with time zone,
    effective_at timestamp with time zone,
    expires_at timestamp with time zone,
    tags jsonb DEFAULT '[]'::jsonb,
    notes text,
    health_status text DEFAULT 'unknown'::text NOT NULL,
    health_checked_at timestamp with time zone,
    health_source text,
    health_warning_code text,
    health_error text,
    health_latency_ms integer,
    health_probe_model text,
    lifecycle_status text DEFAULT 'active'::text NOT NULL,
    availability_state text DEFAULT 'ready'::text NOT NULL,
    quota_state text DEFAULT 'ok'::text NOT NULL,
    state_reason_code text,
    state_reason_detail text,
    state_updated_at timestamp with time zone,
    availability_recover_at timestamp with time zone,
    quota_recover_at timestamp with time zone,
    balance_currency text DEFAULT 'USD'::text,
    balance_last_checked_at timestamp with time zone,
    balance_check_endpoint text,
    pool_group text,
    acquisition_source text,
    acquisition_detail text,
    manual_disabled boolean DEFAULT false NOT NULL,
    default_probe_model text,
    default_probe_model_source text,
    default_probe_model_picked_at timestamp with time zone,
    concurrency_limit_auto integer,
    fp_slot_limit integer NOT NULL,
    CONSTRAINT chk_credentials_health_source CHECK (((health_source IS NULL) OR (health_source = ANY (ARRAY['models'::text, 'probe'::text, 'mixed'::text, 'none'::text, 'fast_reprobe'::text])))),
    CONSTRAINT chk_credentials_health_status CHECK ((health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'warning'::text, 'unreachable'::text]))),
    CONSTRAINT credentials_availability_state_check CHECK ((availability_state = ANY (ARRAY['ready'::text, 'cooling'::text, 'rate_limited'::text, 'auth_failed'::text, 'unreachable'::text, 'suspended'::text]))),
    CONSTRAINT credentials_circuit_state_chk CHECK ((circuit_state = ANY (ARRAY['closed'::text, 'open'::text, 'half_open'::text]))),
    CONSTRAINT credentials_fp_slot_limit_check CHECK (((fp_slot_limit >= 0) AND (fp_slot_limit <= 10000))),
    CONSTRAINT credentials_fp_slot_vs_concurrency CHECK (((concurrency_limit IS NULL) OR (fp_slot_limit IS NULL) OR (fp_slot_limit <= concurrency_limit))),
    CONSTRAINT credentials_lifecycle_status_check CHECK ((lifecycle_status = ANY (ARRAY['active'::text, 'disabled'::text, 'suspended'::text, 'retired'::text]))),
    CONSTRAINT credentials_status_check CHECK ((status = ANY (ARRAY['active'::text, 'cooling'::text, 'degraded'::text, 'quarantine'::text, 'quota_expired'::text, 'disabled'::text]))),
    CONSTRAINT credentials_trust_level_check CHECK ((trust_level = ANY (ARRAY['trusted'::text, 'cooling'::text, 'degraded'::text, 'quarantine'::text])))
);


--
-- Name: COLUMN credentials.api_models_ok; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_ok IS '最近一次模型清单 API 拉取是否成功（NULL=未验证）';


--
-- Name: COLUMN credentials.api_models_last_checked_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_last_checked_at IS '最近一次模型清单 API 验证时间';


--
-- Name: COLUMN credentials.api_models_error; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_error IS '最近一次模型清单 API 验证失败原因（HTTP 状态码 + 错误摘要，已脱敏）';


--
-- Name: COLUMN credentials.balance_check_endpoint; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.balance_check_endpoint IS 'URL template to check remaining balance';


--
-- Name: COLUMN credentials.pool_group; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.pool_group IS 'free | shared | dedicated | NULL';


--
-- Name: COLUMN credentials.acquisition_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.acquisition_source IS 'Free pool: signup | env | oauth | mirrored | discovered | no_key | manual';


--
-- Name: COLUMN credentials.acquisition_detail; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.acquisition_detail IS 'Free pool source detail: env var name, mirror source label, oauth file, signup URL, etc.';


--
-- Name: COLUMN credentials.concurrency_limit_auto; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.concurrency_limit_auto IS 'Algorithm-recommended concurrency limit. Adjusted dynamically based on 429/503 errors and success rate. Read priority: concurrency_limit (manual) > concurrency_limit_auto > default 5.';


--
-- Name: COLUMN credentials.fp_slot_limit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.fp_slot_limit IS 'Fingerprint slot pool size: number of distinct virtual user identities this credential can simulate. 0 = unlimited. Distinct from concurrency_limit which controls in-flight request count.';


--
-- Name: CONSTRAINT credentials_fp_slot_vs_concurrency ON credentials; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON CONSTRAINT credentials_fp_slot_vs_concurrency ON public.credentials IS 'fp_slot_limit (distinct user identities) MUST be <= concurrency_limit (in-flight requests). Otherwise the fingerprint pool exceeds the upstream capacity, defeating anti-rate-limit.';


--
-- Name: credentials_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credentials_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credentials_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credentials_id_seq OWNED BY public.credentials.id;


--
-- Name: credit_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger (
    id bigint NOT NULL,
    tenant_id character varying NOT NULL,
    entry_type character varying NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying,
    ref_id character varying,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying
)
PARTITION BY RANGE (created_at);


--
-- Name: credit_ledger_partitioned_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credit_ledger_partitioned_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credit_ledger_partitioned_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credit_ledger_partitioned_id_seq OWNED BY public.credit_ledger.id;


--
-- Name: credit_ledger_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger_2026_06 (
    id bigint DEFAULT nextval('public.credit_ledger_partitioned_id_seq'::regclass) NOT NULL,
    tenant_id character varying NOT NULL,
    entry_type character varying NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying,
    ref_id character varying,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying
);


--
-- Name: credit_ledger_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger_2026_07 (
    id bigint DEFAULT nextval('public.credit_ledger_partitioned_id_seq'::regclass) NOT NULL,
    tenant_id character varying NOT NULL,
    entry_type character varying NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying,
    ref_id character varying,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying
);


--
-- Name: credit_ledger_2026_08; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger_2026_08 (
    id bigint DEFAULT nextval('public.credit_ledger_partitioned_id_seq'::regclass) NOT NULL,
    tenant_id character varying NOT NULL,
    entry_type character varying NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying,
    ref_id character varying,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying
);


--
-- Name: credit_ledger_old; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger_old (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    entry_type character varying(32) NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying(32),
    ref_id character varying(128),
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying(32),
    CONSTRAINT credit_ledger_entry_type_check CHECK (((entry_type)::text = ANY (ARRAY[('consume'::character varying)::text, ('topup'::character varying)::text, ('subscribe'::character varying)::text, ('adjust'::character varying)::text, ('refund'::character varying)::text])))
);


--
-- Name: credit_ledger_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credit_ledger_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credit_ledger_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credit_ledger_id_seq OWNED BY public.credit_ledger_old.id;


--
-- Name: request_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.request_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: request_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
)
PARTITION BY RANGE (ts);

ALTER TABLE ONLY public.request_logs FORCE ROW LEVEL SECURITY;


--
-- Name: COLUMN request_logs.cost_display; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.cost_display IS 'Request-level displayed cost in its native currency; may differ from cost_usd when provider pricing is not USD.';


--
-- Name: COLUMN request_logs.cost_currency; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.cost_currency IS 'Currency for request_logs.cost_display, e.g. USD/CNY.';


--
-- Name: COLUMN request_logs.is_auto_request; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.is_auto_request IS 'Auto route: was this request model=auto?';


--
-- Name: COLUMN request_logs.task_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.task_type IS 'Auto route: classified task type (chat/reasoning/code/...)';


--
-- Name: COLUMN request_logs.auto_profile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_profile IS 'Auto route: profile used (smart/speed_first/cost_first)';


--
-- Name: COLUMN request_logs.auto_decision; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_decision IS 'Auto route: top-N candidates + chosen model + scoring breakdown';


--
-- Name: COLUMN request_logs.auto_confidence; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_confidence IS 'Auto route: classification confidence 0-1';


--
-- Name: COLUMN request_logs.parent_request_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.parent_request_id IS 'Round 47 (2026-06-18): the pre-compression request_id when compressor rewrote the body. NULL for uncompressed rows. Single-level chain only (child has at most 1 parent).';


--
-- Name: COLUMN request_logs.compression_reason; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_reason IS 'Round 47 (2026-06-18): why compression fired. mode_1_auto_threshold = body > cand.ContextWindow × 0.8 × 3.5 (LLM_GATEWAY_COMPRESSION_MODE=1). mode_2_on_4xx = upstream 4xx context_length_exceeded (LLM_GATEWAY_COMPRESSION_MODE=2). NULL = no compression event, OR pre-request trim happened without 4xx (T-NEW-4). See compression_meta.trim_phase for explicit phase tagging.';


--
-- Name: COLUMN request_logs.compression_strategy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_strategy IS 'Round 47 (2026-06-18): which decompression path succeeded. mechanical_trim = oldest-pair drop (transform/ctx_compress.go). memora_l1_inject = dynamic_context user message from Memora /product/search. llm_summary = 1M-context model summary. noop = attempted but skipped (e.g. warmup_min_facts guard).';


--
-- Name: COLUMN request_logs.compression_meta; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_meta IS 'Round 47 (2026-06-18): compression telemetry. 4xx recovery fields (T-NEW-2): tokens_before/after, bytes_before/after, context_window_used, threshold_bytes, dropped_messages, summary_chars, model_used, latency_ms, memora_facts_used, warmup_skipped, first_user_retained, system_retained, reason_detail. Pre-request trim fields (T-NEW-4): trim_phase="pre_request", phases=["pre_request_trim"] or ["pre_request_trim","4xx_recovery"], reason_detail="pre-request trim (cand.ContextWindow × 0.85 × 3.5 threshold)". See v7 §3.2.';


--
-- Name: COLUMN request_logs.outbound_body; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.outbound_body IS 'v3 (2026-06-19): LLM wire body JSONB — what was actually forwarded to the
     upstream provider. NULL = no session compressor active (outbound == client).
     Differs from request_body when v3 session-level delta-append or proactive
     sliding-window summary rewrote the body before forwarding.';


--
-- Name: COLUMN request_logs.outbound_msg_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.outbound_msg_count IS 'v3 (2026-06-19): Message count inside outbound_body (including system).
     Compare to the client message count in request_body to measure delta.';


--
-- Name: COLUMN request_logs.outbound_token_est; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.outbound_token_est IS 'v3 (2026-06-19): Estimated token count for outbound_body using the
     3.5 chars/token heuristic (same as compressor/estimator.go). Used to
     audit sliding-window threshold decisions in request_logs UI.';


--
-- Name: COLUMN request_logs.outbound_msg_hashes; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.outbound_msg_hashes IS 'v3 (2026-06-19): Per-message fingerprint array [{index, sha256}] for
     outbound_body messages. The next request with the same gw_session_id
     reads this column to run LCS diff and find the incremental message tail,
     enabling delta-append without full re-send of conversation history.';


--
-- Name: COLUMN request_logs.upstream_finish_reason; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.upstream_finish_reason IS '2026-06-19 T-NEW-7: the SOLE home for the upstream finish_reason
     (stop, tool_calls, length, end_turn, function_call, max_tokens, …).
     NULL means the stream ended without a finish_reason (e.g. truncated
     pre-finish).  Populated for BOTH success and failure rows.
     This column REPLACES the prior use of failure_detail_code for
     finish reasons; see the migration header for the full rationale.';


--
-- Name: COLUMN request_logs.tool_calls; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.tool_calls IS 'Structured tool calls from assistant message. OpenAI format: [{id, type, function: {name, arguments}}]. Populated for both streaming and non-streaming responses.';


--
-- Name: customer_cost_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.customer_cost_view AS
 SELECT akmc.api_key_id,
    ak.key_alias,
    ak.tenant_id,
    ak.application_id,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_1h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '24:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_24h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '7 days'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_7d,
    sum(akmc.requests_total) AS total_auto_requests,
    sum(akmc.requests_success) AS total_auto_success,
    ( SELECT count(*) AS count
           FROM public.request_logs rl
          WHERE ((rl.api_key_id = akmc.api_key_id) AND (rl.is_auto_request = true) AND (rl.ts >= (now() - '00:05:00'::interval)) AND (rl.success IS NOT NULL) AND (rl.ts IS NOT NULL))) AS active_concurrent,
    max(akmc.concurrency_limit) AS concurrency_limit,
    avg(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.pressure_ratio
            ELSE NULL::numeric
        END) AS avg_pressure_1h,
    max(akmc.score_smart) AS best_score_smart,
    max(akmc.score_speed_first) AS best_score_speed_first,
    max(akmc.score_cost_first) AS best_score_cost_first,
    max(akmc.last_request_at) AS last_request_at
   FROM (public.api_key_model_cost akmc
     JOIN public.api_keys ak ON ((ak.id = akmc.api_key_id)))
  GROUP BY akmc.api_key_id, ak.key_alias, ak.tenant_id, ak.application_id;


--
-- Name: VIEW customer_cost_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from request_logs (5min window).';


--
-- Name: internal_service_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.internal_service_keys (
    service_id text NOT NULL,
    secret_hash text NOT NULL,
    description text,
    enabled boolean DEFAULT true NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone,
    rotation_notes text
);


--
-- Name: TABLE internal_service_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.internal_service_keys IS 'Registry of HMAC secrets for internal service-to-service authentication.
     The actual secret is stored in INTERNAL_SERVICE_KEYS_JSON env var (not here).
     This table tracks registration metadata and last-used timestamps for audit.';


--
-- Name: key_applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.key_applications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    client_ip inet NOT NULL,
    fingerprint text NOT NULL,
    contact text NOT NULL,
    purpose text,
    status text DEFAULT 'pending'::text NOT NULL,
    issued_key_id bigint,
    admin_notes text,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    expires_at timestamp with time zone DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT key_applications_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text])))
);


--
-- Name: key_rpm_daily; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.key_rpm_daily (
    api_key_id bigint NOT NULL,
    day_bucket date NOT NULL,
    peak_rpm numeric(10,3) DEFAULT 0 NOT NULL,
    avg_rpm numeric(10,3) DEFAULT 0 NOT NULL,
    request_count bigint DEFAULT 0 NOT NULL
);


--
-- Name: local_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_models (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    canonical_id bigint,
    raw_name text NOT NULL,
    quantization text,
    size_bytes bigint,
    family text,
    parameters_b numeric(8,2),
    loaded boolean DEFAULT false NOT NULL,
    keep_alive_seconds integer DEFAULT 0 NOT NULL,
    last_used_at timestamp with time zone
);


--
-- Name: local_models_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.local_models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: local_models_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.local_models_id_seq OWNED BY public.local_models.id;


--
-- Name: local_runtimes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_runtimes (
    id bigint NOT NULL,
    host_code text NOT NULL,
    runtime_type text NOT NULL,
    base_url text NOT NULL,
    mode text DEFAULT 'direct'::text NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    gpu_info_json jsonb,
    vram_total_mb integer,
    vram_used_mb integer,
    ram_total_mb integer,
    last_heartbeat_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT local_runtimes_mode_check CHECK ((mode = ANY (ARRAY['direct'::text, 'agent'::text]))),
    CONSTRAINT local_runtimes_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['ollama'::text, 'vllm'::text, 'llamacpp'::text, 'lmstudio'::text, 'mlx'::text]))),
    CONSTRAINT local_runtimes_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'degraded'::text, 'offline'::text])))
);


--
-- Name: local_runtimes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.local_runtimes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: local_runtimes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.local_runtimes_id_seq OWNED BY public.local_runtimes.id;


--
-- Name: maas_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maas_settings (
    id integer DEFAULT 1 NOT NULL,
    cents_per_credit numeric(10,4) DEFAULT 0.1 NOT NULL,
    base_credits_per_1m bigint DEFAULT 10000 NOT NULL,
    currency_display character varying(8) DEFAULT 'CNY'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    alipay_account character varying(128) DEFAULT ''::character varying NOT NULL,
    wechat_mch_id character varying(128) DEFAULT ''::character varying NOT NULL,
    stub_alipay_qr_url text DEFAULT ''::text NOT NULL,
    stub_wechat_qr_url text DEFAULT ''::text NOT NULL,
    base_credits_per_1m_out bigint,
    base_credits_per_1m_cache_in bigint,
    base_credits_per_1m_cache_out bigint,
    global_discount numeric(6,4) DEFAULT 1.0 NOT NULL,
    CONSTRAINT maas_settings_id_check CHECK ((id = 1))
);


--
-- Name: model_aliases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_aliases (
    id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    raw_name text NOT NULL,
    quantization text,
    surface text,
    status text DEFAULT 'active'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    client_profiles text[],
    CONSTRAINT model_aliases_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: model_aliases_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_aliases_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_aliases_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_aliases_id_seq OWNED BY public.model_aliases.id;


--
-- Name: model_cost_per_task_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_cost_per_task_view AS
 SELECT mcp.canonical_id,
    mcp.raw_model,
    sum(mcp.cost_usd) AS total_cost_usd,
    sum((mcp.tokens_input + mcp.tokens_output)) AS total_tokens,
        CASE
            WHEN (sum((mcp.tokens_input + mcp.tokens_output)) > (0)::numeric) THEN ((sum(mcp.cost_usd) / sum((mcp.tokens_input + mcp.tokens_output))) * (1000000)::numeric)
            ELSE (0)::numeric
        END AS avg_cost_per_1m_usd,
        CASE
            WHEN (sum(mcp.requests_total) > 0) THEN ((sum(mcp.requests_success))::numeric / (sum(mcp.requests_total))::numeric)
            ELSE (0)::numeric
        END AS success_rate,
    ( SELECT avg(rl.latency_ms) AS avg
           FROM public.request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(mcp.requests_total) AS total_requests,
    count(DISTINCT mcp.api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (mcp.bucket >= (now() - '7 days'::interval))
  GROUP BY mcp.canonical_id, mcp.raw_model;


--
-- Name: VIEW model_cost_per_task_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';


--
-- Name: model_credit_rates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_credit_rates (
    canonical_id integer NOT NULL,
    credits_per_1m_in bigint,
    credits_per_1m_out bigint,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    credits_per_1m_cache_in bigint,
    credits_per_1m_cache_out bigint,
    manual_in boolean DEFAULT false NOT NULL,
    manual_out boolean DEFAULT false NOT NULL,
    manual_cache_in boolean DEFAULT false NOT NULL,
    manual_cache_out boolean DEFAULT false NOT NULL
);


--
-- Name: model_discovery_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_discovery_runs (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    trigger text DEFAULT 'manual'::text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    requested_by text,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    summary_json jsonb,
    error text,
    CONSTRAINT chk_model_discovery_runs_status CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT chk_model_discovery_runs_trigger CHECK ((trigger = ANY (ARRAY['manual'::text, 'scheduled'::text, 'credential_added'::text])))
);


--
-- Name: model_discovery_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_discovery_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_discovery_runs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_discovery_runs_id_seq OWNED BY public.model_discovery_runs.id;


--
-- Name: model_families; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_families (
    id text NOT NULL,
    display_name text NOT NULL,
    vendor text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_families_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: model_fingerprints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_fingerprints (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    fingerprint_hash text NOT NULL,
    sampled_features_json jsonb,
    last_verified_at timestamp with time zone,
    drift_detected boolean DEFAULT false NOT NULL
);


--
-- Name: model_fingerprints_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_fingerprints_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_fingerprints_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_fingerprints_id_seq OWNED BY public.model_fingerprints.id;


--
-- Name: model_lifecycle_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_lifecycle_jobs (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    progress_pct numeric(5,2) DEFAULT 0,
    log text,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_lifecycle_jobs_action_check CHECK ((action = ANY (ARRAY['pull'::text, 'rm'::text, 'load'::text, 'unload'::text, 'keepalive'::text]))),
    CONSTRAINT model_lifecycle_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'success'::text, 'failed'::text, 'canceled'::text])))
);


--
-- Name: model_lifecycle_jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_lifecycle_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_lifecycle_jobs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_lifecycle_jobs_id_seq OWNED BY public.model_lifecycle_jobs.id;


SET default_table_access_method = columnar;

--
-- Name: model_offer_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offer_events (
    id bigint,
    ts timestamp with time zone,
    source text,
    action text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text,
    reason_code text,
    reason_detail text,
    request_id text,
    run_id bigint,
    metadata_json jsonb
);


SET default_table_access_method = heap;

--
-- Name: provider_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_models (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    raw_model_name text NOT NULL,
    canonical_id bigint,
    standardized_name text,
    outbound_model_name text,
    available boolean DEFAULT true NOT NULL,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE provider_models; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_models IS 'Provider-exposed models: one row per (provider, raw_model_name)';


--
-- Name: COLUMN provider_models.canonical_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_models.canonical_id IS 'FK to models_canonical.id for canonical name resolution';


--
-- Name: model_offers; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
    pm.raw_model_name,
    cmb.success_rate,
    cmb.p95_latency_ms,
    cmb.available,
    pm.last_seen_at,
    cmb.routing_tier,
    cmb.weight,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    cmb.currency,
    pm.outbound_model_name,
    cmb.cache_read_price_per_1m,
    cmb.cache_write_price_per_1m,
    pm.standardized_name,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));


--
-- Name: COLUMN model_offers.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers.billing_mode IS 'Billing mode: token (PAYG per-1M) | token_plan (prepaid credits/package) | code_plan (subscription, monthly fee + bundle) | free (rate=0) | per_token/per_request/monthly (legacy aliases)';


--
-- Name: model_offers_legacy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offers_legacy (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean DEFAULT true NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    routing_tier smallint DEFAULT 2,
    weight smallint DEFAULT 100,
    unit_price_in_per_1m numeric(12,6),
    unit_price_out_per_1m numeric(12,6),
    currency text DEFAULT 'USD'::text,
    outbound_model_name text,
    cache_read_price_per_1m numeric(12,6),
    cache_write_price_per_1m numeric(12,6),
    standardized_name text,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    billing_mode text DEFAULT 'per_token'::text,
    pricing_source text,
    pricing_updated_at timestamp with time zone,
    manual_priority smallint DEFAULT 99,
    active_sessions integer DEFAULT 0,
    consecutive_failures integer DEFAULT 0,
    CONSTRAINT model_offers_manual_priority_chk CHECK (((manual_priority >= 1) AND (manual_priority <= 99))),
    CONSTRAINT model_offers_routing_tier_chk CHECK (((routing_tier >= 1) AND (routing_tier <= 9))),
    CONSTRAINT model_offers_weight_chk CHECK (((weight >= 1) AND (weight <= 1000)))
);


--
-- Name: COLUMN model_offers_legacy.cache_read_price_per_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.cache_read_price_per_1m IS 'Per-million-token price for cache reads (NULL = use unit_price_in_per_1m)';


--
-- Name: COLUMN model_offers_legacy.cache_write_price_per_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.cache_write_price_per_1m IS 'Per-million-token price for cache writes (NULL = use unit_price_in_per_1m)';


--
-- Name: COLUMN model_offers_legacy.standardized_name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.standardized_name IS 'Standardized model name in format: family-version[-feature], e.g. "minimax-m2.7", "glm-4.5-flash", "claude-opus-4.8". Auto-filled on discovery, can be manually edited.';


--
-- Name: COLUMN model_offers_legacy.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.billing_mode IS 'per_token | per_request | monthly | free';


--
-- Name: COLUMN model_offers_legacy.pricing_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.pricing_source IS 'manual | scraped | inherited';


--
-- Name: model_offers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_offers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_offers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_offers_id_seq OWNED BY public.model_offers_legacy.id;


SET default_table_access_method = columnar;

--
-- Name: model_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_runs (
    id bigint,
    tenant_id text,
    credential_id bigint,
    raw_model_name text,
    status text,
    http_status integer,
    error_code text,
    error_message text,
    latency_ms integer,
    state_change text,
    state_applied boolean,
    triggered_by text,
    created_at timestamp with time zone
);


SET default_table_access_method = heap;

--
-- Name: model_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_state (
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    state text DEFAULT 'unknown'::text NOT NULL,
    consecutive_successes integer DEFAULT 0 NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    total_attempts integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamp with time zone,
    next_retry_at timestamp with time zone DEFAULT now() NOT NULL,
    last_status text,
    last_state_change_at timestamp with time zone,
    last_state_change_run bigint,
    last_unavailable_reason text,
    last_err_code text,
    next_retry_at_override timestamp with time zone
);


--
-- Name: TABLE model_probe_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_probe_state IS 'Per-(credential, model) probe consensus state. 3 consecutive successes to recover; 3 consecutive failures to confirm-broken.';


--
-- Name: COLUMN model_probe_state.consecutive_successes; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_probe_state.consecutive_successes IS 'Counter; resets to 0 on any failure. State flips to healthy_confirmed when this hits 3.';


--
-- Name: COLUMN model_probe_state.consecutive_failures; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_probe_state.consecutive_failures IS 'Counter; resets to 0 on any success. Stops probing when this hits 3 (broken_confirmed).';


--
-- Name: model_reconcile_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_reconcile_log (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    credential_id bigint,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    added integer DEFAULT 0 NOT NULL,
    removed integer DEFAULT 0 NOT NULL,
    changed integer DEFAULT 0 NOT NULL,
    diff_json jsonb
);


--
-- Name: model_reconcile_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_reconcile_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_reconcile_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_reconcile_log_id_seq OWNED BY public.model_reconcile_log.id;


--
-- Name: model_task_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_task_index (
    bucket timestamp with time zone NOT NULL,
    canonical_id integer NOT NULL,
    task_type text NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL,
    success_rate numeric(5,4),
    avg_latency_ms integer,
    p95_latency_ms integer,
    avg_cost_per_1k_usd numeric(10,6),
    primary_credential_id bigint,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE model_task_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_task_index IS 'Auto route: per-model-per-task 5min rolled-up performance (success/latency/cost)';


--
-- Name: models_canonical; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.models_canonical (
    id bigint NOT NULL,
    canonical_name text NOT NULL,
    family text,
    parameters_b numeric(8,2),
    modality text DEFAULT 'text'::text NOT NULL,
    context_window integer,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    tags_locked boolean DEFAULT false NOT NULL,
    tags_updated_at timestamp with time zone,
    display_name text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    disabled_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    input_price_cny numeric(10,4) DEFAULT 0,
    output_price_cny numeric(10,4) DEFAULT 0,
    released_at date,
    strengths text[] DEFAULT '{}'::text[] NOT NULL,
    cost_tier text DEFAULT 'unknown'::text NOT NULL,
    multimodal_caps text[] DEFAULT '{}'::text[] NOT NULL,
    version_rank integer,
    CONSTRAINT models_canonical_cost_tier_check CHECK ((cost_tier = ANY (ARRAY['free'::text, 'low'::text, 'medium'::text, 'high'::text, 'premium'::text, 'unknown'::text]))),
    CONSTRAINT models_canonical_modality_check CHECK ((modality = ANY (ARRAY['text'::text, 'vision'::text, 'audio'::text, 'multimodal'::text, 'embedding'::text]))),
    CONSTRAINT models_canonical_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: COLUMN models_canonical.input_price_cny; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.input_price_cny IS 'Input price in CNY per million tokens (0 = not set/unknown)';


--
-- Name: COLUMN models_canonical.output_price_cny; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.output_price_cny IS 'Output price in CNY per million tokens (0 = not set/unknown)';


--
-- Name: COLUMN models_canonical.released_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.released_at IS '模型发布日期，用于 version_recency 评分维度（高难度任务偏好最新版，普通任务偏好次新版）';


--
-- Name: COLUMN models_canonical.strengths; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.strengths IS '运营标注的优势方向数组，用于 strength_match 评分维度（比 tags 更精准）';


--
-- Name: COLUMN models_canonical.cost_tier; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.cost_tier IS '成本粗评：free/low/medium/high/premium，用于快速筛选和展示';


--
-- Name: COLUMN models_canonical.multimodal_caps; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.multimodal_caps IS '多模态能力细粒度标签：vision/audio/image_gen/video/embedding 等';


--
-- Name: COLUMN models_canonical.version_rank; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.version_rank IS '版本级次：1=最新, 2=次新, 3=稳定版... 用于路由策略（普通任务偏次新，高难度偏最新）';


--
-- Name: models_canonical_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.models_canonical_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: models_canonical_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.models_canonical_id_seq OWNED BY public.models_canonical.id;


--
-- Name: ops_model_offers_backup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ops_model_offers_backup (
    backup_id bigint NOT NULL,
    run_tag text NOT NULL,
    backed_at timestamp with time zone DEFAULT now() NOT NULL,
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean NOT NULL,
    last_seen_at timestamp with time zone NOT NULL
);


--
-- Name: ops_model_offers_backup_backup_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.ops_model_offers_backup_backup_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: ops_model_offers_backup_backup_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.ops_model_offers_backup_backup_id_seq OWNED BY public.ops_model_offers_backup.backup_id;


--
-- Name: passive_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.passive_probe_state (
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    error_kind text NOT NULL,
    consecutive_count integer DEFAULT 0 NOT NULL,
    total_recent_count integer DEFAULT 0 NOT NULL,
    window_total_count integer DEFAULT 0 NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    in_reviewing boolean DEFAULT false NOT NULL,
    reviewing_until timestamp with time zone,
    final_marked_at timestamp with time zone,
    unavailable_reason text,
    last_response_body_preview text
);


--
-- Name: TABLE passive_probe_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.passive_probe_state IS 'v5: Passive observation state for Layer 5. Accumulates consecutive errors from request_logs for the secondary-verification trigger (consecutive>=3 or error_rate>=0.6).';


SET default_table_access_method = columnar;

--
-- Name: price_change_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.price_change_events (
    id bigint,
    old_plan_id bigint,
    new_plan_id bigint,
    delta_json jsonb,
    detected_at timestamp with time zone,
    notify_channel text,
    applied boolean
);


SET default_table_access_method = heap;

--
-- Name: pricing_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_plans (
    id bigint NOT NULL,
    scope text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    tenant_id text,
    model_canonical_id bigint,
    plan_type text NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    plan_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    effective_from timestamp with time zone DEFAULT now() NOT NULL,
    effective_to timestamp with time zone,
    source text DEFAULT 'manual'::text NOT NULL,
    confidence numeric(4,3) DEFAULT 1.000,
    scraped_url text,
    offer_scope_key text GENERATED ALWAYS AS (((((((((((scope || ':'::text) || COALESCE((provider_id)::text, '-'::text)) || ':'::text) || COALESCE((credential_id)::text, '-'::text)) || ':'::text) || COALESCE(tenant_id, '-'::text)) || ':'::text) || COALESCE((model_canonical_id)::text, '-'::text)) || ':'::text) || plan_type)) STORED,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pricing_plans_plan_type_check CHECK ((plan_type = ANY (ARRAY['token'::text, 'token_plan'::text, 'code_plan'::text, 'agent_plan'::text, 'request'::text, 'seat'::text, 'compute_time'::text, 'flat_quota'::text, 'free'::text]))),
    CONSTRAINT pricing_plans_scope_check CHECK ((scope = ANY (ARRAY['provider'::text, 'credential'::text, 'tenant'::text]))),
    CONSTRAINT pricing_plans_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'seed'::text, 'litellm'::text, 'scraped'::text, 'catalog'::text])))
);


--
-- Name: COLUMN pricing_plans.plan_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_plans.plan_type IS 'Plan type: token (PAYG per-1M) | token_plan (prepaid credits/package, NEW 2026-06-12) | code_plan (subscription) | agent_plan (agent bundle) | seat (per seat) | request (per request) | compute_time | flat_quota | free';


--
-- Name: pricing_plans_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pricing_plans_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pricing_plans_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pricing_plans_id_seq OWNED BY public.pricing_plans.id;


--
-- Name: pricing_refresh_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_refresh_log (
    id bigint NOT NULL,
    run_id text NOT NULL,
    run_ts timestamp with time zone DEFAULT now() NOT NULL,
    trigger text DEFAULT 'cron'::text NOT NULL,
    status text NOT NULL,
    before_summary jsonb NOT NULL,
    after_summary jsonb NOT NULL,
    diff_count integer DEFAULT 0 NOT NULL,
    new_offers integer DEFAULT 0 NOT NULL,
    removed_offers integer DEFAULT 0 NOT NULL,
    changed_offers integer DEFAULT 0 NOT NULL,
    artifacts_path text,
    feishu_sent boolean DEFAULT false NOT NULL,
    error_message text,
    duration_seconds integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE pricing_refresh_log; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.pricing_refresh_log IS 'Audit log for monthly pricing refresh cron job. Each run inserts one row.';


--
-- Name: COLUMN pricing_refresh_log.before_summary; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.before_summary IS 'pricing/summary response BEFORE refresh (pricing_plans + cmb state)';


--
-- Name: COLUMN pricing_refresh_log.after_summary; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.after_summary IS 'pricing/summary response AFTER refresh';


--
-- Name: COLUMN pricing_refresh_log.diff_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.diff_count IS 'Total offers changed (new + removed + changed)';


--
-- Name: COLUMN pricing_refresh_log.artifacts_path; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.artifacts_path IS 'PVC path containing fetch.log, tier-pricing.csv, summary_*.json';


--
-- Name: pricing_refresh_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pricing_refresh_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pricing_refresh_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pricing_refresh_log_id_seq OWNED BY public.pricing_refresh_log.id;


--
-- Name: provider_catalog; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_catalog (
    code text NOT NULL,
    tier text NOT NULL,
    display_name text NOT NULL,
    display_name_en text,
    category text DEFAULT 'official'::text NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    protocol text NOT NULL,
    base_url_template text NOT NULL,
    docs_url text,
    default_egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate_default numeric(5,4) DEFAULT 1.0,
    models_manifest_json jsonb DEFAULT '[]'::jsonb,
    discovery_strategy text DEFAULT 'auto'::text NOT NULL,
    models_endpoint_template text,
    seed_pricing_plans_json jsonb DEFAULT '[]'::jsonb,
    price_sources_json jsonb DEFAULT '{}'::jsonb,
    hidden boolean DEFAULT false NOT NULL,
    notes text,
    catalog_version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    header_profile_code text,
    capabilities jsonb DEFAULT '{}'::jsonb,
    vendor_name text,
    CONSTRAINT provider_catalog_category_check CHECK ((category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text]))),
    CONSTRAINT provider_catalog_discovery_strategy_check CHECK ((discovery_strategy = ANY (ARRAY['auto'::text, 'manifest'::text, 'hybrid'::text]))),
    CONSTRAINT provider_catalog_kind_check CHECK ((kind = ANY (ARRAY['cloud'::text, 'local'::text]))),
    CONSTRAINT provider_catalog_protocol_check CHECK ((protocol = ANY (ARRAY['openai-completions'::text, 'openai-responses'::text, 'anthropic-messages'::text, 'gemini-generate'::text, 'ollama-native'::text]))),
    CONSTRAINT provider_catalog_tier_check CHECK ((tier = ANY (ARRAY['tier1'::text, 'tier2'::text, 'local'::text, 'restricted'::text])))
);


--
-- Name: COLUMN provider_catalog.models_endpoint_template; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.models_endpoint_template IS '模型清单 API 模板：NULL=自动推导；/models 或 /v1/models 追加到 base_url；https://… 全 URL；空串=仅 manifest';


--
-- Name: COLUMN provider_catalog.capabilities; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.capabilities IS 'Per-catalog capability flags and request sanitization config';


--
-- Name: COLUMN provider_catalog.vendor_name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.vendor_name IS 'Human-readable vendor name for grouped view, e.g. "OpenAI", "Anthropic", "DeepSeek"';


SET default_table_access_method = columnar;

--
-- Name: provider_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_events (
    id bigint,
    credential_id bigint,
    event_kind text,
    payload_json jsonb,
    ts timestamp with time zone
);


SET default_table_access_method = heap;

--
-- Name: provider_header_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_header_profiles (
    id bigint NOT NULL,
    profile_code text NOT NULL,
    display_name text NOT NULL,
    protocol text,
    headers_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    strip_headers_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_header_profiles_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_header_profiles_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_header_profiles_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_header_profiles_id_seq OWNED BY public.provider_header_profiles.id;


--
-- Name: provider_models_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_models_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_models_id_seq OWNED BY public.provider_models.id;


--
-- Name: provider_quality_rollup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_quality_rollup (
    provider_id integer NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    total_requests integer DEFAULT 0 NOT NULL,
    bad_requests integer DEFAULT 0 NOT NULL,
    fixed_requests integer DEFAULT 0 NOT NULL,
    avg_quality_score numeric(3,2),
    top_flag text
);


--
-- Name: provider_scores; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_scores (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    score numeric(6,4) NOT NULL,
    factors_json jsonb,
    computed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_scores_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_scores_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_scores_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_scores_id_seq OWNED BY public.provider_scores.id;


--
-- Name: provider_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settings (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    setting_key text NOT NULL,
    setting_value jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by text DEFAULT 'system'::text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE provider_settings; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_settings IS 'Provider级别的配置覆盖，优先级高于平台默认配置';


--
-- Name: COLUMN provider_settings.setting_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_settings.setting_key IS '配置键，如: compression.mode, cache.enabled, format_conversion.enabled';


--
-- Name: COLUMN provider_settings.setting_value; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_settings.setting_value IS '配置值，JSON格式，如: "off", true, false';


--
-- Name: COLUMN provider_settings.enabled; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_settings.enabled IS '是否启用该配置覆盖';


--
-- Name: provider_settings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_settings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_settings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_settings_id_seq OWNED BY public.provider_settings.id;


--
-- Name: providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.providers (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    catalog_code text,
    is_custom boolean DEFAULT false NOT NULL,
    catalog_version_at_create integer,
    user_overrides_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    category text DEFAULT 'official'::text NOT NULL,
    protocol text NOT NULL,
    base_url text NOT NULL,
    egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate numeric(5,4) DEFAULT 1.0,
    enabled boolean DEFAULT true NOT NULL,
    network_quality_score numeric(4,3) DEFAULT 1.000,
    owner_user text,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    manual_disabled boolean DEFAULT false NOT NULL,
    quality_fix_mode text DEFAULT 'off'::text NOT NULL,
    CONSTRAINT providers_category_check CHECK ((category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text]))),
    CONSTRAINT providers_kind_check CHECK ((kind = ANY (ARRAY['cloud'::text, 'local'::text]))),
    CONSTRAINT providers_quality_fix_mode_check CHECK ((quality_fix_mode = ANY (ARRAY['off'::text, 'detect_only'::text, 'fix'::text])))
);


--
-- Name: COLUMN providers.quality_fix_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.providers.quality_fix_mode IS 'off         : passthrough, no detection, no rewrite.
     detect_only : detect tool_call quality issues, write request_log signals,
                   but do NOT modify the response body sent to the client.
     fix         : detect + write signals + rewrite the response body
                   (rename empty names, dedup ids, etc.) before forwarding.';


--
-- Name: providers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.providers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: providers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.providers_id_seq OWNED BY public.providers.id;


--
-- Name: request_envelope; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_envelope (
    request_id uuid NOT NULL,
    client_model text NOT NULL,
    client_metadata jsonb,
    client_headers_redacted jsonb,
    outbound_model text,
    outbound_protocol text,
    credential_id bigint,
    fingerprint_seed text,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    stream_completed boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: request_logs_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_06 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_07 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_08; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_08 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_archive; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_archive (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    stream_chunk_errors integer,
    stream_done_sent boolean,
    client_timeout boolean,
    client_endpoint text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    stream_interrupted boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    CONSTRAINT chk_archive_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_archive_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
)
PARTITION BY RANGE (ts);


--
-- Name: request_logs_all; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.request_logs_all AS
 SELECT request_logs.id,
    request_logs.request_id,
    request_logs.ts,
    request_logs.tenant_id,
    request_logs.application_id,
    request_logs.api_key_id,
    request_logs.end_user_id,
    request_logs.client_model,
    request_logs.outbound_model,
    request_logs.credential_id,
    request_logs.provider_id,
    request_logs.canonical_id,
    request_logs.client_profile,
    request_logs.request_mode,
    request_logs.prompt_tokens,
    request_logs.completion_tokens,
    request_logs.total_tokens,
    request_logs.cost_usd,
    request_logs.latency_ms,
    request_logs.success,
    request_logs.error_kind,
    request_logs.search_text,
    request_logs.cache_read_tokens,
    request_logs.cache_write_tokens,
    request_logs.identity_hash,
    request_logs.virtual_client_id,
    request_logs.virtual_ip,
    request_logs.virtual_mac,
    request_logs.affinity_hit,
    request_logs.stream_first_chunk_ms,
    request_logs.stream_chunk_count,
    request_logs.stream_chunk_errors,
    request_logs.stream_chunks_sent,
    request_logs.client_endpoint,
    request_logs.client_timeout,
    request_logs.failure_stage,
    request_logs.failure_detail_code,
    request_logs.request_preview,
    request_logs.transform_summary,
    request_logs.response_preview,
    request_logs.stream_done_received,
    request_logs.request_body,
    request_logs.response_body,
    request_logs.cost_display,
    request_logs.cost_currency,
    request_logs.usage_source,
    request_logs.gw_session_id,
    request_logs.gw_task_id,
    request_logs.request_status,
    request_logs.api_key_prefix,
    request_logs.owner_user,
    request_logs.application_code,
    request_logs.key_alias,
    request_logs.api_key_owner_user,
    request_logs.is_auto_request,
    request_logs.task_type,
    request_logs.auto_profile,
    request_logs.auto_decision,
    request_logs.auto_confidence,
    request_logs.work_type,
    request_logs.task_type_chosen,
    request_logs.confidence_num,
    request_logs.model_chosen,
    request_logs.strategy_used,
    request_logs.credits_charged,
    request_logs.parent_request_id,
    request_logs.compression_reason,
    request_logs.compression_strategy,
    request_logs.compression_meta,
    request_logs.outbound_body,
    request_logs.outbound_msg_count,
    request_logs.outbound_token_est,
    request_logs.outbound_msg_hashes,
    request_logs.quality_flags,
    request_logs.quality_fix_actions,
    request_logs.quality_score,
    request_logs.upstream_finish_reason,
    request_logs.tool_calls,
    request_logs.egress_protocol,
    request_logs.request_checksum,
    request_logs.response_checksum,
    request_logs.transform_rule_id
   FROM public.request_logs
UNION ALL
 SELECT request_logs_archive.id,
    request_logs_archive.request_id,
    request_logs_archive.ts,
    request_logs_archive.tenant_id,
    request_logs_archive.application_id,
    request_logs_archive.api_key_id,
    request_logs_archive.end_user_id,
    request_logs_archive.client_model,
    request_logs_archive.outbound_model,
    request_logs_archive.credential_id,
    request_logs_archive.provider_id,
    request_logs_archive.canonical_id,
    request_logs_archive.client_profile,
    request_logs_archive.request_mode,
    request_logs_archive.prompt_tokens,
    request_logs_archive.completion_tokens,
    request_logs_archive.total_tokens,
    request_logs_archive.cost_usd,
    request_logs_archive.latency_ms,
    request_logs_archive.success,
    request_logs_archive.error_kind,
    request_logs_archive.search_text,
    request_logs_archive.cache_read_tokens,
    request_logs_archive.cache_write_tokens,
    request_logs_archive.identity_hash,
    request_logs_archive.virtual_client_id,
    request_logs_archive.virtual_ip,
    request_logs_archive.virtual_mac,
    request_logs_archive.affinity_hit,
    request_logs_archive.stream_first_chunk_ms,
    request_logs_archive.stream_chunk_count,
    request_logs_archive.stream_chunk_errors,
    request_logs_archive.stream_chunks_sent,
    request_logs_archive.client_endpoint,
    request_logs_archive.client_timeout,
    request_logs_archive.failure_stage,
    request_logs_archive.failure_detail_code,
    request_logs_archive.request_preview,
    request_logs_archive.transform_summary,
    request_logs_archive.response_preview,
    request_logs_archive.stream_done_received,
    request_logs_archive.request_body,
    request_logs_archive.response_body,
    request_logs_archive.cost_display,
    request_logs_archive.cost_currency,
    request_logs_archive.usage_source,
    request_logs_archive.gw_session_id,
    request_logs_archive.gw_task_id,
    request_logs_archive.request_status,
    request_logs_archive.api_key_prefix,
    request_logs_archive.owner_user,
    request_logs_archive.application_code,
    request_logs_archive.key_alias,
    request_logs_archive.api_key_owner_user,
    request_logs_archive.is_auto_request,
    request_logs_archive.task_type,
    request_logs_archive.auto_profile,
    request_logs_archive.auto_decision,
    request_logs_archive.auto_confidence,
    request_logs_archive.work_type,
    request_logs_archive.task_type_chosen,
    request_logs_archive.confidence_num,
    request_logs_archive.model_chosen,
    request_logs_archive.strategy_used,
    request_logs_archive.credits_charged,
    request_logs_archive.parent_request_id,
    request_logs_archive.compression_reason,
    request_logs_archive.compression_strategy,
    request_logs_archive.compression_meta,
    request_logs_archive.outbound_body,
    request_logs_archive.outbound_msg_count,
    request_logs_archive.outbound_token_est,
    request_logs_archive.outbound_msg_hashes,
    request_logs_archive.quality_flags,
    request_logs_archive.quality_fix_actions,
    request_logs_archive.quality_score,
    request_logs_archive.upstream_finish_reason,
    request_logs_archive.tool_calls,
    request_logs_archive.egress_protocol,
    request_logs_archive.request_checksum,
    request_logs_archive.response_checksum,
    request_logs_archive.transform_rule_id
   FROM public.request_logs_archive;


--
-- Name: request_logs_default; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_default (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    client_endpoint text,
    client_timeout boolean,
    stream_chunk_errors integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_wal; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal (
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
    compression_meta jsonb
)
PARTITION BY RANGE (created_at);


--
-- Name: TABLE request_wal; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_wal IS 'Request WAL: synchronous initial log + async batch updates for request lifecycle';


--
-- Name: request_wal_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_2026_06 (
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
    compression_meta jsonb
);


--
-- Name: request_wal_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_2026_07 (
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
    compression_meta jsonb
);


--
-- Name: request_wal_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE request_wal_bodies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_wal_bodies IS 'Large outbound bodies separated for performance';


--
-- Name: route_decisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.route_decisions (
    id bigint NOT NULL,
    request_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text,
    api_key_id bigint,
    canonical_id bigint,
    selected_credential_id bigint,
    candidates_json jsonb,
    reason text,
    sticky_hit boolean
);


--
-- Name: route_decisions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.route_decisions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: route_decisions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.route_decisions_id_seq OWNED BY public.route_decisions.id;


--
-- Name: routing_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target_type text,
    target_id bigint,
    before_json jsonb,
    after_json jsonb
);


--
-- Name: routing_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_audit_log_id_seq OWNED BY public.routing_audit_log.id;


--
-- Name: routing_decision_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_decision_log (
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_id uuid NOT NULL,
    idempotency_key text,
    tenant_id text,
    api_key_id bigint,
    model text NOT NULL,
    chosen_credential_id bigint,
    chosen_provider_id bigint,
    tier smallint,
    candidates_tried smallint,
    latency_ms integer,
    success boolean NOT NULL,
    error_class text,
    prompt_tokens integer,
    completion_tokens integer,
    cost_usd numeric(12,6),
    request_bytes integer,
    response_bytes integer,
    client_model text,
    resolved_raw_model text,
    sticky_hit boolean,
    client_profile text,
    outbound_model text,
    request_mode text,
    identity_hash text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    resolution_path text,
    canonical_model text,
    resolution_raw_models jsonb,
    decision_trace jsonb
);


--
-- Name: routing_overrides; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_overrides (
    id bigint NOT NULL,
    task_type text NOT NULL,
    profile text DEFAULT ''::text NOT NULL,
    mode text NOT NULL,
    model_chosen text,
    reason text DEFAULT ''::text NOT NULL,
    created_by text,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT routing_overrides_mode_check CHECK ((mode = ANY (ARRAY['pin'::text, 'ban'::text])))
);


--
-- Name: routing_overrides_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_overrides_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    override_id bigint,
    task_type text,
    profile text,
    mode text,
    model_chosen text,
    reason text,
    expires_at timestamp with time zone,
    old_expires_at timestamp with time zone,
    actor text,
    CONSTRAINT routing_overrides_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text])))
);


--
-- Name: routing_overrides_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_overrides_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_overrides_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_overrides_audit_id_seq OWNED BY public.routing_overrides_audit.id;


--
-- Name: routing_overrides_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_overrides_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_overrides_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_overrides_id_seq OWNED BY public.routing_overrides.id;


--
-- Name: routing_policy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_policy (
    id smallint DEFAULT 1 NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    weights_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    sticky_ttl_seconds integer DEFAULT 1800 NOT NULL,
    local_bonus numeric(4,3) DEFAULT 0.000 NOT NULL,
    notes text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    algorithm_version smallint DEFAULT 2,
    retry_per_credential smallint DEFAULT 1,
    tier_fallback_max smallint DEFAULT 4,
    slot_soft_limit_ratio numeric(3,2) DEFAULT 1.00,
    slot_hard_limit_ratio numeric(3,2) DEFAULT 1.50,
    slot_wait_max_ms smallint DEFAULT 200,
    circuit_open_seconds integer DEFAULT 300,
    circuit_failure_threshold smallint DEFAULT 5,
    circuit_max_open_seconds integer DEFAULT 1800,
    featured_models text[] DEFAULT ARRAY['gpt-4o'::text, 'gpt-4o-mini'::text, 'claude-3-5-sonnet-20241022'::text, 'claude-3-7-sonnet-20250219'::text, 'gemini-2.0-flash'::text, 'gemini-1.5-pro'::text, 'deepseek-chat'::text, 'qwen-plus'::text],
    transient_fail_threshold integer DEFAULT 2 NOT NULL,
    stats_window_minutes integer DEFAULT 10,
    stats_update_interval_seconds integer DEFAULT 60,
    scoring_weights_json jsonb DEFAULT '{"price": 10, "session_load": 5, "failure_penalty": 20, "default_price_cny": 5.0, "default_price_usd": 5.0}'::jsonb,
    CONSTRAINT routing_policy_id_check CHECK ((id = 1)),
    CONSTRAINT routing_policy_transient_fail_threshold_check CHECK (((transient_fail_threshold >= 0) AND (transient_fail_threshold <= 10)))
);


--
-- Name: schema_migration_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migration_audit (
    migration_id text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL,
    row_count bigint DEFAULT 0 NOT NULL,
    note text DEFAULT ''::text NOT NULL
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version text NOT NULL,
    description text,
    applied_at timestamp with time zone DEFAULT now()
);


--
-- Name: security_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    event_kind text NOT NULL,
    api_key_id bigint,
    internal_service_id text,
    actor text,
    tenant_id text,
    remote_ip inet,
    detail_json jsonb,
    CONSTRAINT security_audit_log_event_kind_check CHECK ((event_kind = ANY (ARRAY['key_created'::text, 'key_disabled'::text, 'key_throttled'::text, 'key_unthrottled'::text, 'key_revoked'::text, 'key_revealed'::text, 'auth_failed'::text, 'auth_expired'::text, 'admin_login_failed'::text, 'key_reencrypted'::text, 'hmac_sig_failed'::text, 'hmac_nonce_replay'::text, 'hmac_timestamp_bad'::text, 'rate_limited'::text, 'anomaly_spike'::text])))
);


--
-- Name: security_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.security_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: security_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.security_audit_log_id_seq OWNED BY public.security_audit_log.id;


--
-- Name: session_memora_extraction_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_memora_extraction_log (
    task_id text NOT NULL,
    extracted_at timestamp with time zone DEFAULT now() NOT NULL,
    written integer DEFAULT 0 NOT NULL,
    skipped_noise integer DEFAULT 0 NOT NULL,
    skipped_duplicate integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'ok'::text NOT NULL,
    detail jsonb
);


--
-- Name: session_titles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_titles (
    task_id text NOT NULL,
    scoped_session_id text DEFAULT ''::text NOT NULL,
    title text NOT NULL,
    generated_at timestamp with time zone DEFAULT now() NOT NULL,
    model text,
    api_key_id integer
);


--
-- Name: settings_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_audit (
    id bigint NOT NULL,
    setting_key character varying(128) NOT NULL,
    tenant_id character varying(64),
    action character varying(16) NOT NULL,
    old_value jsonb,
    new_value jsonb,
    operator_user character varying(64) NOT NULL,
    operator_role character varying(32) NOT NULL,
    confirm_token character varying(64),
    client_ip character varying(45),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.settings_audit FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE settings_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_audit IS '设置修改审计日志（bg/settings_audit_cleaner.go 每 24h 清理 7 天前的数据）';


--
-- Name: COLUMN settings_audit.action; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.settings_audit.action IS 'update / rollback / delete';


--
-- Name: settings_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.settings_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: settings_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.settings_audit_id_seq OWNED BY public.settings_audit.id;


--
-- Name: settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_kv (
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    scope character varying(16) DEFAULT 'platform'::character varying NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);


--
-- Name: TABLE settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_kv IS '平台级运行时设置（Q2: 立即生效）';


--
-- Name: COLUMN settings_kv.prev_value; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.settings_kv.prev_value IS '上次的值，用于一键回滚';


--
-- Name: sticky_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sticky_sessions (
    sticky_key text NOT NULL,
    credential_id bigint NOT NULL,
    set_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    canonical_id bigint,
    last_request_id text
);


--
-- Name: subscription_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.subscription_plans (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    tier character varying(16) NOT NULL,
    name character varying(128) NOT NULL,
    price_cents integer NOT NULL,
    monthly_credits bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT subscription_plans_tier_check CHECK (((tier)::text = ANY (ARRAY[('basic'::character varying)::text, ('pro'::character varying)::text, ('max'::character varying)::text])))
);


--
-- Name: subscription_plans_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.subscription_plans_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: subscription_plans_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.subscription_plans_id_seq OWNED BY public.subscription_plans.id;


--
-- Name: system_identity_pool; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_identity_pool (
    id integer DEFAULT 1 NOT NULL,
    max_identities integer DEFAULT 10000 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text,
    CONSTRAINT system_identity_pool_id_check CHECK ((id = 1))
);


--
-- Name: TABLE system_identity_pool; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.system_identity_pool IS 'Global cap on total distinct end-user identities the gateway will accept. Once this many unique fingerprints are active, new connections must reuse an existing fingerprint (round-robin among least-recently-used).';


--
-- Name: tenant_credit_wallets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_credit_wallets (
    tenant_id character varying(64) NOT NULL,
    balance_credits bigint DEFAULT 0 NOT NULL,
    locked_credits bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    granted_balance bigint DEFAULT 0 NOT NULL,
    purchased_balance bigint DEFAULT 0 NOT NULL
);


--
-- Name: tenant_model_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_model_policies (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    canonical_name text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    created_by character varying(128) DEFAULT ''::character varying NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by character varying(128),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_model_policies_canonical_name_check CHECK ((canonical_name <> ''::text))
);

ALTER TABLE ONLY public.tenant_model_policies FORCE ROW LEVEL SECURITY;


--
-- Name: tenant_model_policies_active; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.tenant_model_policies_active AS
 SELECT tenant_model_policies.id,
    tenant_model_policies.tenant_id,
    tenant_model_policies.canonical_name,
    tenant_model_policies.reason,
    tenant_model_policies.created_by,
    tenant_model_policies.created_at,
    tenant_model_policies.updated_at
   FROM public.tenant_model_policies
  WHERE (tenant_model_policies.deleted_at IS NULL);


--
-- Name: tenant_model_policies_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_model_policies_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    policy_id bigint,
    tenant_id text,
    canonical_name text,
    reason text,
    actor text,
    CONSTRAINT tenant_model_policies_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text, 'undelete'::text])))
);

ALTER TABLE ONLY public.tenant_model_policies_audit FORCE ROW LEVEL SECURITY;


--
-- Name: tenant_model_policies_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_model_policies_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_model_policies_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_model_policies_audit_id_seq OWNED BY public.tenant_model_policies_audit.id;


--
-- Name: tenant_model_policies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_model_policies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_model_policies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_model_policies_id_seq OWNED BY public.tenant_model_policies.id;


--
-- Name: tenant_settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_settings_kv (
    tenant_id character varying(64) NOT NULL,
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);

ALTER TABLE ONLY public.tenant_settings_kv FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE tenant_settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tenant_settings_kv IS '租户级运行时设置（Q3）';


--
-- Name: tenant_subscriptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_subscriptions (
    id integer NOT NULL,
    tenant_id character varying(64) NOT NULL,
    plan_id integer NOT NULL,
    status character varying(32) DEFAULT 'active'::character varying NOT NULL,
    period_start timestamp with time zone NOT NULL,
    period_end timestamp with time zone NOT NULL,
    quota_remaining bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_subscriptions_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('active'::character varying)::text, ('expired'::character varying)::text, ('cancelled'::character varying)::text])))
);


--
-- Name: tenant_subscriptions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_subscriptions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_subscriptions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_subscriptions_id_seq OWNED BY public.tenant_subscriptions.id;


--
-- Name: tenant_tool_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_tool_policies (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    tool_pattern character varying(128) NOT NULL,
    policy_type character varying(16) NOT NULL,
    reason character varying(256),
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by character varying(128),
    CONSTRAINT chk_policy_type CHECK (((policy_type)::text = ANY (ARRAY[('allow'::character varying)::text, ('deny'::character varying)::text])))
);

ALTER TABLE ONLY public.tenant_tool_policies FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE tenant_tool_policies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tenant_tool_policies IS 'Tenant-level tool access policies (Phase 3.4: 权限控制)';


--
-- Name: COLUMN tenant_tool_policies.tool_pattern; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tenant_tool_policies.tool_pattern IS 'Tool pattern: exact match (filesystem.read_file) or wildcard (filesystem.*)';


--
-- Name: COLUMN tenant_tool_policies.policy_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tenant_tool_policies.policy_type IS 'Policy type: allow (whitelist) or deny (blacklist)';


--
-- Name: COLUMN tenant_tool_policies.reason; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tenant_tool_policies.reason IS 'Reason for this policy (audit trail)';


--
-- Name: tenant_tool_policies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_tool_policies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_tool_policies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_tool_policies_id_seq OWNED BY public.tenant_tool_policies.id;


--
-- Name: tenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenants (
    code character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    status character varying(32) DEFAULT 'active'::character varying NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    contact_email character varying(256) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenants_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('trial'::character varying)::text, ('suspended'::character varying)::text, ('expired'::character varying)::text, ('disabled'::character varying)::text])))
);


SET default_table_access_method = columnar;

--
-- Name: test_columnar_new; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.test_columnar_new (
    id integer NOT NULL,
    tenant_id text,
    model text,
    prompt_tokens integer,
    completion_tokens integer,
    created_at timestamp with time zone DEFAULT now()
);


SET default_table_access_method = heap;

--
-- Name: token_audit_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.token_audit_events (
    id bigint NOT NULL,
    request_id text NOT NULL,
    credential_id bigint NOT NULL,
    claimed_tokens integer,
    estimated_tokens integer,
    delta_pct numeric(6,3),
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: token_audit_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.token_audit_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: token_audit_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.token_audit_events_id_seq OWNED BY public.token_audit_events.id;


SET default_table_access_method = columnar;

--
-- Name: tool_call_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_call_events (
    id bigint,
    tool_id character varying(128),
    tenant_id character varying(64),
    request_id character varying(64),
    api_key character varying(64),
    status character varying(16),
    latency_ms integer,
    error_code character varying(64),
    called_at timestamp with time zone
);


SET default_table_access_method = heap;

--
-- Name: tool_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_categories (
    id character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    description text,
    enabled boolean DEFAULT true,
    display_order integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE tool_categories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_categories IS 'Phase 2: Tool category definitions for layered loading';


--
-- Name: tool_registry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_registry (
    id integer NOT NULL,
    category character varying(64) NOT NULL,
    tool_name character varying(128) NOT NULL,
    tool_definition jsonb NOT NULL,
    enabled boolean DEFAULT true,
    priority integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying,
    version integer DEFAULT 1,
    deprecation_date timestamp with time zone,
    min_client_version character varying(32),
    breaking_changes jsonb DEFAULT '[]'::jsonb,
    superseded_by character varying(128)
);


--
-- Name: TABLE tool_registry; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_registry IS 'Phase 2: Centralized tool definition registry';


--
-- Name: COLUMN tool_registry.tool_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.tool_id IS 'Phase 3: Unique tool identifier (category.tool_name)';


--
-- Name: COLUMN tool_registry.tenant_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.tenant_id IS 'Phase 3: Tenant isolation (default = global shared)';


--
-- Name: COLUMN tool_registry.version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.version IS 'Tool version (Phase 3.2: 多版本共存)';


--
-- Name: COLUMN tool_registry.deprecation_date; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.deprecation_date IS 'Deprecated after this date (Phase 3.2: 版本管理)';


--
-- Name: COLUMN tool_registry.min_client_version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.min_client_version IS 'Minimum client version required (Phase 3.2: 版本管理)';


--
-- Name: COLUMN tool_registry.breaking_changes; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.breaking_changes IS 'List of breaking changes in this version (Phase 3.2: 版本管理)';


--
-- Name: COLUMN tool_registry.superseded_by; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.superseded_by IS 'Newer tool_id that replaces this version (Phase 3.2: 版本管理)';


--
-- Name: tool_registry_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_registry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_registry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_registry_id_seq OWNED BY public.tool_registry.id;


--
-- Name: tool_usage_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats (
    id bigint NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
)
PARTITION BY RANGE (created_at);


--
-- Name: tool_usage_stats_partitioned_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_usage_stats_partitioned_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_usage_stats_partitioned_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_usage_stats_partitioned_id_seq OWNED BY public.tool_usage_stats.id;


--
-- Name: tool_usage_stats_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats_2026_06 (
    id bigint DEFAULT nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
);


--
-- Name: tool_usage_stats_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats_2026_07 (
    id bigint DEFAULT nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
);


--
-- Name: tool_usage_stats_2026_08; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats_2026_08 (
    id bigint DEFAULT nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) NOT NULL,
    tool_id character varying NOT NULL,
    tenant_id character varying NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms integer,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
);


--
-- Name: tool_usage_stats_old; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats_old (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    usage_date date DEFAULT CURRENT_DATE NOT NULL,
    call_count bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.tool_usage_stats_old FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE tool_usage_stats_old; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_usage_stats_old IS 'Tool usage statistics (Phase 3.3: 使用统计)';


--
-- Name: COLUMN tool_usage_stats_old.call_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_usage_stats_old.call_count IS 'Total call count for this tool on this day';


--
-- Name: COLUMN tool_usage_stats_old.success_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_usage_stats_old.success_count IS 'Successful call count';


--
-- Name: COLUMN tool_usage_stats_old.error_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_usage_stats_old.error_count IS 'Failed call count';


--
-- Name: tool_usage_stats_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_usage_stats_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_usage_stats_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_usage_stats_id_seq OWNED BY public.tool_usage_stats_old.id;


--
-- Name: topup_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.topup_packages (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    tier character varying(16) NOT NULL,
    name character varying(128) NOT NULL,
    price_cents integer NOT NULL,
    credits_amount bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT topup_packages_tier_check CHECK (((tier)::text = ANY (ARRAY[('small'::character varying)::text, ('medium'::character varying)::text, ('large'::character varying)::text])))
);


--
-- Name: topup_packages_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.topup_packages_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: topup_packages_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.topup_packages_id_seq OWNED BY public.topup_packages.id;


--
-- Name: tuning_params; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_params (
    key text NOT NULL,
    value jsonb NOT NULL,
    category text NOT NULL,
    source text DEFAULT 'default'::text NOT NULL,
    confidence numeric(4,3) DEFAULT 1.0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    description text,
    applied_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: tuning_proposals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_proposals (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    category text NOT NULL,
    task_type text,
    proposal jsonb NOT NULL,
    evidence jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    applied_at timestamp with time zone,
    review_note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tuning_proposals_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'applied'::text, 'expired'::text])))
);


--
-- Name: TABLE tuning_proposals; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tuning_proposals IS 'Auto-generated tuning proposals from feedback analysis. Require admin approval before applying to hot path.';


--
-- Name: tuning_proposals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tuning_proposals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tuning_proposals_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tuning_proposals_id_seq OWNED BY public.tuning_proposals.id;


--
-- Name: tuning_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_signals (
    id bigint NOT NULL,
    request_id text NOT NULL,
    session_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    task_type text NOT NULL,
    classifier text NOT NULL,
    confidence numeric(4,3),
    chosen_model text,
    canonical_id integer,
    success_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    cost_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    drift_flag boolean DEFAULT false NOT NULL,
    quality_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_ms integer,
    cost_usd numeric(10,6),
    prompt_tokens integer,
    completion_tokens integer,
    signal_payload jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    strategy text DEFAULT 'pattern_layered'::text NOT NULL,
    CONSTRAINT tuning_signals_strategy_check CHECK ((strategy = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text])))
);


--
-- Name: TABLE tuning_signals; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tuning_signals IS 'Implicit feedback signals for auto-route tuning. Written async per-request, analyzed daily by feedback_analyzer.';


--
-- Name: tuning_signals_5m; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_5m AS
 SELECT (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)) AS bucket,
    tuning_signals.task_type,
    tuning_signals.classifier,
    count(*) AS total,
    avg(tuning_signals.quality_score) AS avg_quality,
    avg(tuning_signals.success_score) AS avg_success,
    avg(tuning_signals.latency_score) AS avg_latency,
    avg(tuning_signals.cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN tuning_signals.drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (tuning_signals.ts >= (now() - '7 days'::interval))
  GROUP BY (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)), tuning_signals.task_type, tuning_signals.classifier
  WITH NO DATA;


--
-- Name: tuning_signals_daily; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_daily AS
 SELECT date_trunc('day'::text, tuning_signals.ts) AS bucket,
    tuning_signals.task_type,
    tuning_signals.classifier,
    count(*) AS total,
    avg(tuning_signals.quality_score) AS avg_quality,
    avg(tuning_signals.success_score) AS avg_success,
    avg(tuning_signals.latency_score) AS avg_latency,
    avg(tuning_signals.cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN tuning_signals.drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (tuning_signals.ts >= (now() - '90 days'::interval))
  GROUP BY (date_trunc('day'::text, tuning_signals.ts)), tuning_signals.task_type, tuning_signals.classifier
  WITH NO DATA;


--
-- Name: tuning_signals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tuning_signals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tuning_signals_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tuning_signals_id_seq OWNED BY public.tuning_signals.id;


--
-- Name: usage_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
)
PARTITION BY RANGE (ts);


--
-- Name: usage_ledger_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger_2026_06 (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
);


--
-- Name: usage_ledger_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger_2026_07 (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
);


--
-- Name: usage_ledger_2026_08; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger_2026_08 (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
);


--
-- Name: usage_ledger_old; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger_old (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
);


--
-- Name: usage_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    requests bigint DEFAULT 0 NOT NULL,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    errors bigint DEFAULT 0 NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    username character varying(128) NOT NULL,
    password_hash character varying(256) NOT NULL,
    display_name character varying(128) DEFAULT ''::character varying NOT NULL,
    email character varying(256) DEFAULT ''::character varying NOT NULL,
    role character varying(32) DEFAULT 'tenant_admin'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;


--
-- Name: v_fp_slot_policy; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_fp_slot_policy AS
 SELECT COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::boolean AS bool
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_enabled'::text)), true) AS enabled,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_max_per_credential'::text)), 100) AS max_per_credential,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::numeric AS "numeric"
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_default_ratio'::text)), 0.25) AS default_ratio,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_client_fingerprint_ttl_days'::text)), 30) AS client_ttl_days,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_max_total_clients'::text)), 10000) AS max_total_clients;


--
-- Name: VIEW v_fp_slot_policy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_fp_slot_policy IS 'Active fingerprint-slot policy derived from settings_kv. Used by admin UI and the credentialfpslot manager at boot.';


--
-- Name: v_idle_credential_slots; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_idle_credential_slots AS
 SELECT model_probe_state.credential_id,
    model_probe_state.raw_model_name,
    model_probe_state.state,
    model_probe_state.consecutive_failures,
    model_probe_state.last_attempt_at,
    (EXTRACT(epoch FROM (now() - model_probe_state.last_attempt_at)))::integer AS idle_seconds
   FROM public.model_probe_state
  WHERE (model_probe_state.state <> 'broken_confirmed'::text);


--
-- Name: VIEW v_idle_credential_slots; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_idle_credential_slots IS 'For monitoring: per-binding rows with last_attempt_at and idle_seconds. Used by admin dashboards to spot slots that need reclaim.';


--
-- Name: v_routable_credential_models; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_routable_credential_models AS
 SELECT cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
        CASE
            WHEN (NOT p.enabled) THEN 'provider_disabled'::text
            WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'::text
            WHEN (c.status <> 'active'::text) THEN ('credential_status_'::text || c.status)
            WHEN (c.lifecycle_status <> 'active'::text) THEN ('lifecycle_'::text || c.lifecycle_status)
            WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'::text
            WHEN (c.availability_state = 'cooling'::text) THEN 'availability_cooling'::text
            WHEN (c.availability_state = 'rate_limited'::text) THEN 'availability_rate_limited'::text
            WHEN (c.availability_state = 'auth_failed'::text) THEN 'availability_auth_failed'::text
            WHEN (c.availability_state = 'unreachable'::text) THEN 'availability_unreachable'::text
            WHEN (c.availability_state = 'suspended'::text) THEN 'availability_suspended'::text
            WHEN (c.quota_state = ANY (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) THEN ('quota_'::text || c.quota_state)
            WHEN ((c.health_status = 'unreachable'::text) AND (c.health_checked_at > (now() - '01:00:00'::interval))) THEN 'recent_probe_unreachable'::text
            WHEN (NOT pm.available) THEN 'model_unavailable'::text
            WHEN (cmb.unavailable_reason = 'manual'::text) THEN 'model_manual_disabled'::text
            WHEN (NOT cmb.available) THEN 'binding_unavailable'::text
            ELSE NULL::text
        END AS unavailable_reason,
    (p.enabled AND (COALESCE(p.manual_disabled, false) = false) AND (c.status = 'active'::text) AND (c.lifecycle_status = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (c.availability_state = 'ready'::text) AND (c.quota_state <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) AND (pm.available = true) AND (cmb.available = true) AND (cmb.unavailable_reason IS DISTINCT FROM 'manual'::text) AND (COALESCE(c.health_status, 'unknown'::text) = ANY (ARRAY['healthy'::text, 'unknown'::text]))) AS is_routable,
    (((((cmb.manual_priority * 100))::numeric + (COALESCE(cmb.success_rate, 0.5) * (50)::numeric)) - (COALESCE(cmb.unit_price_in_per_1m, (0)::numeric) * 0.001)) - ((COALESCE(cmb.p95_latency_ms, 1000))::numeric * 0.01)) AS routing_score
   FROM (((public.credential_model_bindings cmb
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));


--
-- Name: work_type_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_type_config (
    key text NOT NULL,
    label text NOT NULL,
    category text NOT NULL,
    l1_task_type text NOT NULL,
    default_profile text DEFAULT 'smart'::text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    prompt_keywords text[] DEFAULT '{}'::text[] NOT NULL,
    acc_task_type text,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    synced_from_acc_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    system_prompt text,
    CONSTRAINT work_type_config_default_profile_check CHECK ((default_profile = ANY (ARRAY['smart'::text, 'speed_first'::text, 'cost_first'::text])))
);


--
-- Name: TABLE work_type_config; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.work_type_config IS 'Work type definitions (P1 seed; Phase 3 sync from ACC)';


--
-- Name: work_type_model_route; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_type_model_route (
    id integer NOT NULL,
    work_type_key text NOT NULL,
    canonical_name text NOT NULL,
    weight numeric(5,2) DEFAULT 1.0 NOT NULL,
    min_score numeric(8,4) DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    tier text DEFAULT 'secondary'::text NOT NULL,
    task_quality_score numeric(5,2) DEFAULT 0 NOT NULL,
    CONSTRAINT work_type_model_route_task_quality_score_check CHECK (((task_quality_score >= (0)::numeric) AND (task_quality_score <= (100)::numeric))),
    CONSTRAINT work_type_model_route_tier_check CHECK ((tier = ANY (ARRAY['primary'::text, 'secondary'::text, 'fallback'::text])))
);


--
-- Name: TABLE work_type_model_route; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.work_type_model_route IS 'Preferred model routes per work type (L1 selection hints)';


--
-- Name: COLUMN work_type_model_route.weight; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.work_type_model_route.weight IS '同 tier 内的排序权重（tier 间优先级：primary > secondary > fallback，tier 内按 weight DESC 排）';


--
-- Name: COLUMN work_type_model_route.tier; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.work_type_model_route.tier IS '三级偏好：primary（首选）/ secondary（次选）/ fallback（兜底）。Index.Recommend 先推荐 primary，全挂时用 secondary，最后才 fallback';


--
-- Name: COLUMN work_type_model_route.task_quality_score; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.work_type_model_route.task_quality_score IS '该模型在该任务上的人工评分覆盖（0-100）。0 表示用公式计算 scoreStrengthMatch；>0 则直接用该分数';


--
-- Name: work_type_model_route_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.work_type_model_route_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: work_type_model_route_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.work_type_model_route_id_seq OWNED BY public.work_type_model_route.id;


--
-- Name: credit_ledger_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ATTACH PARTITION public.credit_ledger_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: credit_ledger_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ATTACH PARTITION public.credit_ledger_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: credit_ledger_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ATTACH PARTITION public.credit_ledger_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');


--
-- Name: request_logs_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: request_logs_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: request_logs_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');


--
-- Name: request_logs_default; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_default DEFAULT;


--
-- Name: request_wal_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal ATTACH PARTITION public.request_wal_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: request_wal_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal ATTACH PARTITION public.request_wal_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: tool_usage_stats_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats ATTACH PARTITION public.tool_usage_stats_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: tool_usage_stats_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats ATTACH PARTITION public.tool_usage_stats_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: tool_usage_stats_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats ATTACH PARTITION public.tool_usage_stats_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');


--
-- Name: usage_ledger_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ATTACH PARTITION public.usage_ledger_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: usage_ledger_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ATTACH PARTITION public.usage_ledger_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: usage_ledger_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ATTACH PARTITION public.usage_ledger_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');


--
-- Name: agents id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents ALTER COLUMN id SET DEFAULT nextval('public.agents_id_seq'::regclass);


--
-- Name: api_keys id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);


--
-- Name: applications id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications ALTER COLUMN id SET DEFAULT nextval('public.applications_id_seq'::regclass);


--
-- Name: armor_judgments id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.armor_judgments ALTER COLUMN id SET DEFAULT nextval('public.armor_judgments_id_seq'::regclass);


--
-- Name: auto_tune_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auto_tune_audit ALTER COLUMN id SET DEFAULT nextval('public.auto_tune_audit_id_seq'::regclass);


--
-- Name: background_tasks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_tasks ALTER COLUMN id SET DEFAULT nextval('public.background_tasks_id_seq'::regclass);


--
-- Name: billing_orders id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.billing_orders ALTER COLUMN id SET DEFAULT nextval('public.billing_orders_id_seq'::regclass);


--
-- Name: credential_capabilities id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_capabilities ALTER COLUMN id SET DEFAULT nextval('public.credential_capabilities_id_seq'::regclass);


--
-- Name: credential_health_checks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_health_checks ALTER COLUMN id SET DEFAULT nextval('public.credential_health_checks_id_seq'::regclass);


--
-- Name: credential_model_bindings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings ALTER COLUMN id SET DEFAULT nextval('public.credential_model_bindings_id_seq'::regclass);


--
-- Name: credential_quota_usage id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage ALTER COLUMN id SET DEFAULT nextval('public.credential_quota_usage_id_seq'::regclass);


--
-- Name: credential_quotas id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quotas ALTER COLUMN id SET DEFAULT nextval('public.credential_quotas_id_seq'::regclass);


--
-- Name: credentials id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials ALTER COLUMN id SET DEFAULT nextval('public.credentials_id_seq'::regclass);


--
-- Name: credit_ledger id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ALTER COLUMN id SET DEFAULT nextval('public.credit_ledger_partitioned_id_seq'::regclass);


--
-- Name: credit_ledger_old id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger_old ALTER COLUMN id SET DEFAULT nextval('public.credit_ledger_id_seq'::regclass);


--
-- Name: local_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models ALTER COLUMN id SET DEFAULT nextval('public.local_models_id_seq'::regclass);


--
-- Name: local_runtimes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes ALTER COLUMN id SET DEFAULT nextval('public.local_runtimes_id_seq'::regclass);


--
-- Name: model_aliases id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_aliases ALTER COLUMN id SET DEFAULT nextval('public.model_aliases_id_seq'::regclass);


--
-- Name: model_discovery_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_discovery_runs ALTER COLUMN id SET DEFAULT nextval('public.model_discovery_runs_id_seq'::regclass);


--
-- Name: model_fingerprints id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_fingerprints ALTER COLUMN id SET DEFAULT nextval('public.model_fingerprints_id_seq'::regclass);


--
-- Name: model_lifecycle_jobs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_lifecycle_jobs ALTER COLUMN id SET DEFAULT nextval('public.model_lifecycle_jobs_id_seq'::regclass);


--
-- Name: model_offers_legacy id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy ALTER COLUMN id SET DEFAULT nextval('public.model_offers_id_seq'::regclass);


--
-- Name: model_reconcile_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_reconcile_log ALTER COLUMN id SET DEFAULT nextval('public.model_reconcile_log_id_seq'::regclass);


--
-- Name: models_canonical id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical ALTER COLUMN id SET DEFAULT nextval('public.models_canonical_id_seq'::regclass);


--
-- Name: ops_model_offers_backup backup_id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_model_offers_backup ALTER COLUMN backup_id SET DEFAULT nextval('public.ops_model_offers_backup_backup_id_seq'::regclass);


--
-- Name: pricing_plans id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_plans ALTER COLUMN id SET DEFAULT nextval('public.pricing_plans_id_seq'::regclass);


--
-- Name: pricing_refresh_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_refresh_log ALTER COLUMN id SET DEFAULT nextval('public.pricing_refresh_log_id_seq'::regclass);


--
-- Name: provider_header_profiles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles ALTER COLUMN id SET DEFAULT nextval('public.provider_header_profiles_id_seq'::regclass);


--
-- Name: provider_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models ALTER COLUMN id SET DEFAULT nextval('public.provider_models_id_seq'::regclass);


--
-- Name: provider_scores id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_scores ALTER COLUMN id SET DEFAULT nextval('public.provider_scores_id_seq'::regclass);


--
-- Name: provider_settings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings ALTER COLUMN id SET DEFAULT nextval('public.provider_settings_id_seq'::regclass);


--
-- Name: providers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers ALTER COLUMN id SET DEFAULT nextval('public.providers_id_seq'::regclass);


--
-- Name: route_decisions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.route_decisions ALTER COLUMN id SET DEFAULT nextval('public.route_decisions_id_seq'::regclass);


--
-- Name: routing_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_audit_log ALTER COLUMN id SET DEFAULT nextval('public.routing_audit_log_id_seq'::regclass);


--
-- Name: routing_overrides id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides ALTER COLUMN id SET DEFAULT nextval('public.routing_overrides_id_seq'::regclass);


--
-- Name: routing_overrides_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides_audit ALTER COLUMN id SET DEFAULT nextval('public.routing_overrides_audit_id_seq'::regclass);


--
-- Name: security_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_audit_log ALTER COLUMN id SET DEFAULT nextval('public.security_audit_log_id_seq'::regclass);


--
-- Name: settings_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_audit ALTER COLUMN id SET DEFAULT nextval('public.settings_audit_id_seq'::regclass);


--
-- Name: subscription_plans id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_plans ALTER COLUMN id SET DEFAULT nextval('public.subscription_plans_id_seq'::regclass);


--
-- Name: tenant_model_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_id_seq'::regclass);


--
-- Name: tenant_model_policies_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies_audit ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_audit_id_seq'::regclass);


--
-- Name: tenant_subscriptions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_subscriptions ALTER COLUMN id SET DEFAULT nextval('public.tenant_subscriptions_id_seq'::regclass);


--
-- Name: tenant_tool_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_tool_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_tool_policies_id_seq'::regclass);


--
-- Name: token_audit_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_audit_events ALTER COLUMN id SET DEFAULT nextval('public.token_audit_events_id_seq'::regclass);


--
-- Name: tool_registry id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry ALTER COLUMN id SET DEFAULT nextval('public.tool_registry_id_seq'::regclass);


--
-- Name: tool_usage_stats id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats ALTER COLUMN id SET DEFAULT nextval('public.tool_usage_stats_partitioned_id_seq'::regclass);


--
-- Name: tool_usage_stats_old id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_old ALTER COLUMN id SET DEFAULT nextval('public.tool_usage_stats_id_seq'::regclass);


--
-- Name: topup_packages id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages ALTER COLUMN id SET DEFAULT nextval('public.topup_packages_id_seq'::regclass);


--
-- Name: tuning_proposals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_proposals ALTER COLUMN id SET DEFAULT nextval('public.tuning_proposals_id_seq'::regclass);


--
-- Name: tuning_signals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_signals ALTER COLUMN id SET DEFAULT nextval('public.tuning_signals_id_seq'::regclass);


--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);


--
-- Name: work_type_model_route id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route ALTER COLUMN id SET DEFAULT nextval('public.work_type_model_route_id_seq'::regclass);


--
-- Name: agents agents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);


--
-- Name: api_keys api_keys_key_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_key_hash_key UNIQUE (key_hash);


--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);


--
-- Name: applications applications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_pkey PRIMARY KEY (id);


--
-- Name: applications applications_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code);


--
-- Name: armor_judgments armor_judgments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.armor_judgments
    ADD CONSTRAINT armor_judgments_pkey PRIMARY KEY (id);


--
-- Name: billing_orders billing_orders_order_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.billing_orders
    ADD CONSTRAINT billing_orders_order_no_key UNIQUE (order_no);


--
-- Name: credential_model_bindings cmb_unique_credential_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings
    ADD CONSTRAINT cmb_unique_credential_model UNIQUE (credential_id, provider_model_id);


--
-- Name: credential_capabilities credential_capabilities_credential_id_capability_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_capabilities
    ADD CONSTRAINT credential_capabilities_credential_id_capability_key UNIQUE (credential_id, capability);


--
-- Name: credential_model_call_history credential_model_call_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_call_history
    ADD CONSTRAINT credential_model_call_history_pkey PRIMARY KEY (credential_id, raw_model, window_start);


--
-- Name: credential_model_index credential_model_index_bucket_cred_model_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_index
    ADD CONSTRAINT credential_model_index_bucket_cred_model_key UNIQUE (bucket, credential_id, raw_model);


--
-- Name: credential_quota_usage credential_quota_usage_quota_id_window_started_at_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage
    ADD CONSTRAINT credential_quota_usage_quota_id_window_started_at_key UNIQUE (quota_id, window_started_at);


--
-- Name: credential_quotas credential_quotas_credential_id_quota_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quotas
    ADD CONSTRAINT credential_quotas_credential_id_quota_name_key UNIQUE (credential_id, quota_name);


--
-- Name: credentials credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);


--
-- Name: credentials credentials_unique_provider_label; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_unique_provider_label UNIQUE (provider_id, tenant_id, label);


--
-- Name: credit_ledger credit_ledger_partitioned_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger
    ADD CONSTRAINT credit_ledger_partitioned_pkey PRIMARY KEY (id, created_at);


--
-- Name: credit_ledger_2026_06 credit_ledger_2026_06_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger_2026_06
    ADD CONSTRAINT credit_ledger_2026_06_pkey PRIMARY KEY (id, created_at);


--
-- Name: credit_ledger_2026_07 credit_ledger_2026_07_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger_2026_07
    ADD CONSTRAINT credit_ledger_2026_07_pkey PRIMARY KEY (id, created_at);


--
-- Name: credit_ledger_2026_08 credit_ledger_2026_08_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger_2026_08
    ADD CONSTRAINT credit_ledger_2026_08_pkey PRIMARY KEY (id, created_at);


--
-- Name: local_models local_models_runtime_id_raw_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models
    ADD CONSTRAINT local_models_runtime_id_raw_name_key UNIQUE (runtime_id, raw_name);


--
-- Name: local_runtimes local_runtimes_host_code_runtime_type_base_url_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes
    ADD CONSTRAINT local_runtimes_host_code_runtime_type_base_url_key UNIQUE (host_code, runtime_type, base_url);


--
-- Name: maas_settings maas_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maas_settings
    ADD CONSTRAINT maas_settings_pkey PRIMARY KEY (id);


--
-- Name: model_fingerprints model_fingerprints_credential_id_canonical_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_fingerprints
    ADD CONSTRAINT model_fingerprints_credential_id_canonical_id_key UNIQUE (credential_id, canonical_id);


--
-- Name: model_offers_legacy model_offers_credential_id_raw_model_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy
    ADD CONSTRAINT model_offers_credential_id_raw_model_name_key UNIQUE (credential_id, raw_model_name);


--
-- Name: model_probe_state model_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_state
    ADD CONSTRAINT model_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name);


--
-- Name: model_task_index model_task_index_bucket_canonical_task_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_task_index
    ADD CONSTRAINT model_task_index_bucket_canonical_task_key UNIQUE (bucket, canonical_id, task_type);


--
-- Name: models_canonical models_canonical_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical
    ADD CONSTRAINT models_canonical_canonical_name_key UNIQUE (canonical_name);


--
-- Name: passive_probe_state passive_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.passive_probe_state
    ADD CONSTRAINT passive_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name, error_kind);


--
-- Name: agent_relationships pk_agent_relationships; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_relationships
    ADD CONSTRAINT pk_agent_relationships PRIMARY KEY (src_agent_id, dst_agent_id, rel);


--
-- Name: asset_relationships pk_asset_relationships; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_relationships
    ADD CONSTRAINT pk_asset_relationships PRIMARY KEY (src_kind, src_ref_id, dst_kind, dst_ref_id, rel);


--
-- Name: assets pk_assets; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.assets
    ADD CONSTRAINT pk_assets PRIMARY KEY (kind, ref_id);


--
-- Name: provider_header_profiles provider_header_profiles_profile_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles
    ADD CONSTRAINT provider_header_profiles_profile_code_key UNIQUE (profile_code);


--
-- Name: provider_models provider_models_unique_provider_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models
    ADD CONSTRAINT provider_models_unique_provider_model UNIQUE (provider_id, raw_model_name);


--
-- Name: provider_quality_rollup provider_quality_rollup_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_rollup
    ADD CONSTRAINT provider_quality_rollup_pkey PRIMARY KEY (provider_id, bucket_start);


--
-- Name: provider_settings provider_settings_unique_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings
    ADD CONSTRAINT provider_settings_unique_key UNIQUE (provider_id, setting_key);


--
-- Name: providers providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_pkey PRIMARY KEY (id);


--
-- Name: providers providers_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_tenant_id_code_key UNIQUE (tenant_id, code);


--
-- Name: request_wal request_wal_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal
    ADD CONSTRAINT request_wal_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: request_wal_2026_06 request_wal_2026_06_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal_2026_06
    ADD CONSTRAINT request_wal_2026_06_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: request_wal_2026_07 request_wal_2026_07_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal_2026_07
    ADD CONSTRAINT request_wal_2026_07_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: subscription_plans subscription_plans_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_plans
    ADD CONSTRAINT subscription_plans_code_key UNIQUE (code);


--
-- Name: system_identity_pool system_identity_pool_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_identity_pool
    ADD CONSTRAINT system_identity_pool_pkey PRIMARY KEY (id);


--
-- Name: tenant_model_policies tenant_model_policies_tenant_id_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies
    ADD CONSTRAINT tenant_model_policies_tenant_id_canonical_name_key UNIQUE (tenant_id, canonical_name);


--
-- Name: tenants tenants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (code);


--
-- Name: tool_registry tool_registry_tool_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry
    ADD CONSTRAINT tool_registry_tool_name_key UNIQUE (tool_name);


--
-- Name: tool_usage_stats tool_usage_stats_partitioned_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT tool_usage_stats_partitioned_pkey PRIMARY KEY (id, created_at);


--
-- Name: tool_usage_stats_2026_06 tool_usage_stats_2026_06_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_06
    ADD CONSTRAINT tool_usage_stats_2026_06_pkey PRIMARY KEY (id, created_at);


--
-- Name: tool_usage_stats tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key UNIQUE (tool_id, tenant_id, usage_date, created_at);


--
-- Name: tool_usage_stats_2026_06 tool_usage_stats_2026_06_tool_id_tenant_id_usage_date_creat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_06
    ADD CONSTRAINT tool_usage_stats_2026_06_tool_id_tenant_id_usage_date_creat_key UNIQUE (tool_id, tenant_id, usage_date, created_at);


--
-- Name: tool_usage_stats_2026_07 tool_usage_stats_2026_07_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_07
    ADD CONSTRAINT tool_usage_stats_2026_07_pkey PRIMARY KEY (id, created_at);


--
-- Name: tool_usage_stats_2026_07 tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_07
    ADD CONSTRAINT tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key UNIQUE (tool_id, tenant_id, usage_date, created_at);


--
-- Name: tool_usage_stats_2026_08 tool_usage_stats_2026_08_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_08
    ADD CONSTRAINT tool_usage_stats_2026_08_pkey PRIMARY KEY (id, created_at);


--
-- Name: tool_usage_stats_2026_08 tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_2026_08
    ADD CONSTRAINT tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key UNIQUE (tool_id, tenant_id, usage_date, created_at);


--
-- Name: topup_packages topup_packages_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages
    ADD CONSTRAINT topup_packages_code_key UNIQUE (code);


--
-- Name: tenant_tool_policies uk_tenant_tool_policy; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_tool_policies
    ADD CONSTRAINT uk_tenant_tool_policy UNIQUE (tenant_id, tool_pattern);


--
-- Name: tool_usage_stats_old uk_tool_usage_stats; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats_old
    ADD CONSTRAINT uk_tool_usage_stats UNIQUE (tool_id, tenant_id, usage_date);


--
-- Name: usage_ledger usage_ledger_partitioned_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger
    ADD CONSTRAINT usage_ledger_partitioned_request_id_ts_key UNIQUE (request_id, ts);


--
-- Name: usage_ledger_2026_06 usage_ledger_2026_06_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_2026_06
    ADD CONSTRAINT usage_ledger_2026_06_request_id_ts_key UNIQUE (request_id, ts);


--
-- Name: usage_ledger_2026_07 usage_ledger_2026_07_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_2026_07
    ADD CONSTRAINT usage_ledger_2026_07_request_id_ts_key UNIQUE (request_id, ts);


--
-- Name: usage_ledger_2026_08 usage_ledger_2026_08_request_id_ts_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_2026_08
    ADD CONSTRAINT usage_ledger_2026_08_request_id_ts_key UNIQUE (request_id, ts);


--
-- Name: usage_ledger_old usage_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger_old
    ADD CONSTRAINT usage_ledger_pkey PRIMARY KEY (request_id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);


--
-- Name: work_type_config work_type_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_config
    ADD CONSTRAINT work_type_config_pkey PRIMARY KEY (key);


--
-- Name: work_type_model_route work_type_model_route_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route
    ADD CONSTRAINT work_type_model_route_pkey PRIMARY KEY (id);


--
-- Name: work_type_model_route work_type_model_route_work_type_key_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route
    ADD CONSTRAINT work_type_model_route_work_type_key_canonical_name_key UNIQUE (work_type_key, canonical_name);


--
-- Name: idx_credit_ledger_part_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_created ON ONLY public.credit_ledger USING btree (created_at);


--
-- Name: credit_ledger_2026_06_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_06_created_at_idx ON public.credit_ledger_2026_06 USING btree (created_at);


--
-- Name: idx_credit_ledger_part_ref; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_ref ON ONLY public.credit_ledger USING btree (ref_type, ref_id);


--
-- Name: credit_ledger_2026_06_ref_type_ref_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_06_ref_type_ref_id_idx ON public.credit_ledger_2026_06 USING btree (ref_type, ref_id);


--
-- Name: idx_credit_ledger_part_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_part_tenant ON ONLY public.credit_ledger USING btree (tenant_id, created_at);


--
-- Name: credit_ledger_2026_06_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_06_tenant_id_created_at_idx ON public.credit_ledger_2026_06 USING btree (tenant_id, created_at);


--
-- Name: credit_ledger_2026_07_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_07_created_at_idx ON public.credit_ledger_2026_07 USING btree (created_at);


--
-- Name: credit_ledger_2026_07_ref_type_ref_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_07_ref_type_ref_id_idx ON public.credit_ledger_2026_07 USING btree (ref_type, ref_id);


--
-- Name: credit_ledger_2026_07_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_07_tenant_id_created_at_idx ON public.credit_ledger_2026_07 USING btree (tenant_id, created_at);


--
-- Name: credit_ledger_2026_08_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_08_created_at_idx ON public.credit_ledger_2026_08 USING btree (created_at);


--
-- Name: credit_ledger_2026_08_ref_type_ref_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_08_ref_type_ref_id_idx ON public.credit_ledger_2026_08 USING btree (ref_type, ref_id);


--
-- Name: credit_ledger_2026_08_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credit_ledger_2026_08_tenant_id_created_at_idx ON public.credit_ledger_2026_08 USING btree (tenant_id, created_at);


--
-- Name: idx_agent_rel_dst; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agent_rel_dst ON public.agent_relationships USING btree (dst_agent_id);


--
-- Name: idx_agent_rel_src; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agent_rel_src ON public.agent_relationships USING btree (src_agent_id);


--
-- Name: idx_agents_capabilities; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_capabilities ON public.agents USING gin (capabilities jsonb_path_ops);


--
-- Name: idx_agents_heartbeat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_heartbeat ON public.agents USING btree (last_heartbeat) WHERE (last_heartbeat IS NOT NULL);


--
-- Name: idx_agents_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_kind ON public.agents USING btree (tenant_id, kind);


--
-- Name: idx_agents_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_tenant ON public.agents USING btree (tenant_id);


--
-- Name: idx_applications_tenant_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_applications_tenant_code ON public.applications USING btree (tenant_id, code) WHERE (enabled = true);


--
-- Name: idx_armor_judgments_request; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_armor_judgments_request ON public.armor_judgments USING btree (request_id);


--
-- Name: idx_armor_judgments_stats; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_armor_judgments_stats ON public.armor_judgments USING btree (check_type, decision);


--
-- Name: idx_armor_judgments_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_armor_judgments_tenant_time ON public.armor_judgments USING btree (tenant_id, created_at DESC);


--
-- Name: idx_asset_rel_dst; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_rel_dst ON public.asset_relationships USING btree (dst_kind, dst_ref_id);


--
-- Name: idx_asset_rel_src; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_rel_src ON public.asset_relationships USING btree (src_kind, src_ref_id);


--
-- Name: idx_assets_tags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_assets_tags ON public.assets USING gin (tags jsonb_path_ops);


--
-- Name: idx_assets_tenant_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_assets_tenant_kind ON public.assets USING btree (tenant_id, kind);


--
-- Name: idx_billing_orders_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_billing_orders_status ON public.billing_orders USING btree (status, created_at DESC);


--
-- Name: idx_billing_orders_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_billing_orders_tenant ON public.billing_orders USING btree (tenant_id, created_at DESC);


--
-- Name: idx_call_history_cred_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_cred_time ON public.credential_model_call_history USING btree (credential_id, window_start DESC);


--
-- Name: idx_call_history_errors; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_errors ON public.credential_model_call_history USING btree (credential_id, raw_model, window_start DESC) WHERE ((error_rate_limit_count > 0) OR (error_concurrent_count > 0));


--
-- Name: idx_call_history_model_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_model_time ON public.credential_model_call_history USING btree (raw_model, window_start DESC);


--
-- Name: idx_cmb_unavailable_recover_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_unavailable_recover_at ON public.credential_model_bindings USING btree (unavailable_recover_at) WHERE (available = false);


--
-- Name: idx_credentials_auto_limit; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_auto_limit ON public.credentials USING btree (concurrency_limit_auto) WHERE (concurrency_limit_auto IS NOT NULL);


--
-- Name: idx_credit_ledger_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_tenant_ts ON public.credit_ledger_old USING btree (tenant_id, created_at DESC);


--
-- Name: idx_model_probe_state_retry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_probe_state_retry ON public.model_probe_state USING btree (state, next_retry_at) WHERE (state = 'recovering'::text);


--
-- Name: idx_models_canonical_released; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_released ON public.models_canonical USING btree (released_at DESC NULLS LAST);


--
-- Name: idx_models_canonical_strengths; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_strengths ON public.models_canonical USING gin (strengths);


--
-- Name: idx_models_canonical_version_rank; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_version_rank ON public.models_canonical USING btree (version_rank);


--
-- Name: idx_mps_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_due ON public.model_probe_state USING btree (next_retry_at) WHERE (state = ANY (ARRAY['unknown'::text, 'recovering'::text]));


--
-- Name: idx_passive_probe_reviewing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_passive_probe_reviewing ON public.passive_probe_state USING btree (in_reviewing, reviewing_until) WHERE (in_reviewing = true);


--
-- Name: idx_provider_quality_rollup_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_quality_rollup_bucket ON public.provider_quality_rollup USING btree (bucket_start DESC);


--
-- Name: idx_provider_settings_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_settings_key ON public.provider_settings USING btree (setting_key) WHERE (enabled = true);


--
-- Name: idx_provider_settings_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_settings_provider ON public.provider_settings USING btree (provider_id) WHERE (enabled = true);


--
-- Name: idx_request_logs_client_model_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_client_model_trgm ON ONLY public.request_logs USING gin (client_model public.gin_trgm_ops);


--
-- Name: idx_request_logs_2026_06_client_model_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_2026_06_client_model_trgm ON public.request_logs_2026_06 USING gin (client_model public.gin_trgm_ops);


--
-- Name: idx_request_logs_2026_07_client_model_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_2026_07_client_model_trgm ON public.request_logs_2026_07 USING gin (client_model public.gin_trgm_ops);


--
-- Name: idx_request_logs_2026_08_client_model_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_2026_08_client_model_trgm ON public.request_logs_2026_08 USING gin (client_model public.gin_trgm_ops);


--
-- Name: idx_request_logs_credits_charged; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_credits_charged ON ONLY public.request_logs USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: idx_request_logs_default_client_model_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_default_client_model_trgm ON public.request_logs_default USING gin (client_model public.gin_trgm_ops);


--
-- Name: idx_request_logs_gw_session_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_gw_session_ts ON ONLY public.request_logs USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: idx_request_logs_gw_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_gw_task_ts ON ONLY public.request_logs USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: idx_request_logs_outbound_msg_count; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_outbound_msg_count ON ONLY public.request_logs USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: idx_request_logs_parent_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_parent_ts ON ONLY public.request_logs USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: idx_request_logs_provider_quality; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_quality ON ONLY public.request_logs USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: idx_request_logs_provider_tool_calls; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_tool_calls ON ONLY public.request_logs USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: idx_request_logs_quality_flags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_quality_flags ON ONLY public.request_logs USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: idx_request_logs_request_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_request_logs_request_id_unique ON ONLY public.request_logs USING btree (request_id);


--
-- Name: idx_request_logs_session_outbound; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_session_outbound ON ONLY public.request_logs USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: idx_request_logs_status_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_status_ts ON ONLY public.request_logs USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: idx_request_logs_tenant_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_tenant_task_ts ON ONLY public.request_logs USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: idx_request_logs_tool_calls; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_tool_calls ON ONLY public.request_logs USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: idx_request_logs_upstream_finish_reason; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_upstream_finish_reason ON ONLY public.request_logs USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: idx_request_logs_work_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_work_type ON ONLY public.request_logs USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: idx_routing_overrides_audit_actor_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_actor_ts ON public.routing_overrides_audit USING btree (actor, ts DESC) WHERE (actor IS NOT NULL);


--
-- Name: idx_routing_overrides_audit_override_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_override_ts ON public.routing_overrides_audit USING btree (override_id, ts DESC) WHERE (override_id IS NOT NULL);


--
-- Name: idx_routing_overrides_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_ts ON public.routing_overrides_audit USING btree (ts DESC);


--
-- Name: idx_routing_overrides_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_expires ON public.routing_overrides USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_routing_overrides_task_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_task_profile ON public.routing_overrides USING btree (task_type, profile);


--
-- Name: idx_routing_overrides_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_routing_overrides_unique ON public.routing_overrides USING btree (task_type, profile, COALESCE(model_chosen, ''::text), mode);


--
-- Name: idx_session_memora_extraction_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_memora_extraction_at ON public.session_memora_extraction_log USING btree (extracted_at DESC);


--
-- Name: idx_session_titles_generated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_titles_generated_at ON public.session_titles USING btree (generated_at DESC);


--
-- Name: idx_settings_audit_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_created ON public.settings_audit USING btree (created_at);


--
-- Name: idx_settings_audit_key_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_key_time ON public.settings_audit USING btree (setting_key, created_at DESC);


--
-- Name: idx_settings_audit_operator; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_operator ON public.settings_audit USING btree (operator_user, created_at DESC);


--
-- Name: idx_settings_audit_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_tenant_time ON public.settings_audit USING btree (tenant_id, created_at DESC);


--
-- Name: idx_settings_kv_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_category ON public.settings_kv USING btree (category);


--
-- Name: idx_settings_kv_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_scope ON public.settings_kv USING btree (scope);


--
-- Name: idx_settings_kv_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_updated ON public.settings_kv USING btree (updated_at DESC);


--
-- Name: idx_tenant_settings_kv_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_settings_kv_category ON public.tenant_settings_kv USING btree (category);


--
-- Name: idx_tenant_settings_kv_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_settings_kv_tenant ON public.tenant_settings_kv USING btree (tenant_id);


--
-- Name: idx_tenant_subscriptions_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_subscriptions_tenant ON public.tenant_subscriptions USING btree (tenant_id, status);


--
-- Name: idx_tenant_tool_policies_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_tool_policies_enabled ON public.tenant_tool_policies USING btree (enabled);


--
-- Name: idx_tenant_tool_policies_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_tool_policies_tenant ON public.tenant_tool_policies USING btree (tenant_id) WHERE (enabled = true);


--
-- Name: idx_tenants_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenants_name ON public.tenants USING btree (name);


--
-- Name: idx_tenants_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenants_status ON public.tenants USING btree (status);


--
-- Name: idx_tmp_audit_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_audit_tenant_ts ON public.tenant_model_policies_audit USING btree (tenant_id, ts DESC);


--
-- Name: idx_tmp_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_audit_ts ON public.tenant_model_policies_audit USING btree (ts DESC);


--
-- Name: idx_tmp_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_canonical ON public.tenant_model_policies USING btree (canonical_name);


--
-- Name: idx_tmp_tenant_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_tenant_active ON public.tenant_model_policies USING btree (tenant_id) WHERE (deleted_at IS NULL);


--
-- Name: idx_tool_categories_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_categories_order ON public.tool_categories USING btree (display_order) WHERE (enabled = true);


--
-- Name: idx_tool_registry_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_category ON public.tool_registry USING btree (category) WHERE (enabled = true);


--
-- Name: idx_tool_registry_deprecation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_deprecation ON public.tool_registry USING btree (deprecation_date) WHERE (deprecation_date IS NOT NULL);


--
-- Name: idx_tool_registry_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_name ON public.tool_registry USING btree (tool_name) WHERE (enabled = true);


--
-- Name: idx_tool_registry_tenant_tool; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_tenant_tool ON public.tool_registry USING btree (tenant_id, tool_id, version DESC);


--
-- Name: idx_tool_registry_unique_version; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tool_registry_unique_version ON public.tool_registry USING btree (tenant_id, tool_id, version);


--
-- Name: idx_tool_stats_part_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_created ON ONLY public.tool_usage_stats USING btree (created_at);


--
-- Name: idx_tool_stats_part_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_date ON ONLY public.tool_usage_stats USING btree (usage_date);


--
-- Name: idx_tool_stats_part_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_tenant ON ONLY public.tool_usage_stats USING btree (tenant_id, usage_date);


--
-- Name: idx_tool_stats_part_tool; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_stats_part_tool ON ONLY public.tool_usage_stats USING btree (tool_id, usage_date);


--
-- Name: idx_tool_usage_stats_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_date ON public.tool_usage_stats_old USING btree (usage_date DESC);


--
-- Name: idx_tool_usage_stats_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tenant_id ON public.tool_usage_stats_old USING btree (tenant_id);


--
-- Name: idx_tool_usage_stats_tool_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tool_id ON public.tool_usage_stats_old USING btree (tool_id);


--
-- Name: idx_tool_usage_stats_tool_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tool_tenant ON public.tool_usage_stats_old USING btree (tool_id, tenant_id, usage_date DESC);


--
-- Name: idx_tuning_proposals_cat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_cat ON public.tuning_proposals USING btree (category, task_type) WHERE (status = 'pending'::text);


--
-- Name: idx_tuning_proposals_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_created ON public.tuning_proposals USING btree (created_at) WHERE (status = 'pending'::text);


--
-- Name: idx_tuning_proposals_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_status ON public.tuning_proposals USING btree (status, ts DESC);


--
-- Name: idx_tuning_signals_5m_pk; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tuning_signals_5m_pk ON public.tuning_signals_5m USING btree (bucket, task_type, classifier);


--
-- Name: idx_tuning_signals_5m_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_5m_task_ts ON public.tuning_signals_5m USING btree (task_type, classifier, bucket DESC);


--
-- Name: idx_tuning_signals_daily_pk; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tuning_signals_daily_pk ON public.tuning_signals_daily USING btree (bucket, task_type, classifier);


--
-- Name: idx_tuning_signals_daily_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_daily_task_ts ON public.tuning_signals_daily USING btree (task_type, classifier, bucket DESC);


--
-- Name: idx_tuning_signals_lowq; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_lowq ON public.tuning_signals USING btree (task_type, ts DESC) WHERE ((quality_score < 0.5) AND (classifier = 'heuristic'::text));


--
-- Name: idx_tuning_signals_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_session ON public.tuning_signals USING btree (session_id, ts DESC) WHERE (session_id IS NOT NULL);


--
-- Name: idx_tuning_signals_strategy_task; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_strategy_task ON public.tuning_signals USING btree (strategy, task_type, ts DESC) WHERE (task_type IS NOT NULL);


--
-- Name: idx_tuning_signals_strategy_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_strategy_ts ON public.tuning_signals USING btree (strategy, ts DESC);


--
-- Name: idx_tuning_signals_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_task_ts ON public.tuning_signals USING btree (task_type, ts DESC);


--
-- Name: idx_usage_ledger_part_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_part_request_id ON ONLY public.usage_ledger USING btree (request_id);


--
-- Name: idx_usage_ledger_part_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_part_tenant ON ONLY public.usage_ledger USING btree (tenant_id, ts);


--
-- Name: idx_usage_ledger_part_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_part_ts ON ONLY public.usage_ledger USING btree (ts);


--
-- Name: idx_users_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_tenant ON public.users USING btree (tenant_id);


--
-- Name: idx_users_username; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_username ON public.users USING btree (username);


--
-- Name: idx_wal_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_session ON ONLY public.request_wal USING btree (gw_session_id, created_at);


--
-- Name: idx_wal_status_stage; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_status_stage ON ONLY public.request_wal USING btree (status, stage);


--
-- Name: idx_wal_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_tenant_created ON ONLY public.request_wal USING btree (tenant_id, created_at DESC);


--
-- Name: idx_work_type_config_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_work_type_config_category ON public.work_type_config USING btree (category, sort_order);


--
-- Name: idx_work_type_config_l1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_work_type_config_l1 ON public.work_type_config USING btree (l1_task_type);


--
-- Name: idx_wtmr_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wtmr_tier ON public.work_type_model_route USING btree (work_type_key, tier, weight DESC);


--
-- Name: idx_wtmr_work_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wtmr_work_type ON public.work_type_model_route USING btree (work_type_key);


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_session_id_ts_idx ON public.request_logs_2026_06 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_session_id_ts_idx1 ON public.request_logs_2026_06 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_06_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_task_id_ts_idx ON public.request_logs_2026_06 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_06_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_parent_request_id_ts_idx ON public.request_logs_2026_06 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_06_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_provider_id_quality_score_ts_idx ON public.request_logs_2026_06 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_06_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_provider_id_ts_idx ON public.request_logs_2026_06 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_06_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_quality_flags_idx ON public.request_logs_2026_06 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_06_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_06_request_id_idx ON public.request_logs_2026_06 USING btree (request_id);


--
-- Name: request_logs_2026_06_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_request_status_ts_idx ON public.request_logs_2026_06 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_06_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_06 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_06_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_ts_idx ON public.request_logs_2026_06 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_06_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_ts_idx1 ON public.request_logs_2026_06 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_06_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tool_calls_idx ON public.request_logs_2026_06 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_06_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_upstream_finish_reason_ts_idx ON public.request_logs_2026_06 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_06_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_work_type_ts_idx ON public.request_logs_2026_06 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_session_id_ts_idx ON public.request_logs_2026_07 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_session_id_ts_idx1 ON public.request_logs_2026_07 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_07_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_task_id_ts_idx ON public.request_logs_2026_07 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_07_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_parent_request_id_ts_idx ON public.request_logs_2026_07 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_07_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_provider_id_quality_score_ts_idx ON public.request_logs_2026_07 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_07_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_provider_id_ts_idx ON public.request_logs_2026_07 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_07_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_quality_flags_idx ON public.request_logs_2026_07 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_07_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_07_request_id_idx ON public.request_logs_2026_07 USING btree (request_id);


--
-- Name: request_logs_2026_07_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_request_status_ts_idx ON public.request_logs_2026_07 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_07_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_07 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_07_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_ts_idx ON public.request_logs_2026_07 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_07_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_ts_idx1 ON public.request_logs_2026_07 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_07_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tool_calls_idx ON public.request_logs_2026_07 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_07_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_upstream_finish_reason_ts_idx ON public.request_logs_2026_07 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_07_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_work_type_ts_idx ON public.request_logs_2026_07 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_session_id_ts_idx ON public.request_logs_2026_08 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_session_id_ts_idx1 ON public.request_logs_2026_08 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_08_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_task_id_ts_idx ON public.request_logs_2026_08 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_08_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_parent_request_id_ts_idx ON public.request_logs_2026_08 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_08_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_provider_id_quality_score_ts_idx ON public.request_logs_2026_08 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_08_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_provider_id_ts_idx ON public.request_logs_2026_08 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_08_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_quality_flags_idx ON public.request_logs_2026_08 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_08_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_08_request_id_idx ON public.request_logs_2026_08 USING btree (request_id);


--
-- Name: request_logs_2026_08_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_request_status_ts_idx ON public.request_logs_2026_08 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_08_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_08 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_08_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_ts_idx ON public.request_logs_2026_08 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_08_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_ts_idx1 ON public.request_logs_2026_08 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_08_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tool_calls_idx ON public.request_logs_2026_08 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_08_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_upstream_finish_reason_ts_idx ON public.request_logs_2026_08 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_08_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_work_type_ts_idx ON public.request_logs_2026_08 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_default_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_session_id_ts_idx ON public.request_logs_default USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_default_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_session_id_ts_idx1 ON public.request_logs_default USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_default_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_task_id_ts_idx ON public.request_logs_default USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_default_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_parent_request_id_ts_idx ON public.request_logs_default USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_default_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_provider_id_quality_score_ts_idx ON public.request_logs_default USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_default_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_provider_id_ts_idx ON public.request_logs_default USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_default_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_quality_flags_idx ON public.request_logs_default USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_default_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_default_request_id_idx ON public.request_logs_default USING btree (request_id);


--
-- Name: request_logs_default_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_request_status_ts_idx ON public.request_logs_default USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_default_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_gw_task_id_ts_idx ON public.request_logs_default USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_default_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_ts_idx ON public.request_logs_default USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_default_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_ts_idx1 ON public.request_logs_default USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_default_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tool_calls_idx ON public.request_logs_default USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_default_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_upstream_finish_reason_ts_idx ON public.request_logs_default USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_default_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_work_type_ts_idx ON public.request_logs_default USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_wal_2026_06_gw_session_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_gw_session_id_created_at_idx ON public.request_wal_2026_06 USING btree (gw_session_id, created_at);


--
-- Name: request_wal_2026_06_status_stage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_status_stage_idx ON public.request_wal_2026_06 USING btree (status, stage);


--
-- Name: request_wal_2026_06_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_tenant_id_created_at_idx ON public.request_wal_2026_06 USING btree (tenant_id, created_at DESC);


--
-- Name: request_wal_2026_07_gw_session_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_gw_session_id_created_at_idx ON public.request_wal_2026_07 USING btree (gw_session_id, created_at);


--
-- Name: request_wal_2026_07_status_stage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_status_stage_idx ON public.request_wal_2026_07 USING btree (status, stage);


--
-- Name: request_wal_2026_07_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_tenant_id_created_at_idx ON public.request_wal_2026_07 USING btree (tenant_id, created_at DESC);


--
-- Name: tool_usage_stats_2026_06_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_06_created_at_idx ON public.tool_usage_stats_2026_06 USING btree (created_at);


--
-- Name: tool_usage_stats_2026_06_tenant_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_06_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (tenant_id, usage_date);


--
-- Name: tool_usage_stats_2026_06_tool_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_06_tool_id_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (tool_id, usage_date);


--
-- Name: tool_usage_stats_2026_06_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_06_usage_date_idx ON public.tool_usage_stats_2026_06 USING btree (usage_date);


--
-- Name: tool_usage_stats_2026_07_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_07_created_at_idx ON public.tool_usage_stats_2026_07 USING btree (created_at);


--
-- Name: tool_usage_stats_2026_07_tenant_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_07_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (tenant_id, usage_date);


--
-- Name: tool_usage_stats_2026_07_tool_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_07_tool_id_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (tool_id, usage_date);


--
-- Name: tool_usage_stats_2026_07_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_07_usage_date_idx ON public.tool_usage_stats_2026_07 USING btree (usage_date);


--
-- Name: tool_usage_stats_2026_08_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_08_created_at_idx ON public.tool_usage_stats_2026_08 USING btree (created_at);


--
-- Name: tool_usage_stats_2026_08_tenant_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_08_tenant_id_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (tenant_id, usage_date);


--
-- Name: tool_usage_stats_2026_08_tool_id_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_08_tool_id_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (tool_id, usage_date);


--
-- Name: tool_usage_stats_2026_08_usage_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tool_usage_stats_2026_08_usage_date_idx ON public.tool_usage_stats_2026_08 USING btree (usage_date);


--
-- Name: usage_ledger_2026_06_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_06_request_id_idx ON public.usage_ledger_2026_06 USING btree (request_id);


--
-- Name: usage_ledger_2026_06_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_06_tenant_id_ts_idx ON public.usage_ledger_2026_06 USING btree (tenant_id, ts);


--
-- Name: usage_ledger_2026_06_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_06_ts_idx ON public.usage_ledger_2026_06 USING btree (ts);


--
-- Name: usage_ledger_2026_07_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_07_request_id_idx ON public.usage_ledger_2026_07 USING btree (request_id);


--
-- Name: usage_ledger_2026_07_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_07_tenant_id_ts_idx ON public.usage_ledger_2026_07 USING btree (tenant_id, ts);


--
-- Name: usage_ledger_2026_07_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_07_ts_idx ON public.usage_ledger_2026_07 USING btree (ts);


--
-- Name: usage_ledger_2026_08_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_08_request_id_idx ON public.usage_ledger_2026_08 USING btree (request_id);


--
-- Name: usage_ledger_2026_08_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_08_tenant_id_ts_idx ON public.usage_ledger_2026_08 USING btree (tenant_id, ts);


--
-- Name: usage_ledger_2026_08_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_ledger_2026_08_ts_idx ON public.usage_ledger_2026_08 USING btree (ts);


--
-- Name: credit_ledger_2026_06_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_created ATTACH PARTITION public.credit_ledger_2026_06_created_at_idx;


--
-- Name: credit_ledger_2026_06_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.credit_ledger_partitioned_pkey ATTACH PARTITION public.credit_ledger_2026_06_pkey;


--
-- Name: credit_ledger_2026_06_ref_type_ref_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_ref ATTACH PARTITION public.credit_ledger_2026_06_ref_type_ref_id_idx;


--
-- Name: credit_ledger_2026_06_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_tenant ATTACH PARTITION public.credit_ledger_2026_06_tenant_id_created_at_idx;


--
-- Name: credit_ledger_2026_07_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_created ATTACH PARTITION public.credit_ledger_2026_07_created_at_idx;


--
-- Name: credit_ledger_2026_07_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.credit_ledger_partitioned_pkey ATTACH PARTITION public.credit_ledger_2026_07_pkey;


--
-- Name: credit_ledger_2026_07_ref_type_ref_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_ref ATTACH PARTITION public.credit_ledger_2026_07_ref_type_ref_id_idx;


--
-- Name: credit_ledger_2026_07_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_tenant ATTACH PARTITION public.credit_ledger_2026_07_tenant_id_created_at_idx;


--
-- Name: credit_ledger_2026_08_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_created ATTACH PARTITION public.credit_ledger_2026_08_created_at_idx;


--
-- Name: credit_ledger_2026_08_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.credit_ledger_partitioned_pkey ATTACH PARTITION public.credit_ledger_2026_08_pkey;


--
-- Name: credit_ledger_2026_08_ref_type_ref_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_ref ATTACH PARTITION public.credit_ledger_2026_08_ref_type_ref_id_idx;


--
-- Name: credit_ledger_2026_08_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_credit_ledger_part_tenant ATTACH PARTITION public.credit_ledger_2026_08_tenant_id_created_at_idx;


--
-- Name: idx_request_logs_2026_06_client_model_trgm; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_client_model_trgm ATTACH PARTITION public.idx_request_logs_2026_06_client_model_trgm;


--
-- Name: idx_request_logs_2026_07_client_model_trgm; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_client_model_trgm ATTACH PARTITION public.idx_request_logs_2026_07_client_model_trgm;


--
-- Name: idx_request_logs_2026_08_client_model_trgm; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_client_model_trgm ATTACH PARTITION public.idx_request_logs_2026_08_client_model_trgm;


--
-- Name: idx_request_logs_default_client_model_trgm; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_client_model_trgm ATTACH PARTITION public.idx_request_logs_default_client_model_trgm;


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_06_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_06_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_06_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_06_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_06_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_06_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_06_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_06_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_06_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_06_provider_id_ts_idx;


--
-- Name: request_logs_2026_06_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_06_quality_flags_idx;


--
-- Name: request_logs_2026_06_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_unique ATTACH PARTITION public.request_logs_2026_06_request_id_idx;


--
-- Name: request_logs_2026_06_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_06_request_status_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_06_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_06_tenant_id_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_06_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_06_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_06_tool_calls_idx;


--
-- Name: request_logs_2026_06_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_06_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_06_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_06_work_type_ts_idx;


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_07_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_07_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_07_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_07_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_07_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_07_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_07_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_07_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_07_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_07_provider_id_ts_idx;


--
-- Name: request_logs_2026_07_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_07_quality_flags_idx;


--
-- Name: request_logs_2026_07_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_unique ATTACH PARTITION public.request_logs_2026_07_request_id_idx;


--
-- Name: request_logs_2026_07_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_07_request_status_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_07_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_07_tenant_id_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_07_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_07_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_07_tool_calls_idx;


--
-- Name: request_logs_2026_07_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_07_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_07_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_07_work_type_ts_idx;


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_08_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_08_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_08_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_08_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_08_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_08_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_08_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_08_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_08_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_08_provider_id_ts_idx;


--
-- Name: request_logs_2026_08_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_08_quality_flags_idx;


--
-- Name: request_logs_2026_08_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_unique ATTACH PARTITION public.request_logs_2026_08_request_id_idx;


--
-- Name: request_logs_2026_08_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_08_request_status_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_08_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_08_tenant_id_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_08_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_08_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_08_tool_calls_idx;


--
-- Name: request_logs_2026_08_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_08_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_08_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_08_work_type_ts_idx;


--
-- Name: request_logs_default_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_default_gw_session_id_ts_idx;


--
-- Name: request_logs_default_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_default_gw_session_id_ts_idx1;


--
-- Name: request_logs_default_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_default_gw_task_id_ts_idx;


--
-- Name: request_logs_default_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_default_parent_request_id_ts_idx;


--
-- Name: request_logs_default_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_default_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_default_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_default_provider_id_ts_idx;


--
-- Name: request_logs_default_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_default_quality_flags_idx;


--
-- Name: request_logs_default_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_unique ATTACH PARTITION public.request_logs_default_request_id_idx;


--
-- Name: request_logs_default_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_default_request_status_ts_idx;


--
-- Name: request_logs_default_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_default_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_default_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx;


--
-- Name: request_logs_default_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx1;


--
-- Name: request_logs_default_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_default_tool_calls_idx;


--
-- Name: request_logs_default_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_default_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_default_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_default_work_type_ts_idx;


--
-- Name: request_wal_2026_06_gw_session_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_session ATTACH PARTITION public.request_wal_2026_06_gw_session_id_created_at_idx;


--
-- Name: request_wal_2026_06_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_wal_pkey ATTACH PARTITION public.request_wal_2026_06_pkey;


--
-- Name: request_wal_2026_06_status_stage_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_status_stage ATTACH PARTITION public.request_wal_2026_06_status_stage_idx;


--
-- Name: request_wal_2026_06_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_tenant_created ATTACH PARTITION public.request_wal_2026_06_tenant_id_created_at_idx;


--
-- Name: request_wal_2026_07_gw_session_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_session ATTACH PARTITION public.request_wal_2026_07_gw_session_id_created_at_idx;


--
-- Name: request_wal_2026_07_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_wal_pkey ATTACH PARTITION public.request_wal_2026_07_pkey;


--
-- Name: request_wal_2026_07_status_stage_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_status_stage ATTACH PARTITION public.request_wal_2026_07_status_stage_idx;


--
-- Name: request_wal_2026_07_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_tenant_created ATTACH PARTITION public.request_wal_2026_07_tenant_id_created_at_idx;


--
-- Name: tool_usage_stats_2026_06_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_created ATTACH PARTITION public.tool_usage_stats_2026_06_created_at_idx;


--
-- Name: tool_usage_stats_2026_06_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_pkey ATTACH PARTITION public.tool_usage_stats_2026_06_pkey;


--
-- Name: tool_usage_stats_2026_06_tenant_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tenant ATTACH PARTITION public.tool_usage_stats_2026_06_tenant_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_06_tool_id_tenant_id_usage_date_creat_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key ATTACH PARTITION public.tool_usage_stats_2026_06_tool_id_tenant_id_usage_date_creat_key;


--
-- Name: tool_usage_stats_2026_06_tool_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tool ATTACH PARTITION public.tool_usage_stats_2026_06_tool_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_06_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_date ATTACH PARTITION public.tool_usage_stats_2026_06_usage_date_idx;


--
-- Name: tool_usage_stats_2026_07_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_created ATTACH PARTITION public.tool_usage_stats_2026_07_created_at_idx;


--
-- Name: tool_usage_stats_2026_07_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_pkey ATTACH PARTITION public.tool_usage_stats_2026_07_pkey;


--
-- Name: tool_usage_stats_2026_07_tenant_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tenant ATTACH PARTITION public.tool_usage_stats_2026_07_tenant_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key ATTACH PARTITION public.tool_usage_stats_2026_07_tool_id_tenant_id_usage_date_creat_key;


--
-- Name: tool_usage_stats_2026_07_tool_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tool ATTACH PARTITION public.tool_usage_stats_2026_07_tool_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_07_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_date ATTACH PARTITION public.tool_usage_stats_2026_07_usage_date_idx;


--
-- Name: tool_usage_stats_2026_08_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_created ATTACH PARTITION public.tool_usage_stats_2026_08_created_at_idx;


--
-- Name: tool_usage_stats_2026_08_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_pkey ATTACH PARTITION public.tool_usage_stats_2026_08_pkey;


--
-- Name: tool_usage_stats_2026_08_tenant_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tenant ATTACH PARTITION public.tool_usage_stats_2026_08_tenant_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key ATTACH PARTITION public.tool_usage_stats_2026_08_tool_id_tenant_id_usage_date_creat_key;


--
-- Name: tool_usage_stats_2026_08_tool_id_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_tool ATTACH PARTITION public.tool_usage_stats_2026_08_tool_id_usage_date_idx;


--
-- Name: tool_usage_stats_2026_08_usage_date_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_tool_stats_part_date ATTACH PARTITION public.tool_usage_stats_2026_08_usage_date_idx;


--
-- Name: usage_ledger_2026_06_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_request_id ATTACH PARTITION public.usage_ledger_2026_06_request_id_idx;


--
-- Name: usage_ledger_2026_06_request_id_ts_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.usage_ledger_partitioned_request_id_ts_key ATTACH PARTITION public.usage_ledger_2026_06_request_id_ts_key;


--
-- Name: usage_ledger_2026_06_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_tenant ATTACH PARTITION public.usage_ledger_2026_06_tenant_id_ts_idx;


--
-- Name: usage_ledger_2026_06_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_ts ATTACH PARTITION public.usage_ledger_2026_06_ts_idx;


--
-- Name: usage_ledger_2026_07_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_request_id ATTACH PARTITION public.usage_ledger_2026_07_request_id_idx;


--
-- Name: usage_ledger_2026_07_request_id_ts_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.usage_ledger_partitioned_request_id_ts_key ATTACH PARTITION public.usage_ledger_2026_07_request_id_ts_key;


--
-- Name: usage_ledger_2026_07_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_tenant ATTACH PARTITION public.usage_ledger_2026_07_tenant_id_ts_idx;


--
-- Name: usage_ledger_2026_07_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_ts ATTACH PARTITION public.usage_ledger_2026_07_ts_idx;


--
-- Name: usage_ledger_2026_08_request_id_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_request_id ATTACH PARTITION public.usage_ledger_2026_08_request_id_idx;


--
-- Name: usage_ledger_2026_08_request_id_ts_key; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.usage_ledger_partitioned_request_id_ts_key ATTACH PARTITION public.usage_ledger_2026_08_request_id_ts_key;


--
-- Name: usage_ledger_2026_08_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_tenant ATTACH PARTITION public.usage_ledger_2026_08_tenant_id_ts_idx;


--
-- Name: usage_ledger_2026_08_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_usage_ledger_part_ts ATTACH PARTITION public.usage_ledger_2026_08_ts_idx;


--
-- Name: credential_model_bindings cmb_protect_manual_disable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER cmb_protect_manual_disable BEFORE UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.trg_cmb_protect_manual_disable();


--
-- Name: model_offers model_offers_delete; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_delete_trigger();


--
-- Name: model_offers model_offers_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_insert INSTEAD OF INSERT ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_insert_trigger();


--
-- Name: model_offers model_offers_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_update_trigger();


--
-- Name: routing_overrides routing_overrides_audit_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER routing_overrides_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.routing_overrides FOR EACH ROW EXECUTE FUNCTION public.routing_overrides_audit_fn();


--
-- Name: tenant_model_policies tenant_model_policies_audit_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER tenant_model_policies_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.tenant_model_policies FOR EACH ROW EXECUTE FUNCTION public.tenant_model_policies_audit_fn();


--
-- Name: credentials trg_auto_fp_slot_limit_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_auto_fp_slot_limit_insert BEFORE INSERT ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.auto_set_fp_slot_limit();


--
-- Name: credentials trg_check_credential_dates; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_check_credential_dates BEFORE INSERT OR UPDATE ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.check_credential_dates();


--
-- Name: key_applications trg_key_applications_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_key_applications_updated_at BEFORE UPDATE ON public.key_applications FOR EACH ROW EXECUTE FUNCTION public.key_applications_set_updated_at();


--
-- Name: api_keys trg_notify_auto_route_apikeys; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_apikeys AFTER UPDATE OF rate_limit_rpm, budget_usd, enabled, status ON public.api_keys FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();


--
-- Name: credential_model_bindings trg_notify_auto_route_cmb; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_cmb AFTER INSERT OR DELETE OR UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.notify_auto_route_refresh();


--
-- Name: credentials trg_notify_auto_route_creds; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status ON public.credentials FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();


--
-- Name: request_logs trg_update_api_key_model_cost; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_update_api_key_model_cost AFTER INSERT ON public.request_logs FOR EACH ROW WHEN ((new.is_auto_request = true)) EXECUTE FUNCTION public.update_api_key_model_cost();


--
-- Name: provider_settings trigger_provider_settings_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trigger_provider_settings_updated_at BEFORE UPDATE ON public.provider_settings FOR EACH ROW EXECUTE FUNCTION public.update_provider_settings_updated_at();


--
-- Name: agent_relationships fk_agent_rel_dst; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_relationships
    ADD CONSTRAINT fk_agent_rel_dst FOREIGN KEY (dst_agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;


--
-- Name: agent_relationships fk_agent_rel_src; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_relationships
    ADD CONSTRAINT fk_agent_rel_src FOREIGN KEY (src_agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;


--
-- Name: asset_relationships fk_asset_rel_dst; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_relationships
    ADD CONSTRAINT fk_asset_rel_dst FOREIGN KEY (dst_kind, dst_ref_id) REFERENCES public.assets(kind, ref_id) ON DELETE CASCADE;


--
-- Name: asset_relationships fk_asset_rel_src; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_relationships
    ADD CONSTRAINT fk_asset_rel_src FOREIGN KEY (src_kind, src_ref_id) REFERENCES public.assets(kind, ref_id) ON DELETE CASCADE;


--
-- Name: agent_relationships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.agent_relationships ENABLE ROW LEVEL SECURITY;

--
-- Name: agents; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.agents ENABLE ROW LEVEL SECURITY;

--
-- Name: armor_judgments; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.armor_judgments ENABLE ROW LEVEL SECURITY;

--
-- Name: asset_relationships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.asset_relationships ENABLE ROW LEVEL SECURITY;

--
-- Name: assets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.assets ENABLE ROW LEVEL SECURITY;

--
-- Name: billing_orders; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.billing_orders ENABLE ROW LEVEL SECURITY;

--
-- Name: credit_ledger_old; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.credit_ledger_old ENABLE ROW LEVEL SECURITY;

--
-- Name: request_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.request_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: settings_audit; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.settings_audit ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_credit_wallets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_credit_wallets ENABLE ROW LEVEL SECURITY;

--
-- Name: agent_relationships tenant_isolation_agent_relationships; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_agent_relationships ON public.agent_relationships USING (((EXISTS ( SELECT 1
   FROM public.agents a_src
  WHERE ((a_src.id = agent_relationships.src_agent_id) AND (a_src.tenant_id = public.get_current_tenant())))) AND (EXISTS ( SELECT 1
   FROM public.agents a_dst
  WHERE ((a_dst.id = agent_relationships.dst_agent_id) AND (a_dst.tenant_id = public.get_current_tenant()))))));


--
-- Name: agents tenant_isolation_agents; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_agents ON public.agents USING ((tenant_id = public.get_current_tenant()));


--
-- Name: armor_judgments tenant_isolation_armor_judgments; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_armor_judgments ON public.armor_judgments USING ((tenant_id = public.get_current_tenant()));


--
-- Name: asset_relationships tenant_isolation_asset_relationships; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_asset_relationships ON public.asset_relationships USING (((EXISTS ( SELECT 1
   FROM public.assets a_src
  WHERE ((a_src.kind = asset_relationships.src_kind) AND (a_src.ref_id = asset_relationships.src_ref_id) AND (a_src.tenant_id = public.get_current_tenant())))) AND (EXISTS ( SELECT 1
   FROM public.assets a_dst
  WHERE ((a_dst.kind = asset_relationships.dst_kind) AND (a_dst.ref_id = asset_relationships.dst_ref_id) AND (a_dst.tenant_id = public.get_current_tenant()))))));


--
-- Name: assets tenant_isolation_assets; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_assets ON public.assets USING ((tenant_id = public.get_current_tenant()));


--
-- Name: billing_orders tenant_isolation_billing_orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_billing_orders ON public.billing_orders USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: credit_ledger_old tenant_isolation_credit_ledger; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_credit_ledger ON public.credit_ledger_old USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: request_logs tenant_isolation_request_logs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_request_logs ON public.request_logs USING ((tenant_id = public.get_current_tenant()));


--
-- Name: settings_audit tenant_isolation_settings_audit; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_settings_audit ON public.settings_audit USING ((((tenant_id)::text = public.get_current_tenant()) OR (tenant_id IS NULL)));


--
-- Name: tenant_credit_wallets tenant_isolation_tenant_credit_wallets; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_credit_wallets ON public.tenant_credit_wallets USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_settings_kv tenant_isolation_tenant_settings_kv; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_subscriptions tenant_isolation_tenant_subscriptions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_subscriptions ON public.tenant_subscriptions USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_tool_policies tenant_isolation_tenant_tool_policies; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies tenant_isolation_tmp; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies_audit tenant_isolation_tmp_audit; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit USING (((tenant_id = public.get_current_tenant()) OR (tenant_id IS NULL)));


--
-- Name: tool_call_events tenant_isolation_tool_call_events; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tool_registry tenant_isolation_tool_registry; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry USING ((((tenant_id)::text = public.get_current_tenant()) OR (tenant_id IS NULL) OR ((tenant_id)::text = 'default'::text)));


--
-- Name: tool_usage_stats tenant_isolation_tool_usage_stats; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tool_usage_stats_old tenant_isolation_tool_usage_stats; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats_old USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: users tenant_isolation_users; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_users ON public.users USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_model_policies ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_model_policies_audit; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_model_policies_audit ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_settings_kv; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_settings_kv ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_subscriptions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_subscriptions ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_tool_policies; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_tool_policies ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_call_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_call_events ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_registry; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_registry ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_usage_stats; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_usage_stats ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_usage_stats_old; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_usage_stats_old ENABLE ROW LEVEL SECURITY;

--
-- Name: users; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--

\unrestrict DecnQ5LcXZiA4Ym22zU9MfCPapcDGQ1BOMhox7mKLCvW69xTtslsAuWX5d1okz2

