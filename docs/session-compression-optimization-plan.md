# 会话压缩与三级缓存优化方案

**制定日期**: 2026-08-02  
**版本**: v1.0-DRAFT  
**状态**: 待审核

---

## 📋 执行摘要

本方案旨在优化当前的会话压缩与缓存架构，学习 OmniRoute 的优势，重新定义三级缓存语义，并增强会话总结模块的多维度能力。

### 核心目标
1. **重新定义三级缓存语义** - 从存储层次转变为处理阶段
2. **学习 OmniRoute 压缩优势** - 引入 Lite/Deep/RTK 压缩策略
3. **增强会话总结模块** - 支持即时/事后总结，多维度分析
4. **完整测试验证** - 确保优化后的稳定性与性能

---

## 🎯 问题定义

### 当前架构的局限

#### 1. 三层缓存语义不清晰
**现状**: 当前的 L1/L2/L3 缓存被定义为存储层次
- L1: 进程内存 LRU
- L2: Redis
- L3: PostgreSQL

**问题**:
- ❌ 缺少"处理阶段"的语义划分
- ❌ 无法追踪会话在不同处理阶段的状态
- ❌ 压缩前后的会话状态混淆
- ❌ 安全处理后的会话无独立缓存

#### 2. 缺少 OmniRoute 的压缩策略
**OmniRoute 的优势**:
- ✅ Lite 压缩: 空格规范化、系统消息去重、工具长度压缩
- ✅ Caveman 正则规则: 保护代码块、URL、路径
- ✅ RTK 工具输出过滤: 精确保留关键信息
- ✅ 策略选择: 根据场景动态选择压缩强度
- ✅ 分层引擎: lite → standard → aggressive → ultra

**当前状态**:
- ✅ 有 LLM 摘要 (compaction)
- ✅ 有机械裁剪 (strip)
- ✅ 有差分压缩 (diff)
- ❌ 缺少 Lite 预处理
- ❌ 缺少 Caveman 保护规则
- ❌ 缺少 RTK 过滤
- ❌ 缺少策略选择器

#### 3. 会话总结功能不完整
**现状**:
- ✅ 有 LLM 摘要功能 (compaction)
- ❌ 缺少即时总结 vs 事后总结模式
- ❌ 缺少多维度总结 (项目/关键词/任务)
- ❌ 缺少总结模型配置
- ❌ 缺少总结质量评估

---

## 🏗️ 优化方案设计

### 方案 1: 重新定义三级缓存架构

#### 新的三级缓存语义

```
┌─────────────────────────────────────────────────────────────┐
│  会话处理流水线                                                │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  [客户端请求]                                                  │
│       ↓                                                       │
│  ┌──────────────────────────────────────────────┐           │
│  │ 1️⃣ 原始会话缓存 (Raw Session Cache)            │           │
│  │  - 存储客户端原始消息序列                        │           │
│  │  - 记录轮次编号 (turn_id)                      │           │
│  │  - 双向存储: 发送(request) + 接收(response)      │           │
│  │  - 用途: 审计、回溯、差分计算                    │           │
│  └──────────────────────────────────────────────┘           │
│       ↓                                                       │
│  ┌──────────────────────────────────────────────┐           │
│  │ 2️⃣ 压缩会话缓存 (Compressed Session Cache)     │           │
│  │  - 存储压缩后的消息序列                          │           │
│  │  - 记录压缩策略 (lite/standard/aggressive)      │           │
│  │  - 记录压缩区间: [start_turn, end_turn]        │           │
│  │  - 记录压缩损失度 (lossiness: none/tail/whole) │           │
│  │  - 用途: 增量压缩、压缩效果分析                  │           │
│  └──────────────────────────────────────────────┘           │
│       ↓                                                       │
│  ┌──────────────────────────────────────────────┐           │
│  │ 3️⃣ 安全处理会话缓存 (Safe Session Cache)        │           │
│  │  - 存储发送给 LLM 的最终消息序列                 │           │
│  │  - 已完成敏感词过滤                             │           │
│  │  - 已完成 PII 脱敏                              │           │
│  │  - 已完成 Token 预算控制                        │           │
│  │  - 用途: LLM 直接消费、请求重放                 │           │
│  └──────────────────────────────────────────────┘           │
│       ↓                                                       │
│  [上游 LLM]                                                  │
│       ↓                                                       │
│  [响应处理] → 回写三级缓存 (response 方向)                    │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

#### 数据结构设计

##### 1️⃣ 原始会话缓存 (RawSessionCache)

```go
type RawSessionCache struct {
    SessionID    string                 `json:"session_id"`
    Turns        []RawTurn              `json:"turns"`
    TotalTurns   int                    `json:"total_turns"`
    CreatedAt    time.Time              `json:"created_at"`
    UpdatedAt    time.Time              `json:"updated_at"`
}

