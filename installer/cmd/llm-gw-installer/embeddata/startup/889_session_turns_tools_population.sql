-- 889: 裁决 session_turns.tools 死列（填充，不删）+ 订正 tools / turn_no 的列注释
--
-- -----------------------------------------------------------------------
-- 缺口本体（2026-10-20 会话族审计 G-6，实测非推理）
-- -----------------------------------------------------------------------
--   public.session_turns.tools（JSONB NOT NULL DEFAULT '[]'::jsonb，456 建列）
--   **从来没有被写过**。
--
--   证据（三条互相独立）：
--     ① 写方的 INSERT 列清单里没有它 —— domains/session/v2/turn_writer.go
--        的 appendTurnInLockedTx 列清单从 session_id 一路列到 tags，
--        **没有 tools** ⇒ 每行都停在列默认值 '[]'::jsonb。
--     ② 全仓无第二个写入方（grep session_turns 的 INSERT/UPDATE 列清单）。
--     ③ 读侧把这件事写在了注释里：db/request_logs_view_schema.go 的
--        sessionFamilyProjection 里，tools 那格旁边写着
--        「⚠ tools 尚无写入路径，本列恒为空数组 —— 读它得不到信息」。
--
--   ⇒ 后果：视图 request_logs_with_current_month（710 建，816/817/824 逐版
--     重建）把 t.tools 投影成 tool_calls ⇒ **管理台「工具调用」字段恒为空
--     数组**。注意这不是「该请求没有工具调用」，而是「没人写过这一列」。
--
--   真正的工具事实落在 public.session_tools / public.session_tools_hot
--   （写方 domains/toolexecution/postgres_store.go:61 的 Save）。
--
-- -----------------------------------------------------------------------
-- 二选一：为什么是「填充」而不是「删列」
-- -----------------------------------------------------------------------
--   删列（DROP COLUMN tools）在本仓**不可行**，会撞上一串依赖对象：
--
--     · 视图链：710_request_logs_view_session_family_v2.sql:271、
--       816_request_logs_view_client_ip_projection.sql:261、
--       817_request_logs_view_client_ip_semantic_guard.sql:244、
--       824_request_status_rate_limited_projection.sql:271 都写着
--       `, t.tools AS tool_calls`；
--     · Go 侧投影：db/request_logs_view_schema.go 的 sessionFamilyProjection
--       第 475 行是 "t.tools"（该投影按**位置**对齐视图列，改视图列序或
--       删列都会打乱对齐）；
--     · SELECT * 读者与三份基线快照（sql/schema/01-schema.sql、
--       deploy/sql/schemas/baseline/01-schema.sql、
--       installer/cmd/llm-gw-installer/embeddata/01-schema.sql）。
--
--   ⇒ 裁决：**填充**（POPULATE）。列保留并变成活列。
--
-- -----------------------------------------------------------------------
-- 填充是怎么做的（写路径侧，代码在 domains/session/v2/turn_writer.go）
-- -----------------------------------------------------------------------
--   turn_writer 的 INSERT 在**同一条语句**里用子查询从 session_tools_hot
--   聚合出 tools 的值（常量名 turnToolsJSONBExpr）。要点：
--
--     · 原子：子查询与 INSERT 同语句求值 ⇒ 不存在「插进去之后又被
--       promote 搬走、随后的 UPDATE 打空」的竞态。
--     · 零额外往返：位置参数仍是 101 个（只追加列、不重编号）⇒
--       既有判据（turn_host_tags_test.go 等）钉住的参数表不动。
--     · 零新索引：谓词是 (session_id, request_id)；session_tools_hot 上有
--       idx_session_tools_hot_session (session_id, started_at DESC)
--       ⇒ session_id 是索引前缀。
--
--   形状：每轮一个 JSON 数组，元素是
--     {tool_name, tool_call_id, status, duration_ms, error_type} 的**摘要**。
--     **不含** arguments / result —— 那两份正文在 session_bodies 与
--     session_tools 里，复制到轮次元数据行上是无界的重复。
--
--   ★ 历史行**不回填**：它们的工具行可能已被 promote 搬进月分区，
--     回填出来的值会不完整（而且要为每个历史行跑一次跨表聚合）。
--     「本列从 889 起才有值」是本次裁决的明示代价。
--
-- -----------------------------------------------------------------------
-- 顺带记录的**另一个**坑：session_tools.turn_no 恒为 NULL
-- -----------------------------------------------------------------------
--   session_tools / session_tools_hot 有 turn_no 列（706 建），但
--   **写方从不写它**：domains/toolexecution/postgres_store.go 的 INSERT
--   列清单是 (execution_id, session_id, request_id, tenant_id, tool_name,
--   tool_call_id, arguments, result, status, error_message, error_type,
--   started_at, completed_at, duration_ms, identity_hash, model, created_at,
--   partition_date) —— **没有 turn_no** ⇒ 100% 的行为 NULL。
--
--   ⇒ 任何按 (session_id, turn_no) 的 turn→tool join **会丢掉全部行**
--     （NULL 不等于任何值，包括 NULL = NULL 为假）。
--     可用的连接键是 (session_id, request_id)：两列都 NOT NULL。
--
--   本迁移**只把这件事写进列注释**（\d session_tools 就能看到），
--   **不改表结构、不回填** —— 回填 turn_no 需要把工具行与轮次行对上，
--   而工具行是在请求进行中写的、轮次行在请求结束时才写，那时
--   turn_no 还没定 ⇒ 结构上补不齐（这是 G-6 的遗留项，见
--   docs/audit/2026-10-20-session-family-dead-tables.md）。
--
-- -----------------------------------------------------------------------
-- 本迁移做什么
-- -----------------------------------------------------------------------
--   只用 COMMENT ON COLUMN 订正 4 条列注释：
--     public.session_turns.tools
--     public.session_turns_hot.tools
--     public.session_tools.turn_no
--     public.session_tools_hot.turn_no
--
--   ★ **不改表结构、不动数据、不加索引** ⇒ 零锁、零 IO、零回填代价。
--     COMMENT ON COLUMN 只写 pg_description 一行元数据。
--   ★ **不改 456 / 706 本体**：它们是**已 applied + 已记账 + 校验和已登记**
--     的迁移，改它们会重写已应用历史、打破 checksum 台账（AGENTS.md 纪律）。
--     与 886 / 877 订正注释用的是同一手法：**在新的迁移里用 COMMENT 订正**。
--   ★ 幂等：COMMENT 是幂等操作（重复执行结果相同）。
--   回滚：.down.sql 把注释**还原成 456/706 的原文**（还原语义 = 回到迁移前
--         的库状态，而不是「回到正确状态」）。
-- ===========================================================================

