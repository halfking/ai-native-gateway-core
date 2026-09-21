# 2026-09-01 24小时修正审计（第二轮）— 汇总报告

- 审计对象：`2026-08-31 07:20 ~ 2026-09-01 07:20 +0800` 的约 100 个提交（390 文件，+38338/-8786）。
- 方法：codegraph 代码图谱（5063 文件 / 216201 边）+ 主代理 + 5 子代理并行审计（IR/digest、队列并发、存储分区、厂商错误闭环、前端与代码结构），随后按文件所有权分区并行修正。
- 输入文档：`docs/audit/2026-08-31-24h-comprehensive-audit-final.md` 等 16 份 24h 内方案文档摘要。

## 一、总体结论

24h 内的修正质量整体良好：前轮 P0 全部闭环（IR json tags、636 digest ACL 保全、_to_be_deleted 清理、promote 幂等、fresh-install bootstrap）。本轮审计**未发现新的 P0 级代码缺陷在当轮修复中引入回归**，但确认了 **3 个跨轮遗留 P0** 与若干 P1，并全部完成本轮修正。

## 二、系统级问题与方案（本轮实施）

### S1. hot→分区数据闭环断链（P0，跨轮遗留）
`session_bodies_unified` 视图分区分支过滤 `partition_date <= CURRENT_DATE - 1`（625），而 writer 只写 hot（当日 partition_date）。promote 后行迁入父表即从两个分支同时消失，直到次日零点。`GetLatestBodies` 是 request_delta 去重基线 → 重放场景增量消息重复。
**方案**：迁移 637 去除视图过滤（promote 后 hot 行即删，无重复风险）；迁移 638 为 615/626 的 promote 函数补 retention=0 / batch_size guard（对齐 628 契约）；sessionsummary 两处直读父表改读 unified 视图；admin 手动 promote 拒绝 retention<=0 并补 session_bodies_hot 条目。

### S2. 供应商错误无法归因凭据（P0，产品诉求缺口）
`provider_error_details` 聚合表无 credential_id 维度，"凭据详情下的错误集合"（评估供应商服务质量）落空。
**方案**：迁移 639 加列并纳入聚合粒度；`getProviderErrorStats` 支持按凭据过滤；同时修复 provider_diagnose 的死代码分类（`Contains("401")` 等永不匹配）与 `eof_without_done` 良性路径 Kind 串空值污染 transient 统计。

### S3. 新错误 kind 未注册决策表（P1 埋雷）
`KindCircuitOpen` / `KindFpSlotSaturated` 落库但不在 `retryableActionKind`/`terminalActionKind`，`DecideNextAction` 走 default → `ActionFailClosed + unmapped_kind`；routeincident 3-streak 检测会把纯网关侧信号误当上游故障。
**方案**：注册进 action_policy（terminal 语义：前置拒绝不消耗上游重试）、ProjectRecovery、nonRoutingFailureKinds 排除表。

### S4. 恢复重试预算双标准（P1）
context-length 恢复的 attempt 退还只修了 chat 执行器，anthropic 原生路径仍会"恢复成功但从未发送"；`ParseContextLimitFromError` 的 "resulted in" 兜底把请求用量当模型上限持久化，永久污染裁剪配置。
**方案**：anthropic 执行器移植 ctxLenRecoveryRetry 退还；持久化仅限 primary 模式命中，兜底值只作本次压缩目标。

### S5. digest 与路由追踪的可观测缺口（P1）
digest 回退静默（无日志/指标）且 admin 端第二套算法按字节截断（与持久化层 rune 截断不一致）；首试成功不记 routing_attempts（零类样本系统性缺失）；session 聚合 outbox 死信无指标。
**方案**：回退加 Warn+counter；截断统一 rune-safe；首试成功写精简成功记录；死信复用 outbox 指标命名空间。

### S6. 死代码（P0 债务，零风险删除）
unifiedProbe 死链横跨 bg/main/executor 三层（scheduler 永为 nil）；v1 session turns handler 未挂载；58MB 编译产物入库。
**方案**：本轮直接删除（附引用核查证据），后续批次（Gin 死包、modelProbe 等）记入 docs/audit/2026-09-01-deadcode-cleanup-round2.md。

## 三、确认无需修复（抽样验证）

- 636 digest 迁移 ACL 保全（临时表+GRANT 循环）、promote 全列投影、51 列 parity 断言。
- 626/636 promote 幂等（ON CONFLICT + 按全键删实际插入行）、advisory 锁防双实例。
- WRR 平滑加权负载均衡 + weight-ratio 测试；Tier-0/1/2 三层 drainer 关闭路径补发 ErrShutdown。
- WriteFrame 信号量、VACUUM advisory 互斥、ConnectionRegistry 写槽位 fail-fast。
- 前端 24h 修复（内存泄漏、i18n 8 locales parity、DashboardViewLegacy 删除）无回归。

## 四、hot + 分区表全景（核查基准）

| 表 | hot 窗口 | promote | 更新/删除仅 hot | guard |
|---|---|---|---|---|
| request_logs_hot | 8h | 602 | 是 | 有 |
| session_turns_hot | 7d | 636 | 是 | 有 |
| session_bodies_hot | 8h | 615/626 | 是 | **无 → 638 补** |
| candidate_failure_logs_hot | 24h | 628 | 是 | 有 |
| request_logs_bodies_hot | 24h | 562(heap) | 是 | 无（Go floor 1h 兜底） |
| usage_ledger / request_wal / routing_decision_log / credential_model_index / credit_ledger / tool_usage_stats | 8h | 各自 columnar | 是 | 无（Go floor 兜底，低风险） |
| handoff_logs_hot | 8h | 534 | 是 | 有 |

## 五、遗留（下一轮，不阻塞本轮）

1. resume_blocked 长流中断：holdback 覆盖率 0%，需 L2 前缀续传/可见重启方案（.handoff/gateway-error-fix-phase2-20260901.md）。
2. sessionv2mirror shadow write 失败：252 缺唯一约束。
3. 前端 579 处硬编码中文、confirm 4 种实现、EmptyState/AppSpinner 组件收敛。
4. writeJSON 6 处重复收敛至 httpx；handler.go 8717 行拆分。
5. 非流式 DispatchNotice 无通道（本轮仅 metrics 补盲区在 Agent4 建议中，未实施）。
6. 1866-1868 日志增强未部署 245（部署事项）。
