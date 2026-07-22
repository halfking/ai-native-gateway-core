# 超时与重试优化设计方案

## 📊 问题分析

### 当前状态（154服务器过去2小时数据）

**延迟分布：**
- 总请求数：514
- 平均延迟：11.6秒
- <10秒：77%（400个）✅
- 10-30秒：10%（55个）⚠️
- 30-60秒：7%（39个）❌
- 60-90秒：1%（9个）❌
- >90秒：2%（11个）❌

**核心问题：**
1. **当前超时配置过短**：`LLM_GATEWAY_UPSTREAM_TIMEOUT=30秒`
2. **13%的请求（59+9+11=79个）超过30秒** → 触发 `stream_timeout`
3. **客户端收到 "upstream read timeout" 错误**，但LLM实际仍在返回数据
4. **Token浪费**：已消耗的prompt tokens无法利用

### 根本原因

```
客户端请求 → 网关(30s超时) → LLM供应商(实际60s响应)
                   ↓ 30秒后
              stream_timeout ❌
              熔断器打开
              客户端收到错误
                   ↓ 但是...
              LLM继续返回数据(被丢弃) 💸
```

---

## 🎯 设计目标

### 1. 客户端体验优化
- ✅ 长时间等待时发送 keepalive 消息，避免客户端超时
- ✅ 节点故障自动切换时通知客户端（不计入上下文）
- ✅ 检测"继续/重试"类提示词，智能复用已有响应

### 2. Token利用率提升
- ✅ 动态超时：根据上下文大小和网络延迟调整
- ✅ 响应缓存：网关收到但客户端断开的响应可复用
- ✅ 智能重试：避免重复消耗相同prompt的tokens

### 3. 系统健壮性增强
- ✅ 多节点故障转移
- ✅ 配置热更新
- ✅ 分级重试策略

---

## 🏗️ 架构设计

### 整体流程图

```
┌─────────────────────────────────────────────────────────────┐
│                     客户端请求                                 │
└───────────────────────┬─────────────────────────────────────┘
                        ↓
        ┌───────────────────────────────────┐
        │   请求预处理 & 会话检查            │
        │  - 提取 session_id                │
        │  - 检测"继续/重试"提示词           │
        │  - 查找上次请求状态                │
        └───────────┬───────────────────────┘
                    ↓
        ┌───────────────────────────────────┐
        │  智能路由决策                       │
        │  ├─ 情况1: 上次成功但客户端断开     │
        │  │   → 直接返回缓存响应             │
        │  ├─ 情况2: 上次失败/超时            │
        │  │   → 重新路由到可用节点           │
        │  └─ 情况3: 新请求                   │
        │      → 正常路由                     │
        └───────────┬───────────────────────┘
                    ↓
        ┌───────────────────────────────────┐
        │   动态超时计算                      │
        │  baseTimeout = 30s                │
        │  + contextBonus(>20K → +30s)      │
        │  + networkLatencyBonus            │
        │  = effectiveTimeout               │
        └───────────┬───────────────────────┘
                    ↓
        ┌───────────────────────────────────┐
        │   LLM节点请求 (带重试)             │
        │  ├─ 记录开始时间                   │
        │  ├─ 定时发送 keepalive (每10s)     │
        │  ├─ 超时 → 下一节点 (发送切换通知)  │
        │  └─ 最后节点 → 等待10s再重试       │
        └───────────┬───────────────────────┘
                    ↓
        ┌───────────────────────────────────┐
        │   响应处理 & 缓存                  │
        │  ├─ 客户端在线 → 实时流式返回       │
        │  ├─ 客户端断开 → 缓存完整响应       │
        │  └─ 记录请求状态 (成功/失败/超时)   │
        └───────────────────────────────────┘
```

---

## 📐 核心功能设计

### 功能1：动态超时计算

#### 数据库Schema扩展

