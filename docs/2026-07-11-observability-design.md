# 可观测性字段扩展设计文档

**日期**: 2026-07-11  
**版本**: v1.0  
**作者**: Implementer  
**任务**: P2.1 - Observability Fields Extension

---

## 1. 概述

本方案为 `request_logs` 表新增 **26 个可观测性字段**，用于全方位追踪调用方、会话、安全状态、协议转换和厂商元数据。

### 设计目标

1. **调用方溯源**: 记录真实 IP、Agent 名称、API Key 指纹、客户 ID
2. **会话上下文**: 关联 session/task 元信息，支持任务级分析
3. **压缩与优化**: 追踪上下文压缩、缓存命中情况
4. **安全合规**: 记录内容安全评分、DLP 违规、敏感关键词、限流状态
5. **协议透明**: 标记客户端协议、上游协议、是否发生转换
6. **厂商扩展**: 保留 JSONB 字段存储厂商特有字段（如 reasoning_tokens）

---

## 2. Schema 设计

### 2.1 新增字段清单

| 类别 | 字段名 | 类型 | 说明 |
|---|---|---|---|
| **调用方信息** | `client_ip` | `INET` | 客户端真实 IP（从 X-Forwarded-For 提取） |
| | `client_forwarded_for` | `TEXT` | 完整 X-Forwarded-For 头链 |
| | `agent_name` | `VARCHAR(255)` | 智能体名称（claude-code/opencode/cursor） |
| | `agent_type` | `VARCHAR(50)` | 智能体类型（web/mobile/cli/api/bot/internal） |
| | `api_key_fingerprint` | `VARCHAR(16)` | API Key 前 8 字符（掩码） |
| | `customer_id` | `BIGINT` | 客户/组织 ID（多租户计费） |
| **供应商详情** | `upstream_endpoint` | `TEXT` | 上游 API 完整 URL |
| **会话与任务** | `session_title` | `TEXT` | 会话标题（人类可读） |
| | `session_summary` | `TEXT` | 会话摘要 |
| | `task_id` | `VARCHAR(255)` | 任务 ID（JIRA-123、task_001） |
| | `task_title` | `TEXT` | 任务标题 |
| **压缩优化** | `compression_start_index` | `INT` | 压缩起始消息索引 |
| | `compression_end_index` | `INT` | 压缩结束消息索引 |
| | `compression_ratio` | `FLOAT` | 压缩比（0.0-1.0） |
| | `cache_hit` | `BOOLEAN` | 是否命中缓存 |
| | `cache_tokens_saved` | `INT` | 缓存节省的 token 数 |
| **安全合规** | `content_safety_score` | `JSONB` | 内容安全评分 `{"score": 0.95, "categories": {...}}` |
| | `dlp_violations` | `JSONB` | DLP 违规详情 `[{"type": "ssn", "count": 1}]` |
| | `sensitive_keywords` | `TEXT[]` | 匹配到的敏感关键词 `["password", "secret"]` |
| | `rate_limit_status` | `VARCHAR(50)` | 限流状态（under_limit/approaching_limit/exceeded/bypassed） |
| **协议转换** | `client_protocol` | `VARCHAR(50)` | 客户端协议（openai/anthropic/gemini） |
| | `upstream_protocol` | `VARCHAR(50)` | 上游协议（anthropic/openai/bedrock） |
| | `protocol_conversion` | `BOOLEAN` | 是否进行协议转换 |
| | `ir_extensions` | `JSONB` | IR 扩展字段 `{"reasoning_effort": "medium"}` |
| | `sanitizer_mutations` | `JSONB` | 消毒器变更记录 `{"stripped_fields": [...]}` |
| **厂商元数据** | `vendor_metadata` | `JSONB` | 厂商特定字段快照 `{"reasoning_tokens": 1500}` |

### 2.2 索引策略

```sql
-- 高频查询字段
CREATE INDEX idx_request_logs_client_ip ON request_logs(client_ip);
CREATE INDEX idx_request_logs_customer_id ON request_logs(customer_id);
CREATE INDEX idx_request_logs_task_type ON request_logs(task_type);
CREATE INDEX idx_request_logs_agent_type ON request_logs(agent_type);

-- 部分索引（仅索引关键状态）
CREATE INDEX idx_request_logs_protocol_conversion 
    ON request_logs(protocol_conversion) 
    WHERE protocol_conversion = true;

CREATE INDEX idx_request_logs_rate_limit_status 
    ON request_logs(rate_limit_status) 
    WHERE rate_limit_status IN ('exceeded', 'approaching_limit');
```

