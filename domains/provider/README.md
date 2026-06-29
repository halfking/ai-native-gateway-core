# Provider 包分层设计文档

> **版本**: v1.0  
> **日期**: 2026-06-29  
> **状态**: 双包分层设计

---

## 概述

当前 `provider` 相关功能分为两个包：

1. **顶层包 `provider/`**: 数据访问层（Data Access Layer）
2. **域包 `domains/provider/`**: Pipeline Hook 层（Hook Layer）

---

## 包职责划分

### 顶层包 `provider/` - 数据访问层

**路径**: `github.com/kaixuan/llm-gateway-go/provider`

**职责**:
- 数据库访问（Provider、Model、Credential 查询）
- 外部 API 客户端（Provider API 调用）
- 业务逻辑（billing 计费、健康检查、候选缓存）
- 领域实体定义（Provider、Candidate、Policy 等）

**核心文件**:
- `client.go` (1,482 行总计，包含以下功能)
- `billing.go`: 计费集成
- `suspicious_exit_metrics.go`: 异常退出监控
- `client_diagnostic_test.go`: 诊断测试

**使用场景**:
- 被 `cmd/gateway/main.go` 生产入口直接引用
- 被旧的 executor 和 routing 逻辑引用
- 包含完整的 Provider 管理逻辑

---

### 域包 `domains/provider/` - Hook 层

**路径**: `github.com/kaixuan/llm-gateway-go/domains/provider`

**职责**:
- Pipeline Hook 实现（ProviderDiscoveryHook）
- 轻量级 Provider 查询接口
- 健康探测逻辑（Prober）
- InMemoryStore（测试和 demo 用）

**核心文件**:
- `hook.go`: ProviderDiscoveryHook 实现
- `probe.go`: 健康探测逻辑
- `types.go`: Hook 层类型定义
- `provider_test.go`: Hook 单元测试

**使用场景**:
- 被 `cmd/gateway/main_pipeline.go` Pipeline 入口引用
- 为 Pipeline 提供 Provider 发现能力
- 不包含复杂业务逻辑

---

## 依赖关系

```
┌─────────────────────────────────────────────────────┐
│  cmd/gateway/main.go (生产 v1 入口)                  │
│  ├─ import "provider"                               │
│  └─ 使用 provider.Client                            │
└─────────────────────────────────────────────────────┘
                    ▼
┌─────────────────────────────────────────────────────┐
│  provider/ (数据访问层)                              │
│  - Client: DB 查询 + API 调用                       │
│  - Billing: 计费集成                                │
│  - Cache: 候选缓存                                   │
└─────────────────────────────────────────────────────┘


┌─────────────────────────────────────────────────────┐
│  cmd/gateway/main_pipeline.go (Pipeline v2 入口)    │
│  ├─ import "domains/provider"                       │
│  └─ 使用 provider.ProviderDiscoveryHook             │
└─────────────────────────────────────────────────────┘
                    ▼
┌─────────────────────────────────────────────────────┐
│  domains/provider/ (Hook 层)                        │
│  - ProviderDiscoveryHook: Pipeline 集成             │
│  - Prober: 健康探测                                 │
│  - InMemoryStore: 轻量级存储                        │
│  └─ (可选) import "provider" 引用数据层             │
└─────────────────────────────────────────────────────┘
```

---

## 为什么是双包设计？

### 优点

1. **职责分离**: 数据访问和 Pipeline 集成分离
2. **向后兼容**: 不破坏现有 v1 入口的引用
3. **渐进迁移**: 可以逐步将 v1 迁移到 Pipeline 而不需要大重构
4. **测试隔离**: Hook 层可以用 InMemoryStore 独立测试

### 缺点

1. **概念重叠**: 两个包都有 Provider 相关类型
2. **学习曲线**: 新开发者需要理解双包关系
3. **潜在重复**: 某些逻辑可能在两个包中重复

---

## 使用指南

### 场景 1: 在 v1 生产代码中使用 Provider

```go
import "github.com/kaixuan/llm-gateway-go/provider"

// 使用完整的数据访问层
client := provider.NewClient()
candidates, err := client.GetCandidates(ctx, model, profile)
```

### 场景 2: 在 Pipeline Hook 中使用 Provider

```go
import "github.com/kaixuan/llm-gateway-go/domains/provider"

// 使用轻量级 Hook 层
store := provider.NewInMemoryStore()
prober := provider.NewProber(store)
hook := provider.NewProviderDiscoveryHook(store, prober)
```

### 场景 3: Hook 层需要调用数据层（未来）

```go
// domains/provider/hook.go
import (
    "github.com/kaixuan/llm-gateway-go/provider"  // 数据层
)

type ProviderDiscoveryHook struct {
    dataClient *provider.Client  // 引用数据层客户端
}
```

---

## 未来优化方向

### 选项 A: 保持双包（推荐）

- 维持当前分层
- 文档化分层设计
- 逐步将 v1 的 provider 引用迁移到 domains/provider

### 选项 B: 合并为单包

- 将顶层 `provider/` 迁移到 `domains/provider/`
- 更新所有 import 路径
- 需要大规模重构（风险较高）

### 选项 C: 重命名顶层包

- 将顶层 `provider/` 重命名为 `providerlegacy/`
- 保留向后兼容
- 逐步废弃旧包

---

## 当前状态

| 指标 | 顶层 provider/ | 域包 domains/provider/ |
|------|----------------|----------------------|
| 文件数 | 6 | 4 |
| 代码行数 | 1,482 | 637 |
| 测试覆盖 | 部分 | 完整 |
| 引用方 | cmd/gateway/main.go | cmd/gateway/main_pipeline.go |
| 状态 | 生产使用 | Pipeline 使用 |

---

## 相关文档

- 审计报告: `docs/2026-06-29-phase1-reuse-audit.md`
- 原始设计: `docs/architecture/domain-refactoring-plan.md`
