-- ===========================================================================
-- File:          sql/migrations/startup/935_credit_ledger_owner_campaign_detail.sql
-- Migration:     935
-- Purpose:       「运营管理与价格计费体系」P0（总体设计 §2.3/§2.4，AF-3 修订）：
--                credit_ledger 与 credit_ledger_hot **两表同改**各加 3 列
--                （owner_user / campaign_id / detail），可解释账单五因子的
--                数据载体（detail = {rate_in, rate_out, tokens, discount,
--                rate_multiplier, campaign_id?}）。
--                ★ hot 是独立表（349 刻意与分区父表分离）必须单独 ALTER；
--                  新列落父表后，ensure_credit_ledger_partition 用
--                  `PARTITION OF` 建的新月分区自动继承（声明式分区），
--                  但 698 的 promote_credit_ledger_hot_to_partition 列清单
--                  是**硬编码 10 列** ⇒ 不重铸会把三列静默丢在 promote 边界
--                  （风险登记册「hot 表加列与 promote 函数列不齐 ⇒ 提升丢列」）
--                  ⇒ 本迁移同时重铸 promote 函数（列清单 10 → 13）。
--                ★ 另补 credit_ledger_hot 的 RLS（2026-11-04 真库实测
--                  relrowsecurity=f，pg_policies 零命中）——读侧两臂并读视图
--                  两臂并读视图（credit_ledger_with_current_month，本迁移
--                  扩列 + security_invoker）把它带进租户可见面 ⇒
--                  按「RLS 是租户隔离的唯一载体」红线必须带策略。
--
-- Down: 935_credit_ledger_owner_campaign_detail.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁与超时守卫（照 933 形态）。
--   ★ 实测（2026-11-04，211 真库 acc-pg17/llm_gateway，W2-T0）：
--     credit_ledger 父表=0 行、credit_ledger_hot=0 行、月分区 6 个（全空），
--     三个 CREATE INDEX 全部建在空表上（父表索引级联到全空分区），
--     本地 PG17 空库同脚本墙钟 <0.1s ⇒ 10min 预算差多个数量级。
--   ★ 无 VALIDATE CONSTRAINT；ALTER ADD COLUMN 带 DEFAULT 是元数据级。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 两表同改（★ hot 单独 ALTER，与父表各一次） ─────────────────────────
ALTER TABLE public.credit_ledger
    ADD COLUMN IF NOT EXISTS owner_user  varchar(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS campaign_id bigint,
    ADD COLUMN IF NOT EXISTS detail      jsonb        NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE public.credit_ledger_hot
    ADD COLUMN IF NOT EXISTS owner_user  varchar(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS campaign_id bigint,
    ADD COLUMN IF NOT EXISTS detail      jsonb        NOT NULL DEFAULT '{}'::jsonb;

-- ── ② 索引（父表分区索引自动级联到既有 6 个月分区；hot 同构） ────────────
CREATE INDEX IF NOT EXISTS idx_credit_ledger_owner
    ON public.credit_ledger (tenant_id, owner_user, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_hot_owner
    ON public.credit_ledger_hot (tenant_id, owner_user, created_at DESC);

-- ── ③ hot 表 RLS 补齐（红线：租户可见的表必须带策略） ────────────────────
ALTER TABLE public.credit_ledger_hot ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_credit_ledger_hot ON public.credit_ledger_hot;
CREATE POLICY tenant_isolation_credit_ledger_hot ON public.credit_ledger_hot
    USING (((tenant_id)::text = public.get_current_tenant()));

-- ── ④ 读侧两臂并读视图（AF-3：ListLedger/对账 C 侧的统一底座） ───────────
--   ★ 实施时定夺（总体设计 §2.3 留白）：仓里早已有两臂并读视图
--     credit_ledger_with_current_month（hot ∪ 父表），但 **Go 侧零消费方**
--     ——「台账只写不读」族的又一实例。本迁移不建重复的第二视图，而是
--     **扩既有视图**：末尾追加 3 新列 + source_arm 观测列 + 补
--     security_invoker（526 先例；底层两表现在都带 tenant_isolation 策略）。
--   hot 行 id 与分区行 id 来自同一序列（credit_ledger_partitioned_id_seq）
--   且 promote 是「删 hot、插分区」的搬移 ⇒ id 空间不重叠，UNION ALL 安全。
DROP VIEW IF EXISTS public.credit_ledger_with_current_month;
CREATE VIEW public.credit_ledger_with_current_month
WITH (security_invoker = true) AS
    SELECT id, tenant_id, entry_type, amount, balance_after,
           ref_type, ref_id, note, created_at, pool,
           owner_user, campaign_id, detail,
           'hot' AS source_arm
      FROM public.credit_ledger_hot
    UNION ALL
    SELECT id, tenant_id, entry_type, amount, balance_after,
           ref_type, ref_id, note, created_at, pool,
           owner_user, campaign_id, detail,
           'partition' AS source_arm
      FROM public.credit_ledger;

COMMENT ON VIEW public.credit_ledger_with_current_month IS
    'Two-arm ledger read (AF-3): hot table is the canonical write target and rows only reach monthly partitions via the promoter, so any reader that touches only the parent misses the promotion lag window. 935 appends owner_user/campaign_id/detail plus the source arm and turns on security_invoker so tenant RLS stays effective (credit_ledger_hot gained its own policy in 935).';

-- ── ⑤ promote 函数重铸（列清单 10 → 13，698 正文逐字 + 三列） ────────────
--   与 698 的差异只有三处：RETURNING 加 owner_user/campaign_id/detail、
--   INSERT 列清单同加三列、SELECT 同加三列。其余（timezone pin、retention
--   校验、advisory 语义、ensure 分区调用）一字不改。
CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  -- 698: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 694-pinned ensure_* target was created for.
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credit_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credit_ledger_hot h USING batch b
    WHERE h.id = b.id
    RETURNING h.id, h.tenant_id, h.entry_type, h.amount, h.balance_after,
      h.ref_type, h.ref_id, h.note, h.created_at, h.pool,
      h.owner_user, h.campaign_id, h.detail
  ), inserted AS (
    INSERT INTO public.credit_ledger (
      id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool,
      owner_user, campaign_id, detail)
    SELECT id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool,
      owner_user, campaign_id, detail
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('935', 'credit_ledger + credit_ledger_hot +3 cols (owner_user/campaign_id/detail), owner indexes both arms, hot RLS policy, two-arm read view credit_ledger_with_current_month extended + security_invoker, promote function recast 10->13 cols')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