BEGIN;

-- COMMENT ON COLUMN 不拿强锁（只写 pg_description 一行）。
-- 但本文件正文提到 "DROP COLUMN"/"ALTER TABLE" 等字样，
-- startup_ddl_lock_guard_gate_test.go 的检测器按**正文**（含注释）匹配
-- 强锁 DDL 关键字 ⇒ 一并带上守卫，避免门因关键字命中而红。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- 守卫：对象不存在就跳过（不硬失败）——
-- session_turns / session_turns_hot 由 430/526 建，session_tools /
-- session_tools_hot 由 706 建；只有在「对象确实不在」的库里才可能缺席，
-- 那种库里这些注释也无从谈起 ⇒ NOTICE 后返回。
DO $comment_889_tools$
BEGIN
  IF to_regclass('public.session_turns') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_turns.tools IS
      'Tools invoked during this turn (JSON array). ★ 2026-10-20 起由写路径填充（审计 G-6，迁移 889）：
本列此前**从未被写过**（domains/session/v2/turn_writer.go 的 INSERT 列清单里没有它）
⇒ 每一行都停在默认值 ''[]''::jsonb ⇒ 视图 request_logs_with_current_month
投影出的 tool_calls 恒为空数组（不是「该请求没有工具调用」，是「没人写过这一列」）。
现在由 turn_writer.go 的 turnToolsJSONBExpr 在 INSERT 的**同一条语句**里用子查询
从 public.session_tools_hot 聚合（按 session_id + request_id），元素是
{tool_name, tool_call_id, status, duration_ms, error_type} 的摘要
（**不含** arguments/result —— 那两份正文在 session_bodies 与 session_tools 里）。
★ 历史行**不回填**（工具行可能已被 promote 搬走，回填值会不完整）。
原始注释（456）：Tools invoked during this turn (JSON array of tool_use records).';
  ELSE
    RAISE NOTICE '889: public.session_turns 不存在（表未建）⇒ 跳过 tools 列注释订正';
  END IF;

  IF to_regclass('public.session_turns_hot') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_turns_hot.tools IS
      'Tools invoked during this turn (JSON array). ★ 2026-10-20 起由写路径填充（审计 G-6，迁移 889）：
本列此前**从未被写过** ⇒ 每行都停在默认值 ''[]''::jsonb。
现在由 domains/session/v2/turn_writer.go 的 turnToolsJSONBExpr 在 INSERT 的
**同一条语句**里用子查询从 public.session_tools_hot 聚合（按 session_id + request_id），
元素是 {tool_name, tool_call_id, status, duration_ms, error_type} 的摘要
（**不含** arguments/result）。★ 历史行**不回填**。';
  ELSE
    RAISE NOTICE '889: public.session_turns_hot 不存在（表未建）⇒ 跳过 tools 列注释订正';
  END IF;

  IF to_regclass('public.session_tools') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_tools.turn_no IS
      '★ 2026-10-20 实测：本列**从未被写**（审计 G-6）。
写方 domains/toolexecution/postgres_store.go 的 INSERT 列清单里没有 turn_no
⇒ 100% 的行为 NULL。⇒ 任何按 (session_id, turn_no) 的 turn→tool join
**会丢掉全部行**（NULL 不等于任何值）；可用的连接键是 (session_id, request_id)
（两列都 NOT NULL 且有索引）。';
  ELSE
    RAISE NOTICE '889: public.session_tools 不存在（表未建）⇒ 跳过 turn_no 列注释订正';
  END IF;

  IF to_regclass('public.session_tools_hot') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_tools_hot.turn_no IS
      '★ 2026-10-20 实测：本列**从未被写**（审计 G-6）。
写方 domains/toolexecution/postgres_store.go 的 INSERT 列清单里没有 turn_no
⇒ 100% 的行为 NULL。⇒ 任何按 (session_id, turn_no) 的 turn→tool join
**会丢掉全部行**；可用的连接键是 (session_id, request_id)。';
  ELSE
    RAISE NOTICE '889: public.session_tools_hot 不存在（表未建）⇒ 跳过 turn_no 列注释订正';
  END IF;
END
$comment_889_tools$;

COMMIT;
