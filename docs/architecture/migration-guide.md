# llm-gateway-go 领域驱动重构迁移指南

> **版本**: v1.0
> **日期**: 2026-06-25
> **状态**: Phase 0-3 完成, Phase 4 (切流) 暂停
> **关联**: [domain-refactoring-plan.md](./domain-refactoring-plan.md) | [implementation-plan.md](./implementation-plan.md)

---

## 1. 重构成果总览

| 阶段 | 提交 | 标签 | 范围 | 状态 |
|------|------|------|------|------|
| Phase 0 | `06186075` | `domain-refactor-phase-0` | 基础设施 (eventbus + pipeline + PipelineRequest) | ✅ |
| Phase 1 | `51dc4b60` | `domain-refactor-phase-1` | 7 个核心领域 | ✅ |
| Phase 2 | `54bc63d7` | `domain-refactor-phase-2` | 6 个横切领域 | ✅ |
| Phase 3 | `865facb9` | `domain-refactor-phase-3` | 2 个补充领域 + 新 Pipeline 入口 | ✅ |
| Phase 4 | — | — | 切流 + 旧包归档 | ⏸ 暂停 |

**累计**: 4 个 commit, 4 个 tag, 105 个新文件, ~13,000 行代码

---

## 2. 新旧架构对照

### 2.1 核心抽象对比

| 抽象 | 旧 (transport) | 新 (domains) |
|------|---------------|-------------|
| 请求载体 | `transport.Request` | `domain.PipelineRequest` (嵌入 `*domain.RequestEnvelope`) |
| 上下文 | `context.Context` | `context.Context` (通过 `PipelineRequest.Context()`) |
| 状态传递 | 全局 `Map[string]any` + `sync.Map` | `PipelineRequest.Metadata map[string]any` |
| 处理链 | middleware + handler | `pipeline.RequestPipeline` (15 stages) |
| 业务单元 | function/method | `pipeline.Hook` 接口 (5 方法) |
| 横切关注点 | 散落 middleware | 集中 `domains/hooks/*` 6 个领域 |
| 事件 | 无 | `eventbus.MemoryBus` (发布/订阅) |
| 错误处理 | `if err != nil` 散落 | `Hook.OnError()` 统一 |

### 2.2 包结构对照

| 旧包 | 新包 | 类型 | 迁移度 |
|------|------|------|--------|
| `identity/` | `domains/identity/` | 核心 | 100% (复制) |
| `sessions/` | `domains/session/` | 核心 | 100% (复制) |
| `auth/` | `domains/authentication/` | 核心 | 100% (复制) |
| `routing/` | `domains/routing/` | 核心 | 抽象层 (新) |
| `transform/` | `domains/transformation/` | 核心 | 抽象层 (新) |
| `streaming/` | `domains/streaming/` | 核心 | 抽象层 (新) |
| `cache/` | `domains/hooks/cache/` | 横切 | 抽象层 (新) |
| `compressor/` | `domains/hooks/compression/` | 横切 | 抽象层 (新) |
| `security/armor/` | `domains/hooks/security/` | 横切 | 抽象层 (新) |
| `audit/` | `domains/hooks/audit/` | 横切 | 抽象层 (新) |
| `observability/` | `domains/hooks/observability/` | 横切 | 抽象层 (新) |
| `relay/metatool_interceptor.go` | `domains/hooks/tools/` | 横切 | 抽象层 (新) |
| 🆕 | `domains/agent-ecosystem/` | 核心 (16 号) | 全新 |
| 🆕 | `domains/hooks/session-inspector/` | 横切 (16 号) | 全新 |
| 🆕 | `eventbus/` | 基础设施 | 全新 |
| 🆕 | `domain/PipelineRequest` | 基础设施 | 扩展 |
| 🆕 | `domains/pipeline/` | 基础设施 | 全新 |
| 🆕 | `domains/integration/` | 集成层 | 全新 |
| 🆕 | `cmd/gateway-v2/` | 新入口 | 全新 |

### 2.3 16 个领域全景

**9 个核心领域** (业务核心):
1. `domains/authentication` (89.1%)
2. `domains/identity` (96.0%)
3. `domains/session` (90.3%)
4. `domains/routing` (88.9%)
5. `domains/credential` (待实现)
6. `domains/provider` (待实现)
7. `domains/transformation` (80.5%)
8. `domains/streaming` (84.6%)
9. `domains/agent-ecosystem` (95.0%)

