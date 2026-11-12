# 提示词优化模块 —— 集成指南

> 本文描述的是**已落地并通过测试验证**的实现。
> 2026-11-11 测试验证轮修掉了一批"文档写得像已实现、代码里其实没有"的偏差，
> 本文档已按当前代码校正。

## 1. 现状速览

| 项 | 状态 |
|----|------|
| 核心模块 | ✅ `internal/prompt-optimizer/`（7 个包） |
| 优化策略 | ✅ 结构化 / Few-shot / 迭代（3 种） |
| memora 集成 | ✅ 对接 `domains/memory.Reader` |
| LLM 通道 | ✅ OpenAI 兼容客户端（`internal/prompt-optimizer/llm`） |
| Harness 注入 | ✅ 工具 / 安全 / 错误处理 / 可观测性 |
| HTTP 服务 | ✅ `cmd/prompt-optimizer`（标准库，无新依赖） |
| Docker 环境 | ✅ `docker-compose.prompt-optimizer.yml` |
| 单元测试 | ✅ 7 个包全覆盖 |
| 变异验证 | ✅ 14 条臂全部被抓住 |
| 端到端测试 | ✅ 31 项断言全通过 |

**未实现**（README 曾承诺但从未落地）：`ensemble.go`（集成策略）、
`dynamic.go`（动态上下文骨架）、`comparator.go` / `validator.go`
（已并入 `evaluation/evaluator.go`）、`examples/advanced.go`。

## 2. 目录与依赖

```
internal/prompt-optimizer/
├── optimizer.go        # 核心优化器 + 意图分析 + 策略调度
├── interfaces.go       # ContextManager / HarnessInjector / Evaluator 接口
├── strategies/         # structural / fewshot / iterative
├── context/            # manager.go（memora）+ window.go（token 预算）
├── harness/            # injector.go
├── evaluation/         # evaluator.go + prompt_quality.go
├── llm/                # openai.go（OpenAI 兼容客户端）
└── examples/           # basic.go（库包）
```

依赖方向（无环）：`llm` ← `evaluation` / `strategies` ← 顶层包。
**不引入任何新第三方依赖** —— 仓库用 `vendor/` 模式，多一个依赖就要动 vendor 树。

## 3. 三种集成方式

### 方式一：作为库

```go
import (
    optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
    "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies"
)

opt := optimizer.NewOptimizer(
    optimizer.WithStrategies(
        strategies.NewStructural(strategies.DefaultStructuralConfig()),
        strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil),
    ),
)

result, err := opt.Optimize(ctx, "帮我翻译这段文字", optimizer.OptimizeOptions{
    TaskType:      "translation",
    Domain:        "technical",
    InjectHarness: true,
})
```

### 方式二：独立 HTTP 服务

```bash
docker compose -f docker-compose.prompt-optimizer.yml up -d --build
curl -s localhost:8090/health
```

端点：

| 端点 | 用途 | 需要 LLM 通道 |
|------|------|--------------|
| `GET /health` | 健康检查 | 否 |
| `POST /api/v1/optimize` | 优化单条提示词 | 否 |
| `POST /api/v1/optimize/batch` | 批量优化 | 否 |
| `POST /api/v1/optimize/evaluate` | 优化 + 评估 | **是** |
| `POST /api/v1/compare` | 对比两个提示词 | `mode=quality` 否 / `mode=output` 是 |

### 方式三：中间件

仓库用标准库 `net/http`，中间件写成普通 `http.Handler`：

```go
func PromptOptimizerMiddleware(opt optimizer.Optimizer) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req ChatRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, "bad request", http.StatusBadRequest)
            return
        }
        if req.EnableOptimization {
            optimized, err := opt.Optimize(r.Context(), req.Prompt, optimizer.OptimizeOptions{
                TaskType: req.TaskType,
            })
            if err == nil {
                req.Prompt = optimized.Optimized
            }
        }
        // 继续原处理链
    })
}
```

> ⚠️ 中间件里**不要**吞掉错误后继续用原提示词 —— 那会让调用方以为优化成功了。
> 要么用优化结果，要么显式记录失败。

## 4. 接入 memora

优化器对接仓库既有的 `domains/memory.Reader` 接口，**不另造 HTTP 客户端**。

```go
import (
    memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
    promptcontext "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/context"
)

memoraClient := memclient.NewClient(memclient.ClientConfig{
    BaseURL:   os.Getenv("LLM_GATEWAY_MEMORA_BASE_URL"),
    APIKey:    os.Getenv("LLM_GATEWAY_MEMORA_API_KEY"),
    ProjectID: os.Getenv("LLM_GATEWAY_MEMORA_PROJECT_ID"),
})

cfg := promptcontext.DefaultConfig()
cfg.UserID = "user-42"   // 记忆按用户隔离，不要留空

opt := optimizer.NewOptimizer(
    optimizer.WithContextManager(promptcontext.NewManager(memoraClient, cfg)),
)
```

行为契约：

