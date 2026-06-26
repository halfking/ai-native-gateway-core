-- ============================================================
-- Index sync for database: memos
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 5
-- ============================================================

\connect memos

CREATE INDEX idx_doc_tools_tasks_retry ON public.doc_tools_tasks USING btree (next_retry_at) WHERE ((status)::text = 'retrying'::text);

CREATE INDEX idx_doc_tools_tasks_created ON public.doc_tools_tasks USING btree (created_at DESC);

CREATE INDEX idx_doc_tools_tasks_status ON public.doc_tools_tasks USING btree (status);

CREATE INDEX idx_doc_tools_tasks_user ON public.doc_tools_tasks USING btree (user_id);

CREATE INDEX idx_doc_tools_tasks_token ON public.doc_tools_tasks USING btree (download_token);

