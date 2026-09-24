-- ===========================================================================
-- File:          installer/cmd/llm-gw-installer/embeddata/startup/743_normalize_provider_protocol.down.sql
-- Migration:     743 down
-- Purpose:       按反向映射把 743 归一过的 protocol 还原为代表性脏值。
--
--                【已知不完美处，声明如下】protocol 列没有变更台账，743 up
--                触碰过哪些行不可回照：升级前本来就是 canonical 五值的干净
--                行与升级产生的行无法区分，本 down 会把它们一并还原成别名
--                拼写（数据回污）。因此本 down 只用于测试/回滚演练库；
--                生产库回滚前必须先对 providers.protocol /
--                provider_catalog.protocol 做人工快照。每个 canonical 的
--                反向代表值取实证过的脏拼写（vapeur 事故的
--                "openai-response"、client.go 记载的历史 "openai" /
--                "anthropic" 等），而非完整别名表——一对多反向本身不可逆。
-- ===========================================================================

BEGIN;

-- 反向映射（canonical → 代表性脏值）。与 up 的正向映射不对称是本迁移的
-- 固有不完美处，见文件头声明。
UPDATE public.providers AS p
SET protocol = v.legacy
FROM (VALUES
    ('openai-completions',  'openai'),
    ('openai-responses',    'openai-response'),
    ('anthropic-messages',  'anthropic'),
    ('gemini-generate',     'gemini'),
    ('ollama-native',       'ollama')
) AS v(canonical, legacy)
WHERE p.protocol = v.canonical;

-- provider_catalog 不参与反向还原：该列自建表起带 provider_catalog_protocol_check
-- （五值枚举 CHECK），脏值根本无法持久化，743 up 对它恒为 no-op；反过来把
-- canonical 改回别名拼写会直接违反 CHECK 使本 down 事务中止。

DO $$
BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    DELETE FROM public.schema_migrations WHERE version = '743';
  END IF;
END $$;

COMMIT;
