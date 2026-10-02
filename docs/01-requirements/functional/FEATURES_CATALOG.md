# LLM Gateway Go — 功能特性目录（实现映射）

> **版本**: v3.0 · **事实快照**: 2026-10-01 · 基线 `3efae99`（main）
> **地位**: 需求（[`../SYSTEM_REQUIREMENTS.md`](../SYSTEM_REQUIREMENTS.md) §4）到代码/接口/页面的**实现映射层**。回答"每个功能在哪儿、暴露成什么、状态如何"。
> **重点标注约定**: 🔴 **并行** = 该功能存在新旧两版实现并存（详见 [`../../03-design/01-architecture/parallel-implementations-comparison.md`](../../03-design/01-architecture/parallel-implementations-comparison.md)，下称"并行对比文档"）；🟡 影子/灰度 = 已实现但非默认路径；⚪ 死代码 = 零外部引用待清理。
> **生成方法**: 2026-10-01 全码审计（admin 路由注册 + cmd 装配 + web 前端路由逐域盘点），包路径经主代理抽查存在。

---

## A. 多协议数据面网关

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| OpenAI 兼容对话/补全（SSE） | `domains/streaming` | `/v1/chat/completions`、`/v1/completions` | — | CURRENT |
| Anthropic Messages + count_tokens | `domains/streaming` | `/v1/messages` | — | CURRENT |
| OpenAI Responses | `domains/streaming` | `/v1/responses` | — | CURRENT |
| Gemini native（IR 翻译复用主链） | `domains/streaming` + `internal/ir` | `/v1beta/models/*` | — | CURRENT |
| Embeddings / 模型列表 | `domains/streaming` | `/v1/embeddings`、`/v1/models` | — | CURRENT |
| 协议归一 IR（5 协议 parse/serialize/语义分析/vendor 裁剪） | `internal/ir`、`internal/irconv`、`domains/transformation` | — | — | CURRENT |
| SSE 读取/完整性/empty-stream gate | `internal/sse`、`domains/streaming/integrity` | — | — | CURRENT |
| Vendor 字段精确过滤 | `internal/vendorstrip` | — | — | CURRENT |
| 统一适配器（旧尝试） | `adapter/unified` | — | — | ⚪ **死代码**（0 外部引用，2026-08-30 已声明废弃） |
| v2 Hook Pipeline（阶段管线架构） | `domains/pipeline`、`cmd/gateway/main_v2_pipeline.go` | `/v2/*`（`LLM_GATEWAY_V2_ENABLED`）+ v1 端点包装（`LLM_GATEWAY_USE_V2_PIPELINE`） | — | 🔴 **并行**（P-01：v1 handler 主用，v2 双 flag 默认 OFF） |
| Pipeline 演示独立入口 | `cmd/gateway-v2` | 独立 :8782 | — | 🔴 **并行**（P-01：仅研发环境，生产镜像不构建） |
| Mock 上游（fast/slow） | `internal/providers/mock` | `/mock/v1/*` | — | CURRENT |
| 会话式 API | `domains/session` | `/v1/sessions`、`/v1/gw/sessions` | — | CURRENT |
| Goal 运行编排 | `cmd/gateway/goal_control.go`、`internal/handlers/goalrun_handler.go` | `/v1/goal-runs`、`/v1/handoffs/confirm` | — | CURRENT |
| 托管任务 | `domains/hostedtask`、`bg/hosted_task_reconciler.go` | `/v1/hosted-tasks` | — | CURRENT |
| 请求存续 survival | `durable/` | 配置开关 | — | CURRENT |

## B. 智能路由与自动路由

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 路由解析/概览/健康/评分权重/紧急修复 | `admin/routing.go` | `/api/routing/*` | RoutingOverview 等 | CURRENT |
| 候选绑定管理 | `admin/credential_routing_log.go` | `/api/routing/candidate-binding*` | — | CURRENT |
| 路由覆盖+审计 | `admin/routing_overrides.go` + `control/routing` | `/api/admin/routing/overrides` | `/routing/overrides(/audit)` | 🔴 **并行**（P-07：CQRS 迁移中，create 已迁 `control/routing/create.go`，delete/extend 留旧包） |
| 自动路由（分类→评分→决策→结算） | `autoroute/` | `/api/admin/auto-route/*` | DecisionsView、CorrelationsView | 🔴 **并行**（P-03：decision v1/v2、classifier v1/v3、scoring×3 同热路径，v1 默认） |
| 自动调参闭环 | `routingopt/`、`admin/auto_route_tuning.go` | `/api/admin/auto-route/tuning/*`、`/api/admin/auto-tune/*` | AutoTuningView | CURRENT |
| 任务画像/工单类型 | `taskprofile/`、work-types | `/api/admin/work-types*` | WorkTypesView、TaskProfileView | CURRENT |
| 路由事故 | `domains/routeincident` | `/api/admin/route-incidents*` | — | CURRENT |
| 编排引擎（旧尝试） | `internal/orchestration` | — | — | ⚪ **死代码**（0 外部引用） |

