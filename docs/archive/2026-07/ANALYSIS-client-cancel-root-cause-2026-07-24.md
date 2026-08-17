# 深度分析：为什么客户端会"自动取消"请求

## 背景

**问题现象**：
- VSCode Copilot 连接 llm.kxpms.cn 后，发送请求立即停止
- 日志显示 `probe-client_cancel-cred21-xxx` 错误
- **关键疑点**：用户没有手动取消操作，但系统检测到 `context.Canceled`

## ✅ 实际服务器配置（已确认）

**查看实际配置**：`cmd/gateway/main.go:3945`

```go
srv := &http.Server{
    Addr:    cfg.Listen,
    Handler: finalHandler,
    ReadHeaderTimeout: 10 * time.Second,   // 仅读取 HTTP 头部超时
    ReadTimeout:       300 * time.Second,  // 5分钟 - 读取完整请求超时
    WriteTimeout:      0,                  // 无限制！
    IdleTimeout:       300 * time.Second,  // 5分钟 - 空闲连接超时
    MaxHeaderBytes:    1 << 20,
}
```

**重要发现**：
- ✅ `ReadTimeout: 300s` - 足够长，不太可能是读取超时
- ✅ `WriteTimeout: 0` - **无限制**，意味着服务器不会主动因为写入超时而关闭连接
- ⚠️ **这意味着 `client_cancel` 不是服务器主动超时导致的！**

## 可能的根本原因分析（按概率排序）

### 1. 负载均衡器/反向代理超时 🌐 ⭐⭐⭐⭐⭐

**最可能的原因**：VSCode → Nginx/ALB → llm.kxpms.cn

由于服务器 `WriteTimeout=0`（无限制），但仍然出现 `client_cancel`，**最大可能是中间的反向代理超时**。

**触发场景**：
- `ReadTimeout`：如果客户端发送请求体过慢（大文件、网络慢），超过 30s 会触发
- `WriteTimeout`：如果上游响应过慢，超过 90s 会触发服务器主动关闭连接
- **结果**：服务器关闭连接 → `context.Canceled` → 记录为 `client_cancel`

#### 验证方法：
1. 检查请求的 `latency_ms` 字段
2. 如果接近 30000ms（ReadTimeout）或 90000ms（WriteTimeout），说明是服务器超时
3. 查看请求体大小，判断是否因为读取慢导致

---

### 2. 负载均衡器/反向代理超时 🌐

**场景**：VSCode → 负载均衡器 → llm.kxpms.cn

#### 常见的超时设置：
- **Nginx**：`proxy_read_timeout 60s;` `proxy_send_timeout 60s;`
- **ALB/CLB**：默认 60 秒超时
- **Cloudflare**：免费版 100 秒，Pro 版 600 秒

**触发场景**：
- 如果 LLM 模型思考时间过长（特别是 o1/reasoning 模型）
- 如果流式响应间隔超过代理超时时间
- **结果**：代理关闭连接 → Gateway 检测到 `context.Canceled`

#### 验证方法：
```bash
# 检查是否有反向代理
curl -I https://llm.kxpms.cn/v1/chat/completions

# 查看响应头中的 Server/X-Powered-By 字段
# 如果有 nginx/cloudflare 等，可能受代理超时影响
```

---

### 3. VSCode/Copilot 客户端超时 💻

**Copilot 客户端特性**：
- VSCode 扩展有内置超时机制
- 如果响应时间过长，客户端主动取消请求
- 典型超时：30-60 秒

**触发场景**：
- Copilot 发送请求后，等待超过客户端超时时间
- 客户端主动关闭 HTTP 连接
- **结果**：服务端检测到连接关闭 → `context.Canceled`

#### 特征：
- `latency_ms` 会显示一个固定的时间（如 30000ms、60000ms）
- 请求可能还在处理中，但客户端已经放弃等待

---

### 4. 网络连接中断 📡

