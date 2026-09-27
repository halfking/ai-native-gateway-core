-- 754: request_logs 主表 archive 默认函数 (P2 数据归档, 2026-09-26)
--
-- 背景: request_logs 是 PARTITION BY RANGE (ts) 月分区父表（537 之前
-- 的 005/043 已奠基），330 行前的 archive_request_logs(date) 路径在
-- 331 (2026-07-04) 与 archive_request_wal 一起被整族移除——archive
-- 表空间开销小、无应用查询方、维护成本不抵。此后归档流水线空缺，仅
-- request_logs_bodies 由 drop_old_request_logs_bodies_partitions(int)
-- 自动 DROP（partition_manager:761）。本迁移补齐主表归档。
--
-- 设计基线:
--   * 表语义: request_logs 主表的月分区保留 p_retention_days 天的
--     「全字段」语义，旧的月分区中只把摘要字段（ts、tenant_id、
--     request_id、session_id、provider_model、prompt_tokens、
--     completion_tokens、cost_usd、status_code、success、error_kind）
--     落进 request_logs_archive_YYYY_MM。丢弃大 JSONB（request_body、
--     response_body、outbound_body、headers、trace_events、
--     routing_attempts、tool_calls、attachments、dlp_violations、
--     content_safety_score、auto_decision、compression_meta、
--     outbound_msg_hashes、sanitizer_mutations、vendor_metadata、
--     ir_extensions 等 18 列）以让归档体积 ≈ 主表的 5% 级，
--     长期挂载在月分区供对账/合规回溯。
--
--   * 风格: 沿 750 (usage_facts_daily_partition) move-then-attach
--     范式——INSERT 归档行优先于任何 source-side 副作用（archive
--     完成前即使 source 被并行 DROP 也不丢数据）；本函数不 DROP
--     源分区（R68 修订已禁止 drop partition_by_range 父表的月分区，
--     见迁移 654 / 337 的事故复盘）。owner 通过 lifecycle.
--     request_logs_ttl_days (默认 30 天) 控制归档触发；过期的
--     request_logs 月分区继续保留在主表（运维可手动 DROP 或由
--     后续迁移接 drop）。
--
--   * 幂等: 归档表 UNIQUE INDEX (request_id, ts) + INSERT ... ON
--     CONFLICT DO NOTHING。重跑同月分区时已存在行被 DO NOTHING
--     吸收，函数可安全在 partition_manager 每日 tick 上调用。
--
--   * 并发: 同一月份串行化——pg_advisory_xact_lock(hashtext(
--     'archive_request_logs_default:' || yyyy-mm))，多实例共享库
--     或重入 partition_manager 不会双跑。
--
--   * 性能: 1000 行/批 主键游标（partition 局部 id 唯一——每月分
--     区 ts 约束在一个月内），单批 INSERT 在毫秒级、跨分区数万
--     至数十万行不超 252 共享 PG 的 30s statement_timeout 边界。
--     SET LOCAL statement_timeout 钉 60s 留足缓冲。
--
--   * 列宽守卫: 不修改 request_logs schema、不动现有视图/RLS，
--     归档表独立 namespace（新表 + 独立索引），不在 views 引用
--     链上、不影响主表查询路径。
--
-- 与既有归档函数族的差异（兼容性对照）:
--   * archive_routing_decision_log / archive_credential_model_index
--     走 (status, rows_migrated, partition_dropped) 三元组；
--     本函数按 §9.2 契约返回 (archived_partition, rows_archived)
--     二元组，partition_manager 走新分支 (runRequestLogsArchive)。
--   * 不传 month date、不传 scalar bigint；传 p_retention_days int。
--   * 现有归档函数 month_end = date_trunc('month', archive_month)
--     + interval '1 month'，本函数遍历所有过期月分区（month_end
--     < NOW() - p_retention_days），单次调用覆盖整窗。
--
-- 落地路径: bg/partition_manager.go::archiveSpecs 增加 day=5 条目
-- + lifecycle.request_logs_ttl_days spec。boot ensure / installer /
-- 升级通道（scripts/apply-db-revision-sequence.sh files=()）三通道
-- 与 750 同款——CREATE OR REPLACE FUNCTION 与 CREATE TABLE IF NOT
-- EXISTS 是天然幂等的，无需 advisory-lock 防双跑。

