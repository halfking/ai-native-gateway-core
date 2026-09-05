# 请求发送前强制校验 Hook 设计

## 问题背景

从 2026-07-16 MiniMax 超时事件中发现的潜在问题：

1. **上下文过大**：947KB-953KB 请求体，可能超出供应商限制或导致处理慢
2. **格式完整性**：无法确认发给上游的请求格式是否符合 OpenAI API 规范
3. **关键字段缺失**：无法确认 model、messages 等必需字段是否存在
4. **累积错误**：可能存在内存泄漏、连接泄漏导致的错误积累

## 设计目标

**在发送给上游 API 之前，强制校验请求：**
1. ✅ 格式正确（JSON 有效、符合 OpenAI API schema）
2. ✅ 关键字段不缺（model, messages, etc.）
3. ✅ 大小合理（不超过供应商限制）
4. ✅ 上下文压缩（如果过大，自动触发压缩）
5. ✅ 资源健康（检测连接池、内存状态）

## 核心设计

### Hook 接口

```go
// domains/streaming/executors/pre_request_validator.go

package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PreRequestValidator 请求发送前的强制校验器
type PreRequestValidator interface {
	// Validate 校验请求，返回错误或修正后的请求体
	Validate(ctx context.Context, req *PreRequestValidationInput) (*PreRequestValidationOutput, error)
}

// PreRequestValidationInput 校验输入
type PreRequestValidationInput struct {
	// 请求基本信息
	CredentialID int
	ProviderID   int
	RawModel     string
	BaseURL      string

	// 请求体
	RequestBody  []byte

	// 上下文信息
	RequestID    string
	SessionID    string
	IsRetry      bool
	AttemptNum   int
}

// PreRequestValidationOutput 校验输出
type PreRequestValidationOutput struct {
	// 是否需要修正
	Modified     bool

	// 修正后的请求体（如果 Modified=true）
	RequestBody  []byte

	// 校验结果
	Valid        bool
	Errors       []ValidationError
	Warnings     []ValidationWarning

	// 统计信息
	OriginalSize int
	FinalSize    int
	Compressed   bool

	// 资源健康
	PoolHealthy  bool
	MemoryHealthy bool
}

type ValidationError struct {
	Field   string
	Code    string
	Message string
}

type ValidationWarning struct {
	Code    string
	Message string
}

// DefaultPreRequestValidator 默认实现
type DefaultPreRequestValidator struct {
	// 配置
	MaxRequestSize       int           // 最大请求体大小（默认 1MB）
	WarnRequestSize      int           // 警告阈值（默认 500KB）
	AutoCompress         bool          // 自动压缩上下文
	StrictValidation     bool          // 严格模式（格式错误直接拒绝）

	// 依赖
	ContextCompressor    ContextCompressor
	ResourceMonitor      ResourceMonitor
}

func (v *DefaultPreRequestValidator) Validate(ctx context.Context, input *PreRequestValidationInput) (*PreRequestValidationOutput, error) {
	output := &PreRequestValidationOutput{
		OriginalSize: len(input.RequestBody),
		FinalSize:    len(input.RequestBody),
	}

	// ========== 1. JSON 格式校验 ==========
	var reqBody map[string]interface{}
	if err := json.Unmarshal(input.RequestBody, &reqBody); err != nil {
		output.Valid = false
		output.Errors = append(output.Errors, ValidationError{
			Field:   "body",
			Code:    "invalid_json",
			Message: fmt.Sprintf("invalid JSON: %v", err),
		})
		return output, fmt.Errorf("invalid JSON: %w", err)
	}

	// ========== 2. 必需字段校验 ==========
	errors := v.validateRequiredFields(reqBody)
	output.Errors = append(output.Errors, errors...)

	if len(errors) > 0 && v.StrictValidation {
		output.Valid = false
		return output, fmt.Errorf("missing required fields: %v", errors)
	}

	// ========== 3. 大小检查 ==========
	if len(input.RequestBody) > v.MaxRequestSize {
		output.Errors = append(output.Errors, ValidationError{
			Field:   "body",
			Code:    "too_large",
			Message: fmt.Sprintf("request body too large: %d bytes (max %d)", len(input.RequestBody), v.MaxRequestSize),
		})

		// 尝试自动压缩
		if v.AutoCompress && v.ContextCompressor != nil {
			compressed, err := v.ContextCompressor.Compress(ctx, reqBody)
			if err != nil {
				return output, fmt.Errorf("compression failed: %w", err)
			}
			compressedBody, _ := json.Marshal(compressed)
			output.Modified = true
			output.RequestBody = compressedBody
			output.FinalSize = len(compressedBody)
			output.Compressed = true

			slog.Info("pre_request_validator: auto-compressed",
				"original_bytes", len(input.RequestBody),
				"compressed_bytes", len(compressedBody),
				"ratio", float64(len(compressedBody))/float64(len(input.RequestBody)),
				"credential_id", input.CredentialID,
			)
		} else {
			return output, fmt.Errorf("request too large and auto-compress disabled")
		}
	} else if len(input.RequestBody) > v.WarnRequestSize {
		output.Warnings = append(output.Warnings, ValidationWarning{
			Code:    "large_request",
			Message: fmt.Sprintf("request body large: %d bytes (warn threshold %d)", len(input.RequestBody), v.WarnRequestSize),
		})
	}

	// ========== 4. 资源健康检查 ==========
	if v.ResourceMonitor != nil {
		health := v.ResourceMonitor.Check()
		output.PoolHealthy = health.PoolHealthy
		output.MemoryHealthy = health.MemoryHealthy

		if !health.PoolHealthy {
			output.Warnings = append(output.Warnings, ValidationWarning{
				Code:    "pool_unhealthy",
				Message: fmt.Sprintf("connection pool unhealthy: %s", health.PoolMessage),
			})
		}

		if !health.MemoryHealthy {
			output.Warnings = append(output.Warnings, ValidationWarning{
				Code:    "memory_pressure",
				Message: fmt.Sprintf("memory pressure detected: %s", health.MemoryMessage),
			})
		}
	}

	// ========== 5. 特定供应商规则 ==========
	providerErrors := v.validateProviderSpecific(input.BaseURL, reqBody)
	output.Errors = append(output.Errors, providerErrors...)

	output.Valid = len(output.Errors) == 0
	return output, nil
}

func (v *DefaultPreRequestValidator) validateRequiredFields(body map[string]interface{}) []ValidationError {
	var errors []ValidationError

	// 1. model 字段
	if model, ok := body["model"]; !ok || model == "" {
		errors = append(errors, ValidationError{
			Field:   "model",
			Code:    "missing_required_field",
			Message: "model field is required",
		})
	}

	// 2. messages 字段
	messages, ok := body["messages"]
	if !ok {
		errors = append(errors, ValidationError{
			Field:   "messages",
			Code:    "missing_required_field",
			Message: "messages field is required",
		})
	} else {
		// 检查 messages 是否为数组
		messagesArray, ok := messages.([]interface{})
		if !ok {
			errors = append(errors, ValidationError{
				Field:   "messages",
				Code:    "invalid_type",
				Message: "messages must be an array",
			})
		} else if len(messagesArray) == 0 {
			errors = append(errors, ValidationError{
				Field:   "messages",
				Code:    "empty_array",
				Message: "messages array cannot be empty",
			})
		} else {
			// 检查每个 message 的格式
			for i, msg := range messagesArray {
				msgMap, ok := msg.(map[string]interface{})
				if !ok {
					continue
				}

				// 检查 role 和 content
				if _, ok := msgMap["role"]; !ok {
					errors = append(errors, ValidationError{
						Field:   fmt.Sprintf("messages[%d].role", i),
						Code:    "missing_required_field",
						Message: "message role is required",
					})
				}
				if _, ok := msgMap["content"]; !ok {
					errors = append(errors, ValidationError{
						Field:   fmt.Sprintf("messages[%d].content", i),
						Code:    "missing_required_field",
						Message: "message content is required",
					})
				}
			}
		}
	}

	return errors
}

func (v *DefaultPreRequestValidator) validateProviderSpecific(baseURL string, body map[string]interface{}) []ValidationError {
	var errors []ValidationError

	// MiniMax 特定规则
	if strings.Contains(baseURL, "minimaxi.com") {
		// 检查模型名格式
		if model, ok := body["model"].(string); ok {
			// MiniMax 可能不支持某些参数
			if _, ok := body["response_format"]; ok {
				errors = append(errors, ValidationError{
					Field:   "response_format",
					Code:    "unsupported_parameter",
					Message: "MiniMax may not support response_format parameter",
				})
			}

			// 检查上下文长度
			if messages, ok := body["messages"].([]interface{}); ok {
				totalTokens := estimateTokens(messages)
				if totalTokens > 200000 { // MiniMax-M3 的上下文限制
					errors = append(errors, ValidationError{
						Field:   "messages",
						Code:    "context_too_long",
						Message: fmt.Sprintf("estimated tokens %d exceed MiniMax-M3 limit 200k", totalTokens),
					})
				}
			}
		}
	}

	return errors
}

// estimateTokens 粗略估算 token 数量（1 token ≈ 4 字符）
func estimateTokens(messages []interface{}) int {
	totalChars := 0
	for _, msg := range messages {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			if content, ok := msgMap["content"].(string); ok {
				totalChars += len(content)
			}
		}
	}
	return totalChars / 4
}
```

