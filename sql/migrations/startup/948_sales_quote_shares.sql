-- ===========================================================================
-- File:          sql/migrations/startup/948_sales_quote_shares.sql
-- Migration:     948
-- Purpose:       报价系统 MVP（实施规划 W1-T2，Invoice Ninja
--                QuoteInvitation 形态）：
--                sales_quote_shares —— 客户版分享，**按联系人一行**。
--                token 128bit 随机、**明文不落库**（只存 sha256 hash）、
--                TTL、可撤销；sent/viewed/opened 三时点埋点；
--                访问审计明细并进 detail jsonb（实施规划 §4 明确：
--                949 不建新表，访问明细并入 detail）。
--
--                ★ 公开面（/api/public/quote/{token}）只读、无登录：
--                  查找 = sha256(token) 精确命中 ⇒ 无枚举面；
--                  响应体只从版本快照取销售价列——成本/毛利/供应商身份
--                  物理不进响应体（响应体断言测试是 W1-T2 验收门）。
--
--                定位：自营内部运营表（token 行不含客户敏感数据之外的
--                东西）⇒ 不挂 tenant_isolation_* RLS（风暴档 §5 预登记）。
--
-- Down: 948_sales_quote_shares.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942–947 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + UNIQUE 索引 + 1 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_quote_shares (
    id            bigserial PRIMARY KEY,
    quote_id      bigint NOT NULL,
    version_id    bigint NOT NULL,
    contact_name  text NOT NULL,
    contact_email text NOT NULL DEFAULT '',
    -- sha256(128bit random token)；明文 token 只出现在分享链接里
    token_hash    text NOT NULL,
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    -- 三时点埋点（Invoice Ninja QuoteInvitation：sent/viewed/opened）
    sent_at       timestamptz,
    viewed_at     timestamptz,
    opened_at     timestamptz,
    view_count    int NOT NULL DEFAULT 0,
    -- 访问审计明细并进本列（949 不建新表）：[{at, ip, ua, outcome}, ...]
    -- （实施规划 §4 明确授权；写侧截断保护见 Go 层）
    detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_quote_shares_token_unique
        UNIQUE (token_hash)
);

CREATE INDEX IF NOT EXISTS idx_sqs_quote
    ON public.sales_quote_shares (quote_id);

COMMENT ON TABLE public.sales_quote_shares IS
    'Customer-facing share links for the sales MVP (948), one row per contact (Invoice Ninja QuoteInvitation form). Raw 128-bit token is never stored — only its sha256 (unique index gives O(1) exact-hit lookup for the public endpoint, no enumeration surface). TTL + revocation + sent/viewed/opened timestamps; per-access audit entries are folded into detail jsonb (949 deliberately not created, per the 2026-11-04 plan §4).';
COMMENT ON COLUMN public.sales_quote_shares.token_hash IS
    'sha256 hex of the share token. The public endpoint hashes the incoming token and exact-matches this column; plaintext exists only in the issued link.';
COMMENT ON COLUMN public.sales_quote_shares.detail IS
    'Folded access-audit log (per plan §4: no separate 949 table): {access:[{at, ip, ua, outcome}]} entries appended by the public endpoint, capped by the Go writer.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('948', 'sales_quote_shares per-contact share links (token sha256 unique — plaintext never stored, TTL, revocation, sent/viewed/opened tracking, access audit folded into detail jsonb per plan: no separate 949)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
