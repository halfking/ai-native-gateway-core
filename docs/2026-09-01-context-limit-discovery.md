# Context Limit Discovery & Aggressive Recovery (2026-09-01)

## 背景

当前 context-length 恢复机制存在以下问题:

1. **压缩目标不够激进**: `defaultSoftLimitFraction = 0.85`,在 4xx 恢复场景仍可能失败
2. **无法识别实际上下文限制**: 上游错误包含真实限制但未解析
3. **配置不准确无法自动修正**: 发现实际限制≠配置值时,未更新数据库

## 实际案例 (2026-09-01 245 服务器)

### 失败请求分析
- **Request ID**: `92b2304ea77f20ceb90c67912413cd3f`
- **模型**: Minimax-m3
- **配置上下文**: 可能未设置或不准确
- **实际限制**: 262144 tokens (从错误消息提取)
- **实际 tokens**: 263247 tokens (超出 1103 tokens, 仅 0.4%)
- **Body 大小**: 1022366 bytes

### 错误消息
```json
{
  "error": {
    "message": "This model's maximum context length is 262144 tokens. However, your messages resulted in 263247 tokens. Please reduce the length of the messages."
  }
}
```

### 当前压缩行为
- 其他请求显示: 1241225→1033448 bytes (压缩到 83%)
- 目标: `0.85 * contextWindow`
- **问题**: 压缩到 85% 时,对于仅超出 0.4% 的请求,压缩后可能仍接近临界值

## 改进方案

### 1. 从错误消息中提取上下文限制

在 `errorsx` 包添加解析函数:

```go
// ParseContextLimitFromError 从上游错误消息中提取实际的上下文限制
// 匹配模式:
//   - "maximum context length is 262144 tokens"
//   - "context window is 128000"
//   - "上下文长度限制为 32768 个token"
func ParseContextLimitFromError(body string) (limit int, found bool) {
    // 英文模式
    re := regexp.MustCompile(`(?i)(?:maximum\s+)?context\s+(?:window|length)\s+(?:is\s+)?(\d+)`)
    if matches := re.FindStringSubmatch(body); len(matches) > 1 {
        if val, err := strconv.Atoi(matches[1]); err == nil {
            return val, true
        }
    }
    
    // 中文模式
    reCN := regexp.MustCompile(`(?:上下文|Context)(?:长度)?限制(?:为)?[\s]*(\d+)`)
    if matches := reCN.FindStringSubmatch(body); len(matches) > 1 {
        if val, err := strconv.Atoi(matches[1]); err == nil {
            return val, true
        }
    }
    
    return 0, false
}
```

### 2. 4xx 恢复场景使用更激进的压缩目标

在 `transformation/ctx_compress.go` 添加:

```go
// aggressiveSoftLimitFraction 用于 4xx 恢复场景的激进压缩目标
// 压缩到 60% 可以:
//   1. 为响应生成留出足够空间 (max_tokens)
//   2. 避免边界附近的 token 估算误差
//   3. 为后续对话轮次预留增长空间
const aggressiveSoftLimitFraction = 0.60

// CompressMessagesAggressively 是 CompressMessagesIfNeeded 的激进版本,
// 用于 context-length 4xx 恢复场景。压缩到 contextWindow * 0.6
func CompressMessagesAggressively(bodyBytes []byte, contextWindow int) []byte {
    return compressMessagesWithTarget(bodyBytes, contextWindow, aggressiveSoftLimitFraction)
}
```

### 3. 异步更新凭据的上下文限制

在 `handleContextLengthRecovery` 中:

```go
func (e *Executor) handleContextLengthRecovery(
    ctx context.Context,
    params *ExecParams,
    targetCand provider.Candidate,
    sourceBody *[]byte,
    st *contextLengthRecoveryState,
    status int,
    errorBody string, // 新增参数
) ctxLenRecoveryAction {
    // 1. 从错误消息中提取实际限制
    if actualLimit, found := errorsx.ParseContextLimitFromError(errorBody); found {
        // 2. 与配置的限制比较
        configLimit := 0
        if targetCand.ContextWindow != nil {
            configLimit = *targetCand.ContextWindow
        }
        
        // 3. 如果差异 > 5%,异步更新数据库
        if configLimit == 0 || 
           math.Abs(float64(actualLimit-configLimit)/float64(actualLimit)) > 0.05 {
            slog.Warn("context_limit_mismatch_detected",
                "credential_id", targetCand.CredentialID,
                "model", targetCand.RawModel,
                "config_limit", configLimit,
                "actual_limit", actualLimit,
                "will_update", true,
            )
            
            // 异步更新
            if e.ContextLimitUpdater != nil {
                go e.ContextLimitUpdater.UpdateContextLimit(
                    context.Background(), // 使用 background context
                    targetCand.CredentialID,
                    targetCand.RawModel,
                    actualLimit,
                )
            }
        }
        
        // 4. 使用实际限制进行压缩
        targetCand.ContextWindow = &actualLimit
    }
    
    // 现有恢复逻辑,但使用激进目标...
}
```

