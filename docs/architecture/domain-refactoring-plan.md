# LLM Gateway Go 领域驱动架构重构方案

> **版本**: v2.0  
> **日期**: 2026-06-25  
> **状态**: Design Approved  
> **作者**: Architecture Team  

## 执行摘要

本方案对 llm-gateway-go 进行领域驱动的架构重构，将现有 38,336 行代码重组为 16 个内聚领域（9 个核心 + 7 个横切），通过事件驱动架构实现领域间松耦合，引入可插拔会话检查器框架和智能体生态系统管理能力。

**核心目标**：
- ✅ 测试覆盖率 90%+（当前 domain 91.4%）
- ✅ 循环依赖为 0（包依赖图是 DAG）
- ✅ 新增供应商 < 0.5 天（扩展性提升 6 倍）

**时间线**: 3 周（4 个 Phase，AI 团队并行执行）

---

## 一、架构概览

### 1.1 分层模型

```
┌─────────────────────────────────────────────────────────────────┐
│                    HTTP Handler Layer                           │
│                (接收请求 → 启动 Pipeline)                         │
└────────────────────────┬────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────────┐
│                 Hook Pipeline (横切关注点)                        │
│  认证 → 安全 → 缓存 → 压缩 → 检查器 → 审计 → 监控 → 智能体发现   │
└────────────────────────┬────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────────┐
│              Request Envelope (IR 中间表示)                      │
│         domain.RequestEnvelope + IR Protocol Layer              │
└────────────────────────┬────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────────┐
│                   核心领域层 (9 个领域)                           │
│  认证 | 租户 | 客户识别 | 会话 | 路由 | 凭据 | 供应商 | 转换 | 流式 │
└────────────────────────┬────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Event Bus (领域间通信)                         │
│            内存事件总线 (关键路径) + Redis (审计)                 │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 16 个领域清单

#### 核心领域（9 个）

| 序号 | 领域 | 职责 | 现有代码复用 |
|------|------|------|-------------|
| 1 | 用户认证 | API Key/JWT 验证、RBAC | `auth/` → 移动到 `domains/authentication/` |
| 2 | 租户管理 | 配额、策略、RLS | `settings/` 部分 → `domains/tenant/` |
| 3 | 客户识别 | 虚拟身份隧道 | `identity/` → `domains/identity/` |
| 4 | 会话管理 | gw_session_id、粘性 | `sessions/` → `domains/session/` |
| 5 | 模型路由 | 候选规划、策略 | `routing/` → `domains/routing/` |
| 6 | 凭据管理 | 熔断、并发、解密 | `credentialstate/` + `circuit/` + `limiter/` → `domains/credential/` |
| 7 | 供应商管理 | 健康探测、配置 | `provider/` → `domains/provider/` |
| 8 | 协议转换 | IR 中间表示、4 象限 | `transform/` + `transport/` → `domains/transformation/` |
| 9 | 流式转发 | SSE 中继、背压 | `relay/` → `domains/streaming/` |

#### 横切领域（7 个）

| 序号 | 领域 | 职责 | 现有代码复用 |
|------|------|------|-------------|
| 10 | 会话缓存 | L1/L2/L3 缓存 | `cache/` + `sessions/session_cache.go` → `domains/hooks/cache/` |
| 11 | 会话压缩 | LCS + LLM 总结 | `compressor/` → `domains/hooks/compression/` |
| 12 | 安全检查 | 意图识别、威胁检测 | `security/armor/` + 🆕 `intent_analyzer.go` → `domains/hooks/security/` |
| 13 | 审计日志 | 批量写入、DLQ | `audit/` + `telemetry/` → `domains/hooks/audit/` |
| 14 | 可观测性 | Prometheus、Trace | `observability/` → `domains/hooks/observability/` |
| 15 | 工具拦截 | Meta-tool 扩展 | `relay/metatool_interceptor.go` → `domains/hooks/tools/` |
| 16 | 智能体生态 | 发现、行为分析、能力集成 | 🆕 `domains/agent-ecosystem/` |

---

## 二、现有代码复用策略

### 2.1 直接移动（无需重写，70%）

这些包逻辑正确，只需移动到新的领域目录：

| 现有包 | 移动到 | 改动 |
|--------|--------|------|
| `identity/` | `domains/identity/` | 仅修改 import 路径 |
| `sessions/` | `domains/session/` | 拆分 `session_cache.go` 到 hooks/cache |
| `compressor/` | `domains/hooks/compression/` | 保留所有文件 |
| `transform/` + `transport/` | `domains/transformation/` | 合并两个包 |
| `auth/` | `domains/authentication/` | 仅移动 |
| `provider/` | `domains/provider/` | 仅移动 |
| `observability/` | `domains/hooks/observability/` | 仅移动 |

### 2.2 重组（需拆分/合并，20%）

| 现有包 | 操作 | 目标 |
|--------|------|------|
| `routing/` | 拆分 | `executor_*.go` → `domains/streaming/`<br>`route_resolver.go` → `domains/routing/` |
| `credentialstate/` + `circuit/` + `limiter/` | 合并 | `domains/credential/` |
| `relay/` | 拆分 | `metatool_interceptor.go` → `domains/hooks/tools/`<br>`handler.go` → `domains/streaming/` |
| `settings/` | 拆分 | 租户策略 → `domains/tenant/`<br>系统配置 → `config/` |

### 2.3 新增（10%）

| 功能 | 文件 |
|------|------|
| Hook Pipeline 框架 | `domains/pipeline/` (全新) |
| 会话检查器编排 | `domains/hooks/session-inspector/` (全新) |
| 智能体生态系统 | `domains/agent-ecosystem/` (全新) |
| 事件总线 | `eventbus/` (全新) |
| 安全意图识别 | `domains/hooks/security/intent_analyzer.go` (全新) |

---

## 三、3 周迁移计划（4 个 Phase）

### Phase 0: 准备阶段（2 天）

**目标**: 搭建基础设施，不影响现有功能

#### 任务清单

- [ ] **T0.1**: 创建新目录结构（`domains/`、`eventbus/`）
- [ ] **T0.2**: 实现内存事件总线（`eventbus/memory_bus.go`）
- [ ] **T0.3**: 实现 Hook Pipeline 框架（`domains/pipeline/`）
- [ ] **T0.4**: 编写迁移脚本（`scripts/migrate-to-domains.sh`）
- [ ] **T0.5**: 设置 CI 验证（循环依赖检测、测试覆盖率）

#### 验收标准

```bash
# 编译通过
go build ./domains/pipeline/...
go build ./eventbus/...

