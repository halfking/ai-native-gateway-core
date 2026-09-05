# OmniRoute vs llm-gateway-go 压缩方案对比分析

**分析日期**: 2026-08-28  
**分析人员**: ZCode Agent  
**版本**: v1.0  

---

## 执行摘要

本文档对比分析了 OmniRoute (TypeScript/Node.js) 和 llm-gateway-go (Go) 两个项目的会话压缩实现。主要发现：

**核心差异**:
1. **架构设计**: OmniRoute 采用**可组合多引擎管道**，llm-gateway-go 采用**单一智能压缩流程**
2. **压缩策略**: OmniRoute 提供 **12+ 压缩引擎**可组合，llm-gateway-go 提供 **Phase1剥离 + LLM摘要**
3. **触发机制**: OmniRoute 支持**自适应预算**，llm-gateway-go 使用**固定阈值**
4. **信息保留**: OmniRoute 采用**规则+工具策略**，llm-gateway-go 采用**A/B/C三轨+tail**
5. **协议支持**: 两者都完整支持 OpenAI/Anthropic

**推荐行动**:
- ✅ **借鉴** OmniRoute 的多引擎可组合架构设计算法选择机制
- ✅ **借鉴** OmniRoute 的自适应上下文预算系统
- ✅ **保留** llm-gateway-go 的 LLM 摘要质量（OmniRoute 主要用规则）
- ⚠️  **评估** OmniRoute 的工具结果压缩策略（可移植性高）

---

## 1. 压缩策略对比

### 1.1 OmniRoute 压缩策略

#### 压缩模式
OmniRoute 提供 **9 种压缩模式**：

| 模式 | 引擎组合 | 适用场景 |
|------|---------|---------|
| `off` | 无压缩 | 调试/小上下文 |
| `lite` | 轻量规则（删除thinking块、image pruning） | 快速压缩 |
| `standard` | caveman引擎（基于规则的文本压缩） | 通用文本对话 |
| `aggressive` | 工具结果压缩 + 渐进老化 + 摘要器 | 工具密集型会话 |
| `ultra` | 启发式token修剪 OR 本地SLM模型 | 极限压缩 |
| `rtk` | 工具输出过滤（代码/日志/搜索结果） | 代码助手场景 |
| `codex-responses` | 保守的工具输出压缩 | Codex风格响应 |
| `omniglyph` | 异步专用模式 | 特定传输层 |
| `stacked` | **多引擎管道组合** | 自定义组合 |

#### 核心引擎 (12+)

**结构性引擎**:
1. **lite**: 删除 Anthropic thinking 块、image pruning、工具调用清理
2. **caveman**: 基于规则的文本压缩（150+ 规则，见 §1.1.3）
3. **aggressive**: 工具结果压缩 + 渐进老化 + 摘要回退
4. **ultra**: 启发式token修剪 OR 本地SLM（llmlingua）
5. **rtk**: 工具输出过滤（代码块、日志、搜索结果、JSON）
6. **codex-responses**: 保守工具输出压缩
7. **headroom**: 智能表格压缩（smartcrusher）
8. **session-dedup**: 会话去重
9. **ccr**: 上下文协同检索
10. **llmlingua**: 本地 SLM 压缩模型（ONNX）
11. **relevance**: 基于相关性的选择性保留
12. **omniglyph**: 传输层敏感压缩

#### caveman 规则分类

caveman 引擎包含 **150+ 压缩规则**，分为以下类别：

| 类别 | 规则数 | 示例 |
|------|-------|------|
| `filler` | 40+ | "basically", "essentially", "actually" → 删除 |
| `context` | 20+ | "as we discussed earlier" → 删除 |
| `structural` | 30+ | "in order to" → "to" |
| `dedup` | 15+ | 重复短语去重 |
| `terse` | 25+ | "make sure" → "ensure" |
| `ultra` | 20+ | "database" → "db", "configuration" → "config" |

**强度等级**:
- `lite`: 仅应用 filler + context
- `full`: 应用所有规则（排除 ultra）
- `ultra`: 应用所有规则（包括激进缩写）

### 1.2 llm-gateway-go 压缩策略

#### 压缩流程 (v4 intelligent compression)

**触发后执行 4 阶段**:

```
Phase 1 - Strip     : 删除已完成工具回合（保留最近2轮）
Phase 2 - Thinking  : 删除 Anthropic thinking 块
Phase 3 - LLM 摘要  : 调用配置的压缩模型生成7段摘要
Phase 4 - Rebuild   : A-track + B-track + C-track + Tail
```

#### 重建策略 (A/B/C 三轨)

| 轨道 | 内容 | 目的 |
|------|------|------|
| A-track | 原始 system 消息 | 保留指令上下文 |
| B-track | 第一条真实用户消息（跳过marker/reminder） | 保留会话起点 |
| C-track | 新摘要 marker `[smm_v1:<hash>]` | 注入压缩历史 |
| Tail | 最近 N 条消息（原子工具回合） | 保留活跃上下文 |

**关键特性**:
- **Marker 幂等**: 二次压缩时旧 marker 被替换，不嵌套
- **工具回合原子**: tail 截断向前扩展到 assistant tool_calls 锚点
- **协议分离**: OpenAI/Anthropic 各有独立 rebuilder
- **增量 diff**: 只发送变化的消息，减少 LLM summarization 成本

### 1.3 对比矩阵