### ContextCompressor 接口

```go
// ContextCompressor 上下文压缩器
type ContextCompressor interface {
	Compress(ctx context.Context, reqBody map[string]interface{}) (map[string]interface{}, error)
}

// DefaultContextCompressor 默认实现
type DefaultContextCompressor struct {
	// 压缩策略
	SummarizeThreshold int  // 多少 messages 后开始 summarize
	TrimOldMessages    bool // 是否裁剪旧消息
	KeepRecentCount    int  // 保留最近 N 条消息
}

func (c *DefaultContextCompressor) Compress(ctx context.Context, reqBody map[string]interface{}) (map[string]interface{}, error) {
	messages, ok := reqBody["messages"].([]interface{})
	if !ok || len(messages) <= c.SummarizeThreshold {
		return reqBody, nil // 不需要压缩
	}

	compressed := make(map[string]interface{})
	for k, v := range reqBody {
		compressed[k] = v
	}

	// 策略 1: 裁剪旧消息
	if c.TrimOldMessages && len(messages) > c.KeepRecentCount {
		// 保留 system message（如果有）
		var systemMsg interface{}
		if msg, ok := messages[0].(map[string]interface{}); ok {
			if role, ok := msg["role"].(string); ok && role == "system" {
				systemMsg = messages[0]
			}
		}

		// 保留最近 N 条
		recentMessages := messages[len(messages)-c.KeepRecentCount:]

		var finalMessages []interface{}
		if systemMsg != nil {
			finalMessages = append(finalMessages, systemMsg)
		}
		finalMessages = append(finalMessages, recentMessages...)

		compressed["messages"] = finalMessages

		slog.Info("context_compressor: trimmed old messages",
			"original_count", len(messages),
			"final_count", len(finalMessages),
		)
	}

	// 策略 2: TODO - 使用 LLM summarize 中间对话

	return compressed, nil
}
```