# 测试通过
go test ./domains/pipeline/... -cover
# 期望: 覆盖率 ≥ 80%

# 无循环依赖
go mod graph | scripts/check-cycles.sh
# 期望: 0 cycles
```

---

### Phase 1: 核心领域迁移（1 周）

**目标**: 迁移 9 个核心领域，保持功能一致性

#### 并行工作流（3 个 AI Agent）

**Agent 1: 身份与会话组**
- [ ] **T1.1**: 迁移 `identity/` → `domains/identity/`
- [ ] **T1.2**: 迁移 `sessions/` → `domains/session/`（拆分缓存部分）
- [ ] **T1.3**: 迁移 `auth/` → `domains/authentication/`
- [ ] **T1.4**: 集成到 Pipeline（Phase: PreAuthentication ~ PostAuthentication）

**Agent 2: 路由与凭据组**
- [ ] **T1.5**: 重组 `routing/` → `domains/routing/`（只保留路由逻辑）
- [ ] **T1.6**: 合并 `credentialstate/ + circuit/ + limiter/` → `domains/credential/`
- [ ] **T1.7**: 迁移 `provider/` → `domains/provider/`
- [ ] **T1.8**: 集成到 Pipeline（Phase: Routing ~ PostRouting）

**Agent 3: 转换与流式组**
- [ ] **T1.9**: 合并 `transform/ + transport/` → `domains/transformation/`
- [ ] **T1.10**: 拆分 `relay/` → `domains/streaming/`（保留流式逻辑）
- [ ] **T1.11**: 拆分 `routing/executor_*.go` → `domains/streaming/`
- [ ] **T1.12**: 集成到 Pipeline（Phase: Transform ~ Response）

#### 验收标准

```bash
# 所有核心领域编译通过
go build ./domains/{identity,session,authentication,routing,credential,provider,transformation,streaming}/...