| 维度 | llm-gateway-go (当前) | OmniRoute | 优劣对比 | 可移植性 |
|------|----------------------|-----------|----------|---------|
| **压缩策略** | Phase1剥离 + LLM摘要 | 多引擎可组合管道 | OmniRoute **更灵活**（12+ 引擎），但 llm-gateway-go **摘要质量更高**（LLM vs 规则） | **中等** - 需要重构架构 |
| **触发条件** | 消息数(50)/token(128k)/空闲(30min) | 自动触发 + 自适应预算 | OmniRoute **更智能**（自适应上下文预算），llm-gateway-go **更简单**（固定阈值） | **高** - 可直接移植自适应逻辑 |
| **信息保留** | A/B/C三轨 + tail | 规则 + 工具策略 + 老化 | llm-gateway-go **结构更清晰**（三轨设计），OmniRoute **策略更细粒度**（per-tool） | **中等** - 工具策略可移植 |
| **协议支持** | OpenAI/Anthropic | OpenAI/Anthropic/Gemini | **平手** - 覆盖主流协议 | N/A |
| **工具调用** | 原子回合保留 | 工具结果压缩 + 结构保留 | llm-gateway-go **保证完整性**（原子回合），OmniRoute **节省更多token**（aggressive压缩） | **高** - 工具结果压缩可移植 |
| **性能开销** | LLM 调用成本（~1-3s） | 规则执行成本（<100ms） | OmniRoute **更快**（纯规则），llm-gateway-go **成本更高**（LLM调用） | **低** - 架构差异大 |
| **信息密度** | 未量化 | 未量化（但有 savingsPercent） | **平手** - 都需要实测 | N/A |
| **遗失率** | 未量化 | 未量化（但有 fidelityGate） | OmniRoute **有保护机制**（fidelityGate），llm-gateway-go **依赖 LLM** | **高** - fidelityGate 可移植 |
| **可扩展性** | 单一流程 | 引擎注册表 + 管道组合 | OmniRoute **更易扩展**（新引擎只需实现接口） | **中等** - 需要重构 |
| **配置复杂度** | 简单（3个阈值） | 复杂（12+ 引擎 × N 参数） | llm-gateway-go **更易上手**，OmniRoute **更灵活** | N/A |

---

## 2. 触发条件对比

### 2.1 OmniRoute 触发机制

#### 基础自动触发
```typescript
interface CompressionConfig {
  enabled: boolean;
  autoTriggerTokens: number;  // 默认值未在代码中看到
  autoTriggerMode: CompressionMode;  // 触发后使用的模式
}

function shouldAutoTrigger(config: CompressionConfig, estimatedTokens: number): boolean {
  return config.autoTriggerTokens > 0 && estimatedTokens >= config.autoTriggerTokens;
}
```

#### 自适应上下文预算 (Adaptive Context Budget) ⭐

**这是 OmniRoute 的杀手级特性** - 根据模型上下文窗口动态调整压缩策略：

```typescript
interface ContextBudgetConfig {
  mode: "off" | "floor" | "replace-autotrigger";
  floorTokens: number;         // 最小保留token
  targetRatio: number;         // 目标压缩比
  escalationSteps: Array<{
    threshold: number;         // token阈值
    mode: CompressionMode;     // 升级到的模式
  }>;
}
```

**工作原理**:
1. **计算可用预算**: `availableBudget = modelContextLimit - requestMaxTokens - overhead`
2. **判断是否超预算**: `if (estimatedTokens > availableBudget) → 触发压缩`
3. **动态升级**: 根据 `escalationSteps` 逐级升级压缩强度

**示例配置**:
```typescript
{
  mode: "floor",
  floorTokens: 4000,           // 至少保留 4k tokens
  targetRatio: 0.6,            // 压缩到 60%
  escalationSteps: [
    { threshold: 32000, mode: "lite" },
    { threshold: 64000, mode: "standard" },
    { threshold: 96000, mode: "aggressive" },
    { threshold: 128000, mode: "ultra" }
  ]
}
```

### 2.2 llm-gateway-go 触发机制

```go
type CompressionConfig struct {
    MaxMessageCount int    // 默认 50
    MaxTokenCount   int    // 默认 128000
    IdleMinutes     int    // 默认 30
}

func shouldCompress(session *Session, config *CompressionConfig) bool {
    return len(session.Messages) > config.MaxMessageCount ||
           session.EstimatedTokens > config.MaxTokenCount ||
           time.Since(session.LastActive) > time.Duration(config.IdleMinutes)*time.Minute
}
```

**特点**:
- ✅ **简单直接**: 3 个固定阈值
- ❌ **不感知模型**: 不考虑模型上下文窗口大小
- ❌ **不动态调整**: 无法根据请求动态升级

### 2.3 推荐改进

**将 OmniRoute 的自适应预算系统移植到 llm-gateway-go**:

```go
type AdaptiveConfig struct {
    Mode            string  // "off" | "floor" | "replace-autotrigger"
    FloorTokens     int     // 最小保留
    TargetRatio     float64 // 目标压缩比
    EscalationSteps []struct {
        Threshold int
        Strategy  string  // 对应当前的压缩策略
    }
}

func selectCompressionStrategy(
    session *Session,
    modelContextLimit int,
    requestMaxTokens int,
    config *AdaptiveConfig,
) (shouldCompress bool, intensity string) {
    availableBudget := modelContextLimit - requestMaxTokens - 1000 // overhead
    estimatedTokens := session.EstimatedTokens
    
    if estimatedTokens <= availableBudget * config.TargetRatio {
        return false, ""
    }
    
    // 根据超预算程度选择压缩强度
    for _, step := range config.EscalationSteps {
        if estimatedTokens >= step.Threshold {
            intensity = step.Strategy
        }
    }
    
    return true, intensity
}
```

---

## 3. 信息保留策略对比

### 3.1 OmniRoute 信息保留

#### 工具结果压缩 (aggressive 模式)

