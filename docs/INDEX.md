# LLM Gateway 文档主题索引

> 本索引按主题/功能组织，方便快速定位。与 [README.md](./README.md) 的目录结构导览配套使用。

> 🧹 2026-09-07：根目录过程文档已按规范整理（详见 [README.md](./README.md#-2026-09-07-根目录文档整理)），docs/ 重组为 01-requirements～07-reporting 编号结构。2026-09-07 重建本索引：全部链接已按实际目录逐一校验并更新为规范路径。

---

## 1. 入门（Start Here）

| 主题 | 文档 |
|---|---|
| 项目总览 | [PROJECT_OVERVIEW.md](./PROJECT_OVERVIEW.md) |
| 功能模块指南 | [MODULES_GUIDE.md](./MODULES_GUIDE.md) |
| 快速参考手册 | [QUICK_REFERENCE.md](./QUICK_REFERENCE.md) |
| 快速启动 | [06-deployment/04-runbooks/operations/QUICKSTART.md](./06-deployment/04-runbooks/operations/QUICKSTART.md) |
| 用户指南 | [04-implementation/deliverables/user-guide/session-management.md](./04-implementation/deliverables/user-guide/session-management.md) |
| 常见问题 / 故障排查 | [troubleshooting/](./troubleshooting/) |
| 升级指南 | [06-deployment/04-runbooks/operations/UPGRADE.md](./06-deployment/04-runbooks/operations/UPGRADE.md) |

## 2. 架构与设计

### 2.1 顶层架构

| 文档 | 说明 |
|---|---|
| [03-design/01-architecture/architecture/ARCHITECTURE.md](./03-design/01-architecture/architecture/ARCHITECTURE.md) | 系统架构总览 |
| [03-design/01-architecture/architecture/REPO_LAYOUT.md](./03-design/01-architecture/architecture/REPO_LAYOUT.md) | **仓库布局权威地图**（顶层目录用途+入位规则） |
| [03-design/01-architecture/architecture/API.md](./03-design/01-architecture/architecture/API.md) | API 入口文档 |
| [03-design/01-architecture/architecture/architecture-decisions.md](./03-design/01-architecture/architecture/architecture-decisions.md) | 架构决策汇总 |
| [03-design/01-architecture/architecture/ARCHITECTURE_REFACTOR_GUIDE.md](./03-design/01-architecture/architecture/ARCHITECTURE_REFACTOR_GUIDE.md) | 架构重构指南 |
| [03-design/01-architecture/architecture/routing-domain.md](./03-design/01-architecture/architecture/routing-domain.md) | 路由域设计 |
| [03-design/01-architecture/architecture/node-probe-mechanism.md](./03-design/01-architecture/architecture/node-probe-mechanism.md) | 节点探活机制 |
| [03-design/01-architecture/architecture/routing-and-state.md](./03-design/01-architecture/architecture/routing-and-state.md) | 路由与状态管理 |
| [03-design/01-architecture/architecture/runtime-request-flow.md](./03-design/01-architecture/architecture/runtime-request-flow.md) | 运行时请求流程 |

### 2.2 ADR（Architecture Decision Records）

- [adr/ADR-0001-handoff-goal-state-at-rest-encryption.md](./adr/ADR-0001-handoff-goal-state-at-rest-encryption.md) — 目标交接状态加密
- [adr/ADR-0002-target-go-package-layout.md](./adr/ADR-0002-target-go-package-layout.md) — Go 包分层迁移路线（分波次）
- [adr/2026-08-20-jwks-migration.md](./adr/2026-08-20-jwks-migration.md) — JWKS 迁移决策
- [adr/2026-08-23-step7-raw-bytes-fallback.md](./adr/2026-08-23-step7-raw-bytes-fallback.md) — 原始字节回退机制
- [adr/2026-08-28-request-action-policy.md](./adr/2026-08-28-request-action-policy.md) — 请求动作策略
- [adr/2026-08-28-requestjourney-journal-snapshot.md](./adr/2026-08-28-requestjourney-journal-snapshot.md) — 请求旅程日志快照
- [adr/2026-08-29-success-empty-response-body.md](./adr/2026-08-29-success-empty-response-body.md) — 成功响应空体处理
- [adr/2026-09-04-plugin-runtime-v2-capability-lifecycle.md](./adr/2026-09-04-plugin-runtime-v2-capability-lifecycle.md) — 插件运行时 V2 能力生命周期
- [adr/2026-09-05-dual-mode-storage-package-layout.md](./adr/2026-09-05-dual-mode-storage-package-layout.md) — 双模式存储包布局

### 2.3 集成 / 接口

| 文档 | 说明 |
|---|---|
| [03-design/01-architecture/architecture/a2a-spec-2027.md](./03-design/01-architecture/architecture/a2a-spec-2027.md) | A2A 协议规范 |
| [03-design/01-architecture/architecture/apihub-design.md](./03-design/01-architecture/architecture/apihub-design.md) | APIHub 设计 |
| [03-design/01-architecture/architecture/admin-api-authentication.md](./03-design/01-architecture/architecture/admin-api-authentication.md) | 管理 API 鉴权 |
| [03-design/01-architecture/architecture/attachment-auth-integration.md](./03-design/01-architecture/architecture/attachment-auth-integration.md) | 附件鉴权集成 |
| [03-design/01-architecture/architecture/adaptive-response-format-converter.md](./03-design/01-architecture/architecture/adaptive-response-format-converter.md) | 自适应响应格式转换 |
| [03-design/01-architecture/architecture/armor-sdp-feasibility.md](./03-design/01-architecture/architecture/armor-sdp-feasibility.md) | ARMOR SDP 可行性 |
| [03-design/01-architecture/architecture/omniroute-integration-boundary.md](./03-design/01-architecture/architecture/omniroute-integration-boundary.md) | Omniroute 集成边界 |

### 2.4 OpenAPI / API 契约

- [03-design/03-interface-design/api-yaml/ops-platform-openapi.yaml](./03-design/03-interface-design/api-yaml/ops-platform-openapi.yaml) — 运维平台 API 契约
- [03-design/03-interface-design/api-yaml/session-analytics.yaml](./03-design/03-interface-design/api-yaml/session-analytics.yaml) — 会话分析 API 契约
- [03-design/03-interface-design/api-yaml/llm-gateway-routing-admin.yaml](./03-design/03-interface-design/api-yaml/llm-gateway-routing-admin.yaml) — 路由管理 API 契约

### 2.5 领域注册表

- [03-design/01-architecture/domains/DOMAIN_REGISTRY.md](./03-design/01-architecture/domains/DOMAIN_REGISTRY.md) — 领域注册

## 3. 功能设计

### 3.1 模型质量监控

| 文档 | 说明 |
|---|---|
| [03-design/02-feature-design/model-quality/README.md](./03-design/02-feature-design/model-quality/README.md) | 模型质量监控总览 |
| [03-design/02-feature-design/model-quality/FEATURED_MODELS_GUIDE.md](./03-design/02-feature-design/model-quality/FEATURED_MODELS_GUIDE.md) | 特色模型指南 |
| [03-design/02-feature-design/model-quality/GATEWAY_INTEGRATION.md](./03-design/02-feature-design/model-quality/GATEWAY_INTEGRATION.md) | 网关集成说明 |
| [03-design/02-feature-design/model-iq/01-design.md](./03-design/02-feature-design/model-iq/01-design.md) | 模型评估（Model-iQ）设计 |

### 3.2 核心模块

| 模块 | 文档 |
|---|---|
| 会话治理 | [03-design/01-architecture/architecture/routing-domain.md](./03-design/01-architecture/architecture/routing-domain.md) |
| Memora | [03-design/02-feature-design/modules/memora.md](./03-design/02-feature-design/modules/memora.md) |
| 安全引擎 | [03-design/02-feature-design/modules/security-engine.md](./03-design/02-feature-design/modules/security-engine.md) |
| 会话检查器 | [03-design/02-feature-design/modules/session-inspector.md](./03-design/02-feature-design/modules/session-inspector.md) · [03-design/02-feature-design/modules/session-inspector-flowchart.md](./03-design/02-feature-design/modules/session-inspector-flowchart.md) |
| UA-TLS 伪装 | [03-design/02-feature-design/features/ua-tls-disguise-module-summary.md](./03-design/02-feature-design/features/ua-tls-disguise-module-summary.md) |

### 3.3 详细设计

| 主题 | 文档 |
|---|---|
| 路由自选择 | [03-design/02-feature-design/design/AUTO_SELECTION_SPEC.md](./03-design/02-feature-design/design/AUTO_SELECTION_SPEC.md) · [03-design/02-feature-design/design/AUTO_SELECTION_IMPLEMENTATION_PLAN.md](./03-design/02-feature-design/design/AUTO_SELECTION_IMPLEMENTATION_PLAN.md) |
| 通道质量路由 | [03-design/02-feature-design/design/CHANNEL_QUALITY_ROUTING_DESIGN.md](./03-design/02-feature-design/design/CHANNEL_QUALITY_ROUTING_DESIGN.md) |
| 列存 Body 切分 | [03-design/02-feature-design/design/COLUMNAR_BODY_SPLIT_PLAN.md](./03-design/02-feature-design/design/COLUMNAR_BODY_SPLIT_PLAN.md) |
| 仪表盘 V2 | [03-design/02-feature-design/design/DASHBOARD_API.md](./03-design/02-feature-design/design/DASHBOARD_API.md) · [03-design/02-feature-design/design/DASHBOARD_V2_IMPLEMENTATION.md](./03-design/02-feature-design/design/DASHBOARD_V2_IMPLEMENTATION.md) · [03-design/02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md](./03-design/02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md) |
| 自适应超时 | [03-design/02-feature-design/design/adaptive-timeout-strategy.md](./03-design/02-feature-design/design/adaptive-timeout-strategy.md) |
| P1 优化计划 | [03-design/02-feature-design/design/p1-optimization-plan.md](./03-design/02-feature-design/design/p1-optimization-plan.md) |
| Pre-request 校验钩子 | [03-design/02-feature-design/design/pre-request-validation-hook.md](./03-design/02-feature-design/design/pre-request-validation-hook.md) |
| 实时请求流修复 | [03-design/02-feature-design/design/realtime-request-stream-fix.md](./03-design/02-feature-design/design/realtime-request-stream-fix.md) |
| 请求追踪系统 | [03-design/02-feature-design/design/request-trace-system.md](./03-design/02-feature-design/design/request-trace-system.md) |
| 统一凭据状态钩子 | [03-design/02-feature-design/design/unified-credential-state-hook.md](./03-design/02-feature-design/design/unified-credential-state-hook.md) |
| 超时重试优化 | [03-design/02-feature-design/design/timeout-retry-optimization/](./03-design/02-feature-design/design/timeout-retry-optimization/) |
| 三层缓存速查 | [03-design/02-feature-design/design/QUICK-REF-THREE-TIER-CACHE.md](./03-design/02-feature-design/design/QUICK-REF-THREE-TIER-CACHE.md) |
| 路由尝试跟踪 | [03-design/02-feature-design/design/routing-attempts-tracking/](./03-design/02-feature-design/design/routing-attempts-tracking/) |
| 自动路由反馈优化 | [03-design/02-feature-design/design/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md](./03-design/02-feature-design/design/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md) |
| 管理注册表视图 | [03-design/02-feature-design/design/admin-registry-views.md](./03-design/02-feature-design/design/admin-registry-views.md) |
| 凭据控制台交接 | [03-design/02-feature-design/design/CREDENTIAL_CONSOLE_HANDOFF.md](./03-design/02-feature-design/design/CREDENTIAL_CONSOLE_HANDOFF.md) |

### 3.4 自动模型优化

- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V2.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V2.md) — V2 设计
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md) — V3 计划
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_README.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_README.md) — V3 说明
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_QUICKREF.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_QUICKREF.md) — V3 速查
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md) — V3 执行
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_SUMMARY.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_SUMMARY.md) — V3 总结
- [03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_COMPLETION_REPORT.md](./03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_COMPLETION_REPORT.md) — V3 完成报告

