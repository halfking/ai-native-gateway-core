# 模型智商 503 问题 - 最终诊断

## 🔴 当前状态：部署后仍然 503

### 最可能的原因

**数据库设置覆盖了代码默认值**

即使我们修改了代码让 `mqEnabled := true`，如果数据库中存在：
```sql
settings_kv.key = 'model_quality.enabled'
settings_kv.value = false
```

这个设置会覆盖代码默认值，导致 worker 不初始化。

## ⚡ 快速修复（3 步）

### 在服务器上执行：

```bash
# 方式 1: 使用一键脚本（推荐）
cd /path/to/gateway
bash scripts/fix_503_one_click.sh
```

或手动执行：

```bash
# 1. 删除数据库设置
psql -U postgres -d llm_gateway -c "DELETE FROM settings_kv WHERE key = 'model_quality.enabled';"

# 2. 重启服务
systemctl restart llm-gateway

# 3. 验证日志（等待 5 秒后）
tail -f /var/log/llm-gateway/gateway.log | grep "model_quality_worker started"
```

**期望输出**：
```
CHECKPOINT: model_quality_worker started
  data_dir=./data
  base_url=http://localhost:8787
  interval_hours=24
  ...
```

看到此日志后按 Ctrl+C，然后测试 API。

## 🧪 测试验证

```bash
curl -X POST 'https://llm.kxpms.cn/api/admin/model-iq/trigger' \
  -H 'Authorization: Bearer YOUR_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{"credential_id": 42, "raw_model_name": "glm-5.2"}'
```

**期望**：返回 200 OK，不再是 503

## 📋 详细诊断步骤

如果快速修复无效，按以下步骤诊断：

### 1. 检查数据库设置

```sql
SELECT key, value, value_type, updated_at, updated_by
FROM settings_kv 
WHERE key = 'model_quality.enabled';
```

**如果返回 `false`**：这就是问题！执行快速修复。

**如果返回 `true` 或空**：继续下一步。

### 2. 检查服务是否真的重启了

```bash
# 查看进程启动时间
ps -eo pid,lstart,cmd | grep llm-gateway | grep -v grep
```

**启动时间应该是最近的**（几分钟内）

### 3. 检查数据库连接

Worker 初始化在 `if dbConn != nil && dbConn.Enabled()` 块内，如果数据库连接失败，整个块不会执行。

```bash
# 查找数据库连接日志
grep -i "database\|dbConn\|CHECKPOINT: inside bg services" /var/log/llm-gateway/gateway.log | tail -10
```

**应该看到**：
```
CHECKPOINT: inside bg services enabled block
```

**如果没看到**：数据库连接有问题。

### 4. 检查完整的启动日志

```bash
# 查看最近 100 行日志
tail -100 /var/log/llm-gateway/gateway.log
```

查找：
- 错误信息（ERROR）
- 数据库连接问题
- settings 初始化问题

## 🔧 其他可能的修复

### 修复 A: 显式设置为 true（而不是删除）

```sql
UPDATE settings_kv 
SET value = 'true', updated_at = NOW() 
WHERE key = 'model_quality.enabled';

-- 如果不存在，插入
INSERT INTO settings_kv (key, value, value_type, scope, category)
VALUES ('model_quality.enabled', 'true', 'boolean', 'platform', 'model_quality')
ON CONFLICT (key) DO UPDATE SET value = 'true';
```

### 修复 B: 检查其他 model_quality 设置

```sql
SELECT key, value FROM settings_kv WHERE key LIKE 'model_quality.%';
```

如果有其他异常设置，也可能导致问题。

### 修复 C: 重新部署二进制

确认部署的是正确版本：

```bash
# 在服务器上
md5sum /path/to/gateway/llm-gateway

# 在本地
md5sum /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/llm-gateway

# 应该相同
```

如果不同，重新上传二进制。

## 📞 如果还是不行

收集以下信息后联系支持：

```bash
# 1. 数据库设置
psql -U postgres -d llm_gateway -c "SELECT key, value FROM settings_kv WHERE key LIKE 'model_quality.%';"

# 2. 最近日志
tail -100 /var/log/llm-gateway/gateway.log > /tmp/gateway_logs.txt

# 3. 进程信息
ps aux | grep llm-gateway > /tmp/process_info.txt

# 4. 服务状态
systemctl status llm-gateway > /tmp/service_status.txt

# 5. 二进制信息
ls -lh /path/to/gateway/llm-gateway
md5sum /path/to/gateway/llm-gateway
```

## 📚 相关文档

- `FIX_503_EMERGENCY.md` - 详细诊断指南
- `scripts/fix_model_quality_503.sql` - SQL 修复脚本
- `scripts/fix_503_one_click.sh` - 一键修复脚本

## ✅ 成功标志

修复成功后应该看到：

1. ✓ 日志中有 "model_quality_worker started"
2. ✓ API 返回 200（不是 503）
3. ✓ 管理后台可以测试模型智商
4. ✓ 测试结果存储在 `model_iq_runs` 表中

---

**下一步**：在服务器上执行 `bash scripts/fix_503_one_click.sh`