```sql
-- 新增配置表
CREATE TABLE IF NOT EXISTS system_settings (
    id SERIAL PRIMARY KEY,
    key VARCHAR(255) NOT NULL UNIQUE,
    value JSONB NOT NULL,
    description TEXT,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    updated_by VARCHAR(100)
);

-- 超时配置示例
INSERT INTO system_settings (key, value, description) VALUES
('timeout.client_default_seconds', '30', '客户端默认超时(秒)'),
('timeout.upstream_base_seconds', '30', 'LLM节点基础超时(秒)'),
('timeout.upstream_min_seconds', '10', 'LLM节点最小超时(秒)'),
('timeout.upstream_max_seconds', '300', 'LLM节点最大超时(秒)'),
('timeout.context_threshold_tokens', '20000', '上下文增加超时的阈值(tokens)'),
('timeout.context_bonus_seconds', '30', '超过阈值时增加的超时(秒)'),
('timeout.dynamic_mode', '"context_aware"', '动态调整模式: static/context_aware/network_aware/adaptive'),
('retry.max_attempts', '2', '最大重试次数(0-5)'),
('retry.keepalive_interval_seconds', '10', 'Keepalive发送间隔(秒)');

-- 扩展 request_logs 表
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    cached_response_id BIGINT REFERENCES request_logs(id);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    effective_timeout_seconds INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    context_size_tokens INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    is_continuation BOOLEAN DEFAULT FALSE;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    continuation_keywords TEXT[];
```

#### Go代码实现

```go
// config/timeout_config.go
package config

import (
    "context"
    "sync"
    "time"
)

type TimeoutConfig struct {
    ClientDefaultSeconds     int    `json:"client_default_seconds"`
    UpstreamBaseSeconds      int    `json:"upstream_base_seconds"`
    UpstreamMinSeconds       int    `json:"upstream_min_seconds"`
    UpstreamMaxSeconds       int    `json:"upstream_max_seconds"`
    ContextThresholdTokens   int    `json:"context_threshold_tokens"`
    ContextBonusSeconds      int    `json:"context_bonus_seconds"`
    DynamicMode              string `json:"dynamic_mode"` // static/context_aware/network_aware/adaptive
    
    mu sync.RWMutex
}

var globalTimeoutConfig = &TimeoutConfig{
    ClientDefaultSeconds:   30,
    UpstreamBaseSeconds:    30,
    UpstreamMinSeconds:     10,
    UpstreamMaxSeconds:     300,
    ContextThresholdTokens: 20000,
    ContextBonusSeconds:    30,
    DynamicMode:            "context_aware",
}

// CalculateEffectiveTimeout 计算实际超时时间
func (tc *TimeoutConfig) CalculateEffectiveTimeout(
    contextTokens int,
    networkLatencyMs int,
) time.Duration {
    tc.mu.RLock()
    defer tc.mu.RUnlock()
    
    timeout := tc.UpstreamBaseSeconds
    
    switch tc.DynamicMode {
    case "context_aware":
        if contextTokens > tc.ContextThresholdTokens {
            timeout += tc.ContextBonusSeconds
        }
    case "network_aware":
        // 网络延迟 > 1秒时，增加 networkLatency + 30秒
        if networkLatencyMs > 1000 {
            timeout += (networkLatencyMs/1000) + 30
        }
    case "adaptive":
        // 综合模式
        if contextTokens > tc.ContextThresholdTokens {
            timeout += tc.ContextBonusSeconds
        }
        if networkLatencyMs > 1000 {
            timeout += (networkLatencyMs / 1000)
        }
    }
    
    // 限制范围
    if timeout < tc.UpstreamMinSeconds {
        timeout = tc.UpstreamMinSeconds
    }
    if timeout > tc.UpstreamMaxSeconds {
        timeout = tc.UpstreamMaxSeconds
    }
    
    return time.Duration(timeout) * time.Second
}

// ReloadFromDB 从数据库热加载配置
func (tc *TimeoutConfig) ReloadFromDB(ctx context.Context, db *sql.DB) error {
    tc.mu.Lock()
    defer tc.mu.Unlock()
    
    rows, err := db.QueryContext(ctx, `
        SELECT key, value 
        FROM system_settings 
        WHERE key LIKE 'timeout.%'
    `)
    if err != nil {
        return err
    }
    defer rows.Close()
    
    // 解析并更新配置...
    return nil
}
```

### 功能2：继续/重试语义检测

#### 数据库Schema