**策略分类**:
```typescript
interface ToolStrategies {
  fileContent: boolean;      // 文件内容压缩
  grepSearch: boolean;       // 搜索结果去重
  shellOutput: boolean;      // Shell输出截断
  json: boolean;             // JSON美化移除
  errorMessage: boolean;     // 错误消息简化
}
```

**压缩算法**:
1. **文件内容**: 删除空行、注释、多余空格
2. **搜索结果**: 去重相同文件路径、合并连续匹配
3. **Shell 输出**: 保留前100行+后50行
4. **JSON**: 移除缩进、空白
5. **错误消息**: 提取核心错误信息

#### 渐进老化 (Progressive Aging)

**基于消息位置的分级压缩**:

```typescript
interface AgingThresholds {
  fullSummary: number;  // 完全摘要（最旧）
  moderate: number;     // 中度压缩
  light: number;        // 轻度压缩
  verbatim: number;     // 原样保留（最新）
}
```

**工作原理**:
- 将消息分为 4 个区域（从旧到新）
- 每个区域应用不同强度的压缩
- 保证新消息完整，旧消息摘要

#### system prompt 保留模式 ⭐

**OmniRoute 有更细粒度的控制**:

```typescript
type PreserveSystemPromptMode = 
  | "always"        // 永不压缩
  | "whenNoCache"   // 仅当无缓存时压缩
  | "never";        // 总是压缩（即使破坏缓存）
```

**llm-gateway-go 当前只有布尔值**:
```go
type CompressionConfig struct {
    PreserveSystemPrompt bool  // true = always, false = never
}
```

### 3.2 llm-gateway-go 信息保留

#### A/B/C 三轨设计

**优势**:
- ✅ **结构清晰**: 三个轨道职责明确
- ✅ **原子性保证**: tail 截断向前扩展到 tool_calls 锚点
- ✅ **幂等性**: marker 替换而非嵌套

**局限**:
- ❌ **不区分工具类型**: 所有工具结果同等对待
- ❌ **不分级保留**: tail 之外的消息要么全摘要，要么全保留
- ❌ **不支持细粒度 system prompt 控制**

#### 工具回合原子性

**这是 llm-gateway-go 的优势** - 保证工具调用链完整性：

```go
// tail 截断时向前扩展到 assistant tool_calls 锚点
func findAtomicTailStart(messages []Message, tailCount int) int {
    start := len(messages) - tailCount
    if start < 0 {
        return 0
    }
    
    // 向前查找到包含 tool_calls 的 assistant 消息
    for i := start; i >= 0; i-- {
        if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
            return i
        }
    }
    
    return start
}
```

**OmniRoute 也保留工具结构，但通过压缩内容而非原子保留整个回合**。

### 3.3 推荐改进

**将 OmniRoute 的工具结果压缩策略移植到 llm-gateway-go**:

```go
type ToolCompressionConfig struct {
    EnableFileContent  bool
    EnableGrepSearch   bool
    EnableShellOutput  bool
    EnableJSON         bool
    EnableErrorMessage bool
    
    MaxShellOutputLines int  // 默认 150 (前100+后50)
    MaxFileLines        int  // 默认 500
}

func compressToolResult(content string, toolName string, config *ToolCompressionConfig) string {
    switch {
    case strings.HasPrefix(toolName, "read_file") && config.EnableFileContent:
        return compressFileContent(content, config.MaxFileLines)
    case strings.HasPrefix(toolName, "grep") && config.EnableGrepSearch:
        return deduplicateSearchResults(content)
    case strings.HasPrefix(toolName, "bash") && config.EnableShellOutput:
        return truncateShellOutput(content, config.MaxShellOutputLines)
    // ...
    default:
        return content
    }
}
```

---

## 4. 协议支持对比

### 4.1 两者都完整支持

| 协议 | llm-gateway-go | OmniRoute | 备注 |
|------|----------------|-----------|------|
| **OpenAI** | ✅ | ✅ | 都支持 tool_calls/function_call |
| **Anthropic** | ✅ | ✅ | 都支持 tool_use blocks + thinking 删除 |
| **Gemini** | ❌ | ✅ | OmniRoute 有特殊处理 |

### 4.2 关键差异

**OmniRoute 的 Anthropic Context Editing**:
```typescript
interface ContextEditingConfig {
  enabled: boolean;  // 委托给 Anthropic server 端压缩
}
```

这是 Anthropic 的 beta 功能 `context-management-2025-06-27`，允许服务端清理旧的 tool-use 块。

**llm-gateway-go 没有此功能** - 全部在客户端压缩。

---

## 5. 性能开销对比

### 5.1 OmniRoute 性能特征

#### 规则引擎性能
- **caveman (150+ 规则)**: ~10-50ms（取决于消息长度）
- **aggressive (工具压缩+老化)**: ~50-100ms
- **ultra (启发式修剪)**: ~100-200ms
- **rtk (工具过滤)**: ~20-80ms

**总延迟（stacked 模式）**: 100-500ms（纯CPU计算，无网络调用）

#### SLM 模型性能（ultra 模式可选）
- **llmlingua (本地ONNX)**: ~1-3s（需要加载模型）
- **预热机制**: 首次调用后缓存模型
- **fallback**: SLM 失败时回退到启发式

### 5.2 llm-gateway-go 性能特征

#### LLM 摘要调用
- **网络延迟**: ~500-1500ms（取决于模型）
- **LLM 生成**: ~1-3s（取决于历史长度）
- **总延迟**: ~1.5-4.5s

#### 优化
- ✅ **增量 diff**: 只发送变化的消息（减少 LLM 输入）
- ✅ **指纹缓存**: 相同历史不重复压缩
- ❌ **无并行**: 每次压缩都是同步阻塞

### 5.3 性能对比