## 4. 数据设计

### 4.1 分区表管理

| 文档 | 说明 |
|---|---|
| [03-design/04-data-design/partition/README.md](./03-design/04-data-design/partition/README.md) | 分区表总览 |
| [03-design/04-data-design/partition/partition-architecture.md](./03-design/04-data-design/partition/partition-architecture.md) | 分区架构 |
| [03-design/04-data-design/partition/partition-standards.md](./03-design/04-data-design/partition/partition-standards.md) | 分区标准 |
| [03-design/04-data-design/partition/partition-test-cases.md](./03-design/04-data-design/partition/partition-test-cases.md) | 测试用例 |
| [03-design/04-data-design/partition/OPERATIONS_RUNBOOK.md](./03-design/04-data-design/partition/OPERATIONS_RUNBOOK.md) | 运维手册 |
| [03-design/04-data-design/partition/HOT_TABLE_OPTIMIZATION.md](./03-design/04-data-design/partition/HOT_TABLE_OPTIMIZATION.md) | 热表优化 |
| [03-design/04-data-design/partition/MIGRATION_341_TEST_CHECKLIST.md](./03-design/04-data-design/partition/MIGRATION_341_TEST_CHECKLIST.md) | 迁移测试检查清单 |
| [03-design/04-data-design/partition/MONTHLY_CHECKLIST.md](./03-design/04-data-design/partition/MONTHLY_CHECKLIST.md) | 月度检查清单 |
| [03-design/04-data-design/partition/IMPLEMENTATION_NOTES.md](./03-design/04-data-design/partition/IMPLEMENTATION_NOTES.md) | 实施笔记 |
| [03-design/04-data-design/partition/QUERY_PERFORMANCE_ANALYSIS.md](./03-design/04-data-design/partition/QUERY_PERFORMANCE_ANALYSIS.md) | 查询性能分析 |
| [03-design/04-data-design/partition/partition-background.md](./03-design/04-data-design/partition/partition-background.md) | 背景知识 |

