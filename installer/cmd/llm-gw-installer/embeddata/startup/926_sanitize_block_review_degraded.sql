-- 926: 拦截审核单区分「降级放行」与「硬阻断」（2026-10-30 输出合规审计轮）。
--
-- # 缺口本体（211 生产实测，2026-10-30）
--
-- 误报轮（921）把「守卫降级放行」（待检缓冲越界、无规则命中 ⇒ 放行并留痕）
-- 也登记成拦截审核单，与硬阻断共用同一张表、同一个 pending 状态 ⇒
--   ① 生产以 ~230 条/天的速度把审核列表灌成降级遥测（24h 内 226/226 条全是
--     builtin.guard.capacity_exceeded）⇒ 真正的拦截单被挤出首页；
--   ② 降级单挂着「批准/拒绝」按钮：请求已成功送达，批准写出的放行行
--     （规则=builtin.guard.capacity_exceeded）没有任何消费方（放行表只被
--     内容规则读面查询）⇒ 一个看起来能操作、实际什么都不做的死按钮；
--   ③ pending_count 把降级单计成「待审拦截」⇒ 徽标数字失真。
--
-- 迁移 917 对这张表的契约本来就是「每次 output/egress **硬阻断**一条」；
-- 降级不是硬阻断 ⇒ 本迁移把降级单标出来（degraded），让它回到遥测语义：
-- 列表仍可见（降级留痕是 921 的明确设计，不删），但
--   - 不计入 pending_count（待审数只数真拦截）；
--   - 读数（insights）不再把它当规则反馈的分母；
--   - 前端渲染成只读遥测行（无批准/拒绝/标注操作）。
--
-- # 回填口径与已知边界
--
-- 以规则前缀回填（builtin.guard.% = 守卫合成哨兵，policyBlockEvidenceKind
-- 对整个前缀一视同仁）。★ 边界：若某部署以 block 模式跑守卫
-- （LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE=block），其容量终断行**是**硬阻断、
-- 却会被本回填标成 degraded —— 当前所有在跑部署（211/245/154）均为出厂
-- release 模式 ⇒ 回填在本仓部署面上精确；block 模式部署如需把这类行恢复
-- 成可决，手工 UPDATE degraded=false 即可（行上证据与原因齐全）。
--
-- 全部幂等（ADD COLUMN IF NOT EXISTS / NOT degraded 守卫），纯新增列。

-- 锁守卫（startup_ddl_lock_guard_gate_test.go）：含 UPDATE 回填 ⇒ 必须自带
-- lock_timeout 与 statement_timeout（917/921 同款纪律）。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE sanitize_block_reviews
    ADD COLUMN IF NOT EXISTS degraded BOOLEAN NOT NULL DEFAULT false;

UPDATE sanitize_block_reviews
SET degraded = TRUE
WHERE rule_id LIKE 'builtin.guard.%' AND NOT degraded;
