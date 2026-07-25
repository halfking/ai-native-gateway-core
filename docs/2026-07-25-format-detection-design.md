# 智能请求格式识别与自适应系统设计

**日期**: 2026-07-25  
**设计者**: Kiro AI Assistant  
**需求来源**: 用户反馈 - 需要自动检测和适配不同客户端的请求格式  

---

## 需求分析

### 核心问题
1. 不同客户端（OpenAI SDK、OpenCode、Cursor、Cherry Studio 等）可能使用不同的请求格式
2. 某些客户端可能发送不完整或非标准的请求
3. 需要自动识别格式并适配，而不是直接拒绝

### 设计目标
1. **自动识别** - 检测请求格式，匹配已知模式
2. **格式修复** - 尝试修复常见的格式问题
3. **会话绑定** - 将格式信息缓存到 Redis，同一会话复用
4. **提高成功率** - 最大化兼容性，减少拒绝率

---

## 系统架构

### 整体流程

```
客户端请求
    ↓
格式识别 (FormatDetector)
    ↓
Redis 查询 (已知格式?)
    ↓ 是              ↓ 否
使用缓存格式      格式检测 + 评分
    ↓                  ↓
格式修复 (FormatFixer)
    ↓                  ↓
Redis 缓存     ← 保存格式信息
    ↓
正常处理
```

### 核心组件

1. **FormatDetector** - 格式识别引擎
   - 检测请求体结构
   - 匹配已知格式模式
   - 评分并选择最佳匹配

2. **FormatFixer** - 格式修复器
   - 修复空对象 → 空数组
   - 补全缺失字段
   - 标准化字段名

3. **FormatRegistry** - 格式注册表
   - 维护已知客户端格式
   - 提供格式模板
   - 支持动态注册

4. **FormatCache** - Redis 缓存层
   - 会话 → 格式映射
   - TTL: 24 小时
   - 支持强制刷新

---

## 详细设计

### 1. 格式模式定义

```go
// FormatPattern 定义一个客户端格式模式
type FormatPattern struct {
    ID          string   // 唯一标识，如 "opencode-v1"
    Name        string   // 显示名称，如 "OpenCode CLI"
    ClientHint  []string // User-Agent 提示，如 ["opencode", "openai-python"]
    Version     string   // 版本，如 "1.0.0"
    
    // 结构特征
    Features    FormatFeatures
    
    // 常见问题
    KnownIssues []FormatIssue
    
    // 修复策略
    Fixes       []FormatFix
}

// FormatFeatures 描述格式的结构特征
type FormatFeatures struct {
    // 必填字段
    RequiredFields []string // ["model", "messages"]
    
    // 可选字段
    OptionalFields []string // ["temperature", "stream", "tools"]
    
    // 字段类型签名
    FieldTypes map[string]string // {"messages": "array", "model": "string"}
    
    // 特殊标记
    Markers []string // 用于识别的特征，如特定的 header
}

// FormatIssue 描述已知问题
type FormatIssue struct {
    Type        string // "empty_object", "missing_field", "wrong_type"
    Field       string // 问题字段
    Description string // 描述
    Frequency   int    // 出现频率（用于优先级）
}

// FormatFix 描述修复策略
type FormatFix struct {
    IssueType   string
    FixFunc     func(body map[string]any) (map[string]any, error)
    Description string
}
```

### 2. 格式检测算法

