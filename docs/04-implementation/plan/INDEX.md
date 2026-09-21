# 04-implementation/plan · 索引

> 最后更新：2026-08-27 05:00:00

## 请求记录与会话持久化重构（活跃线）

主链（按时间顺序阅读）：

| 文档 | 状态 | 说明 |
| --- | --- | --- |
| [2026-08-24-request-body-storage-optimization-assessment.md](2026-08-24-request-body-storage-optimization-assessment.md) | 评估 | 正文存储现状评估（前置输入） |
| [2026-08-24-request-body-storage-optimization-plan.md](2026-08-24-request-body-storage-optimization-plan.md) | 已吸收 | 正文存储优化早期方案（部分结论并入最终方案） |
| [2026-08-25-request-session-persistence-final-plan.md](2026-08-25-request-session-persistence-final-plan.md) | **执行中** | 总方案：owner 划分、Phase 0–6 路线、schema/cutover 约束（§5/§6 关键） |
| [2026-08-26-request-fact-phase0-contract.md](2026-08-26-request-fact-phase0-contract.md) | **已冻结** | Phase 0 契约：`internal/requestfact` 四版本常量（envelope/payload/codec/projection_event=1）、canonical JSON、payload hash、additive 容忍 |
| [2026-08-27-phase0-drift-metrics-inventory.md](2026-08-27-phase0-drift-metrics-inventory.md) | 本阶段产出 | V1/V2/body/stats 漂移只读对账 SQL、指标清单、failure matrix、迁移/installer/deploy manifest 核对 |
| [2026-08-27-projection-outbox-dispatcher-design.md](2026-08-27-projection-outbox-dispatcher-design.md) | 本阶段产出（设计） | Phase 3 设计：request_logs 主事务 body-free projection event、claim/lease/retry/DLQ/replay、post-persist dispatcher |

配套标准：[docs/standards/database-change-and-real-verification.md](../../standards/database-change-and-real-verification.md)（任何 schema/迁移变更必须走该流程；未注册迁移一律「待审核，禁止执行」）。

## 看板 / 编排（历史线）

| 文档 | 状态 | 说明 |
| --- | --- | --- |
| [2026-08-19-unified-auto-orchestration-plugin-execution-plan.md](2026-08-19-unified-auto-orchestration-plugin-execution-plan.md) | 已交付 | 统一自动编排插件执行计划 |
| [2026-08-20-dashboard-overview-node-optimization-plan.md](2026-08-20-dashboard-overview-node-optimization-plan.md) | 已交付 | 看板总览节点优化 |
| [2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md](2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md) | 已交付 | 节点卡片窗口 DnD overlay |
| [2026-08-22-dispatch-waterfall-relative-redesign-plan.md](2026-08-22-dispatch-waterfall-relative-redesign-plan.md) | 已交付 | 调度瀑布流相对值重设计 |
| [2026-08-24-session-queue-memory-optimization-plan.md](2026-08-24-session-queue-memory-optimization-plan.md) | 已交付 | 会话队列内存优化 |
