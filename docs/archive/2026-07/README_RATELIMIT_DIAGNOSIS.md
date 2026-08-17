# API Key 限速问题诊断指南

## 问题描述
使用 `sk-1vH6C2I9pywyvUXa` 访问 minimax-m3 时，频繁遇到 "User API Key Rate limit exceeded" 错误。

## 错误来源分析

根据代码分析，"**User API Key Rate limit exceeded**" 这个错误消息**不是**从 LLM Gateway 返回的。网关代码中的限速错误消息是：
- `"Rate limit exceeded"` (不带 "User API Key" 前缀)
- `"rate_limit_exceeded"`

因此，这个错误很可能是从 **MiniMax 上游 API** 直接返回的，说明是 MiniMax 供应商侧对您使用的凭证（credential）进行了限速。

## 限速层级说明

LLM Gateway 有多层限速机制：

### 1. API Key 层级（网关侧）
位于数据库 `api_keys` 表：
- `rate_limit_rpm`: 每分钟请求数限制
- `rate_limit_concurrent`: 并发请求数限制  
- `rate_limit_tpm`: 每分钟 token 数限制
- `key_tier`: 等级（system/production/default/applicant）

**默认值**（当数据库值为 NULL 时）：
| Tier | RPM | Concurrent |
|------|-----|------------|
| system | 300 | 50 |
| production | 60 | 20 |
| default | 12 | 6 |
| applicant | 6 | 2 |

**特殊值**：
- `NULL`: 使用 tier 默认值
- `0`: 明确设置为无限制
- `> 0`: 使用该具体值

### 2. 供应商凭证层级（网关侧）
位于 `credentials` 表：
- `concurrent_limit`: 单个凭证的并发限制
- `rpm_limit`: 单个凭证的 RPM 限制

这些限制是为了保护供应商凭证不被过度使用。

### 3. MiniMax API 层级（上游侧）
MiniMax 官方 API 对每个 API Key 有自己的限速策略，包括：
- QPM (Queries Per Minute)
- 并发请求数
- TPM (Tokens Per Minute)

**重要**：即使网关侧没有设置限制，上游 MiniMax API 仍然会执行其自身的限速。

## 诊断步骤

### 1. 使用诊断脚本
```bash
# 设置数据库连接
export LLM_GATEWAY_DATABASE_URL='postgres://user:pass@host:port/dbname'

# 运行诊断
./diagnose_key_ratelimit.sh sk-1vH6C2I9pywyvUXa
```

### 2. 手动查询 API Key 配置
```sql
SELECT 
    id,
    key_prefix,
    rate_limit_rpm,
    rate_limit_concurrent,
    rate_limit_tpm,
    key_tier,
    status,
    enabled
FROM api_keys 
WHERE key_prefix = 'sk-1vH6C2I9pywyvUXa';
```

### 3. 查询 MiniMax 凭证状态
```sql
SELECT 
    c.id,
    c.display_name,
    c.concurrent_limit,
    c.rpm_limit,
    c.enabled,
    c.availability_status,
    c.breaker_state,
    p.vendor_name
FROM credentials c
JOIN providers p ON c.provider_id = p.id
WHERE p.vendor_name LIKE '%minimax%'
  AND c.enabled = true;
```

### 4. 查看最近的错误日志
```sql
SELECT 
    created_at,
    error_kind,
    failure_stage,
    provider_name,
    credential_id,
    response_preview
FROM request_logs_2026_07
WHERE api_key_id = (SELECT id FROM api_keys WHERE key_prefix = 'sk-1vH6C2I9pywyvUXa')
  AND error_kind = 'rate_limit'
  AND created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 20;
```

## 解决方案

### 场景 1: 网关侧限速过严
如果 API Key 的 `rate_limit_rpm` 或 `rate_limit_concurrent` 设置过低：

```sql
-- 设置为无限制（网关侧不限速）
UPDATE api_keys 
SET rate_limit_rpm = 0, 
    rate_limit_concurrent = 0,
    rate_limit_tpm = 0
WHERE key_prefix = 'sk-1vH6C2I9pywyvUXa';

-- 或者设置为更高的值
UPDATE api_keys 
SET rate_limit_rpm = 300, 
    rate_limit_concurrent = 50
WHERE key_prefix = 'sk-1vH6C2I9pywyvUXa';
```

### 场景 2: 供应商凭证限速过严
如果 credentials 表中 minimax 凭证的并发限制过低：

```sql
-- 查看当前限制
SELECT id, display_name, concurrent_limit, rpm_limit 
FROM credentials 
WHERE provider_id IN (SELECT id FROM providers WHERE vendor_name LIKE '%minimax%');

-- 调整凭证限制
UPDATE credentials 
SET concurrent_limit = 100,  -- 根据实际需求调整
    rpm_limit = 500
WHERE id = <credential_id>;
```

### 场景 3: MiniMax 上游 API 限速（最可能）
如果错误消息确实是 "User API Key Rate limit exceeded"（带 "User API Key" 前缀），这表明：

1. **MiniMax API 本身对您使用的供应商 Key 进行了限速**
2. 网关只是透传了这个错误

**解决办法**：
- 联系 MiniMax 提升您的 API Key 的配额
- 在网关中配置多个 MiniMax 凭证进行负载均衡
- 降低请求频率
- 实现客户端重试逻辑（遵守 Retry-After 头）

### 场景 4: 添加更多 MiniMax 凭证
如果单个 MiniMax 凭证被限速，可以添加多个凭证让网关自动轮换：

```sql
-- 查看当前 minimax 凭证数量
SELECT COUNT(*) as credential_count
FROM credentials c
JOIN providers p ON c.provider_id = p.id
WHERE p.vendor_name LIKE '%minimax%' 
  AND c.enabled = true;

-- 如果只有 1-2 个凭证，考虑添加更多
```

## 监控建议

### 1. 设置告警
监控 `request_logs` 表中 `error_kind = 'rate_limit'` 的频率：

```sql
SELECT 
    DATE_TRUNC('hour', created_at) as hour,
    COUNT(*) as rate_limit_errors,
    COUNT(DISTINCT credential_id) as affected_credentials
FROM request_logs_2026_07
WHERE error_kind = 'rate_limit'
  AND created_at > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;
```

### 2. 查看凭证使用分布
```sql
SELECT 
    credential_id,
    c.display_name,
    COUNT(*) as request_count,
    SUM(CASE WHEN error_kind = 'rate_limit' THEN 1 ELSE 0 END) as rate_limit_count,
    ROUND(100.0 * SUM(CASE WHEN error_kind = 'rate_limit' THEN 1 ELSE 0 END) / COUNT(*), 2) as error_rate_pct
FROM request_logs_2026_07 rl
JOIN credentials c ON rl.credential_id = c.id
WHERE rl.api_key_id = (SELECT id FROM api_keys WHERE key_prefix = 'sk-1vH6C2I9pywyvUXa')
  AND rl.created_at > NOW() - INTERVAL '1 hour'
GROUP BY credential_id, c.display_name
ORDER BY request_count DESC;
```

## 代码引用

限速相关代码位置：
- API Key 限速检查: `domains/streaming/rate_limit.go`
- 凭证并发控制: `domains/credential/limiter.go`
- 错误分类: `errorsx/classify.go`
- Key 验证: `domains/authentication/verifier.go`

## 下一步行动

1. 运行诊断脚本获取当前配置
2. 检查 `request_logs` 中的 `response_preview` 字段，确认错误消息的完整内容
3. 根据诊断结果选择对应的解决方案
4. 如果是 MiniMax 上游限速，联系 MiniMax 技术支持提升配额