type RawTurn struct {
    TurnID       int                    `json:"turn_id"`
    Request      RawMessage             `json:"request"`   // 客户端发送
    Response     RawMessage             `json:"response"`  // LLM 响应
    Timestamp    time.Time              `json:"timestamp"`
}

type RawMessage struct {
    Role         string                 `json:"role"`
    Content      string                 `json:"content"`
    ToolCalls    []ToolCall             `json:"tool_calls,omitempty"`
    TokenCount   int                    `json:"token_count"`
    ByteSize     int                    `json:"byte_size"`
}
```

**存储策略**:
- L1 (内存): 最近 10 轮
- L2 (Redis): 最近 100 轮，TTL 3 天
- L3 (PostgreSQL): 全量，分区存储

##### 2️⃣ 压缩会话缓存 (CompressedSessionCache)

```go
type CompressedSessionCache struct {
    SessionID          string                 `json:"session_id"`
    CompressionHistory []CompressionBlock     `json:"compression_history"`
    CurrentBlock       *CompressionBlock      `json:"current_block"`
    TotalTurns         int                    `json:"total_turns"`
}

type CompressionBlock struct {
    BlockID            string                 `json:"block_id"`
    Strategy           string                 `json:"strategy"`          // lite/standard/aggressive/ultra
    StartTurn          int                    `json:"start_turn"`
    EndTurn            int                    `json:"end_turn"`
    OriginalTokens     int                    `json:"original_tokens"`
    CompressedTokens   int                    `json:"compressed_tokens"`
    CompressionRatio   float64                `json:"compression_ratio"`
    Lossiness          string                 `json:"lossiness"`         // none/tail/whole
    CompressedMessages []CompressedMessage    `json:"compressed_messages"`
    Metadata           CompressionMetadata    `json:"metadata"`
    CreatedAt          time.Time              `json:"created_at"`
}

type CompressedMessage struct {
    Role         string                 `json:"role"`
    Content      string                 `json:"content"`
    Hash         string                 `json:"hash"`           // 用于增量差分
    TokenCount   int                    `json:"token_count"`
}

type CompressionMetadata struct {
    WindowTriggered    string             `json:"window_triggered,omitempty"`
    SummaryMarker      string             `json:"summary_marker,omitempty"`
    Degraded           bool               `json:"degraded"`
    AppliedRules       []string           `json:"applied_rules"`
}
```

**存储策略**:
- L1 (内存): 当前压缩块
- L2 (Redis): 最近 10 个压缩块，TTL 3 天
- L3 (PostgreSQL): 全量压缩历史

##### 3️⃣ 安全处理会话缓存 (SafeSessionCache)

```go
type SafeSessionCache struct {
    SessionID          string                 `json:"session_id"`
    SafeMessages       []SafeMessage          `json:"safe_messages"`
    SecurityPolicies   []string               `json:"security_policies"`
    TotalTokens        int                    `json:"total_tokens"`
    TokenBudget        int                    `json:"token_budget"`
    LastSentAt         time.Time              `json:"last_sent_at"`
}

type SafeMessage struct {
    Role               string                 `json:"role"`
    Content            string                 `json:"content"`
    OriginalHash       string                 `json:"original_hash"`    // 指向压缩缓存
    RedactionApplied   bool                   `json:"redaction_applied"`
    FilteredPatterns   []string               `json:"filtered_patterns,omitempty"`
    TokenCount         int                    `json:"token_count"`
}
```

**存储策略**:
- L1 (内存): 最近发送的完整消息序列
- L2 (Redis): 最近 3 次发送，TTL 1 小时
- L3 (PostgreSQL): 仅记录 hash 引用，不重复存储

#### 三级缓存的关联关系

```
原始会话缓存[turn_1, turn_2, ..., turn_n]
     ↓ 压缩
压缩会话缓存[block_1: turns 1-5, block_2: turns 6-10]
     ↓ 安全处理
安全处理会话缓存[safe_messages: 基于 block_2 + 新增 turns 11-12]
```

**轮次一致性**:
- 所有缓存层共享同一 `turn_id` 序列
- 压缩块记录 `[start_turn, end_turn]` 区间
- 安全缓存记录 `original_hash` 指向压缩块

---

### 方案 2: 学习 OmniRoute 压缩策略

#### OmniRoute 压缩引擎分析

**从 OmniRoute 源码学习到的核心策略**:

##### 1. 压缩模式层次

```typescript
// OmniRoute 支持的压缩模式
type CompressionMode = 
  | "off"              // 关闭
  | "lite"             // 轻量预处理
  | "standard"         // 标准压缩
  | "aggressive"       // 激进压缩
  | "ultra"            // 极限压缩
  | "rtk"              // RTK 工具过滤
  | "codex-responses"  // Codex 响应优化
  | "stacked"          // 堆叠多策略
  | "omniglyph";       // 专有算法