### 4.2 迁移与模型目录

- [03-design/04-data-design/migrations/r1.13-security-config.md](./03-design/04-data-design/migrations/r1.13-security-config.md) — R1.13 安全配置迁移
- [03-design/04-data-design/model-catalog/README.md](./03-design/04-data-design/model-catalog/README.md) — 模型目录

### 4.3 数据治理

- [03-design/04-data-design/governance/candidate-failure-logs-252-governance-2026-08-17.md](./03-design/04-data-design/governance/candidate-failure-logs-252-governance-2026-08-17.md) — 候选失败日志治理

## 5. 价格

| 文档 | 说明 |
|---|---|
| [02-resources/research/pricing/README.md](./02-resources/research/pricing/README.md) | 价格数据总览 |
| [02-resources/research/pricing/2026-06-12-llm-pricing.md](./02-resources/research/pricing/2026-06-12-llm-pricing.md) | LLM 价格快照 |
| [02-resources/research/pricing/2026-06-12-diff.md](./02-resources/research/pricing/2026-06-12-diff.md) | 价格差异对比 |
| [02-resources/research/pricing/scripts/](./02-resources/research/pricing/scripts/) | 价格导入脚本 |
| [02-resources/research/pricing/raw/](./02-resources/research/pricing/raw/) | 原始价格数据（按厂商） |
| [02-resources/research/pricing/2026-06-12-tier2/](./02-resources/research/pricing/2026-06-12-tier2/) | Tier2 定价 |

