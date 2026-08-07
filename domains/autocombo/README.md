# AutoCombo Domain

虚拟自动路由模块，实现零配置的免费资源聚合。

## 概述

本模块实现了 `auto/*` 虚拟模型路由功能，用户无需手工配置，即可自动聚合所有可用的免费资源，并根据健康度、延迟、配额剩余等多维度评分智能选择最优候选。

## 核心功能

### 1. Resolver - 路由解析器

解析 `auto/*` 模型 ID 到 AutoComboSpec：

- 优先查找数据库中的自定义模板
- 回退到内置模板（6个常用模板）
- 支持的内置模板：
  - `auto/free` - 最低成本
  - `auto/best-free` - 最低成本
  - `auto/coding:free` - 代码任务优化
  - `auto/reasoning:free` - 推理任务优化
  - `auto/fast:free` - 低延迟优化
  - `auto/creative:free` - 创作任务优化

### 2. VirtualFactory - 候选池工厂

动态构建虚拟 combo 的候选池：

- 加载已连接的免费凭据
- 加载 keyless 提供商
- 配额预检过滤（跳过即将耗尽的）
- ToS 合规过滤
- 白名单/黑名单过滤

### 3. Engine - 评分引擎

6维评分 + 分层轮换：

**评分维度**：
- `health_score` - 健康分数（默认权重 0.3）
- `latency_p95` - P95 延迟（默认权重 0.2）
- `quota_remaining` - 配额剩余（默认权重 0.25）
- `cost` - 成本（免费模式权重 0）
- `task_fit` - 任务适配度（默认权重 0.15）
- `tier_affinity` - 层级亲和度（默认权重 0.1）

**分层策略**：
- Top tier: score >= 0.8
- Mid tier: 0.5 <= score < 0.8
- Rest tier: score < 0.5

**选择策略**：
- 如果 Top tier 有明显优胜者（领先 >= 0.1）：直接选择
- 否则：在当前 tier 内轮换

## 使用示例

### 基本使用

```go
import "llm-gateway-go/domains/autocombo"

// 1. 创建组件
resolver := autocombo.NewResolver(db)
factory := autocombo.NewVirtualFactory(db, quotaTracker)

// 2. 解析 auto/* 模型
spec, err := resolver.Resolve(ctx, "auto/free", tenantID)
if spec == nil {
    // 不是 auto combo，按正常流程处理
}

// 3. 构建候选池
virtualCombo, err := factory.Build(ctx, spec, tenantID)

// 4. 创建评分引擎
engine, err := autocombo.NewEngine(spec.ScoringWeightsJSON)

// 5. 选择最优候选
candidate, err := engine.SelectCandidate(virtualCombo.CandidatePool)

// 6. 使用候选执行请求
// ...
```

### 自定义权重

```go
// 创建自定义权重（优先低延迟）
weights := autocombo.ScoringWeights{
    HealthScore:    0.3,
    LatencyP95:     0.5,  // 提高延迟权重
    QuotaRemaining: 0.2,
    Cost:           0.0,
    TaskFit:        0.0,
    TierAffinity:   0.0,
}

weightsJSON, _ := json.Marshal(weights)
engine, _ := autocombo.NewEngine(weightsJSON)
```

## 集成点

### Streaming Handler

在 `domains/streaming/handler.go` 中集成：

```go
func (h *Handler) HandleChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    // 1. 解析是否为 auto combo
    autoSpec, err := h.autoResolver.Resolve(ctx, req.Model, req.TenantID)
    if err != nil {
        return nil, fmt.Errorf("resolve auto combo: %w", err)
    }

    if autoSpec != nil {
        // 2. 构建虚拟 combo
        virtualCombo, err := h.virtualFactory.Build(ctx, autoSpec, req.TenantID)
        if err != nil {
            return nil, fmt.Errorf("build virtual combo: %w", err)
        }

        // 3. 创建引擎并选择候选
        engine, err := autocombo.NewEngine(autoSpec.ScoringWeightsJSON)
        if err != nil {
            return nil, fmt.Errorf("create engine: %w", err)
        }

        candidate, err := engine.SelectCandidate(virtualCombo.CandidatePool)
        if err != nil {
            return nil, fmt.Errorf("select candidate: %w", err)
        }

        // 4. 重写请求
        req.ProviderCode = candidate.ProviderCode
        req.ModelID = candidate.ModelID
        req.CredentialID = candidate.CredentialID

        log.Infof("auto-routed %s → %s/%s (score=%.2f)",
            autoSpec.ComboName, candidate.ProviderCode, candidate.ModelID, candidate.HealthScore)
    }

    // 5. 执行常规流程
    return h.executor.Execute(ctx, req)
}
```

## 数据库依赖

本模块依赖以下数据库表：

- `auto_combo_templates` - Auto Combo 模板配置
- `free_resource_catalog` - 免费资源目录
- `credentials` - 凭据表（需要 is_free_tier 列）
- `keyless_providers` - Keyless 提供商注册表

## 测试

```bash
# 运行单元测试
go test ./domains/autocombo -v -short

# 运行集成测试（需要数据库）
go test ./domains/autocombo -v
```

## 设计文档

详细设计请参考：
- `docs/omnifree/03-AUTO-COMBO.md` - 虚拟路由设计
- `docs/omnifree/00-OVERVIEW.md` - 架构总览

## 未来扩展

- [ ] 基于任务分类的 TaskFit 评分
- [ ] 持久化的轮换状态
- [ ] Bandit 算法（Exploration/Exploitation）
- [ ] 候选池缓存（减少数据库查询）
- [ ] 基于历史表现的动态权重调整

## 维护

- 维护者：SI-LLM-Gateway 团队
- 最后更新：2026-08-07
