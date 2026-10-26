-- Migration 885: session_turns 加 host（网关实例身份）+ tags（轮次级标签数组）
--
-- -----------------------------------------------------------------------
-- 缺口本体（2026-10-20 审计 A5，实测非推理）
-- -----------------------------------------------------------------------
--   · **全族无 host/host_name/instance_id 列** —— 逐列核过 session_turns /
--     session_turns_hot / session_turn_details* / sessions：没有任何一列回答
--     「这轮是**哪台机器**写的」。同机多实例、跨机部署、灾备切换之后，
--     单看一行无法判断它来自哪台网关。
--   · **轮次级无 tags** —— 标签只到 session 级（public.session_tags，
--     迁移 351），turn 级没有标签位；而轮次上已经存在一堆分类事实
--     （source_kind / client_protocol / agent_type / task_type），
--     下游要按这些维度过滤时只能对十来个列做 OR。
--
-- -----------------------------------------------------------------------
-- 这条迁移做什么
-- -----------------------------------------------------------------------
-- 给**父表与 hot 表**各加两列：
--
--     host  TEXT        写这轮的网关实例身份
--     tags  TEXT[]      轮次级标签数组
--
-- ★ **必须两侧同形**：迁移 707 给 promote_session_turns_hot_to_partition
--   加了**列集合契约校验**（父表与 hot 的 (列名:类型:非空) 集合必须全等，
--   否则直接 RAISE EXCEPTION 'session_turns hot/parent column contract has
--   drifted'）。只加一侧 ⇒ promote **当场报错**，热表数据永远排不进月分区
--   （这就是「hot 表越滚越大」的形态）。所以这里两侧一次加齐，且类型/非空
--   属性完全一致。
--
-- promote 函数本身**不用改**：707 的搬行体是「目录派生列清单 + RETURNING h.*」
--   （707:330-348），对称加列自动流经 promote —— 这正是 707 头注释承诺的
--   「后续任何对称加列自动流经 promote，无需再改本函数」。
--
-- Compatibility: 纯可空加列，零回填（历史行的 host/tags 为 NULL）。
--   ⚠ NULL 与「写了但没值」在语义上不同：本迁移**不回填**历史行 ——
--   历史行的写方实例已经无法重建（当时可能有多个网关实例写过同一张表），
--   填一个猜出来的值比留 NULL 更坏。判读时以「非空即当期」为准。
--
-- down: DROP 这两列（父表与 hot 同时）—— promote 的目录派生清单对少列的
--   父表同样成立（两侧一起少，集合仍全等），故 down 不需要回滚函数。
--
-- 词表（写方在 domains/session/v2/turn_host_tags.go 实现）：
--   host = LLM_GATEWAY_INSTANCE_ID → hostname → "llm-gateway"
--          （internal/instanceid；与 cmd/gateway 的 dispatch 接线同一份实现）
--   tags = 显式传入优先；否则派生：
--          "session_turn"（命名空间标记，保证数组非空）
--          "src:<source_kind>" / "proto:<client_protocol>"
--          "agent:<agent_type>" / "task:<task_type>"

BEGIN;

-- ALTER TABLE ADD COLUMN 要拿 ACCESS EXCLUSIVE 锁。抢不到锁时**快速失败**
-- （整个迁移回滚，稍后重跑），而不是排在写流量后面把全库堵住 ——
-- 881/882 同款做法（installer 的 startup_ddl_lock_guard_gate_test.go 是这道
-- 约束的门）。
SET LOCAL lock_timeout = '5s';

-- =============================================
-- 1. 父表（分区表）
-- =============================================

ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS host TEXT,
    ADD COLUMN IF NOT EXISTS tags TEXT[] DEFAULT '{}'::TEXT[];

-- =============================================
-- 2. hot 表（与父表**完全同形** —— 707 的列集合契约要求）
-- =============================================

ALTER TABLE public.session_turns_hot
    ADD COLUMN IF NOT EXISTS host TEXT,
    ADD COLUMN IF NOT EXISTS tags TEXT[] DEFAULT '{}'::TEXT[];

-- =============================================
-- 3. 列注释（两侧同样一份）
-- =============================================

COMMENT ON COLUMN public.session_turns.host IS
    '写入该轮次的网关实例身份（LLM_GATEWAY_INSTANCE_ID → hostname → llm-gateway）。
     2026-10-20 由迁移 885 补齐（审计 A5：全族此前无任何主机/实例列）。
     历史行为 NULL（不回填：当时的写方实例已无法重建）。';

COMMENT ON COLUMN public.session_turns.tags IS
    '轮次级标签数组（2026-10-20 迁移 885 补齐，审计 A5：此前标签只到 session 级）。
     词表：session_turn（命名空间标记）/ src:<source_kind> / proto:<client_protocol>
     / agent:<agent_type> / task:<task_type>；显式传入优先。';

COMMENT ON COLUMN public.session_turns_hot.host IS
    '写入该轮次的网关实例身份（LLM_GATEWAY_INSTANCE_ID → hostname → llm-gateway）。
     2026-10-20 由迁移 885 补齐（审计 A5：全族此前无任何主机/实例列）。
     历史行为 NULL（不回填：当时的写方实例已无法重建）。';

COMMENT ON COLUMN public.session_turns_hot.tags IS
    '轮次级标签数组（2026-10-20 迁移 885 补齐，审计 A5：此前标签只到 session 级）。
     词表：session_turn（命名空间标记）/ src:<source_kind> / proto:<client_protocol>
     / agent:<agent_type> / task:<task_type>；显式传入优先。';

COMMIT;
