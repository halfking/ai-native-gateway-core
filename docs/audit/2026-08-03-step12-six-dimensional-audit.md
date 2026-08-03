# spec §12 六维独立审计报告

> 审计日期：2026-08-03
> 审计基线：origin/main @ `193189c7`
> 审计方式：只读代码审计 + 定向 -race/集成测试
> 关联 spec：docs/superpowers/specs/2026-07-27-request-flow-audit-design.md §12

## 总体结论

**发布就绪（READY），零 BLOCK。**

spec §13 的 11 项发布阻断条件全部 PASS 或 RESOLVED。生产默认 IR=off / URSM=off 不变；切换前须补齐 4 项非阻断可观测性 GAP（见下文）。

## 六维审计结果

### 维度 1 — 代码审计（owner 唯一性）— PASS

| 契约 | 结论 | 证据 |
|---|---|---|
| 身份 owner | PASS | 四入口 request_id 派生后初始化身份并回写响应头 |
| WAL owner | PASS | RequestLogger 单一：队列满 fallback、整批 fallback、Stop 幂等+drain |
| 终态 owner | PASS | SetTerminal CAS 单 winner；EmitFailure/EmitRateLimited 已接入；MarkLogged 含 CAS；DB L-2 guard 兜底 |
| 转换 owner | PASS | IR Parse→Preserve→Validate→Normalize→Serialize 全协议；conversion_path 记录 |
| 路由 owner | PASS | PlanCandidatesWithContext 单一入口；outer source 恰好记录一次 |
| 并发 owner | PASS | credentialstate per-key COW；breaker 独立无 Redis；NodeMirror 只读 |
| Session V2 owner | PASS | sessionv2mirror.PersistHook 唯一；pipeline hook no-op；turn/body 同事务 |

遗留非阻断 GAP：InitializeRequestIdentity 未生产收敛（行为等价）；IRScopedReporter 未接线（anomaly 不丢失，仅缺 per-request 归属）。

### 维度 2 — 数据审计（request_id 可关联性）— PASS

所有 sink（WAL、主日志、bodies、usage、decision log、Session V2 turns/bodies）均以 request_id 为主键可对账。L-1 位置不变量成立。G-ID-1/2 修复确认。重复 request_id 返回真实 turn_no。

### 维度 3 — 协议审计（官网 schema 比对）— PASS

F-1/F-2/F-3/F-5/E-1/D-1/D-2 全部 PASS。6 方向 golden fixture + 官网 schema 比对。Responses 解析器/序列化器补齐。全协议 E2E 9 组合 × 4 变体 PASS。

### 维度 4 — 并发审计 — PASS

-race 关键包 0 DATA RACE。重复终态/重复 replay/aggregate 幂等。真实 PG/Redis 故障注入 5 场景 PASS。shutdown 顺序正确。C-1 authoritative 零装配。

### 维度 5 — 运行审计（可观测性就绪度）— PASS（3 个非阻断 GAP）

核心可观测性就绪：镜像失败计数、fallback 环形缓冲、S-3 Prometheus metric。

切换前须补齐的 3 个非阻断 GAP：
1. WAL 7 个 atomic 计数器未暴露到 Prometheus/HTTP 端点。
2. Session V2 mirror 失败无 replay 队列/backlog（spec §6.3 要求）。
3. routing_state_source/conversion_path 未落 request_logs/decision_log 列（spec §8.2/§7.1 完整闭环）。

### 维度 6 — 目标一致性审计（§3.1.1 八类关注点）— PASS

八类关注点全部 PASS，无隐性遗漏。所有已知 GAP 均已显式声明延期。

## 切换前最小修复清单（非阻断，IR/authoritative 切换前完成）

1. WAL 计数器接 Prometheus collector 或 `/internal/wal/stats` 端点。
2. sessionv2mirror 失败行接入 replay 队列 + 暴露 backlog。
3. routing_state_source 在 router 记录点同步写入 RequestLogContext/DecisionLogEntry。
4. （可选）四入口统一切到 InitializeRequestIdentity；IR build 入口 NewIRScopedReporter 接线。
