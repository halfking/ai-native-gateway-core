-- 765: bodies 存储列存化 + lz4 TOAST + request_stage_events 死索引清理
-- （R16 存储轮，2026-09-30；取证 docs/audit/2026-09-30-bodies-columnar-storage-opt.md）
--
-- 背景（252 真库实测）：
--   request_logs_bodies 月分区为 heap+TOAST(pglz)，2026_09 已 53GB/1.39M 行
--   （均 370KB/行），252 根盘 93%。252 真库 500 行样本基准：
--     raw 79.8MB → heap+pglz 38.1MB（2.1×）→ citus columnar(zstd-3) 1.41MB（56.6×）。
--   生命周期兼容（scratch 全链 PASS）：promote 为裸 INSERT..SELECT（无 ON
--   CONFLICT）可路由进 columnar 分区；父表 WHERE request_id 读 PASS；TTL
--   drop_old_request_logs_bodies_partitions 的 DROP TABLE PASS。drawer 冷读
--   （request_logs_bodies_with_current_month WHERE request_id）在 2026_09 分区
--   本就 0 索引全扫，columnar 压缩扫描严格不劣于现状。
--   session_bodies 的 promote（615/626）使用 ON CONFLICT DO NOTHING——columnar
--   无唯一索引不可列存，改 lz4 TOAST（真库 200 行样本：pglz 35.95MB vs lz4
--   35.37MB，尺寸中性、压缩 CPU 显著降低；body 写为会话热路径）。
--   本 PG 构建 TOAST zstd 运行时不可用（SET COMPRESSION zstd → invalid
--   compression method；--with-lz4 在位），heap 侧压缩牌=lz4；columnar 的
--   zstd 为 citus 自带链接，不受影响。
--
-- A. ensure_request_logs_bodies_partition 重定义：citus_columnar 在位时以
--    USING columnar 建月分区，否则回退 heap（全新环境无扩展亦可安装）。
-- B. 存量空 heap 月分区一次性转 columnar：仅转空分区（数据安全阀），先取
--    父表 ACCESS EXCLUSIVE 再复核空、防 promote 竞态；非空分区（如 2026_09
--    的 53GB）不动，按 TTL 整分区 DROP 退役（10-08）。
-- C. bodies 两族 lz4 TOAST：列压缩只影响新 toast 行，零重写；父表 ALTER 会被
--    后续 PARTITION OF 继承（真库已验证），既有空 2026_10 子分区逐个补齐。
-- D. request_stage_events 三死索引：pss 自 2026-09-11 累计 idx_scan=0
--    （556MB + 352MB + 30MB = 938MB），非测试代码零读面（全仓 grep 仅
--    retention 走 pkey/created_at）。普通 DROP INDEX：索引 unlink 亚秒级，
--    ACCESS EXCLUSIVE 窗口可控（764 先例：随部署序列执行，无 CONCURRENTLY
--    即不触发 installer 单事务豁免清单）。
--
-- 幂等：CREATE OR REPLACE / IF EXISTS / DO 守卫；台账自登记（695-705 定式）。

-- ── A. ensure 函数（columnar 分支 + heap 回退）────────────────────────────
CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
    use_columnar   boolean;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'citus_columnar')
            INTO use_columnar;
        IF use_columnar THEN
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs_bodies
                 FOR VALUES FROM (%L) TO (%L) USING columnar',
                partition_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as columnar', partition_name;
        ELSE
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs_bodies
                 FOR VALUES FROM (%L) TO (%L)',
                partition_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
        END IF;
    END IF;
END;
$$;

-- ── B. 存量空 heap 月分区 → columnar ─────────────────────────────────────
DO $$
DECLARE
    part        record;
    is_empty    boolean;
    month_start date;
    month_end   date;
    has_col     boolean;