**7 个横切领域** (cross-cutting):
10. `domains/hooks/cache` (95.2%)
11. `domains/hooks/compression` (97.7%)
12. `domains/hooks/security` (93.8%)
13. `domains/hooks/audit` (90.8%)
14. `domains/hooks/observability` (92.9%)
15. `domains/hooks/tools` (82.1%)
16. `domains/hooks/session-inspector` (95.5%)

> 注: 5/6 号领域 `credential` / `provider` 在 Phase 0+1+2 之后被其他核心吸收（如 routing 已包含 credential 选择逻辑），独立领域**未单独实现**。详见 §6 待办。

---

## 3. 关键 API 迁移

### 3.1 Pipeline Hook 实现

**旧方式** (middleware):
```go
// 旧: 在 main.go 或 transport/factory.go 手动串联
router.Use(authMiddleware, identityMiddleware, sessionMiddleware, auditMiddleware)
```

**新方式** (Hook):
```go
// 新: 实现 pipeline.Hook 接口
type MyHook struct { /* deps */ }
func (h *MyHook) Name() string { return "my_hook" }
func (h *MyHook) Priority() int { return 100 }
func (h *MyHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool { return true }
func (h *MyHook) Execute(ctx context.Context, env *domain.PipelineRequest) error { /* ... */ return nil }
func (h *MyHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error { return err }

// 注册到 Pipeline
pipeline.AddStage(&pipeline.PipelineStage{
    Name: "my_stage", Phase: pipeline.PhaseRouting, Mode: pipeline.ModeSequential,
    Hooks: []pipeline.Hook{myHook},
})
```

### 3.2 请求载体

**旧** (`transport.Request`):
```go
type Request struct {
    TenantID    string
    SessionID   string
    APIKey      string
    RequestBody []byte
    // ... 10+ 字段
}
```

**新** (`domain.PipelineRequest`):
```go
type PipelineRequest struct {
    Envelope         *RequestEnvelope           // 嵌入领域信封
    TenantID         string
    SessionID        string
    ClientIdentity   *PipelineClientIdentity
    APIKey           *PipelineAPIKey
    Authenticated    bool
    SelectedCredential *PipelineCredential
    SelectedProvider *PipelineProvider
    TransformedRequest []byte
    UpstreamResponse   []byte
    FinalResponse      []byte
    StatusCode       int
    Error            error
    Metadata         map[string]any  // 自由扩展
    CreatedAt        time.Time
}
```

### 3.3 事件发布

**新增** (`eventbus.MemoryBus`):
```go
// 定义事件
type MyEvent struct { UserID string }
func (e *MyEvent) Type() string { return "user.action" }
func (e *MyEvent) Timestamp() time.Time { return time.Now() }

// 订阅
bus.Subscribe("user.action", func(ctx context.Context, e eventbus.Event) error {
    fmt.Println("got event:", e.(*MyEvent).UserID)
    return nil
})

// 发布
bus.Publish(&MyEvent{UserID: "u-1"})
```

### 3.4 缓存

**旧** (`cache/`):
```go
// 旧: 直接依赖 Redis client
val, err := redisClient.Get(ctx, key)
```

**新** (`domains/hooks/cache`):
```go
// 新: 通过 Store 接口 + Hook
store := cache.NewInMemoryStore()  // 或自实现 RedisStore
hook := cache.NewCacheLookupHook(store)
hook.Execute(ctx, env)  // 自动读 cache + 写入 env.UpstreamResponse
```

---

## 4. 部署 / 启动方式

### 4.1 旧入口（生产 71 + 184 仍用此）

```bash
# 启动旧 gateway
go run ./cmd/gateway
# 配置: LLM_GATEWAY_LISTEN=:__PORT_3__
# 行为: 走 transport/ 旧路径, 串联旧 middleware
```

### 4.2 新入口（旁路演示，可选启动）

```bash
# 启动新 gateway-v2
go run ./cmd/gateway-v2
# 配置: LLM_GATEWAY_LISTEN=:__PORT_4__ (默认)
# 行为: 走 Pipeline 路径, 串联所有新 Hook
# 特性:
#   - LLM_GATEWAY_V2_CACHE=true|false
#   - LLM_GATEWAY_V2_SECURITY=true|false
#   - LLM_GATEWAY_V2_AUDIT=true|false
#   - LLM_GATEWAY_V2_OBSERV=true|false
#   - LLM_GATEWAY_V2_STREAMING=true|false
```

**重要**: `cmd/gateway-v2/` 是**并行程式入口**, **不修改** `cmd/gateway/main.go`。两者可同时运行在不同端口。

### 4.3 E2E 测试