```

##### 2. Lite 压缩流水线 (5 步)

```go
// 参考 OmniRoute: open-sse/services/compression/lite.ts
type LiteCompressionPipeline struct {
    Steps []LiteCompressionStep
}

type LiteCompressionStep interface {
    Name() string
    Apply(messages []Message) ([]Message, error)
}

// Step 1: 空格规范化
type WhitespaceNormalizer struct{}
func (w *WhitespaceNormalizer) Apply(messages []Message) ([]Message, error) {
    // 移除多余空格、换行
    // 保留代码块内的格式
}

// Step 2: 系统消息去重
type SystemMessageDeduplicator struct{}
func (s *SystemMessageDeduplicator) Apply(messages []Message) ([]Message, error) {
    // 合并重复的 system 消息
    // 保留第一次出现的内容
}

// Step 3: 工具长度压缩
type ToolLengthCompressor struct {
    MaxToolLength int // 默认 1000 字符
}
func (t *ToolLengthCompressor) Apply(messages []Message) ([]Message, error) {
    // 截断过长的 tool_call 参数
    // 保留结构完整性
}

// Step 4: 冗余消息移除
type RedundantMessageRemover struct{}
func (r *RedundantMessageRemover) Apply(messages []Message) ([]Message, error) {
    // 检测连续重复的消息
    // 保留第一次出现
}

// Step 5: 图片占位符处理
type ImagePlaceholderHandler struct{}
func (i *ImagePlaceholderHandler) Apply(messages []Message) ([]Message, error) {
    // 将 base64 图片替换为 [IMAGE: {size}KB]
    // 保留图片数量和大小信息
}
```

##### 3. Caveman 正则规则 (保护关键内容)

```go
// 参考 OmniRoute Caveman 规则
type CavemanConfig struct {
    Intensity          string   // lite/full/ultra
    CompressRoles      []string // user/assistant/system
    SkipRules          []string // 跳过的规则 ID
    MinMessageLength   int      // 最小消息长度才压缩
    PreservePatterns   []string // 保护的正则模式
}

// 默认保护模式
var DefaultPreservePatterns = []string{
    // 代码块
    "```[\\s\\S]*?```",
    "`[^`]+`",
    // URL
    "https?://[^\\s]+",
    // 文件路径
    "/[\\w/\\.\\-]+",
    "\\w:[\\w/\\\\\\.\\-]+",
    // 工具调用
    "<tool_use>[\\s\\S]*?</tool_use>",
    // 数字标识符
    "\\b[A-Z0-9]{8,}\\b",
}
```

##### 4. RTK 工具输出过滤

```go
// RTK (Reduced Tool Kit) 配置
type RtkConfig struct {
    Enabled            bool
    MaxOutputLength    int      // 默认 2000 字符
    PreserveSections   []string // 保留的章节
    CompressLevel      string   // lite/full/ultra
}

// RTK 过滤器
type RtkFilter struct {
    Config RtkConfig
}

func (r *RtkFilter) FilterToolOutput(output string) string {
    // 1. 提取关键段落
    // 2. 移除冗余日志
    // 3. 保留错误信息
    // 4. 压缩数据表格
}
```

##### 5. 自适应策略选择器

```go
type AdaptiveCompressionSelector struct {
    Rules []CompressionRule
}

type CompressionRule struct {
    Condition  func(ctx *SessionContext) bool
    Strategy   string // lite/standard/aggressive/ultra
    Priority   int
}

// 示例规则
var DefaultCompressionRules = []CompressionRule{
    {
        Condition: func(ctx *SessionContext) bool {
            return ctx.TotalTokens < 4000
        },
        Strategy: "lite",
        Priority: 100,
    },
    {
        Condition: func(ctx *SessionContext) bool {
            return ctx.TotalTokens >= 4000 && ctx.TotalTokens < 8000
        },
        Strategy: "standard",
        Priority: 90,
    },
    {
        Condition: func(ctx *SessionContext) bool {
            return ctx.TotalTokens >= 8000 && ctx.HasToolCalls
        },
        Strategy: "rtk",
        Priority: 80,
    },
    {
        Condition: func(ctx *SessionContext) bool {
            return ctx.TotalTokens >= 12000
        },
        Strategy: "aggressive",
        Priority: 70,
    },
}
```

#### Go 实现计划

```go
// 新增 Lite 压缩包
package lite

// 新增 Caveman 规则包
package caveman

// 新增 RTK 过滤器包
package rtk

// 新增策略选择器
package selector

