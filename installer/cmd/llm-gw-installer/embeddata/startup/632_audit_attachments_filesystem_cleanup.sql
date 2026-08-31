-- Migration 632: audit_attachments_filesystem_cleanup — append-only ledger
-- for attachment files physically removed from LLM_GATEWAY_ATTACHMENT_DIR.
--
-- Rationale (audit-data-closure-2 / 2026-08-31): migration 629 introduced
-- audit_attachments_cleanup for the DB-side tombstone (request_logs_hot
-- .attachments = NULL). The filesystem-side cleanup endpoint
-- /api/admin/attachments/filesystem/cleanup (handler
-- handleAttachmentFilesystemCleanup in data_lifecycle_attachments_filesystem.go)
-- previously had no audit row of its own, so an operator could not correlate
-- a DB cleanup_run_id with the corresponding set of files removed from disk.
-- This migration closes that gap: the FS endpoint now writes one row per
-- removed file into audit_attachments_filesystem_cleanup, sharing the same
-- cleanup_run_id as the DB endpoint when the operator supplies it (or a
-- freshly generated UUID when the FS endpoint is invoked standalone).
--
-- The DB and FS operations remain physically independent — there is no
-- two-phase commit between request_logs_hot.attachments = NULL and
-- os.Remove(path) — because a Saga over a filesystem + a database
-- transaction is out of scope for this audit pass (see
-- docs/audit/2026-08-31-4h-correction-audit.md §3 deferred items). The
-- audit row + cleanup_run_id give operators a single query that joins the
-- two halves post hoc.
--
-- RLS / FORCE RLS: NONE. Same access pattern as 629: the cleanup is a
-- super-admin-only action, and any future read endpoint must enforce the
-- super_admin guard at the handler level (the table is intentionally
-- unprotected so a future reconciler job can scan all rows).
--
-- Idempotency: the unique key (request_id, attachment_hash, cleanup_run_id)
-- mirrors 629's so a re-run of the same cleanup_run cannot double-count
-- the same file. (request_id is optional for FS-only entries — see the
-- nullable column below; we still require a non-empty value to anchor the
-- unique constraint. Operators running the FS endpoint standalone should
-- set request_id='__fs_only__' for entries that have no DB counterpart.)

BEGIN;

CREATE TABLE IF NOT EXISTS public.audit_attachments_filesystem_cleanup (
    id              bigserial PRIMARY KEY,
    cleanup_run_id  uuid        NOT NULL,
    tenant_id       text        NOT NULL,
    request_id      text        NOT NULL,
    file_path       text        NOT NULL,
    file_hash       text,
    file_size       bigint,
    file_mtime      timestamptz,
    cleaned_at      timestamptz NOT NULL DEFAULT NOW(),
    triggered_by_user text      NOT NULL,
    reason          text,
    CONSTRAINT audit_attachments_filesystem_cleanup_unique
        UNIQUE (request_id, file_path, cleanup_run_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_attachments_filesystem_cleanup_run
    ON public.audit_attachments_filesystem_cleanup (cleanup_run_id);

CREATE INDEX IF NOT EXISTS idx_audit_attachments_filesystem_cleanup_request
    ON public.audit_attachments_filesystem_cleanup (request_id, cleaned_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_attachments_filesystem_cleanup_tenant_ts
    ON public.audit_attachments_filesystem_cleanup (tenant_id, cleaned_at DESC);

COMMIT;
