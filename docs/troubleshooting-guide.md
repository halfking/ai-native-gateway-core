# LLM Gateway 问题排查手册

## 快速诊断命令

### 1. 检查服务状态
```bash
# 71 服务器
ssh -p __PORT_1__ __SSH_TARGET_2__

# 检查容器状态
docker ps | grep llm-gateway
docker logs llm-gateway-go --tail 50

# 健康检查
curl http://localhost:__PORT_3__/healthz
```

### 2. 检查模型发现状态
```bash
# 查看最近的模型发现日志
docker logs llm-gateway-go | grep "model discovery completed" | tail -5

# 期望输出：models > 0
# {"msg":"model discovery completed","models":619}
```

### 3. 检查数据库连接
```bash
# 连接数据库
PGPASSWORD='__DB_PWD_1__' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway

# 检查可用模型数量
SELECT COUNT(*) FROM credential_model_bindings WHERE available = true;
```

### 4. 测试请求
```bash
curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
  -H "Authorization: Bearer __API_KEY_1__" \
  -H "Content-Type: application/json" \
  -H "X-Request-Id: test-$(date +%s)" \
  -d '{
    "model": "glm-4-flash",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'
```

---

## 常见问题排查

### 问题 1：请求超时或 502 错误

**症状：**
- HTTP 502 Bad Gateway
- 请求超时
- 无响应

**排查步骤：**

1. **检查服务是否运行**
   ```bash
   docker ps | grep llm-gateway
   # 应该显示 Up 状态
   ```

2. **检查端口监听**
   ```bash
   netstat -tlnp | grep __PORT_3__
   # 应该显示 LISTEN
   ```

3. **查看错误日志**
   ```bash
   docker logs llm-gateway-go --tail 100 | grep -E "ERROR|WARN"
   ```

4. **检查数据库连接**
   ```sql
   SELECT 1;  -- 应该返回 1
   ```

### 问题 2：所有模型不可用（no_candidate 错误）

**症状：**
```json
{"error":{"code":"no_candidate","message":"no available provider for model 'xxx'"}}
```

**排查步骤：**

1. **检查模型发现状态**
   ```bash
   docker logs llm-gateway-go | grep "model discovery" | tail -5
   ```
   
   如果看到 `"models":0`，说明模型发现失败。

2. **检查数据库架构**
   ```sql
   -- 检查 plan_type 列是否存在
   \d credentials
   \d credential_model_bindings
   ```

3. **检查可用凭据**
   ```sql
   SELECT 
       c.id,
       c.label,
       c.plan_type,
       COUNT(cmb.id) as model_count
   FROM credentials c
   LEFT JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
   WHERE c.status = 'active'
     AND cmb.available = true
   GROUP BY c.id, c.label, c.plan_type;
   ```

4. **检查具体模型**
   ```sql
   SELECT 
       c.label,
       pm.raw_model_name,
       cmb.available,
       cmb.unavailable_reason
   FROM credential_model_bindings cmb
   JOIN credentials c ON c.id = cmb.credential_id
   JOIN provider_models pm ON pm.id = cmb.provider_model_id
   WHERE pm.raw_model_name = 'your-model-name';
   ```

### 问题 3：计费模式不一致错误

**症状：**
- 日志中出现 "UnsupportedModel" 错误
- 错误信息提到 "coding plan feature"

**排查步骤：**

1. **检查计费模式一致性**
   ```sql
   SELECT 
       c.plan_type,
       cmb.billing_mode,
       COUNT(*) as count
   FROM credentials c
   JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
   GROUP BY c.plan_type, cmb.billing_mode;
   ```

2. **查找不一致数据**
   ```sql
   SELECT 
       c.id,
       c.label,
       c.plan_type,
       cmb.billing_mode,
       COUNT(*) as count
   FROM credentials c
   JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
   WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token')
      OR (c.plan_type IN ('token_plan','code_plan','agent_plan') 
          AND cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan'))
   GROUP BY c.id, c.label, c.plan_type, cmb.billing_mode;
   ```

3. **修复不一致（参考标准化文档）**
   ```sql
   -- 见 docs/billing-mode-standardization.md
   ```

### 问题 4：特定凭据一直失败

**症状：**
- 日志中看到 "anti_flap: marked credential unavailable"
- 某个 credential_id 的请求全部失败

**排查步骤：**

1. **查看凭据状态**
   ```sql
   SELECT 
       id,
       label,
       plan_type,
       status,
       lifecycle_status,
       availability_state,
       quota_state
   FROM credentials
   WHERE id = <credential_id>;
   ```

