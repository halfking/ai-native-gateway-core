-- ============================================================
-- Index sync for database: smart_bidding
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 12
-- ============================================================

\connect smart_bidding

CREATE INDEX idx_sbil_project ON public.smart_bidding_image_library USING btree (project_id);

CREATE INDEX idx_sbc_parent ON public.smart_bidding_chapters USING btree (parent_id);

CREATE INDEX idx_sbcv_chapter ON public.smart_bidding_chapter_versions USING btree (chapter_id);

CREATE INDEX idx_sbal_action ON public.smart_bidding_audit_logs USING btree (action);

CREATE INDEX idx_sbal_user ON public.smart_bidding_audit_logs USING btree (user_id);

CREATE INDEX idx_sbn_user ON public.smart_bidding_notifications USING btree (user_id);

CREATE INDEX idx_sbp_owner ON public.smart_bidding_projects USING btree (owner_id);

CREATE INDEX idx_sbc_project ON public.smart_bidding_chapters USING btree (project_id);

CREATE INDEX idx_sbp_status ON public.smart_bidding_projects USING btree (status);

CREATE INDEX idx_sbal_created ON public.smart_bidding_audit_logs USING btree (created_at);

CREATE INDEX idx_sbt_category ON public.smart_bidding_templates USING btree (category);

CREATE INDEX idx_sbn_read ON public.smart_bidding_notifications USING btree (is_read);

