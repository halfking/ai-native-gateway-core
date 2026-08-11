-- 479: 凭据并发模式（concurrency_mode）与队列参数。
-- 关联设计：docs/会话优化v2/57-多层队列调度架构设计方案.md
-- 厂商依据：docs/会话优化v2/56-LLM厂商并发模式参考.md
--
-- 注意：本文件仅为 source-of-truth；运行时真正生效靠 db/db.go 的
-- ensureConcurrencyMode() 启动钩子（仓库既有约定，见 ensureFpSlotLimit）。
BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'credentials' AND column_name = 'concurrency_mode'
    ) THEN
        ALTER TABLE public.credentials ADD COLUMN concurrency_mode TEXT;
    END IF;
END $$;

-- 历史回填：有 rpm_limit 且无并发数 → rpm；否则 concurrency（沿用 concurrency_limit）。
UPDATE public.credentials
   SET concurrency_mode = 'rpm'
 WHERE concurrency_mode IS NULL
   AND rpm_limit IS NOT NULL
   AND concurrency_limit IS NULL;

UPDATE public.credentials
   SET concurrency_mode = 'concurrency'
 WHERE concurrency_mode IS NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'credentials' AND column_name = 'concurrency_mode' AND is_nullable = 'YES'
    ) THEN
        ALTER TABLE public.credentials ALTER COLUMN concurrency_mode SET DEFAULT 'concurrency';
        ALTER TABLE public.credentials ALTER COLUMN concurrency_mode SET NOT NULL;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'credentials_concurrency_mode_check'
          AND conrelid = 'public.credentials'::regclass
    ) THEN
        ALTER TABLE public.credentials
            ADD CONSTRAINT credentials_concurrency_mode_check
            CHECK (concurrency_mode IN ('concurrency','rpm','tpm','disabled'));
    END IF;
END $$;

ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS tpm_limit INTEGER;
ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS max_queue_depth INTEGER;
ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS max_queue_wait_ms INTEGER;

COMMENT ON COLUMN public.credentials.concurrency_mode IS '并发/限流模式: concurrency(并发数硬上限) | rpm(每分钟请求数) | tpm(每分钟token数) | disabled(不限流). 见 docs/会话优化v2/57';
COMMENT ON COLUMN public.credentials.tpm_limit IS 'tpm 模式下的令牌/分钟上限; NULL=不限';
COMMENT ON COLUMN public.credentials.max_queue_depth IS '凭据请求队列深度上限(可空=用全局 llmgw_dispatch_max_queue_depth)';
COMMENT ON COLUMN public.credentials.max_queue_wait_ms IS '凭据请求队列最长等待毫秒(可空=用全局 llmgw_dispatch_max_queue_wait_ms)';

COMMIT;
