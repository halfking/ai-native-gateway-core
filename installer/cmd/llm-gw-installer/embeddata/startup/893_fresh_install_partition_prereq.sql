-- 893_fresh_install_partition_prereq.sql
--
-- 全新安装的「月分区」前置：让新装库在**装完那一刻**就有当月+次月分区。
--
-- ── 为什么需要这条（2026-10-21 R61 轮实测，handoff §19.7）────────────────────
--
-- 全新安装的路径是：embeddata/00-prereqs → 01-schema（基线）→ 02-seed
--   → StartupFiles 登记的 startup 迁移。
-- 它**不跑**那批建分区的迁移 ⇒ 新装库里 15 个 RANGE 族只有 2026_07/2026_08：
--
--   ERROR:  no partition of relation "session_turns" found for row
--   DETAIL:  Partition key of the failing row contains (partition_date) = (2026-10-20).
--
-- ⇒ **核心写路径（session_turns / sessions / usage_ledger / request_wal /
--   routing_decision_log …）在全新安装上直接 23514**。
--
-- ── 纠正一：R61 的「ensure 函数也没了」是**错的**（2026-10-21 R62 轮实测）──────
--
-- R61 轮记的是「运行期 `bg/partition_manager.go` 每 24h 调 ensure_<表>_partition()
-- 建当月/次月分区，而这 11 个函数的来源迁移在 installer 链里全部未登记
-- ⇒ 新装库既没有函数、也没有分区」。
--
-- ★ **后半句是错的**：那 11 个 ensure 函数**全部在 baseline `sql/schema/01-schema.sql`
-- 里**（实测逐个核对：`ensure_request_logs_partition` / `…_bodies_…` /
-- `ensure_request_wal_partition` / `ensure_routing_decision_log_partition` /
-- `ensure_credential_model_index_partition` / `ensure_usage_ledger_partition` /
-- `ensure_credit_ledger_partition` / `ensure_tool_usage_stats_partition` /
-- `ensure_dashboard_events_partition` / `ensure_session_module_executions_partition` /
-- `ensure_sessions_v2_partitions` ⇒ 11/11 命中），
-- **函数有，缺的是「当月/次月分区」这个产物**。
--
-- ★★ **纠正一之补（2026-10-21 R62 轮合并后发现，比上面更要紧）**：
--   并行会话的迁移 `890_drop_request_logs_v1_family.sql` **退役了 v1 request_logs 族**
--   ⇒ 它把 `ensure_request_logs_partition` 与 `ensure_request_logs_bodies_partition`
--   **一起 DROP 了**：
--       DROP FUNCTION IF EXISTS public.ensure_request_logs_partition(timestamptz);
--       DROP FUNCTION IF EXISTS public.ensure_request_logs_bodies_partition(timestamptz);
--   ⇒ ★★ **本迁移在安装链的链尾（索引 294）⇒ 它跑在退役迁移之后**
--   ⇒ ⇒ ★★★ **若本条还「确保那两个函数存在」，就会把已退役的族「复活」**
--     ⇒ ⇒ 所以「**不建任何函数**」这个决定，在合并后从「省事」升级为**安全关键属性**。
--   ⇒ ⇒ 本条的 §② 也就**自动不含**那两个族（它们的表已被 DROP ⇒ 发现式循环看不见它们）。
--
-- ⇒ 所以本条**不负责建函数**（那会与 baseline 的既有版本重名、且参数/返回类型
--   都与运行期约定不一致 ⇒ 纯风险无收益）。
--
-- ── 纠正二：子分区名**不能**用 `parent_YYYY_MM` 推（2026-10-21 R62 轮实测）────
--
-- 本条第一版用 `p_parent || '_' || YYYY_MM` 造子分区名 ⇒ **对 `_request_logs`
-- 与 `_request_logs_bodies` 两族造错了名字**：
--
--   父表 `_request_logs` 的子分区在 baseline 与**生产 252** 里都叫
--   `request_logs_YYYY_MM`（**没有下划线**）—— S4 把父表改名成 `_request_logs`
--   时子分区沿用了旧名；而 baseline 自带的 `ensure_request_logs_partition`
--   函数体里写死 `part_name := 'request_logs_' || to_char(month_start,'YYYY_MM')`。
--
-- ⇒ 后果实测（不是推理）：
--   ERROR: partition "request_logs_2026_10" would overlap partition "_request_logs_2026_10"
--   ⇒ 新装库里 **baseline 自带的 ensure 函数永远报错、该族永远无法自愈**。
--
-- ⇒ 所以正确形态是：**前缀按该族既有兄弟分区的「众数命名」推导**，
--   而不是拿父表名去拼。（`llm_gateway_monthly_family_prefix`）
--
-- ── 纠正三：default 兜底是**仓库规范**（rule 33 §2），不是可选项 ────────────
--
-- baseline **一个 default 分区都不建**（实测 grep = 0 命中）⇒ 新装库里
-- 任何落在范围外的行都会 23514。
-- rule 33 §2 明确要求 DEFAULT 分区做兜底（见
-- docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md
-- 「DEFAULT 分区是仓库规范（rule 33 §2）要求的兜底，不是可选项」）。
-- ⇒ 本条给每个有月分区的族补 default。
--
-- ⚠ 诚实边界（生产侧差异，**本条不处理**）：生产 252 上
--   `credential_model_index` / `request_wal` / `routing_decision_log` / `usage_ledger`
--   **没有** default 分区（另外 4 族有）⇒ 新装库与生产在这 4 族上形态不一致。
--   ⇒ 补生产那 4 个属于**生产写入**，需属主另行拍板，不在本条范围。
--
-- ── 为什么不能直接登记 473 / 334 / 335 / 330 / 328a（实测硬伤）───────────────
--
--   473_partition_precreate_2026_09_10.sql
--     → 新装库上直接失败：`relation "public.request_logs" does not exist`
--       （它引用的是 S4 退役改名前的表名）
--     → 而且它是 **dated** 的（只到 2026_10）⇒ 2026-11 起又缺
--   334 / 335 → `credit_ledger_old already exists` / `cannot change return type`
--   330      → `cannot change return type of existing function`
--   328a     → `request_logs parent not found`（同上，改名前）
--
-- ⇒ 所以本条是**新的、幂等的、认得现表名与现命名约定**的迁移：
--   ① 通用助手：从**库里已有的最新月分区**推导边界约定（因此与表名/偏移无关），
--      平移月份建出目标月分区；
--   ② 给所有「有月分区」的 RANGE 族补出**当月 + 次月**分区（+ default 兜底）；
--   ③ **不建任何函数**（baseline 已有，见「纠正一」）。
--
-- Idempotent: 全部按 pg_class / 边界覆盖判断；重复跑无副作用。