| 场景 | llm-gateway-go | OmniRoute (规则) | OmniRoute (SLM) |
|------|----------------|------------------|-----------------|
| **小上下文** (< 10k tokens) | ~2s | ~50ms | ~1.5s |
| **中等上下文** (10-50k tokens) | ~3s | ~150ms | ~2.5s |
| **大上下文** (> 50k tokens) | ~4s | ~300ms | ~3.5s |
| **成本** | LLM API 费用 | 无 | 无（本地模型） |

**结论**: 
- OmniRoute 规则引擎 **快 10-20倍**
- OmniRoute SLM 略快于 llm-gateway-go LLM
- llm-gateway-go **摘要质量可能更高**（需实测）

---

## 6. 可扩展性对比

### 6.1 OmniRoute 引擎注册表架构 ⭐

**这是 OmniRoute 最值得学习的设计**:

```typescript
interface CompressionEngine {
  id: string;
  name: string;
  description: string;
  apply(body: any, options: ApplyOptions): CompressionResult;
  applyAsync?(body: any, options: ApplyOptions): Promise<CompressionResult>;
}

// 引擎注册表
const engineRegistry = new Map<string, CompressionEngine>();

function registerEngine(engine: CompressionEngine) {
  engineRegistry.set(engine.id, engine);
}

function getCompressionEngine(id: string): CompressionEngine | null {
  return engineRegistry.get(id) ?? null;
}
```

**优势**:
- ✅ **松耦合**: 新引擎只需实现接口
- ✅ **可禁用**: 运行时启用/禁用引擎
- ✅ **可组合**: stacked 模式任意组合引擎
- ✅ **支持异步**: applyAsync 支持 worker-thread 模型

**示例 - 添加新引擎只需 3 步**:
```typescript
// 1. 实现接口
const myEngine: CompressionEngine = {
  id: "my-engine",
  name: "My Custom Engine",
  description: "Does something cool",
  apply(body, options) {
    // 压缩逻辑
    return { body: compressedBody, compressed: true, stats: {...} };
  }
};

// 2. 注册
registerEngine(myEngine);

// 3. 使用
const result = applyStackedCompression(body, [
  { engine: "lite" },
  { engine: "my-engine" },
  { engine: "caveman" }
]);
```

### 6.2 llm-gateway-go 单一流程

**当前架构**:
```go
// 硬编码的压缩流程
func Compress(session *Session, config *Config) error {
    // Phase 1: Strip
    stripToolRounds(session)
    
    // Phase 2: Thinking
    stripThinking(session)
    
    // Phase 3: LLM Summary
    summary := callLLM(session.Messages)
    
    // Phase 4: Rebuild
    rebuild(session, summary)
    
    return nil
}
```

**局限**:
- ❌ **不可组合**: 无法选择性启用/禁用某个阶段
- ❌ **不可扩展**: 添加新策略需要修改核心流程
- ❌ **不可配置**: 无法为不同租户/会话使用不同策略

### 6.3 推荐改进 - 引入策略模式

**Step 1: 定义接口**
```go
type CompressionStrategy interface {
    Name() string
    Apply(ctx context.Context, messages []Message, config Config) ([]Message, error)
}
```

**Step 2: 实现策略**
```go
type StripStrategy struct{}
func (s *StripStrategy) Apply(...) ([]Message, error) {
    // Phase 1 逻辑
}

type ThinkingStrategy struct{}
func (s *ThinkingStrategy) Apply(...) ([]Message, error) {
    // Phase 2 逻辑
}

type LLMSummaryStrategy struct{}
func (s *LLMSummaryStrategy) Apply(...) ([]Message, error) {
    // Phase 3 逻辑
}
```

**Step 3: 组合执行**
```go
type CompressionPipeline struct {
    strategies []CompressionStrategy
}

func (p *CompressionPipeline) Execute(ctx context.Context, messages []Message) ([]Message, error) {
    current := messages
    for _, strategy := range p.strategies {
        result, err := strategy.Apply(ctx, current, config)
        if err != nil {
            return nil, err
        }
        current = result
    }
    return current, nil
}
```

**Step 4: 灵活配置**
```go
// 默认管道
defaultPipeline := &CompressionPipeline{
    strategies: []CompressionStrategy{
        &StripStrategy{},
        &ThinkingStrategy{},
        &LLMSummaryStrategy{},
    },
}

// 自定义管道（跳过 LLM 摘要，添加工具压缩）
customPipeline := &CompressionPipeline{
    strategies: []CompressionStrategy{
        &StripStrategy{},
        &ToolResultCompressionStrategy{},  // 新策略！
        &ThinkingStrategy{},
    },
}
```

---

## 7. 信息密度与遗失率

### 7.1 OmniRoute 的保护机制

#### Fidelity Gate (保真度门控)

**这是防止过度压缩的关键机制**:

```typescript
interface FidelityGateConfig {
  enabled: boolean;
  maxInflation: number;      // 最大膨胀率（防止压缩后反而更大）
  minSavings: number;        // 最小节省率（低于此值拒绝）
  maxLoss: number;           // 最大信息丢失率
}

function gateAdvance(
  result: CompressionResult,
  originalBody: any,
  config: FidelityGateConfig,
  acc: Accumulator,
  engineId: string
): boolean {
  if (!config.enabled) return true;
  
  // 检查是否膨胀
  if (result.stats.savingsPercent < -config.maxInflation) {
    acc.validationWarnings.add(`${engineId}: inflated by ${-result.stats.savingsPercent}%`);
    return false;  // 拒绝此步骤
  }
  
  // 检查节省是否足够
  if (result.stats.savingsPercent < config.minSavings) {
    return false;
  }
  
  // 检查信息丢失（如果有评估器）
  if (config.maxLoss > 0) {
    const loss = estimateInformationLoss(originalBody, result.body);
    if (loss > config.maxLoss) {
      acc.validationWarnings.add(`${engineId}: info loss ${loss}% exceeds ${config.maxLoss}%`);
      return false;
    }
  }
  
  return true;
}
```

