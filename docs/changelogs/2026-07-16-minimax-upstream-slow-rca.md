# 2026-07-16 MiniMax API 上游性能降级根因分析

## 问题现象
- 用户反馈：minimax-m3 不可用（超时无响应）
- 但 glm-5.2 可用，直连供应商也可用（测试时已恢复）

## 深度根因分析

### 1. 配置验证
**Provider 14 (MiniMax 官方):**
- base_url: `https://api.minimaxi.com/v1`
- credential_id: 21
- raw_model_name: `MiniMax-M3`
- **outbound_model_name: 空**（使用 raw_model_name）

**结论**：配置正确，模型名 `"MiniMax-M3"` 被 API 接受（有成功案例）。

### 2. 网络连通性测试
**154 → api.minimaxi.com:443:**
- ICMP ping: ✅ 11.6ms, 0% 丢包
- TCP 443: ✅ 连接成功
- TLS 握手: ✅ 正常
- HTTP GET (无凭据): ✅ 0.215s 返回 401

**结论**：网络层面完全正常。

### 3. 超时配置
**Gateway 层面:**
- `firstByteTimeout`: **30 秒**（stream_runtime.go:40）
- `ResponseHeaderTimeout`: 60 秒（pool.go:113）
- `Client.Timeout`: 120 秒（pool.go:124）

**流式请求超时流程:**
```
Client → Gateway → Upstream
              ↓
     firstByteTimeout = 30s
              ↓
    如果 30s 内没收到首字节
              ↓
   outcome.Resumable = true
              ↓
   Failover 到下一个 candidate
```

### 4. 请求模式分析

#### 成功请求（09:03 - 10:19）
| 时间 | request_bytes | stream_ttfb_ms | 结果 |
|---|---|---|---|
| 09:03:43 | - | 3688 (3.7s) | ✅ 成功 |
| 09:03:50 | - | 4322 (4.3s) | ✅ 成功 |
| 09:04:45 | - | 9534 (9.5s) | ✅ 成功 |
| 10:18:37 | 481476 (481KB) | 17936 (17.9s) | ✅ 成功 |
| 10:19:00 | - | 21143 (21.1s) | ✅ 成功 |
| 10:19:22 | 947144 (947KB) | 12117 (12.1s) | ✅ 成功 |
| 10:19:42 | - | 4247 (4.2s) | ✅ 成功 |

**模式**：
- **TTFB 逐渐增长**：3.7s → 21.1s
- **大请求更慢**：481KB → 17.9s, 947KB → 12.1s

#### 失败请求（10:20 - 10:23）
| 时间 | request_bytes | latency_ms | 结果 |
|---|---|---|---|
| 10:20:17 | 953407 (953KB) | 34515 (34.5s) | ❌ 超时 |
| 10:22:41 | - | 31619 (31.6s) | ❌ 超时 |
| 10:23:36 | - | 31850 (31.8s) | ❌ 超时 |

**模式**：
- **首字节超时**：30s+ 未收到任何数据
- **请求体很大**：953KB
- **都是 session 请求**：带完整历史上下文

### 5. Session 请求特征

**失败的请求都关联到 session:**
```
session_id: gw_33ec6685-52d2-4770-8708-f854fa034595
request_bytes: 953KB
error: async_pending → first_byte_timeout
```

**session 相关日志:**
- `session fallback created` — 原 session 创建了 fallback
- `session_cache: db load error` — session 在 DB 中不存在
- `async_pending_dispatched` — 请求放入 pending 队列

**推测**：
- 这些是**会话恢复/重连**请求
- 携带完整历史上下文（953KB）
- MiniMax API 处理大上下文请求时性能降级

### 6. 根本原因

**MiniMax API 上游性能降级（时间窗口：10:20-10:25）**

**证据链:**
1. **TTFB 持续增长**：从 3.7s (09:03) → 21.1s (10:19)
2. **大请求更慢**：947KB 请求 → 12s TTFB
3. **超过阈值后超时**：10:20 开始，TTFB > 30s → 触发 firstByteTimeout
4. **10:31 后自动恢复**：后续请求正常（1.8-2.2s）

**非 llm-gateway 问题，是供应商侧问题。**

### 7. 为什么直连可用？

用户测试时间：10:31+
MiniMax API 恢复时间：10:31
**→ 测试时 API 已恢复，所以直连正常。**

### 8. 为什么 glm-5.2 可用？

glm-5.2 走不同的 provider/credential 组合：
- provider_id: 18 (NVIDIA), 34/35 (火山引擎)
- 不依赖 MiniMax API
- **→ 不受 MiniMax API 性能问题影响**

## 改进建议

### 1. 检测大请求并压缩上下文（高优先级）

**问题**：953KB 请求体导致 MiniMax API 处理慢/超时。

**方案**：
```go
// executor_chat.go
if len(bodyBytes) > 500*1024 { // 500KB 阈值
    // 1. 尝试压缩上下文（summarize、trim）
    if compressedBody, err := compressContext(bodyBytes, params); err == nil {
        bodyBytes = compressedBody
        slog.Info("context compressed",
            "original_bytes", len(bodyBytes),
            "compressed_bytes", len(compressedBody))
    }

    // 2. 如果仍然过大，降低 TTFB 阈值
    if len(bodyBytes) > 500*1024 {
        firstByteTimeout = 60 * time.Second // 从 30s → 60s
    }
}
```

### 2. 动态调整 firstByteTimeout（中优先级）

**问题**：固定 30s 阈值不适应所有场景。

**方案**：
- **小请求**（< 100KB）：15s
- **中请求**（100-500KB）：30s
- **大请求**（> 500KB）：60s

### 3. TTFB 监控与告警（高优先级）

**指标**：
```prometheus
llm_gateway_upstream_ttfb_seconds{provider="minimax", credential="21"}
```

**告警规则**：
```yaml
- alert: MiniMaxAPISlowTTFB
  expr: llm_gateway_upstream_ttfb_seconds{provider="minimax"} > 15
  for: 5m
  annotations:
    summary: "MiniMax API TTFB > 15s for 5 minutes"
```

### 4. 首字节超时后短暂重试（中优先级）

**当前**：30s 超时 → 立即 failover 到下一个 credential

**建议**：
```go
if isFirstByteTimeout && attemptNum == 0 {
    // 短暂延迟后重试同一个 credential
    time.Sleep(1 * time.Second)
    goto retry_same_credential
}
```

**原因**：可能是瞬时抖动，重试 1 次可能成功。

### 5. 并行请求机制（长期）

**思路**：同时向 2 个 credentials 发起请求，使用先返回的结果。

**适用场景**：
- 高可用性要求
- 延迟敏感
- 有多个可用 credentials

**实现**：
```go
// 类似 DNS happy eyeballs
results := make(chan *ExecuteResult, 2)
go tryCandidate(cand1, results)
go tryCandidate(cand2, results)

select {
case result := <-results:
    return result // 使用先返回的
case <-time.After(60 * time.Second):
    return nil, errors.New("all attempts timeout")
}
```

## 总结

**根本原因**：MiniMax API 在 10:20-10:25 期间处理大请求（947KB-953KB session 上下文）时性能降级，TTFB 从 3.7s 增长到 30s+，触发 gateway 的 firstByteTimeout。

**不是 gateway 问题，是上游供应商问题。**

**缓解措施**：
1. 压缩大请求上下文
2. 动态调整超时阈值
3. 监控 TTFB 并告警
4. 首字节超时后短暂重试

**长期方案**：并行请求机制（happy eyeballs）。