```sql
-- 继续/重试关键词配置
INSERT INTO system_settings (key, value, description) VALUES
('continuation.keywords', 
 '["继续", "请继续", "continue", "go on", "go", "重试", "请重试", "retry", "come on", "再来", "接着", "keep going"]',
 '继续/重试关键词列表(支持多语言)');

-- 会话最后请求缓存表
CREATE TABLE IF NOT EXISTS session_last_requests (
    session_id VARCHAR(255) PRIMARY KEY,
    last_request_id BIGINT NOT NULL REFERENCES request_logs(id),
    last_request_status VARCHAR(50) NOT NULL, -- success/timeout/error/client_disconnected
    last_request_user_message TEXT,
    last_response_cached TEXT, -- 缓存的完整响应(客户端断开时)
    last_response_chunks INT DEFAULT 0,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ DEFAULT NOW() + INTERVAL '1 hour'
);

CREATE INDEX idx_session_last_requests_expires 
    ON session_last_requests(expires_at);
```

#### Go代码实现

```go
// domains/streaming/continuation_detector.go
package streaming

import (
    "context"
    "encoding/json"
    "strings"
)

type ContinuationDetector struct {
    keywords []string
    db       *sql.DB
}

func NewContinuationDetector(db *sql.DB) *ContinuationDetector {
    return &ContinuationDetector{
        keywords: []string{"继续", "请继续", "continue", "go", "retry"},
        db:       db,
    }
}

// ReloadKeywords 热加载关键词
func (cd *ContinuationDetector) ReloadKeywords(ctx context.Context) error {
    var valueJSON string
    err := cd.db.QueryRowContext(ctx, `
        SELECT value FROM system_settings WHERE key = 'continuation.keywords'
    `).Scan(&valueJSON)
    if err != nil {
        return err
    }
    
    return json.Unmarshal([]byte(valueJSON), &cd.keywords)
}

// IsContinuation 检测是否为继续/重试请求
func (cd *ContinuationDetector) IsContinuation(userMessage string) (bool, []string) {
    userMessage = strings.ToLower(strings.TrimSpace(userMessage))
    
    var matched []string
    for _, kw := range cd.keywords {
        if strings.Contains(userMessage, strings.ToLower(kw)) {
            matched = append(matched, kw)
        }
    }
    
    return len(matched) > 0, matched
}

// GetLastRequestStatus 获取会话最后一次请求状态
type LastRequestInfo struct {
    RequestID         int64
    Status            string // success/timeout/error/client_disconnected
    UserMessage       string
    CachedResponse    string
    ResponseChunks    int
}

func (cd *ContinuationDetector) GetLastRequestStatus(
    ctx context.Context, 
    sessionID string,
) (*LastRequestInfo, error) {
    var info LastRequestInfo
    err := cd.db.QueryRowContext(ctx, `
        SELECT 
            last_request_id,
            last_request_status,
            last_request_user_message,
            COALESCE(last_response_cached, ''),
            last_response_chunks
        FROM session_last_requests
        WHERE session_id = $1 AND expires_at > NOW()
    `, sessionID).Scan(
        &info.RequestID,
        &info.Status,
        &info.UserMessage,
        &info.CachedResponse,
        &info.ResponseChunks,
    )
    
    if err == sql.ErrNoRows {
        return nil, nil
    }
    return &info, err
}
```

### 功能3：Keepalive & 节点切换通知

#### SSE格式设计

```
客户端实时收到的流式消息类型：

1. 正常数据块：
data: {"id":"chatcmpl-xxx","choices":[{"delta":{"content":"你好"}}]}

2. Keepalive消息（不计入上下文）：
event: keepalive
data: {"type":"keepalive","message":"正在等待响应...","elapsed_seconds":15}

3. 节点切换通知（不计入上下文）：
event: node_switch
data: {"type":"node_switch","from_provider_id":18,"to_provider_id":5917,"reason":"timeout","attempt":2}

4. 最后节点重试通知：
event: retry_wait
data: {"type":"retry_wait","message":"最后节点失败，10秒后重试...","retry_after_seconds":10}
```

#### Go代码实现

