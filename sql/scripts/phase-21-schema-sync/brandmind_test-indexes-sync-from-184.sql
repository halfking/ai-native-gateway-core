-- ============================================================
-- Index sync for database: brandmind_test
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 91
-- ============================================================

\connect brandmind_test

CREATE INDEX idx_crm_lead_status ON public.crm_leads USING btree (status);

CREATE INDEX idx_alert_severity ON public.alerts USING btree (severity);

CREATE INDEX idx_published_brand ON public.published_contents USING btree (brand_id);

CREATE INDEX idx_strategy_goal_brand ON public.strategy_goals USING btree (brand_id);

CREATE INDEX idx_daily_brief_status ON public.daily_briefs USING btree (push_status);

CREATE INDEX idx_strategy_goal_status ON public.strategy_goals USING btree (status);

CREATE INDEX idx_geo_reports_brand ON public.geo_reports USING btree (brand_id);

CREATE INDEX idx_oplog_brand ON public.operation_logs USING btree (brand_id);

CREATE INDEX idx_publish_task_brand ON public.publish_tasks USING btree (brand_id);

CREATE INDEX idx_knowledge_items_tenant ON public.knowledge_items USING btree (tenant_id);

CREATE INDEX idx_knowledge_source ON public.knowledge_items USING btree (source);

CREATE INDEX idx_knowledge_category ON public.knowledge_items USING btree (category);

CREATE INDEX idx_brands_industry ON public.brands USING btree (industry);

CREATE INDEX idx_alert_created ON public.alerts USING btree (created_at DESC);

CREATE INDEX idx_ooda_brand_type ON public.ooda_cycles USING btree (brand_id, cycle_type, started_at DESC);

CREATE INDEX idx_industry_benchmarks_tenant ON public.industry_benchmarks USING btree (tenant_id);

CREATE INDEX idx_publish_tasks_tenant ON public.publish_tasks USING btree (tenant_id);

CREATE INDEX idx_platform_accounts_tenant ON public.platform_accounts USING btree (tenant_id);

CREATE INDEX idx_report_brand ON public.reports USING btree (brand_id);

CREATE INDEX idx_followup_created ON public.lead_followups USING btree (created_at DESC);

CREATE INDEX idx_ooda_status ON public.ooda_cycles USING btree (status);

CREATE INDEX idx_strategy_plan_goal ON public.strategy_plans USING btree (goal_id);

CREATE INDEX idx_platform_account_platform ON public.platform_accounts USING btree (platform);

CREATE INDEX idx_ingest_jobs_status ON public.ingest_jobs USING btree (status);

CREATE UNIQUE INDEX idx_probe_brand_snapshot ON public.probe_results USING btree (brand_id, snapshot_date);

CREATE INDEX idx_knowledge_brand ON public.knowledge_items USING btree (brand_id);

CREATE INDEX idx_ingest_jobs_created ON public.ingest_jobs USING btree (created_at DESC);

CREATE INDEX idx_daily_brief_brand ON public.daily_briefs USING btree (brand_id);

CREATE INDEX idx_strategy_plan_type ON public.strategy_plans USING btree (plan_type);

CREATE INDEX idx_oplog_user ON public.operation_logs USING btree (user_id);

CREATE INDEX idx_daily_brief_date ON public.daily_briefs USING btree (brief_date DESC);

CREATE INDEX idx_weekly_plan_week ON public.weekly_plans USING btree (week_start DESC);

CREATE INDEX idx_crm_lead_brand ON public.crm_leads USING btree (brand_id);

CREATE INDEX idx_alerts_tenant ON public.alerts USING btree (tenant_id);

CREATE INDEX idx_benchmark_metric ON public.industry_benchmarks USING btree (metric);

CREATE INDEX idx_notif_channel ON public.notification_log USING btree (channel);

CREATE INDEX idx_alert_brand ON public.alerts USING btree (brand_id);

CREATE INDEX idx_content_jobs_tenant ON public.content_jobs USING btree (tenant_id);

CREATE INDEX idx_users_tenant ON public.users USING btree (tenant_id);

CREATE INDEX idx_ooda_acc_task ON public.ooda_cycles USING btree (acc_task_id);

CREATE INDEX idx_detail_platform ON public.probe_details USING btree (platform);

CREATE INDEX idx_published_contents_tenant ON public.published_contents USING btree (tenant_id);

CREATE INDEX idx_publish_task_date ON public.publish_tasks USING btree (created_at DESC);

CREATE INDEX idx_notif_dedup ON public.notification_log USING btree (brand_id, content_hash, channel);

