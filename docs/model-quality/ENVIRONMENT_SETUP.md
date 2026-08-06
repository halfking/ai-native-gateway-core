# 模型质量监控 - 环境配置与验证指南

## 概述

模型质量监控系统已完全集成到settings配置系统中，支持针对不同环境（本地、245测试、154生产）进行独立配置和验证。

## 配置项说明

### 核心配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `model_quality.enabled` | bool | false | 是否启用质量监控 |
| `model_quality.interval_hours` | int | 24 | 检测周期（小时） |
| `model_quality.use_lite_benchmark` | bool | true | 是否使用快速测试（50题） |
| `model_quality.alert_threshold` | float | 5.0 | 告警阈值（准确率下降%） |
| `model_quality.data_dir` | string | ./data | 数据存储目录 |
| `model_quality.api_key` | string | "" | 专用API Key（空则用系统key） |
| `model_quality.base_url` | string | http://localhost:8787 | 网关地址 |
| `model_quality.test_timeout_seconds` | int | 30 | 单个测试超时（秒） |

### 配置优先级

```
settings_kv (数据库) > 默认值
```

## 环境配置

### 1. 本地开发环境

**配置特点**：
- 使用本地网关地址
- 检测周期短（6小时，便于测试）
- 使用系统API key（无需专用key）
- 数据存储在本地目录

**初始化步骤**：

```bash
# 1. 执行SQL脚本配置
psql -d llm_gateway -f scripts/setup_model_quality_local.sql

# 2. 验证配置
psql -d llm_gateway -c "
SELECT key, value 
FROM settings_kv 
WHERE category = 'model_quality' 
ORDER BY key;
"

# 3. 重启gateway生效
./gateway

# 4. 验证启动
tail -f gateway.log | grep model_quality
```

**预期日志**：
```
INFO model quality worker: using real gateway invoker base_url=http://localhost:8787
INFO CHECKPOINT: model_quality_worker started data_dir=./data/model-quality base_url=http://localhost:8787
```

---

### 2. 245测试环境