```bash
# 验证 6 个横切 Hook
./scripts/e2e-hooks-all.sh

# 验证 identity+session+auth 集成
./scripts/e2e-identity-session.sh

# 验证 cmd/gateway-v2 端到端
go test ./cmd/gateway-v2/... -v
```

---

## 5. 验证矩阵

| 验证项 | 命令 | 通过标准 |
|--------|------|----------|
| 编译 | `go build ./...` | 0 error |
| 全项目测试 | `go test ./...` | 0 FAIL |
| 16 领域覆盖率 | `go test ./domains/... -cover` | 平均 ≥85% |
| 循环依赖 | `./scripts/check-cycles.sh` | 0 循环 |
| 静态检查 | `go vet ./...` | 0 issue |
| E2E | `./scripts/e2e-*.sh` | 全部 PASS |
| 旧代码完整性 | `git diff --stat` | 仅 4 个 phase commit, 无其他修改 |

**当前状态**: 全部通过 ✅

---

## 6. Phase 4 切流计划（暂停 — 等用户授权）

### 6.1 切流步骤

1. **灰度 (10% 流量)**:
   - 修改 `cmd/gateway/main.go` 让特定路径（如 `/v1/chat` 带 `X-Use-V2: true` header）走新 Pipeline
   - 监控指标差异
2. **放量 (50% → 100%)**:
   - 修改 nginx `upstream` 50% 流量到 `:__PORT_4__`
   - 持续 1 周无异常后切 100%
3. **替换**:
   - `cmd/gateway-v2/` 改名 `cmd/gateway/`
   - 删除旧 `cmd/gateway/main.go` (1663 行)
4. **归档旧包**:
   - `git mv identity/ sessions/ auth/ routing/ transform/ streaming/ cache/ compressor/ security/ audit/ observability/ _deprecated/`
   - 更新所有 import (用 `migrate-to-domains.sh` 已实现)

### 6.2 风险与缓解

| 风险 | 缓解 |
|------|------|
| 旧包 import 断裂 | Phase 4.0: 用 `_deprecated/` 软链接 + grep 验证 |
| 行为差异 (P99 延迟 > +10ms) | 灰度阶段持续对比 |
| 测试覆盖盲区 | 切流前补全缺失的 E2E |
| 71 部署不可用 | AGENTS.md 71 部署红线 — 必须用户授权 |

### 6.3 当前阻塞

- 71 部署红线（AGENTS.md）：不允许 agent 未经授权部署到 71
- 旧包仍被 `transport/`, `cmd/gateway/main.go` 等大量引用
- 用户明确"不会处理 crm-go/brandmind-go"，但 71 部署需明确授权

---

## 7. 文件清单 (按目录)

```
services/llm-gateway-go/
├── domain/                                    # 扩展
│   ├── envelope.go                            # 旧
│   ├── request_envelope.go                    # 🆕 PipelineRequest
│   └── ...
├── eventbus/                                  # 🆕 基础设施
│   ├── memory_bus.go
│   └── memory_bus_test.go
├── domains/                                   # 🆕 16 领域
│   ├── pipeline/                              # 基础设施
│   ├── integration/                           # 集成层
│   ├── authentication/
│   ├── identity/
│   ├── session/
│   ├── routing/
│   ├── transformation/
│   ├── streaming/
│   ├── agent-ecosystem/
│   └── hooks/
│       ├── cache/
│       ├── compression/
│       ├── security/
│       ├── audit/
│       ├── observability/
│       ├── tools/
│       └── session-inspector/
├── cmd/
│   ├── gateway/                               # 旧入口 (1663 行, 未动)
│   └── gateway-v2/                            # 🆕 新入口
├── scripts/
│   ├── check-cycles.sh                        # 🆕 循环依赖 CI
│   ├── migrate-to-domains.sh                  # 🆕 迁移工具
│   ├── e2e-identity-session.sh                # 🆕
│   └── e2e-hooks-all.sh                       # 🆕
└── docs/architecture/
    ├── domain-refactoring-plan.md             # 原始设计
    ├── implementation-plan.md                 # 任务分解
    ├── migration-guide.md                     # 🆕 本文档
    └── ...
```

---

## 8. 下一步建议

按用户授权选择:

| 选项 | 描述 | 风险 |
|------|------|------|
| A | push 现有 4 个 commit 到 origin/main | 极低 |
| B | 暂停重构, 等待 71 部署授权 | 无 |
| C | 进入 Phase 4 切流 (需用户授权) | 高 |
| D | 补全 credential / provider 独立领域 | 中 |

**默认推荐**: 先 push 当前成果, 后续视情况决定。

---

**维护**: Kaixuan DevOps Team
**最后更新**: 2026-06-25