// 扩展现有 SessionCompressor
type SessionCompressor struct {
    // 现有字段
    Cache              *SessionCache
    CompactionDeps     *Dependencies
    
    // 新增字段
    LitePipeline       *lite.Pipeline
    CavemanEngine      *caveman.Engine
    RtkFilter          *rtk.Filter
    StrategySelector   *selector.Selector
}
```

---

### 方案 3: 增强会话总结模块

#### 当前总结功能审计

**已有功能** (`domains/hooks/compression/compaction.go`):
- ✅ LLM 摘要生成 (tryLLMContextCompaction)
- ✅ 系统提示词增强 (compactionSystemPrompt v7)
- ✅ 保留精确值 (IDs, paths, error messages)
- ✅ 引用原文关键陈述

**缺失功能**:
- ❌ 即时总结 vs 事后总结模式
- ❌ 多维度总结 (项目/关键词/任务/决策)
- ❌ 可配置总结模型
- ❌ 总结质量评估

#### 新增功能设计

##### 1. 即时总结 vs 事后总结

```go
type SummaryMode string

const (
    SummaryModeRealtime  SummaryMode = "realtime"  // 即时总结
    SummaryModeDeferred  SummaryMode = "deferred"  // 事后总结
    SummaryModeHybrid    SummaryMode = "hybrid"    // 混合模式
)

type SummaryConfig struct {
    Mode               SummaryMode
    RealtimeTrigger    RealtimeTrigger
    DeferredSchedule   DeferredSchedule
    SummaryModel       ModelConfig
}

// 即时总结触发器
type RealtimeTrigger struct {
    TurnThreshold      int     // 每 N 轮触发一次
    TokenThreshold     int     // 超过 N tokens 触发
    TimeThreshold      time.Duration // 超过 N 时间触发
}

// 事后总结调度
type DeferredSchedule struct {
    OnSessionClose     bool    // 会话关闭时总结
    OnUserRequest      bool    // 用户请求时总结
    BatchSize          int     // 批量处理大小
}
```

**即时总结** (Realtime):
- **触发时机**: 每 10 轮、超过 8000 tokens、或超过 5 分钟
- **用途**: 在会话进行中压缩上下文，节省 Token
- **特点**: 低延迟、增量式、实时可用

**事后总结** (Deferred):
- **触发时机**: 会话关闭后、用户显式请求、或批量处理
- **用途**: 生成完整会话摘要，用于审计和分析
- **特点**: 高质量、完整性、异步处理

**混合模式** (Hybrid):
- 即时总结用于压缩
- 事后总结用于归档
- 两者互补

##### 2. 多维度总结设计

```go
type MultiDimensionSummary struct {
    SessionID          string                 `json:"session_id"`
    GeneratedAt        time.Time              `json:"generated_at"`
    SummaryModel       string                 `json:"summary_model"`
    
    // 维度 1: 项目上下文
    ProjectContext     ProjectSummary         `json:"project_context"`
    
    // 维度 2: 关键词提取
    Keywords           KeywordsSummary        `json:"keywords"`
    
    // 维度 3: 任务追踪
    Tasks              TasksSummary           `json:"tasks"`
    
    // 维度 4: 决策记录
    Decisions          DecisionsSummary       `json:"decisions"`
    
    // 维度 5: 问题与解决方案
    Problems           ProblemsSummary        `json:"problems"`
    
    // 维度 6: 代码与技术细节
    TechnicalDetails   TechnicalSummary       `json:"technical_details"`
}

// 维度 1: 项目上下文
type ProjectSummary struct {
    ProjectName        string                 `json:"project_name,omitempty"`
    ProjectType        string                 `json:"project_type,omitempty"`      // web/mobile/backend/ml
    TechStack          []string               `json:"tech_stack,omitempty"`
    MainGoal           string                 `json:"main_goal"`
    CurrentPhase       string                 `json:"current_phase,omitempty"`     // planning/development/testing
}

// 维度 2: 关键词提取
type KeywordsSummary struct {
    HighFrequency      []KeywordEntry         `json:"high_frequency"`     // 高频关键词
    TechnicalTerms     []KeywordEntry         `json:"technical_terms"`    // 技术术语
    DomainSpecific     []KeywordEntry         `json:"domain_specific"`    // 领域特定词
    EmergingTopics     []KeywordEntry         `json:"emerging_topics"`    // 新出现的主题
}

type KeywordEntry struct {
    Keyword            string                 `json:"keyword"`
    Frequency          int                    `json:"frequency"`
    FirstMention       int                    `json:"first_mention_turn"` // 首次出现轮次
    Relevance          float64                `json:"relevance"`          // 相关性评分
}

// 维度 3: 任务追踪
type TasksSummary struct {
    Completed          []Task                 `json:"completed"`
    InProgress         []Task                 `json:"in_progress"`
    Planned            []Task                 `json:"planned"`
    Blocked            []Task                 `json:"blocked"`
}

type Task struct {
    TaskID             string                 `json:"task_id"`
    Description        string                 `json:"description"`
    Status             string                 `json:"status"`
    Priority           string                 `json:"priority,omitempty"`
    MentionedTurns     []int                  `json:"mentioned_turns"`    // 涉及该任务的轮次
    Dependencies       []string               `json:"dependencies,omitempty"`
}

// 维度 4: 决策记录
type DecisionsSummary struct {
    Decisions          []Decision             `json:"decisions"`
}