## 6. 安全 / 合规

| 文档 | 说明 |
|---|---|
| [03-design/05-security-design/security/SECURITY_FEATURES.md](./03-design/05-security-design/security/SECURITY_FEATURES.md) | 安全特性 |
| [03-design/05-security-design/security/SECURITY-AUDIT-INDEX.md](./03-design/05-security-design/security/SECURITY-AUDIT-INDEX.md) | 安全审计索引 |
| [03-design/05-security-design/security/VIBECODING_GUIDELINES.md](./03-design/05-security-design/security/VIBECODING_GUIDELINES.md) | VibeCoding 规范 |
| [02-resources/compliance/legal/disguise-compliance.md](./02-resources/compliance/legal/disguise-compliance.md) | 伪装合规 |

## 7. 测试 / 验证

### 7.1 测试计划

| 文档 | 说明 |
|---|---|
| [05-testing/02-test-plans/testing/comprehensive-test-plan.md](./05-testing/02-test-plans/testing/comprehensive-test-plan.md) | 综合测试计划 |
| [05-testing/02-test-plans/testing/ROUTING_TEST_PLAN.md](./05-testing/02-test-plans/testing/ROUTING_TEST_PLAN.md) | 路由测试计划 |
| [05-testing/02-test-plans/testing/three-env-unified-verification.md](./05-testing/02-test-plans/testing/three-env-unified-verification.md) | 三环境统一验证 |
| [05-testing/02-test-plans/TP-CLIENT-GOAL-SIGNALS.md](./05-testing/02-test-plans/TP-CLIENT-GOAL-SIGNALS.md) | 客户端目标信号测试计划 |
| [05-testing/02-test-plans/会话优化v4/客户端会话保持-测试方案与用例.md](./05-testing/02-test-plans/会话优化v4/客户端会话保持-测试方案与用例.md) | 客户端会话保持测试 |

