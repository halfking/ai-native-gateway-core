# LLM Gateway 文档主题索引

> 本索引按主题/功能组织，方便快速定位。与 [README.md](./README.md) 的目录结构导览配套使用。

> 🧹 2026-09-07：根目录过程文档已按规范整理（详见 [README.md](./README.md#-2026-09-07-根目录文档整理)）。本索引部分条目为 2026-08-17 快照，路径如有出入以 docs/README.md 与实际目录为准。

---

## 1. 入门（Start Here）

| 主题 | 文档 |
|---|---|
| 快速启动 | [operations/QUICKSTART.md](./operations/QUICKSTART.md) |
| 用户指南 | [user-guide/session-management.md](./user-guide/session-management.md) |
| 常见问题 / 故障排查 | [operations/troubleshooting-guide.md](./operations/troubleshooting-guide.md) |
| 升级指南 | [operations/UPGRADE.md](./operations/UPGRADE.md) |

## 2. 架构与设计

### 2.1 顶层架构

| 文档 | 说明 |
|---|---|
| [architecture/ARCHITECTURE.md](./architecture/ARCHITECTURE.md) | 系统架构总览 |
| [architecture/REPO_LAYOUT.md](./architecture/REPO_LAYOUT.md) | **仓库布局权威地图**（顶层目录用途+入位规则） |
| [architecture/API.md](./architecture/API.md) | API 入口文档 |
| [architecture/architecture-decisions.md](./architecture/architecture-decisions.md) | 架构决策汇总 |
| [architecture/ARCHITECTURE_REFACTOR_GUIDE.md](./architecture/ARCHITECTURE_REFACTOR_GUIDE.md) | 架构重构指南 |
| [architecture/routing-domain.md](./architecture/routing-domain.md) | 路由域设计 |
| [architecture/node-probe-mechanism.md](./architecture/node-probe-mechanism.md) | 节点探活机制 |

### 2.2 ADR（Architecture Decision Records）

- [adr/ADR-0001-handoff-goal-state-at-rest-encryption.md](./adr/ADR-0001-handoff-goal-state-at-rest-encryption.md)
- [adr/ADR-0002-target-go-package-layout.md](./adr/ADR-0002-target-go-package-layout.md) — Go 包分层迁移路线（分波次）

### 2.3 集成 / 接口

| 文档 | 说明 |
|---|---|
| [architecture/a2a-spec-2027.md](./architecture/a2a-spec-2027.md) | A2A 协议规范 |
| [architecture/apihub-design.md](./architecture/apihub-design.md) | APIHub 设计 |
| [architecture/admin-api-authentication.md](./architecture/admin-api-authentication.md) | 管理 API 鉴权 |
| [architecture/attachment-auth-integration.md](./architecture/attachment-auth-integration.md) | 附件鉴权集成 |
| [architecture/adaptive-response-format-converter.md](./architecture/adaptive-response-format-converter.md) | 自适应响应格式转换 |
| [architecture/armor-sdp-feasibility.md](./architecture/armor-sdp-feasibility.md) | ARMOR SDP 可行性 |

### 2.4 OpenAPI / API 契约

- [api/ops-platform-openapi.yaml](./api/ops-platform-openapi.yaml) — 运维平台 API 契约
- [api/session-analytics.yaml](./api/session-analytics.yaml) — 会话分析 API 契约

## 3. 模块设计

| 模块 | 文档 |
|---|---|
| 会话治理 | [architecture/routing-domain.md](./architecture/routing-domain.md) |
| 模型质量监控 | [model-quality/README.md](./model-quality/README.md) · [model-quality/FEATURED_MODELS_GUIDE.md](./model-quality/FEATURED_MODELS_GUIDE.md) · [model-quality/GATEWAY_INTEGRATION.md](./model-quality/GATEWAY_INTEGRATION.md) |
| 模型评估（Model-iQ） | [model-iq/01-design.md](./model-iq/01-design.md) |
| Memora | [modules/memora.md](./modules/memora.md) |
| 安全引擎 | [modules/security-engine.md](./modules/security-engine.md) |
| 会话检查器 | [modules/session-inspector.md](./modules/session-inspector.md) · [modules/session-inspector-flowchart.md](./modules/session-inspector-flowchart.md) |
| UA-TLS 伪装 | [features/ua-tls-disguise-module-summary.md](./features/ua-tls-disguise-module-summary.md) |

## 4. 设计/规格

| 主题 | 文档 |
|---|---|
| 路由自选择 | [design/AUTO_SELECTION_SPEC.md](./design/AUTO_SELECTION_SPEC.md) · [design/AUTO_SELECTION_IMPLEMENTATION_PLAN.md](./design/AUTO_SELECTION_IMPLEMENTATION_PLAN.md) · [design/AUTOROUTE_V2_FINAL_AUDIT_AND_IMPLEMENTATION_SUMMARY.md](./design/AUTOROUTE_V2_FINAL_AUDIT_AND_IMPLEMENTATION_SUMMARY.md) |
| 通道质量路由 | [design/CHANNEL_QUALITY_ROUTING_DESIGN.md](./design/CHANNEL_QUALITY_ROUTING_DESIGN.md) |
| 列存 Body 切分 | [design/COLUMNAR_BODY_SPLIT_PLAN.md](./design/COLUMNAR_BODY_SPLIT_PLAN.md) |
| 仪表盘 V2 | [design/DASHBOARD_API.md](./design/DASHBOARD_API.md) · [design/DASHBOARD_V2_IMPLEMENTATION.md](./design/DASHBOARD_V2_IMPLEMENTATION.md) · [design/DASHBOARD_V2_TECHNICAL_DESIGN.md](./design/DASHBOARD_V2_TECHNICAL_DESIGN.md) |
| 自适应超时 | [design/adaptive-timeout-strategy.md](./design/adaptive-timeout-strategy.md) |
| P1 优化计划 | [design/p1-optimization-plan.md](./design/p1-optimization-plan.md) |
| Pre-request 校验钩子 | [design/pre-request-validation-hook.md](./design/pre-request-validation-hook.md) |
| 实时请求流修复 | [design/realtime-request-stream-fix.md](./design/realtime-request-stream-fix.md) |
| 请求追踪系统 | [design/request-trace-system.md](./design/request-trace-system.md) |
| 统一凭据状态钩子 | [design/unified-credential-state-hook.md](./design/unified-credential-state-hook.md) |
| 超时重试优化 | [design/timeout-retry-optimization.md](./design/timeout-retry-optimization.md) |
| 三层缓存速查 | [design/QUICK-REF-THREE-TIER-CACHE.md](./design/QUICK-REF-THREE-TIER-CACHE.md) |
| 路由尝试跟踪 | [design/routing-attempts-tracking/](./design/routing-attempts-tracking/) |
| 自动路由反馈优化 | [design/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md](./design/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md) |

## 5. 部署 / 迁移

| 主题 | 文档 |
|---|---|
| 部署指南 | [deployment/DEPLOYMENT_GUIDE.md](./deployment/DEPLOYMENT_GUIDE.md) |
| 部署规则 | [deployment/DEPLOYMENT_RULES.md](./deployment/DEPLOYMENT_RULES.md) |
| 构建与部署 | [deployment/BUILD_AND_DEPLOY_GUIDE.md](./deployment/BUILD_AND_DEPLOY_GUIDE.md) |
| 配置指南 | [deployment/CONFIGURATION_GUIDE.md](./deployment/CONFIGURATION_GUIDE.md) |
| 客户部署指南 | [deployment/CUSTOMER-DEPLOY-GUIDE.md](./deployment/CUSTOMER-DEPLOY-GUIDE.md) |
| 71 服务器修复 | [deployment/71_repair_plan.md](./deployment/71_repair_plan.md) · [deployment/71-pg-trgm-installation-guide.md](./deployment/71-pg-trgm-installation-guide.md) |
| 自动控制部署 | [deployment/AUTO_CONTROL_DEPLOYMENT_20260701.md](./deployment/AUTO_CONTROL_DEPLOYMENT_20260701.md) |
| 仪表盘 V2 部署 | [deployment/DASHBOARD_V2_DEPLOYMENT.md](./deployment/DASHBOARD_V2_DEPLOYMENT.md) · [deployment/DASHBOARD_V2_QUICKSTART.md](./deployment/DASHBOARD_V2_QUICKSTART.md) · [deployment/DASHBOARD_V2_VERIFICATION.md](./deployment/DASHBOARD_V2_VERIFICATION.md) |
| 数据库环境分离 | [deployment/DATABASE-ENVIRONMENT-SEPARATION.md](./deployment/DATABASE-ENVIRONMENT-SEPARATION.md) · [deployment/DATABASE-SEPARATION-FIX-SUMMARY.md](./deployment/DATABASE-SEPARATION-FIX-SUMMARY.md) · [deployment/database-migration-fix.md](./deployment/database-migration-fix.md) |
| 备份策略 | [deployment/BACKUP_STRATEGY_71.md](./deployment/BACKUP_STRATEGY_71.md) |
| 零停机设计 | [deploy/zero-downtime-design.md](./deploy/zero-downtime-design.md) |
| 迁移配置 | [migrations/r1.13-security-config.md](./migrations/r1.13-security-config.md) |

## 6. 运维 / Runbook

| 主题 | 文档 |
|---|---|
| 运维手册 | [operations/OPERATIONS.md](./operations/OPERATIONS.md) |
| TODO 索引 | [operations/TODO_INDEX.md](./operations/TODO_INDEX.md) · [operations/TODO_APPROVAL_INTEGRATION.md](./operations/TODO_APPROVAL_INTEGRATION.md) · [operations/TODO_COMMUNITY_MODE.md](./operations/TODO_COMMUNITY_MODE.md) · [operations/TODO_MEMORY_DLQ.md](./operations/TODO_MEMORY_DLQ.md) |
| 升级指南 | [operations/UPGRADE.md](./operations/UPGRADE.md) |
| 仓库镜像策略 | [operations/REPO-MIRROR-POLICY.md](./operations/REPO-MIRROR-POLICY.md) |
| 测试报告 | [operations/TEST_REPORT_20260701.md](./operations/TEST_REPORT_20260701.md) |
| 154 服务器 Runbook | [ops/154-runbook.md](./ops/154-runbook.md) |
| 会话健康运维 | [ops/session-health-operations.md](./ops/session-health-operations.md) |
| 紧急操作 | [runbooks/ursm-v2-cutover.md](./runbooks/ursm-v2-cutover.md) |

## 7. 分区表管理

- [partition/README.md](./partition/README.md) — 分区表总览
- [partition/partition-architecture.md](./partition/partition-architecture.md) — 架构
- [partition/partition-standards.md](./partition/partition-standards.md) — 标准
- [partition/partition-test-cases.md](./partition/partition-test-cases.md) — 测试用例
- [partition/OPERATIONS_RUNBOOK.md](./partition/OPERATIONS_RUNBOOK.md) — 运维手册
- [partition/HOT_TABLE_OPTIMIZATION.md](./partition/HOT_TABLE_OPTIMIZATION.md) — 热表优化
- [partition/MIGRATION_341_TEST_CHECKLIST.md](./partition/MIGRATION_341_TEST_CHECKLIST.md) — 迁移测试
- [partition/MONTHLY_CHECKLIST.md](./partition/MONTHLY_CHECKLIST.md) — 月度检查
- [partition/IMPLEMENTATION_NOTES.md](./partition/IMPLEMENTATION_NOTES.md) — 实施笔记
- [partition/QUERY_PERFORMANCE_ANALYSIS.md](./partition/QUERY_PERFORMANCE_ANALYSIS.md) — 查询性能分析
- [partition/partition-background.md](./partition/partition-background.md) — 背景知识

## 8. 价格

- [pricing/README.md](./pricing/README.md) — 价格数据总览
- [pricing/2026-06-12-llm-pricing.md](./pricing/2026-06-12-llm-pricing.md) — LLM 价格快照
- [pricing/2026-06-12-diff.md](./pricing/2026-06-12-diff.md) — 价格差异对比
- [pricing/scripts/](./pricing/scripts/) — 价格导入脚本
- [pricing/raw/](./pricing/raw/) — 原始价格数据（按厂商）
- [pricing/2026-06-12-tier2/](./pricing/2026-06-12-tier2/) — Tier2 定价

## 9. 安全 / 合规

- [security/SECURITY_FEATURES.md](./security/SECURITY_FEATURES.md) — 安全特性
- [security/SECURITY-AUDIT-INDEX.md](./security/SECURITY-AUDIT-INDEX.md) — 安全审计索引
- [security/VIBECODING_GUIDELINES.md](./security/VIBECODING_GUIDELINES.md) — VibeCoding 规范
- [legal/disguise-compliance.md](./legal/disguise-compliance.md) — 伪装合规

## 10. 测试 / 验证

| 主题 | 文档 |
|---|---|
| 综合测试计划 | [testing/comprehensive-test-plan.md](./testing/comprehensive-test-plan.md) |
| 路由测试计划 | [testing/ROUTING_TEST_PLAN.md](./testing/ROUTING_TEST_PLAN.md) |
| 三环境统一验证 | [testing/three-env-unified-verification.md](./testing/three-env-unified-verification.md) |
| Claude Sonnet 测试 | [testing/CLAUDE_SONNET_4_6_TEST_REPORT.md](./testing/CLAUDE_SONNET_4_6_TEST_REPORT.md) |

## 11. 国际化 (i18n)

- [i18n/GUIDE_NAV_I18N.md](./i18n/GUIDE_NAV_I18N.md) — 导航国际化指南

## 12. 领域注册表

- [domains/DOMAIN_REGISTRY.md](./domains/DOMAIN_REGISTRY.md) — 领域注册

## 13. 变更日志

- [changelogs/](./changelogs/) — 近 14 天变更日志（按日）
- [archive/process/changelogs/](./archive/process/changelogs/) — 14 天前的历史变更日志

## 14. 截图与图像

- [screenshots/](./screenshots/) — 系统截图归档
- [ui-verification/](./ui-verification/) — UI 验证截图
- [images/](./images/) — 通用图片资源（logo、二维码）

## 15. 会话优化（活跃）

- [会话优化v4/](./会话优化v4/) — 当前版本（2026-08-17 起的活跃设计）

历史版本归档：
- [archive/process/session-optimization-v2/](./archive/process/session-optimization-v2/)
- [archive/process/session-optimization-v3/](./archive/process/session-optimization-v3/)

## 16. 历史归档

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

**最后更新**：2026-08-17
**维护者**：LLM Gateway Team