-- ===========================================================================
-- File:          sql/migrations/startup/835_modality_verify_probe_ledger.down.sql
-- Migration:     835 (down)
-- Database:      llm_gateway
--
-- 把 system_probe_runs 的 task_type 词表收回原来的 6 个值。
--
-- ⚠ 本 down **会删数据**，且这件事无法回避 —— 先说清楚，免得被当成无害清理
--
--   收窄 CHECK 与放宽是**不对称**的。835 加约束时，新增的 modality_verify
--   在原 6 值之外，ADD 成功是因为没有任何一行取过它；回滚方向相反：
--   一旦核实循环在生产跑过，库里就有 task_type='modality_verify' 的行，
--   而收窄后的 CHECK 不接受它们 ⇒ `ADD CONSTRAINT` 会**整条失败**。
--
--   与 833 的 down 形成对照：833 的 down 只 DROP 约束，删掉即回到「没有这条
--   约束」的状态，不需要处理数据；本条 down 是把词表**改小**，改小必然要先
--   把越界的行清掉，否则迁移中止在一条比原问题更让人困惑的错误上。
--
--   删的范围精确到一行 WHERE：只删 task_type='modality_verify'，其它
--   task_type 的行（direct_ping / gateway_ping / chat_minimal / chat_tool /
--   chat_stream / http_ping）一条不碰。
--
--   这确实是在删证据。执行前请先导一份：
--       SELECT * FROM public.system_probe_runs
--        WHERE task_type = 'modality_verify' ORDER BY created_at DESC
--        \copy ... TO '...csv' CSV HEADER
--   台账行是「核实循环跑过什么」的**唯一**留痕（语义探针直连上游，不产生
--   request_logs），删掉之后那一段运行历史就只存在于日志轮转里。
--
-- 回滚它 = 重新打开那个盲区：多模态核实循环恢复成「在跑，但运维侧查不到」。
--
-- 幂等：DELETE 与 DROP ... IF EXISTS 均可安全重放。
-- ===========================================================================
BEGIN;

-- 先报数再删：执行者应当知道自己在删多少行，而不是事后从 DELETE 的影响
-- 行数里反推。
DO $do$
DECLARE
    n bigint;
BEGIN
    SELECT count(*) INTO n
      FROM public.system_probe_runs
     WHERE task_type = 'modality_verify';
    IF n > 0 THEN
        RAISE NOTICE '835 down: deleting % system_probe_runs row(s) with task_type=modality_verify '
                     '(required: the narrowed CHECK would otherwise reject them and the migration '
                     'would abort). Export them first if you need the run history.', n;
    END IF;
END
$do$;

DELETE FROM public.system_probe_runs WHERE task_type = 'modality_verify';

ALTER TABLE public.system_probe_runs
    DROP CONSTRAINT IF EXISTS system_probe_runs_task_type_check;

ALTER TABLE public.system_probe_runs
    ADD CONSTRAINT system_probe_runs_task_type_check
    CHECK (task_type = ANY (ARRAY[
        'direct_ping'::text,
        'gateway_ping'::text,
        'chat_minimal'::text,
        'chat_tool'::text,
        'chat_stream'::text,
        'http_ping'::text
    ]));

-- ⚠ 若在 835 之后有别的迁移往 task_type 加过值，收窄会把**那些**值也一起抹掉
--   （本 down 的词表是硬编码的 6 值）。真发生过的话，回滚前把该值补进上面
--   的 ARRAY，否则那些行会被 CHECK 拒绝，且报错信息指向的是本迁移。
--   查法：SELECT DISTINCT task_type FROM public.system_probe_runs;

COMMIT;
