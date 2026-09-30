# LLM Gateway Go — 系统需求规格说明书（SRS）

> **版本**: v3.0（全码审计重生成版）
> **事实快照**: 2026-10-01 · 基线提交 `3efae99`（main）
> **地位**: `docs/01-requirements/` 的权威总纲。单特性需求继续按 `functional/FR-*.md` 拆分细化；本文回答"系统要做什么"，实现映射见 [`functional/FEATURES_CATALOG.md`](./functional/FEATURES_CATALOG.md)，架构方案见 [`../03-design/01-architecture/architecture/ARCHITECTURE.md`](../03-design/01-architecture/architecture/ARCHITECTURE.md)。
> **证据优先级**: 运行时 wiring / Go 代码 / SQL 迁移 / 自动化测试 > 本文档 > 历史审计与路线图。
> **生成方法**: 2026-10-01 四域并行子代理审计（重复实现盘点 / 运行时架构接线 / 功能特性盘点 / 文档陈旧度盘点）+ 主代理逐项实证抽查；所有"现状"列均以代码接线为准，非以历史文档为准。

---

## 1. 系统定位与目标

LLM Gateway Go 是**企业级大模型统一接入网关**：为多租户企业提供 OpenAI/Anthropic/Gemini/Responses 等协议兼容的数据面、智能路由与凭据治理、计费与配额、可观测与合规能力，以单进程 Go 巨石（数据面/控制面/管理面同进程）+ PostgreSQL（+可选 Redis）交付，支持 full（PG+Redis）与 lite（SQLite+本地文件，零外部依赖）两种存储模式。

**业务目标**:

1. **统一接入**——一套端点代理全部上游协议，客户端零改造切换供应商。
2. **低成本**——双层路由（先选模型、再选凭据）+ 免费资源池 + 语义/会话压缩，持续降低单 token 成本。
3. **高可用**——凭据健康治理、多层队列调度、故障自愈与自动 failover，目标上游故障对客户端透明。
4. **可运营**——多租户隔离、MaaS 计费闭环、审批/审计/合规、中心化管控与自动升级，满足企业交付。

## 2. 用户与角色

| 角色 | 使用入口 | 核心诉求 |
|---|---|---|
| API 调用方（Agent/应用） | `/v1/*` 数据面 + sk-* API Key | 协议兼容、稳定、低延迟、失败可重试 |
| 平台管理员（superAdmin/platformOps） | web 管理台 `/api/admin/*` | 供应商/凭据/路由/探测/安全/成本全量运营 |
| 租户管理员 | `/api/tenant/*`、MaaS 门户 `/tenant/*` | 模型目录、套餐/账单/用量、自主限额 |
| 运维/交付人员 | installer、`/api/system/*`、maintain | 安装、激活、升级、回滚、备份恢复 |
| 标注/训练人员 | Annotation 页、annotator/exporter CLI | 路由决策标注、训练数据导出 |
| 中心管控（主控端） | `llm.kxpms.cn` ↔ `center/` | 实例注册监控、命令下发、License 分发 |

## 3. 系统边界

**含**: 协议代理与转换（IR canonical）、路由决策与调度、凭据与供应商治理、探测与自检、会话记录与快照、计费配额、审批流、通知、标注与训练数据导出、插件运行时、License/自动更新、中心管控 agent、管理台与 API。

**不含（由三服务边界承担，见 ARCHITECTURE §9）**:
- 会话分析/审批/任务投影的**所有权**——归 ai-session-manager（ASM），Gateway 只产生事件与兼容读取；
- License/制品分发/激活/升级的**所有权**——归 ai-native-maintain，Gateway 保留 compatibility proxy；
- 主控端 License/实例管理服务端——`llm.kxpms.cn`（`docs/03-design/01-architecture/architecture/API.md`）。

**上游依赖**: 各 LLM Provider API（OpenAI/Anthropic/Gemini/国产厂商/MaaS）、可选出口代理池、对象/文件存储（请求体归档）、Prometheus/OTel collector。

## 4. 功能需求（FR）

> 状态标记沿用 ARCHITECTURE 纪律：`CURRENT`=已接线验证 / `PARTIAL`=已接入有缺口 / `SHADOW`=影子灰度非默认 / `GAP`=代码有需求文档曾缺失 / `PLAN`=规划。优先级：M=必须，S=应该，O=可选。

