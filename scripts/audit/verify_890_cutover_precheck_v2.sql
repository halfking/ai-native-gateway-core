-- ===========================================================================
-- 迁移 890（v1 `request_logs` 族真删）——**加固版执行前置核验**（只读）
--
-- ★ 为什么要有 v2（2026-10-21，R72g）：
--   v1（`scripts/audit/verify_890_cutover_precheck.sql`）有**两个会致命**的缺陷，
--   都是在 211/252 上**实测撞到**的：
--
--   ① **在 211 上直接报错**（不是「判据失效」，是**脚本报错**）：
--      v1 的第 ③ 段无条件引用 `public._request_logs_hot` 与
--      `public.request_logs_2026_10` ⇒ 而 211 的 v1 族**已经被 890 删掉了**
--      ⇒ 整段 ERROR ⇒ ★ **量具在目标环境上根本跑不起来**。
--      ⇒ v2 全部改成 `to_regclass(...) IS NULL` 守卫。
--
--   ② **在 211 上「量具不可用」⇒ 判词无效**：
--      211 的 `pg_stat_statements` 扩展装了但**没进 shared_preload_libraries**
--      ⇒ 查询直接 `ERROR: pg_stat_statements must be loaded via
--      "shared_preload_libraries"` ⇒ v1 的第 ①② 段（唯一给出「允许/禁止」
--      判词的两段）在 211 上**必然失败** ⇒ ⇒ 不能拿它当唯一依据。
--      ⇒ v2 把量具**降级为「旁证」**，并在「量具不可用」时**显式说出口**
--        （而不是静默跳过、让人误以为「没查到=没问题」）。
--
--   ③ ★★ **更根本的量具缺陷（本轮实测发现）**：
--      `pg_stat_statements` **只累加不删除** ⇒ 旧版本语句会**长期留在表里**
--      ⇒ 「统计表里还有一条引用 v1 的语句」**不能**证明「此刻在跑的还引用 v1」。
--      ⇒ 实测：252 的统计表里有 1 条**三臂**亲和度语句（536 次调用），
--        但 252 **当前产物**（`/opt/llm-gateway-go/bin/gateway`，Oct 5）
--        的 `FROM _request_logs_hot rl` / `FROM _request_logs rl` 命中数
--        **都是 0** ⇒ 那条三臂语句是**历史残留**。
--      ⇒ ★ **权威判据是「产物」**（strings 二进制），不是统计表。
--        本文件因此把判词分成两层：
--          · **权威层**（下方的 ①）：**产物**里还有没有 v1 直读 ⇒ 手工跑一条
--            `strings` 命令（见文件末尾）；
--          · **旁证层**（②）：统计表里「最近仍在跑」的语句（按 `stats_since`
--            与调用量判读），量具不可用时显式 SKIP 并说明理由。
--
-- ★★ 判词口径（本轮定案，勿弱化）：
--   · v1 族**表**（`_request_logs*` / `request_logs_hot` / `request_logs_20xx_*`）
--     ⇒ 890 会 DROP ⇒ **产物里有直读 ⇒ 禁止执行 890**（先换二进制）。
--   · v1 族**视图**（`request_logs_with_current_month_without_customer_id`
--     / `..._without_request_class_due_at`）⇒ 890 会 DROP ⇒ 同上。
--   · canonical 视图 `request_logs_with_current_month` ⇒ 890 会**替换**成
--     会话两臂体 ⇒ **读它不算 v1 直读**（这是 890 之后不 42P01 的关键机理）。
--
-- 用法（两台都跑，全程只读）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_890_cutover_precheck_v2.sql 120
--   # 211 的容器名是 acc-pg17，252 的是 pg-252-pg17（pg-252-pg17 用 podman）
-- ===========================================================================
SET statement_timeout = '60s';
SET lock_timeout = '5s';

\echo '════════ ① 权威层：v1 族【表/视图】是否还在（决定 890 能不能删）════════'
SELECT v.obj,
       to_regclass('public.' || v.obj) IS NOT NULL AS exists,
       CASE WHEN to_regclass('public.' || v.obj) IS NULL THEN NULL
            ELSE pg_size_pretty(pg_total_relation_size(to_regclass('public.' || v.obj)))
       END AS size
  FROM unnest(ARRAY[
         'request_logs', '_request_logs',
         'request_logs_hot', '_request_logs_hot',
         'request_logs_bodies', '_request_logs_bodies',
         'request_logs_bodies_hot', '_request_logs_bodies_hot',
         'request_logs_archive', '_request_logs_archive',
         'request_logs_with_current_month_without_customer_id',
         'request_logs_with_current_month_without_request_class_due_at',
         'request_logs_bodies_with_current_month',
         'request_logs_bodies_progress'
       ]) AS v(obj)
 ORDER BY 1;