## C. 供应商与凭据管理

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 供应商 CRUD/测试/播种 | `admin/providers.go` | `/api/providers*` | ProvidersView、ProviderDetailView | CURRENT |
| 凭据生命周期（约 20 端点） | `secret/`、admin | `/api/credentials/*` | — | CURRENT |
| 凭据健康/热力图/滑窗 | `credentialhealth/`、`admin/credential_monitor*.go` | `/api/admin/credential-monitor/*` | CredentialMonitorView、CredentialHeatmapView | CURRENT |
| 凭据指纹槽 | `credentialfpslot/` | `/api/admin/fp-slot-policy` | NodeHealthTimelineView | CURRENT |
| 余额/配额探测 | `bg/balance_quota_probe.go` 等 | — | — | CURRENT |
| 出口代理池 | `proxy/` | `/api/proxy/*` | ProxyView | CURRENT |
| 指纹伪装 | `disguise/` | — | — | CURRENT |
| identity-bound 连接池 | `pool/` | `/api/admin/identity-pool/*` | — | CURRENT |

## D. 探测 / 自检 / 节点健康

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 新探测 worker 族（默认） | `bg/credential_selfcheck.go`、`bg/node_probe.go`、`bg/system_health.go` | — | — | CURRENT（2026-07-14 起默认，`internal/probemode` 唯一开关权威） |
| legacy 探测 5 worker | `bg/self_check_worker.go`、`credential_probe_v2.go`、`model_probe.go`、`passive_probe_listener.go`、`active_probe_worker.go` | — | — | 🔴 **并行**（P-02：默认停用，需双 env 才回滚启动；对象仍装配注入 admin） |
| 持久探测队列/看板/SSE | `bg/probe_*.go`、`admin` | `/api/admin/probe/*` | — | CURRENT |
| 凭据自检 | `admin` | `/api/self-check/*` | SelfCheckPanel | CURRENT（唯一有专项 FR 文档的域） |
| 系统监控 | `bg/systemmonitor/` | `/api/admin/system-monitor/*` | — | CURRENT |
| 请求级探测学习 | `internal/reqprobe` | — | — | CURRENT（生产 executor 在用） |
| 2×2 mock 探测 | `internal/mockprobe` | — | — | CURRENT |
| 诊断与修复向导 | `admin/provider_diagnose.go`、`health_check_handlers.go` | `/api/admin/diagnostics/*`、`/admin/api/v1/health-checks` | — | CURRENT |
| trace 探测（旧尝试） | `internal/probe` | — | — | ⚪ **死代码**（0 外部引用） |

## E. 模型目录 / 质量 / 模型 IQ

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 模型目录/别名/模态/窗口校准 | `modelcatalog/`、`modelname/`、`resolve/`、`modelbinding/` | `/api/models*`、`/api/catalog*` | ModelsView | CURRENT |
| 模型 IQ 标准基准 | `modeliqdata/` | `/api/admin/model-iq/*` | — | CURRENT |
| 质量画像/聚合 | `internal/quality`、`internal/handlers/quality_handler.go` | `/api/quality/*` | — | CURRENT（P0 门禁：auth/tenant scope 待验证） |
| 完整性/异常检测 | `bg/integrity_*.go` | `/api/admin/model-integrity`、`/format-anomalies` | ModelIntegrityView | CURRENT |
| 能力评分（旧尝试） | `internal/capabilityscore` | — | — | ⚪ **死代码**（0 外部引用） |

