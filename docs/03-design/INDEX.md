# 方案设计 · 索引

> 最后更新：2026-10-01（全码审计轮程序化重建：`find docs/03-design -name "*.md"` 全量 + git 最后提交日期；替代 2026-08-18 自动索引的断更状态）

## 权威入口（2026-10-01 全码审计重生成）

| 文件 | 角色 |
|---|---|
| `01-architecture/architecture/ARCHITECTURE.md` | **当前系统架构（内部权威，事实快照 2026-10-01）** |
| `01-architecture/parallel-implementations-comparison.md` | **新旧并行实现对比（🔴 重点标注）** |
| `01-architecture/unified-optimization-prompts.md` | **统一优化提示词方案（U-01~U-10）** |
| `01-architecture/REPO_LAYOUT.md` | 仓库布局权威地图 |
| `01-architecture/domains/DOMAIN_REGISTRY.md` | 域名 SSOT 注册表 |

## 文件清单（全量，按路径排序）

| 文件 | 类型 | 状态 | 最后修改 |
|---|---|---|---|
| `01-architecture/architecture/a2a-spec-2027.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/adaptive-response-format-converter.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/admin-api-authentication.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/API.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/apihub-design.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/architecture-decisions.md` | md | active | 2026-09-07 |
| `01-architecture/architecture/ARCHITECTURE.md` | md | active | 2026-09-19 |
| `01-architecture/architecture/ARCHITECTURE_REFACTOR_GUIDE.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/armor-sdp-feasibility.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/attachment-auth-integration.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/node-probe-mechanism.md` | md | active | 2026-09-08 |
| `01-architecture/architecture/omniroute-integration-boundary.md` | md | active | 2026-08-21 |
| `01-architecture/architecture/optimization-roadmap.md` | md | active | 2026-08-23 |
| `01-architecture/architecture/README.md` | md | active | 2026-08-21 |
| `01-architecture/architecture/REPO_LAYOUT.md` | md | active | 2026-09-07 |
| `01-architecture/architecture/routing-and-state.md` | md | active | 2026-09-19 |
| `01-architecture/architecture/routing-domain.md` | md | active | 2026-08-18 |
| `01-architecture/architecture/runtime-request-flow.md` | md | active | 2026-09-19 |
| `01-architecture/deploy-zero-downtime/zero-downtime-design.md` | md | active | 2026-09-07 |
| `01-architecture/domains/DOMAIN_REGISTRY.md` | md | active | 2026-08-18 |
| `01-architecture/parallel-implementations-comparison.md` | md | active | untracked |
| `01-architecture/README.md` | md | active | 2026-08-18 |
| `01-architecture/unified-optimization-prompts.md` | md | active | untracked |
| `02-feature-design/design/adaptive-timeout-strategy.md` | md | active | 2026-08-18 |
| `02-feature-design/design/admin-registry-views.md` | md | active | 2026-08-22 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V2.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_COMPLETION_REPORT.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md` | md | active | 2026-09-04 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_QUICKREF.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_README.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_SUMMARY.md` | md | active | 2026-09-02 |
| `02-feature-design/design/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md` | md | active | 2026-08-18 |
| `02-feature-design/design/AUTO_SELECTION_IMPLEMENTATION_PLAN.md` | md | active | 2026-08-18 |
| `02-feature-design/design/AUTO_SELECTION_SPEC.md` | md | active | 2026-08-18 |
| `02-feature-design/design/CHANNEL_QUALITY_ROUTING_DESIGN.md` | md | active | 2026-08-18 |
| `02-feature-design/design/COLUMNAR_BODY_SPLIT_PLAN.md` | md | active | 2026-08-18 |
| `02-feature-design/design/CREDENTIAL_CONSOLE_HANDOFF.md` | md | active | 2026-09-04 |
| `02-feature-design/design/DASHBOARD_API.md` | md | active | 2026-08-18 |
| `02-feature-design/design/DASHBOARD_V2_IMPLEMENTATION.md` | md | active | 2026-08-18 |
| `02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md` | md | active | 2026-08-18 |
| `02-feature-design/design/p1-optimization-plan.md` | md | active | 2026-08-18 |
| `02-feature-design/design/pre-request-validation-hook.md` | md | active | 2026-08-18 |
| `02-feature-design/design/QUICK-REF-THREE-TIER-CACHE.md` | md | active | 2026-08-18 |
| `02-feature-design/design/realtime-request-stream-fix.md` | md | active | 2026-08-18 |
| `02-feature-design/design/request-trace-system.md` | md | active | 2026-09-08 |
| `02-feature-design/design/routing-attempts-tracking/00-code-context.md` | md | active | 2026-08-18 |
| `02-feature-design/design/routing-attempts-tracking/01-implementation-plan.md` | md | active | 2026-08-18 |
| `02-feature-design/design/routing-attempts-tracking/02-completion-report.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/00-design-spec.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/01-quick-wins.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/02-implementation-checklist.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/03-summary-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/04-execution-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/05-phase1-completion-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/06-progress-tracking.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/07-phase1-execution-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/08-today-summary.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/09-phase2-completion-report.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/10-final-summary.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/11-phase2-4-quick-guide.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/12-phase2-integration-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/timeout-retry-optimization/13-phase3-implementation-plan.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/14-phase4-implementation-plan.md` | md | active | 2026-08-18 |
| `02-feature-design/design/timeout-retry-optimization/15-project-completion-report.md` | md | active | 2026-09-07 |
| `02-feature-design/design/unified-credential-state-hook.md` | md | active | 2026-08-18 |
| `02-feature-design/design/vendor-credential-error-detail/00-code-context.md` | md | active | 2026-08-24 |
| `02-feature-design/design/vendor-credential-error-detail/01-logic-points.md` | md | active | 2026-08-24 |
| `02-feature-design/design/vendor-credential-error-detail/02-LP1.md` | md | active | 2026-08-30 |
| `02-feature-design/design/vendor-credential-error-detail/02-LP3.md` | md | active | 2026-08-30 |
| `02-feature-design/design/vendor-credential-error-detail/02-LP4.md` | md | active | 2026-08-30 |
| `02-feature-design/design/vendor-credential-error-detail/02-LP5.md` | md | active | 2026-08-30 |
| `02-feature-design/design/vendor-credential-error-detail/03-drift-check.md` | md | active | 2026-08-24 |
| `02-feature-design/features/ua-tls-disguise-module-summary.md` | md | active | 2026-08-18 |
| `02-feature-design/i18n/GUIDE_NAV_I18N.md` | md | active | 2026-08-18 |
| `02-feature-design/model-iq/01-design.md` | md | active | 2026-08-18 |
| `02-feature-design/model-quality/ENVIRONMENT_SETUP.md` | md | active | 2026-09-07 |
| `02-feature-design/model-quality/FEATURED_MODELS_GUIDE.md` | md | active | 2026-08-18 |
| `02-feature-design/model-quality/GATEWAY_INTEGRATION.md` | md | active | 2026-08-18 |
| `02-feature-design/model-quality/QUICKSTART.md` | md | active | 2026-08-18 |
| `02-feature-design/model-quality/README.md` | md | active | 2026-08-18 |
| `02-feature-design/modules/memora.md` | md | active | 2026-08-18 |
| `02-feature-design/modules/security-engine.md` | md | active | 2026-08-18 |
| `02-feature-design/modules/session-inspector-flowchart.md` | md | active | 2026-08-18 |
| `02-feature-design/modules/session-inspector.md` | md | active | 2026-08-18 |
| `02-feature-design/README.md` | md | active | 2026-08-18 |
| `02-feature-design/session-project-attribution.md` | md | active | 2026-08-20 |
| `02-feature-design/会话优化v4/00-README.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/01-现状基线与版本裁决.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/02-会话管理与压缩设计.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/03-缓存映射与请求队列设计.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/04-节点状态与路由处理设计.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/05-会话分析与模型选择设计.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/10-实施计划.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/11-完成情况核实与并发执行方案.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/12-GoalHandoff契约.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/13-T0契约冻结与所有权.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md` | md | active | 2026-08-20 |
| `02-feature-design/会话优化v4/14-URSM Redis delimiter-safe key兼容迁移冻结决策.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/15-URSM delimiter-safe key迁移实施计划.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/16-URSM delimiter-safe key迁移测试矩阵与TDD顺序.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/17-L3-k2-migration-implementation.md` | md | active | 2026-08-18 |
| `02-feature-design/会话优化v4/18-Goal影子指令与续跑优化方案.md` | md | active | 2026-09-17 |
| `02-feature-design/会话优化v4/会话分析元数据升级v1.md` | md | active | 2026-08-23 |
| `02-feature-design/会话优化v4/会话分析元数据契约v1.md` | md | active | 2026-08-23 |
| `02-feature-design/会话优化v4/客户端会话保持.md` | md | active | 2026-09-07 |
| `02-modules/fs-request-store.md` | md | active | 2026-08-26 |
| `02-modules/log-search-bleve.md` | md | active | 2026-08-26 |
| `03-interface-design/README.md` | md | active | 2026-08-18 |
| `04-data-design/governance/candidate-failure-logs-252-governance-2026-08-17.md` | md | active | 2026-09-07 |
| `04-data-design/migrations/r1.13-security-config.md` | md | active | 2026-08-18 |
| `04-data-design/model-catalog/README.md` | md | active | 2026-08-29 |
| `04-data-design/model-catalog/standard-models-canonical.md` | md | active | 2026-08-29 |
| `04-data-design/partition/HOT_TABLE_OPTIMIZATION.md` | md | active | 2026-08-18 |
| `04-data-design/partition/IMPLEMENTATION_NOTES.md` | md | active | 2026-08-19 |
| `04-data-design/partition/MIGRATION_341_TEST_CHECKLIST.md` | md | active | 2026-08-18 |
| `04-data-design/partition/MONTHLY_CHECKLIST.md` | md | active | 2026-08-19 |
| `04-data-design/partition/OPERATIONS_RUNBOOK.md` | md | active | 2026-08-18 |
| `04-data-design/partition/partition-architecture.md` | md | active | 2026-08-18 |
| `04-data-design/partition/partition-background.md` | md | active | 2026-08-18 |
| `04-data-design/partition/partition-standards.md` | md | active | 2026-08-18 |
| `04-data-design/partition/partition-test-cases.md` | md | active | 2026-08-18 |
| `04-data-design/partition/QUERY_PERFORMANCE_ANALYSIS.md` | md | active | 2026-08-18 |
| `04-data-design/partition/README.md` | md | active | 2026-08-18 |
| `04-data-design/README.md` | md | active | 2026-08-18 |
| `04-data-design/storage-observation-ledger.md` | md | active | 2026-09-30 |
| `04-data-design/storage-optimization-plan.md` | md | active | 2026-09-15 |
| `04-data-design/数据库处理框架方案.md` | md | active | 2026-08-28 |
| `05-security-design/README.md` | md | active | 2026-08-18 |
| `05-security-design/security/SECURITY-AUDIT-INDEX.md` | md | active | 2026-08-18 |
| `05-security-design/security/SECURITY_FEATURES.md` | md | active | 2026-08-18 |
| `05-security-design/security/VIBECODING_GUIDELINES.md` | md | active | 2026-08-18 |
| `AUDIT-auto-multidim-progress-reuse-analysis.md` | md | active | 2026-09-16 |
| `FEATURE-REQ-auto-multidim-progress-and-pricing-presets.md` | md | active | 2026-09-16 |
| `FEATURE-REQ-credential-heatmap-routing-log.md` | md | active | 2026-09-08 |
| `FEATURE-REQ-session-snapshot-and-request-detail.md` | md | active | 2026-09-08 |
| `frontend-component-usage-guide.md` | md | active | 2026-09-14 |
| `frontend-responsive-and-componentization-design.md` | md | active | 2026-09-13 |
| `perf-2026-09-05-core-quality-baseline.md` | md | active | 2026-09-08 |
| `perf-2026-09-25-probe-cost-optimization.md` | md | active | 2026-09-26 |
| `proxy-dataplane-integration-design.md` | md | active | 2026-09-17 |
| `proxy-management-design.md` | md | active | 2026-09-08 |
| `README.md` | md | active | 2026-09-13 |
| `streaming-validation-architecture.md` | md | active | 2026-08-30 |
| `streaming-validation-flowcharts.md` | md | active | 2026-08-29 |

## 统计

- 总文件：145（另加本 INDEX；2026-08-18 旧索引仅登记 67 篇，已断更归零重计）
- 分布（2026-10-01 实测）：01-architecture 23 · 02-feature-design 83 · 02-modules 2 · 03-interface-design 1（另含 api-yaml/ OpenAPI 3 份）· 04-data-design 19 · 05-security-design 4 · 根级（FEATURE-REQ/perf/proxy/streaming-validation 等）13