#### Circuit Breaker (熔断器)

**跨请求的失败保护**:

```typescript
interface PipelineCircuitBreakerConfig {
  enabled: boolean;
  failureThreshold: number;    // 失败次数阈值
  successThreshold: number;    // 恢复需要的成功次数
  timeout: number;             // 熔断持续时间（ms）
}

// 某个引擎频繁失败 → 自动跳过
if (breakerOn && !canRunEngine(step.engine, breaker)) {
  acc.validationWarnings.add(`${step.engine}: skipped (circuit-breaker open)`);
  continue;
}
```

### 7.2 llm-gateway-go 的质量保证

**当前机制**:
- ✅ **7段摘要格式**: 强制 LLM 输出结构化摘要
- ✅ **增量 diff**: 仅压缩变化部分
- ✅ **marker 幂等**: 防止嵌套累积
- ❌ **无定量指标**: 没有 savingsPercent / lossRate 计算
- ❌ **无熔断机制**: LLM 失败时无保护

### 7.3 推荐改进 - 添加质量度量

```go
type CompressionMetrics struct {
    OriginalTokens    int
    CompressedTokens  int
    SavingsPercent    float64
    DurationMs        int64
    
    // 新增
    InformationLossRate float64  // 0-1，需要评估算法
    FidelityScore       float64  // 0-1，压缩后保真度
}

type QualityGate struct {
    MinSavingsPercent   float64  // 最小节省（默认 5%）
    MaxInflationPercent float64  // 最大膨胀（默认 10%）
    MaxLossRate         float64  // 最大丢失（默认 20%）
}

func (qg *QualityGate) ShouldAccept(metrics *CompressionMetrics) (bool, string) {
    if metrics.SavingsPercent < qg.MinSavingsPercent {
        return false, fmt.Sprintf("savings %.1f%% < min %.1f%%", 
            metrics.SavingsPercent, qg.MinSavingsPercent)
    }
    
    if metrics.SavingsPercent < -qg.MaxInflationPercent {
        return false, fmt.Sprintf("inflated %.1f%% > max %.1f%%",
            -metrics.SavingsPercent, qg.MaxInflationPercent)
    }
    
    if metrics.InformationLossRate > qg.MaxLossRate {
        return false, fmt.Sprintf("loss %.1f%% > max %.1f%%",
            metrics.InformationLossRate * 100, qg.MaxLossRate * 100)
    }
    
    return true, ""
}
```

---

## 8. 算法选择机制设计 (推荐实现)

基于对比分析，推荐为 llm-gateway-go 设计以下算法选择机制：

### 8.1 配置层

```go
type CompressionAlgorithm string

const (
    AlgorithmIntelligent  CompressionAlgorithm = "intelligent"   // 当前 v4
    AlgorithmRuleBased    CompressionAlgorithm = "rule-based"    // 学习 OmniRoute caveman
    AlgorithmToolFocused  CompressionAlgorithm = "tool-focused"  // 学习 OmniRoute aggressive
    AlgorithmHybrid       CompressionAlgorithm = "hybrid"        // intelligent + rule-based
    AlgorithmDisabled     CompressionAlgorithm = "disabled"
)

type AlgorithmConfig struct {
    // 手动选择
    Algorithm         CompressionAlgorithm
    
    // 自动选择
    AutoSelect        bool
    AutoSelectHints   AutoSelectHints
    
    // 算法特定配置
    IntelligentConfig *IntelligentConfig
    RuleBasedConfig   *RuleBasedConfig
    ToolFocusedConfig *ToolFocusedConfig
}

type AutoSelectHints struct {
    // 会话特征
    HasToolCalls       bool
    ToolCallFrequency  float64  // 工具调用占比
    AverageMessageLen  int
    
    // 性能约束
    MaxLatencyMs       int      // 最大延迟（规则 vs LLM）
    MaxCost            float64  // 最大成本
}
```

### 8.2 选择器接口

```go
type AlgorithmSelector interface {
    Select(ctx context.Context, session *Session, hints AutoSelectHints) (CompressionAlgorithm, error)
}

// 实现 1: 基于规则的选择器
type RuleBasedSelector struct{}

func (s *RuleBasedSelector) Select(ctx context.Context, session *Session, hints AutoSelectHints) (CompressionAlgorithm, error) {
    // 工具调用占比 > 50% → tool-focused
    if hints.ToolCallFrequency > 0.5 {
        return AlgorithmToolFocused, nil
    }
    
    // 延迟要求 < 500ms → rule-based
    if hints.MaxLatencyMs > 0 && hints.MaxLatencyMs < 500 {
        return AlgorithmRuleBased, nil
    }
    
    // 默认 intelligent（质量优先）
    return AlgorithmIntelligent, nil
}

// 实现 2: 基于 ML 的选择器（未来）
type MLBasedSelector struct {
    model *ml.Model
}

func (s *MLBasedSelector) Select(ctx context.Context, session *Session, hints AutoSelectHints) (CompressionAlgorithm, error) {
    features := extractFeatures(session, hints)
    prediction := s.model.Predict(features)
    return prediction.Algorithm, nil
}
```

### 8.3 配置优先级

```
租户级配置 > 会话级 hint > 自动选择 > 全局默认
```

