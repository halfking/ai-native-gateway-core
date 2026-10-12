-- 841_monthly_partition_retention.sql
-- 2026-10-07: 给**月度分区族**补上分区级 DROP 保留（机制先建、策略留空）。
--
-- 背景（runbook §10.106.13、§10.106.15）：
--   · 生产库 15 GB，`2026_09` 一个月份份就占 **4,767 MB（全库 32%）**，
--     横跨 13 个数据族；其中会话原始数据约 3,423 MB。
--   · 死元组比例极低（全场最高仅 6.9% ≈ 42 MB）⇒ **靠 VACUUM 回收存储这条路不存在**，
--     唯一有效杠杆是 **DROP 分区**。
--   · ⚠️ 代码里**没有**「DROP 老月度分区」的逻辑：
--     `bg/partition_manager.go` 里与保留期相关的常量只有
--     `DefaultRetentionWindow = 8 * time.Hour`（那是 830 快照族的窗口，8 小时），
--     全文没有任何 DROP 老月度分区的路径。
--     ⇒ 月度族**只会「被清空」、不会「被丢弃」**，
--       这正是 `session_*_2026_07` / `_2026_08` 变成 0 字节空壳却仍留在目录里的机制。
--
-- 本迁移只做三件事，**默认不删任何东西**：
--   A. 建一张配置表 `llm_gateway_partition_retention`，**建表即为空**。
--      ⇒ 迁移应用后行为与今天完全一致，**没有任何数据被删**；
--        真正启用只需按族 INSERT 保留月数（业务决定，不在本迁移内）。
--   B. 建 `llm_gateway_expired_month_partitions()`：只读地列出「按当前配置已过期」
--      的月度分区，供人工核查与门禁断言。
--   C. 建 `llm_gateway_drop_expired_month_partitions()`：真正执行 DROP，
--      逐条写审计表 `llm_gateway_partition_drop_log`。
--
-- ★ 为什么是 DROP 而不是 DELETE：
--   实测月度分区死元组几乎为 0，说明 DELETE 之后空间也回不来（要 VACUUM 才能回收，
--   而这类大 TOAST 表的回收代价高）。DROP 是 O(1) 的元数据操作、不搬数据。
--
-- ★ 安全约束（写在这里是为了让门禁能逐条钉住）：
--   1. 配置表为空 ⇒ 一个分区都不会被列为过期；
--   2. 只看**直接子分区**（pg_inherits 一层），且父表名必须等于配置里的族名；
--   3. 分区名必须匹配 `_<YYYY_MM>$`，否则不认（防止误删同名普通表）；
--   4. `retain_months` 语义 = **保留 N 个月（含当月）**：
--      仅当 `分区月 < 当月月初 - (N-1) 个月` 才算过期；
--   5. ★ 当月与**未来月**（例如预建的 `_2026_11`）**永不过期**——
--      由第 4 条的 `<` 严格不等号保证，这里再显式挡一次，
--      防止未来有人把 `<` 改成 `<=`；
--   6. DROP 用 `format('DROP TABLE ... %I')` 引用，标识符绝不拼字符串；
--   7. 每次 DROP 写一行审计日志（族、父表、分区、行数、时刻）。
--
-- 回滚判据：.down.sql 删函数与两张表，**不动任何已存在的分区**。
--   ⚠️ 被 841 DROP 掉的分区不可逆；回滚只是「不再继续删」，
--   已删的分区需要从备份恢复。

BEGIN;

-- ── A1. 保留期配置表（默认空 = 不删任何东西）────────────────────────
CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_retention (
    family         text        PRIMARY KEY,
    retain_months  integer     NOT NULL,
    enabled        boolean     NOT NULL DEFAULT true,
    note           text,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT llm_gateway_partition_retention_family_nonempty
        CHECK (family <> ''),
    CONSTRAINT llm_gateway_partition_retention_months_positive
        CHECK (retain_months >= 1)
);

COMMENT ON TABLE public.llm_gateway_partition_retention IS
    '月度分区族的 DROP 保留策略：每族一行，retain_months = 保留几个月（含当月）。'
    '到期由 llm_gateway_drop_expired_month_partitions() **DROP 整个分区**（不是 DELETE）。'
    '★ 本表默认为空 ⇒ 一个分区都不会被删；启用需按族逐条 INSERT（业务决定）。'
    '★ 语料：生产 2026_09 占 4,767 MB / 全库 32%，其中会话原始数据约 3,423 MB。';

-- ── B. 只读地列出已过期的月度分区 ────────────────────────────────────
CREATE OR REPLACE FUNCTION public.llm_gateway_expired_month_partitions()
RETURNS TABLE (
    family          text,
    parent_name     text,
    partition_name  text,
    partition_month date,
    live_rows       bigint
)
    LANGUAGE plpgsql
    STABLE
    AS $_$