# 测试覆盖率
go test ./domains/... -cover
# 期望: 平均覆盖率 ≥ 85%

# 集成测试
./scripts/e2e-core-domains.sh
# 期望: 请求流程完整，响应与旧版一致

# 无循环依赖
scripts/check-cycles.sh
# 期望: 0 cycles
```

---

### Phase 2: 横切领域迁移（1 周）

**目标**: 迁移 7 个横切领域，接入 Hook Pipeline

#### 并行工作流（3 个 AI Agent）

**Agent 1: 缓存与压缩组**
- [ ] **T2.1**: 迁移 `cache/` + `sessions/session_cache.go` → `domains/hooks/cache/`
- [ ] **T2.2**: 实现 `CacheLookupHook` + `CacheSaveHook`
- [ ] **T2.3**: 迁移 `compressor/` → `domains/hooks/compression/`
- [ ] **T2.4**: 实现 `CompressionHook`（包装现有 SessionCompressor）

**Agent 2: 安全与审计组**
- [ ] **T2.5**: 迁移 `security/armor/` → `domains/hooks/security/`
- [ ] **T2.6**: 实现意图识别器（`intent_analyzer.go`）
- [ ] **T2.7**: 实现威胁检测器（`threat_detector.go`）
- [ ] **T2.8**: 迁移 `audit/` + `telemetry/` → `domains/hooks/audit/`
- [ ] **T2.9**: 实现 `AuditLogHook`（异步批量写入）

**Agent 3: 监控与工具组**
- [ ] **T2.10**: 迁移 `observability/` → `domains/hooks/observability/`
- [ ] **T2.11**: 实现 `TracingHook` + `MetricsHook`
- [ ] **T2.12**: 迁移 `relay/metatool_interceptor.go` → `domains/hooks/tools/`
- [ ] **T2.13**: 实现 `ToolInterceptionHook`

#### 验收标准

```bash
# 所有横切领域编译通过
go build ./domains/hooks/...

# 测试覆盖率
go test ./domains/hooks/... -cover
# 期望: 平均覆盖率 ≥ 80%

# Hook 集成测试
./scripts/e2e-hook-pipeline.sh
# 期望: 14 个 Stage 全部执行，无错误

# 性能测试（确保 Hook 不增加延迟）
go test -bench=. ./domains/pipeline/
# 期望: P99 延迟增加 < 10ms
```

---

### Phase 3: 智能体生态与会话检查器（4 天）

**目标**: 实现新增能力，扩展系统

#### 串行工作流

**Day 1-2: 智能体生态系统**
- [ ] **T3.1**: 实现智能体注册表（`agent_registry.go`）
- [ ] **T3.2**: 实现行为分析器（`behavior_analyzer.go`）
- [ ] **T3.3**: 实现能力中心（`capability_hub.go`）
- [ ] **T3.4**: 实现信任评分引擎（`trust_scorer.go`）
- [ ] **T3.5**: 创建数据库表（`migrations/xxx_agent_ecosystem.sql`）
- [ ] **T3.6**: 实现 `AgentDiscoveryHook`
- [ ] **T3.7**: 实现 `BehaviorLoggingHook`

**Day 3-4: 会话检查器框架**
- [ ] **T3.8**: 实现检查器接口（`session_inspector.go`）
- [ ] **T3.9**: 实现编排引擎（`inspector_orchestrator.go`）
- [ ] **T3.10**: 实现内置检查器（IntentAnalysis、ThreatDetection、ContentFilter）
- [ ] **T3.11**: 实现 `SessionInspectionHook`
- [ ] **T3.12**: 集成到 Pipeline（Phase: PreTransform）

#### 验收标准

```bash
# 智能体生态编译通过
go build ./domains/agent-ecosystem/...

# 数据库迁移成功
psql -d llm_gateway -f migrations/xxx_agent_ecosystem.sql

