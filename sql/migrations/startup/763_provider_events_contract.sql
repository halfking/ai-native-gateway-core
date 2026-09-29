-- 763: provider_events 契约对齐（D16 关闭，R14 批判式复审轮，2026-09-30）
--
-- 缺陷（round11 D16 登记为本轮执行）：provider_events 真库形态与契约漂移——
--   · id 无序列、无默认值、可空，无主键，无 (credential_id, ts) 索引；
--   · 漂移源头 = sql/objects/tables/provider_events.sql（dump 式基线，裸表）；
--   · deploy/sql/migrations/2026-07-26-provider-events-local.sql（parity 文件）
--     从未进入任何投递通道（036 死文件同款病），仅 252 与本机在 R14 部署窗
--     手工执行过（该文件 sha256 前 16 = 3487e5c39276a391）。
-- 本迁移把对齐收编正典通道（752 收编 036 同款）：存量漂移库随下次部署自愈，
-- 已手工修复的库幂等 no-op 并补台账登记，新环境经 installer 通道直接拿到契约态。
--
-- 证据链（全部实测，R14 round14 文档 §二/§十）：
--   · 252 修复前：5 列全 nullable / 无序列 / 无默认 / 无 PK / 146 行 id=1..146
--     零重复零 NULL → PK 安全；本机同形态（18 行）。
--   · 修复后活写验证：PK 在位前提下 rows 146→177 正常增长（writer 显式供
--     id：domains/providerprofile/credential_actor.go:153 与
--     pg_reconciliation_store.go:286 均为 COALESCE(max(id),0)+1 形态，
--     不依赖序列默认；PK 使并发双写从"静默重复"变为显式唯一冲突——可接受，
--     该写入路径月级频次）。
--
-- 执行纪律（758 实测教训）：DDL 一律顶层语句，禁止包进 DO $$ … EXECUTE $ddl$ …
-- $$（静默无效）；DO 块只用于纯 SELECT 断言 / setval 守卫逻辑。
-- 幂等：CREATE IF NOT EXISTS ×3 + 幂等 ALTER + conname 守卫 PK + 条件 setval。
-- fail-closed：若存量 id 有重复，ADD CONSTRAINT PK 显式报错而非静默吞掉
-- （重复 id 属既有数据腐坏，不允许此处粉饰）。
--
-- 与 parity 文件的关系：语义等价 + setval 防回退加固（GREATEST/last_value
-- 守卫，绝不把序列 setval 到低于当前值——删行场景防主键冲突）。契约测试
-- migration_763_test.go 钉死两通道一致性。

BEGIN;

-- 空库（全新环境）建表：契约形态直达（与 parity 文件逐列一致——credential_id
-- 必须可空，pg_reconciliation_store.go:287 存在 credential_id=NULL 的真实写入
-- 路径，收紧即炸新环境）；存量库 NOTICE skipped。
CREATE TABLE IF NOT EXISTS public.provider_events (
    id            bigint NOT NULL,
    credential_id bigint,
    event_kind    text,
    payload_json  jsonb,
    ts            timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE IF NOT EXISTS public.provider_events_id_seq
    AS bigint START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE public.provider_events_id_seq OWNED BY public.provider_events.id;

-- 幂等 ALTER（顶层，758 纪律）：存量库该语句重复执行是 no-op。
ALTER TABLE public.provider_events
    ALTER COLUMN id SET DEFAULT nextval('public.provider_events_id_seq');

-- PK：conname 守卫（重放安全）；存量 id 有重复时此处显式失败（fail-closed）。
DO $pk$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'provider_events_pkey'
          AND conrelid = 'public.provider_events'::regclass
    ) THEN
        ALTER TABLE public.provider_events
            ADD CONSTRAINT provider_events_pkey PRIMARY KEY (id);
    END IF;
END
$pk$;

CREATE INDEX IF NOT EXISTS idx_provider_events_credential_ts
    ON public.provider_events (credential_id, ts DESC);

-- 序列推进守卫：仅在 max(id) 超过序列当前位（is_called 语义敏感）时 setval，
-- 绝不回退（防删行场景 setval 到低值 → 后续默认取号撞主键）。
DO $seq$
DECLARE
    v_max    bigint;
    v_last   bigint;
    v_called boolean;
BEGIN
    SELECT COALESCE(max(id), 0) INTO v_max FROM public.provider_events;
    SELECT last_value, is_called INTO v_last, v_called
        FROM public.provider_events_id_seq;
    IF v_max > (CASE WHEN v_called THEN v_last ELSE 0 END) THEN
        PERFORM setval('public.provider_events_id_seq', v_max);
    END IF;
END
$seq$;

-- 台账自登记（695-705 定式）：文件自带 schema_migrations 行，任何通道
-- （脚本/installer/手工重放）应用后账本一致；重复应用 ON CONFLICT 幂等。
INSERT INTO public.schema_migrations (version, description)
VALUES ('763', 'provider_events contract alignment (D16 closure: sequence + default + PK + credential_ts index; idempotent, is_called-aware setval)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