-- ── ①a 通用助手：该族的「子分区名前缀」（按既有兄弟的众数推导）───────────────
--
-- ⚠ 为什么不能直接用 `p_parent || '_'`：
--   `_request_logs` 族的子分区叫 `request_logs_YYYY_MM`（无下划线），
--   父表却叫 `_request_logs` ⇒ 用父表拼会造出**与既有约定不同名**的分区，
--   进而让 baseline 自带的 ensure 函数判定「不存在」→ 重建 → 边界重叠报错。
--
--   众数（而不是"最新一个"）是刻意的：万一库里同时存在两种前缀，
--   多数派才是该族真正的约定。

-- ★★ 锁守卫（2026-10-21 §143.35 收尾轮补齐；本仓规则，见 §44）
--   本文件建当月/次月分区 ⇒ `CREATE TABLE ... PARTITION OF` 要拿父表的
--   AccessExclusiveLock ⇒ **无上限的锁等待会在等锁期间堵住全库**
--   （2026-10-18 生产实测：一次迁移这样把 14 个网关会话堵了约 1.5 分钟）。
--   ⇒ 本仓规则：**870 号之后的迁移必须自带 lock_timeout**
--   （门：`installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go`）。
--
-- ★ 为什么用【session 级】`SET` 而不是 `SET LOCAL`：
--   本文件**没有显式事务**（三个 DO 块各自独立）⇒ `SET LOCAL` 离开事务块
--   只会被 PG 当成「无效」而告警 ⇒ 不生效。
--   ⇒ ⇒ 而【本文件对同一形态的既有惯例就是 session 级 `SET`】：
--   871 / 877 / 887 / 888 四个迁移同样没有显式事务、同样用 `SET lock_timeout`。
--   ⇒ ★ 不外泄：installer 的 applySQL 对【每个文件单独起一次 psql】
--   ⇒ ⇒ session 级设置只作用于本文件这次执行。
--
-- ★ 取值 60s：与本仓 870+ 迁移的通行值一致（872~892 的 `SET LOCAL` 都是 60s）。
--   ⇒ 命中即失败（55P03）而不是继续堵库 —— 这是本仓的既定取舍：
--   **宁可让迁移失败，也不要让整库被一条迁移卡住**。
SET lock_timeout = '60s';