---

## 3. Go 实现

### 3.1 核心结构体

```go
package telemetry

type RequestMetadata struct {
    // 调用方信息
    ClientIP           string
    ClientForwardedFor string
    AgentName          string
    AgentType          string  // web/mobile/cli/api/bot/internal
    APIKeyFingerprint  string
    CustomerID         *int64

    // 供应商信息
    CredentialID     *int64
    UpstreamEndpoint string

    // 会话上下文
    SessionTitle   string
    SessionSummary string
    TaskID         string
    TaskTitle      string
    TaskType       string

    // 压缩优化
    CompressionStartIndex *int
    CompressionEndIndex   *int
    CompressionRatio      *float64
    CacheHit              *bool
    CacheTokensSaved      *int

    // 安全合规
    ContentSafetyScore map[string]interface{} // JSONB
    DLPViolations      []map[string]interface{} // JSONB array
    SensitiveKeywords  []string
    RateLimitStatus    string

    // 协议元数据
    ClientProtocol     string
    UpstreamProtocol   string
    ProtocolConversion *bool
    IRExtensions       map[string]interface{} // JSONB
    SanitizerMutations map[string]interface{} // JSONB

    // 厂商元数据
    VendorMetadata map[string]interface{} // JSONB
}
```

### 3.2 工具函数

#### 提取客户端 IP

```go
// 优先级: X-Real-IP > X-Forwarded-For (首个) > RemoteAddr
func ExtractClientIP(r *http.Request) string {
    if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
        return strings.TrimSpace(realIP)
    }
    if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
        parts := strings.Split(fwd, ",")
        return strings.TrimSpace(parts[0])
    }
    host, _, _ := net.SplitHostPort(r.RemoteAddr)
    return host
}
```

#### API Key 掩码

```go
// sk-1234abcd5678efgh -> sk-1234ab***
func MaskAPIKey(key string) string {
    if len(key) <= 8 {
        return "***"
    }
    return key[:8] + "***"
}
```

#### Agent 识别

```go
func ExtractAgentName(r *http.Request) string {
    if agentName := r.Header.Get("X-Agent-Name"); agentName != "" {
        return agentName
    }
    ua := strings.ToLower(r.Header.Get("User-Agent"))
    switch {
    case strings.Contains(ua, "claude-code"):
        return "claude-code"
    case strings.Contains(ua, "opencode"):
        return "opencode"
    case strings.Contains(ua, "cursor"):
        return "cursor"
    // ...
    default:
        return "unknown"
    }
}

func ExtractAgentType(r *http.Request) string {
    if agentType := r.Header.Get("X-Agent-Type"); agentType != "" {
        return agentType
    }
    ua := strings.ToLower(r.Header.Get("User-Agent"))
    // CLI: curl, wget, claude-code, opencode, ...
    // API: python, go-http-client, okhttp, ...
    // Bot: googlebot, crawler, spider, ...
    // Mobile: android, iphone, ipad, ...
    // Web: chrome, safari, firefox, ...
    // 返回: cli/api/bot/mobile/web/unknown
}
```

---

## 4. 典型查询场景

### 4.1 按客户 IP 聚合

```sql
SELECT 
    client_ip,
    COUNT(*) as request_count,
    SUM(total_tokens) as total_tokens,
    AVG(latency_ms) as avg_latency,
    COUNT(*) FILTER (WHERE success = false) * 100.0 / COUNT(*) as error_rate
FROM request_logs
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY client_ip
ORDER BY request_count DESC
LIMIT 20;
```

### 4.2 协议转换分析

```sql
SELECT 
    client_protocol,
    upstream_protocol,
    COUNT(*) as conversion_count,
    AVG(latency_ms) as avg_latency,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms) as p95_latency
FROM request_logs
WHERE protocol_conversion = true
  AND ts > NOW() - INTERVAL '7 days'
GROUP BY client_protocol, upstream_protocol;
```

### 4.3 压缩效率统计

```sql
SELECT 
    compression_strategy,
    COUNT(*) as compressed_requests,
    AVG(compression_ratio) as avg_ratio,
    AVG(compression_end_index - compression_start_index) as avg_compressed_messages,
    SUM(COALESCE((outbound_token_est - total_tokens), 0)) as tokens_saved
FROM request_logs
WHERE compression_strategy IS NOT NULL
  AND ts > NOW() - INTERVAL '24 hours'
GROUP BY compression_strategy;
```