**网络层问题**：
- TCP 连接异常关闭（网络波动、防火墙）
- 客户端网络切换（WiFi → 移动网络）
- 中间设备重启或故障

**触发场景**：
- 长时间请求过程中网络不稳定
- **结果**：TCP FIN/RST → `context.Canceled`

#### 特征：
- `latency_ms` 不规律，可能在任何时间点
- 日志中可能有 "connection reset by peer" 或 "broken pipe" 错误

---

### 5. 上游模型响应慢 🐌

**场景**：选中的 credential（cred21）对应的模型响应极慢

**触发链**：
```
请求发送 → 路由到 cred21 → 上游响应慢 → 
超过某个超时阈值 → context.Canceled
```

#### 检查点：
- 查看 cred21 的历史延迟（`latency_ms` 平均值）
- 检查该凭据的成功率（`success_rate`）
- 查看是否有其他请求也在 cred21 上超时

---

### 6. 流式响应首包超时 📦

**代码路径**：`domains/streaming/stream.go`

```go
for {
    select {
    case <-ctx.Done():
        // 客户端或服务器超时 → 标记为 client_cancel
        outcome.Interrupted = true
        outcome.Reason = "client_cancel"
        return outcome
    default:
    }
    
    readResult := readNextStreamLine(ctx, reader, bodyCloser, w, &lastSend, runtimeCfg)
    // ...
}
```

**触发场景**：
- 流式请求发送后，等待首个 chunk
- 如果上游迟迟不返回第一个 chunk，触发超时
- **结果**：`stream_first_chunk_timeout` → 但可能被记录为 `client_cancel`

---

## 🔍 如何诊断具体原因

### 步骤 1：查看完整日志（修复后可用）

```sql
SELECT 
    request_id,
    client_model,
    credential_id,
    latency_ms,
    request_preview,
    upstream_status_code,
    client_timeout,
    error_kind
FROM request_logs 
WHERE request_id = 'probe-client_cancel-cred21-1784899122602079383';
```

**关键字段分析**：
- `latency_ms`：
  - ~30000ms → 可能是服务器 ReadTimeout
  - ~60000ms → 可能是 Nginx/负载均衡器超时
  - ~90000ms → 可能是服务器 WriteTimeout
- `request_preview`：检查请求大小（`total_content_length`）
- `upstream_status_code`：如果为 NULL，说明根本没到上游

### 步骤 2：检查相同 credential 的其他请求

```sql
-- 查看 cred21 的最近请求情况
SELECT 
    COUNT(*) as total,
    SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count,
    AVG(latency_ms) as avg_latency,
    MAX(latency_ms) as max_latency,
    COUNT(CASE WHEN error_kind = 'client_cancel' THEN 1 END) as cancel_count
FROM request_logs 
WHERE credential_id = 21 
  AND event_at > NOW() - INTERVAL '1 hour'
GROUP BY credential_id;
```

**判断标准**：
- 如果 `cancel_count` 很高 → cred21 可能有问题（响应慢）
- 如果 `avg_latency` > 30000ms → 该凭据普遍超时
- 如果只有个别请求 cancel → 可能是网络或客户端问题

### 步骤 3：复现测试

```bash
# 使用 curl 模拟 Copilot 请求
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }' \
  -w "\nTotal time: %{time_total}s\n"
```

**观察**：
- 如果请求在 30s/60s/90s 准时超时 → 配置的超时问题
- 如果请求能正常完成 → 可能是 Copilot 客户端问题
- 如果请求卡住不返回 → 上游模型问题

---

## 🛠️ 可能的解决方案

### 方案 1：调整服务器超时配置

```go
// cmd/gateway/main.go
server := &http.Server{
    ReadTimeout:    60 * time.Second,  // 增加到 60s
    WriteTimeout:   180 * time.Second, // 增加到 180s (3分钟)
    IdleTimeout:    120 * time.Second,
}
```