2. **查看失败历史**
   ```sql
   SELECT 
       model,
       error_kind,
       COUNT(*) as failure_count
   FROM candidate_failure_logs
   WHERE credential_id = <credential_id>
     AND created_at > NOW() - INTERVAL '1 hour'
   GROUP BY model, error_kind
   ORDER BY failure_count DESC
   LIMIT 10;
   ```

3. **重置熔断器**
   ```sql
   UPDATE credentials
   SET circuit_state = 'closed',
       consecutive_failures = 0,
       cooling_until = NULL
   WHERE id = <credential_id>;
   ```

---

## 数据库诊断 SQL

### 统计查询

```sql
-- 1. 凭据和模型统计
SELECT 
    c.plan_type,
    COUNT(DISTINCT c.id) as credential_count,
    COUNT(cmb.id) as total_bindings,
    COUNT(CASE WHEN cmb.available THEN 1 END) as available_bindings
FROM credentials c
LEFT JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE c.status = 'active'
GROUP BY c.plan_type;

-- 2. 最近 1 小时的请求统计
SELECT 
    DATE_TRUNC('minute', created_at) as minute,
    status,
    COUNT(*) as request_count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY DATE_TRUNC('minute', created_at), status
ORDER BY minute DESC
LIMIT 60;

-- 3. 错误类型分布
SELECT 
    error_kind,
    COUNT(*) as count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND error_kind IS NOT NULL
GROUP BY error_kind
ORDER BY count DESC;

-- 4. 热门模型
SELECT 
    client_model,
    COUNT(*) as request_count,
    AVG(duration_ms) as avg_duration_ms
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND status = 200
GROUP BY client_model
ORDER BY request_count DESC
LIMIT 10;
```

---

## 日志查询命令

### 按关键字过滤

```bash
# 查看错误日志
docker logs llm-gateway-go 2>&1 | grep -E "ERROR|WARN" | tail -50

# 查看特定 request_id
docker logs llm-gateway-go 2>&1 | grep "test-trace-001"

# 查看路由决策
docker logs llm-gateway-go 2>&1 | grep "GetCandidates"

# 查看模型发现
docker logs llm-gateway-go 2>&1 | grep "model discovery"
```

### 实时监控

```bash
# 实时查看日志（格式化）
docker logs llm-gateway-go -f 2>&1 | jq -r '"\(.time) [\(.level)] \(.msg)"'

# 只看错误
docker logs llm-gateway-go -f 2>&1 | jq -r 'select(.level == "ERROR" or .level == "WARN")'

# 监控请求
docker logs llm-gateway-go -f 2>&1 | grep "request"
```

---

## 性能诊断

### 1. 检查数据库性能
```sql
-- 慢查询
SELECT 
    query,
    mean_exec_time,
    calls
FROM pg_stat_statements
WHERE mean_exec_time > 100
ORDER BY mean_exec_time DESC
LIMIT 10;

-- 活跃连接
SELECT COUNT(*) as active_connections
FROM pg_stat_activity
WHERE datname = 'llm_gateway';
```

### 2. 检查内存和 CPU
```bash
# 容器资源使用
docker stats llm-gateway-go --no-stream

# 服务器资源
free -h
top -bn1 | head -20
```

---

## 紧急修复步骤

### 场景 1：服务完全不响应

```bash
# 1. 重启容器
docker restart llm-gateway-go

# 2. 等待 10 秒
sleep 10

# 3. 检查健康状态
curl http://localhost:__PORT_3__/healthz

# 4. 测试请求
curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4-flash","messages":[{"role":"user","content":"test"}],"max_tokens":5}'
```

### 场景 2：数据库迁移缺失

```bash
# 1. 检查当前迁移版本
PGPASSWORD='xxx' psql -h __PRIV_IP_2__ -U llm_gateway -d llm_gateway \
  -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;"

# 2. 查看待应用的迁移
ls -la migrations/*.sql | tail -5

# 3. 手动应用迁移（谨慎！）
# 联系 DBA 或高级管理员
```

### 场景 3：所有凭据被熔断

```sql
-- 批量重置熔断器
UPDATE credentials
SET circuit_state = 'closed',
    consecutive_failures = 0,
    cooling_until = NULL
WHERE circuit_state = 'open';

-- 触发缓存刷新
UPDATE credentials SET updated_at = NOW() WHERE id > 0;
```

---

## 联系信息

- **运维团队：** ops@example.com
- **紧急联系：** +86-xxx-xxxx-xxxx
- **文档仓库：** https://github.com/xxx/llm-gateway-go

---

**最后更新：** 2026-07-03  
**维护者：** AI 运维团队