# 智能体注册测试
curl -X POST https://llmgateway.internal.example.com/api/admin/agents \
  -H "X-Agent-ID: test-agent" \
  -H "X-Agent-Name: Test Agent" \
  -d '{"category":"coding"}'
# 期望: 200 OK，agents 表新增记录

# 会话检查器测试
go test ./domains/hooks/session-inspector/... -v
# 期望: 所有检查器通过，同步/异步模式正常

# 行为分析测试
./scripts/e2e-agent-behavior.sh
# 期望: agent_behavior_events 表有日志
```

---

### Phase 4: 清理与优化（2 天）

**目标**: 删除旧代码，优化性能，完成文档

#### 任务清单

- [ ] **T4.1**: 删除旧包（标记 deprecated，3 个月后删除）
  ```bash
  # 移动到 _deprecated/
  mv identity/ _deprecated/identity/
  mv sessions/ _deprecated/sessions/
  # ... 其他旧包
  ```

- [ ] **T4.2**: 更新所有 import 路径
  ```bash
  # 全局替换
  find . -name "*.go" -exec sed -i 's|github.com/kaixuan/llm-gateway-go/identity|github.com/kaixuan/llm-gateway-go/domains/identity|g' {} \;
  ```

- [ ] **T4.3**: 性能优化
  - 事件总线批量发送（减少 goroutine 开销）
  - Hook 执行超时控制（防止慢 Hook 阻塞）
  - 并行 Hook 数量限制（防止 goroutine 爆炸）

- [ ] **T4.4**: 文档完善
  - [ ] 每个领域 README.md
  - [ ] Hook Pipeline 使用指南
  - [ ] 智能体生态 API 文档
  - [ ] 迁移指南（给其他团队）

- [ ] **T4.5**: 静态验证
  ```bash
  # 循环依赖检查
  scripts/check-cycles.sh
  # 期望: 0 cycles
  
  # 测试覆盖率
  go test ./domains/... -cover | grep "coverage:"
  # 期望: 所有包 ≥ 80%
  
  # golangci-lint
  golangci-lint run ./domains/...
  # 期望: 0 issues
  ```

#### 验收标准

```bash
# 完整构建
go build ./cmd/gateway/

# E2E 测试套件
./scripts/e2e-all.sh
# 期望: 100% 通过

# 性能基准测试
go test -bench=. -benchmem ./domains/pipeline/
# 期望: 
#   - 单请求延迟 < 50ms
#   - 内存分配 < 10KB per request

# 压力测试
./scripts/load-test.sh --rps 1000 --duration 5m
# 期望:
#   - P99 延迟 < 500ms
#   - 错误率 < 0.1%
#   - 无内存泄漏

