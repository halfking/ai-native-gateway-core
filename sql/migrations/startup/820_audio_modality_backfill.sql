-- ===========================================================================
-- File:          sql/migrations/startup/820_audio_modality_backfill.sql
-- Migration:     820
-- Database:      llm_gateway
-- Purpose:       存量音频模型（ASR/TTS 家族）modality 回填 audio。
--
-- 背景（2026-10-03 音频端点轮，小米上游实测）：
--   modelname.InferModality 的旧规则表只有前缀 asr-/tts- 与包含
--   -asr-/-tts- 的匹配，于是「后缀」形态的模型名（mimo-v2.5-asr、
--   mimo-v2.5-tts）在 discovery 播种时落回默认 text。
--
--   modality=text 的行会被 audio 请求的候选过滤
--   （COALESCE(mc.modality,'text') IN ('audio','multimodal')）排除，
--   表现为 503 no_candidate——上游本身完全健康（chat+input_audio
--   转写/合成都实测可用），纯粹是标注错误。
--
-- 回填口径与新的 InferModality 后缀/包含规则保持一致（820 与规则
-- 修改同轮合入）；只升级 text → audio，不碰已有 audio/multimodal/
-- vision 行，幂等可重放。
-- ===========================================================================

-- 后缀形态：*-asr / *-tts（mimo-v2.5-asr、mimo-v2.5-tts、qwen3-asr 等）
UPDATE models_canonical
   SET modality = 'audio',
       updated_at = now()
 WHERE modality = 'text'
   AND status = 'active'
   AND (canonical_name ~ '-asr$' OR canonical_name ~ '-tts$');

-- 包含形态：*-asr-* / *-tts-* / *-stt-*（qwen3-asr-0.6b、*-tts-hybrid 等）
UPDATE models_canonical
   SET modality = 'audio',
       updated_at = now()
 WHERE modality = 'text'
   AND status = 'active'
   AND (canonical_name ~ '-asr-' OR canonical_name ~ '-tts-' OR canonical_name ~ '-stt-');

-- whisper 家族（InferModality 的 contains 'whisper' 规则对应存量）
UPDATE models_canonical
   SET modality = 'audio',
       updated_at = now()
 WHERE modality = 'text'
   AND status = 'active'
   AND canonical_name ~ 'whisper';