### 4.1 数据面协议代理（DP）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-DP-01 | OpenAI 兼容 `/v1/chat/completions`、`/v1/completions`（流式+非流式） | M | CURRENT | `domains/streaming` |
| FR-DP-02 | Anthropic Messages `/v1/messages`(+`count_tokens`)，x-api-key 与兼容鉴权 | M | CURRENT | 同上 |
| FR-DP-03 | OpenAI Responses `/v1/responses` | S | CURRENT | 同上 |
| FR-DP-04 | Gemini native `/v1beta/models/*`（经 IR 翻译复用主链路） | S | CURRENT | `main.go:6230` |
| FR-DP-05 | `/v1/embeddings`、`/v1/models` | M | CURRENT | `domains/streaming` |
| FR-DP-06 | 协议归一 IR：parse/serialize 覆盖 openai/anthropic/gemini/ollama/responses；vendor 扩展字段不静默丢弃 | M | CURRENT | `internal/ir`、`domains/transformation` |
| FR-DP-07 | SSE 流式中继：单行字节上限、empty-stream gate、首帧缓冲、流完整性检测 | M | CURRENT | `internal/sse`、`domains/streaming/integrity` |
| FR-DP-08 | Vendor 字段过滤按厂商精确匹配（MiniMax base_resp 先检后删等） | M | CURRENT | `internal/vendorstrip` |
| FR-DP-09 | 会话式 API `/v1/sessions`（含 `/v1/gw` 别名） | S | CURRENT | `domains/session` |
| FR-DP-10 | Goal 运行编排 `/v1/goal-runs`：完成度判定、追问深度、审计拦截、handoff 确认 | S | CURRENT | `cmd/gateway/goal_control.go`、`internal/handlers/goalrun_handler.go` |
| FR-DP-11 | 托管后台任务 `/v1/hosted-tasks` + 对账 | O | CURRENT | `domains/hostedtask`、`bg/hosted_task_reconciler.go` |
| FR-DP-12 | 请求存续（survival）：interactive/durable 双阶段、指数退避、夜间模式、租户任务上限 | S | CURRENT | `durable/`、`config`（request_survival） |
| FR-DP-13 | Mock 上游 `/mock/*`（fast/slow）供探测与 E2E | O | CURRENT | `internal/providers/mock`、`internal/mockprobe` |

### 4.2 路由与调度（RT）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-RT-01 | 双层路由：L1 `model=auto` 选模型（任务分类+多维评分），L2 选凭据 | M | CURRENT | `autoroute/`、`domains/streaming/executors` |
| FR-RT-02 | 候选规划：tier 顺序、健康/可用性过滤、sticky 会话粘性、压力感知 | M | CURRENT | `executors/router.go` |
| FR-RT-03 | P2C 候选排序；边际成本惩罚默认折入综合分（env 可关） | M | CURRENT | 同上 |
| FR-RT-04 | 多层队列调度（dispatch）：model 队列→credential 队列→forwarder，governor 削峰，tiered failover | M | CURRENT（唯一执行路径） | `domains/dispatch`、`executor_dispatch.go:24` |
| FR-RT-05 | URSM 统一路由状态机：Redis 权威 + PG 持久化，off/shadow/canary/authoritative 四档 | M | CURRENT | `domains/ursm/v2` |
| FR-RT-06 | 路由覆盖（overrides）人工干预 + 审计 | S | CURRENT | `admin/routing_overrides.go`、`control/routing`（CQRS 迁移中） |
| FR-RT-07 | 自动调参闭环：决策回流→调参提案→回测→灰度应用 | S | CURRENT | `routingopt/`、`cmd/tuning-backtest` |
| FR-RT-08 | 任务画像与工单类型（L1 分类）参与路由；人工修正回流 | S | CURRENT | `taskprofile/`、work-types API |
| FR-RT-09 | 路由决策可解释：`X-Gw-Auto-Decision` 头 + `auto_decision` JSONB + 决策审计查询 | M | CURRENT | `autoroute`、`/api/admin/auto-route/decisions` |
| FR-RT-10 | 近期模型热榜（租户 7 天）参与自检与路由 | O | CURRENT | `recentmodels/` |

