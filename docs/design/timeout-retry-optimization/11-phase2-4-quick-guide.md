# Phase 2-4 快速实施指南

## 当前状态（2026-07-23 00:10）

✅ **已完成**：
- Phase 0: 配置优化已上线
- Phase 1: 数据库Schema已部署
- Phase 2: TimeoutConfig核心代码完成（756行）

⏳ **待完成**：
- Phase 2: 集成到Executor（预计2-3小时）
- Phase 3: Keepalive & 节点切换（预计1天）
- Phase 4: 继续/重试检测（预计2天）

---

## Phase 2 集成步骤（明天上午，2-3小时）

### 步骤1: 在main.go中初始化TimeoutConfig

**文件**: `cmd/gateway/main.go`

```go
// 在main函数中，数据库连接后
import "github.com/kaixuan/llm-gateway-go/config"

func main() {
    // ... existing db setup
    
    // 创建TimeoutConfig
    timeoutConfig := config.NewTimeoutConfig(dbPool, logger)
    defer timeoutConfig.Stop()
    
    // 传递给Router或Executor
    router := &executors.Router{
        // ... existing fields
        TimeoutConfig: timeoutConfig,  // 新增
    }
}
```

### 步骤2: 在Executor中添加TimeoutConfig字段

**文件**: `domains/streaming/executors/executor.go`

在Executor结构体中添加（约第420行附近）：

```go
type Executor struct {
    Router     *Router
    Circuit    *credential.Manager
    // ... existing fields
    
    // TimeoutConfig (Phase 2, 2026-07-23) dynamic timeout calculation
    TimeoutConfig *config.TimeoutConfig  // 新增
    
    // ... rest of fields
}
```

### 步骤3: 在Execute方法中使用动态超时

**文件**: `domains/streaming/executors/executor.go`

找到Execute方法（或executeWithCandidate），在HTTP请求前添加：

```go
func (e *Executor) Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error) {
    // 1. 估算上下文大小
    contextTokens := estimateContextTokens(params.Messages)
    
    // 2. 获取历史延迟（从缓存或数据库）
    historicalLatency := 0
    if params.Model != "" && params.ProviderID > 0 {
        historicalLatency = getHistoricalLatency(e.Router.DB, params.Model, params.ProviderID)
    }
    
    // 3. 计算有效超时
    var effectiveTimeout int
    var timeoutMode string
    var timeoutReason string
    
    if e.TimeoutConfig != nil {
        result := e.TimeoutConfig.CalculateEffectiveTimeout(config.TimeoutCalculationInput{
            ContextSizeTokens:   contextTokens,
            HistoricalLatencyMS: historicalLatency,
            ModelName:           params.Model,
            ProviderID:          params.ProviderID,
        })
        
        effectiveTimeout = result.EffectiveTimeoutSeconds
        timeoutMode = string(result.Mode)
        timeoutReason = result.Reason
        
        params.Logger.Info("dynamic timeout calculated",
            "effective_timeout", effectiveTimeout,
            "mode", timeoutMode,
            "reason", timeoutReason,
            "context_tokens", contextTokens)
    } else {
        // 降级：使用环境变量配置
        effectiveTimeout = e.Router.Config.UpstreamTimeout
        timeoutMode = "static_fallback"
    }
    
    // 4. 创建带超时的Context
    ctx, cancel := context.WithTimeout(ctx, time.Duration(effectiveTimeout)*time.Second)
    defer cancel()
    
    // 5. 继续执行请求...
    result, err := e.executeUpstream(ctx, params)
    
    // 6. 记录到数据库（在audit或日志记录中）
    if result != nil && result.RequestID != "" {
        go recordTimeoutMetrics(e.Router.DB, result.RequestID, 
            effectiveTimeout, contextTokens, timeoutMode)
    }
    
    return result, err
}
```

### 步骤4: 添加辅助函数

**文件**: `domains/streaming/executors/executor.go`（文件末尾）

```go
// estimateContextTokens 估算请求上下文的token数量
func estimateContextTokens(messages []map[string]any) int {
    totalChars := 0
    for _, msg := range messages {
        if content, ok := msg["content"].(string); ok {
            totalChars += len(content)
        }
    }
    // 粗略估算: 1 token ≈ 4 characters
    return totalChars / 4
}

// getHistoricalLatency 从数据库获取模型的历史平均延迟
func getHistoricalLatency(db *sql.DB, model string, providerID int) int {
    var avgLatency int
    query := `
        SELECT COALESCE(AVG(latency_ms), 0)::int
        FROM request_logs
        WHERE outbound_model = $1
          AND provider_id = $2
          AND success = true
          AND ts > NOW() - INTERVAL '24 hours'
    `
    _ = db.QueryRow(query, model, providerID).Scan(&avgLatency)
    return avgLatency
}

// recordTimeoutMetrics 异步记录超时指标到数据库
func recordTimeoutMetrics(db *sql.DB, requestID string, 
    effectiveTimeout, contextTokens int, timeoutMode string) {
    query := `
        UPDATE request_logs
        SET effective_timeout_seconds = $1,
            context_size_tokens = $2,
            timeout_mode = $3
        WHERE request_id = $4
    `
    _, err := db.Exec(query, effectiveTimeout, contextTokens, timeoutMode, requestID)
    if err != nil {
        slog.Warn("failed to record timeout metrics", "error", err, "request_id", requestID)
    }
}
```