CREATE INDEX idx_reports_tenant ON public.reports USING btree (tenant_id);

CREATE INDEX idx_probe_results_tenant ON public.probe_results USING btree (tenant_id);

CREATE INDEX idx_oplog_action ON public.operation_logs USING btree (action);

CREATE INDEX idx_channel_weight_week ON public.channel_weights USING btree (week_of DESC);

CREATE INDEX idx_subscriptions_tenant ON public.subscriptions USING btree (tenant_id);

CREATE INDEX idx_content_job_brand ON public.content_jobs USING btree (brand_id);

CREATE INDEX idx_publish_task_status ON public.publish_tasks USING btree (status);

CREATE INDEX idx_followup_lead ON public.lead_followups USING btree (lead_id);

CREATE INDEX idx_published_platform ON public.published_contents USING btree (platform);

CREATE INDEX idx_channel_weight_roi ON public.channel_weights USING btree (actual_roi DESC);

CREATE INDEX idx_detail_probe_id ON public.probe_details USING btree (probe_id);

CREATE INDEX idx_weekly_plan_brand ON public.weekly_plans USING btree (brand_id);

CREATE INDEX idx_content_job_status ON public.content_jobs USING btree (status);

CREATE INDEX idx_oplog_date ON public.operation_logs USING btree (created_at DESC);

CREATE INDEX idx_benchmark_industry ON public.industry_benchmarks USING btree (industry);

CREATE INDEX idx_brands_tenant ON public.brands USING btree (tenant_id);

CREATE INDEX idx_user_casdoor ON public.users USING btree (casdoor_id);

CREATE INDEX idx_weekly_plan_status ON public.weekly_plans USING btree (status);

CREATE INDEX idx_crm_lead_created ON public.crm_leads USING btree (created_at DESC);

CREATE INDEX idx_brands_name ON public.brands USING btree (name);

CREATE INDEX idx_notif_brand ON public.notification_log USING btree (brand_id);

CREATE INDEX idx_crm_lead_score ON public.crm_leads USING btree (lead_score DESC);

CREATE INDEX idx_followup_action ON public.lead_followups USING btree (action_type);

CREATE INDEX idx_alert_status ON public.alerts USING btree (status);

CREATE INDEX idx_crm_lead_acc_task ON public.crm_leads USING btree (acc_task_id);

CREATE INDEX idx_strategy_plan_brand ON public.strategy_plans USING btree (brand_id);

CREATE INDEX idx_ingest_jobs_tenant ON public.ingest_jobs USING btree (tenant_id);

CREATE INDEX idx_operation_logs_tenant ON public.operation_logs USING btree (tenant_id);

CREATE INDEX idx_followup_next ON public.lead_followups USING btree (next_action_at) WHERE (next_action_at IS NOT NULL);

CREATE UNIQUE INDEX idx_benchmark_unique ON public.industry_benchmarks USING btree (industry, metric, period);

CREATE INDEX idx_probe_snapshot ON public.probe_results USING btree (snapshot_date);

CREATE INDEX idx_probe_brand_id ON public.probe_results USING btree (brand_id);

CREATE INDEX idx_published_date ON public.published_contents USING btree (published_at DESC);

CREATE INDEX idx_notif_type ON public.notification_log USING btree (message_type);

CREATE INDEX idx_channel_weight_brand ON public.channel_weights USING btree (brand_id);

CREATE INDEX idx_ooda_brand ON public.ooda_cycles USING btree (brand_id);

CREATE INDEX idx_notif_created ON public.notification_log USING btree (created_at DESC);

CREATE INDEX idx_notif_status ON public.notification_log USING btree (status);

CREATE INDEX idx_content_job_date ON public.content_jobs USING btree (created_at DESC);

CREATE INDEX idx_geo_reports_tenant ON public.geo_reports USING btree (tenant_id);

CREATE INDEX idx_report_type ON public.reports USING btree (type);

CREATE INDEX idx_ingest_jobs_brand ON public.ingest_jobs USING btree (brand_id);

CREATE INDEX idx_platform_account_brand ON public.platform_accounts USING btree (brand_id);

CREATE INDEX idx_strategy_goal_deadline ON public.strategy_goals USING btree (deadline);

CREATE INDEX idx_strategy_plan_status ON public.strategy_plans USING btree (status);

CREATE INDEX idx_probe_date ON public.probe_results USING btree (probe_date DESC);

CREATE INDEX idx_ooda_state_paused ON public.ooda_schedule_state USING btree (is_paused);

