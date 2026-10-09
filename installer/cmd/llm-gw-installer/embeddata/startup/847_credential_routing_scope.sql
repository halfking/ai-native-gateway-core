-- Migration 845: credentials.routing_scope — 多媒体特殊服务凭据路由域隔离。
--
-- 背景（2026-10-08）：语音/视频/图片类厂商 API（MiniMax STT、智谱 ASR/TTS 等）
-- 的 key 与标准 chat 套餐 key 混用时，候选 SQL 仅靠 modality 无法挡住
-- 「media 套餐 key 误绑 text 模型」被标准 /v1/chat/completions 选中 → 烧钱与流量。
-- tags 不参与候选过滤，必须有显式列 + 候选 WHERE 硬隔离。
--
-- 取值：
--   all         — 默认，兼容存量（chat + media 均可）
--   chat_only   — 仅标准文本/工具/embedding 域
--   media_only  — 仅 audio/vision/video 等多媒体域
--
-- Compatibility: ADD COLUMN IF NOT EXISTS + DEFAULT 'all' → 存量行为零变化。
-- credentials 热表，元数据级变更（与 701/704 同级）。

BEGIN;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS routing_scope text NOT NULL DEFAULT 'all';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'credentials_routing_scope_check'
          AND conrelid = 'public.credentials'::regclass
    ) THEN
        ALTER TABLE public.credentials
            ADD CONSTRAINT credentials_routing_scope_check
            CHECK (routing_scope = ANY (ARRAY['all'::text, 'chat_only'::text, 'media_only'::text]));
    END IF;
END $$;

COMMENT ON COLUMN public.credentials.routing_scope IS
    'Routing domain: all | chat_only | media_only. media_only keys are excluded from text/chat/embedding candidate selection.';

CREATE INDEX IF NOT EXISTS idx_credentials_routing_scope
    ON public.credentials (routing_scope)
    WHERE status <> 'deleted';

INSERT INTO public.schema_migrations (version, description)
VALUES ('847', 'credentials.routing_scope for media/chat credential isolation')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