### 4.3 供应商与凭据治理（CR）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-CR-01 | 供应商 CRUD、启用/测试/刷新、从目录播种 | M | CURRENT | `admin/providers.go` |
| FR-CR-02 | 凭据生命周期：加密存储（Fernet/AES-GCM+AAD）、promote/demote、禁用、批量测试 | M | CURRENT | `secret/`、`/api/credentials/*` |
| FR-CR-03 | 凭据健康：credential×model 状态机（cooling/fail 计数）、熔断与自动恢复 | M | CURRENT | `domains/credentialstate` |
| FR-CR-04 | 凭据指纹槽（fp slots）：每凭据虚拟并发配额（Redis Lua），与 Python 版键互通 | S | CURRENT | `credentialfpslot/` |
| FR-CR-05 | 余额/配额探测与下限保护 | S | CURRENT | `bg/balance_quota_probe.go` 等 |
| FR-CR-06 | 出口代理池：订阅节点、区域、健康检查、按 egress_profile 绑定供应商 | O | CURRENT | `proxy/`、`upstream/` |
| FR-CR-07 | 请求伪装（UA/指纹池）辅助反限流 | O | CURRENT | `disguise/` |
| FR-CR-08 | identity-bound 连接池防凭据串号 | M | CURRENT | `pool/` |

### 4.4 探测与自检（PB）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-PB-01 | 持久探测队列 + 新探测 worker 族（node_probe/credential_selfcheck/system_health），错误门控停放与早停 | M | CURRENT（默认） | `bg/node_probe.go` 等、`internal/probemode` |
| FR-PB-02 | 自检及时恢复（已有专项 FR 文档） | M | CURRENT | `functional/FR-selfcheck-timely-recovery.md` |
| FR-PB-03 | 请求级探测学习（reqprobe）随生产 executor 运行 | S | CURRENT | `internal/reqprobe` |
| FR-PB-04 | 2×2 mock 探测（可用性×延迟矩阵） | O | CURRENT | `internal/mockprobe` |
| FR-PB-05 | 探测控制台：dashboard、队列快照、SSE 实时流、可用性时间线 | S | CURRENT | `/api/admin/probe/*` |
| FR-PB-06 | 系统监控：按凭据/模型/供应商并发监控、自动恢复、SSE | S | CURRENT | `bg/systemmonitor/` |

### 4.5 模型目录与质量（MC）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-MC-01 | 模型目录：canonical 归一、别名映射、模态默认、上下文窗口校准 | M | CURRENT | `modelname/`、`modelcatalog/`、`resolve/` |
| FR-MC-02 | 模型 IQ 标准基准（内嵌表 + Artificial Analysis 刷新） | S | CURRENT | `modeliqdata/`、`cmd/fetch-standard-iq` |
| FR-MC-03 | 质量画像管道与聚合视图（summary/ranking/provider stats） | S | CURRENT | `internal/quality`、`/api/quality/*` |
| FR-MC-04 | 模型完整性/格式异常检测与采集 | O | CURRENT | `bg/integrity_*.go`、`/api/admin/model-integrity` |

### 4.6 会话与请求记录（SS）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-SS-01 | 请求日志（request_logs 热分区+归档）为 canonical 事实源 | M | CURRENT | `telemetry/`、`db/`（分区） |
| FR-SS-02 | 会话视图：列表/详情/轮次/时间线/导出/对比/聚类/全景图 | S | CURRENT | `admin/session_*.go` |
| FR-SS-03 | Sessions V2 影子写（schema+writer+cache），双读行级对账，7 天零漂移门禁后翻主读 | S | SHADOW（迁移中） | `domains/session/v2`、`dual_writer.go`、`dual_read_validator.go` |
| FR-SS-04 | 会话摘要/标题自动生成 | O | CURRENT | `domains/sessionsummary/` |
| FR-SS-05 | 请求体归档（热区/文件/对象存储）与详情回放 | S | CURRENT | `domains/requestdetail` |
| FR-SS-06 | 会话分析（用户/客户端/任务维度，物化视图+RLS） | S | CURRENT | `admin/session_analytics_*.go` |
| FR-SS-07 | turn_logs 轮次日志聚合 | S | CURRENT | `cmd/gateway/turn_logs_aggregator.go` |