type Decision struct {
    DecisionID         string                 `json:"decision_id"`
    Question           string                 `json:"question"`           // 决策问题
    Options            []string               `json:"options"`            // 可选方案
    ChosenOption       string                 `json:"chosen_option"`      // 选定方案
    Rationale          string                 `json:"rationale"`          // 决策理由
    Turn               int                    `json:"turn"`               // 决策发生的轮次
    Impact             string                 `json:"impact,omitempty"`   // 影响范围
}

// 维度 5: 问题与解决方案
type ProblemsSummary struct {
    Problems           []Problem              `json:"problems"`
}

type Problem struct {
    ProblemID          string                 `json:"problem_id"`
    Description        string                 `json:"description"`
    Severity           string                 `json:"severity"`           // low/medium/high/critical
    Status             string                 `json:"status"`             // open/investigating/resolved
    Solution           string                 `json:"solution,omitempty"`
    DiscoveredTurn     int                    `json:"discovered_turn"`
    ResolvedTurn       int                    `json:"resolved_turn,omitempty"`
}

// 维度 6: 代码与技术细节
type TechnicalSummary struct {
    CodeSnippets       []CodeSnippet          `json:"code_snippets"`
    APIs               []APIReference         `json:"apis"`
    Commands           []CommandEntry         `json:"commands"`
    FilesPaths         []string               `json:"files_paths"`
}

type CodeSnippet struct {
    Language           string                 `json:"language"`
    Code               string                 `json:"code"`
    Purpose            string                 `json:"purpose"`
    Turn               int                    `json:"turn"`
}

type APIReference struct {
    Endpoint           string                 `json:"endpoint"`
    Method             string                 `json:"method"`
    Purpose            string                 `json:"purpose"`
}

type CommandEntry struct {
    Command            string                 `json:"command"`
    Description        string                 `json:"description"`
    ExecutedTurns      []int                  `json:"executed_turns"`
}
```

##### 3. 可配置总结模型

```go
type SummaryModelConfig struct {
    // 模型选择
    DefaultModel       string                 // 默认总结模型
    FallbackModels     []string               // 备用模型列表
    
    // 按维度配置不同模型
    DimensionModels    map[string]string      // 维度 -> 模型映射
    
    // 模型参数
    Temperature        float64
    MaxTokens          int
    TopP               float64
}

// 默认配置
var DefaultSummaryModelConfig = SummaryModelConfig{
    DefaultModel: "gpt-4o-mini",
    FallbackModels: []string{"gpt-3.5-turbo", "claude-haiku-3.5"},
    DimensionModels: map[string]string{
        "project_context":     "gpt-4o-mini",
        "keywords":            "gpt-3.5-turbo",  // 简单任务用便宜模型
        "tasks":               "gpt-4o-mini",
        "decisions":           "gpt-4o",         // 重要决策用好模型
        "problems":            "gpt-4o-mini",
        "technical_details":   "gpt-4o",         // 技术细节用好模型
    },
    Temperature: 0.3,
    MaxTokens:   2000,
    TopP:        0.9,
}
```

##### 4. 总结质量评估

```go
type SummaryQualityMetrics struct {
    // 完整性评分
    Completeness       float64                // 0-1, 信息覆盖度
    
    // 准确性评分
    Accuracy           float64                // 0-1, 是否忠实原文
    
    // 简洁性评分
    Conciseness        float64                // 0-1, 压缩比合理性
    
    // 可读性评分
    Readability        float64                // 0-1, 是否易于理解
    
    // 实用性评分
    Usefulness         float64                // 0-1, 对后续对话的帮助度
    
    // 详细指标
    OriginalTurns      int
    SummaryTurns       int
    CompressionRatio   float64
    KeyInformationLoss int                    // 丢失的关键信息数量
}

type QualityEvaluator struct {
    Config EvaluationConfig
}

func (e *QualityEvaluator) Evaluate(
    original []Message,
    summary *MultiDimensionSummary,
) (*SummaryQualityMetrics, error) {
    // 实现评估逻辑
}
```

#### 总结模块实现架构

```go
package summary

type SummaryService struct {
    // 配置
    Config             SummaryConfig
    ModelConfig        SummaryModelConfig
    
    // 依赖
    LLMClient          LLMClientInterface
    Cache              SummaryCacheInterface
    Evaluator          *QualityEvaluator
    
    // 内部状态
    realtimeQueue      chan SummaryRequest
    deferredQueue      chan SummaryRequest
    workers            []*SummaryWorker
}

// 主入口: 生成多维度总结
func (s *SummaryService) GenerateSummary(
    ctx context.Context,
    sessionID string,
    messages []Message,
    mode SummaryMode,
) (*MultiDimensionSummary, error) {
    // 1. 根据 mode 选择处理路径
    // 2. 调用维度总结器
    // 3. 评估质量
    // 4. 缓存结果
}

