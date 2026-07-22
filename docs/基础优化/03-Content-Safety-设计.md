# Content Safety Filter 设计文档

> **版本**: v1.0
> **日期**: 2026-07-18
> **状态**: Draft
> **负责人**: Infrastructure Team

---

## 1. 背景与目标

### 1.1 背景

LLM Gateway 需要对用户输入和模型输出进行内容安全审核，防止：
- 敏感词泄露
- 违规内容传播
- 恶意 Prompt 注入
- 有害信息输出

### 1.2 目标

- ✅ **实时检测**: 请求/响应时检测，延迟 < 5ms
- ✅ **高准确率**: 误报率 < 1%，漏报率 < 0.1%
- ✅ **可配置**: 支持自定义规则和敏感词库
- ✅ **多层防护**: 关键词 + 正则 + 语义检测
- ✅ **可观测**: 完整的拦截日志和统计

---

## 2. 架构设计

### 2.1 整体架构

```
┌─────────────────────────────────────────────┐
│            Content Safety Filter             │
├─────────────────────────────────────────────┤
│                                             │
│  ┌──────────────┐  ┌──────────────┐       │
│  │ Keyword      │  │ Regex        │       │
│  │ Matcher      │  │ Matcher      │       │
│  └──────────────┘  └──────────────┘       │
│                                             │
│  ┌──────────────────────────────────────┐  │
│  │         Rule Engine                  │  │
│  │  - 黑名单 / 白名单                    │  │
│  │  - 组合规则                          │  │
│  └──────────────────────────────────────┘  │
│                                             │
│  ┌──────────────────────────────────────┐  │
│  │         Action Handler               │  │
│  │  - Block / Warn / Log                │  │
│  └──────────────────────────────────────┘  │
└─────────────────────────────────────────────┘
```

### 2.2 检测流程

```
请求/响应
    │
    ├─→ [Keyword Matcher] → 匹配敏感词
    │
    ├─→ [Regex Matcher]   → 匹配正则模式
    │
    └─→ [Rule Engine]     → 综合判断
            │
            ├─→ Safe     → 放行
            ├─→ Block    → 拦截
            ├─→ Warn     → 警告 + 放行
            └─→ Sanitize → 脱敏 + 放行
```

---

## 3. 核心接口

### 3.1 Filter 接口

```go
type Filter interface {
    // CheckRequest 检查请求内容
    CheckRequest(ctx context.Context, req *CheckRequest) (*CheckResult, error)

    // CheckResponse 检查响应内容
    CheckResponse(ctx context.Context, resp *CheckResponse) (*CheckResult, error)

    // UpdateRules 更新规则
    UpdateRules(rules []Rule) error

    // Metrics 返回统计指标
    Metrics() FilterMetrics
}
```

### 3.2 数据结构

```go
// CheckRequest 检查请求
type CheckRequest struct {
    Content  string
    UserID   string
    Metadata map[string]string
}

// CheckResult 检查结果
type CheckResult struct {
    Safe         bool              // 是否安全
    Action       Action            // 动作 (block/warn/sanitize)
    Reason       string            // 原因
    MatchedRules []string          // 匹配的规则
    Hits         []Hit             // 匹配详情
    SanitizedContent string        // 脱敏后内容（如适用）
}

// Action 动作类型
type Action string

const (
    ActionAllow    Action = "allow"
    ActionBlock    Action = "block"
    ActionWarn     Action = "warn"
    ActionSanitize Action = "sanitize"
)

// Hit 匹配项
type Hit struct {
    RuleID   string
    RuleName string
    Pattern  string
    Position int
    Length   int
    Severity Severity
}

// Severity 严重程度
type Severity string

const (
    SeverityLow      Severity = "low"
    SeverityMedium   Severity = "medium"
    SeverityHigh     Severity = "high"
    SeverityCritical Severity = "critical"
)
```

---

## 4. 规则系统

### 4.1 规则类型

```go
type Rule struct {
    ID          string
    Name        string
    Type        RuleType
    Pattern     string      // 关键词或正则
    Action      Action
    Severity    Severity
    Enabled     bool
    WhiteList   []string    // 白名单（例外）
    Description string
}

type RuleType string

const (
    RuleTypeKeyword RuleType = "keyword"
    RuleTypeRegex   RuleType = "regex"
)
```

### 4.2 内置规则

| 类别 | 规则示例 | 严重程度 | 动作 |
|------|---------|---------|------|
| **API Key 泄露** | `sk-[a-zA-Z0-9]{48}` (OpenAI) | Critical | Block |
| **身份证号** | `\d{17}[\dXx]` | High | Sanitize |
| **手机号** | `1[3-9]\d{9}` | Medium | Sanitize |
| **敏感词** | 政治/暴力/色情关键词 | High | Block |
| **SQL 注入** | `(union|select|drop|insert)\s+` | High | Block |

---

## 5. 性能优化