### 4.4 安全合规审计

```sql
SELECT 
    ts,
    tenant_id,
    client_ip,
    agent_name,
    dlp_violations,
    sensitive_keywords,
    content_safety_score
FROM request_logs
WHERE (
    dlp_violations IS NOT NULL 
    OR array_length(sensitive_keywords, 1) > 0
    OR (content_safety_score->>'score')::FLOAT < 0.8
)
AND ts > NOW() - INTERVAL '7 days'
ORDER BY ts DESC
LIMIT 100;
```

### 4.5 任务级成本分析

```sql
SELECT 
    task_id,
    task_title,
    task_type,
    COUNT(*) as request_count,
    SUM(total_tokens) as total_tokens,
    SUM(cost_usd) as total_cost,
    AVG(latency_ms) as avg_latency,
    STRING_AGG(DISTINCT agent_name, ', ') as agents_used
FROM request_logs
WHERE task_id IS NOT NULL
  AND ts > NOW() - INTERVAL '30 days'
GROUP BY task_id, task_title, task_type
ORDER BY total_cost DESC
LIMIT 50;
```

### 4.6 厂商元数据提取

```sql
-- 统计 OpenAI o1/o3 reasoning tokens
SELECT 
    DATE_TRUNC('day', ts) as date,
    outbound_model,
    COUNT(*) as request_count,
    AVG((vendor_metadata->>'reasoning_tokens')::INT) as avg_reasoning_tokens,
    SUM((vendor_metadata->>'reasoning_tokens')::INT) as total_reasoning_tokens
FROM request_logs
WHERE vendor_metadata ? 'reasoning_tokens'
  AND ts > NOW() - INTERVAL '30 days'
GROUP BY date, outbound_model
ORDER BY date DESC, total_reasoning_tokens DESC;
```

---

## 5. 隐私保护措施

### 5.1 API Key 掩码

- **存储**: 仅保留前 8 字符 + `***`（如 `sk-1234ab***`）
- **用途**: 问题排查时可识别具体 key，但无法还原完整 key
- **实现**: `MaskAPIKey()` 函数在写入前自动掩码

### 5.2 IP 地址脱敏（可选）

生产环境可考虑：

```go
// 掩码最后一段 IPv4
func MaskIP(ip string) string {
    parts := strings.Split(ip, ".")
    if len(parts) == 4 {
        parts[3] = "***"
        return strings.Join(parts, ".")
    }
    return ip
}
```

### 5.3 敏感关键词去重

```sql
-- 仅记录关键词类型，不记录具体值
UPDATE request_logs 
SET sensitive_keywords = ARRAY['<redacted>']
WHERE array_length(sensitive_keywords, 1) > 0;
```

### 5.4 DLP 违规脱敏

```json
// 生产存储格式（不记录具体值）
{
  "violations": [
    {"type": "ssn", "count": 2, "locations": ["request.messages[3]"]},
    {"type": "credit_card", "count": 1, "locations": ["request.messages[5]"]}
  ]
}
```

---

## 6. 部署与验证

### 6.1 Migration 执行

```bash
# Forward migration
psql -h 14.103.112.184 -p 5432 -U llmgo -d llm_gateway \
    -f deploy/sql/migrations/2026-07-11-observability-fields.sql

# Rollback (if needed)
psql -h 14.103.112.184 -p 5432 -U llmgo -d llm_gateway \
    -f deploy/sql/migrations/2026-07-11-observability-fields.down.sql
```

### 6.2 测试脚本

```bash
# 运行测试 SQL
psql -h 14.103.112.184 -p 5432 -U llmgo -d llm_gateway \
    -f deploy/sql/migrations/2026-07-11-observability-fields_test.sql
```