# 文档完整性
find domains/ -name "README.md" | wc -l
# 期望: ≥ 16 (每个领域一个)
```

---

## 四、风险与缓解措施

### 4.1 风险矩阵

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| 循环依赖 | 中 | 高 | CI 自动检测 + 每日审查 |
| 性能退化 | 低 | 高 | 每个 Phase 压测 + 性能基线对比 |
| 测试覆盖不足 | 低 | 中 | 每个 PR 强制覆盖率 ≥ 80% |
| 事件总线成为瓶颈 | 中 | 中 | 内存总线 + 批量发送优化 |
| 旧代码误删除 | 低 | 高 | 先移到 `_deprecated/`，3 个月后删除 |

### 4.2 回滚策略

每个 Phase 完成后打 git tag：
```bash
git tag domain-refactor-phase-0  # Phase 0 完成
git tag domain-refactor-phase-1  # Phase 1 完成
git tag domain-refactor-phase-2  # Phase 2 完成
git tag domain-refactor-phase-3  # Phase 3 完成
```

如发现严重问题：
```bash
git revert <commit-range>  # 回滚最近的 Phase
```

---

## 五、验收标准总览

### 5.1 技术指标

| 指标 | 目标 | 验证方式 |
|------|------|---------|
| 测试覆盖率 | ≥ 90% | `go test ./domains/... -cover` |
| 循环依赖 | 0 | `scripts/check-cycles.sh` |
| P99 延迟 | < 500ms | `./scripts/load-test.sh` |
| 错误率 | < 0.1% | 线上监控 |
| 新增供应商时间 | < 0.5 天 | 实际操作计时 |

### 5.2 业务指标

- [ ] 所有现有功能保持一致（响应格式、错误码、延迟）
- [ ] 智能体注册成功率 > 99%
- [ ] 行为分析覆盖 100% 请求
- [ ] 会话检查器误报率 < 1%

### 5.3 文档完整性

- [ ] 16 个领域各有 README.md
- [ ] Hook Pipeline 使用指南
- [ ] 智能体生态 API 文档
- [ ] 事件流图和架构图
- [ ] 迁移指南（给新加入团队成员）

---

## 六、后续优化方向

### 6.1 Phase 5+（3 个月后）

- [ ] Redis 事件总线（支持多实例）
- [ ] 智能体协作图谱可视化
- [ ] 自动异常检测与告警
- [ ] 能力市场（Marketplace）
- [ ] WASM 插件支持（动态加载检查器）

### 6.2 性能优化

- [ ] 事件总线 Zero-Copy
- [ ] Hook Pipeline 并行度自适应
- [ ] 压缩算法优化（LZ4/Zstd）
- [ ] 缓存预热策略

---

## 附录 A：目录结构完整清单

```
llm-gateway-go/
├── cmd/
│   └── gateway/
│       └── main.go
│
├── domains/                           # 🆕 领域层
│   ├── authentication/                # 用户认证
│   ├── tenant/                        # 租户管理
│   ├── identity/                      # 客户识别
│   ├── session/                       # 会话管理
│   ├── routing/                       # 模型路由
│   ├── credential/                    # 凭据管理
│   ├── provider/                      # 供应商管理
│   ├── transformation/                # 协议转换
│   ├── streaming/                     # 流式转发
│   ├── agent-ecosystem/               # 🆕 智能体生态
│   ├── hooks/                         # 🆕 横切关注点
│   │   ├── cache/
│   │   ├── compression/
│   │   ├── security/
│   │   ├── audit/
│   │   ├── observability/
│   │   ├── tools/
│   │   └── session-inspector/         # 🆕 会话检查器
│   └── pipeline/                      # 🆕 Hook Pipeline 框架
│
├── eventbus/                          # 🆕 事件总线
│   ├── memory_bus.go
│   ├── redis_bus.go
│   └── hybrid_bus.go
│
├── domain/                            # 🟢 保留（已有 91.4% 覆盖率）
│   ├── envelope.go
│   ├── builder.go
│   └── ...
│
├── transport/                         # 🟢 保留（已有 IR 实现）
│
├── _deprecated/                       # 🆕 旧代码归档（3 个月后删除）
│   ├── identity/
│   ├── sessions/
│   ├── routing/
│   └── ...
│
├── migrations/                        # 数据库迁移
│   └── xxx_agent_ecosystem.sql       # 🆕
│
└── scripts/
    ├── migrate-to-domains.sh          # 🆕
    ├── check-cycles.sh                # 🆕
    └── e2e-all.sh
```

---

## 附录 B：事件清单

### 核心领域事件

| 领域 | 事件 | 订阅者 |
|------|------|--------|
| 认证 | UserAuthenticated | 租户、审计 |
| 租户 | TenantConfigLoaded | 路由、配额 |
| 客户识别 | ClientIdentified | 会话、智能体 |
| 会话 | SessionLoaded | 压缩、审计 |
| 路由 | RouteResolved | 凭据、审计 |
| 凭据 | CredentialSelected | 上游调用、审计 |
| 凭据 | CircuitOpened | 路由、告警 |
| 转换 | TransformCompleted | 上游调用 |
| 流式 | ChunkReceived | 监控、审计 |
| 流式 | SessionCompleted | 成本、缓存、审计 |

### 横切领域事件

| 领域 | 事件 | 订阅者 |
|------|------|--------|
| 压缩 | CompressionTriggered | 监控、审计 |
| 安全 | ThreatDetected | 审计、告警 |
| 智能体 | AgentDiscovered | 行为分析、审计 |
| 智能体 | BehaviorAnomalyDetected | 告警、审计 |

---

**文档结束**