### 4.7 计费、配额与 MaaS（BL）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-BL-01 | API Key 申请/审批/默认限额 | M | CURRENT | `/api/keys*`、`/api/key-applications*` |
| FR-BL-02 | MaaS 商业化：套餐/订单/钱包/账本/充值包/模型倍率/信用额度桶 | S | CURRENT | `maas/`、`/api/maas/*` |
| FR-BL-03 | 用量统计与成本核算（租户/供应商/模型维度） | M | CURRENT | `stats`、`/api/usage*` |
| FR-BL-04 | 供应商成本对账（双视角报表 + Excel 导出） | S | CURRENT | `admin/provider_cost_reconciliation.go` |
| FR-BL-05 | 限流：RPM 滑窗（Redis）、并发限额、每租户配额 | M | CURRENT | `ratelimit/` |
| FR-BL-06 | 配额充值回调 webhooks | S | CURRENT | `api/webhooks` |

### 4.8 审批、通知与集成（AP/NT）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-AP-01 | 审批工作流：列表/通过/拒绝/统计/超时/恢复 | S | CURRENT | `api/approval_handler.go` |
| FR-AP-02 | 待处理响应（pending responses）管理 | S | CURRENT | `admin/pending_handlers.go` |
| FR-NT-01 | 多通道通知：钉钉/飞书/email/回调；飞书机器人路由规则与发送日志 | S | CURRENT | `domains/notification/`、`admin/feishu_handlers.go` |
| FR-NT-02 | Memora 记忆上下文集成 | O | CURRENT | `/api/system/memora-*` |

### 4.9 可观测与查询（OB）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-OB-01 | 请求日志查询 + Bleve 全文检索 + 归档/清理 | M | CURRENT | `/api/logs*`、`cmd/bleve-backfill` |
| FR-OB-02 | 请求旅程（journey）注册表与队列观测 | S | CURRENT | `requestjourney/` |
| FR-OB-03 | 调度瀑布图（dispatch waterfall 9 阶段） | S | CURRENT | `waterfall_by_request.go` |
| FR-OB-04 | 看板体系：综合/运维/错误下钻/泳道/会话健康/降级兜底 | S | CURRENT | `admin/dashboard_*.go` |
| FR-OB-05 | Prometheus `/metrics`（admin 保护）+ OTel tracing + `/healthz(/full)` `/readyz` | M | CURRENT | `metrics/`、`middleware/` |
| FR-OB-06 | 客户端错误上报与客户端配置审计 | O | CURRENT | `/api/system/client-error` |
| FR-OB-07 | pprof 独立 loopback 端口 | O | CURRENT | `cmd/gateway/pprof_server.go` |

### 4.10 免费资源池与自动发现（FP）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-FP-01 | 免费池：发现/目录/注册/批量注册/env 导入/ signup-hub/临时邮箱/快捷入口/桥接 OAuth | O | CURRENT | `admin/free_discovery.go`、`domains/freeresource` |
| FR-FP-02 | 免费发现任务/模板与周期扫描调度 | O | CURRENT | `discovery/`、`bg.ScanScheduler` |
| FR-FP-03 | 免费额度周期重置与回收 | O | CURRENT | `bg/freequotareset|cleanup` |

### 4.11 安全与合规（SEC）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-SEC-01 | 提示注入防护：规则/引擎/策略/金丝雀 token/攻击向量/严重度矩阵/检测统计 | S | CURRENT | `admin/prompt_injection_handler.go` |
| FR-SEC-02 | 输出合规：关键词/策略/审核队列/反馈 | S | CURRENT | `admin/output_compliance_handler.go` |
| FR-SEC-03 | 敏感词引擎 + 请求消毒（sanitize/secretmask） | M | CURRENT | `security/` |
| FR-SEC-04 | IP 黑名单、安全头、CORS、可信 Origin | M | CURRENT | `middleware/` |
| FR-SEC-05 | 凭据密文静态加密与密钥轮换工具 | M | CURRENT | `secret/`、`cmd/regen-credentials` |
| FR-SEC-06 | admin 审计日志（superAdmin 查询）+ 节点操作审计 + 裁剪 | S | CURRENT | `/api/admin/audit-logs` |
| FR-SEC-07 | 插件 canonical API HMAC 签名 + nonce 防重放 | S | CURRENT | `main.go:6128` |

### 4.12 多租户与用户（TN）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-TN-01 | 租户 CRUD、租户设置（tenant_settings_kv 覆盖平台默认） | M | CURRENT | `admin/tenants.go`、`settings/` |
| FR-TN-02 | 应用层租户隔离（api_key→tenant_id 全链路）+ 补充 RLS（settings/tool 等表） | M | CURRENT | `db/db.go:5083` |
| FR-TN-03 | 用户与角色分级（admin/tenant_admin/platform_ops/superAdmin） | M | CURRENT | `/api/users*` |
| FR-TN-04 | 租户自助只读 API（license/升级/遥测偏好） | O | CURRENT | `tenantops/` |

