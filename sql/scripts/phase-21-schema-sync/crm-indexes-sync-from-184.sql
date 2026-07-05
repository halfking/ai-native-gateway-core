-- ============================================================
-- Index sync for database: crm
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 47
-- ============================================================

\connect crm

CREATE INDEX idx_crm_conv_last_msg ON public.crm_conversations USING btree (last_message_at DESC);

CREATE INDEX idx_crm_act_type ON public.crm_activities USING btree (activity_type);

CREATE INDEX idx_crm_customers_company ON public.crm_customers USING btree (company);

CREATE INDEX idx_crm_conv_status ON public.crm_conversations USING btree (status);

CREATE INDEX idx_crm_customers_owner ON public.crm_customers USING btree (owner_id);

CREATE INDEX idx_crm_kb_status ON public.crm_kb_documents USING btree (status);

CREATE INDEX idx_crm_out_status_channel ON public.crm_outbound_queue USING btree (status, channel_id, created_at);

CREATE INDEX idx_crm_ai_status ON public.crm_ai_replies USING btree (status);

CREATE INDEX idx_crm_out_pending_next ON public.crm_outbound_queue USING btree (status, channel_id, created_at) WHERE ((status)::text = 'pending'::text);

CREATE INDEX idx_crm_usage_time ON public.crm_llm_usage USING btree (occurred_at DESC);

CREATE INDEX idx_crm_opp_stage ON public.crm_opportunities USING btree (stage);

CREATE INDEX idx_crm_lead_scores_customer ON public.crm_lead_scores USING btree (customer_id, created_at DESC);

CREATE INDEX idx_crm_act_customer ON public.crm_activities USING btree (customer_id);

CREATE INDEX idx_crm_customers_lead_score ON public.crm_customers USING btree (lead_score DESC NULLS LAST);

CREATE INDEX idx_crm_msg_conv ON public.crm_messages USING btree (conversation_id, sent_at DESC);

CREATE INDEX idx_crm_msg_direction ON public.crm_messages USING btree (direction);

CREATE UNIQUE INDEX uq_crm_selectors_page_element ON public.crm_rpa_selectors USING btree (page_key, element_key);

CREATE INDEX idx_crm_shop_auth_expires ON public.crm_shop_authorizations USING btree (expires_at) WHERE ((status)::text = 'enabled'::text);

CREATE INDEX idx_crm_channels_external_account ON public.crm_channels USING btree (platform, channel_kind, external_account_id) WHERE (external_account_id IS NOT NULL);

CREATE INDEX idx_crm_openapi_events_shop ON public.crm_openapi_events USING btree (shop_id, event_type, received_at DESC);

CREATE INDEX idx_crm_kb_gaps_status ON public.crm_kb_gaps USING btree (status);

CREATE INDEX idx_crm_usage_conv ON public.crm_llm_usage USING btree (conversation_id);

CREATE INDEX idx_crm_usage_module ON public.crm_llm_usage USING btree (module);

CREATE UNIQUE INDEX uq_crm_channels_platform_account ON public.crm_channels USING btree (platform, account_name);

CREATE INDEX idx_crm_alerts_status ON public.crm_alerts USING btree (status, created_at DESC);

CREATE INDEX idx_crm_opp_customer ON public.crm_opportunities USING btree (customer_id);

CREATE UNIQUE INDEX uq_crm_msg_external ON public.crm_messages USING btree (conversation_id, external_id) WHERE (external_id IS NOT NULL);

CREATE UNIQUE INDEX uq_crm_kb_chunk_doc_idx ON public.crm_kb_chunks USING btree (document_id, chunk_index);

CREATE UNIQUE INDEX uq_crm_openapi_quota_window ON public.crm_openapi_quota USING btree (shop_id, api_method, window_start);

CREATE INDEX idx_crm_kb_category ON public.crm_kb_documents USING btree (category);

CREATE UNIQUE INDEX uq_crm_openapi_events_msgid ON public.crm_openapi_events USING btree (msg_id) WHERE (msg_id IS NOT NULL);

CREATE INDEX idx_crm_conv_priority ON public.crm_conversations USING btree (priority);

CREATE UNIQUE INDEX uq_crm_shop_auth_shop ON public.crm_shop_authorizations USING btree (shop_id);

CREATE INDEX idx_crm_out_dead_letter ON public.crm_outbound_queue USING btree (status, created_at) WHERE ((status)::text = 'dead_letter'::text);

CREATE INDEX idx_crm_customers_tier ON public.crm_customers USING btree (tier);

CREATE INDEX idx_crm_customers_name ON public.crm_customers USING gin (to_tsvector('simple'::regconfig, (name)::text));

CREATE INDEX idx_crm_conv_customer ON public.crm_conversations USING btree (customer_id);

CREATE INDEX idx_crm_ai_conv ON public.crm_ai_replies USING btree (conversation_id, created_at DESC);

CREATE INDEX idx_crm_customers_douyin_uid ON public.crm_customers USING btree (douyin_uid);

CREATE INDEX idx_crm_customers_phone ON public.crm_customers USING btree (phone);

CREATE UNIQUE INDEX uq_crm_conv_channel_visitor ON public.crm_conversations USING btree (channel_id, visitor_uid);

CREATE INDEX idx_crm_cache_expires ON public.crm_semantic_cache USING btree (expires_at);

CREATE INDEX idx_crm_alerts_severity ON public.crm_alerts USING btree (severity);

CREATE INDEX idx_crm_oauth_states_expires ON public.crm_oauth_states USING btree (expires_at) WHERE (expires_at IS NOT NULL);

CREATE INDEX idx_crm_openapi_events_received ON public.crm_openapi_events USING btree (received_at DESC);

CREATE INDEX idx_crm_lead_scores_grade ON public.crm_lead_scores USING btree (lead_grade);

CREATE INDEX idx_crm_cache_intent ON public.crm_semantic_cache USING btree (intent);