```go
// FormatDetector 格式检测引擎
type FormatDetector struct {
    registry *FormatRegistry
}

// DetectResult 检测结果
type DetectResult struct {
    Pattern     *FormatPattern // 匹配的格式
    Confidence  float64        // 置信度 0.0-1.0
    Issues      []FormatIssue  // 检测到的问题
    CanFix      bool           // 是否可修复
    Suggestions []string       // 修复建议
}

// Detect 检测请求格式
func (d *FormatDetector) Detect(body []byte, headers http.Header) DetectResult {
    var bodyMap map[string]any
    if err := json.Unmarshal(body, &bodyMap); err != nil {
        return DetectResult{
            Confidence: 0.0,
            Issues: []FormatIssue{{Type: "invalid_json"}},
            CanFix: false,
        }
    }
    
    // 1. 基于 User-Agent 快速匹配
    if pattern := d.matchByUserAgent(headers); pattern != nil {
        score := d.scorePattern(bodyMap, pattern)
        if score > 0.8 {
            return DetectResult{
                Pattern:    pattern,
                Confidence: score,
                Issues:     d.detectIssues(bodyMap, pattern),
                CanFix:     true,
            }
        }
    }
    
    // 2. 结构特征匹配（遍历所有已知格式）
    bestMatch := d.findBestMatch(bodyMap)
    return bestMatch
}

// scorePattern 为请求体和格式模式打分
func (d *FormatDetector) scorePattern(body map[string]any, pattern *FormatPattern) float64 {
    score := 0.0
    maxScore := 0.0
    
    // 必填字段检查（权重 0.5）
    for _, field := range pattern.Features.RequiredFields {
        maxScore += 0.5
        if _, ok := body[field]; ok {
            score += 0.5
        }
    }
    
    // 字段类型检查（权重 0.3）
    for field, expectedType := range pattern.Features.FieldTypes {
        maxScore += 0.3
        if val, ok := body[field]; ok {
            actualType := getJSONType(val)
            if actualType == expectedType {
                score += 0.3
            } else if isCompatibleType(actualType, expectedType) {
                score += 0.15 // 兼容类型得一半分
            }
        }
    }
    
    // 特殊标记检查（权重 0.2）
    for _, marker := range pattern.Features.Markers {
        maxScore += 0.2
        if hasMarker(body, marker) {
            score += 0.2
        }
    }
    
    if maxScore == 0 {
        return 0.0
    }
    return score / maxScore
}
```

### 3. 格式修复器

```go
// FormatFixer 格式修复器
type FormatFixer struct {
    logger *slog.Logger
}

// FixResult 修复结果
type FixResult struct {
    Original    []byte   // 原始请求体
    Fixed       []byte   // 修复后的请求体
    Applied     []string // 应用的修复
    Changed     bool     // 是否有修改
    Pattern     string   // 使用的格式模式
}

// Fix 修复请求格式
func (f *FormatFixer) Fix(body []byte, pattern *FormatPattern) (FixResult, error) {
    var bodyMap map[string]any
    if err := json.Unmarshal(body, &bodyMap); err != nil {
        return FixResult{}, err
    }
    
    result := FixResult{
        Original: body,
        Pattern:  pattern.ID,
    }
    
    // 应用所有修复策略
    for _, fix := range pattern.Fixes {
        if shouldApplyFix(bodyMap, fix) {
            fixed, err := fix.FixFunc(bodyMap)
            if err != nil {
                f.logger.Warn("fix failed", "type", fix.IssueType, "error", err)
                continue
            }
            bodyMap = fixed
            result.Applied = append(result.Applied, fix.IssueType)
            result.Changed = true
        }
    }
    
    if result.Changed {
        fixedBytes, _ := json.Marshal(bodyMap)
        result.Fixed = fixedBytes
    } else {
        result.Fixed = body
    }
    
    return result, nil
}

// 预定义的修复函数

// fixEmptyObjectMessages 修复空对象的 messages
func fixEmptyObjectMessages(body map[string]any) (map[string]any, error) {
    if msg, ok := body["messages"]; ok {
        // 检查是否为空对象
        if msgMap, isMap := msg.(map[string]any); isMap && len(msgMap) == 0 {
            // 替换为空数组
            body["messages"] = []any{}
            return body, nil
        }
    }
    return body, nil
}

// fixMissingModel 补全缺失的 model 字段
func fixMissingModel(body map[string]any) (map[string]any, error) {
    if _, ok := body["model"]; !ok {
        // 使用默认模型
        body["model"] = "gpt-3.5-turbo"
    }
    return body, nil
}

// fixWrongMessageType 修复错误的 message 类型
func fixWrongMessageType(body map[string]any) (map[string]any, error) {
    if msg, ok := body["messages"]; ok {
        // 如果是字符串，转换为消息数组
        if msgStr, isStr := msg.(string); isStr {
            body["messages"] = []any{
                map[string]any{
                    "role":    "user",
                    "content": msgStr,
                },
            }
            return body, nil
        }
    }
    return body, nil
}

// normalizeStreamField 标准化 stream 字段
func normalizeStreamField(body map[string]any) (map[string]any, error) {
    if stream, ok := body["stream"]; ok {
        // 字符串 "true"/"false" → 布尔值
        if streamStr, isStr := stream.(string); isStr {
            body["stream"] = (streamStr == "true" || streamStr == "1")
        }
        // 数字 1/0 → 布尔值
        if streamNum, isNum := stream.(float64); isNum {
            body["stream"] = (streamNum != 0)
        }
    }
    return body, nil
}
```

