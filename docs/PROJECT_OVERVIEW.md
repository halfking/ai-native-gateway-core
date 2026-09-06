# LLM Gateway Go — 项目总览与架构设计

> **版本**: v2.5.3  
> **更新日期**: 2026-09-06  
> **状态**: 生产运行中  

---

## 1. 项目概述

### 1.1 项目定位

LLM Gateway Go 是一个**企业级大语言模型网关系统**，为企业提供安全、合规、低成本的全球大模型统一接入能力。该系统已在生产环境部署运行，支持多租户、智能路由、流量治理、成本优化等核心能力。

### 1.2 核心价值

- **统一接入**: 兼容 OpenAI/Anthropic/Gemini/国产模型等多种协议
- **智能路由**: 基于成本/质量/延迟的多维度自动路由决策
- **多租户隔离**: 38+ 表 RLS 行级安全，完整租户数据隔离
- **流量治理**: Token 限流、语义缓存、提示词压缩
- **企业级部署**: 支持离线激活、自动升级、多环境部署
- **全链路可观测**: 完整审计、监控、追踪能力

### 1.3 技术栈

| 类别 | 技术选型 |
|------|----------|
| **核心语言** | Go 1.25.0 |
| **Web框架** | Gin/Echo (数据面), Vue 3 + TypeScript (管理面板) |
| **数据库** | PostgreSQL 15+ (RLS + 分区表 + 列存) |
| **缓存/状态** | Redis 7+ (URSM状态、限流、会话、队列) |
| **存储** | Aliyun OSS / AWS S3 (请求体归档) |
| **可观测性** | OpenTelemetry + Prometheus + Grafana |
| **部署** | Docker/K8s/二进制 (M1-M4 四种模式) |

---

### 1.4 代码统计（基于真实代码扫描，2026-09-06）

| 指标 | 数值 | 说明 |
|------|------|------|
| Go源文件总数 | 22,921 个 | `find . -name "*.go" -type f \| wc -l` |
| Admin API文件 | 453 个 | `find ./admin -name "*.go" -type f \| wc -l` |
| 后台Worker文件 | 217 个 | `find ./bg -name "*.go" -type f \| wc -l` |
| 领域模块数量 | 64 个 | `ls ./domains/ \| wc -l` (一级目录) |
| 数据库迁移 | 757 个 | `find ./sql/migrations -name "*.sql" \| wc -l` |
| 单元测试文件 | 5,000+ 个 | `*_test.go` 文件 |
| 可执行程序 | 30 个 | `ls ./cmd/ \| wc -l` |

---

## 2. 系统架构设计

### 2.1 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                       客户端层 (Clients)                          │
│   AI Agent / API调用方 / 管理员控制台                              │
└────────────────────────┬────────────────────────────────────────┘
                         │ OpenAI/Anthropic/Gemini Protocol
                         │ HTTP + SSE Stream
┌────────────────────────▼────────────────────────────────────────┐
│                    LLM Gateway (cmd/gateway)                     │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │              数据面 (Data Plane)                          │   │
│  │  认证 → IR转换 → 自动路由 → 候选执行 → 上游中继          │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │            控制面 (Control Plane)                         │   │
│  │  Admin API (453文件) + Vue SPA + 后台Worker (217文件)    │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │                持久化层 (Storage)                         │   │
│  │  PostgreSQL (租户/凭据/路由/审计) + Redis (状态/缓存)     │   │
│  └─────────────────────────────────────────────────────────┘   │
└───────────────────────┬────────────────────┬────────────────────┘
                        │                    │
         ┌──────────────▼──────────┐  ┌─────▼──────────────────┐
         │  ai-session-manager     │  │  ai-native-maintain    │
         │  (会话投影/分析/治理)    │  │  (License/升级/分发)   │
         └─────────────────────────┘  └────────────────────────┘
