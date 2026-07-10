-- deploy/sql/001_vendor_family_mappings.sql
-- 全球 LLM 模型厂商 Family 映射初始化数据
-- 
-- 用途：
--   1. 为 models_canonical 表提供规范的 family 分类
--   2. 为前端 UI 提供厂商分组依据
--   3. 为路由策略提供 family 筛选基础
--
-- 维护说明：
--   - 新增厂商时在对应区域添加注释
--   - family 命名规则：小写，连字符分隔，尽量保持 vendor-prefix 形式
--   - 同一厂商的不同系列可以是不同 family（如 gemini vs gemma）
--   - notes 字段记录厂商信息
--
-- 数据来源：
--   - discovery/normalize.go (代码映射表)
--   - catalog/display.go (UI 显示映射)
--   - 各厂商官方文档
--   - OpenRouter / Hugging Face 社区清单
--
-- 最后更新：2026-07-10

BEGIN;

-- ============================================================================
-- 1. 国际厂商（北美）
-- ============================================================================

-- OpenAI (美国)
-- family: openai-gpt, openai-embedding, openai-image, openai-audio
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('gpt-4o', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-4 Omni'),
    ('gpt-4-turbo', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-4 Turbo'),
    ('gpt-3.5-turbo', 'openai-gpt', 'seed', 'active', 'OpenAI GPT-3.5 Turbo'),
    ('o1-preview', 'openai-gpt', 'seed', 'active', 'OpenAI o1 系列'),
    ('o3-mini', 'openai-gpt', 'seed', 'active', 'OpenAI o3 mini'),
    ('o5-preview', 'openai-gpt', 'seed', 'active', 'OpenAI o5 preview'),
    ('text-embedding-3-large', 'openai-embedding', 'seed', 'active', 'OpenAI Embedding 模型'),
    ('dall-e-3', 'openai-image', 'seed', 'active', 'OpenAI DALL-E 图像生成'),
    ('whisper-1', 'openai-audio', 'seed', 'active', 'OpenAI Whisper 语音识别'),
    ('tts-1', 'openai-audio', 'seed', 'active', 'OpenAI TTS 语音合成')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Anthropic (美国)
-- family: anthropic-claude
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('claude-sonnet-5', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 5 Sonnet'),
    ('claude-fable-5', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 5 Fable'),
    ('claude-opus-4-8', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 4.8 Opus'),
    ('claude-3-5-sonnet-20241022', 'anthropic-claude', 'seed', 'active', 'Anthropic Claude 3.5 Sonnet')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Meta (美国)
-- family: meta-llama
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('llama-3-70b', 'meta-llama', 'seed', 'active', 'Meta Llama 3 70B'),
    ('llama-3-8b', 'meta-llama', 'seed', 'active', 'Meta Llama 3 8B'),
    ('llama-2-70b-chat', 'meta-llama', 'seed', 'active', 'Meta Llama 2 70B Chat'),
    ('llama-4-preview', 'meta-llama', 'seed', 'active', 'Meta Llama 4 Preview'),
    ('codellama-34b', 'meta-llama', 'seed', 'active', 'Meta Code Llama 34B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Google (美国)
-- family: google-gemini, gemma, google-palm
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('gemini-2.0-flash-exp', 'google-gemini', 'seed', 'active', 'Google Gemini 2.0 Flash'),
    ('gemini-1.5-pro', 'google-gemini', 'seed', 'active', 'Google Gemini 1.5 Pro'),
    ('gemma-2-27b', 'gemma', 'seed', 'active', 'Google Gemma 2 27B'),
    ('gemma-7b', 'gemma', 'seed', 'active', 'Google Gemma 7B'),
    ('palm-2', 'google-palm', 'seed', 'active', 'Google PaLM 2')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Cohere (加拿大)
-- family: cohere
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('command-r-plus', 'cohere', 'seed', 'active', 'Cohere Command R+'),
    ('embed-v3', 'cohere', 'seed', 'active', 'Cohere Embed v3'),
    ('rerank-v3', 'cohere', 'seed', 'active', 'Cohere Rerank v3')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- xAI (美国, Elon Musk)
-- family: xai-grok
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('grok-2', 'xai-grok', 'seed', 'active', 'xAI Grok 2'),
    ('grok-1', 'xai-grok', 'seed', 'active', 'xAI Grok 1')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Microsoft (美国)
-- family: microsoft-phi
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('phi-4', 'microsoft-phi', 'seed', 'active', 'Microsoft Phi-4'),
    ('phi-3-medium', 'microsoft-phi', 'seed', 'active', 'Microsoft Phi-3 Medium')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- NVIDIA (美国)
-- family: nvidia-nemotron
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('nemotron-4-340b', 'nvidia-nemotron', 'seed', 'active', 'NVIDIA Nemotron-4 340B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Perplexity (美国)
-- family: perplexity-sonar
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('sonar-pro', 'perplexity-sonar', 'seed', 'active', 'Perplexity Sonar Pro')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 2. 欧洲厂商
-- ============================================================================

-- Mistral AI (法国)
-- family: mistral
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('mistral-large', 'mistral', 'seed', 'active', 'Mistral AI Mistral Large'),
    ('mixtral-8x22b', 'mistral', 'seed', 'active', 'Mistral AI Mixtral 8x22B'),
    ('codestral', 'mistral', 'seed', 'active', 'Mistral AI Codestral'),
    ('ministral-8b', 'mistral', 'seed', 'active', 'Mistral AI Ministral 8B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Stability AI (英国)
-- family: stability
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('stable-diffusion-3', 'stability', 'seed', 'active', 'Stability AI Stable Diffusion 3'),
    ('stable-lm-2', 'stability', 'seed', 'active', 'Stability AI Stable LM 2')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 3. 中国厂商 - 互联网巨头
-- ============================================================================

-- 阿里云 / Alibaba Cloud
-- family: qwen, qwen2, qwen3, qwq (各版本独立)
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('qwen-max', 'qwen', 'seed', 'active', '阿里云 通义千问 Max'),
    ('qwen-turbo', 'qwen', 'seed', 'active', '阿里云 通义千问 Turbo'),
    ('qwen2-72b', 'qwen2', 'seed', 'active', '阿里云 Qwen2 72B'),
    ('qwen3-235b', 'qwen3', 'seed', 'active', '阿里云 Qwen3 235B'),
    ('qwq-32b-preview', 'qwq', 'seed', 'active', '阿里云 QwQ 32B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 腾讯 / Tencent
-- family: hunyuan
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('hunyuan-lite', 'hunyuan', 'seed', 'active', '腾讯 混元 Lite'),
    ('hunyuan-turbo', 'hunyuan', 'seed', 'active', '腾讯 混元 Turbo'),
    ('hunyuan-pro', 'hunyuan', 'seed', 'active', '腾讯 混元 Pro')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 字节跳动 / ByteDance
-- family: doubao
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('doubao-pro-32k', 'doubao', 'seed', 'active', '字节跳动 豆包 Pro 32K'),
    ('doubao-lite-4k', 'doubao', 'seed', 'active', '字节跳动 豆包 Lite 4K')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 百度 / Baidu
-- family: ernie
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('ernie-4.0-turbo-128k', 'ernie', 'seed', 'active', '百度 文心一言 4.0 Turbo'),
    ('ernie-3.5-8k', 'ernie', 'seed', 'active', '百度 文心一言 3.5')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 4. 中国厂商 - AI 独角兽
-- ============================================================================

-- 智谱 AI / Zhipu AI
-- family: zhipu-glm
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('glm-4-plus', 'zhipu-glm', 'seed', 'active', '智谱AI GLM-4 Plus'),
    ('glm-4v-plus', 'zhipu-glm', 'seed', 'active', '智谱AI GLM-4V Plus (多模态)'),
    ('codegeex-4', 'zhipu-glm', 'seed', 'active', '智谱AI CodeGeeX 4'),
    ('chatglm-turbo', 'zhipu-glm', 'seed', 'active', '智谱AI ChatGLM Turbo')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 月之暗面 / Moonshot AI
-- family: moonshot
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('moonshot-v1-128k', 'moonshot', 'seed', 'active', '月之暗面 Moonshot v1 128K'),
    ('kimi-chat', 'moonshot', 'seed', 'active', '月之暗面 Kimi Chat')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 零一万物 / 01.AI (李开复)
-- family: yi
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('yi-large', 'yi', 'seed', 'active', '零一万物 Yi Large'),
    ('yi-medium', 'yi', 'seed', 'active', '零一万物 Yi Medium')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 稀宇科技 / MiniMax
-- family: minimax
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('minimax-m3', 'minimax', 'seed', 'active', '稀宇科技 MiniMax M3'),
    ('abab6.5s-chat', 'minimax', 'seed', 'active', '稀宇科技 ABAB 6.5s Chat (旧系列)')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 深度求索 / DeepSeek
-- family: deepseek
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('deepseek-chat', 'deepseek', 'seed', 'active', '深度求索 DeepSeek Chat'),
    ('deepseek-coder', 'deepseek', 'seed', 'active', '深度求索 DeepSeek Coder')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 阶跃星辰 / StepFun
-- family: stepfun
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('step-1-256k', 'stepfun', 'seed', 'active', '阶跃星辰 Step-1 256K'),
    ('step-2-16k', 'stepfun', 'seed', 'active', '阶跃星辰 Step-2 16K')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 百川智能 / Baichuan
-- family: baichuan
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('baichuan-53b', 'baichuan', 'seed', 'active', '百川智能 Baichuan 53B'),
    ('baichuan-13b', 'baichuan', 'seed', 'active', '百川智能 Baichuan 13B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 光年之外 / LightYear (王慧文)
-- family: kuae
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('kuae-1.5', 'kuae', 'seed', 'active', '光年之外 光年 1.5'),
    ('skywork-13b', 'kuae', 'seed', 'active', '光年之外 天工 13B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 商汤科技 / SenseTime
-- family: sensetime
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('sensechat-5', 'sensetime', 'seed', 'active', '商汤科技 日日新 SenseChat 5'),
    ('sensenova-xl', 'sensetime', 'seed', 'active', '商汤科技 SenseNova XL')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 5. 中国厂商 - 传统科技公司
-- ============================================================================

-- 科大讯飞 / iFlytek
-- family: spark
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('spark-max', 'spark', 'seed', 'active', '科大讯飞 星火 Max'),
    ('spark-3.5', 'spark', 'seed', 'active', '科大讯飞 星火 3.5')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 小米 / Xiaomi
-- family: mimo
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('mimo-v2.5-pro', 'mimo', 'seed', 'active', '小米 小米大模型 v2.5 Pro')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 华为 / Huawei
-- family: pangu
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('pangu-chat', 'pangu', 'seed', 'active', '华为 盘古大模型')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- 网易 / NetEase
-- family: youdao
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('youdao-translate', 'youdao', 'seed', 'active', '网易 有道翻译')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 6. 其他地区厂商
-- ============================================================================

-- NAVER (韩国)
-- family: naver-hyperclova
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('hyperclova-x', 'naver-hyperclova', 'seed', 'active', 'NAVER HyperCLOVA X')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Rinna (日本)
-- family: rinna
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('rinna-3.6b', 'rinna', 'seed', 'active', 'Rinna 3.6B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Inception / TII (阿联酋)
-- family: falcon
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('falcon-180b', 'falcon', 'seed', 'active', 'Inception Falcon 180B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- SDAIA (沙特)
-- family: allamoe
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('allamoe-13b', 'allamoe', 'seed', 'active', 'SDAIA ALLaMo-E 13B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- ============================================================================
-- 7. 开源社区
-- ============================================================================

-- EleutherAI
-- family: eleutherai
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('gpt-neox-20b', 'eleutherai', 'seed', 'active', 'EleutherAI GPT-NeoX 20B'),
    ('pythia-12b', 'eleutherai', 'seed', 'active', 'EleutherAI Pythia 12B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- BigScience
-- family: bigscience-bloom
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('bloom-176b', 'bigscience-bloom', 'seed', 'active', 'BigScience BLOOM 176B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Together AI
-- family: together
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('together-7b', 'together', 'seed', 'active', 'Together AI 7B')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

-- Cursor (IDE)
-- family: cursor
INSERT INTO models_canonical (canonical_name, family, source, status, notes)
VALUES 
    ('cursor-small', 'cursor', 'seed', 'active', 'Cursor Small')
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();

COMMIT;

-- 统计信息
DO $$
DECLARE
    total_models INT;
    total_families INT;
    seed_models INT;
BEGIN
    SELECT count(*) INTO total_models FROM models_canonical;
    SELECT count(DISTINCT family) INTO total_families FROM models_canonical WHERE family IS NOT NULL;
    SELECT count(*) INTO seed_models FROM models_canonical WHERE source = 'seed';
    
    RAISE NOTICE '=== 初始化完成 ===';
    RAISE NOTICE '总模型数: %', total_models;
    RAISE NOTICE '种子模型数: %', seed_models;
    RAISE NOTICE 'Family 数量: %', total_families;
END $$;