### 4.13 标注与训练数据（AN）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-AN-01 | 人工标注工作流（样本/首轮样本/批量/统计）+ CSV CLI | O | CURRENT | `admin/annotation_handler.go`、`cmd/llm-gw-annotator` |
| FR-AN-02 | 训练数据 Parquet 导出（含隐私校验） | O | CURRENT | `exporter/`、`cmd/llm-gw-exporter` |
| FR-AN-03 | 流量回放/回测/测试台（真实管线评估） | O | CURRENT | `cmd/traffic-replay`、`cmd/tuning-backtest`、`cmd/auto-testbench` |

### 4.14 运维与数据生命周期（OP）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-OP-01 | 数据生命周期：分区归档/删除、热分区提升、vacuum/reindex、blob 清理 | M | CURRENT | `admin/data_lifecycle_*.go`、`bg.PartitionManager` |
| FR-OP-02 | 备份恢复（validate/recover/recover-all） | M | CURRENT | `/api/admin/backups*` |
| FR-OP-03 | 热配置免重启（settings_kv 30s 轮询 + 即时回调 + YAML reload 端点） | M | CURRENT | `hotconfig/`、`settings/` |
| FR-OP-04 | 后台任务统一装配与优雅停机；`runtime_role`（active/traffic-only）角色拆分 | M | CURRENT | `bg/`、`config/runtime_role.go` |
| FR-OP-05 | DB 降级监控与 fallback buffer 运维端点 | S | CURRENT | `domains/dbdegradation` |
| FR-OP-06 | 功能模块注册表（feature toggles 含能力/依赖/危险级别） | S | CURRENT | `admin/modules.go` |
| FR-OP-07 | 元工具与工具注册/策略、Agent 注册表 | O | CURRENT | `metatools/`、`/api/admin/agents` |
| FR-OP-08 | Vibe Coding 项目运营面板 | O | CURRENT | `vibecoding/` |

### 4.15 交付：许可与自动更新（DL）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-DL-01 | License 在线激活/离线激活/试用/设备绑定/CRL 撤销 | M | CURRENT | `licensing/`、`cmd/license-authority` |
| FR-DL-02 | 自动更新：下载、GPG 验签、安装、回滚、rollout gate | M | CURRENT | `autoupdate/` |
| FR-DL-03 | Bootstrap 引导向导（首次部署） | S | CURRENT | `/api/system/bootstrap/*`、BootstrapWizardView |
| FR-DL-04 | 双模存储 full/lite 一键切换（lite 零外部依赖） | S | CURRENT | `storage/`、`storage_mode_init.go` |

### 4.16 中心管控与插件（CN/PL）

| 编号 | 需求 | 优先级 | 状态 | 实现锚点 |
|---|---|---|---|---|
| FR-CN-01 | 实例注册/心跳监控（30s）/命令下发/runtime alerts | S | CURRENT | `center/` |
| FR-CN-02 | apihub 资产图谱同步与监听 | O | CURRENT | `apihub/`、`bg/apihub_watcher.go` |
| FR-PL-01 | 插件运行时：安装/激活/卸载/Supervisor/健康循环/导航注入 | S | CURRENT | `plugin-runtime/`、`cmd/gateway/plugin_*_init.go` |

## 5. 非功能需求（NFR）