// 维度总结器接口
type DimensionSummarizer interface {
    Name() string
    Summarize(ctx context.Context, messages []Message) (interface{}, error)
}

// 具体维度实现
type ProjectContextSummarizer struct{}
type KeywordsSummarizer struct{}
type TasksSummarizer struct{}
type DecisionsSummarizer struct{}
type ProblemsSummarizer struct{}
type TechnicalSummarizer struct{}
```

---

## 🧪 测试验证方案

### 测试目标

1. ✅ **功能正确性** - 三级缓存语义正确、总结准确
2. ✅ **性能指标** - 压缩比、延迟、吞吐量
3. ✅ **稳定性** - 并发安全、故障恢复
4. ✅ **兼容性** - 与现有系统无缝集成

### 测试阶段

#### Phase 1: 单元测试 (2 周)

##### 1.1 三级缓存测试

```go
// 测试文件: domains/hooks/compression/three_tier_cache_test.go

func TestRawSessionCache_TurnSequence(t *testing.T) {
    // 测试轮次序列的正确性
}

func TestCompressedSessionCache_BlockAlignment(t *testing.T) {
    // 测试压缩块与原始轮次的对齐
}

func TestSafeSessionCache_HashReference(t *testing.T) {
    // 测试安全缓存的 hash 引用完整性
}

func TestThreeTierCache_Consistency(t *testing.T) {
    // 测试三级缓存的一致性
}
```

##### 1.2 Lite 压缩测试

```go
// 测试文件: domains/hooks/compression/lite/pipeline_test.go

func TestLitePipeline_WhitespaceNormalization(t *testing.T) {
    // 测试空格规范化
}

func TestLitePipeline_SystemMessageDedup(t *testing.T) {
    // 测试系统消息去重
}

func TestLitePipeline_ToolLengthCompression(t *testing.T) {
    // 测试工具长度压缩
}

func TestLitePipeline_EndToEnd(t *testing.T) {
    // 端到端测试 5 步流水线
}
```

##### 1.3 多维度总结测试

```go
// 测试文件: domains/hooks/compression/summary/multi_dimension_test.go

func TestProjectContextSummarizer(t *testing.T) {
    // 测试项目上下文提取
}

func TestKeywordsSummarizer(t *testing.T) {
    // 测试关键词提取准确性
}

func TestTasksSummarizer(t *testing.T) {
    // 测试任务追踪完整性
}

func TestDecisionsSummarizer(t *testing.T) {
    // 测试决策记录准确性
}

func TestSummaryQualityEvaluator(t *testing.T) {
    // 测试质量评估指标
}
```

**覆盖率目标**: > 85%

#### Phase 2: 集成测试 (2 周)

##### 2.1 压缩流水线集成测试

```go
func TestCompressionPipeline_LiteToStandard(t *testing.T) {
    // 测试从 Lite 升级到 Standard
}

func TestCompressionPipeline_StrategySelection(t *testing.T) {
    // 测试自适应策略选择
}

func TestCompressionPipeline_FallbackMechanism(t *testing.T) {
    // 测试降级机制
}
```

##### 2.2 缓存层集成测试

```go
func TestCacheIntegration_L1L2L3Sync(t *testing.T) {
    // 测试三层缓存同步
}

func TestCacheIntegration_Eviction(t *testing.T) {
    // 测试缓存驱逐策略
}

func TestCacheIntegration_Recovery(t *testing.T) {
    // 测试从缓存恢复
}
```

##### 2.3 总结服务集成测试

```go
func TestSummaryService_RealtimeMode(t *testing.T) {
    // 测试即时总结模式
}

func TestSummaryService_DeferredMode(t *testing.T) {
    // 测试事后总结模式
}

func TestSummaryService_ModelFallback(t *testing.T) {
    // 测试模型降级
}
```

#### Phase 3: 性能测试 (1 周)

##### 3.1 压缩性能基准

```go
func BenchmarkLiteCompression(b *testing.B) {
    // 基准测试 Lite 压缩性能
}

func BenchmarkCavemanCompression(b *testing.B) {
    // 基准测试 Caveman 压缩性能
}

func BenchmarkRtkCompression(b *testing.B) {
    // 基准测试 RTK 压缩性能
}
```

**性能目标**:
- Lite 压缩: < 10ms (100 消息)
- Standard 压缩: < 50ms (100 消息)
- Aggressive 压缩: < 200ms (100 消息)
- 压缩比: 30-70% Token 节省

##### 3.2 缓存性能基准

```go
func BenchmarkRawCacheWrite(b *testing.B) {
    // 测试原始缓存写入性能
}

func BenchmarkCompressedCacheRead(b *testing.B) {
    // 测试压缩缓存读取性能
}

