-- ============================================================================
-- 2026-07-13 model_credit_rates 多模态字段
-- ============================================================================
--
-- 在 /model-pricing 标准模型定价页加入"多模态按 token 计费"的支持。
-- 模型 (gemini-2.5-flash-image, doubao-seed-1-6, glm-4v 等) 的 image / audio /
-- video token 由 IR 层 ir.ResponseUsage 提取后，可以在 ChargeRequest 中按
-- credits_per_1m_{image,audio,video}_tokens 单独计费。
--
-- 之前的 model_credit_rates 只支持 in/out/cache_in/cache_out 四个维度，无法
-- 表达"图片按 token 折算"或"音频按 token 折算"，需要为多模态模型新增三个
-- 独立维度。每个维度都有独立的 manual_* 标志，可以单独跟随全局基准或被手工
-- 覆盖，与现有 4 个维度一致。
--
-- 兼容策略：
--   - 新字段全部为 nullable / DEFAULT false，旧数据 0 行为（跟随全局）
--   - 前端 UI 默认不显示 multimodal 列，仅当 manual_* 为 true 时展示在状态徽章
--   - 后端 calc_credits 接收 multimodal token 计数，缺失时按 0 处理
--   - 不删除 / 不重命名现有任何字段，frontend 不刷新即可正常工作

BEGIN;

ALTER TABLE public.model_credit_rates
  ADD COLUMN IF NOT EXISTS credits_per_1m_image_tokens bigint,
  ADD COLUMN IF NOT EXISTS credits_per_1m_audio_tokens bigint,
  ADD COLUMN IF NOT EXISTS credits_per_1m_video_tokens bigint,
  ADD COLUMN IF NOT EXISTS manual_image boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS manual_audio boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS manual_video boolean DEFAULT false NOT NULL;

COMMENT ON COLUMN public.model_credit_rates.credits_per_1m_image_tokens IS
  '每 1M image_tokens 的 credit 单价（NULL/0 = 跟随全局基准），用于多模态视觉输入计费';
COMMENT ON COLUMN public.model_credit_rates.credits_per_1m_audio_tokens IS
  '每 1M audio_tokens 的 credit 单价，用于多模态音频输入/输出计费';
COMMENT ON COLUMN public.model_credit_rates.credits_per_1m_video_tokens IS
  '每 1M video_tokens 的 credit 单价，用于多模态视频输入计费';
COMMENT ON COLUMN public.model_credit_rates.manual_image IS
  '图像维度是否手工定价；false = 跟随全局基准 × 折扣';
COMMENT ON COLUMN public.model_credit_rates.manual_audio IS
  '音频维度是否手工定价；false = 跟随全局基准 × 折扣';
COMMENT ON COLUMN public.model_credit_rates.manual_video IS
  '视频维度是否手工定价；false = 跟随全局基准 × 折扣';

COMMIT;