### 步骤5: 本地测试

```bash
# 1. 编译
cd /path/to/llm-gateway-go-3
go build -o llm-gateway-go cmd/gateway/main.go

# 2. 设置环境变量
export LLM_GATEWAY_DATABASE_URL="postgres://..."
export LLM_GATEWAY_UPSTREAM_TIMEOUT=90

# 3. 启动服务
./llm-gateway-go

# 4. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "请写一篇3000字的文章"}],
    "stream": true
  }'

# 5. 查看日志
journalctl -u llm-gateway-go -f | grep "dynamic timeout"

# 6. 查询数据库
psql -h 172.16.2.210 -U llm_gateway -d llm_gateway -c "
SELECT 
    request_id,
    effective_timeout_seconds,
    context_size_tokens,
    timeout_mode,
    latency_ms,
    success
FROM request_logs
WHERE ts > NOW() - INTERVAL '10 minutes'
ORDER BY ts DESC
LIMIT 10;
"
```

---

## Phase 3 实施要点（1天）

### 核心文件

```
domains/streaming/
├── keepalive_sender.go          # 新建（Keepalive发送器）
├── node_switch_notifier.go      # 新建（节点切换通知）
└── executors/executor.go        # 修改（集成Keepalive）
```

### 关键接口

```go
// domains/streaming/keepalive_sender.go
package streaming

type KeepaliveSender struct {
    interval time.Duration
    stopChan chan struct{}
    writer   http.ResponseWriter
}

func NewKeepaliveSender(w http.ResponseWriter, interval int) *KeepaliveSender {
    return &KeepaliveSender{
        interval: time.Duration(interval) * time.Second,
        stopChan: make(chan struct{}),
        writer:   w,
    }
}

func (k *KeepaliveSender) Start() {
    go func() {
        ticker := time.NewTicker(k.interval)
        defer ticker.Stop()
        
        for {
            select {
            case <-ticker.C:
                k.SendKeepalive()
            case <-k.stopChan:
                return
            }
        }
    }()
}

func (k *KeepaliveSender) SendKeepalive() {
    // SSE格式
    fmt.Fprintf(k.writer, "event: keepalive\ndata: {\"type\":\"keepalive\",\"timestamp\":%d}\n\n", time.Now().Unix())
    if f, ok := k.writer.(http.Flusher); ok {
        f.Flush()
    }
}

func (k *KeepaliveSender) SendNodeSwitch(fromNode, toNode string, attempt int) {
    data := fmt.Sprintf(`{"type":"node_switch","from":"%s","to":"%s","attempt":%d}`, fromNode, toNode, attempt)
    fmt.Fprintf(k.writer, "event: node_switch\ndata: %s\n\n", data)
    if f, ok := k.writer.(http.Flusher); ok {
        f.Flush()
    }
}

func (k *KeepaliveSender) Stop() {
    close(k.stopChan)
}
```

---

## Phase 4 实施要点（2天）

### 核心文件

```
domains/streaming/
├── continuation_detector.go     # 新建（继续/重试检测）
├── response_cache.go            # 新建（响应缓存管理）
└── executors/executor.go        # 修改（集成检测逻辑）
```

### 关键逻辑

```go
// domains/streaming/continuation_detector.go
package streaming

type ContinuationDetector struct {
    db       *sql.DB
    keywords []string
    cacheTTL time.Duration
}

func NewContinuationDetector(db *sql.DB) *ContinuationDetector {
    keywords := loadKeywordsFromDB(db)
    return &ContinuationDetector{
        db:       db,
        keywords: keywords,
        cacheTTL: 1 * time.Hour,
    }
}

func (cd *ContinuationDetector) IsContinuation(sessionID string, userMessage string) bool {
    // 1. 检查关键词
    for _, kw := range cd.keywords {
        if strings.Contains(strings.ToLower(userMessage), strings.ToLower(kw)) {
            return true
        }
    }
    return false
}

func (cd *ContinuationDetector) GetCachedResponse(sessionID string) (*CachedResponse, error) {
    query := `
        SELECT last_response_cached, last_model, last_latency_ms
        FROM session_last_requests
        WHERE session_id = $1
          AND expires_at > NOW()
    `
    var resp CachedResponse
    err := cd.db.QueryRow(query, sessionID).Scan(&resp.Content, &resp.Model, &resp.LatencyMS)
    if err != nil {
        return nil, err
    }
    return &resp, nil
}
```

