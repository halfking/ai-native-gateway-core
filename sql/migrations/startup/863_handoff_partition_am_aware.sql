-- Migration 863 (2026-10-14): make ensure_handoff_logs_partition storage-am
-- aware (heap worlds are legitimate).
--
-- Root cause (211 日志审计轮): 211 的 llm_gateway 库是 mac mini pg18 全量
-- dump → acc-pg17 的 restore 血缘，目标环境没有 pgcolumnar —— columnar 子
-- 表落成 heap 分区（仍正确挂在 handoff_logs 父表下，default 分区在场）。
-- 714 版函数体要求挂载子表 am='columnar' 否则 RAISE：
--
--   partition_manager: ensure partition failed fn=ensure_handoff_logs_partition
--   error: public.handoff_logs_2026_10 exists but is not an attached columnar
--          handoff partition (SQLSTATE P0001)
--
-- 后果链（写入无损，但一坏三处）：
--   1. 每次 boot/24h tick 对当月+次月各报一条 P0001 噪音；
--   2. 未来月份走 CREATE ... USING columnar 在无 pgcolumnar 环境必失败，
--      新行只能落 default 分区（月度组织与压缩意图静默退化）；
--   3. promote_handoff_logs_hot_to_partition 内部 PERFORM 本函数 → 同因
--      失败 → hot 行永不促升（当前 211 hot=0 行无实害，潜伏）。
--
-- 判定依据（为何接受 heap）：正确性不变量是「挂在 public.handoff_logs 父表
-- 下」（分区边界由挂载动作本身保证），columnar 只是存储优化。挂在错误父表
-- 仍然 raise（那是真歧义）。这与 517 迁移的 relkind 形态探测同一先例：
-- restore/无扩展世界是合法形态，不是需要修库的病态。
--
-- 变更面（只改 ensure 函数本体）：
--   * 已挂载分支：仅校验父表 OID；am 任意（columnar/heap 均接受）。
--   * 创建分支：探测 pg_am 中 columnar 是否在场，在场才 USING columnar，
--     否则建普通 heap 分区（同一组分区级索引照建）。
--   * 714 的会话时区钉扎（SET LOCAL TIME ZONE 'Asia/Shanghai' + 初始化
--     移出 DECLARE）原样保留。
--   * promote 函数不动：它不自带 columnar DDL，失败源就是本函数（修复
--     ensure 即解除）；其 INSERT/DELETE 单语句原子性（535 契约）不受影响。
--
-- Rollback: 863.down 恢复 714 版本体（columnar 硬要求）。

BEGIN;

\set ON_ERROR_STOP on

CREATE OR REPLACE FUNCTION public.ensure_handoff_logs_partition(p_month timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    m0 timestamp;
    m1 timestamp;
    part_name text;
    existing_parent oid;
    existing_am text;
    columnar_available boolean;
    using_am text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    m0 := date_trunc('month', p_month AT TIME ZONE 'Asia/Shanghai');
    m1 := m0 + interval '1 month';
    part_name := 'handoff_logs_' || to_char(p_month AT TIME ZONE 'Asia/Shanghai', 'YYYY_MM');

    SELECT i.inhparent, am.amname INTO existing_parent, existing_am
    FROM pg_inherits i
    JOIN pg_class child ON child.oid = i.inhrelid
    JOIN pg_namespace cn ON cn.oid = child.relnamespace
    LEFT JOIN pg_am am ON am.oid = child.relam
    WHERE cn.nspname = 'public' AND child.relname = part_name;

    IF existing_parent IS NOT NULL THEN
        -- 863: 挂对父表即合法。am 是存储细节：有 pgcolumnar 的环境是
        -- columnar，restore 到无 pgcolumnar 环境（211 acc-pg17 血缘）是
        -- heap。714 版在这里对 am<>'columnar' raise，让上述环境每次
        -- tick 报 P0001 且未来月份创建必败——详见文件头注。
        IF existing_parent <> 'public.handoff_logs'::regclass THEN
            RAISE EXCEPTION 'public.% exists but is attached to oid %, not public.handoff_logs — resolve manually', part_name, existing_parent;
        END IF;
        RETURN;
    END IF;

    SELECT EXISTS(SELECT 1 FROM pg_am WHERE amname = 'columnar')
      INTO columnar_available;
    IF columnar_available THEN
        using_am := ' USING columnar';
    ELSE
        using_am := '';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L)%s',
        part_name, m0, m1, using_am
    );
    EXECUTE format('CREATE INDEX %I ON public.%I (created_at, id)', part_name || '_created_at_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)', part_name || '_session_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)', part_name || '_tenant_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', part_name || '_trigger_idx', part_name);
END;
$function$;

-- 自证：替换后的函数体不再含 am<>'columnar' 的 raise 分支，且创建语句
-- 保留 columnar 路径（经 %s 注入，无 pgcolumnar 世界建 heap）。534/714 的
-- 历史文件保持原样（已应用环境只走本文件；全新环境按序 534→714→863
-- 重放，终态一致）。
DO $verify$
DECLARE
    body text := pg_get_functiondef('public.ensure_handoff_logs_partition(timestamptz)'::regprocedure);
BEGIN
    IF position('not an attached columnar handoff partition' in body) > 0 THEN
        RAISE EXCEPTION 'migration 863: ensure_handoff_logs_partition still carries the columnar-raise branch';
    END IF;
    IF position('USING columnar' in body) = 0 THEN
        RAISE EXCEPTION 'migration 863: ensure_handoff_logs_partition lost the columnar create path';
    END IF;
END
$verify$;

COMMIT;