## F. 会话 / 快照 / 回放

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| request_logs canonical（热/归档分区） | `telemetry/` | `/api/logs*` | RequestLogsView | CURRENT |
| 会话视图族（30+ 文件） | `admin/session_*.go` | `/api/admin/sessions*`、`/api/admin/turns*` | SessionDetailView、TurnsListView | CURRENT |
| Sessions V2（schema+writer+cache+镜像回填） | `domains/session/v2`、`internal/sessionv2mirror` | — | — | 🔴 **并行**（P-04：V1 主写/V2 影子写，双读 7 天零漂移门禁后翻主读） |
| 会话摘要/标题 | `domains/sessionsummary/` | — | — | CURRENT |
| 会话分析（物化视图+RLS） | `admin/session_analytics_*.go` | `/api/admin/session-analytics/*` | UserProfileView、ClientAnalyticsView | CURRENT |
| live-stream SSE | dispatch QueueProjection → LiveStreamSSEHub | `/api/admin/live-stream*` | — | CURRENT |
| 会话压缩 v3（L1/L2/L3 三层缓存） | `compression`（`main_v3_wiring.go`） | `/api/admin/compression/*` | — | CURRENT |

## G. 配额 / 计费 / MaaS

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| API Key 与申请 | `admin` | `/api/keys*`、`/v1/keys/apply` | KeysView、KeyApplicationsView | CURRENT |
| MaaS 计费（订单/套餐/钱包/账本/倍率/信用桶） | `maas/` | `/api/maas/*`、`/api/admin/maas/*` | MaaS 门户 5 页 | CURRENT |
| 用量与成本 | `domains/stats` | `/api/usage*`、`/api/admin/usage/` | UsageCostView | CURRENT |
| 定价管理 | `admin` | `/api/pricing/` | PricingManagementView | CURRENT |
| 成本对账 | `admin/provider_cost_reconciliation.go`、`bg/cost_reconciliation_worker.go` | `/api/admin/provider-cost-reconciliation*` | ReconciliationReportView | CURRENT |
| 限流/并发限额 | `ratelimit/` | `/api/admin/concurrency-limit/*` | — | CURRENT |

## H. 审批 / 通知 / 集成

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 审批工作流 | `api/approval_handler.go`、`bg/approval_timeout_worker.go` | `/v1/approvals/` | ApprovalListView、ApprovalConfigView | CURRENT |
| 钉钉回调 | `api/dingtalk_callback.go` | 回调 | — | CURRENT |
| 飞书机器人 | `cmd/gateway/feishubot_init.go`、`admin/feishu_handlers.go` | `/api/admin/feishubot/*` | — | CURRENT |
| Memora 记忆 | `domains/hooks/memoraauto` | `/api/system/memora-*` | — | CURRENT |

## I. 日志 / 追踪 / 看板

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 请求日志查询+全文检索 | `admin`、bleve | `/api/logs*`（search） | RequestDetailFullscreenView | CURRENT |
| 请求旅程 | `requestjourney/` | `/api/admin/request-journeys/*` | RequestJourneyDetailView | CURRENT |
| 连接注册表 | `admin/connection_registry.go` | `/api/admin/connection-registry*` | ConnectionRegistryView | CURRENT |
| 调度瀑布 | `cmd/gateway/waterfall_*.go` | — | DispatchWaterfallView | CURRENT |
| 看板族 | `admin/dashboard_*.go` | `/api/admin/dashboard/*` | DashboardView(V2)、HomeView | CURRENT |

## J/K/L. 免费资源池 / 安全 / 租户

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 免费池+免费发现 | `domains/freeresource`、`discovery/` | `/api/free-pool/*`、`/api/free-discovery/*` | FreePoolView、FreeDiscoveryView | CURRENT |
| 提示注入防护 | `domains/promptinjection` | `/api/admin/prompt-injection/*` | PromptInjectionSettingsView | CURRENT |
| 输出合规 | `admin/output_compliance_handler.go` | `/api/admin/output-compliance/*` | OutputComplianceView | CURRENT |
| 敏感词/消毒 | `security/sensitive|sanitize|armor|guardian` | `/api/admin/sensitive-words/*` | — | CURRENT |
| IP 黑名单 | `middleware` | `/api/admin/security/ip-blocklist*` | — | CURRENT |
| 租户管理/设置 | `admin/tenants.go`、`domains/tenant` | `/api/admin/tenant-settings/` | TenantsView、TenantDashboardView | CURRENT |
| 用户与角色 | `admin` | `/api/users*` | UsersView | CURRENT |
| 租户自助 | `tenantops/` | `/api/tenant/*` | — | CURRENT |

## M/O/P/Q. 标注训练 / 运维生命周期 / 交付 / 中心管控