func BenchmarkSafeCacheSync(b *testing.B) {
    // 测试安全缓存同步性能
}
```

**性能目标**:
- L1 缓存读取: < 1ms
- L2 缓存读取: < 10ms
- L3 缓存读取: < 100ms
- 缓存命中率: > 80%

##### 3.3 总结性能基准

```go
func BenchmarkMultiDimensionSummary(b *testing.B) {
    // 测试多维度总结性能
}

func BenchmarkRealtimeSummary(b *testing.B) {
    // 测试即时总结延迟
}
```

**性能目标**:
- 即时总结: < 2s (50 轮会话)
- 事后总结: < 10s (200 轮会话)
- 并发处理: > 100 req/s

#### Phase 4: 压力测试 (1 周)

##### 4.1 并发压力测试

```bash
# 并发会话测试
./scripts/stress-test-compression.sh --sessions 1000 --turns-per-session 50

# 压缩引擎压力测试
./scripts/stress-test-engines.sh --concurrency 100 --duration 5m

# 缓存压力测试
./scripts/stress-test-cache.sh --writes 10000 --reads 100000
```

**压力目标**:
- 1000 并发会话稳定运行
- 10000 QPS 压缩请求
- 内存占用 < 2GB (1000 会话)
- CPU 使用率 < 70%

##### 4.2 故障注入测试

```go
func TestFaultInjection_RedisDown(t *testing.T) {
    // 测试 Redis 故障时的降级
}

func TestFaultInjection_PostgreSQLSlow(t *testing.T) {
    // 测试 PostgreSQL 慢查询时的超时
}

func TestFaultInjection_LLMTimeout(t *testing.T) {
    // 测试 LLM 超时时的回退策略
}
```

#### Phase 5: 生产验证 (2 周)

##### 5.1 灰度发布策略

```
Week 1:
- Day 1-2: 245 环境部署，5% 流量
- Day 3-4: 观察监控指标，10% 流量
- Day 5-7: 验证稳定性，20% 流量

Week 2:
- Day 8-10: 154 环境部署，50% 流量
- Day 11-12: 全量切换，100% 流量
- Day 13-14: 持续监控，性能调优
```

##### 5.2 监控指标

```prometheus
# 压缩指标
compression_strategy_selected_total{strategy="lite|standard|aggressive|ultra"}
compression_ratio_histogram{strategy="*"}
compression_duration_seconds{strategy="*"}
compression_errors_total{strategy="*",reason="*"}

# 缓存指标
three_tier_cache_hit_total{tier="raw|compressed|safe"}
three_tier_cache_miss_total{tier="*"}
three_tier_cache_eviction_total{tier="*"}
three_tier_cache_size_bytes{tier="*"}

# 总结指标
summary_generation_total{mode="realtime|deferred",dimension="*"}
summary_duration_seconds{mode="*",dimension="*"}
summary_quality_score{dimension="*"}
summary_errors_total{mode="*",reason="*"}
```

##### 5.3 回滚条件

**自动回滚触发**:
- ❌ 错误率 > 5%
- ❌ P99 延迟 > 5s
- ❌ 内存 OOM 3 次
- ❌ 缓存命中率 < 50%

**人工回滚触发**:
- 用户投诉增加 > 10%
- 数据不一致报告
- 未预期的系统行为

---

## 📅 实施时间表

### 总体规划: 8 周

```
Week 1-2: 设计与评审
├─ Week 1: 详细设计文档编写
│  ├─ Day 1-2: 三级缓存架构设计
│  ├─ Day 3-4: OmniRoute 压缩策略设计
│  └─ Day 5-7: 多维度总结设计
└─ Week 2: 设计评审与优化
   ├─ Day 8-9: 技术评审会议
   ├─ Day 10-11: 设计调整
   └─ Day 12-14: 原型验证

Week 3-4: 核心功能开发
├─ Week 3: 三级缓存实现
│  ├─ RawSessionCache
│  ├─ CompressedSessionCache
│  └─ SafeSessionCache
└─ Week 4: Lite 压缩实现
   ├─ 5 步流水线
   ├─ Caveman 规则
   └─ RTK 过滤器

Week 5-6: 高级功能开发
├─ Week 5: 多维度总结实现
│  ├─ 6 个维度总结器
│  ├─ 总结模型配置
│  └─ 质量评估器
└─ Week 6: 策略选择器实现
   ├─ 自适应选择
   ├─ 降级机制
   └─ 监控集成

Week 7: 测试与优化
├─ Day 43-45: 单元测试 + 集成测试
├─ Day 46-47: 性能测试 + 压力测试
└─ Day 48-49: Bug 修复与优化