**配置特点**：
- 使用245网关地址 (http://10.177.48.245:8787)
- 检测周期24小时
- 建议使用专用API key
- 数据存储在服务器目录

**初始化步骤**：

```bash
# 1. SSH到245服务器
ssh user@10.177.48.245

# 2. 执行SQL脚本配置
psql -h 10.177.48.245 -d llm_gateway -f scripts/setup_model_quality_245.sql

# 3. 配置专用API key（重要！）
# 首先创建专用API key（用于单独追踪质量测试的token消耗）
# 然后更新配置
psql -h 10.177.48.245 -d llm_gateway -c "
UPDATE settings_kv 
SET value = '你的专用API_KEY', updated_at = NOW() 
WHERE key = 'model_quality.api_key';
"

# 4. 创建数据目录
mkdir -p /data/llm-gateway/model-quality
chmod 755 /data/llm-gateway/model-quality

# 5. 验证配置
psql -h 10.177.48.245 -d llm_gateway -c "
SELECT key, value, updated_at
FROM settings_kv 
WHERE category = 'model_quality' 
ORDER BY key;
"

# 6. 重启gateway
sudo systemctl restart llm-gateway
# 或
./gateway

# 7. 验证启动
tail -f /var/log/llm-gateway/gateway.log | grep model_quality
```

**验证清单**：
- [ ] ✅ `model_quality.enabled` = true
- [ ] ✅ `model_quality.base_url` = http://10.177.48.245:8787
- [ ] ✅ `model_quality.api_key` 已配置（非空）
- [ ] ✅ 数据目录存在且可写
- [ ] ✅ 日志中看到 worker started

---

### 3. 154生产环境

**配置特点**：
- 使用154生产网关地址 (http://10.177.48.154:8787)
- 检测周期24小时
- **必须**使用专用API key
- 告警阈值更敏感（3%）
- 超时时间更长（45秒）

**初始化步骤**：

```bash
# 1. SSH到154服务器
ssh user@10.177.48.154

# 2. 执行SQL脚本配置
psql -h 10.177.48.154 -d llm_gateway -f scripts/setup_model_quality_154.sql

# 3. ⚠️ 必须配置专用API key
# 生产环境必须使用专用key，便于:
# - 单独追踪token消耗
# - 控制测试成本
# - 隔离测试流量

# 创建专用API key
psql -h 10.177.48.154 -d llm_gateway -c "
-- 创建专用应用或使用现有测试应用的key
-- 然后更新配置
UPDATE settings_kv 
SET value = '生产环境专用API_KEY', updated_at = NOW() 
WHERE key = 'model_quality.api_key';
"

# 4. 创建数据目录
mkdir -p /data/llm-gateway/model-quality
chmod 755 /data/llm-gateway/model-quality

# 5. 验证配置（含安全检查）
psql -h 10.177.48.154 -d llm_gateway -c "
SELECT 
    key, 
    CASE WHEN key = 'model_quality.api_key' 
         THEN '***MASKED***' 
         ELSE value END AS value,
    updated_at
FROM settings_kv 
WHERE category = 'model_quality' 
ORDER BY key;
"

# 6. 生产环境检查清单
psql -h 10.177.48.154 -d llm_gateway -c "
SELECT 
    CASE 
        WHEN (SELECT value FROM settings_kv WHERE key = 'model_quality.api_key') = '' 
        THEN '❌ ERROR: 生产环境必须配置专用API key'
        ELSE '✓ API key已配置'
    END AS api_key_check,
    CASE 
        WHEN (SELECT value FROM settings_kv WHERE key = 'model_quality.base_url') LIKE '%154%' 
        THEN '✓ base_url配置正确'
        ELSE '❌ ERROR: base_url应该指向154地址'
    END AS url_check;
"

# 7. 重启gateway（生产环境请在维护窗口进行）
sudo systemctl restart llm-gateway

# 8. 验证启动
tail -f /var/log/llm-gateway/gateway.log | grep model_quality
```

**生产环境验证清单**：
- [ ] ✅ `model_quality.enabled` = true
- [ ] ✅ `model_quality.base_url` = http://10.177.48.154:8787
- [ ] ✅ `model_quality.api_key` 已配置专用key（非空）
- [ ] ✅ `model_quality.alert_threshold` = 3.0 (更敏感)
- [ ] ✅ 数据目录存在且可写
- [ ] ✅ 日志中看到 worker started 且 use_dedicated_key=true
- [ ] ✅ 已设置监控告警

---

## 验证方法

### 1. 配置验证

```bash
# 查看所有配置
psql -d llm_gateway -c "
SELECT 
    key, 
    value, 
    scope, 
    category, 
    updated_at 
FROM settings_kv 
WHERE category = 'model_quality' 
ORDER BY key;
"
```

### 2. Worker启动验证

```bash
# 查看启动日志
grep "model_quality_worker" gateway.log

# 预期输出示例
# INFO model quality worker: using real gateway invoker base_url=...
# INFO model quality worker started models=12 interval=24h0m0s storage=...
# INFO CHECKPOINT: model_quality_worker started data_dir=... base_url=... use_dedicated_key=true
```

### 3. 功能验证

```bash
# 等待worker启动后立即执行第一次测试（约1-2分钟）
# 然后查看生成的数据

# 查看测试报告
ls -lh /data/llm-gateway/model-quality/reports/

# 查看评分历史
ls -lh /data/llm-gateway/model-quality/scores/
cat /data/llm-gateway/model-quality/scores/*.jsonl | jq '.'

# 查看告警日志
cat /data/llm-gateway/model-quality/alerts.log | jq '.'
```

### 4. API调用验证

```bash
# 查看gateway日志中的质量测试请求
grep "X-Gateway-Quality-Test" gateway.log

# 验证token消耗
# 如果使用专用API key，可以单独查看该key的使用情况
```

---

## 环境对比表

| 配置项 | 本地 | 245测试 | 154生产 |
|--------|------|---------|---------|
| enabled | true | true | true |
| interval_hours | 6 | 24 | 24 |
| alert_threshold | 5.0 | 5.0 | 3.0 |
| data_dir | ./data/model-quality | /data/llm-gateway/model-quality | /data/llm-gateway/model-quality |
| base_url | http://localhost:8787 | http://10.177.48.245:8787 | http://10.177.48.154:8787 |
| api_key | (空,用系统key) | 专用key | **必须**专用key |
| test_timeout | 30秒 | 30秒 | 45秒 |

---

## 故障排查

### 问题1: Worker未启动

**症状**：日志中看不到 "model_quality_worker started"

**排查步骤**：
```bash
# 1. 检查配置是否启用
psql -d llm_gateway -c "
SELECT value FROM settings_kv WHERE key = 'model_quality.enabled';
"

# 2. 检查系统API key是否可用
grep "selfCheckAPIKey" gateway.log

# 3. 重启gateway并查看详细日志
./gateway 2>&1 | grep -i "model_quality\|error"
```

### 问题2: 测试失败

**症状**：报告中显示大量错误

**排查步骤**：
```bash
# 1. 检查base_url配置
psql -d llm_gateway -c "
SELECT value FROM settings_kv WHERE key = 'model_quality.base_url';
"

# 2. 检查API key是否有效
curl -H "Authorization: Bearer YOUR_API_KEY" \
     http://10.177.48.245:8787/v1/models

# 3. 查看详细错误日志
cat /data/llm-gateway/model-quality/alerts.log | jq '.'
```

### 问题3: 配置未生效

**症状**：修改配置后仍使用旧值

**排查步骤**：
```bash
# 1. 确认配置已更新
psql -d llm_gateway -c "
SELECT key, value, updated_at 
FROM settings_kv 
WHERE key LIKE 'model_quality.%'
ORDER BY updated_at DESC;
"

# 2. 检查HotReload是否支持
# model_quality.data_dir 不支持热更新，需要重启

# 3. 重启gateway
sudo systemctl restart llm-gateway
```

---

## 最佳实践

### 1. API Key管理

**本地开发**：
- 可以不配置专用key，使用系统key
- 适合快速测试和开发

**测试环境**：
- 建议配置专用key
- 便于追踪token消耗

**生产环境**：
- **必须**配置专用key
- 设置token限额
- 配置监控告警

### 2. 监控周期

**建议配置**：
- 本地: 6小时（快速验证）
- 测试: 24小时
- 生产: 24小时

**特殊场景**：
- 新模型接入: 可临时设置为1-6小时，密集监控
- 质量下降: 可临时设置为6小时，加强监控
- 稳定期: 保持24小时

### 3. 数据管理

**定期清理**：
```bash
# 清理90天前的测试报告
find /data/llm-gateway/model-quality/reports/ -name "*.json" -mtime +90 -delete

# 评分历史(JSONL)建议永久保留，文件很小
```

---

## 相关文档

- 完整文档: `docs/model-quality/README.md`
- 集成指南: `docs/model-quality/GATEWAY_INTEGRATION.md`
- 特色模型指南: `docs/model-quality/FEATURED_MODELS_GUIDE.md`
