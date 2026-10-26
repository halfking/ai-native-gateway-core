-- 911_local_audio_catalog.sql
--
-- 本地音频（ASR/TTS）供应商 catalog 三条目（2026-10-26 多模态标准接口轮）。
--
-- 背景：网关的 /v1/audio/transcriptions 与 /v1/audio/speech 端点早已存在，
-- 但本地 ASR/TTS 服务没有 catalog 预置——运营只能走 custom provider
-- （kind='cloud'，无回环校验），且 whisper.cpp server 只说 /inference 方言
-- （标准 OpenAI 路径 404），custom 注册出来的候选走不到它。
--
-- 本迁移新增三个 kind='local' catalog 条目：
--   whisper-cpp        whisper.cpp server 快速档（large-v3-turbo，默认 :8081）
--   whisper-cpp-large  whisper.cpp server 高精档（large-v3，默认 :8082）
--   macos-say          macOS say+afconvert 的 OpenAI 兼容 TTS 垫片（:8086）
--
-- 要点：
--   1. whisper 两条的 base_url_template 不带 /v1——whisper.cpp 的端点全部
--      挂在根路径（POST /inference），网关按 catalog_code 钉
--      whisper-inference 传输（domains/streaming/audio_whisper_stt.go）。
--   2. discovery_strategy 保持 'auto'：whisper.cpp 无 /v1/models，API 发现
--      失败后自动回落 models_manifest_json（discovery.go 的 fallback 路径，
--      与 azure-openai 同款行为）；macos-say 垫片自答 /v1/models，两条路
--      都通。
--   3. 不 seed providers 行：本地供应商由运营经 admin API 创建
--      （POST /api/providers，catalog kind='local' 自动建占位凭据），
--      与 register-local-provider.sh 的既有仪式一致。
--
-- 升级幂等：ON CONFLICT DO NOTHING。运营侧若已手工建过同 code 条目，
-- 本迁移不覆盖。
--
-- Rollback:
--   DELETE FROM provider_catalog WHERE code IN
--     ('whisper-cpp','whisper-cpp-large','macos-say') AND kind = 'local';

INSERT INTO public.provider_catalog
    (code, tier, display_name, display_name_en, category, kind, protocol,
     base_url_template, docs_url, default_egress_profile, domestic,
     discount_rate_default, models_manifest_json, discovery_strategy,
     models_endpoint_template, seed_pricing_plans_json, price_sources_json,
     hidden, notes, catalog_version, header_profile_code, capabilities, vendor_name)
VALUES
    ('whisper-cpp', 'local', 'whisper.cpp Server（快速档）', 'whisper.cpp Server (fast)',
     'self_host', 'local', 'openai-completions',
     'http://{host}:{port}',
     'https://github.com/ggml-org/whisper.cpp/blob/master/examples/server/README.md',
     'direct', true, 1.0000,
     '["whisper-large-v3-turbo"]'::jsonb, 'auto', NULL, '[]'::jsonb, '{}'::jsonb, false,
     'whisper.cpp 内置 server（快速档，如 ggml-large-v3-turbo）。只说 /inference 方言（标准 /v1/audio/transcriptions 404），网关按 catalog_code 钉 whisper-inference 传输；上游只收 16kHz 单声道 WAV；base_url 不带 /v1',
     1, NULL,
     '{"hosting_type":"whisper.cpp","default_port":8081,"start_hint":"whisper-server -m ggml-large-v3-turbo.bin --host 0.0.0.0 --port 8081 --language auto"}'::jsonb,
     'whisper.cpp'),
    ('whisper-cpp-large', 'local', 'whisper.cpp Server（高精档）', 'whisper.cpp Server (large-v3)',
     'self_host', 'local', 'openai-completions',
     'http://{host}:{port}',
     'https://github.com/ggml-org/whisper.cpp/blob/master/examples/server/README.md',
     'direct', true, 1.0000,
     '["whisper-large-v3"]'::jsonb, 'auto', NULL, '[]'::jsonb, '{}'::jsonb, false,
     'whisper.cpp 内置 server（高精档 ggml-large-v3，多语言金标准，速度约为 turbo 的 1/3）。传输方言与输入约束同 whisper-cpp；双档并存供调用方按模型名选择',
     1, NULL,
     '{"hosting_type":"whisper.cpp","default_port":8082,"start_hint":"whisper-server -m ggml-large-v3.bin --host 0.0.0.0 --port 8082 --language auto"}'::jsonb,
     'whisper.cpp'),
    ('macos-say', 'local', 'macOS say TTS（本地合成）', 'macOS say TTS (local)',
     'self_host', 'local', 'openai-completions',
     'http://{host}:{port}/v1',
     NULL,
     'direct', true, 1.0000,
     '["tts-macos-say"]'::jsonb, 'auto', '/models', '[]'::jsonb, '{}'::jsonb, false,
     'macOS 内置语音合成（say+afconvert）经 OpenAI 兼容垫片暴露 /v1/audio/speech；零依赖离线；voice 收 OpenAI 标准音色名并映射到本机 zh/en 音色；wav/m4a 输出（mp3/opus/flac/pcm 上游不收，垫片如实 400）',
     1, NULL,
     '{"hosting_type":"macos-say","default_port":8086,"start_hint":"python3 modelhub-tts-server.py --host 0.0.0.0 --port 8086"}'::jsonb,
     'apple')
ON CONFLICT DO NOTHING;
