-- Migration 333: repair models_canonical.family + provider_models.canonical_id
-- Date: 2026-07-09
-- Purpose: 修复"通过供应商凭据发现的模型 family 永远是 'unknown'"的 bug
--          并修复 claude-sonnet-5 因 canonical_id NULL 路由失败的 outage
--
-- Background:
--   admin/provider_vendor.go 的旧版本在 vendor refresh 时硬编码
--       INSERT INTO models_canonical (canonical_name, family, source, status)
--       VALUES ($1, 'unknown', 'provider_refresh', 'active')
--       ON CONFLICT (canonical_name) DO NOTHING
--
--   直接后果（2026-07-09 已在 252 PG 上确认）：
--     a) apiclaude 的 claude-sonnet-5 / claude-fable-5 在 models_canonical
--        里 family='unknown', tags 缺 family:<id>; 后续 /models 页 chip 过滤、
--        routing_policy.featured_models 白名单都漏选
--     b) claude-sonnet-5 对应的 provider_models.id=178176.canonical_id = NULL
--        → router 走 provider_models JOIN models_canonical 丢行
--        → request_logs_2026_07 印证所有 client_model='claude-sonnet-5' 请求
--          canonical_id IS NULL，error_kind='provider_error'
--     c) model_aliases 表里完全没有 claude-sonnet-5 / claude-fable-5 的
--        raw_name 别名行；客户端用 anthropic/claude-sonnet-5 时也会失败
--
-- Fix (此 migration 同时跑 + 生产代码 patch 一并生效):
--   1. 已知 leading-token 收口回填 models_canonical.family
--      与 discovery/normalize.go:43 vendorCanonicalFamilies 一致
--   2. 修复 claude-sonnet-5 的 provider_models.canonical_id NULL
--   3. 补登 model_aliases（claude-sonnet-5 / claude-fable-5 及带 vendor 前缀）
--
-- 兼容性: 全部使用 ON CONFLICT / WHERE 守卫，幂等可重复跑。
--         仅当 family='unknown' 时才覆盖，避免踩到管理员手动分类。

BEGIN;

-- 1. family 回填：与 discovery/normalize.go vendorCanonicalFamilies 表对齐
UPDATE models_canonical
SET    family = CASE
        -- 多对一收口
        WHEN lower(canonical_name) ~ '^(claude|claude-)'                 THEN 'anthropic-claude'
        WHEN lower(canonical_name) ~ '^(gpt|o1|o3|o4)'                  THEN 'openai-gpt'
        WHEN lower(canonical_name) ~ '^(llama|llama2|llama3)'           THEN 'meta-llama'
        WHEN lower(canonical_name) ~ '^(gemini)'                        THEN 'google-gemini'
        WHEN lower(canonical_name) ~ '^(mistral|ministral|mixtral)'     THEN 'mistral'
        WHEN lower(canonical_name) ~ '^(glm)'                           THEN 'zhipu-glm'
        WHEN lower(canonical_name) ~ '^(kimi|moonshot)'                 THEN 'moonshot'
        WHEN lower(canonical_name) ~ '^(step|stepfun)'                  THEN 'stepfun'
        WHEN lower(canonical_name) ~ '^(doubao|seed)'                   THEN 'doubao'
        WHEN lower(canonical_name) ~ '^(qwen|qwen2|qwen3)'              THEN 'qwen'
        WHEN lower(canonical_name) ~ '^(deepseek)'                      THEN 'deepseek'
        WHEN lower(canonical_name) ~ '^(minimax)'                       THEN 'minimax'
        WHEN lower(canonical_name) ~ '^(mimo)'                          THEN 'mimo'
        WHEN lower(canonical_name) ~ '^(baichuan)'                      THEN 'baichuan'
        WHEN lower(canonical_name) ~ '^(yi)'                            THEN 'yi'
        WHEN lower(canonical_name) ~ '^(sensenova|sensechat)'           THEN 'sensenova'
        WHEN lower(canonical_name) ~ '^(spark|xunfei)'                  THEN 'spark'
        WHEN lower(canonical_name) ~ '^(grok)'                          THEN 'xai'
        WHEN lower(canonical_name) ~ '^(sonar)'                         THEN 'perplexity'
        WHEN lower(canonical_name) ~ '^(rerank|embed|bge|cohere)'       THEN 'embed'
        ELSE family
       END
WHERE  family = 'unknown'
  AND  canonical_name IS NOT NULL
  AND  lower(canonical_name) ~ '^(claude|gpt|o1|o3|o4|llama|gemini|mistral|ministral|mixtral|glm|kimi|moonshot|step|stepfun|doubao|seed|qwen|deepseek|minimax|mimo|baichuan|yi|sensenova|sensechat|spark|xunfei|grok|sonar|rerank|embed|bge|cohere)';

-- 2. 把历史 'unknown' 行若 leading token 完全没匹配，保留为 'unknown'，
--    让后续 discovery 接管或管理员手工分类。
--    （上面 CASE 的 ELSE 已经保留原值，无需额外动作）

-- 3. 修复 provider_models.canonical_id = NULL 的 routing bug
--    专门修 claude-sonnet-5（已知 case）+ 提供一个通用兜底：任何
--    provider_models.raw_model_name 与 models_canonical.canonical_name
--    字面相等但 canonical_id IS NULL 的行都补上
UPDATE provider_models pm
SET    canonical_id = mc.id
FROM   models_canonical mc
WHERE  pm.canonical_id IS NULL
  AND  pm.raw_model_name = mc.canonical_name;

-- 4. 给 claude-sonnet-5 / claude-fable-5 补 raw_name 别名
--    客户端用 'anthropic/claude-sonnet-5' 这种带 vendor 前缀的写法也能识别
--    用 (canonical_name_match, raw_name, surface) 三元组而非 LATERAL 笛卡尔，
--    避免错把 'claude-fable-5' 插到 claude-sonnet-5 下面。
INSERT INTO model_aliases (canonical_id, raw_name, status, surface, notes)
SELECT mc.id, x.raw_name, 'active', x.surface,
       'auto-fill: 333 migration (vendor-refresh family fix)'
FROM   models_canonical mc
JOIN   (VALUES
           ('claude-sonnet-5',         'claude-sonnet-5',         'openai'),
           ('claude-sonnet-5',         'anthropic/claude-sonnet-5','openrouter'),
           ('claude-fable-5',          'claude-fable-5',          'openai'),
           ('claude-fable-5',          'anthropic/claude-fable-5', 'openrouter')
       ) AS x(mc_name, raw_name, surface)
       ON x.mc_name = mc.canonical_name
WHERE  NOT EXISTS (
        SELECT 1 FROM model_aliases ma
        WHERE  ma.canonical_id = mc.id AND ma.raw_name = x.raw_name
       );

-- 5. 触发 credential_model_index_hot 重建（下一次请求会自动触发，
--    这里只是明确化意图，避免依赖后台 reconciler 的时序）
SELECT pg_notify('auto_route_refresh', 'manual:333');

COMMIT;
