# Phase 1.5 修正计划：充分复用成熟代码

> **关联文档**: 
> - [domain-refactoring-plan.md](./domain-refactoring-plan.md) (v2.0)
> - [implementation-plan.md](./implementation-plan.md) (v1.0)
> - [phase1-execution-audit-v2-revised-20260625.md](./phase1-execution-audit-v2-revised-20260625.md) (审计报告)
>
> **版本**: v1.0  
> **日期**: 2026-06-25  
> **执行时间**: 7 天

---

## 0. 计划概述

### 0.1 问题根源

经过深度审计，我们发现当前 Phase 1 执行存在严重的**代码复用不足**问题：

```
旧代码库（成熟、测试充分）: 65,871 行
新代码库（domains/）:        13,279 行
─────────────────────────────────────
实际复用率:                   仅 20.1%
浪费的成熟代码:               52,592 行 (79.9%)
```

**三大错误做法**：
1. **"简化示例"误区**: 大量代码被简化为"最小契约"（如 circuit/breaker.go 从 475 行简化到 98 行）
2. **"接口优先"陷阱**: 只定义接口而无实现（如 domains/credential/limiter.go 仅 63 行）
3. **"重写"浪费**: 为了"领域纯净性"重写完整功能（如 relay/ 23,162 行仅复用 1.7%）

### 0.2 修正目标

| 指标 | 当前 | 目标 | 增量 |
|------|------|------|------|
| **整体复用率** | 20.1% | ≥ 70% | +49.9% |
| **domains/ 总行数** | 13,279 | ≥ 46,000 | +32,721 行 |
| **核心领域完成度** | 6/9 | 9/9 | +3 |
| **功能完整性** | 简化版 | 完整版 | 保留所有功能 |

### 0.3 修正原则

| 原原则 | 修正后原则 |
|--------|-----------|
| 简化示例实现 | **保留完整功能** |
| 接口优先 | **实现优先，接口自然形成** |
| 领域纯净性优先 | **务实复用，渐进重构** |
| 直接移动 70% | **直接移动 ≥ 70%**（按计划） |

---

## 1. 任务清单总览（7 天）

| 天 | 主题 | 任务数 | 目标行数 |
|----|------|--------|---------|
| **Day 1-2** | 凭据管理完整迁移 | 3 | 2,858 行 |
| **Day 3-4** | Executor 完整迁移 | 2 | 7,879 行 |
| **Day 5-6** | 压缩 + 协议转换完整迁移 | 3 | 12,000 行 |
| **Day 7** | 剩余横切关注点 + Pipeline 集成 | 5 | 10,000 行 |
| **合计** | — | **13 个任务** | **32,737 行** |

---

## 2. Day 1-2: 凭据管理完整迁移

### 目标