---

## 快速验证脚本

### 验证Phase 0效果

```bash
#!/bin/bash
# scripts/verify-phase0.sh

PGPASSWORD='4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg' psql -h 172.16.2.210 -U llm_gateway -d llm_gateway <<'EOF'
SELECT 
    '=== Phase 0 验证 ===' as section;

SELECT 
    DATE_TRUNC('hour', ts) as hour,
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE success = false AND error_kind LIKE '%timeout%') as timeout,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = false AND error_kind LIKE '%timeout%') / COUNT(*), 2) as timeout_rate
FROM request_logs
WHERE ts > NOW() - INTERVAL '12 hours'
GROUP BY hour
ORDER BY hour DESC
LIMIT 12;
EOF
```

### 验证Phase 2集成

```bash
#!/bin/bash
# scripts/verify-phase2.sh

PGPASSWORD='4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg' psql -h 172.16.2.210 -U llm_gateway -d llm_gateway <<'EOF'
SELECT 
    '=== Phase 2 验证 ===' as section;

-- 1. 检查是否有effective_timeout记录
SELECT 
    COUNT(*) as total_records,
    COUNT(*) FILTER (WHERE effective_timeout_seconds IS NOT NULL) as with_timeout,
    COUNT(*) FILTER (WHERE context_size_tokens IS NOT NULL) as with_context,
    COUNT(*) FILTER (WHERE timeout_mode IS NOT NULL) as with_mode
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour';

-- 2. 查看超时模式分布
SELECT 
    timeout_mode,
    COUNT(*) as count,
    ROUND(AVG(effective_timeout_seconds), 2) as avg_timeout,
    ROUND(AVG(context_size_tokens), 2) as avg_context
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND timeout_mode IS NOT NULL
GROUP BY timeout_mode;

-- 3. 查看超时效果
SELECT * FROM v_timeout_effectiveness LIMIT 5;
EOF
```

---

## 预计时间表

```
今天（2026-07-23）：
  00:00-01:00  休息
  09:00-12:00  Phase 2集成 + 测试（3小时）
  14:00-18:00  Phase 3实施 + 测试（4小时）

明天（2026-07-24）：
  09:00-12:00  Phase 3完成
  14:00-18:00  Phase 4开始

后天（2026-07-25）：
  09:00-18:00  Phase 4完成 + 整体测试

总计：约3天完成所有Phase
```

---

## 遇到问题时的调试清单

### 问题1: TimeoutConfig未生效

```bash
# 检查服务日志
journalctl -u llm-gateway-go -f | grep -i timeout

# 检查数据库配置
psql ... -c "SELECT * FROM system_settings WHERE category='timeout';"

# 检查代码是否正确初始化
ps aux | grep llm-gateway-go
```

### 问题2: 配置热加载不工作

```bash
# 修改数据库配置
psql ... -c "UPDATE system_settings SET value='120' WHERE key='timeout.upstream_base_seconds';"

# 等待30秒后查看日志
sleep 30
journalctl -u llm-gateway-go | tail -50 | grep "timeout config reloaded"
```

### 问题3: 超时仍然发生

```bash
# 查询实际超时的请求
psql ... -c "
SELECT 
    request_id,
    effective_timeout_seconds,
    latency_ms,
    latency_ms/1000.0 as latency_seconds,
    (latency_ms/1000.0) - effective_timeout_seconds as overrun_seconds
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND success = false
  AND error_kind LIKE '%timeout%'
ORDER BY ts DESC
LIMIT 10;
"
```

---

## 成功标准

### Phase 2集成成功的标志

- [ ] 日志中可见 "dynamic timeout calculated" 消息
- [ ] request_logs 表中 effective_timeout_seconds 有值
- [ ] request_logs 表中 timeout_mode 有值
- [ ] 不同大小的请求使用不同的超时时间
- [ ] 配置热加载生效（修改DB后30秒内生效）

### Phase 3成功的标志

- [ ] 客户端可接收到 keepalive 事件
- [ ] 节点切换时发送 node_switch 事件
- [ ] SSE流不中断
- [ ] request_logs 中 keepalive_sent_count 有值

### Phase 4成功的标志

- [ ] "请继续"等关键词被正确识别
- [ ] 缓存可正确读写
- [ ] 缓存命中时 Token 消耗为 0
- [ ] request_logs 中 is_continuation 有值
- [ ] 缓存命中率 > 60%

---

**文档版本**: v1.0  
**创建时间**: 2026-07-23 00:15  
**适用阶段**: Phase 2-4 实施  
**预计完成**: 3天内（2026-07-25）