**适用场景**：如果是服务器超时导致

### 方案 2：配置反向代理超时

```nginx
# nginx.conf
location /v1/ {
    proxy_pass http://backend;
    proxy_read_timeout 180s;    # 增加到 180s
    proxy_send_timeout 180s;
    proxy_connect_timeout 10s;
}
```

**适用场景**：如果使用了 Nginx/ALB 等代理

### 方案 3：实现请求重试机制

```go
// 在客户端检测到 client_cancel 后，自动重试到其他凭据
if outcome.Reason == "client_cancel" && retryCount < maxRetries {
    // 换一个更快的 credential 重试
    nextCredential := selectFasterCredential()
    return retry(nextCredential)
}
```

**适用场景**：如果是特定凭据响应慢导致

### 方案 4：优化路由策略

```sql
-- 将响应慢的凭据降低优先级
UPDATE credential_model_bindings 
SET manual_priority = 99,  -- 降到最低优先级
    routing_tier = 3        -- 或作为备用层
WHERE credential_id = 21 
  AND raw_model_name = 'glm-5.2';
```

**适用场景**：如果 cred21 普遍慢，影响用户体验

### 方案 5：添加客户端提示

```json
// 在响应中添加预估时间提示
{
  "error": {
    "message": "Request processing, estimated time: 45s",
    "type": "processing",
    "code": "still_processing"
  }
}
```

**适用场景**：帮助客户端了解处理进度，避免提前取消

---

## 📊 监控和告警建议

### 1. 添加超时类型区分

```go
// 区分不同类型的 cancel
if errors.Is(ctxErr, context.DeadlineExceeded) {
    errorKind = "server_timeout"  // 服务器主动超时
} else if errors.Is(ctxErr, context.Canceled) {
    // 进一步区分是客户端主动取消还是服务器关闭
    if /* 检查是否有写入过数据 */ {
        errorKind = "client_cancel_mid_stream"
    } else {
        errorKind = "client_cancel_before_response"
    }
}
```

### 2. 设置告警阈值

```sql
-- 每分钟 client_cancel 超过 10 次触发告警
SELECT COUNT(*) as cancel_count
FROM request_logs 
WHERE error_kind = 'client_cancel'
  AND event_at > NOW() - INTERVAL '1 minute'
HAVING cancel_count > 10;
```

### 3. 记录更详细的上下文

```go
// 在 buildClientDisconnectProbeEntry 中添加：
entry.FailureDetailCode = determineFailureDetail(logCtx)

func determineFailureDetail(logCtx *RequestLogContext) string {
    latency := time.Since(logCtx.StartTime).Seconds()
    
    if latency >= 30 && latency < 31 {
        return "likely_read_timeout_30s"
    } else if latency >= 60 && latency < 61 {
        return "likely_proxy_timeout_60s"
    } else if latency >= 90 && latency < 91 {
        return "likely_write_timeout_90s"
    }
    
    return "unknown_cancel_reason"
}
```

---

## 🎯 下一步行动

1. **立即执行**：
   - ✅ 部署修复后的日志记录（已完成）
   - 🔄 等待下一次 Copilot 请求，收集完整日志
   - 🔍 分析 `request_preview` 中的 `message_count` 和 `total_content_length`

2. **短期（1-2天）**：
   - 查询 cred21 的历史表现，判断是否需要调整优先级
   - 检查服务器和代理的超时配置
   - 监控 `client_cancel` 频率和模式

3. **中期（1周）**：
   - 根据数据决定是否调整超时配置
   - 实现更细粒度的超时类型区分
   - 添加自动重试机制

4. **长期（持续）**：
   - 建立完善的超时监控和告警体系
   - 定期分析凭据性能，自动调整路由策略
   - 优化上游响应速度

---

**文档创建时间**：2026-07-24  
**状态**：待验证，需要实际日志数据确认根本原因
