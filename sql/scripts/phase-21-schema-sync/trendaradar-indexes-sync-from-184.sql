-- ============================================================
-- Index sync for database: trendaradar
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 89
-- ============================================================

\connect trendaradar

CREATE INDEX idx_source_configs_enabled ON public.source_configs USING btree (enabled);

CREATE INDEX idx_mail_items_profile_received ON public.mail_items USING btree (mailbox_profile_id, received_at DESC);

CREATE INDEX idx_brand_jobs_status ON public.brand_collection_jobs USING btree (status);

CREATE INDEX idx_token_platform ON public.token_accounts USING btree (platform, status);

CREATE INDEX idx_mail_resources_mail_item ON public.mail_resources USING btree (mail_item_id);

CREATE INDEX idx_channel_client_devices_region ON public.channel_client_devices USING btree (region) WHERE (status = 'online'::text);

CREATE INDEX idx_intelligence_shares_grantee ON public.intelligence_shares USING btree (grantee_user_id);

CREATE INDEX idx_cpr_status ON public.channel_post_replies USING btree (status, created_at DESC);

CREATE INDEX idx_invoice_records_status ON public.invoice_records USING btree (parse_status, review_status, issue_date DESC);

CREATE INDEX idx_mail_runs_profile ON public.mail_ingestion_runs USING btree (mailbox_profile_id);

CREATE INDEX idx_media_accounts_phone ON public.media_accounts USING btree (phone);

CREATE INDEX idx_invoice_records_no_code ON public.invoice_records USING btree (invoice_code, invoice_no);

CREATE INDEX idx_mail_resources_status ON public.mail_resources USING btree (download_status, parse_status);

CREATE INDEX idx_intelligence_assets_public ON public.intelligence_assets USING btree (is_public);

CREATE INDEX idx_intelligence_assets_owner ON public.intelligence_assets USING btree (owner_id);

CREATE UNIQUE INDEX idx_audit_events_audit_id ON public.audit_events USING btree (audit_id);

CREATE INDEX idx_parsed_documents_resource ON public.parsed_documents USING btree (resource_id);

CREATE INDEX idx_invoice_records_dedupe ON public.invoice_records USING btree (dedupe_key);

CREATE INDEX idx_mail_items_message ON public.mail_items USING btree (mailbox_profile_id, message_id);

CREATE INDEX idx_collection_rules_deleted_at ON public.collection_rules USING btree (deleted_at);

CREATE INDEX idx_rule_runs_rule_id ON public.rule_runs USING btree (rule_id);

CREATE INDEX idx_media_accounts_douban_uid ON public.media_accounts USING btree (douban_uid) WHERE (douban_uid <> ''::text);

CREATE INDEX idx_mailbox_profiles_enabled ON public.mailbox_profiles USING btree (enabled) WHERE (deleted_at IS NULL);

CREATE INDEX idx_cpc_post_time ON public.channel_post_comments USING btree (platform, source_post_id, posted_at DESC);

CREATE INDEX idx_crawl_records_source ON public.crawl_records USING btree (source_name);

CREATE INDEX idx_mail_items_category ON public.mail_items USING btree (category, priority, received_at DESC);

CREATE INDEX idx_feedback_capture ON public.channel_reply_feedback USING btree (platform, captured_at DESC);

CREATE UNIQUE INDEX idx_sync_tasks_task_id ON public.sync_tasks USING btree (task_id);

CREATE INDEX idx_kb_tenant ON public.kb_qa USING btree (tenant_id) WHERE (enabled = true);

CREATE INDEX idx_audit_events_created_at ON public.audit_events USING btree (created_at);

CREATE INDEX idx_brand_jobs_type_status ON public.brand_collection_jobs USING btree (job_type, status);

CREATE INDEX idx_agent_actions_approval ON public.agent_actions USING btree (approval_status);

CREATE INDEX idx_brand_jobs_created_at ON public.brand_collection_jobs USING btree (created_at DESC);

CREATE INDEX idx_token_expires ON public.token_accounts USING btree (expires_at) WHERE (expires_at IS NOT NULL);

CREATE INDEX idx_channel_task_runs_channel ON public.channel_task_runs USING btree (channel, created_at DESC);

CREATE INDEX idx_channel_task_runs_template ON public.channel_task_runs USING btree (template_id, created_at DESC);

CREATE INDEX idx_parsed_documents_type ON public.parsed_documents USING btree (document_type, status);

CREATE INDEX idx_audit_events_rule_id ON public.audit_events USING btree (rule_id);

CREATE INDEX idx_crawl_records_keywords ON public.crawl_records USING gin (keywords_matched);

CREATE UNIQUE INDEX idx_agent_actions_action_id ON public.agent_actions USING btree (action_id);

CREATE INDEX idx_media_accounts_platform ON public.media_accounts USING btree (platform, status);

CREATE INDEX idx_channel_client_devices_status ON public.channel_client_devices USING btree (status, last_heartbeat DESC);

CREATE INDEX idx_skill_registry_skill_id ON public.skill_registry USING btree (skill_id);