### 4. Redis 缓存层

```go
// FormatCache Redis 缓存
type FormatCache struct {
    redis  *redis.Client
    ttl    time.Duration // 默认 24 小时
    prefix string        // 键前缀 "format:session:"
}

// CachedFormat 缓存的格式信息
type CachedFormat struct {
    PatternID   string    `json:"pattern_id"`
    PatternName string    `json:"pattern_name"`
    Confidence  float64   `json:"confidence"`
    CachedAt    time.Time `json:"cached_at"`
    UseCount    int       `json:"use_count"`
    LastUsed    time.Time `json:"last_used"`
}

// Get 获取会话的格式信息
func (c *FormatCache) Get(ctx context.Context, sessionID string) (*CachedFormat, error) {
    key := c.prefix + sessionID
    data, err := c.redis.Get(ctx, key).Bytes()
    if err == redis.Nil {
        return nil, nil // 未找到
    }
    if err != nil {
        return nil, err
    }
    
    var cached CachedFormat
    if err := json.Unmarshal(data, &cached); err != nil {
        return nil, err
    }
    
    // 更新使用统计
    cached.UseCount++
    cached.LastUsed = time.Now()
    c.Set(ctx, sessionID, &cached) // 异步更新，忽略错误
    
    return &cached, nil
}

// Set 保存会话的格式信息
func (c *FormatCache) Set(ctx context.Context, sessionID string, format *CachedFormat) error {
    key := c.prefix + sessionID
    data, err := json.Marshal(format)
    if err != nil {
        return err
    }
    return c.redis.Set(ctx, key, data, c.ttl).Err()
}

// Delete 删除会话的格式信息（用于强制刷新）
func (c *FormatCache) Delete(ctx context.Context, sessionID string) error {
    key := c.prefix + sessionID
    return c.redis.Del(ctx, key).Err()
}

// GetStats 获取格式使用统计
func (c *FormatCache) GetStats(ctx context.Context) (map[string]int, error) {
    // 扫描所有格式缓存，统计使用频率
    iter := c.redis.Scan(ctx, 0, c.prefix+"*", 1000).Iterator()
    stats := make(map[string]int)
    
    for iter.Next(ctx) {
        data, err := c.redis.Get(ctx, iter.Val()).Bytes()
        if err != nil {
            continue
        }
        var cached CachedFormat
        if err := json.Unmarshal(data, &cached); err != nil {
            continue
        }
        stats[cached.PatternID] += cached.UseCount
    }
    
    return stats, iter.Err()
}
```

### 5. 格式注册表