| 编号 | 类别 | 需求 | 现状 |
|---|---|---|---|
| NFR-01 | 性能 | 数据面 P95 低开销代理（上游 120s/流 900s/chunk 300s/首字节 30s 超时预算） | CURRENT（超时矩阵已固化） |
| NFR-02 | 可靠性 | 优雅停机预算（HTTP 23s→worker 5s→遥测→DB 池），请求 WAL 防丢失，多层重试预算 | PARTIAL（统一 request-level retry contract 仍为 TARGET） |
| NFR-03 | 可用性 | 凭据 failover 对客户端透明；Redis 失效降级进程内状态（fail-open/closed 需显式声明） | PARTIAL |
| NFR-04 | 安全 | 生产三密钥缺失 fail-closed；凭据静态加密；secret 不入文档；更新包 GPG 验签 | CURRENT（历史泄露走脱敏轮换专项） |
| NFR-05 | 多租户 | 应用层隔离为主 + 补充 RLS；tenant GUC 缺失回退策略待裁决 fail-closed | PARTIAL（P1 门禁） |
| NFR-06 | 可运维 | 全 worker 统一 owner/Start/Stop/timeout/drain/指标；systemd 停机预算一致 | PARTIAL（P1 收口中） |
| NFR-07 | 可部署 | M1 二进制/M2 compose/M3 K8s sidecar/M4 operator（规划）；lite 模式零依赖 | CURRENT（M4 PLAN） |
| NFR-08 | 可升级 | 迁移幂等（Go ensure* + 数字 SQL）、零停机 `migrate` 子命令、自动回滚 | CURRENT |
| NFR-09 | 可观测 | Prometheus 指标族 + OTel + 结构化日志 + requestid 链路 | CURRENT |
| NFR-10 | 可扩展 | 单进程巨石 + runtime_role 角色拆分 + 插件运行时 | CURRENT（水平拆分为 TARGET） |
| NFR-11 | 国际化 | 前后端各 8 语言（ar/de/en/es/fr/ja/zh-CN/zh-TW） | CURRENT |
| NFR-12 | 数据治理 | 分区归档/保留期/审计裁剪/一致性对账 worker 体系 | CURRENT |
| NFR-13 | 兼容性 | Go 1.27.1；h2c 同端口 HTTP/1.1+2；PG 15+；Redis 7+（lite 模式两者皆免） | CURRENT |

## 6. 全局约束与不变量（设计必须遵守）

1. **dispatch 多层队列是唯一执行路径**（2026-08-17 AUDIT_24H B2b 起）；任何新执行路径必须经 dispatch 接入，`Execute` 中 pipeline 为 nil 视为装配错误。
2. **Session V1 PRIMARY / V2 SHADOW**：V1 写失败即请求失败，V2 影子 fail-open；主读翻转必须过 dual-read 7 天零漂移门禁（`dual_read_validator.go`）。
3. **协议保真**：IR 转换不得静默丢弃 vendor 扩展字段；不可保真字段必须显式记录。
4. **健康判断与资源分配分离**：探测/熔断（健康）与 FP slot/RPM/并发（资源）是两类状态，不得混用同一状态机。
5. **三密钥 fail-closed**：生产环境缺失 JWT/API/DB 密钥直接 panic，禁止降级启动。
6. **secret 卫生**：文档与示例只允许引用环境变量占位符。
7. **并行实现收敛纪律**：同一功能新旧并存期间，新实现必须能独立回滚；退役必须以"零生产调用点"为证据（先例：IR 传输层 ADR）。现存并行对清单见 [`../03-design/01-architecture/parallel-implementations-comparison.md`](../03-design/01-architecture/parallel-implementations-comparison.md)。

## 7. 需求追溯

- 功能需求 → 实现映射：[`functional/FEATURES_CATALOG.md`](./functional/FEATURES_CATALOG.md)（功能域→包→API 前缀→前端页→并行状态）。
- 架构约束 → 设计方案：[`../03-design/01-architecture/architecture/ARCHITECTURE.md`](../03-design/01-architecture/architecture/ARCHITECTURE.md)。
- 已知未满足项与发布门禁：ARCHITECTURE §7（P0/P1/P2）。
- 单特性细化：`functional/FR-*.md`（现有 `FR-selfcheck-timely-recovery.md`，格式模板）。

## 8. 本轮审计登记的现状缺口（GAP）

以下能力**代码已存在但此前需求文档完全未覆盖**，本轮首次纳入 FR/NFR（详见 FEATURES_CATALOG 标注）：

1. 免费资源池/自动发现（FR-FP 域，约 30 个端点）；
2. MaaS 计费商业化闭环（FR-BL-02）；
3. 安全域（提示注入/输出合规/IP 黑名单/伪装，FR-SEC）；
4. Goal 编排 + 托管任务 + 请求存续（FR-DP-10~12）;
5. 交付侧（License/自动更新/双模存储，FR-DL、NFR-07/08）；
6. 标注与训练数据闭环（FR-AN）。

---

**最后更新**: 2026-10-01 · 生成于全码审计轮（四域子代理 + 主代理实证）
**维护团队**: LLM Gateway Team