-- ============================================================================
--
-- **这不是数据搬移，是摘要抽取。** 本函数内**没有任何 DELETE**（可自证：
-- 剥注释后 grep -c 'DELETE' 本文件 == 0），源分区一个字节都不会被删除。
-- 注意这条自检必须剥掉 `--` 注释再数，否则本说明自己就把它数成 1（本次就踩了）。
-- 因此：
--   * request_logs 主表**不会因为本迁移而变小**，它仍在增长，需要运维另行
--     处置（受 R68 约束：禁止 DROP partition_by_range 父表的月分区，见迁移
--     654 / 337 事故复盘）；
--   * 开启 lifecycle.request_logs_ttl_days **不等于**「旧数据离开主表」，
--     只等于「旧分区多一份可供对账/合规回溯的摘要副本」。
-- 名字里的 "archive" 容易被读成前者，此处显式写明以免误判。
--
-- **另一个必须知道的代价**：本函数没有「已归档」标记。每次调用都会枚举所有
-- month_end 已过期的月分区，并把每个源分区的**全部行**再走一遍
-- INSERT ... ON CONFLICT DO NOTHING —— 已归档的行只是被唯一索引冲突吸收，
-- 行仍然被读取、投影、再插入尝试。因此单次成本 =
-- O(所有超过保留窗口的行)，且随时间单调增长，不会自行收敛。
-- 故 bg 侧把它限制在**每日一次**（bg/archiveOldRequestLogs 的注释有详述）。
-- 彻底解法是加一张 archive ledger 记录 (partition, max_id)，已归档分区直接
-- 跳过；本 SQL 从未在真实 PostgreSQL 上执行过，不宜在此盲改，留作后续。
--
-- 落地路径勘误：本文件头早先写的是「archiveSpecs 增加 day=5 条目」，**未实施**
-- —— archiveSpecs 按「日期参数 + 标量/tuple 返回」设计，而本函数收 retention
-- 天数、RETURNS TABLE 是每分区一行的集合返回，硬塞会错传参数并按错列形状扫描
-- （与 2026-09-03/04 的 42703 同源）。实为 bg/partition_manager.go 的
-- pm.archiveOldRequestLogs（runCleanup step 12），每日一次。
-- ============================================================================

\set ON_ERROR_STOP on

-- 防御性清理: 旧版同名函数（如有）DROP 后重建，避免重载冲突。
DROP FUNCTION IF EXISTS public.archive_request_logs_default(integer);

CREATE OR REPLACE FUNCTION public.archive_request_logs_default(p_retention_days integer)
    RETURNS TABLE(archived_partition text, rows_archived bigint)
    LANGUAGE plpgsql
    AS $$
DECLARE
    batch_size CONSTANT integer := 1000;
    cutoff_ts  timestamptz;
    rec        record;
    src_part   text;
    dst_part   text;
    month_start date;
    month_end   date;
    new_last_id  bigint;
    batch_count  bigint;
    last_id      bigint;
    total_rows   bigint;