### ResourceMonitor 接口

```go
// ResourceMonitor 资源健康监控
type ResourceMonitor interface {
	Check() ResourceHealth
}

type ResourceHealth struct {
	PoolHealthy    bool
	PoolMessage    string
	MemoryHealthy  bool
	MemoryMessage  string
}

// DefaultResourceMonitor 默认实现
type DefaultResourceMonitor struct {
	Pools         *pool.Registry
	MemoryLimit   uint64 // 内存告警阈值（字节）
}

func (m *DefaultResourceMonitor) Check() ResourceHealth {
	health := ResourceHealth{
		PoolHealthy:   true,
		MemoryHealthy: true,
	}

	// 1. 检查连接池
	if m.Pools != nil {
		stats := m.Pools.Stats()
		if stats.TotalDead > 0 {
			health.PoolHealthy = false
			health.PoolMessage = fmt.Sprintf("%d dead pools detected", stats.TotalDead)
		}
	}

	// 2. 检查内存
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	if memStats.Alloc > m.MemoryLimit {
		health.MemoryHealthy = false
		health.MemoryMessage = fmt.Sprintf("memory usage %d MB exceeds limit %d MB",
			memStats.Alloc/1024/1024,
			m.MemoryLimit/1024/1024)
	}

	return health
}
```

## Executor 集成

