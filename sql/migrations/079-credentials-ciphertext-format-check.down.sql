-- 079-credentials-ciphertext-format-check.down.sql
-- Reverses migration 079. The up migration adds the constraint with
-- NOT VALID, so dropping it here is safe regardless of row state.

BEGIN;

ALTER TABLE public.credentials
    DROP CONSTRAINT IF EXISTS credentials_ciphertext_format_check;

COMMIT;