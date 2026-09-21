-- Migration 646 rollback: remove only the canonical migration ledger entry.
--
-- Migration 646 reconciles schema that may already have been created by the
-- historical db/migrations/364_proxy_management.sql path. PostgreSQL does not
-- retain object provenance, so dropping tables, columns, constraints, indexes,
-- comments, or existing data here could destroy a valid pre-645 installation.
-- The safe rollback is therefore intentionally non-destructive. If an operator
-- knows the proxy schema was created only by 646 and wants full removal, use the
-- legacy db/migrations/364_proxy_management.down.sql manually after confirming
-- that no providers or runtime paths depend on it.

BEGIN;

DELETE FROM public.schema_migrations WHERE version = '646';

COMMIT;
