-- ===========================================================================
-- v1 `request_logs` 族**清空**（为迁移 890 的「非空闸」铺路）—— 生产执行件
--
-- ★ 为什么需要这一步：
--   迁移 890（`sql/migrations/startup/890_drop_request_logs_v1_family.sql`）
--   自带**非空闸**：族里有行就 `RAISE EXCEPTION`，把「要不要丢历史」变成一个
--   必须显式回答的问题（删表 = 删历史，down 只能重建骨架、救不回行）。
--   ⇒ 属主已裁定：**v1 族历史连表带数据直接删除** ⇒ ⇒ 本脚本就是那条
--   「显式回答」的执行件：先把族清空（让 890 的闸放行），再让 890 删表。
--
-- ★★ 三条硬约束（违反任一条 ⇒ 停下来，不要继续）：
--   ① **只清 v1 族**。会话族（`session_turns*` / `session_turn_details*`）
--      **一个字都不许碰** ⇒ 本脚本只按**白名单**枚举对象，不做任何
--      `DROP` / `DELETE FROM` 的自由文本。
--   ② **确认令牌**：必须显式 `SET LOCAL llm_gateway.confirm_v1_truncate = 'YES';`
--      否则整段拒绝执行 ⇒ ⇒ 防止「误粘贴就跑」。
--   ③ **先核对会话族覆盖**：脚本第 0 段会打印 v1 族与会话族的行数与时间范围；
--      若是人工执行，**必须先看那段输出**再决定是否继续。
--
-- ★ 为什么用 TRUNCATE 而不是 DELETE：
--   · TRUNCATE 是**瞬时**的（不写 WAL、不产生膨胀）⇒ 66.9 万行也是秒级；
--   · DELETE 会产生同等规模的膨胀，删完还要 VACUUM ⇒ 而这些表**紧接着就要
--     被 890 DROP** ⇒ 膨胀是纯浪费。
--   · ★ TRUNCATE 对分区母表会**连带清空所有分区**（PG11+）⇒ 一条语句即可。
--
-- ★ 幂等：已清空再跑一次是 no-op（TRUNCATE 空表不报错）。
--
-- ★ 用法（生产，两台都适用；211 的 v1 族已被 890 删掉 ⇒ 本脚本在其上
--   只做「核对 + 跳过」）：
--
--   ★★ **令牌必须与脚本在同一个 psql 会话里设置**（实测：`PGOPTIONS='-c
--   llm_gateway.confirm_v1_truncate=YES'` 对**自定义 GUC 无效** ⇒
--   `current_setting(..., true)` 仍返回 NULL ⇒ 脚本照常拒绝 ⇒ 别用 PGOPTIONS）：
--
--   { echo "SET llm_gateway.confirm_v1_truncate = 'YES';"; \
--     cat scripts/audit/clear_v1_family_for_890.sql; } \
--     | ssh 252 "docker exec -i pg-252-pg17 psql -U postgres -d llm_gateway -X"
--
--   # 211 同理（容器名 acc-pg17）。
--
-- ★ 本脚本在**一次性库**上实测过（2026-10-21，`itgate_r72g`）：
--   ① 无令牌 ⇒ 拒绝执行且行数不变；② 带令牌 ⇒ v1 族归零而**会话族一字未动**
--   （9,000 / 50 行不变）；③ 再跑一次 ⇒ 幂等；④ 复核段全 0 ⇒ 890 的闸会放行。
-- ===========================================================================
\set ON_ERROR_STOP on
SET statement_timeout = '600s';
SET lock_timeout = '10s';

BEGIN;

-- ── 确认令牌（防误粘贴）──────────────────────────────────────────────────
DO $$
BEGIN
  IF current_setting('llm_gateway.confirm_v1_truncate', true) IS DISTINCT FROM 'YES' THEN
    RAISE EXCEPTION
      '拒绝执行：本脚本会**清空 v1 request_logs 族的历史**（不可恢复）'
      ' ⇒ 请先显式声明：SET LOCAL llm_gateway.confirm_v1_truncate = ''YES'';';
  END IF;
END $$;

-- ── 0. ★ 核对段：会话族是否覆盖 v1 族的时间范围（人工必看）────────────
\echo '════════ 0. 核对：v1 族 vs 会话族（行数 / 时间范围）════════'
DO $$
DECLARE
  v_obj text;
  v_sql text;
  v_cnt bigint;
  v_min text;
  v_max text;