```go
// domains/streaming/keepalive_sender.go
package streaming

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type KeepaliveSender struct {
    w              http.ResponseWriter
    flusher        http.Flusher
    interval       time.Duration
    stopCh         chan struct{}
    startTime      time.Time
}

func NewKeepaliveSender(w http.ResponseWriter, interval time.Duration) *KeepaliveSender {
    flusher, _ := w.(http.Flusher)
    return &KeepaliveSender{
        w:         w,
        flusher:   flusher,
        interval:  interval,
        stopCh:    make(chan struct{}),
        startTime: time.Now(),
    }
}

// Start 启动keepalive发送
func (ks *KeepaliveSender) Start(ctx context.Context) {
    ticker := time.NewTicker(ks.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ks.stopCh:
            return
        case <-ticker.C:
            elapsed := int(time.Since(ks.startTime).Seconds())
            ks.SendKeepalive(elapsed)
        }
    }
}

// SendKeepalive 发送keepalive消息
func (ks *KeepaliveSender) SendKeepalive(elapsedSeconds int) error {
    msg := map[string]interface{}{
        "type":            "keepalive",
        "message":         "正在等待响应...",
        "elapsed_seconds": elapsedSeconds,
    }
    
    data, _ := json.Marshal(msg)
    fmt.Fprintf(ks.w, "event: keepalive\ndata: %s\n\n", data)
    
    if ks.flusher != nil {
        ks.flusher.Flush()
    }
    return nil
}

// SendNodeSwitch 发送节点切换通知
func (ks *KeepaliveSender) SendNodeSwitch(
    fromProvider, toProvider int,
    reason string,
    attempt int,
) error {
    msg := map[string]interface{}{
        "type":            "node_switch",
        "from_provider_id": fromProvider,
        "to_provider_id":   toProvider,
        "reason":           reason,
        "attempt":          attempt,
    }
    
    data, _ := json.Marshal(msg)
    fmt.Fprintf(ks.w, "event: node_switch\ndata: %s\n\n", data)
    
    if ks.flusher != nil {
        ks.flusher.Flush()
    }
    return nil
}

// Stop 停止keepalive
func (ks *KeepaliveSender) Stop() {
    close(ks.stopCh)
}
```

---

## 🔄 完整请求流程

### 场景1：正常新请求（无超时）

```
1. 客户端发送请求
2. 网关计算 effectiveTimeout = 30s + contextBonus
3. 启动 keepalive goroutine (每10s发送一次)
4. 请求 LLM节点A，耗时 15秒
5. 收到完整响应，流式返回客户端
6. 停止 keepalive
7. 记录 request_logs (success=true)
8. 更新 session_last_requests
```

### 场景2：超时切换节点（成功）

```
1. 客户端发送请求
2. effectiveTimeout = 60s
3. 启动 keepalive (10s, 20s, 30s 各发送一次)
4. 请求节点A，65秒未完成
5. **超时 → 切换到节点B**
6. 发送 node_switch 事件给客户端
7. 节点B 15秒返回成功
8. 流式返回客户端
9. 记录 request_logs:
   - attempt_no=2
   - routing_attempts=[{provider:A,timeout},{provider:B,success}]
```

### 场景3：客户端断开但LLM响应成功

```
1. 客户端发送请求
2. 请求节点A，返回中...
3. **客户端在第20秒断开连接**
4. 网关继续接收节点A的响应（共40秒完成）
5. 将完整响应缓存到 session_last_requests:
   - last_request_status = 'client_disconnected'
   - last_response_cached = '完整响应内容'
   - last_response_chunks = 45
6. 记录 request_logs (success=false, client_timeout=true)
```

### 场景4：继续请求复用缓存

```
1. 客户端发送新请求：messages=[..., {role:"user", content:"请继续"}]
2. 检测到"继续"关键词
3. 查询 session_last_requests
4. **发现上次请求状态='client_disconnected' 且有缓存**
5. **直接返回缓存的响应**（流式输出）
6. 记录 request_logs:
   - cached_response_id = 上次请求ID
   - is_continuation = true
   - continuation_keywords = ['继续']
7. Token消耗 = 0（仅使用缓存）
```

### 场景5：最后节点失败，延迟重试

```
1. 客户端发送请求
2. 可用节点列表：[A, B]
3. 节点A超时 → 切换到B
4. 节点B也超时 → **只剩一个节点的情况**
5. 发送 retry_wait 事件：10秒后重试
6. 等待10秒
7. 重试节点B（不受 retry.max_attempts 限制）
8. 如仍失败 → 返回最终错误
```

---

## 📊 数据流与状态机

### 请求状态转换

