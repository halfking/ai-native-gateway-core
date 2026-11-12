# Prompt Optimizer Module

> ⚠️ **本文档描述的是本仓实际实现，不是设计理想态。**
> 2026-11-11 的测试验证轮里，原设计稿里的多个文件（`ensemble.go` / `dynamic.go` /
> `comparator.go` / `validator.go` / `advanced.go`）**从未落地**，文档却按"已实现"写。
> 本文档已按实际代码校正，缺失的部分明确标注为「未实现」。

## 概述

把提示词优化做成一个**可插拔的标准模块**：意图分析 → 上下文增强 → 结构化重构 →
Harness 要素注入 →（可选）度量驱动的迭代优化。

研究基础：DSPy（提示词即参数）、OPRO（LLM 作为优化器）、AGOPS（任务特定指南）、
PromptBreeder（多样性选择）。

## 目录结构（实际）

```
internal/prompt-optimizer/
├── README.md                 # 本文档
├── optimizer.go              # 核心优化器 + 意图分析 + 策略调度
├── interfaces.go             # ContextManager / HarnessInjector / Evaluator 接口
├── strategies/
│   ├── structural.go         # 结构化增强策略
│   ├── fewshot.go            # Few-shot 示例优化策略
│   └── iterative.go          # 迭代优化策略（OPRO 风格）
├── context/
│   ├── manager.go            # 上下文管理器（对接 domains/memory.Reader）
│   └── window.go             # token 预算窗口
├── harness/
│   └── injector.go           # Harness 要素注入（工具/安全/错误/可观测）
├── evaluation/
│   ├── evaluator.go          # 评估器 + 4 项内置指标
│   └── prompt_quality.go     # 结构质量评估（不需要 LLM）
├── llm/
│   └── openai.go             # OpenAI 兼容客户端（"内部小模型"通道）
└── examples/
    └── basic.go              # 三种用法示例（库包，可被测试调用）
```

配套（仓根）：

```
cmd/prompt-optimizer/main.go                    # 独立 HTTP 服务
cmd/prompt-optimizer-mockllm/main.go            # 本地测试用的 mock LLM
Dockerfile.prompt-optimizer                     # 多阶段构建
Dockerfile.prompt-optimizer.dockerignore        # 本镜像专属忽略规则
Dockerfile.prompt-optimizer-mockllm             # mock LLM 镜像
docker-compose.prompt-optimizer.yml             # 本地测试环境
configs/prompt-optimizer.yaml                   # 配置参考
scripts/test-prompt-optimizer.sh                # 端到端测试脚本
scripts/audit/prompt-optimizer-mutation-verify.sh # 变异验证（14 臂）
docs/PROMPT_OPTIMIZER_TEST_VALIDATION.md       # 测试验证记录
```

## 优化流程

```
原始提示词
    ↓
[1. 意图分析] analyzeIntent — 任务类型/领域/显式约束（关键词启发式，不调 LLM）
    ↓
[2. 上下文准备] prepareContext — memora 历史 + 示例 + 领域约束（可跳过）
    ↓
[3. 策略应用] structural → fewshot → iterative（按注册顺序）
    ↓
[4. Harness 注入] 工具定义 / 安全规则 / 错误处理 / 可观测性（可关闭）
    ↓
优化后提示词
```

## 策略详解

### structural —— 结构化增强

产出固定的六段式结构：`# 角色定义` → `# 任务目标` → `# 执行步骤` →
`# 约束条件` → `# 输出格式` → `# 质量检查`。

- **角色定义**随任务类型变化（翻译专家 / 资深软件工程师 / 数据分析专家 …）；
- **约束条件**由「通用约束 + 任务专属约束」组成；
- **输出格式**随任务类型变化（代码给代码块、分析给 Markdown 小节）。

### fewshot —— Few-shot 示例

- 示例来源：内建示例 + `ExampleStore` 接口（当前未接生产实现）；
- 选择策略：`diversity`（默认，最大化多样性）/ `similarity` / `difficulty`；
- 质量阈值过滤（默认 0.8）+ 动态数量（按平均质量定 2~5 条）。

### iterative —— 迭代优化（OPRO 风格）

LLM 生成候选变体 → 评估器打分 → 保留优胜者 → 收敛检测 / 早停。

> **前置条件（硬性）**：必须同时提供 **LLM 客户端** 与 **测试用例**。
> 二者缺一，策略退化为 no-op（返回原提示词）。
> 这是刻意的：没有度量就没有优化目标，与其瞎改不如不动。

## 上下文工程（memora 集成）

对接仓库既有的 `domains/memory.Reader` 接口（生产环境直接传
`domains/memory/client.Client`），不另造 HTTP 客户端。

```go
cfg := promptcontext.DefaultConfig()
cfg.UserID = "user-42"
mgr := promptcontext.NewManager(memoraClient, cfg)
opt := optimizer.NewOptimizer(optimizer.WithContextManager(mgr))
```

行为契约：

| 情况 | 行为 |
|------|------|
| reader 为 nil / 被禁用 | 降级为「无历史」，**不报错**，优化继续 |
| 后端调用失败 | **返回错误**（区分「查不到」与「查失败」）|
| 相似度低于阈值 | 丢弃该条记忆 |
| 超出 token 预算 | 按分数降序裁剪，超出即停 |

## Harness 要素注入

