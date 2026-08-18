-- 079-credentials-ciphertext-format-check.sql
-- DB-level guard against the 2026-08-18 154 incident:
-- credential id=17 (apiclaude/130dao) was stored as 137 raw Fernet bytes
-- (first byte 0x80), bypassing the v1:legacy:<b64-url> envelope format that
-- the gateway's DecryptAny / DecryptFernet code paths expect.  This made
-- every Claude model request return "cannot decrypt: unknown format" until
-- the row was manually rewritten.
--
-- Until this migration, the credentials table had NO CHECK constraint on
-- secret_ciphertext, so the corruption passed DDL and only surfaced at
-- runtime through repeated WARN logs and routing failures.
--
-- Valid formats now enforced:
--   - v1:<kid>:<b64>            AES-GCM envelope (current default)
--   - v1:legacy:<b64>           legacy Fernet envelope (URL-safe base64 body)
--   - gAAAAA...                 bare Fernet base64 (defensive — DecryptFernet
--                                fallback in secret/aes_gcm.go:DecryptAny)
--
-- Disallowed:
--   - Raw Fernet bytes whose first byte is 0x80 (the 154-incident shape).
--     These cannot be decrypted because DecryptFernet expects base64-url.
--   - NULL is allowed (some credentials may legitimately have no key yet,
--     e.g. in pool_group=import workflows), but provider-credential admin
--     endpoints never allow INSERT with an empty ciphertext.
--
-- Operational notes:
--   1. The constraint is added with NOT VALID so the ALTER never blocks
--      against historical rows. After deployment, run a one-shot VALIDATE
--      CONSTRAINT in the next maintenance window to back-check existing
--      rows.  Use cmd/check-credentials (added in this branch) to scan first.
--   2. If VALIDATE CONSTRAINT finds violating rows, fix them with
--      `check-credentials -fix` (re-wraps raw Fernet into v1:legacy:<b64>)
--      or `UPDATE ... SET secret_ciphertext = NULL` for empty rows, then
--      retry the VALIDATE.
--   3. The down migration drops the constraint cleanly.

BEGIN;

ALTER TABLE public.credentials
    DROP CONSTRAINT IF EXISTS credentials_ciphertext_format_check;

-- 2026-08-18 (incident 154): enforce that secret_ciphertext, when present,
-- is either a v1:<kid>:<b64> envelope, a v1:legacy:<b64> envelope, or a
-- bare Fernet base64 token. Raw Fernet binary (first byte 0x80) and
-- anything that doesn't begin with one of the three valid prefixes is
-- rejected at INSERT / UPDATE time.
ALTER TABLE public.credentials
    ADD CONSTRAINT credentials_ciphertext_format_check
    CHECK (
        secret_ciphertext IS NULL
        OR (octet_length(secret_ciphertext) > 0
            AND get_byte(secret_ciphertext, 0) <> 128  -- 0x80, raw Fernet token
            AND (
                encode(secret_ciphertext, 'escape') LIKE 'v1:%'
                OR encode(secret_ciphertext, 'escape') LIKE 'gAAAAA%'
            )
        )
    ) NOT VALID;

COMMIT;

-- Post-deploy manual verification:
--   SELECT conname, contype, convalidated
--   FROM pg_constraint WHERE conname = 'credentials_ciphertext_format_check';
-- Expected: convalidated = false (because of NOT VALID).
--
-- After running cmd/check-credentials -fix on 154, run:
--   ALTER TABLE public.credentials VALIDATE CONSTRAINT credentials_ciphertext_format_check;
-- to back-check existing rows. This will fail if any historical row still
-- violates the constraint — investigate via:
--   SELECT id, label, encode(secret_ciphertext, 'escape')
--   FROM public.credentials
--   WHERE NOT (
--       secret_ciphertext IS NULL
--       OR (octet_length(secret_ciphertext) > 0
--           AND get_byte(secret_ciphertext, 0) <> 128
--           AND (
--               encode(secret_ciphertext, 'escape') LIKE 'v1:%'
--               OR encode(secret_ciphertext, 'escape') LIKE 'gAAAAA%'
--           )
--       )
--   );