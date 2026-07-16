# 自适应首字节超时策略设计

## 问题背景

**当前问题**：
- 固定 `firstByteTimeout = 30s`（stream_runtime.go:40）
- **小请求**（< 100KB）：30s 太长，浪费时间
- **大请求**（> 500KB）：30s 太短，导致误判超时（实际上游正在处理中）
- **慢节点**：某些 provider 天生慢（如 MiniMax 处理大上下文需 10-20s）

**从 2026-07-16 事件中的数据支持**：
| 请求大小 | 实际 TTFB | 固定超时 | 结果 |
|---|---|---|---|
| 481KB | 17.9s | 30s | ✅ 成功（但接近超时） |
| 947KB | 12.1s | 30s | ✅ 成功 |
| 953KB | 34.5s | 30s | ❌ **超时**（实际只需再等 4.5s） |

**结论**：如果超时是 40s，953KB 的请求就会成功！

## 设计目标

**根据请求特征动态调整超时：**
1. ✅ **请求体大小**：大请求延长超时
2. ✅ **供应商特性**：慢节点延长超时
3. ✅ **历史 TTFB**：基于该 credential 的历史表现调整
4. ✅ **是否重试**：重试时缩短超时（快速失败）
5. ✅ **session 请求**：session 请求延长超时（上下文更大）

## 核心设计

### 1. 自适应超时计算器

```go
// domains/streaming/timeout_adapter.go

package streaming

import (
	"time"
	"math"
)

// TimeoutAdapter 自适应超时计算器
type TimeoutAdapter struct {
	// 基础配置
	BaseTimeout          time.Duration // 基础超时（默认 30s）
	MinTimeout           time.Duration // 最小超时（默认 15s）
	MaxTimeout           time.Duration // 最大超时（默认 120s）
	
	// 大小相关
	SizeThresholdSmall   int           // 小请求阈值（100KB）
	SizeThresholdLarge   int           // 大请求阈值（500KB）
	SizeMultiplierSmall  float64       // 小请求倍数（0.5x = 15s）
	SizeMultiplierLarge  float64       // 大请求倍数（2.0x = 60s）
	
	// 供应商相关
	ProviderMultipliers  map[string]float64 // 供应商倍数
	
	// 历史性能
	HistoricalTTFB       *TTFBTracker       // 历史 TTFB 追踪器
}

// AdaptiveTimeoutInput 输入参数
type AdaptiveTimeoutInput struct {
	// 请求信息
	RequestSize      int
	IsSession        bool
	IsRetry          bool
	AttemptNum       int
	
	// Credential 信息
	CredentialID     int
	ProviderID       int
	ProviderBaseURL  string
	RawModel         string
	
	// 历史数据
	RecentTTFB       *time.Duration  // 该 credential 最近的 TTFB
}

// Calculate 计算自适应超时
func (a *TimeoutAdapter) Calculate(input AdaptiveTimeoutInput) time.Duration {
	timeout := a.BaseTimeout
	
	// ========== 1. 基于请求大小调整 ==========
	sizeMultiplier := a.calculateSizeMultiplier(input.RequestSize)
	timeout = time.Duration(float64(timeout) * sizeMultiplier)
	
	// ========== 2. 基于供应商特性调整 ==========
	providerMultiplier := a.getProviderMultiplier(input.ProviderBaseURL)
	timeout = time.Duration(float64(timeout) * providerMultiplier)
	
	// ========== 3. 基于历史 TTFB 调整 ==========
	if input.RecentTTFB != nil {
		historyMultiplier := a.calculateHistoryMultiplier(*input.RecentTTFB)
		timeout = time.Duration(float64(timeout) * historyMultiplier)
	}
	
	// ========== 4. Session 请求额外时间 ==========
	if input.IsSession {
		timeout = time.Duration(float64(timeout) * 1.5) // +50%
	}
	
	// ========== 5. 重试时缩短超时（快速失败） ==========
	if input.IsRetry {
		retryMultiplier := math.Max(0.5, 1.0 - float64(input.AttemptNum)*0.2)
		timeout = time.Duration(float64(timeout) * retryMultiplier)
	}
	
	// ========== 6. 限制在合理范围内 ==========
	if timeout < a.MinTimeout {
		timeout = a.MinTimeout
	}
	if timeout > a.MaxTimeout {
		timeout = a.MaxTimeout
	}
	
	return timeout
}

// calculateSizeMultiplier 基于请求大小计算倍数
func (a *TimeoutAdapter) calculateSizeMultiplier(requestSize int) float64 {
	if requestSize < a.SizeThresholdSmall {
		// 小请求（< 100KB）：0.5x = 15s
		return a.SizeMultiplierSmall
	} else if requestSize > a.SizeThresholdLarge {
		// 大请求（> 500KB）：2.0x = 60s
		// 线性增长：每额外 100KB +0.2x（最多到 4.0x = 120s）
		extraKB := (requestSize - a.SizeThresholdLarge) / 1024
		extra100KB := extraKB / 100
		extraMultiplier := float64(extra100KB) * 0.2
		return math.Min(a.SizeMultiplierLarge + extraMultiplier, 4.0)
	} else {
		// 中等请求（100-500KB）：1.0x = 30s
		return 1.0
	}
}

// getProviderMultiplier 获取供应商倍数
func (a *TimeoutAdapter) getProviderMultiplier(baseURL string) float64 {
	// 内置供应商倍数
	if strings.Contains(baseURL, "minimaxi.com") {
		return 1.5 // MiniMax 慢 +50%
	}
	if strings.Contains(baseURL, "claude.ai") || strings.Contains(baseURL, "anthropic.com") {
		return 1.2 // Claude 稍慢 +20%
	}
	if strings.Contains(baseURL, "openai.com") {
		return 1.0 // OpenAI 正常
	}
	if strings.Contains(baseURL, "deepseek.com") {
		return 0.8 // DeepSeek 快 -20%
	}
	
	// 检查自定义配置
	for pattern, multiplier := range a.ProviderMultipliers {
		if strings.Contains(baseURL, pattern) {
			return multiplier
		}
	}
	
	return 1.0 // 默认
}

// calculateHistoryMultiplier 基于历史 TTFB 计算倍数
func (a *TimeoutAdapter) calculateHistoryMultiplier(recentTTFB time.Duration) float64 {
	// 如果最近 TTFB 很慢，延长超时
	// 公式：multiplier = 1.0 + (recentTTFB / baseTimeout - 0.5)
	//
	// 例子：
	// - recentTTFB = 5s, baseTimeout = 30s  → 1.0 + (5/30 - 0.5) = 0.67x (缩短)
	// - recentTTFB = 15s, baseTimeout = 30s → 1.0 + (15/30 - 0.5) = 1.0x (不变)
	// - recentTTFB = 25s, baseTimeout = 30s → 1.0 + (25/30 - 0.5) = 1.33x (延长)
	
	ratio := float64(recentTTFB) / float64(a.BaseTimeout)
	multiplier := 1.0 + (ratio - 0.5)
	
	// 限制在 0.5x - 2.0x 范围内
	return math.Max(0.5, math.Min(multiplier, 2.0))
}
```

