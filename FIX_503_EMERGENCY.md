# 模型智商 503 问题 - 紧急修复指南

## 问题现象

重新部署后，仍然报 503 错误：
```
POST /api/admin/model-iq/trigger
Status: 503 Service Unavailable
```

## 诊断步骤

### 步骤 1: 检查数据库设置（最可能的原因）

```bash
# 在服务器上执行
psql -U postgres -d llm_gateway << 'EOF'
SELECT key, value, value_type, updated_at 
FROM settings_kv 
WHERE key = 'model_quality.enabled';
EOF
```

**如果返回**:
```
key                      | value  | value_type | updated_at
model_quality.enabled   | false  | boolean    | ...
```

**说明**：数据库设置覆盖了代码默认值！这就是问题所在。

### 步骤 2: 检查服务是否重启

```bash
# 查看进程启动时间
ps -eo pid,lstart,cmd | grep llm-gateway | grep -v grep

# 应该显示最近的启动时间（今天）
```

### 步骤 3: 检查日志

```bash
# 查找 model_quality 相关日志
grep -i "model_quality" /var/log/llm-gateway/gateway.log | tail -20

# 查找 CHECKPOINT 日志
grep "CHECKPOINT" /var/log/llm-gateway/gateway.log | tail -10
```

**期望看到**:
```
CHECKPOINT: inside bg services enabled block
CHECKPOINT: model_quality_worker started
  data_dir=./data
  base_url=http://localhost:8787
  ...
```

**如果没有看到**，继续下一步。

## 修复方案

### 方案 1: 删除数据库设置（推荐）

删除数据库中的设置，让代码使用默认值：

```sql
-- 在服务器上执行
psql -U postgres -d llm_gateway << 'EOF'
DELETE FROM settings_kv WHERE key = 'model_quality.enabled';
EOF
```

### 方案 2: 显式设置为 true

```sql
-- 在服务器上执行
psql -U postgres -d llm_gateway << 'EOF'
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.enabled', 'true', 'boolean', 'platform', 'model_quality', 'fix_503')
ON CONFLICT (key) DO UPDATE SET 
    value = 'true',
    updated_at = NOW(),
    updated_by = 'fix_503';
EOF
```

### 方案 3: 使用修复脚本

```bash
# 在服务器上执行
cd /path/to/gateway
psql -U postgres -d llm_gateway -f scripts/fix_model_quality_503.sql
```

## 执行修复后

### 1. 重启服务

```bash
systemctl restart llm-gateway
```

### 2. 等待几秒，然后检查日志

```bash
tail -f /var/log/llm-gateway/gateway.log | grep -A 10 "model_quality"
```

**应该看到**:
```
CHECKPOINT: model_quality_worker started
  data_dir=./data
  base_url=http://localhost:8787
  use_dedicated_key=false
  interval_hours=24
  alert_threshold=5
  timeout_seconds=30
  use_lite=true
  per_node=false
```

### 3. 测试 API

```bash
curl -X POST 'https://llm.kxpms.cn/api/admin/model-iq/trigger' \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{
    "credential_id": 42,
    "raw_model_name": "glm-5.2"
  }'
```

**期望**: 返回 200 OK，带测试结果

### 4. 在管理后台测试

1. 访问 https://llm.kxpms.cn/providers/{provider_id}
2. 点击"模型"标签
3. 选择一个模型
4. 点击"测试模型智商"
5. 应该开始测试，不再报 503

## 为什么会有这个问题？

### 原因分析

代码中的初始化逻辑：

```go
// 第 3672-3678 行
mqEnabledRaw, _, _ := settings.Global.EffectiveValue(settings.ScopePlatform, "model_quality.enabled", "")
mqEnabled := true // 代码默认值
if len(mqEnabledRaw) > 0 {
    _ = json.Unmarshal(mqEnabledRaw, &mqEnabled)  // 如果数据库有值，覆盖默认值
}

if mqEnabled {  // 如果数据库设置为 false，这个块不会执行
    // 初始化 worker...
}
```

**关键点**：
- 代码默认值是 `true`
- 但如果数据库中有 `model_quality.enabled = false`，会覆盖默认值
- 导致 worker 不初始化，API 返回 503

### 检查是否有其他类似设置

```sql
SELECT key, value FROM settings_kv WHERE key LIKE 'model_quality.%';
```

所有这些设置都可能影响 worker 的行为。

## 快速命令汇总

```bash
# 1. 检查数据库设置
psql -U postgres -d llm_gateway -c "SELECT key, value FROM settings_kv WHERE key = 'model_quality.enabled';"

# 2. 删除设置（如果返回 false）
psql -U postgres -d llm_gateway -c "DELETE FROM settings_kv WHERE key = 'model_quality.enabled';"

# 3. 重启服务
systemctl restart llm-gateway

# 4. 检查日志
tail -f /var/log/llm-gateway/gateway.log | grep "model_quality_worker started"

# 5. 测试 API（成功后按 Ctrl+C）
# 等待日志出现后，测试 API（在另一个终端）
```

## 其他可能的问题

### 问题 A: 数据库连接失败

如果日志中没有 "CHECKPOINT: inside bg services enabled block"，说明 `dbConn` 为 nil 或未启用。

**检查**:
```bash
grep -i "database\|dbConn\|CHECKPOINT" /var/log/llm-gateway/gateway.log | tail -20
```

### 问题 B: settings.Global 未初始化

如果日志中有错误关于 settings，可能是 settings 包初始化失败。

**检查**:
```bash
grep -i "settings\|error" /var/log/llm-gateway/gateway.log | tail -30
```

### 问题 C: 二进制版本不对

确认部署的是正确的二进制：

```bash
# 在服务器上
cd /path/to/gateway
md5sum llm-gateway

# 在本地
md5sum /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/llm-gateway

# 两者应该相同
```

## 联系支持

如果以上步骤都无法解决，请提供：

1. 数据库查询结果：
   ```sql
   SELECT key, value FROM settings_kv WHERE key LIKE 'model_quality.%';
   ```

2. 最近 50 行日志：
   ```bash
   tail -50 /var/log/llm-gateway/gateway.log
   ```

3. 进程信息：
   ```bash
   ps aux | grep llm-gateway
   ```

4. 二进制 MD5：
   ```bash
   md5sum /path/to/gateway/llm-gateway
   ```

---

**最可能的解决方案**: 删除数据库中的 `model_quality.enabled` 设置，然后重启服务。
