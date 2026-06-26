-- ============================================================================
-- llm-gateway-go 提供商初始化数据
-- Source: 184 / providers (sanitized)
-- 说明:
--   1. 仅保留 default 租户下的标准 provider 配置
--   2. 不包含 credentials / api_keys / 自定义业务 provider
--   3. 不包含已手动禁用的业务化 provider 条目
-- ============================================================================

INSERT INTO public.providers (
    id,
    tenant_id,
    code,
    display_name,
    catalog_code,
    is_custom,
    user_overrides_json,
    kind,
    category,
    protocol,
    base_url,
    egress_profile,
    domestic,
    discount_rate,
    enabled,
    manual_disabled,
    quality_fix_mode
) VALUES
    (1, 'default', 'xiaomi', '小米大模型', 'xiaomi', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://token-plan-cn.xiaomimimo.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (2, 'default', 'anthropic', 'Anthropic', 'anthropic', false, '[]'::jsonb, 'cloud', 'official', 'anthropic-messages', 'https://api.anthropic.com', 'proxy', false, 1.0000, true, false, 'off'),
    (3, 'default', 'azure-openai', 'Azure OpenAI', 'azure-openai', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://{resource}.openai.azure.com/openai/deployments/{deployment}', 'proxy', false, 1.0000, true, false, 'off'),
    (4, 'default', 'baichuan', '百川 AI', 'baichuan', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.baichuan-ai.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (5, 'default', 'cohere', 'Cohere', 'cohere', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.cohere.com/compatibility/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (6, 'default', 'deepseek', 'DeepSeek', 'deepseek', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.deepseek.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (7, 'default', 'doubao', '豆包（字节跳动）', 'doubao', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'direct', true, 1.0000, true, false, 'off'),
    (8, 'default', 'fireworks', 'Fireworks AI', 'fireworks', false, '[]'::jsonb, 'cloud', 'aggregator', 'openai-completions', 'https://api.fireworks.ai/inference/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (9, 'default', 'github-copilot', 'GitHub Copilot', 'github-copilot', false, '[]'::jsonb, 'cloud', 'official_proxy', 'openai-completions', 'https://api.githubcopilot.com', 'proxy', false, 1.0000, true, false, 'off'),
    (10, 'default', 'google-gemini', 'Google Gemini', 'google-gemini', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://generativelanguage.googleapis.com/v1beta/openai', 'proxy', false, 1.0000, true, false, 'off'),
    (11, 'default', 'groq', 'Groq', 'groq', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.groq.com/openai/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (12, 'default', 'llamacpp', 'llama.cpp Server', 'llamacpp', false, '[]'::jsonb, 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, false, 'off'),
    (13, 'default', 'lmstudio', 'LM Studio', 'lmstudio', false, '[]'::jsonb, 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, false, 'off'),
    (14, 'default', 'minimax', 'MiniMax', 'minimax', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.minimaxi.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (15, 'default', 'mistral', 'Mistral AI', 'mistral', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.mistral.ai/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (16, 'default', 'mlx', 'Apple MLX Server', 'mlx', false, '[]'::jsonb, 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, false, 'off'),
    (17, 'default', 'moonshot', 'Moonshot / Kimi', 'moonshot', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.moonshot.cn/v1', 'direct', true, 1.0000, true, false, 'off'),
    (18, 'default', 'nvidia', 'NVIDIA NIM', 'nvidia', false, '[]'::jsonb, 'cloud', 'aggregator', 'openai-completions', 'https://integrate.api.nvidia.com/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (19, 'default', 'ollama', 'Ollama', 'ollama', false, '[]'::jsonb, 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, false, 'off'),
    (20, 'default', 'openai', 'OpenAI', 'openai', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.openai.com/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (21, 'default', 'openrouter', 'OpenRouter', 'openrouter', false, '[]'::jsonb, 'cloud', 'third_party_relay', 'openai-completions', 'https://openrouter.ai/api/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (22, 'default', 'perplexity', 'Perplexity AI', 'perplexity', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.perplexity.ai', 'proxy', false, 1.0000, true, false, 'off'),
    (23, 'default', 'qwen', '阿里百炼（通义千问）', 'qwen', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://dashscope.aliyuncs.com/compatible-mode/v1', 'direct', true, 1.0000, true, false, 'off'),
    (24, 'default', 'sensenova', '商汤 SenseNova', 'sensenova', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.sensenova.cn/compatible-mode/v1', 'direct', true, 1.0000, true, false, 'off'),
    (25, 'default', 'siliconflow', '硅基流动', 'siliconflow', false, '[]'::jsonb, 'cloud', 'aggregator', 'openai-completions', 'https://api.siliconflow.cn/v1', 'direct', true, 1.0000, true, false, 'off'),
    (26, 'default', 'stepfun', '阶跃星辰', 'stepfun', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.stepfun.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (27, 'default', 'together', 'Together AI', 'together', false, '[]'::jsonb, 'cloud', 'aggregator', 'openai-completions', 'https://api.together.xyz/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (28, 'default', 'vllm', 'vLLM Server', 'vllm', false, '[]'::jsonb, 'local', 'self_host', 'openai-completions', 'http://{host}:{port}/v1', 'direct', true, 1.0000, true, false, 'off'),
    (29, 'default', 'volcengine-coding', '火山方舟 Coding', 'volcengine-coding', false, '[]'::jsonb, 'cloud', 'aggregator', 'openai-completions', 'https://ark.cn-beijing.volces.com/api/coding/v3', 'direct', true, 1.0000, true, false, 'off'),
    (30, 'default', 'xai', 'xAI (Grok)', 'xai', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.x.ai/v1', 'proxy', false, 1.0000, true, false, 'off'),
    (31, 'default', 'yi', '零一万物', 'yi', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://api.lingyiwanwu.com/v1', 'direct', true, 1.0000, true, false, 'off'),
    (32, 'default', 'zhipu', '智谱AI', 'zhipu', false, '[]'::jsonb, 'cloud', 'official', 'openai-completions', 'https://open.bigmodel.cn/api/coding/paas/v4', 'direct', true, 1.0000, true, false, 'off')
ON CONFLICT (tenant_id, code) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    catalog_code = EXCLUDED.catalog_code,
    is_custom = EXCLUDED.is_custom,
    user_overrides_json = EXCLUDED.user_overrides_json,
    kind = EXCLUDED.kind,
    category = EXCLUDED.category,
    protocol = EXCLUDED.protocol,
    base_url = EXCLUDED.base_url,
    egress_profile = EXCLUDED.egress_profile,
    domestic = EXCLUDED.domestic,
    discount_rate = EXCLUDED.discount_rate,
    enabled = EXCLUDED.enabled,
    manual_disabled = EXCLUDED.manual_disabled,
    quality_fix_mode = EXCLUDED.quality_fix_mode,
    updated_at = NOW();

SELECT setval('public.providers_id_seq', COALESCE((SELECT MAX(id) FROM providers), 1), true);