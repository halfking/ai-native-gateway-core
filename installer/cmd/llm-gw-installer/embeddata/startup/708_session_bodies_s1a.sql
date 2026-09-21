-- Migration 708: 存储优化方案 v2 · S1a —— session_bodies pivot 前置（kind 列）+ sessions 死列清理
--
-- docs/03-design/04-data-design/storage-optimization-plan.md §3 D2/§4 S1a：
--   ① session_bodies/session_bodies_hot 增 kind 列，区分
--        'turn_delta'  —— 历史与过渡期的逐轮 delta 行（既有全部行）
--        'final_full'  —— 每会话"最后一次完整快照"行（turn_no=0，
--                          request_id='final_full:<session_id>'；S1b 起由
--                          writer 按 storage.session_final_full_enabled 灰度
--                          逐轮 upsert，outbound_body 同步停写）
--     DEFAULT 取 'turn_delta' 而非方案草案的 'final_full'：滚动部署期旧
--     二进制继续写不带 kind 的逐轮行——默认 'final_full' 会把它们误标成
--     快照行（2026-09-14 本机实测：旧二进制运行 10 分钟即产生 10 行误标）。
--     默认 'turn_delta' 后旧写链天然正确；新写链对两种 kind 均显式赋值。
--     方案 D2 原文是"会话关闭聚合时拼装写入"；本库无任何自动关闭链路
--     （CloseSession 零调用方，2026-09-14 审计），故实现取逐轮 upsert——
--     同为"最后完整快照"终态，WAL 换取可落地的写点；711 收尾时可再评估。
--   ② 部分唯一索引守护每会话每分区至多一行 final_full（父表+hot）。
--   ③ DROP sessions.last_full_request/last_full_response/last_full_payload_at
--     （migration 456 死列：零读写，仅 cmd/tools/migration-456-test 的列清单
--     引用；与"sessions 不含请求内容"冲突，快照职责移交本表 final_full）。
--     DROP 为不可逆点（方案 §7）：456 起零写入，历史数据随列销毁——本机
--     审计确认三列 100% NULL，无数据损失。
--   ④ promote_session_bodies_hot_to_partition 重写为「集合契约校验 + 目录
--     派生列清单」（同 707 的 turns promote 重写理由：638 的显式列清单会让
--     新列在 promote 时静默丢成默认值——kind 默认 'final_full'，逐轮 delta
--     行被误标为 final_full 是本迁移最危险的数据污染面）。
--     语义承 638：守卫、advisory lock、按 (id, partition_date) 精确删除。
--
-- 回填策略：旧行=全部逐轮 delta。DEFAULT 已是 'turn_delta' 时回填为幂等
-- no-op；仍保留该步，用于兜住"先按草案默认 final_full 建列、后重放本迁移"
-- 的中间态。WHERE turn_no > 0 AND kind='final_full' 判定（final_full 行恒
-- turn_no=0），天然幂等、不碰新行。
--
-- Compatibility: 加列带 NOT NULL DEFAULT（PG11+ 元数据级，无重写），回填
-- 分批；DROP 列为元数据级。down 恢复死列（数据不可恢复，见 down 注释）。

BEGIN;

-- =============================================
-- 1. kind 列（父表 + hot 同形；NOT NULL DEFAULT 走 PG11+ 元数据路径）
-- =============================================

ALTER TABLE public.session_bodies
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'turn_delta';
ALTER TABLE public.session_bodies_hot
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'turn_delta';

COMMENT ON COLUMN public.session_bodies.kind IS
    '行语义：turn_delta=逐轮增量行；final_full=每会话最后完整快照行(turn_no=0)（708）';

-- =============================================
-- 1b. session_bodies_unified 视图追加 kind 列（637 体 + kind）
--     Go 读端（getLatestBodies 回退 / TurnReader.LoadLatestOutbound final_full
--     分支）经 unified 视图按 kind 过滤——视图不带 kind 会是又一次
--     42703 刷屏（693/696 教训）。CREATE OR REPLACE VIEW 只能尾部追加列
--     （42P16），kind 恒为末列（读者按列名取用，位置无关）。637 的
--     同日可见性语义原样保留。
-- =============================================

CREATE OR REPLACE VIEW public.session_bodies_unified AS
SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    request_delta,
    response_delta,
    outbound_body,
    request_attachments,
    response_attachments,
    ts,
    partition_date,
    kind
FROM public.session_bodies_hot
UNION ALL
SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    request_delta,
    response_delta,
    outbound_body,
    request_attachments,
    response_attachments,
    ts,
    partition_date,
    kind
FROM public.session_bodies;

-- CREATE OR REPLACE VIEW 无法改 reloptions，显式重申（625/637 惯例）。
ALTER VIEW public.session_bodies_unified SET (security_invoker = true);

COMMENT ON VIEW public.session_bodies_unified IS
    'Explicit-column union of session_bodies_hot (recent writes) and historical session_bodies partitions. Admin readers MUST use this view rather than public.session_bodies to avoid missing recent writes still in the hot window. Since migration 637 the partition branch is unfiltered; since migration 708 the kind column (turn_delta/final_full) is exposed for snapshot-aware readers.';