```go
// FormatRegistry 格式注册表
type FormatRegistry struct {
    mu       sync.RWMutex
    patterns map[string]*FormatPattern
}

// NewFormatRegistry 创建注册表并预注册已知格式
func NewFormatRegistry() *FormatRegistry {
    r := &FormatRegistry{
        patterns: make(map[string]*FormatPattern),
    }
    
    // 预注册标准 OpenAI 格式
    r.Register(createOpenAIPattern())
    
    // 预注册 OpenCode 格式
    r.Register(createOpenCodePattern())
    
    // 预注册 Anthropic 格式
    r.Register(createAnthropicPattern())
    
    // 更多格式...
    
    return r
}

// Register 注册格式模式
func (r *FormatRegistry) Register(pattern *FormatPattern) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.patterns[pattern.ID] = pattern
}

// Get 获取格式模式
func (r *FormatRegistry) Get(id string) *FormatPattern {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.patterns[id]
}

// List 列出所有格式
func (r *FormatRegistry) List() []*FormatPattern {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    patterns := make([]*FormatPattern, 0, len(r.patterns))
    for _, p := range r.patterns {
        patterns = append(patterns, p)
    }
    return patterns
}

// createOpenCodePattern 创建 OpenCode 格式定义
func createOpenCodePattern() *FormatPattern {
    return &FormatPattern{
        ID:   "opencode-v1",
        Name: "OpenCode CLI",
        ClientHint: []string{"opencode", "openai-python"},
        Version: "1.0.0",
        Features: FormatFeatures{
            RequiredFields: []string{"model"},
            OptionalFields: []string{"messages", "temperature", "stream"},
            FieldTypes: map[string]string{
                "model":    "string",
                "messages": "array",
                "stream":   "boolean",
            },
        },
        KnownIssues: []FormatIssue{
            {
                Type:        "empty_object_messages",
                Field:       "messages",
                Description: "在「请继续」场景下发送 messages: {}",
                Frequency:   50, // 中等频率
            },
            {
                Type:        "missing_messages",
                Field:       "messages",
                Description: "某些请求缺少 messages 字段",
                Frequency:   20, // 低频率
            },
        },
        Fixes: []FormatFix{
            {
                IssueType:   "empty_object_messages",
                FixFunc:     fixEmptyObjectMessages,
                Description: "将空对象 {} 替换为空数组 []",
            },
        },
    }
}

// createOpenAIPattern 创建标准 OpenAI 格式
func createOpenAIPattern() *FormatPattern {
    return &FormatPattern{
        ID:   "openai-standard",
        Name: "OpenAI Standard API",
        ClientHint: []string{"openai-python", "openai-node"},
        Version: "1.0.0",
        Features: FormatFeatures{
            RequiredFields: []string{"model", "messages"},
            OptionalFields: []string{"temperature", "top_p", "n", "stream", "tools"},
            FieldTypes: map[string]string{
                "model":       "string",
                "messages":    "array",
                "temperature": "number",
                "stream":      "boolean",
            },
        },
        KnownIssues: []FormatIssue{},
        Fixes:       []FormatFix{},
    }
}
```

---

## 集成到现有系统

### 在 handler.go 中的集成点

```go
// 在 line 1417 附近，JSON 解析成功后

var reqBody chatRequestBody
if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
    // ... 现有错误处理 ...
    return
}

// ========== 新增：格式检测与修复 ==========

// 1. 尝试从 Redis 获取已知格式
var detectedPattern *FormatPattern
sessionID := r.Header.Get("X-Gw-Session-Id")

if sessionID != "" {
    if cached, err := h.formatCache.Get(ctx, sessionID); err == nil && cached != nil {
        detectedPattern = h.formatRegistry.Get(cached.PatternID)
        slog.Debug("using cached format",
            "session_id", sessionID,
            "pattern", cached.PatternID,
            "confidence", cached.Confidence)
    }
}

// 2. 如果没有缓存，执行格式检测
if detectedPattern == nil {
    detectResult := h.formatDetector.Detect(bodyBytes, r.Header)
    
    if detectResult.Confidence > 0.6 {
        detectedPattern = detectResult.Pattern
        
        // 缓存格式信息
        if sessionID != "" {
            cached := &CachedFormat{
                PatternID:   detectResult.Pattern.ID,
                PatternName: detectResult.Pattern.Name,
                Confidence:  detectResult.Confidence,
                CachedAt:    time.Now(),
                UseCount:    1,
                LastUsed:    time.Now(),
            }
            h.formatCache.Set(ctx, sessionID, cached) // 异步，忽略错误
        }
        
        slog.Info("format detected",
            "pattern", detectResult.Pattern.ID,
            "confidence", detectResult.Confidence,
            "issues", len(detectResult.Issues))
    }
}

// 3. 如果检测到格式且有已知问题，尝试修复
if detectedPattern != nil && len(detectedPattern.KnownIssues) > 0 {
    fixResult, err := h.formatFixer.Fix(bodyBytes, detectedPattern)
    if err != nil {
        slog.Warn("format fix failed", "error", err)
    } else if fixResult.Changed {
        // 使用修复后的请求体
        bodyBytes = fixResult.Fixed
        
        // 重新解析
        if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
            slog.Error("failed to parse fixed body", "error", err)
            // 继续使用原始 body
        } else {
            slog.Info("request body fixed",
                "pattern", fixResult.Pattern,
                "fixes", fixResult.Applied)
            
            // 记录修复指标
            formatFixAppliedCounter.WithLabelValues(detectedPattern.ID).Inc()
        }
    }
}

// 4. 继续原有的验证逻辑
if errMsg := ValidateNonEmptyArray(reqBody.Messages, "messages"); errMsg != "" {
    // ... 现有验证逻辑 ...
}
```

