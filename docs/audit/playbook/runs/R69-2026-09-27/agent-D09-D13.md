# D09+D13 probe/telemetry 子代理报告（窗口：092ab1b61..908256008）

> 主代理复核结论（R69 收口时回填）：发现#1 实锤→注释更正（5m lease + Workers=epWorkers 措辞，含发现#2）；发现#3 实锤→本轮 SQL 形状钉桩（recentSuccessRateExistsSQL 提常量 + TestRecentSuccessRateExistsSQLShape）；发现#5 实锤（语义恒等已论证）→本轮 F5 修复（热表臂补两谓词 + 测试正则更新）；发现#4 登记不处置（指标增设属 owner 权衡）。健康面维持。

聚焦提交：ff317a231（probe 门禁超时 3s→10s + proargtypes 修正）、595380023（claim promoted 臂补谓词）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **"30s lease" 注释漂移（窗口内引入）**：ff317a231 新增注释宣称 "10s is still far below the 30s lease"，但租约自 2026-08-18 起就是 `ProbeQueueLeaseDefault = 5 * time.Minute`（probe_service.go:44 明言 "the previous default was 30s"）。触发路径：读者按注释核 30s 预算关系找不到对应常量 | bg/probe_necessity.go:67-68；bg/probe_service.go:58、bg/probe_queue_worker.go:96-100、cmd/gateway/main.go:4201（真实租约=5m） | 注释改为 "5m lease (ProbeQueueLeaseDefault)" |
| 2 | P3（待复核） | **"单 worker 串行消费"前提与生产接线不符**：commit message 与注释均以"单 worker 串行"为论据；实际 `Workers: epWorkers`，`DefaultErrorProbeWorkers = 5`（max=5）。**功能上无缺陷**：每 worker 经 `Claim(ctx, 1, …)` 一次只取 1 任务，门禁只读证据采集，并发门禁间无共享可变态——但前提应修正，防止未来在门禁路径加非线程安全缓存 | cmd/gateway/main.go:4148,4199-4200；settings/spec_thresholds.go:39-41；bg/probe_queue_worker.go:158；bg/probe_service.go:323-324 | 改注释措辞或记档 |
| 3 | P3（待复核） | **ff317a231 双修均无钉桩回归测试**：`ensureRoutingRecentSuccessRate` 的 pg_proc 存在性检查 SQL 形状零钉桩——pronargtypes 拼写错误存活 09-23→09-27 正因无测试断言该 SQL | db/db.go:5664-5674（无测试引用，grep=0）；对照 595380023 有钉（final_success_claim_test.go:329-332） | 加形状钉（字符串断言 `proargtypes = '20 25 23 23'`） |
| 4 | P3（待复核，横向可观测性） | **门禁证据错误/超预算仅 Debug 级日志、无指标**：3s 时代的门禁超时在应用侧完全不可见（无 counter、Debug 默认不输出）。同族：`ensureRoutingRecentSuccessRate` 目录探测出错静默置 fnMissing=1 无日志。降级方向均正确但不可观测 | bg/probe_service.go:332-333；bg/probe_necessity.go:235-237；db/db.go:5672-5674；对照 skip 有 counter（probe_necessity.go:91-97） | 增设 gate_error_total 或把门禁不可用日志升 Warn；收益/噪音权衡请主代理裁决 |
| 5 | P3（待复核，需真机） | **同一 claim 语句的热表臂未享受同款谓词解锁**：热表臂 `NOT EXISTS (… request_logs_hot other …)` 缺 `gw_session_id IS NOT NULL AND <> ''` 两谓词，`uq_request_logs_hot_final_success_session` 谓词形状相同时蕴含不可证明。缓解因素：hot 表受 8h 保留约束；该臂与外层单行相关、外层已保证非空非空串（补谓词语义重言）。是否已有坏计划需 252 EXPLAIN 实证 | domains/hooks/observability/telemetry/client.go:2620-2626（热表臂）；sql/migrations/startup/532_request_logs_final_success.sql:77-81（hot partial 索引谓词） | 语义恒等已论证，照搬同款字面谓词补齐 |

## 二、核实为健康的面

1. **proargtypes 修复正确（目录学核验通过）**——pg_proc 目录确认不存在 `pronargtypes`；`proargtypes oidvector` = '20 25 23 23'（20=int8/25=text/23=int4）与 db/db.go:5710-5713 及 migration 406:67-71 签名逐位吻合；RETURNS TABLE 的 OUT 参数不计入 proargtypes，4 元素正确。
2. **错误分支与行为链闭环**——存在性检查出错 → fnMissing=1 保守路径（含 DROP），自含幂等；修复前 typo → 每 boot 每实例 DROP FUNCTION ×2 再 CREATE；调用方唯一（db/db.go:359），每 boot 一次。**每 boot DROP 抖动确认消除**。
3. **门禁 10s 预算链成立**——Claim 时 lease_until=now()+5m（bg/probe_queue.go:648）；门禁在 heartbeat 启动前运行（probe_service.go:327-343 → :370），10s 仅占租约 2%；心跳每 60s 续 5m（:68,672-679）；RequeueExpiredLeases 只回收过租约（probe_queue.go:779-782）。超预算→fail-open 跑探测（:331-333），无状态写、不判死亡；context 取消传播正确。
4. **门禁并发安全（5 worker 下）**——skip 移除以租约守卫的队列行 DELETE 为所有权证明（probe_necessity.go:489-520）；门禁只读，无共享内存态。
5. **telemetry claim 修复谓词与索引严格匹配**——新臂与 partial 唯一索引谓词字面全等（client.go:2596-2597 vs migration 532:101/146）；投影仅剩索引列，index-only 不回表。
6. **双臂计数守恒**——外层 WHERE 要求 COALESCE(gw_session_id,'')<>''（:2619）⇒ 半连接等值右值非 NULL 非空串；臂内 NULL/'' 行本就永不匹配，剔除是语义重言，claim 结果集不变。
7. **测试钉桩到位**——TestClaimSessionFinalSuccess_HeapPartitionsGuard 逐分区断言完整臂文本（final_success_claim_test.go:329-332）。
8. **降级行为与既有惯例一致**——claim 任何错误 → savepoint 回滚 + Info/Warn，业务行不丢、主写路径不阻塞（client.go:2628-2646）；与门禁 fail-open、ensure 保守路径三者口径统一。

## 三、未覆盖项与原因

- 252 真库 EXPLAIN 复现验证——需生产库凭据与真机。
- 新月度分区 create 路径是否自动携带 partial 索引——窗口外面，风险趋零未追。
- heapRequestLogsPartitions 5 分钟缓存的月初边界推演——窗口外既有逻辑。
- 运行时指标实测——需真机抓取。
- "2× probe-execution budget" 引用物——全仓未找到该常量（探测轮超时 direct/gateway 各 15s），疑为修辞；承重不变量（10s ≪ 5m 租约）已独立核验。