```

### 2.2 请求处理流程

```
Client Request
    │
    ├─→ [1] 认证 & 租户解析 (middleware/authentication)
    │
    ├─→ [2] 协议解析 & IR转换 (adapter/unified, internal/ir)
    │       • OpenAI/Anthropic/Gemini/Responses 协议归一
    │
    ├─→ [3] 安全检查 (domains/hooks/security, domains/promptinjection)
    │       • Prompt注入检测
    │       • 输出合规检查
    │
    ├─→ [4] 智能路由决策 (domains/streaming/executors)
    │       • model=auto 自动选模型
    │       • 候选凭据发现 (availability/tier/billing/sticky)
    │       • P2C/Bandit 评分排序
    │
    ├─→ [5] 资源治理 (domains/credential, ratelimit)
    │       • FP槽位并发控制
    │       • RPM/TPM 限流
    │       • 语义缓存命中检查
    │
    ├─→ [6] 上游调用 (domains/dispatch, domains/streaming)
    │       • HTTP/SSE 流式中继
    │       • Vendor字段过滤 (internal/vendorstrip)
    │       • 流式完整性检测 (domains/streaming/integrity)
    │
    ├─→ [7] 重试 & 容错 (errorsx, domains/streaming/goal_retry_policy)
    │       • Request级重试策略
    │       • Failover候选路由
    │       • 熔断机制
    │
    └─→ [8] 审计 & 可观测 (domains/hooks/audit, telemetry)
            • Request WAL归档
            • Usage/Ledger计费
            • OTel Trace & Metrics
            • Session快照 (v2 shadow)