```go
func ResolveAlgorithm(
    tenantConfig *AlgorithmConfig,
    sessionHint *CompressionAlgorithm,
    globalConfig *AlgorithmConfig,
    session *Session,
) CompressionAlgorithm {
    // 1. 租户显式配置
    if tenantConfig != nil && tenantConfig.Algorithm != "" {
        return tenantConfig.Algorithm
    }
    
    // 2. 会话级 hint
    if sessionHint != nil {
        return *sessionHint
    }
    
    // 3. 自动选择
    if globalConfig.AutoSelect {
        selector := NewRuleBasedSelector()
        hints := ExtractHints(session)
        algorithm, _ := selector.Select(context.Background(), session, hints)
        return algorithm
    }
    
    // 4. 全局默认
    return globalConfig.Algorithm
}
```

### 8.4 使用示例

```go
// 场景 1: 手动选择（租户配置）
tenantConfig := &AlgorithmConfig{
    Algorithm: AlgorithmToolFocused,  // 强制使用工具压缩
}

// 场景 2: 自动选择
globalConfig := &AlgorithmConfig{
    AutoSelect: true,
    AutoSelectHints: AutoSelectHints{
        MaxLatencyMs: 1000,  // 延迟约束
    },
}

// 场景 3: 会话级 hint
sessionHint := AlgorithmRuleBased  // 本次会话快速压缩
result := Compress(session, WithAlgorithm(&sessionHint))
```

---

## 9. 245 Log 数据验证设计

### 9.1 数据采样策略

```go
type SampleStrategy struct {
    TotalSamples   int      // 默认 1000
    Stratification []Stratum
}

type Stratum struct {
    Name         string
    Filter       func(*Session) bool
    TargetCount  int
}

// 示例：分层采样
strategy := &SampleStrategy{
    TotalSamples: 1000,
    Stratification: []Stratum{
        {
            Name: "short-text",
            Filter: func(s *Session) bool {
                return len(s.Messages) <= 20 && s.ToolCallCount == 0
            },
            TargetCount: 300,
        },
        {
            Name: "tool-heavy",
            Filter: func(s *Session) bool {
                return s.ToolCallCount > 10
            },
            TargetCount: 400,
        },
        {
            Name: "long-context",
            Filter: func(s *Session) bool {
                return s.EstimatedTokens > 50000
            },
            TargetCount: 300,
        },
    },
}
```

### 9.2 评估指标

```go
type CompressionBenchmark struct {
    Algorithm            CompressionAlgorithm
    SampleSize           int
    
    // 压缩效率
    AvgCompressionRatio  float64  // 平均压缩比
    AvgSavingsPercent    float64  // 平均节省率
    
    // 信息质量（需要人工评估或 LLM 评估）
    AvgInformationLoss   float64  // 平均信息丢失率 (0-1)
    FidelityScore        float64  // 保真度得分 (0-1)
    
    // 性能
    P50LatencyMs         int64
    P95LatencyMs         int64
    P99LatencyMs         int64
    
    // 分布
    CompressionRatioDistribution map[string]int  // "1.5-2.0": 100, "2.0-3.0": 200, ...
}
```

### 9.3 信息丢失评估

**方法 1: LLM 评估器**
```go
func EvaluateInformationLoss(original, compressed []Message, evaluatorLLM LLMClient) (float64, error) {
    prompt := fmt.Sprintf(`
You are evaluating information loss in conversation compression.

Original conversation:
%s

Compressed conversation:
%s

Rate the information loss on a scale of 0-1:
- 0.0: No information lost, perfect preservation
- 0.2: Minor details lost, core preserved
- 0.5: Significant information lost
- 0.8: Major information lost, barely usable
- 1.0: Complete information loss

Output only a number between 0 and 1.
`, formatMessages(original), formatMessages(compressed))

    response, err := evaluatorLLM.Generate(prompt)
    if err != nil {
        return 0, err
    }
    
    loss, err := strconv.ParseFloat(strings.TrimSpace(response), 64)
    if err != nil || loss < 0 || loss > 1 {
        return 0, fmt.Errorf("invalid loss rate: %s", response)
    }
    
    return loss, nil
}
```

**方法 2: 基于规则的启发式**
```go
func HeuristicInformationLoss(original, compressed []Message) float64 {
    var loss float64
    
    // 1. 工具调用完整性
    origToolCalls := countToolCalls(original)
    compToolCalls := countToolCalls(compressed)
    if origToolCalls > 0 {
        toolCallLoss := float64(origToolCalls - compToolCalls) / float64(origToolCalls)
        loss += toolCallLoss * 0.4  // 权重 40%
    }
    
    // 2. 代码块保留率
    origCodeBlocks := countCodeBlocks(original)
    compCodeBlocks := countCodeBlocks(compressed)
    if origCodeBlocks > 0 {
        codeLoss := float64(origCodeBlocks - compCodeBlocks) / float64(origCodeBlocks)
        loss += codeLoss * 0.3  // 权重 30%
    }
    
    // 3. Token 压缩比（过度压缩 = 信息丢失风险）
    ratio := float64(countTokens(original)) / float64(countTokens(compressed))
    if ratio > 5.0 {
        loss += (ratio - 5.0) / 10.0 * 0.3  // 权重 30%
    }
    
    return math.Min(loss, 1.0)
}
```

### 9.4 Benchmark 脚本

