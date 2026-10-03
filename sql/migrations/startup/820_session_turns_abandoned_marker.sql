-- ===========================================================================
-- File:          sql/migrations/startup/820_session_turns_abandoned_marker.sql
-- Migration:     820
-- Database:      llm_gateway
-- Purpose:       在**会话族内**给「请求开始了却从没有终态」补一类状态。
--
-- ★ 为什么是「补一类状态」而不是另建小表（2026-10-03 用户拍板，取代迁移 819）
--
-- 819 的做法是新建 `public.request_abandoned`，t0 插一行、终态 DELETE 那行。
-- 那种形态要求一个**独立表**才能表达「集合语义」；本迁移改为在既有
-- session_turns 上表达，于是必须绕开该表的一条硬约束：
--
--   **session_turns 按 (tenant_id, request_id, partition_date) 幂等，
--   且首次插入后不可更新**（`turn_writer.go` 的 `ON CONFLICT DO NOTHING`
--   + RowsAffected==0 后读，注释在 :257-265）。
--
-- ⇒ 「先插一行 in_progress 占位、终态时再 UPDATE 它」这条路是**走不通的**：
-- DO NOTHING 会让第二次写入静默无效，那行会**永久**停在占位值上。
-- 这也正是 mirror hook 的首道门 `if !entry.Success && !isTerminalFailure(entry)`
-- 存在的理由（hook.go:68-72 的注释自陈：镜像占位行会把 success=false
-- 永久写死并吞掉后续富化）。
--
-- 因此本列的语义是**反向的**：
--
--   is_abandoned = TRUE  ⇒ **这行是终态行**，但它对应的请求**在 v1 侧
--                             永远没有等到终态**。
--   is_abandoned = NULL/FALSE ⇒ 正常行（绝大多数）。
--
-- ★ 关键点：**不是**在 t0 插占位行。
-- 落点由**终态时的一次显式 UPDATE**产生：这个请求在 v1 侧走了完整的
-- t0→t1（INSERT + UPDATE），mirror hook 照常镜像了终态行；只是它在
-- v1 侧本来就没有终态（进程死 / 客户端断连 / 某条路径不发终态事件），
-- 于是这次**是它这辈子第一次也是最后一次发事件**。
--
-- 判据就在那个事件本身：`t1 终态事件到达，但 v1 侧从未见过 t0`。
--   · 见过 t0 再见 t1 ⇒ 正常完成，is_abandoned 保持 NULL
--   · 直接见 t1 没见 t0 ⇒ 遗弃，置 is_abandoned = TRUE
--   · 只见过 t0 再无事件 ⇒ 本列**永远不会出现这一行**（镜像 hook 不镜像
--     非终态，这是设计，见 hook.go:71）
--
-- ⚠ **所以本列覆盖不到「只有 t0、之后彻底静默」那一类**——而那恰恰是
-- §9.66 实测的 19 条的主要形态。这是一个**已知的、有界的能力缺口**，
-- 不是本迁移的疏漏：要在 turns 上覆盖它，只能恢复「插占位行」，
-- 而那会被 DO NOTHING 写死 success=false（回到 819 想避开的那个坑）。
-- 要覆盖它必须让 turns 支持 UPDATE，那是另一个量级的改动。
-- 本列的定位因此是：**覆盖「有终态事件但 v1 侧无 t0」这一子集**，
-- 价值在于它是**零新增写放大**的（不加任何 INSERT，只在罕见的 t1 分支
-- 多一次 UPDATE）。
--
-- 252 生产实测的量化背景（§9.66）：遗弃率 0.047%（19/40,585，约每天 6–7 条），
-- 其中 19/19 带 prompt_tokens、合计 414,168，而 cost_usd 全空。
-- ⇒ 丢掉的是已经真实发生的上游消耗。
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- 列定义
--
-- 刻意**不加 DEFAULT**：读侧要能区分「本行没参与过遗弃判定」与
-- 「本行被判定过且不是遗弃」。加上 DEFAULT FALSE 之后这两者同形，
-- 而 NULL 正是「没判定过」的天然表示（PG 的三值逻辑）。
--
-- 母表 + hot 两张都要加：本项目反复踩过的那个坑（§9.86 的纪律
-- 「读面必须 hot ∪ 母表」）在这里变成写面——只加一张会让另一张上的
-- 同名列**根本不存在**，而这不是假红，是那个表上的 UPDATE 直接报 42703。
--
-- ★ 真库实测（llm-gateway-pg / llm_gateway，2026-10-03）记录了一个
-- **读代码看不出来**的事实：`public.session_turns` 是**分区父表**
-- （pg_class.relkind='p'），底下有 5 个月分区 + 一个 default 分区：
--     session_turns_2026_07 / _08 / _09 / _10 / _11 / _default
-- ⇒ 对父表 ADD COLUMN 会**自动传播到所有现存分区**（实测 8 个 relation
-- 全部带上了该列），所以本迁移**不需要**逐个分区 ALTER。
-- ⇒ 但 `CREATE INDEX` 在分区父表上**不会**自动为每个分区建索引，除非
--     带上分区语法；本文件显式建了 hot 与 parent 两张表各自的索引。
--     实测 `idx_session_turns_abandoned_parent` 的定义里带 `ON ONLY`，
--     即它只挂在父表上，**分区上的同名索引不会被自动创建**。
--
-- ⚠ 由此产生一条**给未来月份**的纪律：新分区（由 partition_manager 建）
--    会不会带上这一列，取决于建分区时用的是**模板分区**还是裸
--    `CREATE TABLE ... PARTITION OF`——前者继承，后者不会。
--    **本轮未验证这一点**（需要等到真的建出新分区才能测），
--    记在 §9.92 的遗留里，不假装覆盖。
-- ---------------------------------------------------------------------------
ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS is_abandoned BOOLEAN;