```
          [新请求]
             ↓
    ┌────────────────┐
    │   ROUTING      │ 
    └────────┬───────┘
             ↓
    ┌────────────────┐
    │  ATTEMPTING    │ ← 发送keepalive
    │  (Node A)      │
    └────┬───────────┘
         ↓
    ┌────────────────┐        ┌─────────────┐
    │   超时/失败?    │───是───→│ Node B可用? │
    └────┬───────────┘        └──────┬──────┘
         否                          是│  否→返回错误
         ↓                            ↓
    ┌────────────────┐        ┌─────────────┐
    │    SUCCESS     │        │  SWITCHING  │
    └────────────────┘        └──────┬──────┘
                                     ↓
                              发送node_switch
                                     ↓
                              重新进入ATTEMPTING
```

---

## 🚀 实施计划

### Phase 1: 基础设施（1-2天）

- [ ] 创建 `system_settings` 表
- [ ] 扩展 `request_logs` 表
- [ ] 创建 `session_last_requests` 表
- [ ] 实现配置热加载基础框架

### Phase 2: 动态超时（2-3天）

- [ ] 实现 `TimeoutConfig` 及计算逻辑
- [ ] 集成到 executor
- [ ] 编写单元测试
- [ ] 在154测试环境验证

### Phase 3: Keepalive & 节点切换（2-3天）

- [ ] 实现 `KeepaliveSender`
- [ ] 实现节点切换通知
- [ ] SSE事件格式标准化
- [ ] 前端SDK适配

### Phase 4: 继续/重试检测（3-4天）

- [ ] 实现 `ContinuationDetector`
- [ ] 实现响应缓存机制
- [ ] 实现缓存复用逻辑
- [ ] 编写集成测试

### Phase 5: 监控与优化（持续）

- [ ] Grafana Dashboard
- [ ] 超时分布监控
- [ ] Token节省统计
- [ ] 配置调优

---

## 📈 预期效果

### 性能指标

| 指标 | 当前 | 目标 |
|------|------|------|
| 客户端超时率 | ~13% | <3% |
| Token浪费率 | ~10% | <2% |
| 平均响应时间 | 11.6s | 10s |
| 节点切换成功率 | N/A | >95% |

### 成本节省估算

假设：
- 每天100K请求
- 13%超时（13K请求）
- 平均每次prompt 5000 tokens
- Token价格 $0.01/1K

**当前浪费**：13K × 5K × $0.01/1K = **$650/天**

**优化后浪费**：3K × 5K × $0.01/1K = **$150/天**

**每天节省**：**$500** ≈ **¥3600**

**每月节省**：**$15,000** ≈ **¥108,000**

---

## 🔧 配置示例

### 生产环境推荐配置

```yaml
# system_settings 表内容
timeout:
  client_default_seconds: 60        # 客户端超时60秒
  upstream_base_seconds: 45         # 基础超时45秒
  upstream_min_seconds: 20          # 最小20秒
  upstream_max_seconds: 180         # 最大3分钟
  context_threshold_tokens: 20000   # >20K增加超时
  context_bonus_seconds: 45         # 增加45秒
  dynamic_mode: "adaptive"          # 自适应模式

retry:
  max_attempts: 3                   # 最多重试3次
  keepalive_interval_seconds: 15    # 每15秒keepalive

continuation:
  keywords: 
    - "继续"
    - "请继续"  
    - "continue"
    - "go on"
    - "retry"
    - "重试"
    - "come on"
    - "接着"
```

---

## ⚠️ 注意事项

### 1. 缓存过期策略
- 缓存响应默认保留1小时
- 定期清理过期记录（cron job）

### 2. 并发控制
- Keepalive goroutine需要正确关闭
- 避免goroutine泄漏

### 3. 客户端兼容性
- 旧客户端忽略 `event: keepalive` 等扩展事件
- 保持向后兼容

### 4. 监控告警
- 超时率突增告警
- 节点切换频繁告警
- 缓存命中率过低告警

---

## 📚 相关文档

- [SSE规范](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- [OpenAI Streaming API](https://platform.openai.com/docs/api-reference/streaming)
- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html)

---

**文档版本**: v1.0  
**创建时间**: 2026-07-22  
**作者**: AI Agent  
**状态**: 设计阶段
