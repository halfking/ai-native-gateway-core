-- Migration 516 verification: durable task schema + SR-17 transition extension.
-- Usage: psql <dsn> -v ON_ERROR_STOP=1 -f sql/migrations/test/test_516.test.sql

\set ON_ERROR_STOP on

DO $$
DECLARE
    task_id UUID := '51600000-0000-0000-0000-000000000001';
BEGIN
    IF to_regclass('public.durable_llm_tasks') IS NULL THEN
        RAISE EXCEPTION '516: durable_llm_tasks missing';
    END IF;

    IF (SELECT column_default IS NOT NULL
        FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'durable_llm_tasks'
          AND column_name = 'id') THEN
        RAISE EXCEPTION '516: id must not depend on a sequence/default';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_class
        WHERE oid = 'public.durable_llm_tasks'::regclass AND relrowsecurity
    ) THEN
        RAISE EXCEPTION '516: durable_llm_tasks RLS not enabled';
    END IF;

    IF (SELECT count(*) FROM pg_policies
        WHERE schemaname = 'public' AND tablename = 'durable_llm_tasks') <> 2 THEN
        RAISE EXCEPTION '516: expected two durable_llm_tasks RLS policies';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public' AND tablename = 'durable_llm_tasks'
          AND indexname = 'idx_durable_llm_tasks_runnable'
    ) THEN
        RAISE EXCEPTION '516: runnable partial index missing';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'request_state_transitions'
          AND column_name = 'attempt_no'
    ) THEN
        RAISE EXCEPTION '516: request_state_transitions.attempt_no missing';
    END IF;

    INSERT INTO durable_llm_tasks (
        id, tenant_id, request_id, session_id, protocol, endpoint,
        request_snapshot_ciphertext, snapshot_version, encryption_key_id,
        request_hash, status, next_retry_at, deadline_at, policy
    ) VALUES (
        task_id, 'test-516-tenant', 'test-516-request', 'test-516-session',
        'openai_chat', '/v1/chat/completions', 'encrypted-request', 1,
        'key-1', 'request-hash', 'waiting_recovery', NOW(),
        NOW() + INTERVAL '1 hour', '{}'::JSONB
    );

    BEGIN
        UPDATE durable_llm_tasks
        SET status = 'streaming'
        WHERE id = task_id;
        RAISE EXCEPTION '516: streaming task without lease/fencing was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE durable_llm_tasks
        SET commit_state = 'content', semantic_content_committed = TRUE
        WHERE id = task_id;
        RAISE EXCEPTION '516: runnable content checkpoint was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE durable_llm_tasks
        SET status = 'completed', completed_at = NOW(), result_version = 1
        WHERE id = task_id;
        RAISE EXCEPTION '516: completed task without result was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        UPDATE durable_llm_tasks
        SET status = 'completed', completed_at = NOW(), result_version = 1,
            result_ciphertext = '', result_hash = 'result-hash',
            content_type = 'application/json'
        WHERE id = task_id;
        RAISE EXCEPTION '516: completed task with empty result ciphertext was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    UPDATE durable_llm_tasks
    SET status = 'completed', completed_at = NOW(), result_version = 1,
        result_ciphertext = 'encrypted-result', result_hash = 'result-hash',
        content_type = 'application/json', commit_state = 'terminal'
    WHERE id = task_id;

    INSERT INTO request_state_transitions (
        request_id, tenant_id, transition_type, from_state, to_state,
        attempt_no, metadata, seq
    ) VALUES (
        'test-516-request', 'test-516-tenant', 'survival_waiting',
        'running', 'waiting_recovery', 1, '{"candidate_outcomes":[]}'::JSONB,
        516001
    );

    INSERT INTO request_state_transitions (
        request_id, tenant_id, transition_type, attempt_no, seq
    ) VALUES (
        'test-516-request', 'test-516-tenant', 'survival_waiting', 1, 516001
    ) ON CONFLICT (request_id, seq) DO NOTHING;

    IF (SELECT count(*) FROM request_state_transitions
        WHERE request_id = 'test-516-request' AND seq = 516001) <> 1 THEN
        RAISE EXCEPTION '516: migration 515 replay uniqueness was not preserved';
    END IF;

    BEGIN
        INSERT INTO request_state_transitions (
            request_id, tenant_id, transition_type, attempt_no
        ) VALUES ('test-516-invalid', 'test-516-tenant', 'survival_unknown', 1);
        RAISE EXCEPTION '516: unknown survival transition was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    BEGIN
        INSERT INTO request_state_transitions (
            request_id, tenant_id, transition_type, attempt_no
        ) VALUES ('test-516-invalid-attempt', 'test-516-tenant', 'survival_retry', 0);
        RAISE EXCEPTION '516: zero attempt_no was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;

    DELETE FROM request_state_transitions WHERE request_id LIKE 'test-516-%';
    DELETE FROM durable_llm_tasks WHERE id = task_id;
END $$;

SELECT '516: all checks passed' AS result;