### 2. 历史 TTFB 追踪器

```go
// TTFBTracker 追踪每个 credential 的历史 TTFB
type TTFBTracker struct {
	mu    sync.RWMutex
	data  map[int]*TTFBStats // key = credential_id
}

type TTFBStats struct {
	RecentTTFB    time.Duration // 最近一次 TTFB
	AvgTTFB       time.Duration // 平均 TTFB（最近 10 次）
	P95TTFB       time.Duration // P95 TTFB
	UpdatedAt     time.Time
	SampleCount   int
}

func NewTTFBTracker() *TTFBTracker {
	return &TTFBTracker{
		data: make(map[int]*TTFBStats),
	}
}

func (t *TTFBTracker) Record(credentialID int, ttfb time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	stats, ok := t.data[credentialID]
	if !ok {
		stats = &TTFBStats{}
		t.data[credentialID] = stats
	}
	
	stats.RecentTTFB = ttfb
	stats.UpdatedAt = time.Now()
	stats.SampleCount++
	
	// 简化的滑动平均（实际应用中可以用更精确的算法）
	if stats.AvgTTFB == 0 {
		stats.AvgTTFB = ttfb
	} else {
		// 指数移动平均：新值权重 0.2
		stats.AvgTTFB = time.Duration(
			float64(stats.AvgTTFB)*0.8 + float64(ttfb)*0.2,
		)
	}
}

func (t *TTFBTracker) Get(credentialID int) *TTFBStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	
	stats, ok := t.data[credentialID]
	if !ok {
		return nil
	}
	
	// 如果数据超过 5 分钟未更新，视为过期
	if time.Since(stats.UpdatedAt) > 5*time.Minute {
		return nil
	}
	
	return stats
}
```

### 3. Executor 集成