CREATE INDEX idx_brand_jobs_brand_name ON public.brand_collection_jobs USING btree (brand_name);

CREATE INDEX idx_user_push_configs_channel ON public.user_push_configs USING btree (channel);

CREATE INDEX idx_intelligence_audit_asset ON public.intelligence_audit_log USING btree (asset_id);

CREATE INDEX idx_rule_runs_status ON public.rule_runs USING btree (status);

CREATE INDEX idx_mail_resources_sha256 ON public.mail_resources USING btree (sha256);

CREATE INDEX idx_mailbox_profiles_owner ON public.mailbox_profiles USING btree (owner_user_id);

CREATE INDEX idx_mailbox_profiles_tenant ON public.mailbox_profiles USING btree (tenant_id);

CREATE UNIQUE INDEX idx_rule_runs_run_id ON public.rule_runs USING btree (run_id);

CREATE INDEX idx_invoice_exports_profile ON public.invoice_exports USING btree (mailbox_profile_id, created_at DESC);

CREATE INDEX idx_mail_runs_created ON public.mail_ingestion_runs USING btree (created_at DESC);

CREATE INDEX idx_task_records_created_at ON public.task_records USING btree (created_at);

CREATE INDEX idx_kb_intent ON public.kb_qa USING btree (platform, intent) WHERE (enabled = true);

CREATE INDEX idx_intelligence_delegations_delegate ON public.intelligence_delegations USING btree (delegate_user_id);

CREATE INDEX idx_keyword_tracking_keyword ON public.keyword_tracking USING btree (keyword);

CREATE INDEX idx_media_accounts_xhs_uid ON public.media_accounts USING btree (xhs_uid) WHERE (xhs_uid <> ''::text);

CREATE INDEX idx_intelligence_delegations_expires ON public.intelligence_delegations USING btree (expires_at);

CREATE INDEX idx_task_records_status ON public.task_records USING btree (status);

CREATE INDEX idx_task_records_task_id ON public.task_records USING btree (task_id);

CREATE INDEX idx_source_configs_source_id ON public.source_configs USING btree (source_id);

CREATE INDEX idx_channel_task_runs_status ON public.channel_task_runs USING btree (status, created_at DESC);

CREATE INDEX idx_user_push_configs_enabled ON public.user_push_configs USING btree (enabled);

CREATE INDEX idx_crawl_records_crawled_at ON public.crawl_records USING btree (crawled_at);

CREATE INDEX idx_mail_runs_status ON public.mail_ingestion_runs USING btree (status);

CREATE INDEX idx_brand_jobs_brand_id ON public.brand_collection_jobs USING btree (brand_id);

CREATE INDEX idx_kb_question_trgm ON public.kb_qa USING gin (question public.gin_trgm_ops);

CREATE INDEX idx_push_records_status ON public.push_records USING btree (status);

CREATE INDEX idx_sync_tasks_status_created_at ON public.sync_tasks USING btree (status, created_at DESC);

CREATE INDEX idx_user_push_configs_user_id ON public.user_push_configs USING btree (user_id);

CREATE INDEX idx_media_accounts_weibo_uid ON public.media_accounts USING btree (weibo_uid) WHERE (weibo_uid <> ''::text);

CREATE INDEX idx_intelligence_shares_asset ON public.intelligence_shares USING btree (asset_id);

CREATE INDEX idx_token_meter_occurred_at ON public.token_meter_events USING btree (occurred_at DESC);

CREATE INDEX idx_collection_rules_lifecycle ON public.collection_rules USING btree (lifecycle_status);

CREATE INDEX idx_token_meter_rule_id ON public.token_meter_events USING btree (rule_id);

CREATE INDEX idx_cpr_post ON public.channel_post_replies USING btree (platform, source_post_id, created_at DESC);

CREATE INDEX idx_intelligence_delegations_asset ON public.intelligence_delegations USING btree (asset_id);

CREATE INDEX idx_intelligence_assets_created ON public.intelligence_assets USING btree (created_at);

CREATE INDEX idx_crawl_records_url ON public.crawl_records USING btree (url);

CREATE INDEX idx_push_records_created_at ON public.push_records USING btree (created_at);

CREATE INDEX idx_cpc_unprocessed ON public.channel_post_comments USING btree (processed_at) WHERE (processed_at IS NULL);

CREATE INDEX idx_cpc_target ON public.channel_post_comments USING btree (platform, is_from_target) WHERE (is_from_target = true);

CREATE INDEX idx_rule_runs_created_at ON public.rule_runs USING btree (created_at);

CREATE UNIQUE INDEX idx_mail_items_profile_folder_uid ON public.mail_items USING btree (mailbox_profile_id, folder, uid) WHERE (uid <> ''::text);

CREATE INDEX idx_channel_task_runs_plan ON public.channel_task_runs USING btree (plan_task_id, created_at DESC);

CREATE INDEX idx_feedback_qa ON public.channel_reply_feedback USING btree (qa_id, captured_at DESC);

CREATE INDEX idx_push_records_user_id ON public.push_records USING btree (user_id);

CREATE INDEX idx_token_meter_user_sub ON public.token_meter_events USING btree (user_sub);