ALTER TABLE public.session_turns_hot
    ADD COLUMN IF NOT EXISTS is_abandoned BOOLEAN;

-- ---------------------------------------------------------------------------
-- 部分索引：**只索引 TRUE 那一侧**。
--
-- 不用普通索引的原因：is_abandoned 为 NULL/FALSE 的行是 100% 的流量，
-- 给它们建索引只会让写入变慢、让索引比表还大。
-- 读侧的问题形态是「找出所有遗弃行」，那正好只需要 TRUE 那一侧。
--
-- 带上 ts DESC：运营侧的第一个切面是「最近的遗弃」，而 abandoned 集合
-- 的稳态规模是每天个位数（§9.66），索引会长期极小。
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_session_turns_abandoned
    ON public.session_turns_hot (tenant_id, ts DESC)
    WHERE is_abandoned IS TRUE;

CREATE INDEX IF NOT EXISTS idx_session_turns_abandoned_parent
    ON public.session_turns (tenant_id, ts DESC)
    WHERE is_abandoned IS TRUE;

-- ---------------------------------------------------------------------------
-- 迁移自身的守卫（**必须排在任何 COMMENT 之前**——理由见 819 里记录的
-- 「守卫排在了会先炸的那句话后面」那次教训：CREATE/ADD 之后的 COMMENT
-- 若引用了不存在的列，psql 会在 COMMENT 处 ERROR 退出，守卫**根本没跑到**）
-- ---------------------------------------------------------------------------
DO $$
DECLARE
  tbl  text;
  have text;
  missing text;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['public.session_turns', 'public.session_turns_hot'] LOOP
    SELECT data_type INTO have
      FROM information_schema.columns
     WHERE table_schema = split_part(tbl, '.', 1)
       AND table_name   = split_part(tbl, '.', 2)
       AND column_name  = 'is_abandoned';

    IF have IS DISTINCT FROM 'boolean' THEN
      RAISE EXCEPTION
        'migration 820: % 缺列 is_abandoned（或类型不是 boolean，实测为 %）',
        tbl, coalesce(have, '<不存在>');
    END IF;
  END LOOP;

  RAISE NOTICE 'migration 820: is_abandoned ready on both session_turns faces';
END $$;

COMMENT ON COLUMN public.session_turns_hot.is_abandoned IS
  'TRUE ⇔ 这是一行**终态** turn，但对应的请求在 v1 侧从未出现过 t0（开始了却没有'
  '常规意义上的开始记录）——即「开始了却从没有终态」在会话族内的落点'
  '（审计 §9.92，取代迁移 819 的独立表）。NULL = 本行没参与过遗弃判定（100% 的流量）。'
  '写入只发生在终态事件的分支上，且**刻意不插 t0 占位行**：session_turns 按 '
  '(tenant_id, request_id, partition_date) 幂等且首次插入后不可更新'
  '（turn_writer.go 的 ON CONFLICT DO NOTHING），插占位行会把 success=false '
  '永久写死。⚠ 已知能力缺口：只见过 t0、之后彻底静默的请求**不在本列**（见迁移头注释）。';

COMMENT ON COLUMN public.session_turns.is_abandoned IS
  '母表版本，与 session_turns_hot.is_abandoned 语义相同。两张表都必须有这一列——'
  '只加一张会让另一张上的同名列不存在，UPDATE 报 42703（审计 §9.92）。';

COMMIT;
