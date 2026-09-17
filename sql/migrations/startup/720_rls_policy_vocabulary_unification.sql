-- 720 (R38/R39 RLS Phase 1, 2026-09-17): Unify RLS policy vocabulary to
-- canonical GUCs. Closes RLS design §五 Phase 1 item 1.
--
-- Context: R38 audit found policy GUC vocabulary fragmentation across 199
-- live policies:
--   - app.tenant_id: ~57 policies, ZERO setting points in code (V371 
--     supplier_errors comment acknowledges reliance on bypass_rls fallback)
--   - app.is_super_admin: 2 baseline residual policies, zero setting points
--   - app.current_tenant: good coverage (~90 policies)
--   - app.current_role / app.bypass_rls: good coverage (~57 each, semantically
--     overlapping for super-admin bypass)
--
-- Design decision (docs/design/rls-tenant-isolation-architecture.md §3.2):
-- Canonical tenant GUC = app.current_tenant accessed via get_current_tenant()
-- function (provides 'default' fallback for continuity with single-tenant
-- legacy data). Canonical super-admin bypass = app.current_role='super_admin'
-- OR app.bypass_rls='true' (both branches in standard form).
--
-- This migration rewrites 57+2=59 policies to use canonical vocabulary. The
-- rewrite is behavior-neutral during superuser era (current llm_gateway role
-- has SUPERUSER+BYPASSRLS, bypasses all RLS unconditionally). Post-Phase-2
-- role demotion, these policies will read the correct GUCs that code actually
-- sets.
--
-- Execution: DROP POLICY + CREATE POLICY (PostgreSQL has no ALTER POLICY for
-- USING clause changes). Each table's policies are grouped in a single
-- transaction-safe block. Idempotent: IF EXISTS guards allow re-run.

-- ══════════════════════════════════════════════════════════════════════════
-- Tenant isolation policies: app.tenant_id → get_current_tenant()
-- ══════════════════════════════════════════════════════════════════════════

-- auth_* tables (4 tables)
DROP POLICY IF EXISTS tenant_isolation ON auth_api_key_requests;
CREATE POLICY tenant_isolation ON auth_api_key_requests
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON auth_apikey_applications;
CREATE POLICY tenant_isolation ON auth_apikey_applications
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON auth_sessions;
CREATE POLICY tenant_isolation ON auth_sessions
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON auth_users;
CREATE POLICY tenant_isolation ON auth_users
  USING (tenant_id = get_current_tenant());

-- chat_* tables (2 tables)
DROP POLICY IF EXISTS tenant_isolation ON chat_messages;
CREATE POLICY tenant_isolation ON chat_messages
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON chat_sessions;
CREATE POLICY tenant_isolation ON chat_sessions
  USING (tenant_id = get_current_tenant());

-- context_manifest* tables (2 tables, multi-predicate)
DROP POLICY IF EXISTS tenant_isolation ON context_manifest_entries;
CREATE POLICY tenant_isolation ON context_manifest_entries
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON context_manifests;
CREATE POLICY tenant_isolation ON context_manifests
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

-- conversation_history
DROP POLICY IF EXISTS tenant_isolation ON conversation_history;
CREATE POLICY tenant_isolation ON conversation_history
  USING (tenant_id = get_current_tenant());

-- doc_tools_* (2 tables)
DROP POLICY IF EXISTS tenant_isolation ON doc_tools_tasks;
CREATE POLICY tenant_isolation ON doc_tools_tasks
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON doc_tools_uploads;
CREATE POLICY tenant_isolation ON doc_tools_uploads
  USING (tenant_id = get_current_tenant());

-- document_* tables (5 tables)
DROP POLICY IF EXISTS tenant_isolation ON document_chunks;
CREATE POLICY tenant_isolation ON document_chunks
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON document_feedback;
CREATE POLICY tenant_isolation ON document_feedback
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON document_links;
CREATE POLICY tenant_isolation ON document_links
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON document_retrieval_events;
CREATE POLICY tenant_isolation ON document_retrieval_events
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON documents;
CREATE POLICY tenant_isolation ON documents
  USING (tenant_id = get_current_tenant());

-- graph_* tables (3 tables)
DROP POLICY IF EXISTS tenant_isolation ON graph_entities;
CREATE POLICY tenant_isolation ON graph_entities
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON graph_episodes;
CREATE POLICY tenant_isolation ON graph_episodes
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON graph_facts;
CREATE POLICY tenant_isolation ON graph_facts
  USING (tenant_id = get_current_tenant());

-- knowledge* tables (8 tables)
DROP POLICY IF EXISTS tenant_isolation ON knowledge;
CREATE POLICY tenant_isolation ON knowledge
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_annotations;
CREATE POLICY tenant_isolation ON knowledge_annotations
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_base_acl;
CREATE POLICY tenant_isolation ON knowledge_base_acl
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_bases;
CREATE POLICY tenant_isolation ON knowledge_bases
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_entities;
CREATE POLICY tenant_isolation ON knowledge_entities
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_lineages;
CREATE POLICY tenant_isolation ON knowledge_lineages
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_metadata;
CREATE POLICY tenant_isolation ON knowledge_metadata
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_relations;
CREATE POLICY tenant_isolation ON knowledge_relations
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON knowledge_versions;
CREATE POLICY tenant_isolation ON knowledge_versions
  USING (tenant_id = get_current_tenant());

