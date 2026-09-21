# D03（三层缓存 provenance）+D05（超长自动压缩）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论见轮文档 §一。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | window_source 与两个截断旗标未进 sessionv2mirror 白名单（switch 无 default）→ 镜像时静默丢弃；commit message 声称"白名单已知这些键"对此三键不成立 | internal/sessionv2mirror/hook.go:400-439；写入方 request_log_pipeline.go:1412,1419,1429,1444；读侧 cache_v2.go:759-773 | 补白名单三键+读侧字段（**已修 R36**） |
| 2 | P2 | P0-2 锁租约 5s 无续期，临界区含逐消息检测，租约过期后互斥静默失效（token 比对删除只防误删不防失效） | security/sanitize/smart_sani_guard.go:358；临界区 231→304→312 | 需真机耗时分布；至少补耗时/过期计数器（登记） |
| 3 | P3 | 锁降级与争抢超时仅 Debug 日志（生产 Info 不可见）；rand.Read 失败完全无日志 | smart_sani_guard.go:346-348,360-362,370-372 | 降级改 Warn（**已修 R36**） |
| 4 | P3 | mergeCompressionMetaV3 注释 "Producer-side facts win" 与行为（先到先得）相反 | request_log_pipeline.go:1366-1371 | 修注释（**已修 R36**） |
| 5 | P3 | >128KB 降级丢 *_truncated 旗标；旗标当前零 Go 消费者（仅 SQL 取证） | request_log_pipeline.go:1443-1448 | 登记口径 |
| 6 | P3 | 镜像 safeAlignmentRecords 全有或全无 + producer 可产空 hash → 一条空 hash 丢整张 map（预有严格性，非本窗引入） | hook.go:455-462；alignment.go:52-56 | 观察项 |
| 7 | P3 | executor 4xx tiered 压缩重试成功后 provenance 与最终 outbound 一次性错位（第一层 alignment + 第二层 cut_marker 并存） | executor_chat.go:1032-1070；context_summarize.go:1100-1112 | 语义注记 |
| 8 | 观察 | 写透覆盖面：Prepare 数据面唯一调用点 handler.go:3552-3591；responses 协议显式排除（=R35-gap 遗留#5，非新回归） | handler.go:3552-3591,3670-3672 | 无需动作 |

## 二、核实为健康的面
- P0-1 写端主链路单一写者、读写两侧键名/类型对齐；256 帽在截断前统计 window_source 计数完整
- 镜像三新字段有界校验正确（occurrence 0..4096、target_kind/target_space ≤24 词、hash 32/64 hex）
- P0-2 锁骨架正确：粒度一致、唯一写者在临界区内、无锁泄漏（WithoutCancel+TTL 兜底+token Lua）
- P1-1 口径一致（anthropic_stream.go:535 = anthropic_bridge:1537/:322）；sensitive 硬编码 false 有意
- P2-1 正则无误伤；存量宽模式 input.{0,30}limit 非本窗引入
- survival 决策聚合/gate.Discard 防重复输出与 attempt_outcome.go:381 一致

## 四、F2 取证（survival 层 KindContextLength 现状与插入点）
- (a) attempt_outcome.go:193 legacy 表 FailTerminal + errorsx/action_policy.go:242-254 centralActionForTaskWithHistory(:417-458) 443-444 短路不看 committed；消费端 survival_coordinator.go:476 聚合→renderTerminal
- (b) uncommitted 判定：execute_attempt.go:74 SafeRetry；attempt_outcome.go:331 committed := CommitState >= CommitStateContent
- (c) 已有 tiered 链仅接 HTTP 4xx：handleContextLengthRecovery（context_summarize.go:991-999）、RecoveryCoordinator.Recover（recovery_coordinator.go:108-200）、strategy.Runner.RunWithBody、CompressMessagesIfNeededBody、contextLengthRecoveryState 每层一次 + ctxLenRecoveryRetry 预算豁免；"60s 互斥"未定位到（文档表述存疑）
- (d) 插入点建议：纯策略改判不可行（同 body 再撞同窗口），必须 body 重写配对；备选为流中 outcome 折回 handleContextLengthRecovery（侵入大）
- **主代理裁决**：改判收进 SurvivalCoordinator.Run 本地（survivalCtxLenCompressRetryDue），不动共享决策层——durable recovery worker 复用聚合但无 body 重写钩子，决策层放行会空转烧 retry ceiling