```

### 2.3 核心领域模型

项目采用 **Domain-Driven Design (DDD)** 分层架构，核心领域位于 `domains/` 目录：

| 领域 | 路径 | 职责 |
|------|------|------|
| **流式处理** | `domains/streaming` | 核心请求处理、SSE中继、候选执行 |
| **调度分发** | `domains/dispatch` | 模型/凭据队列、forwarder、failover |
| **凭据管理** | `domains/credential` | 凭据健康、FP池、并发槽位 |
| **路由状态** | `domains/routing`, `domains/ursm` | URSM状态管理、路由决策 |
| **身份认证** | `domains/authentication`, `domains/identity` | API Key验证、租户身份隧道 |
| **会话管理** | `domains/session` (v1), `domains/session/v2` | 会话生命周期、快照、持久化 |
| **审计钩子** | `domains/hooks/audit` | 请求审计、流完整性、事件记录 |
| **安全合规** | `domains/security`, `domains/promptinjection` | Prompt注入检测、输出合规 |
| **模型质量** | `domains/modelquality` | 质量评分、特色模型推荐 |
| **自动路由** | `domains/autoroute` | Cost/Quality策略、反馈优化 |
| **工具执行** | `domains/toolexecution` | MCP工具调用、编排 |
| **记忆集成** | `domains/memory` | Memora记忆服务集成 |

---

## 3. 核心功能模块

### 3.1 License管理与分发

**位置**: `licensing/`, `installer/`

- **在线激活**: 通过主控端 `llm.kxpms.cn` 实时激活，JWT Token 24h自动续期
- **离线激活**: 完全断网环境的 license 授权（request.req → license.dat）
- **试用模式**: 7天免费试用（1租户 / 基础API）
- **设备绑定**: 基于硬件指纹的设备管理，CRL撤销列表支持
- **实例注册**: 启动时自动向主控端注册，60s心跳上报状态（online/degraded/offline）

**关键文件**:
- `licensing/client/activation.go` - 激活客户端
- `licensing/validator/` - License验证器
- `installer/internal/activator/` - 安装器激活模块

### 3.2 智能路由系统

**位置**: `domains/streaming/executors/`, `domains/routing/`, `autoroute/`

#### 双层路由架构

**L1 选模型** (`model=auto`):
- Prompt → 8类任务分类（代码/翻译/推理/创意...）
- 6维评分（质量/速度/成本/上下文/多模态/可用性）
- Profile锁定 + 特色模型推荐

**L2 选凭据**:
- 模型解吸 → Tier回退 → 计费轮次 → P2C评分 → 执行/熔断
- Sticky Session（会话粘性保持）
- 延迟感知（P95 + 并发压力SWRR）

#### 评分策略

| 策略 | 文件 | 说明 |
|------|------|------|
| **Cost-Optimized** | `router_scoring.go` | 成本优先，生产默认 |
| **Quality-Aware** | `autoroute/scorer/quality.go` | 质量优先 |
| **Context-Aware** | `autoroute/scorer/context.go` | 上下文窗口优先 |
| **Cache-Optimized** | `cache/scorer.go` | 缓存命中优先 |
| **Headroom** | `autoroute/scorer/headroom.go` | 剩余配额优先 |

#### 重试策略

**位置**: `domains/streaming/goal_retry_policy.go`, `errorsx/`

- **Goal RetryPolicy**: 租户可覆盖的重试策略（cost-mode preset）
- **Request级重试**: 首字节前安全重试边界
- **Failover**: 候选路由自动切换
- **熔断**: 7类错误分级（Auth/RateLimit/ContextLength/ServerError...）

**关键文件**:
- `domains/streaming/goal_retry_policy.go` - Goal重试策略
- `errorsx/classify.go` - 错误分类器
- `errorsx/failover_policy.go` - Failover策略

### 3.3 凭据健康管理

**位置**: `domains/credential/`, `domains/health/`, `credentialhealth/`

- **双层状态**: 内存 + Redis 双层健康状态
- **7类错误分级**: Auth/RateLimit/ContextLength/ServerError/Network/Timeout/Unknown
- **主动探测**: `bg/active_probe_worker.go` 定期健康检查
- **指纹池**: 50+ UA + 35 Accept-Language + 11 utls profile 自适应伪装
- **并发控制**: FP槽位管理，动态并发限制

**关键组件**:
- `domains/credential/fp_slot.go` - FP槽位管理
- `domains/health/composite.go` - 健康状态聚合
- `bg/active_probe_executor.go` - 探测执行器

### 3.4 流式处理与完整性

**位置**: `domains/streaming/`, `domains/streaming/integrity/`

- **协议中继**: OpenAI/Anthropic/Gemini SSE流式代理
- **增量完整性检测**: 重复内容检测 + 异常事件审计（默认record，可abortable）
- **Vendor字段过滤**: `internal/vendorstrip/` 统一过滤上游厂商私有字段
- **流式边界**: 首字节后遵循流一致性，禁止破坏性重试
- **Pre-stream心跳**: Anthropic header透传 + stall-only超时（无wall-clock cap）

**关键文件**:
- `domains/streaming/stream_relay.go` - 流式中继核心
- `domains/streaming/integrity/detector.go` - 完整性检测
- `internal/vendorstrip/openai.go` - OpenAI字段过滤

### 3.5 多租户与身份隧道

**位置**: `domains/tenant/`, `domains/identity/`, `middleware/`

- **RLS隔离**: 38+ 表行级安全（Row-Level Security）
- **身份隧道**: Virtual IP/MAC/ClientID 租户标识传递
- **凭据池**: 每租户独立凭据池管理
- **配额管理**: 租户级 Token/RPM/TPM 限流
- **MaaS计费**: 套餐 + 积分 + 加油包三段式计费

**关键文件**:
- `domains/tenant/resolver.go` - 租户解析
- `domains/identity/tunnel.go` - 身份隧道
- `middleware/tenant_context.go` - 租户上下文

### 3.6 会话管理 (Sessions V2)

**位置**: `domains/session/v2/`, `domains/sessionstate/`

- **快照持久化**: 会话快照 + 增量更新
- **Shadow模式**: V2 shadow persistence，逐步迁移
- **状态同步**: Gateway → ASM Outbox 异步同步
- **请求详情**: 完整请求体/响应体归档（OSS/S3）
- **Session分析**: `domains/sessiondigest/` 会话摘要生成

**关键文件**:
- `domains/session/v2/persister.go` - V2持久化
- `domains/sessionstate/manager.go` - 状态管理
- `domains/requestdetail/archiver.go` - 请求体归档

### 3.7 后台Worker系统

**位置**: `bg/` (217个Go文件)

核心Worker（部分列举）:

| Worker | 文件 | 职责 |
|--------|------|------|
| **主动探测** | `active_probe_worker.go` | 凭据健康探测 |
| **自动路由结算** | `auto_route_settle_worker.go` | 路由反馈收集 |
| **审计清理** | `audit_trimmer.go` | 审计数据清理 |
| **分区管理** | `partition_manager.go` | 分区表自动维护 |
| **质量监控** | `quality_monitor_worker.go` | 模型质量监控 |
| **系统监测** | `systemmonitor/monitor.go` | Redis队列 + 30s去重 |
| **统计聚合** | `stats_aggregator.go` | 统计数据聚合 |
| **会话摘要** | `session_digest_worker.go` | 会话摘要生成 |

**启动控制**: `cmd/gateway/main.go` 统一装配，支持 graceful shutdown

### 3.8 Admin API与管理面板

**位置**: `admin/` (453个Go文件), `web/` (Vue 3 + TS)

#### Admin API模块

- **租户管理**: CRUD、配额、计费、用户管理
- **凭据管理**: 凭据CRUD、健康监控、FP槽位
- **路由配置**: 路由规则、自动路由策略、模型映射
- **监控仪表盘**: 实时请求流、统计图表、健康状态
- **审计查询**: 请求日志、会话回放、审计追溯
- **系统配置**: Feature Flag、热配置、系统参数

#### Vue管理面板

**位置**: `web/src/`

- **双主题**: Light/Dark主题切换
- **实时监控**: WebSocket实时请求流
- **多维过滤**: 13,000+请求会话，按租户/模型/供应商/时间切片
- **数据可视化**: ECharts图表、Sankey路由流向、热力图
- **凭据矩阵**: 19凭据 × 18模型二维可用性矩阵

**技术栈**:
- Vue 3 + TypeScript + Vite
- Element Plus UI组件
- ECharts 数据可视化
- Vue Router + Pinia 状态管理

### 3.9 数据库设计

**位置**: `migrations/`, `sql/`

#### 核心表（部分）

| 表名 | 说明 | RLS |
|------|------|-----|
| `tenants` | 租户主表 | ✓ |
| `credentials` | 凭据池 | ✓ |
| `requests` | 请求日志 | ✓ |
| `sessions` | 会话记录 | ✓ |
| `usage_records` | 用量记录 | ✓ |
| `routing_attempts` | 路由尝试 | ✓ |
| `credential_health` | 凭据健康 | ✓ |
| `auto_route_selections` | 自动路由选择 | ✓ |
| `model_quality_metrics` | 模型质量指标 | - |

#### 高级特性

- **分区表**: 按月分区 + 自动维护（`bg/partition_manager.go`）
- **列存表**: Citus列存 + Body切分（`domains/requestdetail/`）
- **视图**: 38+ 视图支持复杂查询
- **索引优化**: GiST/GIN全文检索、BRIN时序索引

### 3.10 部署与升级

**位置**: `installer/`, `deploy/`, `scripts/`

#### 四种部署模式

| 模式 | 说明 | 适用场景 |
|------|------|----------|
| **M1** | 二进制 + systemd | 单机离线部署 |
| **M2** | Docker Compose | 单机快速部署 |
| **M3** | K8s Sidecar | 生产集群部署 |
| **M4** | K8s Operator | 声明式管理（规划中） |

#### 升级机制

**位置**: `installer/internal/upgrader/`, `autoupdate/`

- **在线升级**: 自动检查更新（6h周期）+ 一键升级
- **离线升级**: U盘携带升级包 + 本地安装
- **自动备份**: 升级前自动备份 + 失败自动回退
- **健康验证**: 升级后5s健康检查，失败自动回退
- **多平台支持**: darwin-amd64/arm64, linux-amd64/arm64

**关键文件**:
- `installer/cmd/llm-gw-installer/main.go` - 安装器入口
- `installer/internal/upgrader/client.go` - 升级客户端
- `scripts/build-upgrade-package.sh` - 升级包构建

---

## 4. 技术特色

### 4.1 性能优化

- **P2C负载均衡**: Power of Two Choices 算法选择最优凭据
- **P95延迟感知**: 并发压力感知打分 + tier-plane SWRR
- **语义缓存**: 基于embedding的语义缓存命中
- **提示词压缩**: 自动压缩长提示词，节省Token成本
- **连接池**: HTTP连接池 + 数据库连接池优化

### 4.2 可靠性保障

- **熔断机制**: 7类错误自动熔断 + 探测恢复
- **Graceful Shutdown**: 30s优雅关闭，等待请求完成
- **Request WAL**: 请求Write-Ahead Log，防止数据丢失
- **DLQ**: Dead Letter Queue 失败消息持久化
- **磁盘回退**: Redis不可用时回退到磁盘存储

### 4.3 安全合规

- **Prompt注入检测**: `domains/promptinjection/` 检测恶意Prompt
- **输出合规检查**: `domains/outputcompliance/` 检查输出内容
- **敏感信息脱敏**: `domains/secretmask/` API Key/Token脱敏
- **审计完整**: 全链路审计 + 43轮审计 L1=0
- **TLS 1.3**: Ed25519签名 + 严格证书验证

### 4.4 可观测性

- **OpenTelemetry**: 全链路追踪 + 上下文传播
- **Prometheus**: 200+ Metrics指标暴露
- **结构化日志**: 分级日志 + 租户上下文
- **Grafana仪表盘**: 预置监控大盘
- **慢查询监控**: PostgreSQL慢查询自动捕获

---

## 5. 代码统计

```
Go源文件总数:     22,921 个
Admin API文件:      453 个
后台Worker文件:     217 个
领域模块:           85+ 个 (domains/)
数据库迁移:         340+ 个 (migrations/)
测试文件:         5,000+ 个 (*_test.go)
文档文件:           110+ 个 (docs/)
```

---

## 6. 开发规范

### 6.1 代码组织

- **DDD分层**: `domains/` 领域模型，`admin/` 控制面，`bg/` 后台任务
- **依赖注入**: Composition Root在 `cmd/gateway/main.go`
- **接口设计**: 面向接口编程，方便测试和替换

### 6.2 测试策略

- **单元测试**: 核心逻辑100%覆盖
- **集成测试**: `*_integration_test.go` 数据库/Redis真实依赖
- **性能测试**: `cmd/compression-bench/` 等benchmark工具
- **E2E测试**: `test/` 端到端场景测试

### 6.3 Git工作流

- **主分支**: `main` - 生产就绪代码
- **双仓库**: 
  - `codeup` (origin) - 日常开发
  - `github` - 公开镜像（自动严格扫描）
- **提交规范**: Conventional Commits (feat/fix/docs/refactor...)
- **Pre-push扫描**: 49条规则敏感信息扫描

---

## 7. 路线图

### 7.1 已完成 (v2.5.x)

✅ 双层智能路由  
✅ 多租户RLS隔离  
✅ Sessions V2 Shadow  
✅ 流式完整性检测  
✅ License管理与分发  
✅ 四种部署模式  
✅ Admin API + Vue管理面板  
✅ 自动升级与回滚  

### 7.2 进行中 (v2.6.x)

🚧 MCP工具网关全量上线  
🚧 Sessions V2 全量切换  
🚧 Fusion多模态增强  
🚧 A2A协议集成  
🚧 K8s Operator (M4部署模式)  

### 7.3 规划中 (v2.7.x+)

📋 Multi-Region部署  
📋 GraphQL API  
📋 WebAssembly插件系统  
📋 AI Agent编排引擎  

---

## 8. 参考文档

### 8.1 核心文档

- [架构设计](./03-design/01-architecture/architecture/ARCHITECTURE.md) - 系统架构详细设计
- [请求流程](./03-design/01-architecture/architecture/runtime-request-flow.md) - 请求处理流程
- [路由状态](./03-design/01-architecture/architecture/routing-and-state.md) - 路由与状态管理
- [部署指南](./06-deployment/README.md) - 部署与运维

### 8.2 模块文档

- [智能路由](./design/AUTO_SELECTION_SPEC.md) - 自动路由规格
- [模型质量](./model-quality/README.md) - 模型质量监控
- [会话管理](./user-guide/session-management.md) - 会话管理指南
- [安全特性](./security/SECURITY_FEATURES.md) - 安全功能说明

### 8.3 运维文档

- [快速启动](./operations/QUICKSTART.md) - 快速启动指南
- [故障排查](./operations/troubleshooting-guide.md) - 故障排查手册
- [升级指南](./operations/UPGRADE.md) - 升级操作指南
- [监控告警](./monitoring/README.md) - 监控配置指南

---

## 9. 联系方式

- **项目地址**: https://github.com/halfking/SI-LLM-Gateway
- **内部仓库**: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
- **文档索引**: [docs/INDEX.md](./INDEX.md)
- **变更日志**: [docs/changelogs/](./changelogs/)

---

**最后更新**: 2026-09-06  
**维护团队**: LLM Gateway Team