| 情况 | 行为 |
|------|------|
| reader 为 nil / 被禁用 | 降级为「无历史」，**不报错**，优化继续 |
| 后端调用失败 | **返回错误**（区分「查不到」与「查失败」）|
| 相似度 < 阈值 | 丢弃该条记忆 |
| 超出 token 预算 | 按分数降序裁剪，超出即停 |
| `UserID` 为空 | 记忆后端按用户隔离，跨用户会取到无关内容 —— **必须设置** |

## 5. 接入内部小模型

优化器的 LLM 通道走 OpenAI Chat Completions 协议，可直接指向本仓网关
（网关本身就是 OpenAI 兼容的，由它路由到内部小模型）：

```go
client, err := llm.New(llm.Config{
    BaseURL: os.Getenv("LLM_BASE_URL"),  // 例：http://gateway:8080/v1
    APIKey:  os.Getenv("LLM_API_KEY"),
    Model:   "your-small-model",
})
evaluator := evaluation.NewEvaluator(evaluation.DefaultEvaluatorConfig(), evaluation.DefaultMetrics(), client)
opt := optimizer.NewOptimizer(optimizer.WithEvaluator(evaluator))
```

或直接用环境变量启动服务：

```bash
LLM_BASE_URL=http://gateway:8080/v1 \
LLM_API_KEY=... \
EVALUATOR_MODEL=your-small-model \
./prompt-optimizer-service
```

**未配置 LLM 通道时**：需要模型的路径（`mode=output` 对比、带评估的优化、
迭代优化策略）会**明确报错**，不会静默返回 0 分或假装优化过。
不需要模型的结构化优化与 quality 模式对比照常可用。

## 6. 配置

### 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `PORT` / `-http` | `:8090` | 监听地址 |
| `LOG_LEVEL` / `-log-level` | `info` | 日志级别 |
| `LLM_BASE_URL` | 空 | LLM 通道基址（空 = 关闭模型相关路径）|
| `LLM_API_KEY` | 空 | LLM 鉴权 |
| `EVALUATOR_MODEL` | `gpt-4` | 评估/优化模型名 |
| `LLM_GATEWAY_MEMORA_BASE_URL` | 空 | memora 基址（空 = 不接记忆）|
| `LLM_GATEWAY_MEMORA_API_KEY` | 空 | memora 鉴权 |
| `LLM_GATEWAY_MEMORA_PROJECT_ID` | 空 | memora 项目域（fail-closed，缺了会被拒）|
| `MEMORA_USER_ID` | `prompt-optimizer` | 记忆归属用户 |

### 配置文件

`configs/prompt-optimizer.yaml` 是**配置参考**，服务本身用 flag + 环境变量
（不引入 YAML 解析依赖）。两者语义一致。

## 7. 测试

```bash
# 单元测试（7 个包）
go test ./internal/prompt-optimizer/...

# 变异验证（14 条臂，全部必须被抓住）
./scripts/audit/prompt-optimizer-mutation-verify.sh

# Docker 端到端（31 项断言）
./scripts/test-prompt-optimizer.sh all
```

端到端脚本子命令：`build` / `start` / `test` / `stop` / `logs` / `clean`。

### 变异验证是什么、为什么必须有

绿色测试证明不了"测试有牙"。脚本把已修复的缺陷逐个改回去，
确认对应测试**会转红**：

- 变异后仍全绿 ⇒ 这条测试是哑门 ⇒ **失败**（比没有门更坏）；
- 变异后编译失败 ⇒ 红的原因不是断言 ⇒ **不算证据**；
- 变异后测试红 ⇒ 抓住 ⇒ 通过。

14 条臂覆盖：完整性指标恒真、迭代策略死代码、意图分析退化、评估器 nil
panic、除零、阈值过滤、token 预算、章节排序、质量过滤、任务专属约束、
角色定义、预算检查、JSON 字段名、耗时序列化。

## 8. 已知边界（诚实清单）

1. **结构质量分 ≠ 效果分。** `prompt_quality` 衡量"要素齐不齐"，
   `output_similarity` 衡量"输出像不像期望答案"，**两者都不等于任务正确率**。
2. **内置 Few-shot 示例是硬编码固定样本**，不是生产流量挖掘的真实分布。
3. **没有端到端效果实验。** 历史文档里的 "+43% / +19%" 等数字来自
   论文的其它任务（BBH / GSM8K），**不是本仓提示词上的实测值**。
   真实提升必须接入生产流量 + 真实度量后重测。
4. **迭代优化需要 LLM 客户端 + 测试用例**，二者缺一即为 no-op。
5. **memora 检索质量取决于后端**；本模块只做阈值过滤与预算裁剪，
   不改变召回质量。
6. **`applyIntent` 是关键词启发式**，不调用 LLM。显式传 `TaskType`/`Domain`
   更准。

## 9. 相关文档

- 模块文档：`internal/prompt-optimizer/README.md`
- 测试验证记录：`docs/PROMPT_OPTIMIZER_TEST_VALIDATION.md`
- 变更记录：`CHANGELOG.md`
