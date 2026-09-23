-- ===========================================================================
-- File:          installer/cmd/llm-gw-installer/embeddata/startup/743_normalize_provider_protocol.sql
-- Migration:     743
-- Database:      llm_gateway
-- Purpose:       R60 S3-F4 存量数据清洗（2026-09-23 vapeur 事故复盘）：
--                providers.protocol 与 provider_catalog.protocol 里历史脏值
--                归一到 catalog 五值枚举。providers.protocol 列无 CHECK 约束
--                （providers.sql:16），"openai-response"（单数，vapeur 事故
--                的确切脏值）、"openai"、"anthropic" 等别名拼写会静默入库。
--                R59 已封写边界（updateProvider PATCH / createProvider /
--                free-pool register 均走 NormalizeProviderProtocol），本迁移
--                清洗存量行，使读/比较面与写面一致。
--
--                归一规则与 provider/catalog/protocol_normalize.go 的
--                NormalizeProviderProtocol / normalizeProtocolKey 逐条对齐：
--                key = lower(trim(protocol))，'_' 折叠为 '-'；已知别名映射
--                到 canonical；canonical 本身不是别名 key，故重复执行为
--                no-op（幂等）。无法识别的值保持原样（与 Go 侧"归一失败
--                保留原值"语义一致，本迁移不碰它们）。
--
--                provider_catalog.protocol 自建表起带 CHECK 约束
--                （provider_catalog_protocol_check，五值枚举），正常不应有
--                脏值；此处同样清洗属防御性对账（例如 CHECK 约束晚于数据
--                落地的极端环境），命中 0 行时 no-op。
--
--                迁移号核对：2026-09-23 git fetch origin（origin/main =
--                ef87317c5）确认 740/742 已占用、741 为 R58 B11 预留
--                （全历史无 741 文件），743 从未使用，故从 743 起。
-- ===========================================================================

BEGIN;

-- providers.protocol 存量脏值归一。JOIN 条件即 normalizeProtocolKey 的 SQL
-- 等价：btrim(两端空白) + lower + '_'→'-'。
UPDATE public.providers AS p
SET protocol = v.canonical
FROM (VALUES
    -- openai-completions（chat）家族
    ('openai',                 'openai-completions'),
    ('chat',                   'openai-completions'),
    ('openai-chat',            'openai-completions'),
    ('openai-completion',      'openai-completions'),
    ('openai-chatcompletion',  'openai-completions'),
    ('openai-chat-completion', 'openai-completions'),
    ('chat-completions',       'openai-completions'),
    ('chatcompletions',        'openai-completions'),
    ('openai-chat-completions','openai-completions'),
    -- openai-responses 家族 —— 'openai-response'（单数）为 vapeur 事故脏值
    ('openai-response',        'openai-responses'),
    ('response',               'openai-responses'),
    ('responses',              'openai-responses'),
    ('openai-response-api',    'openai-responses'),
    -- anthropic 家族
    ('anthropic',              'anthropic-messages'),
    ('anthropic-message',      'anthropic-messages'),
    ('claude',                 'anthropic-messages'),
    ('claude-messages',        'anthropic-messages'),
    -- gemini 家族
    ('gemini',                 'gemini-generate'),
    ('google-gemini',          'gemini-generate'),
    -- ollama 家族
    ('ollama',                 'ollama-native')
) AS v(dirty, canonical)
WHERE lower(replace(btrim(p.protocol), '_', '-')) = v.dirty;

-- provider_catalog.protocol 防御性对账（CHECK 约束在位时恒命中 0 行）。
UPDATE public.provider_catalog AS pc
SET protocol = v.canonical
FROM (VALUES
    ('openai',                 'openai-completions'),
    ('chat',                   'openai-completions'),
    ('openai-chat',            'openai-completions'),
    ('openai-completion',      'openai-completions'),
    ('openai-chatcompletion',  'openai-completions'),
    ('openai-chat-completion', 'openai-completions'),
    ('chat-completions',       'openai-completions'),
    ('chatcompletions',        'openai-completions'),
    ('openai-chat-completions','openai-completions'),
    ('openai-response',        'openai-responses'),
    ('response',               'openai-responses'),
    ('responses',              'openai-responses'),
    ('openai-response-api',    'openai-responses'),
    ('anthropic',              'anthropic-messages'),
    ('anthropic-message',      'anthropic-messages'),
    ('claude',                 'anthropic-messages'),
    ('claude-messages',        'anthropic-messages'),
    ('gemini',                 'gemini-generate'),
    ('google-gemini',          'gemini-generate'),
    ('ollama',                 'ollama-native')
) AS v(dirty, canonical)
WHERE lower(replace(btrim(pc.protocol), '_', '-')) = v.dirty;

-- 验证（fail-closed，742 惯用法）：两表都不允许再残留已知别名脏值。
DO $$
DECLARE
  leftovers int;
BEGIN
  SELECT count(*) INTO leftovers
  FROM public.providers p
  WHERE lower(replace(btrim(p.protocol), '_', '-')) IN (
      'openai', 'chat', 'openai-chat', 'openai-completion', 'openai-chatcompletion',
      'openai-chat-completion', 'chat-completions', 'chatcompletions', 'openai-chat-completions',
      'openai-response', 'response', 'responses', 'openai-response-api',
      'anthropic', 'anthropic-message', 'claude', 'claude-messages',
      'gemini', 'google-gemini', 'ollama');
  SELECT leftovers + count(*) INTO leftovers
  FROM public.provider_catalog pc
  WHERE lower(replace(btrim(pc.protocol), '_', '-')) IN (
      'openai', 'chat', 'openai-chat', 'openai-completion', 'openai-chatcompletion',
      'openai-chat-completion', 'chat-completions', 'chatcompletions', 'openai-chat-completions',
      'openai-response', 'response', 'responses', 'openai-response-api',
      'anthropic', 'anthropic-message', 'claude', 'claude-messages',
      'gemini', 'google-gemini', 'ollama');
  IF leftovers > 0 THEN
    RAISE EXCEPTION '743 up: % provider rows still carry alias protocol spellings', leftovers;
  END IF;
  RAISE NOTICE '743 up: providers/provider_catalog protocol values normalized to catalog enum';
END $$;

-- Ledger self-registration（710/734/738/740/742 惯例）。带存在性守卫：一次性
-- 测试库（TEST_PG_URL 直灌裸 SQL）没有 installer 基座的 schema_migrations
-- 表，守卫使迁移在两种环境都可执行；生产库恒有该表，行为与 742 一致。
DO $$
BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    INSERT INTO public.schema_migrations (version, description)
    VALUES ('743', 'normalize legacy providers/provider_catalog protocol alias spellings to the catalog enum (R60 S3-F4: vapeur incident data cleanup)')
    ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
  END IF;
END $$;

COMMIT;
