-- Migration: 451_models_canonical_modality_video
-- Purpose: 扩展 models_canonical.modality 的 CHECK 约束，加入 'video' 值。
--   Phase 1 (Layer 1): discovery 推断出 video 模型（Gemini 1.5/2.0 等支持视频的）
--   Phase 2 (Layer 2): bg probe 通过 multimodal payload 验证 video 能力
--   Phase 3 (Layer 3): admin PATCH /api/models/:id/modality 允许覆盖为 'video'
--
-- Background:
--   domains/streaming/modality_detect.go 在 2026-07-15 引入了 video 检测逻辑
--   （line 19, 26, 48），但当时的 SQL CHECK 约束只有 5 个值：
--     text / vision / audio / multimodal / embedding
--   这导致 video 请求进来后 SQL 过滤查不到任何候选，路由直接失败。
--
-- Date: 2026-07-20
-- Idempotent: YES (DROP + ADD CONSTRAINT pattern is safe)

BEGIN;

-- Drop the old 5-value CHECK constraint and replace with 6-value.
-- DO block makes this idempotent: safe to re-run.
DO $$
BEGIN
    -- Drop old constraint if present (any of the 5-value variants we may have shipped).
    ALTER TABLE models_canonical DROP CONSTRAINT IF EXISTS models_canonical_modality_check;

    -- Re-add with 6 values (text / vision / audio / video / multimodal / embedding).
    -- 'video' is added to support providers like Google Gemini 1.5/2.0 that
    -- accept video_url blocks in chat completions.
    ALTER TABLE models_canonical
        ADD CONSTRAINT models_canonical_modality_check
        CHECK (modality = ANY (ARRAY[
            'text'::text,
            'vision'::text,
            'audio'::text,
            'video'::text,
            'multimodal'::text,
            'embedding'::text
        ]));
END $$;

COMMENT ON CONSTRAINT models_canonical_modality_check ON models_canonical IS
    'Allowed modality values: text/vision/audio/video/multimodal/embedding. Updated 2026-07-20 (migration 451) to add video support — fixes the contract mismatch with domains/streaming/modality_detect.go which can return modality=video.';

COMMIT;