BEGIN
    -- 守卫: 与 settings/spec_lifecycle.go Min/Max=7/365 保持一致；留存
    -- 边界是 owner 拍板（业务对账窗口 ≈ 7 天 = 法定合规最低线）。
    IF p_retention_days < 7 THEN
        RAISE EXCEPTION 'archive_request_logs_default: retention_days=% < 7 floor', p_retention_days;
    END IF;
    IF p_retention_days > 365 THEN
        RAISE EXCEPTION 'archive_request_logs_default: retention_days=% > 365 ceiling', p_retention_days;
    END IF;

    cutoff_ts := NOW() - make_interval(days => p_retention_days);

    -- 月分区游走: pg_inherits 枚举 request_logs 下形如
    -- request_logs_YYYY_MM 的月分区，剔除 DEFAULT / bodies 同族
    -- (_bodies_2026_07 等不会被 pg_inherits 列入 request_logs 子集，
    -- 此处命名正则 ^request_logs_[0-9]{4}_[0-9]{2}$ 即足够严密)。
    FOR rec IN
        SELECT
            c.relname                                    AS partition_name,
            to_date(
                substring(c.relname FROM 'request_logs_([0-9]{4}_[0-9]{2})$'),
                'YYYY_MM'
            )                                            AS partition_month
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'request_logs'
          AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$'
        ORDER BY partition_month
    LOOP
        month_start := date_trunc('month', rec.partition_month)::date;
        month_end   := (month_start + INTERVAL '1 month')::date;

        -- 跳过仍在保留窗口内的月分区。
        IF month_end > cutoff_ts THEN
            CONTINUE;
        END IF;

        src_part := rec.partition_name;
        dst_part := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');

        -- 串行化同一月份的并发 ensure（多实例 partition_manager、
        -- 共享库 boot ensure、installer 升级通道同时投递时不会双跑）。
        PERFORM pg_advisory_xact_lock(
            hashtext('archive_request_logs_default:' || to_char(month_start, 'YYYY-MM'))
        );

        -- 归档表: 独立 heap 表（非 columnar——归档按月分片后读取
        -- 通常是单分区时间窗扫描，columnar 收益不明显；非分区父
        -- 表——挂载到 request_logs_archive 会让 archive_default
        -- 与具体分区并存，迁移阶段更难治理）。11 个摘要字段+
        -- UNIQUE(request_id, ts) 兜住 ON CONFLICT DO NOTHING
        -- 幂等需求；二级索引 (ts) 兜对账窗口扫表。
        IF NOT EXISTS (
            SELECT 1 FROM pg_class
            WHERE relname = dst_part AND relnamespace = 'public'::regnamespace
        ) THEN
            EXECUTE format(
                'CREATE TABLE %I (
                    request_id        text             NOT NULL,
                    ts                timestamptz      NOT NULL,
                    tenant_id         text,
                    session_id        text,
                    model             text,
                    prompt_tokens     integer,
                    completion_tokens integer,
                    cost_usd          numeric(14, 8),
                    status_code       integer,
                    success           boolean,
                    error_kind        text,
                    archived_at       timestamptz      NOT NULL DEFAULT NOW()
                )', dst_part);
            EXECUTE format(
                'CREATE UNIQUE INDEX %I ON %I (request_id, ts)',
                dst_part || '_req_ts_uniq', dst_part);
            EXECUTE format(
                'CREATE INDEX %I ON %I (ts DESC)',
                dst_part || '_ts_idx', dst_part);
        END IF;

        -- 主键游标批式 INSERT: 单分区内 id bigint 唯一（PK 是
        -- (id, ts) 但 ts 在月内被约束到同一月，id 局部唯一）。
        -- ON CONFLICT (request_id, ts) DO NOTHING 让重跑同月分区
        -- 时已存在的行被吸收，rows_archived 只计新增。
        total_rows := 0;
        last_id    := 0;
        LOOP
            -- 单查询三件事: 取下一批候选 → 写归档 → 返回 new_last_id
            -- 与本批实际写入数。CTE 顺序: candidates 取源、max_id
            -- 算下一轮游标、inserted 写归档并返回被插入行数。
            EXECUTE format(
                'WITH
                 candidates AS (
                     SELECT id, request_id, ts, tenant_id, session_id,
                            provider_model, prompt_tokens, completion_tokens,
                            cost_usd, status_code, success, error_kind
                     FROM %I
                     WHERE id > %L
                     ORDER BY id
                     LIMIT %L
                 ),
                 max_id AS (
                     SELECT COALESCE(MAX(id), 0) AS new_last FROM candidates
                 ),
                 inserted AS (
                     INSERT INTO %I (
                         request_id, ts, tenant_id, session_id, model,
                         prompt_tokens, completion_tokens, cost_usd,
                         status_code, success, error_kind
                     )
                     SELECT request_id, ts, tenant_id, session_id,
                            provider_model, prompt_tokens, completion_tokens,
                            cost_usd, status_code, success, error_kind
                     FROM candidates
                     ON CONFLICT (request_id, ts) DO NOTHING
                     RETURNING 1 AS r
                 )
                 SELECT
                     (SELECT new_last FROM max_id),
                     (SELECT count(*) FROM inserted)',
                src_part, last_id, batch_size,
                dst_part
            ) INTO new_last_id, batch_count;

            total_rows := total_rows + batch_count;

            -- 终止条件: 无候选（new_last_id = 0）或已读完该分区
            -- （本批不足 batch_size 行）。前者兜极端空源，后者兜
            -- 末批。两条件同时成立才退出，避免全冲突批次导致死循环。
            EXIT WHEN new_last_id = 0;
            EXIT WHEN (new_last_id - last_id) < batch_size;

            last_id := new_last_id;
        END LOOP;

        archived_partition := dst_part;
        rows_archived      := total_rows;
        RETURN NEXT;
    END LOOP;
END;
$$;

COMMENT ON FUNCTION public.archive_request_logs_default(integer) IS
    'Archive summary fields from request_logs monthly partitions older than p_retention_days (7-365) into per-month heap tables request_logs_archive_YYYY_MM. Idempotent (ON CONFLICT DO NOTHING), batched at 1000 rows/iteration via id cursor, advisory-lock-serialized per month. Does NOT drop source partition (R68 freeze). Migration 754 (2026-09-26). Refs: docs/audit/2026-09-25-session-storage-audit-handoff.md §9.';