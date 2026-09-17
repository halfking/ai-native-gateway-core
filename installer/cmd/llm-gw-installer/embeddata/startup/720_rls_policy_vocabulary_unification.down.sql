-- 720 rollback: restore original policy vocabulary
--
-- This restores app.tenant_id and app.is_super_admin references. Rollback is
-- safe during superuser era (policies are dormant either way). Post-demotion
-- this rollback would break tenant isolation (code doesn't set app.tenant_id),
-- so it's an emergency-only path if 720 causes unexpected issues during the
-- superuser Phase 1 period.

-- Restore app.tenant_id references (57 policies)
--
-- 2026-09-18 fix: same per-table to_regclass guards as the up migration —
-- tables skipped by 720 (absent on the target DB) must also be skipped here,
-- otherwise rollback fails with 42P01 on a table that never had the policy.
DO $$
BEGIN
    IF to_regclass('auth_api_key_requests') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON auth_api_key_requests;
    CREATE POLICY tenant_isolation ON auth_api_key_requests
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table auth_api_key_requests not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('auth_apikey_applications') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON auth_apikey_applications;
    CREATE POLICY tenant_isolation ON auth_apikey_applications
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table auth_apikey_applications not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('auth_sessions') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON auth_sessions;
    CREATE POLICY tenant_isolation ON auth_sessions
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table auth_sessions not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('auth_users') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON auth_users;
    CREATE POLICY tenant_isolation ON auth_users
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table auth_users not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('chat_messages') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON chat_messages;
    CREATE POLICY tenant_isolation ON chat_messages
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table chat_messages not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('chat_sessions') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON chat_sessions;
    CREATE POLICY tenant_isolation ON chat_sessions
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table chat_sessions not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('context_manifest_entries') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON context_manifest_entries;
    CREATE POLICY tenant_isolation ON context_manifest_entries
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table context_manifest_entries not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('context_manifests') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON context_manifests;
    CREATE POLICY tenant_isolation ON context_manifests
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table context_manifests not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('conversation_history') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON conversation_history;
    CREATE POLICY tenant_isolation ON conversation_history
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table conversation_history not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('doc_tools_tasks') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON doc_tools_tasks;
    CREATE POLICY tenant_isolation ON doc_tools_tasks
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table doc_tools_tasks not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('doc_tools_uploads') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON doc_tools_uploads;
    CREATE POLICY tenant_isolation ON doc_tools_uploads
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table doc_tools_uploads not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('document_chunks') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON document_chunks;
    CREATE POLICY tenant_isolation ON document_chunks
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table document_chunks not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('document_feedback') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON document_feedback;
    CREATE POLICY tenant_isolation ON document_feedback
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table document_feedback not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('document_links') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON document_links;
    CREATE POLICY tenant_isolation ON document_links
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table document_links not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('document_retrieval_events') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON document_retrieval_events;
    CREATE POLICY tenant_isolation ON document_retrieval_events
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table document_retrieval_events not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('documents') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON documents;
    CREATE POLICY tenant_isolation ON documents
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table documents not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('graph_entities') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON graph_entities;
    CREATE POLICY tenant_isolation ON graph_entities
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table graph_entities not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('graph_episodes') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON graph_episodes;
    CREATE POLICY tenant_isolation ON graph_episodes
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table graph_episodes not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('graph_facts') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON graph_facts;
    CREATE POLICY tenant_isolation ON graph_facts
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table graph_facts not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge;
    CREATE POLICY tenant_isolation ON knowledge
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_annotations') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_annotations;
    CREATE POLICY tenant_isolation ON knowledge_annotations
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_annotations not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_base_acl') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_base_acl;
    CREATE POLICY tenant_isolation ON knowledge_base_acl
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_base_acl not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_bases') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_bases;
    CREATE POLICY tenant_isolation ON knowledge_bases
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_bases not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_entities') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_entities;
    CREATE POLICY tenant_isolation ON knowledge_entities
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_entities not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_lineages') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_lineages;
    CREATE POLICY tenant_isolation ON knowledge_lineages
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_lineages not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_metadata') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_metadata;
    CREATE POLICY tenant_isolation ON knowledge_metadata
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_metadata not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_relations') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_relations;
    CREATE POLICY tenant_isolation ON knowledge_relations
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_relations not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('knowledge_versions') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON knowledge_versions;
    CREATE POLICY tenant_isolation ON knowledge_versions
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table knowledge_versions not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memora_session_summaries') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memora_session_summaries;
    CREATE POLICY tenant_isolation ON memora_session_summaries
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table memora_session_summaries not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memora_session_summaries_orphan') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memora_session_summaries_orphan;
    CREATE POLICY tenant_isolation ON memora_session_summaries_orphan
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table memora_session_summaries_orphan not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memory') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memory;
    CREATE POLICY tenant_isolation ON memory
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table memory not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memory_candidate_events') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memory_candidate_events;
    CREATE POLICY tenant_isolation ON memory_candidate_events
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table memory_candidate_events not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memory_candidates') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memory_candidates;
    CREATE POLICY tenant_isolation ON memory_candidates
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table memory_candidates not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memory_conversation_links') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memory_conversation_links;
    CREATE POLICY tenant_isolation ON memory_conversation_links
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table memory_conversation_links not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('memory_ingest_receipts') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON memory_ingest_receipts;
    CREATE POLICY tenant_isolation ON memory_ingest_receipts
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table memory_ingest_receipts not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('openclaw_events') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON openclaw_events;
    CREATE POLICY tenant_isolation ON openclaw_events
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table openclaw_events not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('openclaw_feedback') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON openclaw_feedback;
    CREATE POLICY tenant_isolation ON openclaw_feedback
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table openclaw_feedback not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('openclaw_handoffs') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON openclaw_handoffs;
    CREATE POLICY tenant_isolation ON openclaw_handoffs
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table openclaw_handoffs not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('openclaw_prompt_ledger') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON openclaw_prompt_ledger;
    CREATE POLICY tenant_isolation ON openclaw_prompt_ledger
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table openclaw_prompt_ledger not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('openclaw_shared_thread') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON openclaw_shared_thread;
    CREATE POLICY tenant_isolation ON openclaw_shared_thread
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table openclaw_shared_thread not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_dreams') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_dreams;
    CREATE POLICY tenant_isolation ON project_dreams
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_dreams not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_identity_audit') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_identity_audit;
    CREATE POLICY tenant_isolation ON project_identity_audit
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_identity_audit not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_insights') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_insights;
    CREATE POLICY tenant_isolation ON project_insights
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_insights not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_jobs') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_jobs;
    CREATE POLICY tenant_isolation ON project_jobs
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_jobs not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_observations') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_observations;
    CREATE POLICY tenant_isolation ON project_observations
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_observations not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('project_states') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON project_states;
    CREATE POLICY tenant_isolation ON project_states
      USING (tenant_id = current_setting('app.tenant_id', true)
         AND project_id::text = current_setting('app.project_id', true));
    ELSE
        RAISE NOTICE '720: table project_states not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('projects') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON projects;
    CREATE POLICY tenant_isolation ON projects
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table projects not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('session_compressions') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON session_compressions;
    CREATE POLICY tenant_isolation ON session_compressions
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table session_compressions not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('session_memory_summaries') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON session_memory_summaries;
    CREATE POLICY tenant_isolation ON session_memory_summaries
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table session_memory_summaries not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('source_assets') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON source_assets;
    CREATE POLICY tenant_isolation ON source_assets
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table source_assets not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('supplier_errors') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation_supplier_errors ON supplier_errors;
    CREATE POLICY tenant_isolation_supplier_errors ON supplier_errors
      USING (tenant_id = current_setting('app.tenant_id', true)
          OR current_setting('app.bypass_rls', true) = 'true');
    ELSE
        RAISE NOTICE '720: table supplier_errors not present, skipping tenant_isolation_supplier_errors rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('supplier_errors_hot') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation_supplier_errors_hot ON supplier_errors_hot;
    CREATE POLICY tenant_isolation_supplier_errors_hot ON supplier_errors_hot
      USING (tenant_id = current_setting('app.tenant_id', true)
          OR current_setting('app.bypass_rls', true) = 'true');
    ELSE
        RAISE NOTICE '720: table supplier_errors_hot not present, skipping tenant_isolation_supplier_errors_hot rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('user_profiles') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON user_profiles;
    CREATE POLICY tenant_isolation ON user_profiles
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table user_profiles not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('wiki_page_versions') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON wiki_page_versions;
    CREATE POLICY tenant_isolation ON wiki_page_versions
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table wiki_page_versions not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('wiki_pages') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON wiki_pages;
    CREATE POLICY tenant_isolation ON wiki_pages
      USING (tenant_id = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table wiki_pages not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('wiki_proposals') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON wiki_proposals;
    CREATE POLICY tenant_isolation ON wiki_proposals
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table wiki_proposals not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('wiki_sections') IS NOT NULL THEN
    DROP POLICY IF EXISTS tenant_isolation ON wiki_sections;
    CREATE POLICY tenant_isolation ON wiki_sections
      USING (tenant_id::text = current_setting('app.tenant_id', true));
    ELSE
        RAISE NOTICE '720: table wiki_sections not present, skipping tenant_isolation rewrite';
    END IF;
END $$;

-- Restore app.is_super_admin references (2 policies)
DO $$
BEGIN
    IF to_regclass('analysis_events') IS NOT NULL THEN
    DROP POLICY IF EXISTS super_admin_analysis_events ON analysis_events;
    CREATE POLICY super_admin_analysis_events ON analysis_events
      USING (current_setting('app.is_super_admin', true) = 'true');
    ELSE
        RAISE NOTICE '720: table analysis_events not present, skipping super_admin_analysis_events rewrite';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('intent_aggregates') IS NOT NULL THEN
    DROP POLICY IF EXISTS super_admin_intent_aggregates ON intent_aggregates;
    CREATE POLICY super_admin_intent_aggregates ON intent_aggregates
      USING (current_setting('app.is_super_admin', true) = 'true');
    ELSE
        RAISE NOTICE '720: table intent_aggregates not present, skipping super_admin_intent_aggregates rewrite';
    END IF;
END $$;