-- memora_session_summaries* (2 tables)
DROP POLICY IF EXISTS tenant_isolation ON memora_session_summaries;
CREATE POLICY tenant_isolation ON memora_session_summaries
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON memora_session_summaries_orphan;
CREATE POLICY tenant_isolation ON memora_session_summaries_orphan
  USING (tenant_id = get_current_tenant());

-- memory* tables (5 tables, some multi-predicate)
DROP POLICY IF EXISTS tenant_isolation ON memory;
CREATE POLICY tenant_isolation ON memory
  USING (tenant_id = get_current_tenant() 
     AND project_id = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON memory_candidate_events;
CREATE POLICY tenant_isolation ON memory_candidate_events
  USING (tenant_id = get_current_tenant() 
     AND project_id = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON memory_candidates;
CREATE POLICY tenant_isolation ON memory_candidates
  USING (tenant_id = get_current_tenant() 
     AND project_id = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON memory_conversation_links;
CREATE POLICY tenant_isolation ON memory_conversation_links
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON memory_ingest_receipts;
CREATE POLICY tenant_isolation ON memory_ingest_receipts
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

-- openclaw_* tables (5 tables)
DROP POLICY IF EXISTS tenant_isolation ON openclaw_events;
CREATE POLICY tenant_isolation ON openclaw_events
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON openclaw_feedback;
CREATE POLICY tenant_isolation ON openclaw_feedback
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON openclaw_handoffs;
CREATE POLICY tenant_isolation ON openclaw_handoffs
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON openclaw_prompt_ledger;
CREATE POLICY tenant_isolation ON openclaw_prompt_ledger
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON openclaw_shared_thread;
CREATE POLICY tenant_isolation ON openclaw_shared_thread
  USING (tenant_id = get_current_tenant());

-- project_* tables (6 tables, all multi-predicate)
DROP POLICY IF EXISTS tenant_isolation ON project_dreams;
CREATE POLICY tenant_isolation ON project_dreams
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON project_identity_audit;
CREATE POLICY tenant_isolation ON project_identity_audit
  USING (tenant_id = get_current_tenant() 
     AND project_id = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON project_insights;
CREATE POLICY tenant_isolation ON project_insights
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON project_jobs;
CREATE POLICY tenant_isolation ON project_jobs
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON project_observations;
CREATE POLICY tenant_isolation ON project_observations
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

DROP POLICY IF EXISTS tenant_isolation ON project_states;
CREATE POLICY tenant_isolation ON project_states
  USING (tenant_id = get_current_tenant() 
     AND project_id::text = current_setting('app.project_id', true));

-- projects
DROP POLICY IF EXISTS tenant_isolation ON projects;
CREATE POLICY tenant_isolation ON projects
  USING (tenant_id = get_current_tenant());

-- session_* tables (2 tables)
DROP POLICY IF EXISTS tenant_isolation ON session_compressions;
CREATE POLICY tenant_isolation ON session_compressions
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON session_memory_summaries;
CREATE POLICY tenant_isolation ON session_memory_summaries
  USING (tenant_id = get_current_tenant());

-- source_assets
DROP POLICY IF EXISTS tenant_isolation ON source_assets;
CREATE POLICY tenant_isolation ON source_assets
  USING (tenant_id::text = get_current_tenant());

-- supplier_errors* (2 tables with bypass OR clause) — add standard bypass form
DROP POLICY IF EXISTS tenant_isolation_supplier_errors ON supplier_errors;
CREATE POLICY tenant_isolation_supplier_errors ON supplier_errors
  USING (tenant_id = get_current_tenant()
      OR current_setting('app.current_role', true) = 'super_admin'
      OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS tenant_isolation_supplier_errors_hot ON supplier_errors_hot;
CREATE POLICY tenant_isolation_supplier_errors_hot ON supplier_errors_hot
  USING (tenant_id = get_current_tenant()
      OR current_setting('app.current_role', true) = 'super_admin'
      OR current_setting('app.bypass_rls', true) = 'true');

-- user_profiles
DROP POLICY IF EXISTS tenant_isolation ON user_profiles;
CREATE POLICY tenant_isolation ON user_profiles
  USING (tenant_id = get_current_tenant());

-- wiki_* tables (4 tables)
DROP POLICY IF EXISTS tenant_isolation ON wiki_page_versions;
CREATE POLICY tenant_isolation ON wiki_page_versions
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON wiki_pages;
CREATE POLICY tenant_isolation ON wiki_pages
  USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON wiki_proposals;
CREATE POLICY tenant_isolation ON wiki_proposals
  USING (tenant_id::text = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation ON wiki_sections;
CREATE POLICY tenant_isolation ON wiki_sections
  USING (tenant_id::text = get_current_tenant());

-- ══════════════════════════════════════════════════════════════════════════
-- Super-admin policies: app.is_super_admin → standard bypass form
-- ══════════════════════════════════════════════════════════════════════════

DROP POLICY IF EXISTS super_admin_analysis_events ON analysis_events;
CREATE POLICY super_admin_analysis_events ON analysis_events
  USING (current_setting('app.current_role', true) = 'super_admin'
      OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS super_admin_intent_aggregates ON intent_aggregates;
CREATE POLICY super_admin_intent_aggregates ON intent_aggregates
  USING (current_setting('app.current_role', true) = 'super_admin'
      OR current_setting('app.bypass_rls', true) = 'true');