CREATE OR REPLACE FUNCTION public.llm_gateway_monthly_family_prefix(p_parent text)
RETURNS text
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_parent regclass;
    v_prefix text;
BEGIN
    v_parent := to_regclass('public.' || p_parent);
    IF v_parent IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT t.pref INTO v_prefix
      FROM (
        SELECT regexp_replace(c.relname, '_\d{4}_\d{2}$', '') AS pref,
               count(*) AS n
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
         WHERE i.inhparent = v_parent
           AND c.relname ~ '_\d{4}_\d{2}$'
         GROUP BY 1
      ) t
     ORDER BY t.n DESC, t.pref
     LIMIT 1;

    RETURN v_prefix;
END;
$$;


-- ── ①b 通用助手：按「库里已有最新月分区」的边界约定，建出目标月的分区 ──────────
--
-- 幂等靠**边界覆盖**判定（不看名字）：只要该族已有某个子分区的范围正好覆盖目标月，
-- 就直接返回 —— 这样即便库里存在「名字不对但边界对」的既有分区，也不会重复建。
CREATE OR REPLACE FUNCTION public.llm_gateway_ensure_monthly_partition(
    p_parent text,
    p_month   date
)
RETURNS boolean
LANGUAGE plpgsql
AS $$
DECLARE
    v_parent   regclass;
    v_prefix   text;
    v_latest   text;
    v_from_lit text;
    v_to_lit   text;
    v_new_from text;
    v_new_to   text;
    v_cur_from date;
    v_cur_to   date;
    v_child    text;
    v_cov      int := 0;