DECLARE
    cfg           record;
    child          record;
    cur_month      date;
    cutoff         date;
    mth            date;
    l_family       text;
    l_parent       text;
    l_part         text;
    l_month        date;
    l_rows         bigint;
BEGIN
    cur_month := date_trunc('month', now())::date;

    FOR cfg IN
        SELECT r.family, r.retain_months
          FROM public.llm_gateway_partition_retention r
         WHERE r.enabled
    LOOP
        -- 保留 N 个月（含当月）⇒ 过期线是「当月月初往前推 N-1 个月」
        cutoff := cur_month - ((cfg.retain_months - 1) || ' months')::interval;

        FOR child IN
            SELECT c.oid, c.relname
              FROM pg_class c
              JOIN pg_inherits i ON i.inhrelid = c.oid
              JOIN pg_class parent ON parent.oid = i.inhparent
              JOIN pg_namespace n  ON n.oid = c.relnamespace
             WHERE n.nspname = 'public'
               AND parent.relname = cfg.family
               AND c.relkind = 'r'
               -- 安全约束 3：只认 `_<YYYY_MM>$` 形态的分区名
               AND c.relname ~ '_\d{4}_\d{2}$'
        LOOP
            -- ★ 必须用 substring 抓 `YYYY_MM`，不能用 right(relname, 6)：
            --   `2026_07` 是 **7** 个字符，取 6 个得到 `026_07`，
            --   to_date 之后是一个无意义的日期 ⇒ `mth < cutoff` 对**所有**分区成立
            --   ⇒ 当月与预建的未来分区也会被 DROP。
            --   （这个 bug 是真库行为门抓到的，契约门只查 `mth < cutoff` 是否存在，完全放行。）
            mth := to_date(substring(child.relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM');
            IF mth IS NULL THEN
                CONTINUE;
            END IF;

            -- 安全约束 4 + 5：严格小于 ⇒ 当月与未来月（预建分区）永不被列为过期
            IF mth < cutoff THEN
                EXECUTE format('SELECT count(*) FROM public.%I', child.relname)
                   INTO l_rows;
                l_family  := cfg.family;
                l_parent  := cfg.family;
                l_part    := child.relname;
                l_month   := mth;
                family          := l_family;
                parent_name     := l_parent;
                partition_name  := l_part;
                partition_month := l_month;
                live_rows       := l_rows;
                RETURN NEXT;
            END IF;
        END LOOP;
    END LOOP;
END;
$_$;

COMMENT ON FUNCTION public.llm_gateway_expired_month_partitions() IS
    '只读：按当前配置列出已过期的月度分区。配置表为空时返回 0 行。';

-- ── C1. DROP 审计表 ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_drop_log (
    dropped_at      timestamptz NOT NULL DEFAULT now(),
    family          text        NOT NULL,
    parent_name     text        NOT NULL,
    partition_name  text        NOT NULL,
    partition_month date,
    live_rows       bigint
);

COMMENT ON TABLE public.llm_gateway_partition_drop_log IS
    '每次 DROP 月度分区写一行。★ DROP 不可逆，本表是事后唯一的线索。';

-- ── C2. 真正执行 DROP ─────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION public.llm_gateway_drop_expired_month_partitions()
RETURNS text[]
    LANGUAGE plpgsql
    VOLATILE
    AS $_$
DECLARE
    rec     record;
    dropped text[] := ARRAY[]::text[];
BEGIN
    FOR rec IN
        SELECT e.family, e.parent_name, e.partition_name, e.partition_month, e.live_rows
          FROM public.llm_gateway_expired_month_partitions() e
    LOOP
        -- 安全约束 6：标识符一律走 format %I，绝不拼字符串
        EXECUTE format('DROP TABLE IF EXISTS public.%I', rec.partition_name);

        INSERT INTO public.llm_gateway_partition_drop_log
               (family, parent_name, partition_name, partition_month, live_rows)
        VALUES (rec.family, rec.parent_name, rec.partition_name,
                rec.partition_month, rec.live_rows);

        dropped := array_append(dropped, rec.partition_name);
    END LOOP;
    RETURN dropped;
END;
$_$;

COMMENT ON FUNCTION public.llm_gateway_drop_expired_month_partitions() IS
    '按 llm_gateway_partition_retention 的配置 DROP 已过期的月度分区，'
    '返回被 DROP 的分区名数组，并逐条写 llm_gateway_partition_drop_log。'
    '配置表为空时返回空数组、不删任何东西。★ DROP 不可逆。';

COMMIT;