### 7.2 测试用例

- [05-testing/03-test-cases/TC-GOAL-CONTINUE.md](./05-testing/03-test-cases/TC-GOAL-CONTINUE.md) — Goal Continue 测试用例
- [05-testing/03-test-cases/TC-GOAL-HANDOFF.md](./05-testing/03-test-cases/TC-GOAL-HANDOFF.md) — Goal Handoff 测试用例

### 7.3 测试工具与框架

- [05-testing/e2e-quick-reference.md](./05-testing/e2e-quick-reference.md) — E2E 快速参考
- [05-testing/mock-testing-framework.md](./05-testing/mock-testing-framework.md) — Mock 测试框架
- [05-testing/01-strategy/test-matrix.md](./05-testing/01-strategy/test-matrix.md) — 测试矩阵

## 8. 部署 / 迁移

### 8.1 部署指南

| 文档 | 说明 |
|---|---|
| [06-deployment/01-environments/deployment/DEPLOYMENT_GUIDE.md](./06-deployment/01-environments/deployment/DEPLOYMENT_GUIDE.md) | 部署指南 |
| [06-deployment/01-environments/deployment/DEPLOYMENT_RULES.md](./06-deployment/01-environments/deployment/DEPLOYMENT_RULES.md) | 部署规则 |
| [06-deployment/01-environments/deployment/CUSTOMER-DEPLOY-GUIDE.md](./06-deployment/01-environments/deployment/CUSTOMER-DEPLOY-GUIDE.md) | 客户部署指南 |
| [06-deployment/01-environments/deployment/LOCAL-HOST-DEPLOY.md](./06-deployment/01-environments/deployment/LOCAL-HOST-DEPLOY.md) | 本地主机部署 |
| [06-deployment/01-environments/deployment/AUTO_CONTROL_DEPLOYMENT_20260701.md](./06-deployment/01-environments/deployment/AUTO_CONTROL_DEPLOYMENT_20260701.md) | 自动控制部署 |

### 8.2 仪表盘 V2 部署

- [06-deployment/01-environments/deployment/DASHBOARD_V2_DEPLOYMENT.md](./06-deployment/01-environments/deployment/DASHBOARD_V2_DEPLOYMENT.md) — 仪表盘 V2 部署
- [06-deployment/01-environments/deployment/DASHBOARD_V2_QUICKSTART.md](./06-deployment/01-environments/deployment/DASHBOARD_V2_QUICKSTART.md) — 仪表盘 V2 快速启动
- [06-deployment/01-environments/deployment/DASHBOARD_V2_VERIFICATION.md](./06-deployment/01-environments/deployment/DASHBOARD_V2_VERIFICATION.md) — 仪表盘 V2 验证

### 8.3 数据库环境

- [06-deployment/01-environments/deployment/DATABASE-ENVIRONMENT-SEPARATION.md](./06-deployment/01-environments/deployment/DATABASE-ENVIRONMENT-SEPARATION.md) — 数据库环境分离
- [06-deployment/01-environments/deployment/database-migration-fix.md](./06-deployment/01-environments/deployment/database-migration-fix.md) — 数据库迁移修复
- [06-deployment/02-database/local-pg-sync-from-252.md](./06-deployment/02-database/local-pg-sync-from-252.md) — 本地 PG 同步
- [06-deployment/2026-09-database-revision-sequence.md](./06-deployment/2026-09-database-revision-sequence.md) — 数据库修订序列

### 8.4 零停机设计

- [03-design/01-architecture/deploy-zero-downtime/zero-downtime-design.md](./03-design/01-architecture/deploy-zero-downtime/zero-downtime-design.md) — 零停机设计

### 8.5 客户安装指南

- [06-deployment/01-environments/customer-install/README.md](./06-deployment/01-environments/customer-install/README.md) — 客户安装总览
- [06-deployment/01-environments/customer-install/linux-docker.md](./06-deployment/01-environments/customer-install/linux-docker.md) — Linux Docker
- [06-deployment/01-environments/customer-install/linux-host.md](./06-deployment/01-environments/customer-install/linux-host.md) — Linux 主机
- [06-deployment/01-environments/customer-install/macos-docker.md](./06-deployment/01-environments/customer-install/macos-docker.md) — macOS Docker
- [06-deployment/01-environments/customer-install/macos-host.md](./06-deployment/01-environments/customer-install/macos-host.md) — macOS 主机
- [06-deployment/01-environments/customer-install/windows-host.md](./06-deployment/01-environments/customer-install/windows-host.md) — Windows 主机

