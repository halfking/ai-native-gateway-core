-- 451_models_canonical_modality_video.down.sql
-- 回滚 451：将 modality CHECK 约束还原为 5 值版本。
-- 注意：回滚前必须先将所有 modality='video' 的行更新为 'multimodal' 或 'vision'，
-- 否则会因违反约束而失败。

BEGIN;

UPDATE models_canonical SET modality = 'multimodal' WHERE modality = 'video';

ALTER TABLE models_canonical DROP CONSTRAINT IF EXISTS models_canonical_modality_check;

ALTER TABLE models_canonical
    ADD CONSTRAINT models_canonical_modality_check
    CHECK (modality = ANY (ARRAY[
        'text'::text,
        'vision'::text,
        'audio'::text,
        'multimodal'::text,
        'embedding'::text
    ]));

COMMIT;