| 特性 | 代码包 | 端点 / API | 前端页 | 状态 |
|---|---|---|---|---|
| 标注工作流 | `admin/annotation_handler.go`、`cmd/llm-gw-annotator` | `/api/admin/annotations*` | AnnotationView | CURRENT |
| 训练数据导出 | `exporter/`、`cmd/llm-gw-exporter` | — | — | CURRENT |
| 回放/回测/测试台 | `cmd/traffic-replay`、`cmd/tuning-backtest`、`cmd/auto-testbench`、`cmd/autoclass-bench` | — | — | CURRENT |
| 数据生命周期 | `admin/data_lifecycle_*.go`、`bg.PartitionManager` 等 | `/api/admin/data-lifecycle/*` | DataLifecycleView+8 子视图 | CURRENT |
| 备份恢复 | `admin` | `/api/admin/backups*` | — | CURRENT |
| 热配置 | `hotconfig/`、`settings/` | `/admin/config/reload` | SettingsView | CURRENT |
| 功能模块注册表 | `admin/modules.go` | `/api/admin/modules*` | ModulesView | CURRENT |
| License/激活/试用 | `licensing/` | `/api/system/license/*` | BootstrapWizardView、OfflineActivationView | CURRENT |
| 自动更新 | `autoupdate/` | `/api/system/upgrade/*` | UpdateActivateView | CURRENT |
| 双模存储 full/lite | `storage/` | `/api/admin/storage/*` | — | CURRENT |
| 中心管控 | `center/` | `/api/admin/center/*` | — | CURRENT |
| apihub 资产图谱 | `apihub/` | `/api/admin/agents` | AgentRegistryView | CURRENT |
| 插件运行时 | `plugin-runtime/` | `/api/v1/plugins*` | — | CURRENT |
| Vibe Coding 运营 | `vibecoding/` | `/ops/vibecoding` | VibeCodingView | CURRENT |
| ctx pool（旧尝试） | `internal/ctxpool` | — | — | ⚪ **死代码**（0 外部引用） |
| agent wsclient（旧尝试） | `internal/agent/wsclient` | — | — | ⚪ **死代码**（0 外部引用） |

## 辅助二进制（cmd/，共 34 个）

`gateway`（主）· `gateway-v2`（🔴 Pipeline 演示入口，见 P-01）· `tools` · `llm-gw-exporter`（Parquet 导出+隐私校验）· `llm-gw-annotator`（标注 CSV）· `quality-service`/`quality-monitor`（质量）· `traffic-replay`（历史流量过真实 autoroute）· `tuning-backtest`（调参回测）· `auto-testbench`（修正即测试抽样套件）· `autoclass-bench`（分类质量门禁 macro-F1）· `autoroute-e2e-audit`（auto 端到端审计）· `scenario_driver`（子用例进程内驱动）· `sessionforensics`/`sessionmeta-bench`（会话取证/基准）· `bleve-backfill`（全文索引回填）· `check-credentials`/`regen-credentials`/`probe-cred`/`verify-model-fetch`（凭据诊断四件套）· `cfg_dump`/`env-injector`（配置/密钥）· `fetch-standard-iq`/`seed-free-resources`（数据种子）· `license-authority`（许可签发）· `migrate-ursm-v2`（生产可用）/`ursm-k2-preflight`（只读）/`k2-migrate-ursm`（**dry-run only，NO-GO 门禁**）· `compression-bench(mark)`（压缩基准）· `calc_quality`/`test_quality_api`/`test_sql`/`routing-test-client`（测试驱动）

## 前端页面（web/，Vue 3.5 + Vite 6 + TS + Element Plus）

约 60+ 路由，权限分级 requiresSuper / requiresPlatformOps / requiresProviderConsole / requiresOpsPlatform。数据面协议无前端页；管理面覆盖上表所列 View；另有内置试聊 `/chat`、示例 `/examples`、维护页 `/maintain`、引导 `/bootstrap`、客户交付 `/customer/*`。8 语言 i18n。

---

**并行实现速查（🔴 汇总，详证见并行对比文档）**: P-01 入口 v1/v2 双轨 · P-02 探测新旧 worker 族 · P-03 autoroute 三代 · P-04 Session V1/V2 双写 · P-05 outbox 事件构造器三代 · P-06 URSM key 三代 schema · P-07 admin/control CQRS 半迁移 · P-08 outbox 模式 4 套 · P-09 centeragent 双路径 · P-10 gateway-v2 三重暴露面 · ⚪ 死代码 7 处（`internal/probe`、`internal/orchestration`、`internal/capabilityscore`、`internal/ctxpool`、`internal/agent/wsclient`、`adapter/unified`、`examples/`）。

**最后更新**: 2026-10-01 · 生成于全码审计轮