---

## Prometheus 指标

```go
var (
    // 格式检测指标
    formatDetectionCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llmgw_format_detection_total",
            Help: "Total number of format detections",
        },
        []string{"pattern", "source"}, // source: "cache" or "detect"
    )
    
    // 格式修复指标
    formatFixAppliedCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llmgw_format_fix_applied_total",
            Help: "Total number of format fixes applied",
        },
        []string{"pattern"},
    )
    
    // 格式置信度直方图
    formatConfidenceHistogram = promauto.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "llmgw_format_confidence",
            Help:    "Distribution of format detection confidence scores",
            Buckets: []float64{0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 1.0},
        },
    )
    
    // 缓存命中率
    formatCacheHitCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llmgw_format_cache_total",
            Help: "Format cache hits and misses",
        },
        []string{"result"}, // "hit" or "miss"
    )
)
```

---

## 配置选项

```yaml
# config.yaml
format_detection:
  enabled: true
  
  # Redis 缓存
  cache:
    enabled: true
    ttl: 24h
    prefix: "format:session:"
  
  # 检测配置
  detection:
    min_confidence: 0.6  # 最低置信度阈值
    enable_user_agent_hint: true
    enable_structure_match: true
  
  # 修复配置
  fix:
    enabled: true
    auto_fix_threshold: 0.7  # 置信度 > 0.7 时自动修复
    log_fixes: true
  
  # 已知格式
  patterns:
    - id: "opencode-v1"
      enabled: true
    - id: "openai-standard"
      enabled: true
    - id: "anthropic-claude"
      enabled: true
```

---

## 使用示例

### 场景 1: 首次请求（未知格式）

```
请求: {"model":"gpt-4","messages":{}}
    ↓
格式检测: 匹配 opencode-v1, 置信度 0.85
    ↓
Redis: 缓存格式信息 (session_123 → opencode-v1)
    ↓
格式修复: 应用 fixEmptyObjectMessages
    ↓
修复后: {"model":"gpt-4","messages":[]}
    ↓
验证: ❌ 空数组仍然无效
    ↓
返回: 400 "messages array cannot be empty"
```

### 场景 2: 同一会话的后续请求

```
请求: {"model":"gpt-4","messages":{}}
    ↓
Redis: 命中缓存 (session_123 → opencode-v1)
    ↓
格式修复: 直接应用已知修复
    ↓
修复后: {"model":"gpt-4","messages":[]}
    ↓
验证: 快速失败
```

### 场景 3: 可修复的请求

```
请求: {"model":"gpt-4","messages":"hello"}
    ↓
格式检测: 匹配 opencode-v1
    ↓
格式修复: 应用 fixWrongMessageType
    ↓
修复后: {"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}
    ↓
验证: ✅ 通过
    ↓
处理: 正常流程
```

---

## 实现优先级

### P0 - 核心功能（本周）
1. ✅ FormatPattern 数据结构
2. ✅ FormatDetector 基础实现
3. ✅ FormatFixer 基础修复函数
4. ✅ FormatRegistry 预定义格式

### P1 - 缓存和优化（下周）
5. ⏳ FormatCache Redis 集成
6. ⏳ 集成到 handler.go
7. ⏳ Prometheus 指标
8. ⏳ 单元测试

### P2 - 高级功能（两周后）
9. ⏳ 动态学习（基于失败请求自动调整）
10. ⏳ 格式统计和分析
11. ⏳ Admin API（查看/管理格式）
12. ⏳ 配置热更新

---

## 预期效果

### 成功率提升
- **修复前**: 拒绝所有格式异常请求
- **修复后**: 自动修复常见问题，提升成功率 20-30%

### 用户体验
- 减少 400 错误
- 更友好的错误提示（明确指出格式问题）
- 无需用户修改客户端代码

### 可观测性
- 清晰的格式分布统计
- 修复效果可量化
- 识别问题客户端

---

**文档完成时间**: 2026-07-25  
**状态**: 设计完成，待实现  
**下一步**: 实现核心组件并集成到系统
