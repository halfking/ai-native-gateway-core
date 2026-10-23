-- Migration 901: 给分区父表 public.usage_ledger 补 (api_key_id, ts DESC) 部分索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-23 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】https://llmgo.kxpms.cn/keys/73 使用统计面板报 "query failed"
--   （GET /api/usage/{id}、/api/usage/{id}/models、/api/usage/{id}/trend
--   在负载窗口整体超时；245 journal user_usage_stats.go:174 / usage.go
--   同族 500，154 同库 24h 内 context deadline exceeded 5,901 次）。
--
-- 【根因：usage_ledger 分区上没有任何以 api_key_id 为首列的索引】
--   生产共享库实测（pg_indexes）：usage_ledger_2026_09（742 MB）/
--   usage_ledger_2026_10（345 MB）只有四组索引 ——
--   request_id_ts（唯一）、request_id_ts_provider_id_prompt_token、
--   tenant_id_ts、ts_tenant_id_prompt_tokens_completion ——
--   **首列全是 request_id / tenant_id / ts，没有 api_key_id**。
--   而按 key 聚合的三个端点全部是
--     `WHERE api_key_id = $1 AND ts >= $2 AND ts < $3`。
--
--   EXPLAIN ANALYZE 实测（252 真库，2026-10-23）：对**7 天窗口内 0 行**的
--   api_key_id=73，规划器只能走
--   usage_ledger_2026_10_ts_tenant_id_prompt_tokens_completion_idx1
--   （Index Cond 只含 ts，api_key_id 沦为 Filter，每 worker 移除 64,822 行）
--   ⇒ **Execution Time: 6,983 ms 为「查一个空结果」买单**。
--   对照组 usage_ledger_hot **有** usage_ledger_hot_api_key_id_ts_idx
--   (api_key_id, ts DESC) WHERE api_key_id IS NOT NULL，同一查询在 hot 侧
--   走 Index Cond 全下推 ⇒ 父表分区侧就是缺这一条。
--
-- 【形态】照抄 hot 侧既有样板（逐列一致：(api_key_id, ts DESC) + 同谓词），
--   部分索引与 891 同族（谓词 api_key_id IS NOT NULL 为 IMMUTABLE 判定，
--   非 614 那种 now() 谓词雷）。
--
-- 【锁与时长】（891 同款语义）
--   普通 CREATE INDEX（分区父表不支持 CONCURRENTLY）持 SHARE 锁到构建完成
--   ⇒ 构建期间分区侧写被阻塞（读照常）。usage_ledger 是活写分区父表。
--   lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑
--   （882/878/876/891 同款；startup_ddl_lock_guard_gate_test 钉住强锁 DDL
--   必须带 lock_timeout）。
--   statement_timeout=10min：索引构建本身可能很久，而 lock_timeout 只管
--   锁等待（849/855/856/862/874/891 同款：共享 PG 服务端 statement_timeout
--   30s 会掐长索引构建，57014 击穿后迁移永不记账，成为永久卡点）。
--
-- 【幂等】IF NOT EXISTS。Down：按名删父表索引（父表 DROP 级联删分区侧
--   子索引，PG 按 pg_index.indisparent 关联，不按名字 grep）。
--
-- 【覆盖范围】分区父表建索引会自动传播到全部既有与未来分区
--   （2026_08/09/10/11/default）；usage_ledger_hot 是**独立表**、已有同款
--   索引，不受本迁移影响。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_usage_ledger_api_key_id_ts
    ON public.usage_ledger (api_key_id, ts DESC)
    WHERE api_key_id IS NOT NULL;

COMMIT;

-- POST_CONDITION:
--   SELECT indexname FROM pg_indexes
--    WHERE schemaname = 'public' AND indexname = 'idx_usage_ledger_api_key_id_ts';
--   -- 分区侧子索引按 pg_index.indisparent / 定义匹配核对（877 台账口径：
--   -- 不按名字 grep —— PG 用列名自动改名）。
--   -- 预期收益（EXPLAIN 口径）：api_key_id=73 的 7 天空窗查询从
--   --   Index Cond(ts)+Filter(api_key_id) 全窗扫描 6,983 ms
--   -- 降为 Index Cond((api_key_id, ts) 全下推) 亚毫秒级。