### 5.1 Trie 树加速

```
关键词匹配使用 Aho-Corasick 算法:
- 构建时间: O(m) (m = 总字符数)
- 匹配时间: O(n) (n = 文本长度)
- 空间: O(m)
```

### 5.2 正则缓存

```go
type regexCache struct {
    cache map[string]*regexp.Regexp
    mu    sync.RWMutex
}

func (c *regexCache) Get(pattern string) (*regexp.Regexp, error) {
    // 缓存已编译的正则表达式
}
```

### 5.3 并行检测

```go
func (f *filter) check(content string) {
    var wg sync.WaitGroup
    results := make(chan Hit, 10)

    // 并行执行 keyword 和 regex 检测
    wg.Add(2)
    go f.checkKeywords(content, results, &wg)
    go f.checkRegex(content, results, &wg)

    // 收集结果
    go func() {
        wg.Wait()
        close(results)
    }()
}
```

---

## 6. Prometheus Metrics

```go
var (
    // 检测总数
    ContentSafetyChecks = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_content_safety_checks_total",
            Help: "Total content safety checks",
        },
        []string{"type", "action"}, // type: request|response, action: allow|block|warn|sanitize
    )

    // 拦截总数
    ContentSafetyBlocked = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_content_safety_blocked_total",
            Help: "Total blocked requests/responses",
        },
        []string{"type", "rule", "severity"},
    )

    // 检测延迟
    ContentSafetyLatency = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_content_safety_latency_ms",
            Help:    "Content safety check latency in milliseconds",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20},
        },
        []string{"type"},
    )

    // 规则命中率
    ContentSafetyHits = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_content_safety_hits_total",
            Help: "Total rule hits",
        },
        []string{"rule_id", "rule_name", "severity"},
    )
)
```

---

## 7. 配置示例

### 7.1 YAML 配置

```yaml
content_safety:
  enabled: true

  # 内置规则集
  builtin_rules:
    - api_key_detection
    - pii_detection
    - sensitive_words

  # 自定义规则
  custom_rules:
    - id: custom_001
      name: "公司内部信息"
      type: keyword
      pattern: "内部机密|商业秘密"
      action: block
      severity: high

    - id: custom_002
      name: "SQL 注入检测"
      type: regex
      pattern: "(union|select|drop|insert)\\s+"
      action: block
      severity: high

  # 动作配置
  actions:
    block:
      response: "内容包含敏感信息，请求被拦截"

    sanitize:
      mask_char: "*"
      expose_prefix: 3
      expose_suffix: 4
```

---

## 8. 测试策略

### 8.1 单元测试

| 测试项 | 覆盖 |
|--------|------|
| 关键词匹配 | ✅ 精确/模糊/大小写 |
| 正则匹配 | ✅ 常见模式 |
| 规则组合 | ✅ 优先级/白名单 |
| 脱敏逻辑 | ✅ 手机/身份证/API Key |
| 并发安全 | ✅ 100 goroutine |
| 性能基准 | ✅ Benchmark |

### 8.2 集成测试

```go
func TestContentSafety_Integration(t *testing.T) {
    // 场景 1: API Key 泄露拦截
    // 场景 2: 身份证号脱敏
    // 场景 3: 白名单例外
    // 场景 4: 组合规则
}
```

---

## 9. 反模式

| 反模式 | 说明 | 正确做法 |
|--------|------|---------|
| ❌ 同步阻塞 | 检测阻塞请求处理 | ✅ 异步 + 超时 |
| ❌ 全量正则 | 所有内容跑正则 | ✅ 先关键词筛选 |
| ❌ 规则爆炸 | 1000+ 规则 | ✅ 分类 + 优先级 |
| ❌ 硬编码 | 规则写死代码 | ✅ 配置文件 + 热更新 |
| ❌ 无白名单 | 误杀合法内容 | ✅ 例外机制 |

---

## 10. 实施计划

### 10.1 里程碑

| 里程碑 | 产物 | 预计 |
|--------|------|------|
| M1: 核心接口 | `filter.go` | 30 min |
| M2: Keyword Matcher | `keyword.go` | 45 min |
| M3: Regex Matcher | `regex.go` | 30 min |
| M4: 内置规则 | `rules.go` | 30 min |
| M5: 单元测试 | `*_test.go` | 45 min |
| **总计** | | **3 小时** |

### 10.2 集成路径

```
1. 独立实现 → 单元测试
2. 集成到 adapter (pre-check)
3. 集成到 proxy (post-check)
4. Prometheus metrics
5. 压力测试 (QPS > 10k)
```

---

## 11. 参考

- OpenAI Moderation API
- Google Perspective API
- AWS Comprehend Content Moderation
- Trie 树: https://en.wikipedia.org/wiki/Trie
- Aho-Corasick: https://en.wikipedia.org/wiki/Aho%E2%80%93Corasick_algorithm