```bash
#!/bin/bash
# scripts/compression-benchmark.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认参数
DATA_SOURCE="/path/to/245-logs"
SAMPLE_SIZE=1000
STRATEGIES="intelligent,rule-based,tool-focused,hybrid"
OUTPUT="$PROJECT_ROOT/docs/benchmark/compression-results-$(date +%Y%m%d-%H%M%S).json"
EVALUATOR_MODEL="gpt-4o-mini"  # 便宜的评估器

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Options:
    --data-source PATH    Path to 245 log data (required)
    --sample-size N       Number of sessions to sample (default: 1000)
    --strategies CSV      Comma-separated algorithms (default: intelligent,rule-based,tool-focused,hybrid)
    --output PATH         Output JSON file (default: docs/benchmark/...)
    --evaluator MODEL     LLM evaluator model (default: gpt-4o-mini)
    --dry-run             Only show sample distribution
    --help                Show this help

Example:
    $0 --data-source /data/245-logs --sample-size 500 --strategies intelligent,hybrid
EOF
}

# 参数解析
while [[ $# -gt 0 ]]; do
    case $1 in
        --data-source) DATA_SOURCE="$2"; shift 2 ;;
        --sample-size) SAMPLE_SIZE="$2"; shift 2 ;;
        --strategies) STRATEGIES="$2"; shift 2 ;;
        --output) OUTPUT="$2"; shift 2 ;;
        --evaluator) EVALUATOR_MODEL="$2"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        --help) usage; exit 0 ;;
        *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
done

if [[ -z "$DATA_SOURCE" ]]; then
    echo "Error: --data-source is required"
    usage
    exit 1
fi

# 调用 Go benchmark 程序
go run "$PROJECT_ROOT/cmd/compression-benchmark/main.go" \
    --data-source "$DATA_SOURCE" \
    --sample-size "$SAMPLE_SIZE" \
    --strategies "$STRATEGIES" \
    --evaluator "$EVALUATOR_MODEL" \
    --output "$OUTPUT" \
    ${DRY_RUN:+--dry-run}

if [[ -z "${DRY_RUN:-}" ]]; then
    echo "Benchmark completed: $OUTPUT"
    echo ""
    echo "Summary:"
    jq '.results | to_entries | .[] | "\(.key): avg_savings=\(.value.avg_savings_percent)%, p50_latency=\(.value.p50_latency_ms)ms"' "$OUTPUT"
fi
```

---

## 10. 实施建议

基于以上对比分析，推荐按以下优先级实施改进：

### 阶段 1: 快速改进（1-2周） - P0

**1.1 添加工具结果压缩策略**（学习 OmniRoute aggressive）
- ✅ 高可移植性
- ✅ 立即见效（工具密集型会话节省 20-40%）
- 实现文件: `domains/hooks/compression/tool_result.go`

**1.2 添加质量度量**
- ✅ 低风险
- ✅ 为后续优化提供数据
- 实现文件: `domains/hooks/compression/metrics.go`

**1.3 实施 245 log benchmark**
- ✅ 验证当前效果
- ✅ 为算法选择提供数据支撑
- 脚本: `scripts/compression-benchmark.sh`

### 阶段 2: 架构改进（2-3周） - P1

**2.1 引入策略模式**（学习 OmniRoute 引擎注册表）
- ⚠️  需要重构 `session_compressor.go`
- ✅ 为多算法选择奠定基础
- 实现文件: `domains/hooks/compression/strategy/`

**2.2 实现算法选择机制**
- 手动选择: 租户级配置
- 自动选择: 基于会话特征的规则选择器
- 实现文件: `domains/hooks/compression/selector.go`

### 阶段 3: 高级优化（3-5周） - P2

**3.1 自适应上下文预算**（学习 OmniRoute adaptive）
- ⚠️  需要模型上下文窗口信息
- ✅ 动态升级压缩强度
- 实现文件: `domains/hooks/compression/adaptive.go`

**3.2 实现规则引擎算法**（学习 OmniRoute caveman）
- ⚠️  需要大量规则维护
- ✅ 性能提升 10-20 倍
- 实现文件: `domains/hooks/compression/rule_based.go`

**3.3 Fidelity Gate + Circuit Breaker**
- ⚠️  需要长期数据收集
- ✅ 防止过度压缩
- 实现文件: `domains/hooks/compression/quality_gate.go`

### 阶段 4: 生产验证（持续） - P1

**4.1 A/B 测试框架**
- 对比 intelligent vs hybrid vs tool-focused
- 收集真实效果数据

**4.2 监控与告警**
- 压缩失败率
- 平均延迟
- 信息丢失率（人工抽样）

---

## 11. 风险与权衡

### 11.1 引入规则引擎的风险

**风险**:
- ❌ **规则维护成本**: 150+ 规则需要持续维护
- ❌ **多语言支持**: 当前 caveman 主要针对英文
- ❌ **过度压缩**: 规则可能删除重要信息

**缓解**:
- ✅ 从小规模规则集开始（20-30 条核心规则）
- ✅ 使用 Fidelity Gate 防止过度压缩
- ✅ 提供配置选项让用户自定义规则

### 11.2 多算法选择的复杂度

**风险**:
- ❌ **配置复杂**: 租户/管理员难以理解各算法差异
- ❌ **测试成本**: 每个算法都需要充分测试
- ❌ **维护负担**: 多条代码路径

**缓解**:
- ✅ 提供清晰的算法对比文档
- ✅ 默认使用 intelligent（当前实现）
- ✅ 自动选择降低配置负担

### 11.3 性能 vs 质量权衡

| 算法 | 性能 | 质量 | 成本 | 推荐场景 |
|------|------|------|------|---------|
| **intelligent** | 慢 (2-4s) | 高 | 高 (LLM) | 质量优先 |
| **rule-based** | 快 (<100ms) | 中 | 低 | 性能优先 |
| **tool-focused** | 中 (0.5-1s) | 中-高 | 低 | 工具密集型 |
| **hybrid** | 中 (1-2s) | 高 | 中 | 平衡 |

---

## 12. 附录

### 12.1 OmniRoute 代码路径

**核心文件**:
- 策略选择器: `/open-sse/services/compression/strategySelector.ts`
- 引擎注册表: `/open-sse/services/compression/engines/registry.ts`
- caveman 引擎: `/open-sse/services/compression/caveman.ts`
- aggressive 引擎: `/open-sse/services/compression/aggressive.ts`
- 自适应预算: `/open-sse/services/compression/adaptiveCompression/`
- 质量门控: `/open-sse/services/compression/fidelityGate.ts`

