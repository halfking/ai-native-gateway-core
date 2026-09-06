# LLM Gateway Go — 功能模块详细指南

> **版本**: v2.5.3  
> **更新日期**: 2026-09-06  
> **目标读者**: 开发者、架构师、运维人员  

---

## 目录

1. [模块总览](#1-模块总览)
2. [数据面模块](#2-数据面模块)
3. [控制面模块](#3-控制面模块)
4. [基础设施模块](#4-基础设施模块)
5. [集成与扩展](#5-集成与扩展)
6. [工具与实用程序](#6-工具与实用程序)

---

## 1. 模块总览

### 1.1 顶层目录结构

```
llm-gateway-go/
├── cmd/                    # 可执行程序入口（30个）
│   ├── gateway/           # 主网关程序 ⭐
│   ├── gateway-v2/        # Pipeline验证入口
│   ├── license-authority/ # License授权服务
│   └── tools/             # 工具集合
├── domains/               # 领域模型层（64个领域）⭐
├── admin/                 # Admin API（453文件）⭐
├── bg/                    # 后台Worker（217文件）⭐
├── web/                   # Vue管理面板 ⭐
├── installer/             # 安装器（独立Go模块）⭐
├── middleware/            # HTTP中间件
├── adapter/               # 协议适配器
├── internal/              # 内部共享代码
├── pkg/                   # 可复用包
├── migrations/            # 数据库迁移（1个，主迁移在 db/migrations/ 和 sql/migrations/）
├── scripts/               # 自动化脚本
├── docs/                  # 文档（110+文件）
└── deploy/                # 部署配置
```

### 1.2 模块分类

| 分类 | 模块数量 | 核心职责 |
|------|----------|----------|
| **数据面** | 20+ | 请求处理、路由、流式中继 |
| **控制面** | 15+ | 管理API、配置、监控 |
| **领域模型** | 64 | 业务逻辑、状态管理 |
| **基础设施** | 30+ | 数据库、缓存、消息队列 |
| **后台任务** | 50+ | 定时任务、数据治理 |
| **工具程序** | 25+ | 开发、测试、运维工具 |

---

## 2. 数据面模块

### 2.1 请求处理流水线

#### 📦 `domains/streaming`
**职责**: 核心请求处理与流式中继

**关键文件**:
```
domains/streaming/
├── handler.go              # HTTP handler入口
├── stream_relay.go         # SSE流式中继核心 ⭐
├── goal_retry_policy.go    # Goal重试策略
├── executors/              # 执行器集合
│   ├── router_scoring.go   # 路由评分器 ⭐
│   ├── candidate_executor.go # 候选执行
│   ├── failover_executor.go  # 故障转移
│   └── retry_executor.go     # 重试执行
├── integrity/              # 流式完整性
│   ├── detector.go         # 增量重复检测 ⭐
│   └── policy.go           # 完整性策略
└── state/                  # 流式状态管理
    ├── context.go          # 请求上下文
    └── metrics.go          # 流式指标
```

**核心能力**:
- ✅ OpenAI/Anthropic/Gemini/Responses 协议兼容
- ✅ SSE流式代理 + 增量完整性检测
- ✅ Pre-stream心跳 + stall-only超时
- ✅ 流式边界保护（首字节后禁止破坏性重试）
- ✅ Vendor字段过滤（统一sanitizer）

**使用示例**:
```go
// 创建流式处理器
handler := streaming.NewHandler(
    router,
    dispatcher,
    integrityDetector,
    retryPolicy,
)

// 处理请求
err := handler.Handle(ctx, request, response)
```

---

#### 📦 `domains/dispatch`
**职责**: 调度分发与资源管理

**关键文件**:
```
domains/dispatch/
├── dispatcher.go           # 主调度器 ⭐
├── queue.go                # 模型/凭据队列
├── forwarder.go            # 上游转发器
├── failover.go             # 故障转移逻辑
├── lifecycle.go            # 生命周期观测
└── resource_gate.go        # 资源门控
```

**核心能力**:
- ✅ 有界队列 + 并发控制
- ✅ FP槽位管理 + RPM/TPM限流
- ✅ 自动failover + 重试调度
- ✅ 生命周期事件发布

---

### 2.2 智能路由系统

#### 📦 `domains/routing`
**职责**: 路由决策与候选发现

**关键文件**:
```
domains/routing/
├── router.go               # 主路由器 ⭐
├── candidate_finder.go     # 候选发现
├── sticky_session.go       # 粘性会话
├── tier_resolver.go        # Tier解析
└── constraints.go          # 路由约束
```

**路由流程**:
```
Client Request
    │
    ├─→ [1] Availability过滤（在线凭据）
    │
    ├─→ [2] Tenant过滤（租户权限）
    │
    ├─→ [3] Protocol过滤（协议兼容）
    │
    ├─→ [4] Model过滤（模型支持）
    │
    ├─→ [5] Tier约束（优先级层级）
    │
    ├─→ [6] Billing轮次（计费策略）
    │
    ├─→ [7] Sticky检查（会话粘性）
    │
    ├─→ [8] P2C/Bandit评分排序 ⭐
    │
    └─→ [9] 返回Top-N候选
```

---

#### 📦 `autoroute`
**职责**: 自动路由决策（`model=auto`）

**关键文件**:
```
autoroute/
├── classifier/             # 任务分类器
│   ├── intent.go           # 意图识别（8类）
│   └── features.go         # 特征提取
├── scorer/                 # 评分器集合
│   ├── quality.go          # 质量评分
│   ├── cost.go             # 成本评分
│   ├── context.go          # 上下文评分
│   ├── latency.go          # 延迟评分
│   └── availability.go     # 可用性评分
├── selector.go             # 模型选择器 ⭐
└── feedback.go             # 反馈优化
```

**8类任务分类**:
1. **代码生成** (Code Generation)
2. **翻译** (Translation)
3. **推理** (Reasoning)
4. **创意写作** (Creative Writing)
5. **数据分析** (Data Analysis)
6. **问答** (Question Answering)
7. **摘要** (Summarization)
8. **对话** (Conversation)

**6维评分**:
- 质量 (Quality): 模型能力评分
- 速度 (Speed): P95延迟
- 成本 (Cost): Token单价
- 上下文 (Context): 上下文窗口大小
- 多模态 (Multimodal): 图像/音频支持
- 可用性 (Availability): 健康状态

---

#### 📦 `domains/ursm`
**职责**: 统一路由状态管理 (Unified Routing State Manager)

**关键文件**:
```
domains/ursm/
├── v2/                     # V2版本 ⭐
│   ├── state_manager.go    # 状态管理器
│   ├── redis_backend.go    # Redis后端
│   ├── lua/                # Lua脚本
│   │   ├── acquire.lua     # 原子获取槽位
│   │   ├── release.lua     # 释放槽位
│   │   └── update.lua      # 更新状态
│   └── metrics.go          # 状态指标
└── legacy/                 # V1兼容层
```

**状态维度**:
- 凭据健康状态（7类错误分级）
- FP槽位占用（并发控制）
- RPM/TPM计数器（限流）
- P95延迟统计
- 成功/失败计数

---

### 2.3 凭据与身份

#### 📦 `domains/credential`
**职责**: 凭据管理与健康监控

**关键文件**:
```
domains/credential/
├── manager.go              # 凭据管理器 ⭐
├── pool.go                 # 凭据池
├── fp_slot.go              # FP槽位管理 ⭐
├── health_tracker.go       # 健康跟踪
├── quota_enforcer.go       # 配额执行
└── rotation.go             # 凭据轮转
```

**凭据生命周期**:
```
注册 → 验证 → 激活 → 监控 → 熔断 → 恢复 → 退役
  ↓      ↓      ↓      ↓      ↓      ↓      ↓
 DB   Health  URSM   Probe  Circuit Probe  Archive
```

**FP槽位机制**:
```go
// 获取槽位（阻塞或超时）
slot, err := fpSlot.Acquire(ctx, credentialID, timeout)
if err != nil {
    return err // 槽位耗尽或超时
}
defer slot.Release() // 确保释放

// 执行上游请求
response, err := upstream.Call(request)
```

---

#### 📦 `domains/identity`
**职责**: 租户身份隧道

**关键文件**:
```
domains/identity/
├── tunnel.go               # 身份隧道 ⭐
├── extractor.go            # 身份提取
├── virtual_ip.go           # 虚拟IP生成
├── virtual_mac.go          # 虚拟MAC生成
└── client_id.go            # ClientID管理
```

**身份隧道流程**:
```
Tenant ID → Virtual Identity → Upstream Headers
    │              │                    │
    │              ├─→ X-Forwarded-For: 10.x.x.x
    │              ├─→ X-Client-MAC: 00:xx:xx:...
    │              └─→ X-Client-ID: tenant_xxx
    │
    └─→ 租户隔离 + 上游伪装
```

---

### 2.4 协议适配

#### 📦 `adapter/unified`
**职责**: 多协议统一适配

**关键文件**:
```
adapter/unified/
├── openai.go               # OpenAI协议
├── anthropic.go            # Anthropic协议
├── gemini.go               # Gemini协议
├── responses.go            # Responses协议
└── converter.go            # 协议转换器
```

**支持协议**:
| 协议 | 端点 | SSE流式 | 状态 |
|------|------|---------|------|
| OpenAI | `/v1/chat/completions` | ✅ | CURRENT |
| Anthropic | `/v1/messages` | ✅ | CURRENT |
| Gemini | `/v1/models/*:generateContent` | ✅ | CURRENT |
| Responses | `/v1/responses` | ✅ | CURRENT |

---

#### 📦 `internal/ir`
**职责**: 中间表示 (Intermediate Representation)

**关键文件**:
```
internal/ir/
├── request.go              # 统一请求IR ⭐
├── response.go             # 统一响应IR
├── message.go              # 消息格式
├── tool.go                 # 工具调用IR
└── streaming.go            # 流式IR
```

**IR设计原则**:
1. **协议无关**: 抽象所有协议共性
2. **扩展字段保留**: `extensions map[string]any`
3. **类型安全**: 强类型 + 验证
4. **向后兼容**: 版本化字段

---

### 2.5 安全与合规

#### 📦 `domains/security`
**职责**: 安全引擎

**关键文件**:
```
domains/security/
├── engine.go               # 安全引擎 ⭐
├── plugins/                # 安全插件
│   ├── auth.go             # 认证插件
│   ├── ratelimit.go        # 限流插件
│   └── firewall.go         # 防火墙插件
└── policy.go               # 安全策略
```

---

#### 📦 `domains/promptinjection`
**职责**: Prompt注入检测

**关键文件**:
```
domains/promptinjection/
├── detector.go             # 检测器 ⭐
├── enhanced/               # 增强检测
│   ├── pattern.go          # 模式匹配
│   ├── semantic.go         # 语义分析
│   └── ml_based.go         # 机器学习
├── rules/                  # 检测规则
└── mitigation.go           # 缓解措施
```

**检测策略**:
- 模式匹配（正则 + 关键词）
- 语义分析（embedding相似度）
- 机器学习分类器
- 行为异常检测

---

#### 📦 `domains/outputcompliance`
**职责**: 输出合规检查

**关键文件**:
```
domains/outputcompliance/
├── checker.go              # 合规检查器 ⭐
├── filters/                # 过滤器
│   ├── sensitive.go        # 敏感信息
│   ├── profanity.go        # 敏感词
│   └── pii.go              # 个人信息
└── policy.go               # 合规策略
```

---

## 3. 控制面模块

### 3.1 Admin API

#### 📦 `admin` (453个Go文件)
**职责**: 管理API与控制台

**目录结构**:
```
admin/
├── handlers/               # API处理器（按模块）
│   ├── tenant/             # 租户管理（30+ handlers）
│   ├── credential/         # 凭据管理（40+ handlers）
│   ├── routing/            # 路由配置（25+ handlers）
│   ├── monitoring/         # 监控仪表盘（35+ handlers）
│   ├── audit/              # 审计查询（20+ handlers）
│   ├── session/            # 会话管理（30+ handlers）
│   ├── stats/              # 统计分析（25+ handlers）
│   ├── config/             # 系统配置（15+ handlers）
│   ├── maas/               # MaaS计费（20+ handlers）
│   └── system/             # 系统管理（15+ handlers）
├── middleware/             # Admin中间件
│   ├── auth.go             # 管理员认证
│   ├── rbac.go             # 基于角色的访问控制
│   └── audit.go            # 操作审计
├── dto/                    # 数据传输对象
├── validation/             # 参数验证
└── server.go               # Admin服务器 ⭐
```

**核心API模块**:

##### 租户管理
```
POST   /api/v1/tenants                    # 创建租户
GET    /api/v1/tenants                    # 列表查询
GET    /api/v1/tenants/:id                # 详情查询
PUT    /api/v1/tenants/:id                # 更新租户
DELETE /api/v1/tenants/:id                # 删除租户
GET    /api/v1/tenants/:id/users          # 租户用户
GET    /api/v1/tenants/:id/quota          # 配额管理
POST   /api/v1/tenants/:id/recharge       # 充值操作
```

##### 凭据管理
```
POST   /api/v1/credentials                # 创建凭据
GET    /api/v1/credentials                # 列表查询
GET    /api/v1/credentials/:id            # 详情查询
PUT    /api/v1/credentials/:id            # 更新凭据
DELETE /api/v1/credentials/:id            # 删除凭据
GET    /api/v1/credentials/:id/health     # 健康状态
POST   /api/v1/credentials/:id/probe      # 手动探测
GET    /api/v1/credentials/matrix         # 可用性矩阵
```

##### 监控仪表盘
```
GET    /api/v1/dashboard/overview         # 总览
GET    /api/v1/dashboard/realtime         # 实时监控
GET    /api/v1/dashboard/stats            # 统计数据
GET    /api/v1/dashboard/health           # 健康状态
GET    /api/v1/dashboard/routing          # 路由分析
```

##### 审计查询
```
GET    /api/v1/audit/requests             # 请求日志
GET    /api/v1/audit/sessions             # 会话记录
GET    /api/v1/audit/operations           # 操作审计
GET    /api/v1/audit/events               # 事件日志
POST   /api/v1/audit/export               # 导出审计
```

---

### 3.2 Vue管理面板

#### 📦 `web` (Vue 3 + TypeScript)
**职责**: 前端管理控制台

**目录结构**:
```
web/
├── src/
│   ├── views/              # 页面组件
│   │   ├── Dashboard.vue   # 仪表盘
│   │   ├── Credentials.vue # 凭据管理
│   │   ├── Tenants.vue     # 租户管理
│   │   ├── Routing.vue     # 路由配置
│   │   ├── Monitoring.vue  # 实时监控
│   │   ├── Audit.vue       # 审计查询
│   │   └── Settings.vue    # 系统设置
│   ├── components/         # 通用组件
│   │   ├── Charts/         # 图表组件
│   │   ├── Tables/         # 表格组件
│   │   ├── Forms/          # 表单组件
│   │   └── Layout/         # 布局组件
│   ├── api/                # API客户端
│   ├── store/              # Pinia状态管理
│   ├── router/             # Vue Router路由
│   ├── composables/        # 组合式API
│   └── utils/              # 工具函数
├── public/                 # 静态资源
└── vite.config.ts          # Vite配置
```

**核心页面**:

##### 仪表盘 (Dashboard)
- 实时请求流（WebSocket）
- 统计图表（ECharts）
- 健康状态监控
- 告警通知

##### 凭据矩阵 (Credential Matrix)
- 19凭据 × 18模型 二维矩阵
- 实时健康状态
- P95延迟展示
- 并发槽位占用

##### 路由全景 (Routing Panorama)
- Sankey路由流向图
- 任务×模型热力图
- 路由决策树
- 成功率分析

##### 请求日志 (Request Logs)
- 13,000+ 请求会话
- 多维过滤器（租户/模型/供应商/时间）
- 请求详情回放
- 导出功能

---

### 3.3 后台Worker系统

#### 📦 `bg` (217个Go文件)
**职责**: 后台定时任务与数据治理

**核心Worker分类**:

##### 健康探测类
```
bg/
├── active_probe_worker.go          # 主动健康探测 ⭐
├── active_probe_executor.go        # 探测执行器
├── active_probe_emitter.go         # 探测任务发射
├── asset_health_probe.go           # 资产健康探测
└── node_health_monitor.go          # 节点健康监控
```

**探测机制**:
- 60s周期性探测
- 失败自动熔断
- 恢复探测（指数退避）
- 多协议探测（OpenAI/Anthropic/Gemini）

##### 路由优化类
```
bg/
├── auto_route_settle_worker.go     # 路由结算 ⭐
├── auto_route_affinity_worker.go   # 路由亲和性
├── auto_route_realtime_listener.go # 实时反馈监听
└── auto_index_refresher.go         # 自动索引刷新
```

**反馈优化流程**:
```
Request Complete → Feedback Event → Redis Queue
    → Settle Worker → Quality Score Update
    → Affinity Cache Refresh → Next Request Routing
```

##### 数据治理类
```
bg/
├── audit_trimmer.go                # 审计数据清理
├── partition_manager.go            # 分区表维护 ⭐
├── archive_worker.go               # 数据归档
├── stats_aggregator.go             # 统计聚合
└── session_digest_worker.go        # 会话摘要生成
```

**分区维护**:
- 自动创建未来分区（提前1个月）
- 自动清理历史分区（保留90天）
- 分区统计信息更新
- 分区索引维护

##### 监控告警类
```
bg/
├── systemmonitor/                  # 系统监测
│   ├── monitor.go                  # 监控主循环 ⭐
│   ├── redis_queue.go              # Redis队列
│   ├── dedup.go                    # 去重逻辑（30s窗口）
│   └── lua/claim.lua               # 原子Claim脚本
├── quality_monitor_worker.go       # 质量监控
└── alert_dispatcher.go             # 告警分发
```

**系统监测流程**:
```
Worker → Redis FIFO Queue → 30s Dedup
    → Old Worker标记 → Monitoring Dashboard
    → Alert Rules → Notification (飞书/邮件)
```

---

## 4. 基础设施模块

### 4.1 数据库

#### 📦 `db`
**职责**: 数据库连接与查询

**关键文件**:
```
db/
├── postgres.go             # PostgreSQL连接池 ⭐
├── transaction.go          # 事务管理
├── query_builder.go        # 查询构建器
├── rls.go                  # RLS上下文设置
└── migration.go            # 迁移工具
```

**连接池配置**:
```go
pool := db.NewPool(&db.Config{
    MaxConns:          100,
    MinConns:          10,
    MaxConnLifetime:   time.Hour,
    MaxConnIdleTime:   15 * time.Minute,
    HealthCheckPeriod: 30 * time.Second,
})
```

---

#### 📦 `migrations` (340+文件)
**职责**: 数据库迁移脚本

**目录结构**:
```
migrations/
├── 000001_initial_schema.up.sql
├── 000002_add_tenants.up.sql
├── 000003_add_credentials.up.sql
├── ...
├── 000340_add_session_v2_shadow.up.sql
└── schema/
    ├── tables/             # 表定义
    ├── views/              # 视图定义
    ├── functions/          # 函数定义
    └── triggers/           # 触发器定义
```

**迁移规范**:
- 幂等性：可重复执行
- 向后兼容：支持滚动升级
- 数据安全：先备份后迁移
- 性能优化：大表分批迁移

---

### 4.2 缓存与状态

#### 📦 `cache`
**职责**: 多层缓存管理

**缓存层级**:
```
L1: 本地内存 (sync.Map)
    ├─ 热点数据（模型映射、租户配置）
    ├─ TTL: 1分钟
    └─ 容量限制: 10,000条
    
L2: Redis (单节点/哨兵/集群)
    ├─ 会话状态、路由状态、限流计数
    ├─ TTL: 可配置（5分钟-24小时）
    └─ LRU淘汰
    
L3: PostgreSQL (持久化)
    └─ 权威数据源
```

**关键文件**:
```
cache/
├── manager.go              # 缓存管理器 ⭐
├── local.go                # L1本地缓存
├── redis.go                # L2 Redis缓存
├── strategy.go             # 缓存策略
│   ├── write_through.go    # 写直达
│   ├── write_back.go       # 写回
│   └── cache_aside.go      # 旁路缓存
└── invalidation.go         # 失效策略
```

---

#### 📦 `redis`
**职责**: Redis客户端封装

**关键文件**:
```
pkg/redis/
├── client.go               # Redis客户端 ⭐
├── sentinel.go             # 哨兵模式
├── cluster.go              # 集群模式
├── lua_scripts.go          # Lua脚本管理
└── pubsub.go               # 发布订阅
```

---

### 4.3 可观测性

#### 📦 `telemetry`
**职责**: 可观测性基础设施

**关键文件**:
```
telemetry/
├── tracer.go               # OpenTelemetry追踪 ⭐
├── metrics.go              # Prometheus指标
├── logger.go               # 结构化日志
└── correlation.go          # 关联ID传播
```

**Trace上下文传播**:
```
HTTP Headers → Context → Upstream Headers
    │              │              │
    ├─ traceparent │              ├─ traceparent
    ├─ tracestate  │              ├─ tracestate
    └─ baggage     │              └─ baggage
                   │
                   └─ Span创建 → Exporter → Collector
```

**核心指标**:
```
# 请求指标
llm_gateway_requests_total              # 请求总数
llm_gateway_request_duration_seconds    # 请求延迟
llm_gateway_request_errors_total        # 错误总数

# 路由指标
llm_gateway_routing_attempts_total      # 路由尝试
llm_gateway_routing_failures_total      # 路由失败
llm_gateway_routing_duration_seconds    # 路由耗时

# 凭据指标
llm_gateway_credential_health_status    # 凭据健康状态
llm_gateway_credential_concurrent_slots # 并发槽位
llm_gateway_credential_ratelimit_hits   # 限流命中

# 系统指标
llm_gateway_goroutines                  # 协程数
llm_gateway_memory_usage_bytes          # 内存使用
llm_gateway_db_connections              # 数据库连接数
```

---

## 5. 集成与扩展

### 5.1 MCP工具网关

#### 📦 `domains/toolexecution`
**职责**: MCP (Model Context Protocol) 工具执行

**关键文件**:
```
domains/toolexecution/
├── executor.go             # 工具执行器 ⭐
├── registry.go             # 工具注册表
├── mcp_client.go           # MCP客户端
├── sandbox.go              # 沙箱环境
└── policy.go               # 执行策略
```

**工具执行流程**:
```
Tool Request → Validation → Sandbox → Execute → Result
    │              │            │         │         │
    │              ├─ Schema    ├─ Timeout  │         ├─ Success
    │              ├─ Auth      ├─ Memory   │         └─ Error
    │              └─ Policy    └─ Network  │
    │                                        │
    └────────────── Audit Log ──────────────┘
```

---

### 5.2 Memora记忆服务

#### 📦 `domains/memory`
**职责**: Memora记忆服务集成

**关键文件**:
```
domains/memory/
├── client/                 # Memora客户端
│   ├── grpc_client.go      # gRPC客户端 ⭐
│   ├── http_client.go      # HTTP客户端
│   └── mock_client.go      # Mock客户端（测试）
├── adapter.go              # 适配器
├── cache.go                # 记忆缓存
└── policy.go               # 记忆策略
```

**记忆流程**:
```
Request → Memory Query → Context Enrichment → LLM Call
    │          │               │                  │
    │          ├─ Vector      ├─ Prompt          ├─ Response
    │          ├─ Semantic    └─ Augmented       │
    │          └─ Temporal                        │
    │                                             │
    └────────── Memory Update ────────────────────┘
```

---

### 5.3 Agent生态系统

#### 📦 `domains/agent-ecosystem`
**职责**: AI Agent编排与管理

**关键文件**:
```
domains/agent-ecosystem/
├── orchestrator.go         # 编排器 ⭐
├── agent_registry.go       # Agent注册表
├── task_queue.go           # 任务队列
├── state_machine.go        # 状态机
└── supervisor.go           # 监督器
```

**Agent编排模式**:
- Sequential（顺序）
- Parallel（并行）
- Conditional（条件）
- Loop（循环）
- Hierarchical（层次）

---

## 6. 工具与实用程序

### 6.1 命令行工具

#### 📦 `cmd/tools`
**职责**: 开发与运维工具集

**工具列表**:

##### 凭据管理
```bash
# 检查凭据可用性
./check-credentials --tenant-id xxx

# 探测凭据健康
./probe-cred --credential-id xxx --model gpt-4

# 重新生成凭据
./regen-credentials --provider openai
```

##### 质量监控
```bash
# 计算质量评分
./calc_quality --model-id xxx --days 7

# 质量监控服务
./quality-monitor --interval 5m

# 质量服务API
./quality-service --port 8080
```

##### 数据治理
```bash
# Bleve索引回填
./bleve-backfill --table requests --batch-size 1000

# URSM迁移
./k2-migrate-ursm --from v1 --to v2

# 会话取证
./sessionforensics --session-id xxx
```

##### 测试工具
```bash
# 自动分类基准测试
./autoclass-bench --dataset eval.jsonl

# 压缩基准测试
./compression-bench --algorithm gzip,zstd,lz4

# 路由测试客户端
./routing-test-client --scenario sticky-session

# 场景驱动测试
./scenario_driver --scenario-file scenarios.yaml
```

##### 配置管理
```bash
# 配置导出
./cfg_dump --output config.yaml

# 环境注入
./env-injector --env production --dry-run
```

---

### 6.2 开发工具

#### 📦 `scripts`
**职责**: 自动化脚本

**脚本列表**:
```bash
scripts/
├── build-upgrade-package.sh    # 构建升级包 ⭐
├── deploy.sh                   # 部署脚本
├── test.sh                     # 测试脚本
├── lint.sh                     # Linter脚本
├── scan-secrets.sh             # 敏感信息扫描 ⭐
├── backup.sh                   # 数据库备份
├── restore.sh                  # 数据库恢复
└── health-check.sh             # 健康检查
```

**升级包构建**:
```bash
# 构建所有平台升级包
./scripts/build-upgrade-package.sh \
    --version v2.5.3 \
    --platforms darwin-amd64,darwin-arm64,linux-amd64,linux-arm64

# 输出
dist/
├── llm-gateway-v2.5.3-darwin-amd64.tar.gz
├── llm-gateway-v2.5.3-darwin-arm64.tar.gz
├── llm-gateway-v2.5.3-linux-amd64.tar.gz
├── llm-gateway-v2.5.3-linux-arm64.tar.gz
└── checksums.txt
```

---

### 6.3 测试套件

#### 测试分类

**单元测试** (`*_test.go`)
```bash
# 运行所有单元测试
go test ./... -short

# 运行特定模块
go test ./domains/streaming/...

# 覆盖率报告
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

**集成测试** (`*_integration_test.go`)
```bash
# 需要真实依赖（PostgreSQL/Redis）
go test ./... -tags=integration

# 指定测试环境
TEST_ENV=ci go test ./... -tags=integration
```

**性能测试** (`*_bench_test.go`)
```bash
# Benchmark测试
go test -bench=. -benchmem ./domains/streaming/

# CPU性能分析
go test -bench=. -cpuprofile=cpu.prof
go tool pprof cpu.prof
```

**E2E测试** (`test/e2e/`)
```bash
# 端到端测试
cd test/e2e
go test -v ./...

# 特定场景
go test -v -run TestStreamingE2E
```

---

## 7. 模块间依赖关系

### 7.1 分层架构

```
┌─────────────────────────────────────────────────────┐
│  Application Layer (cmd/gateway)                    │
│  - Main entry                                       │
│  - Dependency injection                             │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│  Interface Layer (admin/, middleware/)              │
│  - HTTP handlers                                    │
│  - Middleware chain                                 │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│  Domain Layer (domains/)                            │
│  - Business logic                                   │
│  - Domain models                                    │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│  Infrastructure Layer (db/, cache/, telemetry/)     │
│  - Database                                         │
│  - Cache                                            │
│  - Observability                                    │
└─────────────────────────────────────────────────────┘
```

### 7.2 核心依赖图

```
                    cmd/gateway
                        │
        ┌───────────────┼───────────────┐
        │               │               │
    admin/          domains/           bg/
        │               │               │
        ├───────────────┼───────────────┤
        │               │               │
    middleware/     adapter/        internal/
        │               │               │
        └───────────────┴───────────────┘
                        │
        ┌───────────────┼───────────────┐
        │               │               │
       db/           cache/         telemetry/
        │               │               │
    PostgreSQL        Redis        OpenTelemetry
```

---

## 8. 最佳实践

### 8.1 开发建议

1. **遵循DDD**: 新功能优先考虑放在 `domains/`
2. **接口优先**: 定义清晰的接口，方便测试和替换
3. **错误处理**: 使用 `errorsx` 统一错误分类
4. **上下文传播**: 始终传递 `context.Context`
5. **可观测性**: 添加日志、指标、追踪

### 8.2 性能优化

1. **连接池**: 复用HTTP/DB连接
2. **批处理**: 批量操作减少往返
3. **异步处理**: 使用Worker处理耗时任务
4. **缓存策略**: 合理使用三层缓存
5. **索引优化**: 高频查询字段添加索引

### 8.3 安全规范

1. **输入验证**: 所有外部输入必须验证
2. **SQL注入**: 使用参数化查询
3. **敏感信息**: API Key/Token必须脱敏
4. **审计日志**: 敏感操作记录审计
5. **最小权限**: RLS + RBAC双重控制

---

## 9. 扩展阅读

- [项目总览](./PROJECT_OVERVIEW.md) - 项目整体介绍
- [架构设计](./03-design/01-architecture/architecture/ARCHITECTURE.md) - 详细架构
- [API文档](./03-design/01-architecture/architecture/API.md) - API接口
- [部署指南](./06-deployment/README.md) - 部署与运维
- [开发规范](../CONTRIBUTING.md) - 贡献指南

---

**最后更新**: 2026-09-06  
**维护团队**: LLM Gateway Team
