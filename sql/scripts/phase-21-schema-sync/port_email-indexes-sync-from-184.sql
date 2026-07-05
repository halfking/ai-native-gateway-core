-- ============================================================
-- Index sync for database: port_email
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Missing indexes: 11
-- ============================================================

\connect port_email

CREATE INDEX idx_port_skill_versions_skill_id ON public.port_skill_versions USING btree (skill_id);

CREATE INDEX idx_port_task_nodes_task_flow_id ON public.port_task_nodes USING btree (task_flow_id);

CREATE INDEX idx_port_audit_logs_tenant_id ON public.port_audit_logs USING btree (tenant_id);

CREATE INDEX idx_port_agent_owners_agent_id ON public.port_agent_owners USING btree (agent_id);

CREATE INDEX idx_port_task_flows_email_id ON public.port_task_flows USING btree (email_id);

CREATE INDEX idx_port_audit_logs_email_id ON public.port_audit_logs USING btree (email_id);

CREATE INDEX idx_port_audit_logs_action ON public.port_audit_logs USING btree (action);

CREATE INDEX idx_port_task_flows_status ON public.port_task_flows USING btree (status);

CREATE INDEX idx_port_task_flows_tenant_id ON public.port_task_flows USING btree (tenant_id);

CREATE INDEX idx_port_task_nodes_status ON public.port_task_nodes USING btree (status);

CREATE INDEX idx_port_audit_logs_created_at ON public.port_audit_logs USING btree (created_at);