### 8.6 安装布局

- [06-deployment/02-design/install-layout.md](./06-deployment/02-design/install-layout.md) — 安装布局设计

## 9. 运维 / Runbook

### 9.1 运维手册

| 文档 | 说明 |
|---|---|
| [06-deployment/04-runbooks/operations/OPERATIONS.md](./06-deployment/04-runbooks/operations/OPERATIONS.md) | 运维手册 |
| [06-deployment/04-runbooks/operations/UPGRADE.md](./06-deployment/04-runbooks/operations/UPGRADE.md) | 升级指南 |
| [06-deployment/04-runbooks/operations/QUICKSTART.md](./06-deployment/04-runbooks/operations/QUICKSTART.md) | 快速启动 |
| [06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md](./06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) | 仓库镜像策略 |
| [06-deployment/04-runbooks/operations/TEST_REPORT_20260701.md](./06-deployment/04-runbooks/operations/TEST_REPORT_20260701.md) | 测试报告 |

### 9.2 TODO 索引

- [06-deployment/04-runbooks/operations/TODO_INDEX.md](./06-deployment/04-runbooks/operations/TODO_INDEX.md) — TODO 索引
- [06-deployment/04-runbooks/operations/TODO_APPROVAL_INTEGRATION.md](./06-deployment/04-runbooks/operations/TODO_APPROVAL_INTEGRATION.md) — TODO 审批集成
- [06-deployment/04-runbooks/operations/TODO_COMMUNITY_MODE.md](./06-deployment/04-runbooks/operations/TODO_COMMUNITY_MODE.md) — TODO 社区模式
- [06-deployment/04-runbooks/operations/TODO_MEMORY_DLQ.md](./06-deployment/04-runbooks/operations/TODO_MEMORY_DLQ.md) — TODO 内存 DLQ

### 9.3 运维操作

- [operations/154-service-status-and-monitoring.md](./operations/154-service-status-and-monitoring.md) — 154 服务状态监控
- [operations/context-window-override-guide.md](./operations/context-window-override-guide.md) — 上下文窗口覆盖指南
- [operations/context-window-quick-guide.md](./operations/context-window-quick-guide.md) — 上下文窗口快速指南
- [operations/release-checklist.md](./operations/release-checklist.md) — 发布检查清单
- [operations/screenshot-guide.md](./operations/screenshot-guide.md) — 截图指南
- [operations/vacuum-worker-and-error-stats-guide.md](./operations/vacuum-worker-and-error-stats-guide.md) — Vacuum Worker 和错误统计指南
- [operations/p2.2-operations-manual.md](./operations/p2.2-operations-manual.md) — P2.2 操作手册
- [operations/proxy-ops-manual.md](./operations/proxy-ops-manual.md) — 代理运维手册

### 9.4 Runbooks

- [runbooks/auto-route-structured-features.md](./runbooks/auto-route-structured-features.md) — 自动路由结构化特性
- [runbooks/empty-response-routing.md](./runbooks/empty-response-routing.md) — 空响应路由
- [runbooks/ml-routing-rollback.md](./runbooks/ml-routing-rollback.md) — ML 路由回滚
- [runbooks/stats-reconciliation-rollout.md](./runbooks/stats-reconciliation-rollout.md) — 统计对账上线
- [runbooks/telemetry-sanitize-discarded.md](./runbooks/telemetry-sanitize-discarded.md) — 遥测清理丢弃数据
- [06-deployment/04-runbooks/daemon-watchdog.md](./06-deployment/04-runbooks/daemon-watchdog.md) — 守护进程 Watchdog
- [06-deployment/04-runbooks/journal-provider-next-phase.md](./06-deployment/04-runbooks/journal-provider-next-phase.md) — 日志提供者下一阶段
- [06-deployment/04-runbooks/local-k3s-observability.md](./06-deployment/04-runbooks/local-k3s-observability.md) — 本地 K3s 可观测性

### 9.5 故障排查

