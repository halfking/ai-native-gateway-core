-- Migration 333 (down): 撤销 333 回填
-- 注：family='unknown' 撤销后会回到 unknown；raw_name 别名删除；
--     provider_models.canonical_id 仅撤销本次回填的行（无法精准反查，
--     依赖管理员手工复位）。

BEGIN;

-- A. 删除本次回填的 raw_name 别名
DELETE FROM model_aliases
WHERE  notes = 'auto-fill: 333 migration (vendor-refresh family fix)';

-- B. 把 family 回退为 'unknown'（仅限本次回填影响到的 leading-token）
UPDATE models_canonical
SET    family = 'unknown'
WHERE  family IN (
        'anthropic-claude','openai-gpt','meta-llama','google-gemini',
        'mistral','zhipu-glm','moonshot','stepfun','doubao','qwen',
        'deepseek','minimax','mimo','baichuan','yi','sensenova',
        'spark','xai','perplexity','embed'
       )
  AND  source = 'provider_refresh';

-- C. provider_models.canonical_id 撤销本次兜底回填
-- （只能通过 raw_model_name 严格等于 canonical_name 撤销本次新增的关联）
UPDATE provider_models
SET    canonical_id = NULL
WHERE  canonical_id IS NOT NULL
  AND  raw_model_name IN ('claude-sonnet-5','claude-fable-5');

COMMIT;