```go
// domains/streaming/executors/executor_chat.go

func (e *Executor) executeChatRequest(...) (*ExecuteResult, error) {
	// ... 现有逻辑 ...
	
	for attemptNum, cand := range candidates {
		// 准备请求体
		bodyBytes := prepareRequestBody(params, cand)
		
		// ========== 计算自适应超时 ==========
		var recentTTFB *time.Duration
		if stats := e.TTFBTracker.Get(cand.CredentialID); stats != nil {
			recentTTFB = &stats.RecentTTFB
		}
		
		adaptiveTimeout := e.TimeoutAdapter.Calculate(AdaptiveTimeoutInput{
			RequestSize:     len(bodyBytes),
			IsSession:       params.SessionID != "",
			IsRetry:         attemptNum > 0,
			AttemptNum:      attemptNum,
			CredentialID:    cand.CredentialID,
			ProviderID:      cand.ProviderID,
			ProviderBaseURL: cand.BaseURL,
			RawModel:        cand.RawModel,
			RecentTTFB:      recentTTFB,
		})
		
		slog.Info("adaptive_timeout: calculated",
			"credential_id", cand.CredentialID,
			"request_size", len(bodyBytes),
			"is_session", params.SessionID != "",
			"is_retry", attemptNum > 0,
			"recent_ttfb_ms", func() int64 {
				if recentTTFB != nil {
					return recentTTFB.Milliseconds()
				}
				return 0
			}(),
			"timeout_ms", adaptiveTimeout.Milliseconds(),
		)
		// ========== 计算结束 ==========
		
		// 使用自适应超时创建上下文
		upCtx, upCancel := context.WithTimeout(context.Background(), adaptiveTimeout)
		defer upCancel()
		
		// 发起 HTTP 请求
		req := req.WithContext(upCtx)
		reqStart := time.Now()
		resp, err := httpClient.Do(req)
		
		// ========== 记录 TTFB ==========
		if resp != nil && resp.Body != nil {
			ttfb := time.Since(reqStart)
			e.TTFBTracker.Record(cand.CredentialID, ttfb)
		}
		// ========== 记录结束 ==========
		
		// ... 后续处理 ...
	}
}
```

### 4. 配置示例

```go
// cmd/gateway/main.go

timeoutAdapter := &streaming.TimeoutAdapter{
	BaseTimeout:         30 * time.Second,
	MinTimeout:          15 * time.Second,
	MaxTimeout:          120 * time.Second,
	
	SizeThresholdSmall:  100 * 1024,  // 100KB
	SizeThresholdLarge:  500 * 1024,  // 500KB
	SizeMultiplierSmall: 0.5,         // 小请求 15s
	SizeMultiplierLarge: 2.0,         // 大请求 60s
	
	ProviderMultipliers: map[string]float64{
		"minimaxi.com":     1.5,  // MiniMax +50%
		"anthropic.com":    1.2,  // Claude +20%
		"deepseek.com":     0.8,  // DeepSeek -20%
		// 可以从配置文件或 DB 读取
	},
	
	HistoricalTTFB: streaming.NewTTFBTracker(),
}

executor := executors.NewExecutor(executors.ExecutorConfig{
	// ... 现有配置 ...
	TimeoutAdapter: timeoutAdapter,
	TTFBTracker:    timeoutAdapter.HistoricalTTFB,
})
```

## 实际效果模拟

### 案例 1: 小请求到 OpenAI
```
请求大小: 50KB
供应商: OpenAI (1.0x)
历史 TTFB: 无
Is Session: false
Is Retry: false

计算：
  Base: 30s
  Size: 50KB < 100KB → 0.5x
  Provider: OpenAI → 1.0x
  History: 无 → 1.0x
  Session: false → 1.0x
  Retry: false → 1.0x
  
结果: 30s × 0.5 = 15s ✅
```

### 案例 2: 大请求到 MiniMax (无历史)
```
请求大小: 950KB
供应商: MiniMax (1.5x)
历史 TTFB: 无
Is Session: true
Is Retry: false

计算：
  Base: 30s
  Size: 950KB > 500KB → 2.0 + (450/100)*0.2 = 2.9x
  Provider: MiniMax → 1.5x
  History: 无 → 1.0x
  Session: true → 1.5x
  Retry: false → 1.0x
  
结果: 30s × 2.9 × 1.5 × 1.5 = 195s → 限制到 MaxTimeout 120s ✅
```

### 案例 3: 大请求到 MiniMax (有慢历史)
```
请求大小: 950KB
供应商: MiniMax (1.5x)
历史 TTFB: 25s (最近很慢)
Is Session: true
Is Retry: false

计算：
  Base: 30s
  Size: 950KB > 500KB → 2.9x
  Provider: MiniMax → 1.5x
  History: 25s → 1.0 + (25/30 - 0.5) = 1.33x
  Session: true → 1.5x
  Retry: false → 1.0x
  
结果: 30s × 2.9 × 1.5 × 1.33 × 1.5 = 260s → 限制到 120s ✅
```

