# Request Logs 写入失败修复

## 问题描述

**症状**：
- Gateway audit 日志显示请求成功，但 `request_logs` 表没有新记录
- `/routing-v2` 页面显示空数据
- 日志中出现 telemetry 写入错误

**错误日志**：
```
telemetry request db persist failed
ERROR: column "auto_decision" is of type jsonb but expression is of type text (SQLSTATE 42804)
ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)
ERROR: null value in column "usage_source" violates not-null constraint (SQLSTATE 23502)
```

## 根本原因

### 1. `auto_decision` 类型不匹配
- **问题**：`auto_decision` 列是 `jsonb` 类型，但代码传入 `string` 类型
- **位置**：
  - `telemetry/client.go:731` - UPDATE 语句
  - `telemetry/client.go:985` - INSERT 语句

### 2. ON CONFLICT 约束不匹配
- **问题**：代码使用 `ON CONFLICT (request_id)`，但数据库的唯一索引是 `(request_id, ts)`
- **位置**：`telemetry/client.go:415`

### 3. `usage_source` NULL 约束违反
- **问题**：`usage_source` 列是 NOT NULL，但 COALESCE 可能返回 NULL
- **位置**：
  - `telemetry/client.go:710` - UPDATE 语句
  - `telemetry/client.go:981` - INSERT 语句

## 修复方案

### 修改文件：`telemetry/client.go`

#### 1. 修复 UPDATE 语句中的 auto_decision（第 731 行）
```go
// 修复前：
auto_decision = COALESCE($47, auto_decision),

// 修复后：
auto_decision = COALESCE(CAST($47 AS jsonb), auto_decision),
```

#### 2. 修复 UPDATE 语句中的 usage_source（第 710 行）
```go
// 修复前：
usage_source = COALESCE(NULLIF($32, ''), usage_source),

// 修复后：
usage_source = COALESCE(NULLIF($32, ''), usage_source, 'llm'),
```

#### 3. 修复 INSERT 语句中的 auto_decision（第 985 行）
```go
// 修复前：
COALESCE(NULLIF($46, ''), NULL), $47,

// 修复后：
CAST(NULLIF($46, '') AS jsonb), $47,
```

#### 4. 修复 INSERT 语句中的 usage_source（第 981 行）
```go
// 修复前：
COALESCE(NULLIF($37, ''), NULL),

// 修复后：
COALESCE(NULLIF($37, ''), 'llm'),
```

#### 5. 修复 ON CONFLICT 子句（第 415 行）
```go
// 修复前：
ON CONFLICT (request_id) DO NOTHING

// 修复后：
ON CONFLICT (request_id, ts) DO NOTHING
```

## 部署步骤

### 在 71 服务器上：

```bash
# 1. 进入源代码目录
cd /opt/official-deploy/services/llm-gateway-go

# 2. 备份当前文件
cp telemetry/client.go telemetry/client.go.backup.$(date +%Y%m%d-%H%M%S)

# 3. 上传修复后的文件（从本地）
# scp telemetry/client.go root@__HOST_71_IP__:/opt/official-deploy/services/llm-gateway-go/telemetry/

# 4. 验证修改
grep -n "auto_decision.*CAST" telemetry/client.go
grep -n "usage_source.*'llm'" telemetry/client.go
grep -n "ON CONFLICT.*request_id.*ts" telemetry/client.go

# 5. 重新构建镜像（强制无缓存）
docker build --no-cache -t kx-llm-gateway-go:fix-request-logs .

# 6. 停止旧容器
docker stop llm-gateway-go
docker rm llm-gateway-go

# 7. 启动新容器
docker run -d \
  --name llm-gateway-go \
  --network host \
  --env-file /etc/llm-gateway-go/env \
  -v /opt/llm-gateway-go/data:/opt/llm-gateway-go/data \
  --restart unless-stopped \
  kx-llm-gateway-go:fix-request-logs

# 8. 检查日志
docker logs llm-gateway-go --tail 50

# 9. 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"test"}],"max_tokens":10}'

# 10. 验证数据库记录
docker exec llm-gateway-pg-71-replica psql -U kxuser -d llm_gateway -c "
SELECT 
    to_char(ts, 'HH24:MI:SS') AS time,
    substring(request_id, 1, 12) AS req_id,
    client_model,
    success,
    usage_source
FROM request_logs
WHERE ts > now() - interval '1 minute'
ORDER BY ts DESC;
"
```

## 验证清单

- [ ] 无 telemetry 错误日志
- [ ] `request_logs` 表有新记录
- [ ] `usage_source` 列有值（'llm'）
- [ ] `auto_decision` 列可以存储 jsonb 数据
- [ ] `/routing-v2` 页面显示数据

## 相关提交

- 修复 `auto_decision` jsonb 类型转换
- 修复 `usage_source` NOT NULL 约束
- 修复 ON CONFLICT 唯一索引匹配

## 注意事项

1. **必须使用 `--no-cache` 构建镜像**，否则可能使用缓存的旧代码
2. **停止 systemd 服务**，避免自动重启旧版本：`systemctl disable llm-gateway-go.service`
3. 修复后的代码已向后兼容，不会影响现有功能