- [troubleshooting/credential-check-quick-fix.md](./troubleshooting/credential-check-quick-fix.md) — 凭据检查快速修复
- [troubleshooting/credential-decrypt-failed.md](./troubleshooting/credential-decrypt-failed.md) — 凭据解密失败
- [troubleshooting/routing-analytics.md](./troubleshooting/routing-analytics.md) — 路由分析
- [troubleshooting/live-stream-filter-options-diagnostic.md](./troubleshooting/live-stream-filter-options-diagnostic.md) — 实时流过滤选项诊断
- [troubleshooting/live-stream-filter-options-usage.md](./troubleshooting/live-stream-filter-options-usage.md) — 实时流过滤选项使用
- [troubleshooting/LIVE_STREAM_FILTER_ANALYSIS.md](./troubleshooting/LIVE_STREAM_FILTER_ANALYSIS.md) — 实时流过滤分析
- [troubleshooting/FILTER_OPTIONS_FINAL_REPORT.md](./troubleshooting/FILTER_OPTIONS_FINAL_REPORT.md) — 过滤选项最终报告

## 10. 国际化 (i18n)

- [03-design/02-feature-design/i18n/GUIDE_NAV_I18N.md](./03-design/02-feature-design/i18n/GUIDE_NAV_I18N.md) — 导航国际化指南

## 11. 变更日志

- [changelogs/](./changelogs/) — 近期变更日志（按日）
- [archive/process/changelogs/](./archive/process/changelogs/) — 历史变更日志归档

最新变更日志示例：
- [changelogs/2026-09-07-request-logs-view-42p01-selfheal.md](./changelogs/2026-09-07-request-logs-view-42p01-selfheal.md)
- [changelogs/2026-09-04-credential-decrypt-smoke-gate.md](./changelogs/2026-09-04-credential-decrypt-smoke-gate.md)
- [changelogs/2026-09-04-dep-outage-availability.md](./changelogs/2026-09-04-dep-outage-availability.md)
- [changelogs/2026-08-28-compression-response-integrity.md](./changelogs/2026-08-28-compression-response-integrity.md)
- [changelogs/2026-08-28-session-turns-api-500-fixes.md](./changelogs/2026-08-28-session-turns-api-500-fixes.md)

## 12. 截图与图像

- [screenshots/](./screenshots/) — 系统截图归档
- [assets/screenshots/](./assets/screenshots/) — 资产截图

## 13. 会话优化（活跃）

- [会话优化v4/](./会话优化v4/) — 当前版本（2026-08-17 起的活跃设计）

历史版本归档：
- [archive/process/session-optimization-v2/](./archive/process/session-optimization-v2/)
- [archive/process/session-optimization-v3/](./archive/process/session-optimization-v3/)

## 14. 顶层文档（docs/ 根目录）

| 文档 | 说明 |
|---|---|
| [PROJECT_OVERVIEW.md](./PROJECT_OVERVIEW.md) | 项目总览 |
| [MODULES_GUIDE.md](./MODULES_GUIDE.md) | 功能模块指南 |
| [QUICK_REFERENCE.md](./QUICK_REFERENCE.md) | 快速参考手册 |
| [architecture.md](./architecture.md) | 架构概要 |
| [comparison.md](./comparison.md) | 对比分析 |
| [db-changelog.md](./db-changelog.md) | 数据库变更日志 |
| [environment.md](./environment.md) | 环境配置 |
| [project-config.md](./project-config.md) | 项目配置 |
| [getting-started.md](./getting-started.md) | 入门指南 |
| [252-auto-cleanup-deployment.md](./252-auto-cleanup-deployment.md) | 252 自动清理部署 |

## 15. 设计与规划（顶层）

- [design/README.md](./design/README.md) — 设计文档总览
- [design/2026-09-04-tree-state-v2-routing-plan.md](./design/2026-09-04-tree-state-v2-routing-plan.md) — 树状态 V2 路由计划
- [design/2026-09-04-waterfall-overlay-plan.md](./design/2026-09-04-waterfall-overlay-plan.md) — 瀑布叠加计划
- [design/2026-08-19-stream-state-machine.md](./design/2026-08-19-stream-state-machine.md) — 流状态机
- [design/credential-monitor-heatmap-implementation.md](./design/credential-monitor-heatmap-implementation.md) — 凭据监控热力图实施
- [design/credential-monitor-heatmap-requirements.md](./design/credential-monitor-heatmap-requirements.md) — 凭据监控热力图需求
- [design/lite-mode-summary.md](./design/lite-mode-summary.md) — 轻量模式总结
- [design/lite-mode-storage-design.md](./design/lite-mode-storage-design.md) — 轻量模式存储设计
- [design/lite-mode-implementation-checklist.md](./design/lite-mode-implementation-checklist.md) — 轻量模式实施检查清单
- [design/lite-mode-quick-ref.md](./design/lite-mode-quick-ref.md) — 轻量模式速查
- [design/p0-1-host-restart-safe-drain-design.md](./design/p0-1-host-restart-safe-drain-design.md) — P0-1 主机重启安全排空设计
- [design/resume-blocked-long-stream-recovery.md](./design/resume-blocked-long-stream-recovery.md) — 恢复阻塞的长流恢复
- [design/compression-strategy-selector.md](./design/compression-strategy-selector.md) — 压缩策略选择器