BEGIN
  FOREACH v_obj IN ARRAY ARRAY[
      '_request_logs', 'request_logs',
      '_request_logs_bodies', 'request_logs_bodies',
      '_request_logs_hot', 'request_logs_hot',
      '_request_logs_bodies_hot', 'request_logs_bodies_hot',
      '_request_logs_archive', 'request_logs_archive'
  ] LOOP
    IF to_regclass('public.' || v_obj) IS NOT NULL THEN
      v_sql := format('SELECT count(*), min(ts)::text, max(ts)::text FROM public.%I', v_obj);
      EXECUTE v_sql INTO v_cnt, v_min, v_max;
      RAISE NOTICE 'v1 族 % rows=% ts=[%, %]', v_obj, v_cnt, v_min, v_max;
    END IF;
  END LOOP;
  FOREACH v_obj IN ARRAY ARRAY['session_turns', 'session_turns_hot'] LOOP
    IF to_regclass('public.' || v_obj) IS NOT NULL THEN
      v_sql := format('SELECT count(*), min(ts)::text, max(ts)::text FROM public.%I', v_obj);
      EXECUTE v_sql INTO v_cnt, v_min, v_max;
      RAISE NOTICE '会话族 % rows=% ts=[%, %]', v_obj, v_cnt, v_min, v_max;
    END IF;
  END LOOP;
END $$;
\echo '★ 判读：会话族的 ts 下界必须**早于或等于** v1 族的下界（否则有历史没被镜像）。'

-- ── 1. 清空 v1 族（白名单枚举，TRUNCATE 瞬时）──────────────────────────
\echo '════════ 1. 清空 v1 族 ════════'
DO $$
DECLARE
  v_obj text;
BEGIN
  FOREACH v_obj IN ARRAY ARRAY[
      -- 母表（分区）：TRUNCATE 连带清空所有分区
      '_request_logs', 'request_logs',
      '_request_logs_bodies', 'request_logs_bodies',
      '_request_logs_archive', 'request_logs_archive',
      -- hot 表（普通表）
      '_request_logs_hot', 'request_logs_hot',
      '_request_logs_bodies_hot', 'request_logs_bodies_hot'
  ] LOOP
    IF to_regclass('public.' || v_obj) IS NOT NULL THEN
      EXECUTE format('TRUNCATE TABLE public.%I', v_obj);
      RAISE NOTICE '已清空 public.%', v_obj;
    END IF;
  END LOOP;
END $$;

-- ── 2. 复核：族内行数必须全为 0（= 890 的非空闸会放行）────────────────
\echo '════════ 2. 复核（必须全 0 ⇒ 890 的非空闸会放行）════════'
DO $$
DECLARE
  v_obj text;
  v_cnt bigint;
  v_bad int := 0;
BEGIN
  FOREACH v_obj IN ARRAY ARRAY[
      'request_logs', '_request_logs',
      'request_logs_hot', '_request_logs_hot',
      'request_logs_bodies', '_request_logs_bodies',
      'request_logs_bodies_hot', '_request_logs_bodies_hot',
      'request_logs_archive', '_request_logs_archive'
  ] LOOP
    IF to_regclass('public.' || v_obj) IS NOT NULL THEN
      EXECUTE format('SELECT count(*) FROM public.%I', v_obj) INTO v_cnt;
      RAISE NOTICE '  % rows=%', v_obj, v_cnt;
      IF v_cnt > 0 THEN v_bad := v_bad + 1; END IF;
    END IF;
  END LOOP;
  IF v_bad > 0 THEN
    RAISE EXCEPTION '仍有 % 个 v1 对象非空 ⇒ 890 的非空闸会拒绝执行 ⇒ 停下来查', v_bad;
  END IF;
  RAISE NOTICE '★ 全部为 0 ⇒ 890 的非空闸会放行';
END $$;

COMMIT;

\echo '════════ 3. 下一步：执行迁移 890（删表）════════'
\echo '★ 此刻仍**没有删表** —— 清空只是让 890 的闸放行。'
\echo '★ 删表由 890 自己做（且它不用 CASCADE ⇒ 预期外的依赖者会让它响亮失败）。'
