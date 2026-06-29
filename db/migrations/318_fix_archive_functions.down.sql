-- Migration 318 rollback: re-create the previous (buggy) archive functions.
--
-- WARNING: This restores the original ON CONFLICT clause on
-- archive_credential_model_index and the single-batched INSERT on the
-- three other archive functions. It is intended for emergency rollback
-- only — running them on real data will reproduce the original bugs.

-- Re-define the 4 archive functions to their pre-318 implementations.
-- See git history of db/migrations/305_partition_archive_functions.sql
-- and db/migrations/053_archive_request_logs_column_aware.sql for the
-- previous bodies; restoring them is a manual operator action and is
-- not automated here to avoid an unsafe one-line re-application.

DO $$ BEGIN RAISE NOTICE '318.down.sql is a no-op. Manual restore required.'; END $$;
