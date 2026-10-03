-- ===========================================================================
-- File:          sql/migrations/startup/820_audio_modality_backfill.down.sql
-- Migration:     820 (down)
-- Purpose:       回填不可逆——820 只把 text 纠正为 audio，回退会把正确
--                标注重新弄错（audio 请求将再次 no_candidate）。down 是
--                有意的空操作，与 611 等纯纠正型迁移同款处置。
-- ===========================================================================
SELECT 1;