BEGIN
    SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'citus_columnar') INTO has_col;
    IF NOT has_col THEN
        RAISE NOTICE '765: citus_columnar absent, skip partition conversion';
        RETURN;
    END IF;
    FOR part IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_am am ON am.oid = c.relam
        WHERE i.inhparent = 'request_logs_bodies'::regclass
          AND am.amname = 'heap'
          AND c.relname ~ '^request_logs_bodies_[0-9]{4}_[0-9]{2}$'
    LOOP
        EXECUTE format('SELECT NOT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', part.relname)
            INTO is_empty;
        IF NOT is_empty THEN
            RAISE NOTICE '765: % not empty, keep heap (retires via TTL partition drop)', part.relname;
            CONTINUE;
        END IF;
        month_start := to_date(substring(part.relname FROM '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM');
        month_end   := (month_start + interval '1 month')::date;
        LOCK TABLE public.request_logs_bodies IN ACCESS EXCLUSIVE MODE;
        -- 持锁后复核空：与检查窗口内落进的行互斥
        EXECUTE format('SELECT NOT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', part.relname)
            INTO is_empty;
        IF NOT is_empty THEN
            RAISE NOTICE '765: % received rows during window, keep heap', part.relname;
            CONTINUE;
        END IF;
        EXECUTE format('DROP TABLE public.%I', part.relname);
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.request_logs_bodies
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            part.relname, month_start, month_end);
        RAISE NOTICE '765: converted % to columnar', part.relname;
    END LOOP;
END;
$$;

-- ── C. lz4 TOAST（request_logs_bodies 家族 + session_bodies 家族）─────────
ALTER TABLE public.request_logs_bodies ALTER COLUMN request_body SET COMPRESSION lz4;
ALTER TABLE public.request_logs_bodies ALTER COLUMN outbound_body SET COMPRESSION lz4;
ALTER TABLE public.request_logs_bodies ALTER COLUMN response_body SET COMPRESSION lz4;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN request_body SET COMPRESSION lz4;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN outbound_body SET COMPRESSION lz4;
ALTER TABLE public.request_logs_bodies_hot ALTER COLUMN response_body SET COMPRESSION lz4;
ALTER TABLE public.session_bodies ALTER COLUMN request_delta SET COMPRESSION lz4;
ALTER TABLE public.session_bodies ALTER COLUMN response_delta SET COMPRESSION lz4;
ALTER TABLE public.session_bodies ALTER COLUMN outbound_body SET COMPRESSION lz4;
ALTER TABLE public.session_bodies ALTER COLUMN request_attachments SET COMPRESSION lz4;
ALTER TABLE public.session_bodies ALTER COLUMN response_attachments SET COMPRESSION lz4;
ALTER TABLE public.session_bodies_hot ALTER COLUMN request_delta SET COMPRESSION lz4;
ALTER TABLE public.session_bodies_hot ALTER COLUMN response_delta SET COMPRESSION lz4;
ALTER TABLE public.session_bodies_hot ALTER COLUMN outbound_body SET COMPRESSION lz4;
ALTER TABLE public.session_bodies_hot ALTER COLUMN request_attachments SET COMPRESSION lz4;
ALTER TABLE public.session_bodies_hot ALTER COLUMN response_attachments SET COMPRESSION lz4;

-- 既有空 2026_10 heap 子分区：父表 ALTER 不回灌既有子分区，逐个补齐。
-- 守卫：分区缺失（已被 B 段转 columnar 或未预建）则跳过，非致命。
DO $$
DECLARE
    tgt text;
BEGIN
    FOREACH tgt IN ARRAY ARRAY[
        'session_bodies_2026_10'
    ] LOOP
        IF to_regclass('public.' || tgt) IS NOT NULL THEN
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN request_delta SET COMPRESSION lz4', tgt);
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN response_delta SET COMPRESSION lz4', tgt);
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN outbound_body SET COMPRESSION lz4', tgt);
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN request_attachments SET COMPRESSION lz4', tgt);
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN response_attachments SET COMPRESSION lz4', tgt);
        ELSE
            RAISE NOTICE '765: % absent, skip lz4 alignment', tgt;
        END IF;
    END LOOP;
END;
$$;

-- ── D. request_stage_events 三死索引（pss 19 天 0 扫描 / 非测试代码零读面）──
DROP INDEX IF EXISTS public.idx_stage_events_request_id;
DROP INDEX IF EXISTS public.idx_stage_events_tenant_ts;
DROP INDEX IF EXISTS public.idx_stage_events_stage_status;

INSERT INTO public.schema_migrations (version, description)
VALUES ('765', 'bodies columnar storage (ensure fn columnar branch + empty-partition conversion), lz4 toast on bodies families, drop 3 zero-scan request_stage_events indexes')
ON CONFLICT (version) DO NOTHING;