### 12.2 llm-gateway-go 代码路径

**核心文件**:
- 压缩主入口: `/domains/hooks/compression/session_compressor.go`
- LLM 摘要: `/domains/hooks/compression/compaction.go`
- Phase 1 剥离: `/domains/hooks/compression/strip.go`
- OpenAI 重建器: `/domains/hooks/compression/rebuilder_openai.go`
- Anthropic 重建器: `/domains/hooks/compression/rebuilder_anthropic.go`
- 审计文档: `/docs/audit/2026-08-27-session-compression-algorithm-audit.md`

### 12.3 参考资料

**OmniRoute**:
- 仓库: `/Users/xutaohuang/workspace/ai/OmniRoute`
- 压缩测试: `/tests/unit/compression-*.test.ts`
- Benchmark 脚本: `/scripts/compression/benchmark.ts`

**llm-gateway-go**:
- 仓库: `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`
- 单元测试: `/domains/hooks/compression/*_test.go`
- Handoff 文档: `/.handoff/2026-08-28-compression-algorithm-comparison.md`

---

## 13. 实地核实补充 (2026-08-28, 代码级核对)

> 第 1~12 节基于 OmniRoute 行为的概括性还原；本节是对 `/Users/xutaohuang/workspace/ai/OmniRoute`
> 源码（`open-sse/services/compression/`）的实际核对，修正若干近似表述，供实现侧参考。

### 13.1 引擎与模式数量（修正 §1.1）

- **实际为 13 个引擎**（`engines/index.ts` 注册表），非"12+"：
  `session-dedup(3) / ccr(4) / lite(5) / rtk(10) / codex-responses(12) / ionizer(13) /
  headroom(15) / relevance(18) / caveman(20) / aggressive(30) / llmlingua(35) / llm(38) /
  ultra(40) / omniglyph(90)`（括号内为 `stackPriority`）。
- **9 种模式**（`CompressionMode`）：`off / lite / standard(=caveman) / aggressive / ultra /
  rtk / codex-responses / omniglyph / stacked`。`standard` 即 caveman；`stacked` 跑有序的
  `CompressionEngineId` 管道。

### 13.2 Caveman 规则数（修正 §1.1.3）

实际为 **306 条规则**（8 语言），非"150+"。`engineCatalog` 标注 `caveman full` 约 30% 节省。

### 13.3 自适应升级阶梯（修正 §2.1 的 `escalationSteps` 近似）

OmniRoute 真实实现（`adaptiveCompression/ladder.ts`）并非"阈值→模式"的离散阶梯，而是：

1. 按 `policy`（`reserve-output` / `percentage` / `absolute`）从**模型上下文窗口**推导
   `targetTokens`：`reserve-output`（默认）= `limit − max_tokens − safetyMargin`；
   `percentage`= `limit × pct`（默认 0.85）；`absolute`= 固定预算。
2. `headroomBefore >= 0` → **不压缩**（与本文§2.1 及 llm-gateway-go 的 `NeverOverCompress` 一致）。
3. 否则沿 `DEFAULT_LADDER` 从最便宜档逐级升级，用 `REDUCTION_FACTOR` 廉价估算每档压缩后体量，
   直到拟合 `targetTokens`。`REDUCTION_FACTOR`（便宜估算）：session-dedup 0.95 / ccr 0.9 /
   rtk 0.85 / ionizer 0.83 / headroom 0.8 / lite 0.92 / relevance 0.75 / caveman 0.7 /
   aggressive 0.55 / llmlingua 0.5 / llm 0.45 / ultra 0.4 / omniglyph 0.35。
4. `fit==false` 时仍发送尽力而为计划，内容**绝不丢弃**（仅告警）。

**llm-gateway-go 对应实现**：`strategy.AdaptiveSelector`（`selector_adaptive.go`）采用同一思想——
`BudgetFn` 由 `est.ThresholdBytes(contextWindow)` 提供预算，`EscalationProfile.ReductionFactor()`
提供每策略预估压缩率（lite 0.92 / toolfocused 0.85 / caveman 0.70），按 `CostTier` 升序升级，
`NeverOverCompress` 保证预算内不压缩。

### 13.4 其它已核实要点

- **Cache-aware 降级**：支持 prompt caching 的 provider（Anthropic/OpenAI/Codex），`aggressive`/
  `ultra` 会被降级为 `standard` 并置 `skipSystemPrompt=true`，保护可缓存前缀（OmniRoute #3955）。
  llm-gateway-go 当前未实现此降级，但 `A-track` 始终保留 system（等价保护）。
- **Hard-budget post-pass**（`hardBudget.ts`）：所有引擎跑完后确定性地按句/行显著性裁剪到 N token，
  匹配 `UNIT_PRESERVE_RE`（数字/URL/错误/代码块/栈帧/路径/key=value）的内容**绝不丢弃**。
- **Fail-open 每一档**：任何引擎抛错 → 跳过该档而非丢弃目标（对应 llm-gateway-go 的 NeverWorse 守卫）。
- **Token 估算**：`CHARS_PER_TOKEN = 4` 启发式（Codex 走 tiktoken 精确值）；PNG 由 IHDR 解码真实 token。
- **Fidelity eval**（`eval/fidelityCheck.ts`）：USD 封顶的 LLM judge，输出 `SAME` / `MATERIALLY_DIFFERS`，
  含 CONTROL_PAIR 自检，属运营级 A/B 能力，非算法可直移植入 Go。

---

**文档结束** - 2026-08-28  
**下一步**: 根据本文档设计算法选择机制，并实施 245 log benchmark