BEGIN
    v_parent := to_regclass('public.' || p_parent);
    IF v_parent IS NULL THEN
        RETURN FALSE; -- 该族在当前库里不存在（lite 模式 / 裁剪过的库）⇒ 不判
    END IF;

    -- 取「后缀最大」的月分区作为边界约定的样本（_YYYY_MM 字典序 = 时间序）
    SELECT c.relname INTO v_latest
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE i.inhparent = v_parent
       AND c.relname ~ '_\d{4}_\d{2}$'
     ORDER BY substring(c.relname FROM '_(\d{4}_\d{2})$') DESC
     LIMIT 1;
    IF v_latest IS NULL THEN
        RETURN FALSE; -- 不是月分区族（或还没有任何月分区）⇒ 不判
    END IF;

    v_prefix := public.llm_gateway_monthly_family_prefix(p_parent);
    IF v_prefix IS NULL THEN
        RETURN FALSE;
    END IF;

    -- 从该分区的 bound 表达式里取出两个边界字面量
    SELECT m[1], m[2] INTO v_from_lit, v_to_lit
      FROM (
        SELECT regexp_match(pg_get_expr(c.relpartbound, c.oid),
                            'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)') AS m
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
         WHERE i.inhparent = v_parent AND c.relname = v_latest
      ) s;
    IF v_from_lit IS NULL OR v_to_lit IS NULL THEN
        RETURN FALSE; -- bound 形态不是「FROM (lit) TO (lit)」（例如 DEFAULT 分区）⇒ 不判
    END IF;

    -- 平移月份：保留「日 + 时分秒 + 偏移」的约定，只换年月
    --   '2026-08-01 00:00:00+08' → substring(from 8) = '-01 00:00:00+08'
    --   '2026-08-01'             → substring(from 8) = '-01'
    v_new_from := to_char(p_month, 'YYYY-MM') || substring(v_from_lit FROM 8);
    v_new_to   := to_char(p_month + INTERVAL '1 month', 'YYYY-MM') || substring(v_to_lit FROM 8);

    -- 覆盖检查：只看**边界**，不看名字 ⇒ 对「名字不对但边界对」的既有分区也幂等
    v_cur_from := to_date(substring(v_new_from FROM 1 FOR 10), 'YYYY-MM-DD');
    v_cur_to   := to_date(substring(v_new_to   FROM 1 FOR 10), 'YYYY-MM-DD');
    SELECT count(*) INTO v_cov
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE i.inhparent = v_parent
       AND c.relname ~ '_\d{4}_\d{2}$'
       AND to_date(substring((regexp_match(pg_get_expr(c.relpartbound, c.oid),
              'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[1] FROM 1 FOR 10),
              'YYYY-MM-DD') = v_cur_from
       AND to_date(substring((regexp_match(pg_get_expr(c.relpartbound, c.oid),
              'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[2] FROM 1 FOR 10),
              'YYYY-MM-DD') = v_cur_to;
    IF v_cov > 0 THEN
        RETURN TRUE; -- 目标月已被覆盖 ⇒ 幂等返回
    END IF;

    v_child := v_prefix || '_' || to_char(p_month, 'YYYY_MM');
    IF to_regclass('public.' || v_child) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
            v_child, p_parent, v_new_from, v_new_to);
        RAISE NOTICE 'partition created: % (%)', v_child, v_new_from;
    END IF;
    RETURN TRUE;
END;
$$;


-- ── ② 给所有「有月分区」的 RANGE 族补出当月 + 次月分区 + default 兜底 ─────────
--
-- 发现式 ⇒ 与表名无关；时区用 Asia/Shanghai（与 bg/partition_manager.go 的
-- partitionTZ 同源，避免 UTC 会话在 +08 0~8h 把次月误建为本月）。
DO $$
DECLARE
    r            record;
    v_month      date := date_trunc('month', (now() AT TIME ZONE 'Asia/Shanghai')::date)::date;
    v_parent     regclass;
    v_prefix     text;
    v_has_def    int := 0;
BEGIN
    FOR r IN
        SELECT DISTINCT p.relname
          FROM pg_inherits i
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_namespace n ON n.oid = p.relnamespace
         WHERE n.nspname = 'public'
           AND p.relkind = 'p'
           AND c.relname ~ '_\d{4}_\d{2}$'
         ORDER BY 1
    LOOP
        PERFORM public.llm_gateway_ensure_monthly_partition(r.relname, v_month);
        PERFORM public.llm_gateway_ensure_monthly_partition(r.relname, (v_month + INTERVAL '1 month')::date);

        -- default 兜底（rule 33 §2）：该族**一个 default 子分区都没有**时才建
        --   （PG 不允许同一父表有两个 default ⇒ 判据是「有没有」，不是「叫什么」）
        v_parent := to_regclass('public.' || r.relname);
        IF v_parent IS NOT NULL THEN
            SELECT count(*) INTO v_has_def
              FROM pg_inherits i
              JOIN pg_class c ON c.oid = i.inhrelid
             WHERE i.inhparent = v_parent
               AND pg_get_expr(c.relpartbound, c.oid) ILIKE 'DEFAULT%';
            IF v_has_def = 0 THEN
                v_prefix := public.llm_gateway_monthly_family_prefix(r.relname);
                BEGIN
                    EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I DEFAULT',
                                   v_prefix || '_default', r.relname);
                    RAISE NOTICE 'default partition created: %', v_prefix || '_default';
                EXCEPTION WHEN OTHERS THEN
                    -- 父表不是 RANGE 分区 / 已有 default ⇒ 不强加
                    NULL;
                END;
            END IF;
        END IF;
    END LOOP;
END;
$$;
