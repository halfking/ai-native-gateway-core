# 模型智商检测修复说明

## 问题

在管理后台点击"模型智商"检测时，报错：
```
model-quality worker not configured
暂无测试记录
```

## 根本原因

模型质量服务默认未启用。代码中 `mqEnabled` 默认为 `false`，需要在配置中显式启用。

## 修复方案

### 方案 1: 代码修改（已实施）

修改 `cmd/gateway/main.go:3670-3677`，将默认值从 `false` 改为 `true`：

**修改前**：
```go
// Controlled by settings.model_quality.enabled (default false).
mqEnabledRaw, _, _ := settings.Global.EffectiveValue(settings.ScopePlatform, "model_quality.enabled", "")
var mqEnabled bool  // 默认 false
if len(mqEnabledRaw) > 0 {
    _ = json.Unmarshal(mqEnabledRaw, &mqEnabled)
}
```

**修改后**：
```go
// Controlled by settings.model_quality.enabled (default true).
// Can be disabled by setting model_quality.enabled = false in settings_kv.
mqEnabledRaw, _, _ := settings.Global.EffectiveValue(settings.ScopePlatform, "model_quality.enabled", "")
mqEnabled := true  // 默认 true
if len(mqEnabledRaw) > 0 {
    _ = json.Unmarshal(mqEnabledRaw, &mqEnabled)
}
```

### 方案 2: 数据库配置（可选）

如果不想修改代码，可以在数据库中启用：

```sql
-- 执行 scripts/enable_model_quality.sql
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.enabled', 'true', 'boolean', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = 'true',
    updated_at = NOW();
```

然后重启网关。

## 配置说明

模型质量服务的完整配置项（都有合理默认值）：

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `model_quality.enabled` | `true`（修复后） | 是否启用模型质量检测 |
| `model_quality.base_url` | `http://localhost:8787` | 测试端点 URL（通常使用网关自身） |
| `model_quality.api_key` | 使用 `selfCheckAPIKey` | API 密钥（可选） |
| `model_quality.data_dir` | `./data` | 数据存储目录 |
| `model_quality.interval_hours` | `24` | 定期检测间隔（小时） |
| `model_quality.test_timeout_seconds` | `30` | 单次测试超时（秒） |
| `model_quality.use_lite_benchmark` | `true` | 使用轻量级基准测试 |
| `model_quality.enable_per_node` | `false` | 是否启用按节点测试 |
| `model_quality.alert_threshold` | `5.0` | 质量下降告警阈值 |

## 部署后验证

### 1. 检查服务是否启动

启动网关后，查看日志中是否有：

```
CHECKPOINT: model_quality_worker started
  data_dir=./data
  base_url=http://localhost:8787
  interval_hours=24
  ...
```

### 2. 测试模型智商检测

在管理后台：
1. 进入 `https://llm.kxpms.cn/providers/{provider_id}`
2. 点击"模型"标签
3. 选择一个模型并点击打开详情抽屉
4. 点击"测试模型智商"按钮
5. 应该开始测试，不再报 "worker not configured" 错误

### 3. API 测试

```bash
curl -X POST "https://llm.kxpms.cn/api/admin/model-iq/trigger" \
  -H "Authorization: Bearer ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "credential_id": 42,
    "raw_model_name": "glm-5.2"
  }'
```

应该返回测试结果（分数、准确率等），而不是 503 错误。

### 4. 查看历史记录

```sql
-- 查看测试历史
SELECT 
    credential_id,
    raw_model_name,
    overall_score,
    accuracy,
    tested_at
FROM model_iq_runs
ORDER BY tested_at DESC
LIMIT 10;
```

## 功能说明

### 模型智商检测是什么？

模型智商（Model IQ）检测通过一系列标准化的问答测试，评估模型的：
- **准确性**（Accuracy）：回答正确率
- **综合得分**（Overall Score）：综合评分
- **等级**（Grade）：A/B/C/D 等级

### 测试流程

1. 从标准题库中选择测试题（lite 模式使用精简题库）
2. 向模型发送测试请求
3. 评估模型回答的准确性
4. 计算综合得分并存储
5. 在管理后台展示历史趋势图

### 使用场景

1. **手动测试**：在模型详情抽屉中点击"测试模型智商"
2. **定期自动测试**：后台定时任务自动测试（interval_hours 配置）
3. **异常触发测试**：检测到模型质量下降时自动触发

### 注意事项

1. **测试会消耗真实 token**：每次测试会调用实际的模型 API
2. **测试需要时间**：单次测试通常需要 10-30 秒
3. **测试频率**：建议不要过于频繁（默认 24 小时一次）
4. **网络要求**：需要能访问 `base_url` 指定的端点

## 故障排查

### 问题 1: 仍然报 "worker not configured"

**检查**：
```sql
SELECT value FROM settings_kv WHERE key = 'model_quality.enabled';
```

**解决**：
- 如果返回 `false` 或无结果，执行 `scripts/enable_model_quality.sql`
- 重启网关
- 或者部署修复后的代码（默认启用）

### 问题 2: 测试超时或失败

**检查**：
- `base_url` 是否可访问
- `api_key` 是否有效
- 凭据配额是否充足

**解决**：
```sql
-- 调整超时时间
UPDATE settings_kv 
SET value = '60' 
WHERE key = 'model_quality.test_timeout_seconds';
```

### 问题 3: 测试结果不存储

**检查**：
- 数据库连接是否正常
- `model_iq_runs` 表是否存在

**解决**：
```sql
-- 检查表是否存在
SELECT EXISTS (
    SELECT FROM information_schema.tables 
    WHERE table_name = 'model_iq_runs'
);

-- 如果不存在，可能需要运行迁移
```

## 禁用模型质量服务

如果确实不需要该功能，可以禁用：

```sql
UPDATE settings_kv 
SET value = 'false' 
WHERE key = 'model_quality.enabled';
```

或在配置文件中设置（如果使用配置文件）。

## 相关代码位置

- 初始化逻辑: `cmd/gateway/main.go:3670-3788`
- 后端 API: `admin/model_iq.go:255-283`
- 前端组件: `web/src/components/model/ModelOfferExtrasPanel.vue`
- Worker 实现: `bg/model_quality_worker.go`
- 数据存储: `domains/modelquality/db_storage.go`

## 总结

修复后，模型智商检测功能将默认启用，管理员可以直接在后台测试模型质量，无需额外配置。如需禁用，可以通过数据库配置关闭。