| 要素 | 内容 |
|------|------|
| 可用工具 | 工具名/描述/参数/约束/用法（优先用上下文给的工具，其次查注册表）|
| 安全规则 | 5 条通用规则 + 领域专属规则（medical/legal/financial/security）|
| 错误处理 | 4 类错误（工具失败/数据校验/权限/超时）+ 统一错误响应格式 |
| 可观测性 | 可选。`[TRACE]` / `[PERF]` / `[DECISION]` 三类结构化标记 |

章节按 `Order` 排序（安全规则 5 → 工具 10 → 错误处理 15 → 可观测性 20）。

## 评估框架

**两种口径，含义不同，必须分清：**

| 口径 | 指标 | 需要 LLM | 衡量什么 |
|------|------|----------|----------|
| 结构质量 | `prompt_quality` | 否 | 提示词**要素齐不齐**（角色/目标/约束/格式/步骤/自检/示例/错误处理）|
| 输出相似度 | `accuracy` / `completeness` / `consistency` | **是** | 输出**像不像**期望答案 |

内置指标：

| 指标 | 权重 | 口径 |
|------|------|------|
| prompt_quality | 1.0 | 8 项要素的完备度加权 |
| accuracy | 1.0 | Levenshtein 归一化相似度 |
| completeness | 0.8 | 输出长度 / 期望长度 的分档 |
| consistency | 0.6 | 非空且有实质内容 |
| token_usage | 0.3 | 输出 token 估算分档（不进默认集）|

`DefaultMetrics()` 只返回 accuracy / completeness / consistency 三项
（`prompt_quality` 需显式加入；`token_usage` 衡量成本而非质量，默认不进）。

`MetricBreakdown` 额外给出逐指标明细；`PromptQualityBreakdown` 给出
「缺了哪一块」。

> ⚠️ **未接 LLM 客户端时输出相似度评估直接报错**，不会静默返回 0 分 ——
> 静默 0 分会被读成「优化后更差」。

## LLM 通道（内部小模型）

`internal/prompt-optimizer/llm` 是 OpenAI Chat Completions 兼容客户端，
**不引入任何新依赖**。把 BaseURL 指向本仓网关即可由网关路由到内部小模型：

```go
client, _ := llm.New(llm.Config{
    BaseURL: "http://gateway:8080/v1",
    APIKey:  os.Getenv("LLM_API_KEY"),
    Model:   "your-small-model",
})
evaluator := evaluation.NewEvaluator(evaluation.DefaultEvaluatorConfig(), evaluation.DefaultMetrics(), client)
```

## 度量的局限与不做的事

诚实地说明这套评估**不能**证明什么：

1. **accuracy 是字符串相似度，不是任务正确率。** 它衡量「输出像不像期望答案」，
   不衡量「答案对不对」。要衡量正确性，需要接真实 LLM 判官或精确匹配指标。
2. **prompt_quality 是结构分，不是效果分。** 它衡量要素完备度，
   结构齐全不代表输出更好。
3. **默认指标集不含 token_usage**，因为它衡量的是成本而非质量；
   `DefaultMetrics()` 只返回 accuracy / completeness / consistency 三项。
4. **内置示例是硬编码的固定样本**，不是从生产流量挖掘的真实分布。
   它的作用是让策略链路可测，不是代表真实效果。
5. **没有端到端的效果实验。** 历史文档里的「+43% / +19%」等数字来自**论文的
   其它任务**（BBH / GSM8K 等），**不是本仓提示词上的实测值**。
   要拿到本仓的真实提升，必须接入真实流量 + 真实度量后重测。
6. **迭代优化在缺少 LLM 客户端或测试用例时完全不做功**（见上）。

## 使用示例

### 作为库

```go
opt := optimizer.NewOptimizer(
    optimizer.WithStrategies(
        strategies.NewStructural(strategies.DefaultStructuralConfig()),
        strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil),
    ),
    optimizer.WithHarnessInjector(
        harness.NewInjector(harness.DefaultInjectorConfig(),
            harness.NewMockToolRegistry(), harness.NewMockSafetyRules()),
    ),
)

result, err := opt.Optimize(ctx, "帮我翻译这段文字", optimizer.OptimizeOptions{
    TaskType:      "translation",
    Domain:        "technical",
    InjectHarness: true,
})
```

### 作为 HTTP 服务

```bash
docker compose -f docker-compose.prompt-optimizer.yml up -d --build
curl -s localhost:8090/api/v1/optimize \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"帮我翻译这段文字","task_type":"translation","inject_harness":true}'
```

端点：`/health`、`/api/v1/optimize`、`/api/v1/optimize/batch`、
`/api/v1/optimize/evaluate`、`/api/v1/compare`。

### 作为中间件

见 `docs/PROMPT_OPTIMIZER_INTEGRATION.md`。

## 测试

```bash
# 单元测试
go test ./internal/prompt-optimizer/...

# 变异验证（14 条臂，全部必须被抓住）
./scripts/audit/prompt-optimizer-mutation-verify.sh

# Docker 端到端（31 项断言）
./scripts/test-prompt-optimizer.sh all
```

验证记录见 `docs/PROMPT_OPTIMIZER_TEST_VALIDATION.md`。

## 参考

- DSPy: *Compiling Declarative Language Model Calls into Self-Improving Pipelines* (ICLR 2024)
- OPRO: *Large Language Models as Optimizers* (Yang et al., 2023)
- AGOPS: *Automatically Evolving Prompt Guidelines for Task-Specific Optimization* (2026)
- PromptBreeder: 遗传算法式提示词变异与选择