### 案例 4: 重试到慢节点
```
请求大小: 200KB
供应商: MiniMax (1.5x)
历史 TTFB: 20s
Is Session: false
Is Retry: true (第 2 次尝试)
Attempt: 2

计算：
  Base: 30s
  Size: 200KB → 1.0x (中等)
  Provider: MiniMax → 1.5x
  History: 20s → 1.0 + (20/30 - 0.5) = 1.17x
  Session: false → 1.0x
  Retry: true, attempt=2 → 1.0 - 2*0.2 = 0.6x (快速失败)
  
结果: 30s × 1.0 × 1.5 × 1.17 × 0.6 = 31.6s ✅
```

## 与 2026-07-16 事件对比

### 原方案（固定 30s）
```
请求: 953KB session 到 MiniMax
实际 TTFB: 34.5s
超时: 30s
结果: ❌ 超时失败
```

### 新方案（自适应）
```
请求: 953KB session 到 MiniMax
计算超时:
  Base: 30s
  Size: 953KB → 2.9x
  Provider: MiniMax → 1.5x
  Session: true → 1.5x
  
结果超时: 30s × 2.9 × 1.5 × 1.5 = 195s → 限制到 120s
实际 TTFB: 34.5s
结果: ✅ 成功！(34.5s < 120s)
```

## 监控指标

```prometheus
# 自适应超时统计
llm_gateway_adaptive_timeout_seconds{credential_id, size_bucket, is_session, is_retry}

# 超时命中率（实际 TTFB vs 超时）
llm_gateway_timeout_hit_rate{credential_id}

# 历史 TTFB 追踪
llm_gateway_ttfb_p50_seconds{credential_id}
llm_gateway_ttfb_p95_seconds{credential_id}
```

## 配置灵活性

### 环境变量覆盖
```bash
# 基础超时
export LLM_GATEWAY_BASE_TIMEOUT=30s
export LLM_GATEWAY_MIN_TIMEOUT=15s
export LLM_GATEWAY_MAX_TIMEOUT=120s

# 大小阈值
export LLM_GATEWAY_SIZE_THRESHOLD_SMALL=100KB
export LLM_GATEWAY_SIZE_THRESHOLD_LARGE=500KB

# 供应商倍数（JSON 格式）
export LLM_GATEWAY_PROVIDER_MULTIPLIERS='{"minimaxi.com":1.5,"anthropic.com":1.2}'
```

### DB 配置表（动态更新）
```sql
CREATE TABLE timeout_config (
    provider_pattern TEXT PRIMARY KEY,
    multiplier       NUMERIC(4,2) NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO timeout_config VALUES
    ('minimaxi.com', 1.5, NOW()),
    ('anthropic.com', 1.2, NOW()),
    ('deepseek.com', 0.8, NOW());
```

## 实施计划

### Phase 1: 基础自适应（2 天）
- [ ] 实现 TimeoutAdapter
- [ ] 基于大小的动态调整
- [ ] 供应商倍数配置

### Phase 2: 历史追踪（2 天）
- [ ] 实现 TTFBTracker
- [ ] 记录每次请求的 TTFB
- [ ] 基于历史调整超时

### Phase 3: Executor 集成（1 天）
- [ ] 修改 executor_chat.go
- [ ] 替换固定超时为自适应超时
- [ ] 添加日志

### Phase 4: 监控与调优（1 天）
- [ ] 添加 Prometheus 指标
- [ ] 验证超时命中率
- [ ] 调整倍数参数

### Phase 5: 生产验证（2 天）
- [ ] 245 测试环境验证
- [ ] 154 生产灰度发布
- [ ] 监控 MiniMax 超时率变化

## 风险与缓解

### 风险 1: 超时过长导致用户等待
**缓解**：
- 设置 MaxTimeout=120s 上限
- 重试时缩短超时（快速失败）

### 风险 2: 历史 TTFB 被异常值污染
**缓解**：
- 使用指数移动平均（抗噪音）
- 5 分钟过期机制
- 可选：P95 而非平均值

### 风险 3: 配置错误导致超时过短
**缓解**：
- MinTimeout=15s 下限
- 倍数限制在 0.5x-4.0x 范围
- 详细日志记录计算过程

## 总结

**你的建议完全正确：**
1. ✅ 延长首字节超时（30s → 最高 120s）
2. ✅ 根据请求大小调整（950KB → 120s）
3. ✅ 考虑慢节点特性（MiniMax +50%）

**自适应超时 = 智能 + 灵活 + 可配置**

**如果在 2026-07-16 事件中使用此方案，953KB 请求到 MiniMax 的超时会是 120s，完全可以容纳 34.5s 的实际 TTFB，避免误判超时！**