### 4. ContextLimitUpdater 接口

```go
// ContextLimitUpdater 异步更新凭据的上下文限制
type ContextLimitUpdater interface {
    // UpdateContextLimit 更新指定凭据和模型的上下文限制
    // 此调用应该是异步的,不阻塞请求处理
    UpdateContextLimit(ctx context.Context, credentialID int, model string, limit int) error
}

// DB 实现
type DBContextLimitUpdater struct {
    db *sql.DB
}

func (u *DBContextLimitUpdater) UpdateContextLimit(ctx context.Context, credentialID int, model string, limit int) error {
    // 使用 ON CONFLICT 更新或插入
    _, err := u.db.ExecContext(ctx, `
        INSERT INTO credential_model_overrides (credential_id, model, context_window, discovered_at)
        VALUES ($1, $2, $3, NOW())
        ON CONFLICT (credential_id, model) 
        DO UPDATE SET 
            context_window = EXCLUDED.context_window,
            discovered_at = NOW(),
            discovery_count = credential_model_overrides.discovery_count + 1
    `, credentialID, model, limit)
    
    if err != nil {
        slog.Error("failed to update context limit",
            "credential_id", credentialID,
            "model", model,
            "limit", limit,
            "error", err,
        )
    } else {
        slog.Info("context_limit_updated",
            "credential_id", credentialID,
            "model", model,
            "limit", limit,
        )
    }
    
    return err
}
```

## 实施优先级

### P0 (立即)
1. ✅ 修复重试预算消耗 bug (已完成)
2. ✅ 添加 `ParseContextLimitFromError` 函数 (已完成)
3. ✅ 在恢复中使用解析的实际限制 (已完成)

### P1 (本周)
1. ✅ 实现 `CompressMessagesAggressively` (60% 目标) (已完成)
2. ✅ 在 4xx 恢复场景使用激进压缩 (已完成)
3. ✅ 添加压缩前后 token 估算日志 (已完成)

### P2 (已完成)
1. ✅ 实现 `ContextLimitUpdater` 接口 (已完成, `internal/dbx/context_limit_updater.go`)
2. ✅ 在 Executor 中注入 updater 实例 (已完成, `domains/streaming/executors/executor.go`)
3. ✅ 添加 `context_limit_discovery_total` 指标 (已完成, Prometheus counter with labels)
4. ✅ 异步写库逻辑 (已完成, fire-and-forget goroutine with 10s timeout)

**注意**: P2 不需要创建新表。Migration 523 已经在 `credential_model_bindings` 上添加了 
`context_window_override` / `context_window_source` / `context_window_updated_at` 列,
优先级链为: `credential_model_bindings.context_window_override` → 
`models_canonical.context_window_override` → `models_canonical.context_window`。
发现的限制会写入凭据×模型级覆盖,粒度正确。Migration 524 已添加 NOTIFY 触发器,
更新会自动扇出到所有网关实例的缓存。

## 预期效果

### 修复前
- 超出 0.4% 的请求: 压缩到 85% 后仍可能失败
- 配置不准: 每次都要尝试才知道真实限制
- 手动修正: 需要人工更新数据库

### 修复后
- 超出 40% 以内的请求: 压缩到 60% 后有充足余量
- 自动发现: 从错误中提取真实限制并记录
- 自动修正: 异步更新配置,下次直接使用正确值

## 测试计划

1. **单元测试**
   - `ParseContextLimitFromError` 各种错误格式
   - `CompressMessagesAggressively` 压缩比例
   
2. **集成测试**
   - 模拟 context-length 错误,验证提取和更新
   - 验证压缩后 token 在 60% 以下

3. **回归测试**
   - 使用 2026-09-01 失败案例重放
   - 预期: 第一次提取 262144,压缩到 157286 tokens (60%)
