-- 758: diagnostic_runs / routing_audit_log 补代码已在写但 schema 从未定义的列（R79 续十七）
--
-- 缺陷：domains/routeincident 的两处 INSERT 引用了 4 个真库里不存在的列。
-- 证据三方一致：
--   · 基线 installer/.../01-schema.sql 的 diagnostic_runs = 13 列，无 route_key；
--   · 真库 pg_attribute 实测同样 13 列；routing_audit_log 基线 21 列 / 真库 22 列，
--     **都没有 reason**（有的是 failure_reason，语义不同）；
--   · 迁移链里 grep ADD COLUMN 只命中别的表的列（compression_reason /
--     handoff_reason / last_trigger_reason / reasoning_tokens），
--     **从未给这两张表加过**。
--
-- 可达性（这才是 P1 的依据，不是「看起来在主链路上」）：
--   cmd/gateway/main.go:3554  telemetryClient.AddOnRequestLogPersisted(incidentObserver.AsHook())
--   → Observer.Transition → writeAudit → persistRunInTx
--   **每条落库的请求日志都走这条路径**，两条 INSERT 都必然 42703。
--   加重因素：ObserverConfig{MaxRetries: 4}，重试循环 attempt 0..4 跑满 5 次；
--   42703 是**永久性错误**（列不会因重试而出现），重试纯属浪费，
--   耗尽后只 slog.Warn 不升级 ⇒ 每条请求日志触发 5 次注定失败的查询且完全静默。
--
-- 列类型依据（实测，不是推断）：
--   · route_key / parameters / result 在 actions.go:100-104 是 map[string]any，
--     代码传 json.Marshal 的 []byte。用 pgx v5 在 TEMP TABLE 上复刻真实 INSERT
--     形态（目标列 jsonb、SQL 无 ::jsonb 转换、Go 传 []byte）实测**成功**并回读出
--     正确值 ⇒ **只加迁移、不改代码**。
--   · reason 是操作员输入，action_infra.go:65 MaxReasonLen = 256，
--     sanitizeReason 按 rune 截断。PostgreSQL 的 character varying(n) 同样按
--     **字符**计（UTF-8 下中文不会溢出），与代码的 rune 语义一致。
--
-- ## 顶层执行（必读，续十六实测）
--
-- 把 DDL 包进 DO $$ … EXECUTE $ddl$…$ddl$; $$ 会**静默无效**——不报错、列没加、
-- 前后自校验全部照常通过。同事务对照：顶层 55 列 vs EXECUTE 内 54 列。
-- 所以这里一律顶层 ALTER，下面只用 DO 块做纯 SELECT 断言（那是有效的）。

-- ── 前置断言：这些列应当尚未存在 ────────────────────────────────────────────
DO $pre$
DECLARE
    n int;
BEGIN
    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'diagnostic_runs'
       AND column_name IN ('route_key', 'parameters', 'result');
    IF n > 0 THEN
        RAISE NOTICE 'diagnostic_runs 已含 % 个目标列 — 本迁移幂等，仍继续', n;
    END IF;
    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'routing_audit_log'
       AND column_name = 'reason';
    IF n > 0 THEN
        RAISE NOTICE 'routing_audit_log.reason 已存在 — 本迁移幂等，仍继续';
    END IF;
END
$pre$;

-- ── 顶层执行（必须顶层，见文件头）──────────────────────────────────────────
ALTER TABLE public.diagnostic_runs
    ADD COLUMN IF NOT EXISTS route_key  jsonb,
    ADD COLUMN IF NOT EXISTS parameters jsonb,
    ADD COLUMN IF NOT EXISTS result     jsonb;

ALTER TABLE public.routing_audit_log
    ADD COLUMN IF NOT EXISTS reason character varying(256);

-- ── 后置断言：读系统目录确认真的加上了；不满足即报错，绝不静默通过 ───────────
DO $post$
DECLARE
    n int;
BEGIN
    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'diagnostic_runs'
       AND column_name IN ('route_key', 'parameters', 'result');
    IF n <> 3 THEN
        RAISE EXCEPTION 'diagnostic_runs 目标列应 3 个，实得 % 个 — ROLLBACK', n;
    END IF;

    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'routing_audit_log'
       AND column_name = 'reason';
    IF n <> 1 THEN
        RAISE EXCEPTION 'routing_audit_log.reason 未加上 — ROLLBACK';
    END IF;

    -- 形状也要对：jsonb 变 text 会让 []byte 绑定失败，那正是本迁移要修的病。
    PERFORM 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'diagnostic_runs'
       AND column_name = 'route_key' AND data_type = 'jsonb';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'diagnostic_runs.route_key 类型不是 jsonb — ROLLBACK';
    END IF;

    RAISE NOTICE 'post-check ok: diagnostic_runs +3 jsonb, routing_audit_log.reason +varchar(256)';
END
$post$;
