BEGIN;
ALTER TABLE public.credentials DROP COLUMN IF EXISTS max_queue_wait_ms;
ALTER TABLE public.credentials DROP COLUMN IF EXISTS max_queue_depth;
ALTER TABLE public.credentials DROP COLUMN IF EXISTS tpm_limit;
ALTER TABLE public.credentials DROP CONSTRAINT IF EXISTS credentials_concurrency_mode_check;
ALTER TABLE public.credentials DROP COLUMN IF EXISTS concurrency_mode;
COMMIT;