将 **credentialstate/** + **circuit/** + **limiter/** 三个包（2,858 行）完整迁移到 `domains/credential/`，保留所有功能。

### 2.1 任务 R1.1: 迁移 credentialstate/writer.go

**目标文件**:
- 旧: `credentialstate/writer.go` (431 行)
- 旧: `credentialstate/writer_test.go`
- 旧: `credentialstate/writer_regression_test.go`
- 新: `domains/credential/writer.go` + 测试

**执行步骤**:

```bash
# 步骤 1: 复制文件
cp credentialstate/writer.go domains/credential/writer.go
cp credentialstate/writer_test.go domains/credential/writer_test.go
cp credentialstate/writer_regression_test.go domains/credential/writer_regression_test.go

# 步骤 2: 更新 import 路径
find domains/credential/writer*.go -exec sed -i '' \
  's|"__REPO_URL_3__/credentialstate|"__REPO_URL_3__/domains/credential|g' {} \;

# 步骤 3: 修复 package 声明
sed -i '' 's|^package credentialstate|package credential|' domains/credential/writer.go

# 步骤 4: 验证编译
go build ./domains/credential/

# 步骤 5: 运行测试
go test ./domains/credential/ -run TestWriter -v
```

**验收标准**:
- [ ] `domains/credential/writer.go` 至少 431 行（包含完整实现）
- [ ] `go test ./domains/credential/ -run TestWriter` 全部通过
- [ ] 无 import 错误

**工时**: 4 小时

---

### 2.2 任务 R1.2: 完整迁移 circuit/breaker.go

**⚠️ 关键任务**: 当前 `domains/credential/breaker.go` 只有 98 行简化版，必须替换为完整版（475 行）

**目标文件**:
- 旧: `circuit/breaker.go` (475 行) - **完整版**
- 旧: `circuit/breaker_test.go`
- 当前: `domains/credential/breaker.go` (98 行) - **简化版，需替换**

**执行步骤**:

```bash
# 步骤 1: 备份当前简化版
cp domains/credential/breaker.go domains/credential/breaker_simplified.go.bak

# 步骤 2: 复制完整版
cp circuit/breaker.go domains/credential/breaker.go
cp circuit/breaker_test.go domains/credential/breaker_test.go

# 步骤 3: 更新 import 路径
find domains/credential/breaker*.go -exec sed -i '' \
  's|"__REPO_URL_3__/circuit|"__REPO_URL_3__/domains/credential|g' {} \;

# 步骤 4: 修复 package 声明
sed -i '' 's|^package circuit|package credential|' domains/credential/breaker.go

# 步骤 5: 删除简化版备份
rm domains/credential/breaker_simplified.go.bak

# 步骤 6: 验证编译
go build ./domains/credential/

# 步骤 7: 运行测试
go test ./domains/credential/ -run TestBreaker -v
```

**⚠️ 必须保留的功能**:
- [ ] 4 种状态（Closed / Open / HalfOpen / **Quarantined**）
- [ ] 指数退避冷却策略
- [ ] 按错误类型分类（Auth/Quota → 隔离，Network → 短冷却）
- [ ] 半开探测机制
- [ ] StateQuarantined 常量

**验收标准**:
- [ ] `domains/credential/breaker.go` 至少 475 行（不是 98 行）
- [ ] 包含 `StateQuarantined` 常量
- [ ] 包含 `errorsx` 包的错误分类逻辑
- [ ] 所有原有测试通过

**工时**: 3 小时

---

### 2.3 任务 R1.3: 完整迁移 limiter/

**⚠️ 关键任务**: 当前 `domains/credential/limiter.go` 只有 63 行接口定义，必须补充完整实现（1,289 行）

**目标文件**:
- 旧: `limiter/limiter.go` (444 行)
- 旧: `limiter/limiter_test.go`
- 旧: `limiter/limiter_concurrent_test.go`
- 旧: `limiter/redis_identity.go` (Redis 身份识别)
- 当前: `domains/credential/limiter.go` (63 行接口) - **需替换**

**执行步骤**:

```bash
# 步骤 1: 备份当前接口版
cp domains/credential/limiter.go domains/credential/limiter_interface.go.bak

# 步骤 2: 复制完整实现
cp limiter/limiter.go domains/credential/limiter.go
cp limiter/limiter_test.go domains/credential/limiter_test.go
cp limiter/limiter_concurrent_test.go domains/credential/limiter_concurrent_test.go
cp limiter/redis_identity.go domains/credential/redis_identity.go

# 步骤 3: 更新 import 路径
find domains/credential/limiter*.go domains/credential/redis_identity.go \
  -exec sed -i '' \
  's|"__REPO_URL_3__/limiter|"__REPO_URL_3__/domains/credential|g' {} \;

# 步骤 4: 修复 package 声明
sed -i '' 's|^package limiter|package credential|' domains/credential/limiter.go
sed -i '' 's|^package limiter|package credential|' domains/credential/redis_identity.go

# 步骤 5: 删除接口版备份
rm domains/credential/limiter_interface.go.bak

# 步骤 6: 验证编译
go build ./domains/credential/

# 步骤 7: 运行测试
go test ./domains/credential/ -run TestLimiter -v
```

**⚠️ 必须保留的功能**:
- [ ] Redis + 本地双层限流
- [ ] 身份识别集成（redis_identity.go）
- [ ] 动态调整并发槽位
- [ ] 并发测试（limiter_concurrent_test.go）

**验收标准**:
- [ ] `domains/credential/limiter.go` 至少 444 行
- [ ] `domains/credential/redis_identity.go` 存在
- [ ] 所有原有测试通过
- [ ] domains/credential/ 总行数 ≥ 2,858

**工时**: 5 小时

---

### Day 1-2 验收标准

- [ ] domains/credential/ 总行数 ≥ **2,858 行**（不是当前的 814 行）
- [ ] 测试覆盖率 ≥ 85%
- [ ] 所有功能保持与旧代码一致
- [ ] `go test ./domains/credential/ -v` 全部通过
- [ ] 无循环依赖

---

## 3. Day 3-4: Executor 完整迁移

### 目标

将 `routing/executor_*.go`（4,361 行，14 个文件）和 `relay/handler.go`（3,116 行）完整迁移到 `domains/streaming/`。

### 3.1 任务 R1.4: 迁移 routing/executor_*.go

**目标文件**:
- 旧: `routing/executor_*.go` (14 个文件，4,361 行)
- 新: `domains/streaming/executors/`

**执行步骤**:

```bash
# 步骤 1: 创建 executors 子目录
mkdir -p domains/streaming/executors

# 步骤 2: 复制所有 executor 文件
for f in routing/executor_*.go; do
    base=$(basename "$f")
    cp "$f" "domains/streaming/executors/$base"
done

# 步骤 3: 更新 import 路径
find domains/streaming/executors/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/routing|"__REPO_URL_3__/domains/streaming/executors|g' {} \;

# 步骤 4: 修复 package 声明
find domains/streaming/executors/ -name "*.go" -exec sed -i '' \
  's|^package routing|package executors|' {} \;

# 步骤 5: 验证编译
go build ./domains/streaming/executors/

# 步骤 6: 运行测试
go test ./domains/streaming/executors/ -v
```

**⚠️ 必须保留的 Executor**:
- [ ] `executor_chat.go` (973 行) — OpenAI Chat 协议
- [ ] `executor_anthropic.go` (880 行) — Anthropic 协议
- [ ] `executor_common.go` (135 行) — 公共逻辑
- [ ] `executor_ir_test.go` (542 行) — IR 转换测试
- [ ] `executor_glm_test.go` (192 行) — GLM 协议测试
- [ ] 其他 9 个测试文件

**验收标准**:
- [ ] `domains/streaming/executors/` 至少 4,361 行
- [ ] 14 个 executor 文件全部迁移
- [ ] 所有原有测试通过
- [ ] 包含完整的 OpenAI + Anthropic + GLM 协议支持

**工时**: 8 小时

---

### 3.2 任务 R1.5: 迁移 relay/handler.go + stream_*.go

**目标文件**:
- 旧: `relay/handler.go` (3,116 行)
- 旧: `relay/responses_stream.go` (341 行)
- 旧: `relay/stream_*.go` (~1,000 行)
- 新: `domains/streaming/`

**执行步骤**:

```bash
# 步骤 1: 复制核心处理器
cp relay/handler.go domains/streaming/handler.go
cp relay/responses_stream.go domains/streaming/responses_stream.go

# 步骤 2: 复制流式相关文件
for f in relay/stream_*.go; do
    if [[ "$f" != *_test.go ]]; then
        base=$(basename "$f")
        cp "$f" "domains/streaming/$base"
    fi
done

# 步骤 3: 更新 import 路径
find domains/streaming/handler.go domains/streaming/responses_stream.go domains/streaming/stream_*.go \
  -exec sed -i '' \
  's|"__REPO_URL_3__/relay|"__REPO_URL_3__/domains/streaming|g' {} \;

# 步骤 4: 修复 package 声明
sed -i '' 's|^package relay|package streaming|' domains/streaming/handler.go
sed -i '' 's|^package relay|package streaming|' domains/streaming/responses_stream.go
find domains/streaming/stream_*.go -exec sed -i '' 's|^package relay|package streaming|' {} \;

# 步骤 5: 验证编译
go build ./domains/streaming/

# 步骤 6: 运行测试
go test ./domains/streaming/ -v
```

**⚠️ 必须保留的功能**:
- [ ] 完整的错误处理
- [ ] 重试逻辑
- [ ] telemetry 集成
- [ ] SSE 流式响应

**验收标准**:
- [ ] `domains/streaming/handler.go` 至少 3,116 行
- [ ] `domains/streaming/responses_stream.go` 至少 341 行
- [ ] domains/streaming/ 总行数 ≥ **7,477 行**（包含 executors）
- [ ] 所有原有测试通过

**工时**: 6 小时

---

### Day 3-4 验收标准

- [ ] `domains/streaming/` 总行数 ≥ **7,477 行**（不是当前的 402 行）
- [ ] 包含完整的 executor_* 实现
- [ ] 包含 relay/handler.go 核心处理器
- [ ] 可以实际调用上游 API
- [ ] E2E 测试通过

---

## 4. Day 5-6: 压缩 + 协议转换完整迁移

### 目标

迁移 `compressor/`（8,410 行）、`routing/context_summarize.go`（1,368 行）、`relay/anthropic_*.go`（5,055 行）到对应的 hooks/compression 和 transformation 领域。

### 4.1 任务 R1.6: 迁移 compressor/ 核心实现

**⚠️ 关键任务**: 当前 `domains/hooks/compression/` 只有 604 行简化版，必须补充完整实现（至少 3,000 行）

**目标文件**:
- 旧: `compressor/compressor.go` (~500 行，6 种模式)
- 旧: `compressor/` 全部 34 个文件
- 当前: `domains/hooks/compression/` (604 行简化版) - **需扩展**

**执行步骤**:

```bash
# 步骤 1: 备份当前简化版
mv domains/hooks/compression /tmp/compression_simplified_v1
mkdir -p domains/hooks/compression

# 步骤 2: 复制完整 compressor 包
cp -r compressor/* domains/hooks/compression/

# 步骤 3: 更新 import 路径
find domains/hooks/compression/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/compressor|"__REPO_URL_3__/domains/hooks/compression|g' {} \;

# 步骤 4: 修复 package 声明
find domains/hooks/compression/ -name "*.go" -exec sed -i '' \
  's|^package compressor|package compression|' {} \;

# 步骤 5: 验证编译
go build ./domains/hooks/compression/

# 步骤 6: 运行测试
go test ./domains/hooks/compression/ -v
```

**⚠️ 必须保留的功能（6 种模式）**:
- [ ] `ModeOff` (0) — 不压缩
- [ ] `ModeAutoThreshold` (1) — 自动阈值
- [ ] `ModeOn4xx` (2) — 4xx 后压缩
- [ ] `ModeDeltaOnly` (3) — 仅增量
- [ ] `ModeSmart` (4) — 智能压缩
- [ ] `ModeAggressive` (5) — 激进压缩
- [ ] 三层压缩策略（机械去重 → Memora L1 → LLM 总结）
- [ ] Rebuilder 支持（OpenAI / Anthropic）
- [ ] 完整的 telemetry 集成

**验收标准**:
- [ ] `domains/hooks/compression/` 至少 **3,000 行**（不是 604 行）
- [ ] 包含 6 种压缩模式（不是 3 种简化版）
- [ ] 包含 Rebuilder 集成
- [ ] 包含 Memora L1 集成
- [ ] 所有原有测试通过

**工时**: 8 小时

---

### 4.2 任务 R1.7: 迁移 routing/context_summarize.go

**目标文件**:
- 旧: `routing/context_summarize.go` (1,368 行)
- 旧: `routing/context_summarize_test.go` (736 行)
- 新: `domains/hooks/compression/context_summarize.go`

**执行步骤**:

```bash
# 步骤 1: 复制文件
cp routing/context_summarize.go domains/hooks/compression/context_summarize.go
cp routing/context_summarize_test.go domains/hooks/compression/context_summarize_test.go

# 步骤 2: 更新 import 路径
find domains/hooks/compression/context_summarize*.go -exec sed -i '' \
  's|"__REPO_URL_3__/routing|"__REPO_URL_3__/domains/hooks/compression|g' {} \;

# 步骤 3: 修复 package 声明
sed -i '' 's|^package routing|package compression|' domains/hooks/compression/context_summarize.go

# 步骤 4: 验证编译
go build ./domains/hooks/compression/

# 步骤 5: 运行测试
go test ./domains/hooks/compression/ -run TestContextSummarize -v
```

**⚠️ 必须保留的功能**:
- [ ] 三层压缩策略
- [ ] 错误恢复逻辑
- [ ] 与 compressor/ 的集成

**验收标准**:
- [ ] `domains/hooks/compression/context_summarize.go` 至少 1,368 行
- [ ] 三层压缩策略完整
- [ ] 所有原有测试通过

**工时**: 4 小时

---

### 4.3 任务 R1.8: 迁移 relay/anthropic_*.go

**目标文件**:
- 旧: `relay/anthropic_*.go` (21 个文件，5,055 行)
- 新: `domains/transformation/anthropic/`

**执行步骤**:

```bash
# 步骤 1: 创建 anthropic 子目录
mkdir -p domains/transformation/anthropic

# 步骤 2: 复制所有 anthropic 文件
for f in relay/anthropic_*.go; do
    base=$(basename "$f")
    cp "$f" "domains/transformation/anthropic/$base"
done

# 步骤 3: 更新 import 路径
find domains/transformation/anthropic/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/relay|"__REPO_URL_3__/domains/transformation/anthropic|g' {} \;

# 步骤 4: 修复 package 声明
find domains/transformation/anthropic/ -name "*.go" -exec sed -i '' \
  's|^package relay|package anthropic|' {} \;

# 步骤 5: 验证编译
go build ./domains/transformation/anthropic/

# 步骤 6: 运行测试
go test ./domains/transformation/anthropic/ -v
```

**⚠️ 必须保留的功能**:
- [ ] 完整的 Anthropic 协议转换
- [ ] 流式响应处理
- [ ] Tool call 支持
- [ ] 21 个文件的完整功能

**验收标准**:
- [ ] `domains/transformation/anthropic/` 至少 5,055 行
- [ ] 21 个 anthropic 文件全部迁移
- [ ] 所有原有测试通过

**工时**: 6 小时

---

### Day 5-6 验收标准

- [ ] `domains/hooks/compression/` 总行数 ≥ **4,972 行**（不是 604 行）
- [ ] `domains/transformation/anthropic/` ≥ 5,055 行
- [ ] 所有 6 种压缩模式可用
- [ ] Anthropic 协议完整支持
- [ ] E2E 测试通过

---

## 5. Day 7: 剩余横切关注点 + Pipeline 集成

### 5.1 任务 R1.9: 迁移 telemetry/

**目标文件**:
- 旧: `telemetry/` (9 个文件，3,270 行)
- 新: `domains/hooks/observability/telemetry/`

**执行步骤**:

```bash
# 步骤 1: 创建子目录
mkdir -p domains/hooks/observability/telemetry

# 步骤 2: 复制 telemetry 包
cp -r telemetry/* domains/hooks/observability/telemetry/

# 步骤 3: 更新 import 路径
find domains/hooks/observability/telemetry/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/telemetry|"__REPO_URL_3__/domains/hooks/observability/telemetry|g' {} \;

# 步骤 4: 修复 package 声明
find domains/hooks/observability/telemetry/ -name "*.go" -exec sed -i '' \
  's|^package telemetry|package telemetry|' {} \;

# 步骤 5: 验证
go build ./domains/hooks/observability/telemetry/
go test ./domains/hooks/observability/telemetry/ -v
```

**工时**: 3 小时

---

### 5.2 任务 R1.10: 补齐 audit/

**目标文件**:
- 旧: `audit/` (6 个文件，2,444 行)
- 当前: `domains/hooks/audit/` (4 个文件，376 行) - **需补齐**

**执行步骤**:

```bash
# 步骤 1: 复制 audit 包
cp -r audit/* domains/hooks/audit/

# 步骤 2: 更新 import 路径
find domains/hooks/audit/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/audit|"__REPO_URL_3__/domains/hooks/audit|g' {} \;

# 步骤 3: 修复 package 声明
find domains/hooks/audit/ -name "*.go" -exec sed -i '' \
  's|^package audit|package audit|' {} \;

# 步骤 4: 验证
go build ./domains/hooks/audit/
go test ./domains/hooks/audit/ -v
```

**工时**: 2 小时

---

### 5.3 任务 R1.11: 补齐 transform/transport

**目标文件**:
- 旧: `transform/` (8 个文件，2,765 行)
- 旧: `transport/` (19 个文件，3,458 行)
- 当前: `domains/transformation/` (5 个文件，407 行) - **需补齐**

**执行步骤**:

```bash
# 步骤 1: 复制 transform 包
cp -r transform/* domains/transformation/

# 步骤 2: 复制 transport 包
cp -r transport/* domains/transformation/

# 步骤 3: 更新 import 路径
find domains/transformation/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/transform|"__REPO_URL_3__/domains/transformation|g' {} \;
find domains/transformation/ -name "*.go" -exec sed -i '' \
  's|"__REPO_URL_3__/transport|"__REPO_URL_3__/domains/transformation|g' {} \;

# 步骤 4: 修复 package 声明
find domains/transformation/ -name "*.go" -exec sed -i '' \
  's|^package transform|package transformation|' {} \;
find domains/transformation/ -name "*.go" -exec sed -i '' \
  's|^package transport|package transformation|' {} \;

# 步骤 5: 验证
go build ./domains/transformation/
go test ./domains/transformation/ -v
```

**工时**: 4 小时

---

### 5.4 任务 R1.12: 集成 Pipeline 到 cmd/gateway/main.go

**目标**: 在 `cmd/gateway/main.go` 中创建 Pipeline 实例并注册所有 Hook。

**执行步骤**:

```go
// cmd/gateway/main.go

// 在 main() 中添加
pipeline := pipeline.NewRequestPipeline()

// Stage 1: Authentication
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "authentication",
    Phase: pipeline.PhaseAuthentication,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        authentication.NewAPIKeyAuthHook(apiKeyStore),
    },
})

// Stage 2: Pre-Routing
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "pre_routing",
    Phase: pipeline.PhasePreRouting,
    Mode:  pipeline.ModeParallel,
    Hooks: []pipeline.Hook{
        identity.NewClientIdentityHook(identityBuilder),
        session.NewSessionLoaderHook(sessionStore),
    },
})

// Stage 3: Routing
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "routing",
    Phase: pipeline.PhaseRouting,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        routing.NewRouteResolverHook(routeResolver),
    },
})

// Stage 4: Credential
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "credential",
    Phase: pipeline.PhaseRouting,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        credential.NewCredentialSelectorHook(credentialPool, breaker, limiter),
    },
})

// Stage 5: Transform
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "transform",
    Phase: pipeline.PhaseTransform,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        transformation.NewTransformerHook(transformer),
    },
})

// Stage 6: Upstream (Executor)
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "upstream",
    Phase: pipeline.PhaseUpstream,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        streaming.NewExecutorHook(executorPool),
    },
})

// Stage 7: Response
pipeline.AddStage(&pipeline.PipelineStage{
    Name:  "response",
    Phase: pipeline.PhaseResponse,
    Mode:  pipeline.ModeSequential,
    Hooks: []pipeline.Hook{
        streaming.NewResponseHandlerHook(responseHandler),
    },
})

// 执行 Pipeline
envelope := domain.NewRequestEnvelope(req)
if err := pipeline.Execute(ctx, envelope); err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
}
```

**验收标准**:
- [ ] `cmd/gateway/main.go` 包含 Pipeline 实例化
- [ ] 所有 7 个 Stage 注册
- [ ] 所有 Hook 使用完整实现（不是简化版）
- [ ] 启动成功，无编译错误
- [ ] E2E 测试通过

**工时**: 3 小时

---

### 5.5 任务 R1.13: 删除旧代码（移至 _deprecated/）

**⚠️ 重要**: 必须在 Pipeline 集成验证通过后执行

**执行步骤**:

```bash
# 步骤 1: 创建 _deprecated/ 结构
mkdir -p _deprecated/{identity,sessions,auth,routing,credentialstate,circuit,limiter,provider,relay,transform,transport,compressor,cache,observability,audit,telemetry,security}

# 步骤 2: 移动旧代码
mv identity _deprecated/identity
mv sessions _deprecated/sessions
mv auth _deprecated/auth
mv routing _deprecated/routing
mv credentialstate _deprecated/credentialstate
mv circuit _deprecated/circuit
mv limiter _deprecated/limiter
mv provider _deprecated/provider
mv relay _deprecated/relay
mv transform _deprecated/transform
mv transport _deprecated/transport
mv compressor _deprecated/compressor
mv cache _deprecated/cache
mv observability _deprecated/observability
mv audit _deprecated/audit
mv telemetry _deprecated/telemetry
mv security _deprecated/security

# 步骤 3: 更新所有 import 路径
find . -name "*.go" -not -path "./_deprecated/*" -exec sed -i '' \
  -e 's|"__REPO_URL_3__/identity|"__REPO_URL_3__/domains/identity|g' \
  -e 's|"__REPO_URL_3__/sessions|"__REPO_URL_3__/domains/session|g' \
  -e 's|"__REPO_URL_3__/auth|"__REPO_URL_3__/domains/authentication|g' \
  -e 's|"__REPO_URL_3__/routing|"__REPO_URL_3__/domains/routing|g' \
  -e 's|"__REPO_URL_3__/credentialstate|"__REPO_URL_3__/domains/credential|g' \
  -e 's|"__REPO_URL_3__/circuit|"__REPO_URL_3__/domains/credential|g' \
  -e 's|"__REPO_URL_3__/limiter|"__REPO_URL_3__/domains/credential|g' \
  -e 's|"__REPO_URL_3__/provider|"__REPO_URL_3__/domains/provider|g' \
  -e 's|"__REPO_URL_3__/relay|"__REPO_URL_3__/domains/streaming|g' \
  -e 's|"__REPO_URL_3__/transform|"__REPO_URL_3__/domains/transformation|g' \
  -e 's|"__REPO_URL_3__/transport|"__REPO_URL_3__/domains/transformation|g' \
  -e 's|"__REPO_URL_3__/compressor|"__REPO_URL_3__/domains/hooks/compression|g' \
  -e 's|"__REPO_URL_3__/cache|"__REPO_URL_3__/domains/hooks/cache|g' \
  -e 's|"__REPO_URL_3__/observability|"__REPO_URL_3__/domains/hooks/observability|g' \
  -e 's|"__REPO_URL_3__/audit|"__REPO_URL_3__/domains/hooks/audit|g' \
  -e 's|"__REPO_URL_3__/telemetry|"__REPO_URL_3__/domains/hooks/observability/telemetry|g' \
  -e 's|"__REPO_URL_3__/security|"__REPO_URL_3__/domains/hooks/security|g' \
  {} \;

# 步骤 4: 验证编译
go build ./...

# 步骤 5: 运行所有测试
go test ./... -short

# 步骤 6: Git tag
git tag -a "phase1.5-complete-20260625" -m "Phase 1.5: Complete mature code reuse"
```

**工时**: 2 小时

---

### Day 7 验收标准

- [ ] Pipeline 集成到 main.go 并通过测试
- [ ] 旧代码移至 `_deprecated/`
- [ ] 所有 import 路径更新
- [ ] `go build ./...` 成功
- [ ] 所有测试通过

---

## 6. 完整验收标准

### 6.1 代码复用率指标

| 领域 | 当前复用率 | 目标复用率 | 验证方式 |
|------|-----------|-----------|---------|
| **identity** | 248% | ≥ 200% | `wc -l domains/identity/*.go` |
| **authentication** | 224% | ≥ 200% | `wc -l domains/authentication/*.go` |
| **session** | 206% | ≥ 200% | `wc -l domains/session/*.go` |
| **credential** | 20% | **≥ 95%** | `wc -l domains/credential/*.go` ≥ 2,858 |
| **provider** | 57% | **≥ 90%** | `wc -l domains/provider/*.go` ≥ 1,000 |
| **streaming** | 1.7% | **≥ 30%** | `wc -l domains/streaming/*.go` ≥ 7,477 |
| **transformation** | 6.5% | **≥ 80%** | `wc -l domains/transformation/*.go` ≥ 6,000 |
| **hooks/compression** | 7.2% | **≥ 50%** | `wc -l domains/hooks/compression/*.go` ≥ 4,972 |
| **hooks/audit** | 15.4% | **≥ 90%** | `wc -l domains/hooks/audit/*.go` ≥ 2,200 |
| **hooks/observability** | 76.9% | **≥ 80%** | `wc -l domains/hooks/observability/*.go` ≥ 3,500 |

### 6.2 整体指标

| 指标 | 当前 | 目标 |
|------|------|------|
| **整体复用率** | 20.1% | **≥ 70%** |
| **domains/ 总行数** | 13,279 | **≥ 46,000** |
| **核心领域完成度** | 6/9 | **9/9** |
| **功能完整性** | 简化版 | **完整版** |

### 6.3 测试指标

| 指标 | 当前 | 目标 |
|------|------|------|
| **测试覆盖率** | 80.5% - 96.0% | ≥ 85% |
| **循环依赖** | 0 | 0 |
| **E2E 测试** | 未集成 | 全部通过 |
| **P99 延迟** | 未知 | ≤ 旧版 + 10ms |

### 6.4 验证命令

```bash
# 1. 整体代码复用率
OLD_LINES=$(find _deprecated identity sessions auth routing credentialstate circuit limiter provider relay transform transport compressor cache observability audit telemetry security -name "*.go" 2>/dev/null | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}')
NEW_LINES=$(find domains -name "*.go" 2>/dev/null | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}')
echo "复用率: $(echo "scale=1; $NEW_LINES * 100 / $OLD_LINES" | bc)%"

# 2. 所有测试通过
go test ./... -count=1 -timeout=300s

# 3. 编译成功
go build ./cmd/gateway/

# 4. 启动服务
./gateway &
sleep 5
curl -i http://localhost:__PORT_3__/healthz

# 5. E2E 测试
./scripts/e2e-core-domains.sh
./scripts/e2e-hook-pipeline.sh

# 6. 性能测试
go test -bench=. -benchmem ./domains/pipeline/
```

---

## 7. 风险与缓解

### 7.1 风险矩阵

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| **import 循环依赖** | 高 | 高 | 保留旧包作为依赖，逐步解耦 |
| **测试失败** | 高 | 中 | 每个任务单独测试，发现问题立即修复 |
| **功能丢失** | 中 | 高 | 逐行对比旧代码，确保完整迁移 |
| **性能退化** | 低 | 高 | 每个 Stage 打点，对比 P99 延迟 |
| **E2E 测试不通过** | 中 | 高 | 先在测试环境验证，再部署生产 |

### 7.2 回滚策略

```bash
# 每个 Phase 完成后打 tag
git tag phase1.5-day2-complete-20260625
git tag phase1.5-day4-complete-20260625
git tag phase1.5-day6-complete-20260625
git tag phase1.5-complete-20260625

# 如发现严重问题
git revert <commit-range>
# 或
git reset --hard phase1.5-day6-complete-20260625
```

### 7.3 特性开关（Feature Flag）

```go
// config.yml
feature_flags:
  use_domain_pipeline: false  # 默认关闭
  use_new_credential: false   # 默认关闭
  use_new_compression: false  # 默认关闭

// 代码
if config.FeatureFlags.UseDomainPipeline {
    pipeline.Execute(ctx, envelope)
} else {
    // 旧代码路径
    handler.HandleRequest(ctx, req)
}
```

---

## 8. 时间表

| Day | 任务 | 预计工时 |
|-----|------|---------|
| **Day 1** | R1.1 + R1.2（凭据 + 熔断器） | 7h |
| **Day 2** | R1.3（limiter 完整迁移） | 5h |
| **Day 3** | R1.4 1/2（executor 迁移） | 8h |
| **Day 4** | R1.4 2/2 + R1.5（handler 迁移） | 6h |
| **Day 5** | R1.6（compressor 核心） | 8h |
| **Day 6** | R1.7 + R1.8（context_summarize + anthropic） | 10h |
| **Day 7** | R1.9-R1.13（telemetry + audit + transform + pipeline + cleanup） | 14h |
| **合计** | 13 个任务 | **58h** |

---

## 9. 后续行动

### Phase 1.5 完成后的下一步

1. **Phase 2 启动条件检查**:
   - [ ] `domains/` 总行数 ≥ 46,000 行
   - [ ] 整体复用率 ≥ 70%
   - [ ] 所有核心功能有完整实现
   - [ ] E2E 测试通过
   - [ ] Pipeline 已上线

2. **Phase 2 准备**:
   - 实现智能体生态系统（`domains/agent-ecosystem/`）
   - 实现会话检查器框架（`domains/hooks/session-inspector/`）
   - 接入 Redis 事件总线

3. **Phase 3 准备**:
   - 删除 `_deprecated/` 中的旧代码
   - 性能优化
   - 文档完善

---

## 10. 总结

### 核心原则

| 原则 | 说明 |
|------|------|
| **成熟代码优先** | 65,871 行生产验证的代码，能复用就复用 |
| **完整功能保留** | 不要简化，不要"最小契约" |
| **实现优先** | 接口自然形成，不要空接口 |
| **务实架构适配** | 允许"不纯净"，渐进重构 |

### 关键教训

1. **不要"重写"成熟代码**: 重写会丢失 80%+ 的功能
2. **不要"简化示例"**: 简化版无法满足生产需求
3. **不要"接口优先"**: 没有实现的接口毫无价值
4. **不要"理想主义"**: 务实复用，渐进重构

### 成功标准

- [ ] 整体复用率 ≥ 70%
- [ ] `domains/` 总行数 ≥ 46,000
- [ ] 核心领域完成度 9/9
- [ ] 功能完整性 = 完整版
- [ ] Pipeline 已上线

---

**计划完成**

**下一步**: 开始执行 Day 1（迁移 credentialstate/writer.go + circuit/breaker.go）