\echo '════════ ② canonical 视图：已经是会话两臂体了吗（= 890 的效果预览）════════'
SELECT to_regclass('public.request_logs_with_current_month') IS NOT NULL AS canonical_exists,
       CASE WHEN to_regclass('public.request_logs_with_current_month') IS NULL THEN '(不存在)'
            WHEN pg_get_viewdef('public.request_logs_with_current_month'::regclass)
                 ~ '(_request_logs|request_logs_(hot|bodies|2026|default))'
            THEN '★ 仍含 v1 臂（890 会把它换成会话两臂体）'
            ELSE '★ 已是纯会话族（890 已执行过）'
       END AS shape;

\echo '════════ ③ 旁证层：pg_stat_statements（量具不可用时显式 SKIP）════════'
DO $$
DECLARE
  v_ok boolean;
  v_v1  bigint;
  v_all bigint;
BEGIN
  BEGIN
    PERFORM 1 FROM pg_stat_statements LIMIT 1;
    v_ok := true;
  EXCEPTION WHEN OTHERS THEN
    v_ok := false;
    RAISE NOTICE '★ SKIP：pg_stat_statements 不可用（%），本段不产出判词 ⇒ 【不得】把「没查到」当「没问题」', SQLERRM;
  END;
  IF v_ok THEN
    SELECT count(*), count(*) FILTER (WHERE query LIKE '%_request_logs%')
      INTO v_all, v_v1
      FROM pg_stat_statements
     WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
       AND query LIKE '%auto_route_selections_all%';
    RAISE NOTICE '亲和度聚合语句：共 % 条，其中仍引用 v1 的 % 条', v_all, v_v1;
    IF v_v1 > 0 THEN
      RAISE NOTICE '⚠ 统计表里**存在**引用 v1 的亲和度语句 ⇒ 但它**可能是历史残留**'
                   '（本统计表只累加不删除）⇒ ★ **以产物判定为准**（见文件末尾的 strings 命令）';
    END IF;
  END IF;
END $$;

\echo '════════ ④ 会话族是否就位（890 的硬前置，缺任一 ⇒ 禁止执行）════════'
SELECT to_regclass('public.session_turns')           IS NOT NULL AS session_turns,
       to_regclass('public.session_turns_hot')       IS NOT NULL AS session_turns_hot,
       to_regclass('public.session_turn_details')    IS NOT NULL AS details,
       to_regclass('public.session_turn_details_hot')IS NOT NULL AS details_hot;

\echo '════════ ⑤ 迁移台账（★ 台账是「记录」不是「闸门」）════════'
SELECT count(*) AS ledger_rows,
       max(version::text) FILTER (WHERE version ~ '^[0-9]+$') AS max_version,
       bool_or(version = '890') AS applied_890
  FROM schema_migrations;

\echo '════════ ⑥ 权威层的人工一步：产物里还有没有 v1 直读 ════════'
\echo '★ 这一步**不能**用 SQL 做（产物在容器/主机上）⇒ 请手工跑：'
\echo '  # 211：docker exec llm-gateway-211-a sh -c '"'"'
\echo '    for p in "FROM public.request_logs_hot" \'
\echo '             "FROM public._request_logs" \'
\echo '             "FROM public.request_logs_with_current_month_without_customer_id" \'
\echo '             "FROM public.request_logs_with_current_month_without_request_class_due_at"; do \'
\echo '      printf "%-70s %s\n" "$p" "$(strings /app/llm-gateway | grep -cF "$p")"; done'"'"''
\echo '  # 252：B=/opt/llm-gateway-go/bin/gateway （同样四个 pattern）'
\echo '★ 四个计数**必须全是 0** ⇒ 才允许执行 890。'
\echo '★ 若非 0 ⇒ 那个产物**会**在 890 之后 42P01 ⇒ 【先换二进制】。'