-- =============================================
-- 2. 旧行回填 kind='turn_delta'（分批、幂等）
-- =============================================

-- 月分区（含 default 兜底，防游离行）；逐分区一条 UPDATE，幂等
DO $$
DECLARE
    part RECORD;
BEGIN
    FOR part IN
        SELECT c.relname AS partition_name
        FROM pg_class p
        JOIN pg_inherits i ON i.inhparent = p.oid
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE p.oid = 'public.session_bodies'::regclass
    LOOP
        EXECUTE format(
            'UPDATE public.%I SET kind = %L WHERE turn_no > 0 AND kind = %L',
            part.partition_name, 'turn_delta', 'final_full'
        );
    END LOOP;
END
$$;

-- hot 表
UPDATE public.session_bodies_hot
   SET kind = 'turn_delta'
 WHERE turn_no > 0 AND kind = 'final_full';

-- =============================================
-- 3. final_full 部分唯一索引（每会话每分区至多一行）
-- =============================================

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_bodies_final_full
    ON public.session_bodies (tenant_id, session_id, partition_date)
    WHERE kind = 'final_full';

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_bodies_hot_final_full
    ON public.session_bodies_hot (tenant_id, session_id, partition_date)
    WHERE kind = 'final_full';

-- =============================================
-- 4. promote_session_bodies_hot_to_partition 重写
--    （有序契约校验 + SELECT *；语义承 638）
-- =============================================

CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint) AS $$
DECLARE
    cutoff_ts timestamptz;
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
BEGIN
    IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN
        RAISE EXCEPTION 'retention_window must be positive';
    END IF;
    IF batch_size IS NULL OR batch_size < 1 THEN
        RAISE EXCEPTION 'batch_size must be >= 1';
    END IF;

    IF to_regclass('public.session_bodies_hot') IS NULL
       OR to_regclass('public.session_bodies') IS NULL THEN
        RAISE EXCEPTION 'session_bodies hot and parent tables must both exist';
    END IF;

    -- 708 列契约（同 707）：父表与 hot 的（列名:类型:非空）集合全等；
    -- 列映射用目录派生的显式列名清单，对历史库父表/hot 列序漂移不敏感。
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION 'session_bodies hot/parent column contract has drifted (column set mismatch)';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
      INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;

    cutoff_ts := now() - retention_window;

    IF NOT pg_try_advisory_xact_lock(hashtext('public.promote_session_bodies_hot_to_partition')) THEN
        RETURN QUERY SELECT 0::bigint;
        RETURN;
    END IF;

    EXECUTE format(
        'WITH to_move AS (
            SELECT %1$s
            FROM public.session_bodies_hot
            WHERE ts < %3$L::timestamptz
            ORDER BY ts
            LIMIT %2$s
            FOR UPDATE SKIP LOCKED
        ),
        inserted AS (
            INSERT INTO public.session_bodies (%1$s)
            SELECT %1$s FROM to_move
            ON CONFLICT (id, partition_date) DO NOTHING
            RETURNING id, partition_date
        ),
        deleted AS (
            DELETE FROM public.session_bodies_hot h
            USING inserted i
            WHERE h.id = i.id
              AND h.partition_date = i.partition_date
            RETURNING 1
        )
        SELECT count(*) FROM deleted', v_cols, batch_size::text, cutoff_ts::text)
    INTO moved_count;

    RETURN QUERY SELECT moved_count;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.promote_session_bodies_hot_to_partition IS
    'Atomically move old rows from session_bodies_hot to monthly partitions
     (708 rewrite: set-equality column-contract check + catalog-derived
     explicit column list so the kind column and future symmetric columns flow
     through promotion; order-insensitive for legacy parent/hot column-order
     drift). Rejects NULL or non-positive retention_window/batch_size
     (migration 638 semantics kept).';

-- =============================================
-- 5. DROP sessions 456 死列（不可逆点：三列本机审计 100% NULL）
-- =============================================

ALTER TABLE public.sessions
    DROP COLUMN IF EXISTS last_full_request,
    DROP COLUMN IF EXISTS last_full_response,
    DROP COLUMN IF EXISTS last_full_payload_at;

-- =============================================
-- 6. 后置校验
-- =============================================

DO $$
DECLARE
    v_parent_shape TEXT;
    v_hot_shape TEXT;
BEGIN
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_bodies_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION '708 post-condition failed: session_bodies hot/parent column sets diverge';
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='sessions'
          AND column_name IN ('last_full_request','last_full_response','last_full_payload_at')
    ) THEN
        RAISE EXCEPTION '708 post-condition failed: sessions.last_full_* still present';
    END IF;
    IF (SELECT count(*) FROM information_schema.columns
        WHERE table_schema='public' AND table_name='session_bodies_unified') <> 13 THEN
        RAISE EXCEPTION '708 post-condition failed: session_bodies_unified must expose 13 columns (kind appended)';
    END IF;
END
$$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('708', 'storage plan v2 S1a: session_bodies kind pivot prep (final_full/turn_delta) + drop sessions.last_full_* dead columns')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