Week 8: 灰度发布与验证
├─ Day 50-52: 245 环境灰度 (5% → 20%)
├─ Day 53-55: 154 环境灰度 (50% → 100%)
└─ Day 56: 全量验证与文档完善
```

---

## 🎯 验收标准

### 功能验收

- [ ] 三级缓存架构完整实现
  - [ ] 原始会话缓存 (RawSessionCache)
  - [ ] 压缩会话缓存 (CompressedSessionCache)
  - [ ] 安全处理会话缓存 (SafeSessionCache)
  - [ ] 轮次一致性保证
  - [ ] 双向存储 (request + response)

- [ ] OmniRoute 压缩策略实现
  - [ ] Lite 压缩 5 步流水线
  - [ ] Caveman 保护规则
  - [ ] RTK 工具过滤
  - [ ] 自适应策略选择器
  - [ ] 降级机制

- [ ] 多维度总结功能
  - [ ] 6 个维度总结器全部实现
  - [ ] 即时总结 vs 事后总结模式
  - [ ] 可配置总结模型
  - [ ] 质量评估指标

### 性能验收

- [ ] 压缩性能达标
  - [ ] Lite 压缩 < 10ms (100 消息)
  - [ ] 压缩比 30-70%
  - [ ] 并发处理 > 100 req/s

- [ ] 缓存性能达标
  - [ ] L1 读取 < 1ms
  - [ ] L2 读取 < 10ms
  - [ ] 缓存命中率 > 80%

- [ ] 总结性能达标
  - [ ] 即时总结 < 2s (50 轮)
  - [ ] 事后总结 < 10s (200 轮)

### 稳定性验收

- [ ] 并发压力测试通过
  - [ ] 1000 并发会话稳定
  - [ ] 内存占用 < 2GB
  - [ ] CPU 使用率 < 70%

- [ ] 故障恢复测试通过
  - [ ] Redis 故障降级正常
  - [ ] PostgreSQL 慢查询超时保护
  - [ ] LLM 超时回退策略有效

- [ ] 生产验证通过
  - [ ] 灰度发布无重大问题
  - [ ] 监控指标正常
  - [ ] 用户反馈良好

### 文档验收

- [ ] 设计文档完整
- [ ] API 文档完整
- [ ] 运维手册完整
- [ ] 测试报告完整

---

## 🚀 后续迭代规划

### Q4 2026

#### 迭代 1: Deep Compression (Phase 2)
- 引入 aggressive 和 ultra 压缩模式
- 实现 Memora 会话事实集成
- 优化 LLM 摘要质量

#### 迭代 2: 智能缓存预热
- 会话恢复时预加载历史
- 用户行为预测
- 热点会话识别

#### 迭代 3: 总结增强
- 语义相似度聚类
- 跨会话关联分析
- 自动标签生成

### 2027 Q1

#### 迭代 4: MCP Server 集成
- 实现模型上下文协议服务器
- 与 OmniRoute MCP 对接
- 跨系统会话共享

#### 迭代 5: A2A Protocol
- Agent-to-Agent 通信协议
- 分布式会话协调
- 会话迁移与恢复

---

## 📊 成功指标

### 业务指标

- **Token 成本降低**: 30-50%
- **用户满意度**: NPS > 40
- **会话完成率**: > 85%
- **平均会话轮次**: 减少 20%

### 技术指标

- **压缩比**: 30-70%
- **缓存命中率**: > 80%
- **P99 延迟**: < 500ms
- **错误率**: < 0.1%
- **可用性**: 99.9%

### 运维指标

- **部署成功率**: 100%
- **回滚次数**: 0
- **故障恢复时间**: < 5 分钟
- **监控覆盖率**: 100%

---

## 📝 附录

### A. 参考文档

1. **OmniRoute 源码分析**
   - `src/lib/db/compression.ts`
   - `src/lib/db/compressionCombos.ts`
   - 压缩引擎配置与策略

2. **当前架构文档**
   - `domains/hooks/compression/session_compressor.go`
   - `domains/hooks/compression/session_cache.go`
   - 现有压缩与缓存实现

3. **相关设计文档**
   - `docs/omniroute-ref/phase1/03-lite-compression.md`
   - `docs/omniroute-ref/phase2/04-deep-compression.md`

### B. 风险评估

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 三级缓存一致性问题 | 高 | 中 | 事务保护 + 一致性测试 |
| 压缩质量下降 | 中 | 低 | 质量评估 + 人工抽检 |
| 性能回归 | 高 | 低 | 性能基准 + 灰度发布 |
| LLM 成本增加 | 中 | 中 | 模型选择 + Token 预算 |
| 数据迁移失败 | 高 | 低 | 兼容性保证 + 回滚方案 |

### C. 团队分工

| 角色 | 职责 | 人员 |
|------|------|------|
| 架构师 | 整体设计与评审 | halfking |
| 后端开发 | 核心功能实现 | 2-3 人 |
| 测试工程师 | 测试方案与执行 | 1-2 人 |
| 运维工程师 | 部署与监控 | 1 人 |
| 项目经理 | 进度跟踪与协调 | 1 人 |

---

**文档版本**: v1.0-DRAFT  
**最后更新**: 2026-08-02  
**待审核**: ✅ 技术评审 → ✅ 产品评审 → ✅ 资源评审