### 6.3 Go 单元测试

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go test ./telemetry -v -run TestRequestMetadata
```

### 6.4 验证清单

- [ ] 所有 26 个字段成功创建
- [ ] 6 个索引成功创建
- [ ] 测试数据可正常插入和查询
- [ ] JSONB 字段可正确序列化/反序列化
- [ ] Go 工具函数单元测试全部通过
- [ ] IP 提取逻辑正确处理 X-Forwarded-For 链
- [ ] API Key 掩码正确保留前 8 字符
- [ ] Agent 识别覆盖常见 User-Agent 模式

---

## 7. 后续集成建议

### 7.1 中间件集成

```go
// middleware/observability.go
func ObservabilityMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        meta := telemetry.NewRequestMetadata(r)
        
        // 注入到 request context
        ctx := context.WithValue(r.Context(), "observability_meta", meta)
        r = r.WithContext(ctx)
        
        next.ServeHTTP(w, r)
    })
}
```

### 7.2 Request Logger 集成

```go
// 在 request_logs 插入时
func (l *Logger) LogRequest(ctx context.Context, req *Request) {
    meta := ctx.Value("observability_meta").(*telemetry.RequestMetadata)
    
    _, err := l.db.Exec(ctx, `
        INSERT INTO request_logs (
            request_id, ts, tenant_id, success,
            client_ip, agent_name, agent_type, 
            vendor_metadata, ...
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, ...)
    `, 
        req.ID, time.Now(), req.TenantID, req.Success,
        meta.ClientIP, meta.AgentName, meta.AgentType,
        meta.VendorMetadata, ...
    )
}
```

### 7.3 Session Manager 集成

```go
// domains/session/manager.go
func (m *Manager) CreateSession(ctx context.Context, opts CreateOptions) (*Session, error) {
    session := &Session{
        Title:   opts.Title,
        Summary: opts.Summary,
        // ...
    }
    
    // 注入到后续请求的 metadata
    meta := &telemetry.RequestMetadata{
        SessionTitle:   session.Title,
        SessionSummary: session.Summary,
        TaskID:         opts.TaskID,
        TaskTitle:      opts.TaskTitle,
    }
    
    return session, nil
}
```

---

## 8. 性能影响评估

### 8.1 存储开销

- **每行新增字段**: ~500 bytes（含 JSONB）
- **日增 100 万请求**: +500 MB/day
- **分区表**: 按月分区，历史数据可归档到 S3

### 8.2 写入性能

- **26 个新字段**: 对 INSERT 性能影响 < 5%
- **6 个索引**: 增加 ~10% 写入延迟
- **批量插入**: 建议使用 COPY 或 bulk insert

### 8.3 查询性能

- **索引覆盖**: 高频查询字段已建索引
- **部分索引**: 仅索引关键状态（如 protocol_conversion = true）
- **JSONB 查询**: 使用 GIN 索引（后续可按需添加）

---

## 9. 数据保留策略

### 9.1 热数据（0-3 个月）

- 保留在 `request_logs` 分区表（PostgreSQL Citus columnar）
- 全字段可查

### 9.2 温数据（3-12 个月）

- 归档到 S3/Parquet
- 保留聚合统计（按天/小时）

### 9.3 冷数据（> 12 个月）

- 仅保留 metadata（request_id, ts, tenant_id, cost）
- 删除 request_body/response_body

---

## 10. 参考资料

- **Schema 定义**: `deploy/sql/objects/tables/request_logs.sql`
- **Go 实现**: `telemetry/request_metadata.go`
- **测试用例**: `telemetry/request_metadata_test.go`
- **Migration**: `deploy/sql/migrations/2026-07-11-observability-fields.sql`
- **Rollback**: `deploy/sql/migrations/2026-07-11-observability-fields.down.sql`

---

## 附录：字段映射表

| 旧字段 | 新字段 | 说明 |
|---|---|---|
| `virtual_ip` | `client_ip` | 统一为真实 IP（INET 类型） |
| `gw_session_id` | `session_title` / `session_summary` | 新增人类可读字段 |
| `gw_task_id` | `task_id` / `task_title` / `task_type` | 拆分为独立字段 |
| `compression_meta` | `compression_start_index` / `compression_end_index` / `compression_ratio` | 结构化字段替代 JSONB |
| `cache_read_tokens` | `cache_hit` / `cache_tokens_saved` | 新增布尔标志 |
| `egress_protocol` | `client_protocol` / `upstream_protocol` / `protocol_conversion` | 拆分为客户端+上游协议 |
| - | `vendor_metadata` | **新增**：厂商特定字段快照 |
| - | `content_safety_score` | **新增**：内容安全评分 |
| - | `dlp_violations` | **新增**：DLP 违规详情 |

---

**版本历史**:
- v1.0 (2026-07-11): 初版设计，新增 26 个可观测性字段