## 16. 修复记录

- [fixes/](./fixes/) — 近期修复记录
- [fixes/2026-09-06-llm-hourly-stats-timestamp-fix.md](./fixes/2026-09-06-llm-hourly-stats-timestamp-fix.md) — LLM 小时统计时间戳修复
- [fixes/2026-09-06-pg-log-error-fixes-and-deploy.md](./fixes/2026-09-06-pg-log-error-fixes-and-deploy.md) — PG 日志错误修复与部署
- [fixes/2026-09-06-pg-missing-tables-fix.md](./fixes/2026-09-06-pg-missing-tables-fix.md) — PG 缺失表修复
- [fixes/2026-08-31-154-blue-green-deploy-recovery.md](./fixes/2026-08-31-154-blue-green-deploy-recovery.md) — 154 蓝绿部署恢复
- [fixes/2026-08-31-data-closure-audit-implementation.md](./fixes/2026-08-31-data-closure-audit-implementation.md) — 数据闭环审计实施
- [fixes/2026-08-30-complete-audit-implementation.md](./fixes/2026-08-30-complete-audit-implementation.md) — 完整审计实施
- [fixes/2026-08-29-audit-fixes-summary.md](./fixes/2026-08-29-audit-fixes-summary.md) — 审计修复总结
- [fixes/dashboard-lifecycle-2026-08-29.md](./fixes/dashboard-lifecycle-2026-08-29.md) — 仪表盘生命周期

## 17. 存储

- [storage/README.md](./storage/README.md) — 存储总览
- [storage/deployment-guide.md](./storage/deployment-guide.md) — 存储部署指南
- [storage/troubleshooting.md](./storage/troubleshooting.md) — 存储故障排查

## 18. 历史归档

| 归档桶 | 说明 |
|---|---|
| [archive/2026-06/](./archive/2026-06/) | 2026-06 月度归档（按子目录组织） |
| [archive/2026-07/](./archive/2026-07/) | 2026-07 月度归档 |
| [archive/2026-08/](./archive/2026-08/) | 2026-08 月度归档 |
| [archive/process/](./archive/process/) | 过程类归档（59 个子目录） |
| [archive/INDEX.md](./archive/INDEX.md) | 归档索引（按月份） |
| [archive/README.md](./archive/README.md) | 归档说明 |

### 主题归档（archive/process/）

| 主题归档 | 说明 |
|---|---|
| omnifree / omni-ref{1,2,3} / omniroute-ref | 历史 omni 项目资料 |
| revision-0811 | 8 月 11 日密集审计集（39 份） |
| session-optimization-v2 / v3 | 会话优化历史版本 |
| audit-collection / audits-collection / bugfix-collection / fix-collection / fixes-collection / handoff-collection / impl-summaries / incidents-collection / issues-collection / lessons-learned / session-logs-collection / ui-audit-collection | 命名空间合并的历史文档集 |
| analysis / approval / diagnostics / notes / refactor-plans | 主题类过程文档 |
| base-optimization / decay-optimization / pricing-optimization / pool-optimization / pool-state-optimization / routing-optimization-v3 / self-check-feature / self-check-optimization / full-optimization-v1 / param-compat / ir-format-optimization / format-conversion | 优化轨/技术债主题归档 |
| vendor-profile / vendor-management / incident-records | 供应商/事故管理 |
| new-arch-0703 / r112 / ursm-routing-redesign | 架构重构方案（已完成/历史） |
| distribution-activation / pending-tasks / comprehensive-testing / ui-optimization / multimodal-testing / llm-gateway-go-collection | 综合类归档 |
| audit-20260808-002500 / runbook-2026-07-09-request-logging-252 / architecture-diagrams / schema-history / changelogs / superpowers | 特殊归档 |

---

**最后更新**：2026-09-07  
**维护者**：LLM Gateway Team
