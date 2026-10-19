-- 882_request_logs_cost_negative_partial_idx.sql
-- 2026-10-20：给 `_request_logs`（S4 退役改名的 request_logs 分区父表，860）
-- 建 `WHERE cost_usd < 0` 的部分索引 —— 根修
-- bg/routing_health_checks.go `recorded_cost_is_negative` 检查的重扫浪费
-- （R51 96h 审计轮 §3.5 登记的方案 (a)：该表已有同款部分索引先例 532）。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（2026-10-20 252 生产 24h 日志窗实测，非推理）
--
-- 该检查每 ~8-9 分钟跑一次（≈168 次/天），形态：
--     WITH neg AS (SELECT credential_id, outbound_model, count(*), sum(cost_usd), …
--                  FROM public._request_logs
--                  WHERE cost_usd < 0 AND ts > now() - interval '30 days'
--                  GROUP BY 1, 2)
-- 实测 24h 窗（10-19 06:04 → 10-20 06:04，pg-252-pg17 SQL 日志全量解析）：
--   • ≥1s 慢查 158 次 / 2,010s（mean 12.7s，max 29.6s）——
--     总耗时较 R51 轮（154 次 / 1,529s）再 +31%，连续第二轮抬头；
--   • 另有 34 次/天被 statement_timeout 击杀 —— 检查不仅慢，还**整条失败**
--     （路由健康报告里这条检查在那些 tick 里是 error，不是「无负成本」）。
--
-- 为什么它是纯浪费（三层证据）：
--   ① 源已冻结：`storage.request_logs_write_enabled=false`（2026-10-13 起），
--      `public._request_logs` 最新行停在 2026-10-13，30 天窗内结果不会再变；
--   ② 全表负成本行 = **0**（252 生产实测 count(*) WHERE cost_usd < 0 = 0，
--      代码注释里 10-06 时代的 1,628 条已随退役分区消失）——
--      检查现在每次都扫 ~43 万行只为返回空集；
--   ③ 写路径有守卫：cost 计算出负值时落 nil（"cannot compute"）而不是负数，
--      新负成本行结构上不该再出现（检查保留作 canary，覆盖 import/回填路径）。
--
-- -----------------------------------------------------------------------
-- 修法：父表级部分索引（自动级联 + 未来分区自动继承）
--
--   CREATE INDEX … ON public._request_logs (ts, credential_id, outbound_model)
--     INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
--     WHERE cost_usd < 0
--
--   • 键序：ts 在首位服务 30 天窗的范围谓词；credential_id/outbound_model
--     服务 GROUP BY；INCLUDE 四列让聚合 index-only（可见性地图全可见时）。
--   • 条目数 = 负成本行数 = 0 ⇒ 索引本体几页；构建只需对每个分区做**一次**
--     顺序扫描找匹配行（生产 5 个 heap 分区，秒级到十秒级）。
--   • 为什么建在父表而不是 532 式逐分区 DO 循环：父表上的分区索引会被
--     `CREATE TABLE … PARTITION OF` 自动继承（877 台账订正实测确认），
--     不需要再改 ensure_request_logs_partition；生产 5 个分区与 fresh-install
--     路径（01-schema ensure 的 CREATE TABLE…PARTITION OF 不带 USING columnar）
--     全部 heap（252 生产 relam=2 逐分区实测），不存在列存拒绝索引的问题。
--   • 写入侧：本表已停写，索引维护成本为零；若未来负成本行重新出现
--     （import/回填），检查变成 sub-ms 的索引探针，canary 语义不变。
--
-- 锁与时长：普通 CREATE INDEX（父表不支持 CONCURRENTLY）持 SHARE 锁到
-- 构建完成；本表无写入方，只有读者在构建期被阻塞（秒到十秒级）。
-- lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑（878/876 同款，
-- startup_ddl_lock_guard_gate_test 钉住强锁 DDL 必须带 lock_timeout）。
-- 幂等：IF NOT EXISTS。Down：父表 DROP INDEX 级联删除全部分区侧索引。
--
-- 台账/通道：canonical 与 installer embeddata 两副本 SHA 现场一致；
-- installer go:embed + map、dbinit StartupFiles、TSV、部署通道 files=()、
-- .down.sql 均已就位。契约门：migration_882_request_logs_cost_negative_partial_idx_test.go
-- （钉谓词/键序/INCLUDE/cross-pin 检查侧谓词未漂移）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_request_logs_cost_negative
    ON public._request_logs (ts, credential_id, outbound_model)
    INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
    WHERE cost_usd < 0;

COMMIT;

-- POST_CONDITION:
--   SELECT indexname FROM pg_indexes
--    WHERE tablename LIKE '_request_logs%' AND indexname = 'idx_request_logs_cost_negative';
--   -- 分区侧子索引按 pg_index.indisparent=t / 定义匹配核对（877 台账口径：不按名字 grep）