```go
// executor_chat.go

func (e *Executor) executeChatRequest(...) (*ExecuteResult, error) {
	// ... 现有逻辑 ...

	for attemptNum, cand := range candidates {
		// 准备请求体
		bodyBytes := prepareRequestBody(params, cand)

		// ========== 强制校验 ==========
		if e.PreRequestValidator != nil {
			validationInput := &PreRequestValidationInput{
				CredentialID: cand.CredentialID,
				ProviderID:   cand.ProviderID,
				RawModel:     cand.RawModel,
				BaseURL:      cand.BaseURL,
				RequestBody:  bodyBytes,
				RequestID:    params.RequestID,
				SessionID:    params.SessionID,
				IsRetry:      attemptNum > 0,
				AttemptNum:   attemptNum,
			}

			validationOutput, err := e.PreRequestValidator.Validate(params.R.Context(), validationInput)
			if err != nil {
				slog.Error("pre_request_validator: validation failed",
					"error", err,
					"credential_id", cand.CredentialID,
					"errors", validationOutput.Errors,
				)

				// 记录到 credential state
				e.recordValidationFailure(cand.CredentialID, validationOutput.Errors)

				// 尝试下一个 candidate
				continue
			}

			// 使用修正后的请求体（如果有压缩）
			if validationOutput.Modified {
				bodyBytes = validationOutput.RequestBody
				slog.Info("pre_request_validator: using modified request",
					"original_size", validationOutput.OriginalSize,
					"final_size", validationOutput.FinalSize,
					"compressed", validationOutput.Compressed,
				)
			}

			// 记录警告
			for _, warn := range validationOutput.Warnings {
				slog.Warn("pre_request_validator: warning",
					"code", warn.Code,
					"message", warn.Message,
					"credential_id", cand.CredentialID,
				)
			}
		}
		// ========== 校验结束 ==========

		// 发起 HTTP 请求
		resp, err := httpClient.Do(req)

		// ... 后续处理 ...
	}
}
```

## 配置

```go
// cmd/gateway/main.go

validator := &executors.DefaultPreRequestValidator{
	MaxRequestSize:    1 * 1024 * 1024,  // 1MB
	WarnRequestSize:   500 * 1024,       // 500KB
	AutoCompress:      true,
	StrictValidation:  false,

	ContextCompressor: &executors.DefaultContextCompressor{
		SummarizeThreshold: 20,  // 超过 20 条 messages 开始压缩
		TrimOldMessages:    true,
		KeepRecentCount:    10,  // 保留最近 10 条
	},

	ResourceMonitor: &executors.DefaultResourceMonitor{
		Pools:       poolRegistry,
		MemoryLimit: 800 * 1024 * 1024, // 800MB 告警
	},
}

executor := executors.NewExecutor(executors.ExecutorConfig{
	// ... 现有配置 ...
	PreRequestValidator: validator,
})
```

## 监控指标

```prometheus
# 校验失败次数
llm_gateway_pre_request_validation_errors_total{credential_id, error_code}

# 自动压缩次数
llm_gateway_context_compression_total{credential_id, reason}

# 压缩效果
llm_gateway_context_compression_ratio{credential_id}

# 资源健康告警
llm_gateway_resource_unhealthy_total{type="pool"|"memory"}
```

## 实施计划

### Phase 1: 基础校验（2 天）
- [ ] 实现 PreRequestValidator 接口
- [ ] JSON 格式 + 必需字段校验
- [ ] 大小检查 + 告警

### Phase 2: 上下文压缩（2 天）
- [ ] 实现 ContextCompressor 接口
- [ ] 裁剪旧消息策略
- [ ] 自动触发压缩逻辑

### Phase 3: 资源监控（1 天）
- [ ] 实现 ResourceMonitor 接口
- [ ] 连接池健康检查
- [ ] 内存压力检测

### Phase 4: 供应商特定规则（1 天）
- [ ] MiniMax 规则（token 限制、不支持参数）
- [ ] 其他供应商规则

### Phase 5: 集成与测试（2 天）
- [ ] Executor 集成
- [ ] 单元测试
- [ ] 245 测试环境验证

## 总结

**这个 Hook 解决了你提出的所有问题：**

1. ✅ **格式强制检查**：JSON 有效性、必需字段
2. ✅ **关键数据不缺**：model、messages、role、content
3. ✅ **上下文过大检测**：自动压缩（trim 或 summarize）
4. ✅ **资源健康监控**：连接池、内存泄漏检测
5. ✅ **供应商特定规则**：MiniMax token 限制、不支持参数

**与 Post-Execution Hook 配合，形成完整的请求生命周期管理。**
