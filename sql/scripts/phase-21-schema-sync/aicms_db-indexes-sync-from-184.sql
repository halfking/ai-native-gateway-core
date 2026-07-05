-- ============================================================
-- Index sync for database: aicms_db
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 6
-- ============================================================

\connect aicms_db

CREATE INDEX ix_messages_session_id ON public.messages USING btree (session_id);

CREATE INDEX ix_intents_bot_id ON public.intents USING btree (bot_id);

CREATE INDEX ix_messages_created_at ON public.messages USING btree (created_at);

CREATE INDEX ix_contacts_external_id ON public.contacts USING btree (external_id);

CREATE INDEX ix_sessions_contact_id ON public.sessions USING btree (contact_id);

CREATE INDEX ix_sessions_bot_id ON public.sessions USING btree (bot_id);

