# 🔴 ROOT CAUSE CONFIRMED: minimax-m3 Missing Endpoint ID

## 执行摘要

**问题**: minimax-m3 通过火山方舟 TokenPlan (Provider 34) 调用时 tools 信息缺失  
**根因**: `provider_models.outbound_model_name` 配置为 `minimax-m3` 而非正确的 Volcano Ark endpoint ID  
**影响**: 所有通过 Provider 34/35 的 minimax-m3 请求可能失败或返回不完整响应  
**状态**: ✅ 根因已确认，等待正确 endpoint ID 配置

---

## 📊 数据库诊断结果

### 当前配置（错误）

```sql
-- Provider 34: 火山方舟 TokenPlan
provider_model_id: 4210
provider_id: 34
raw_model_name: minimax-m3
outbound_model_name: minimax-m3  ⚠️ 应该是 endpoint ID (ep-XXXXXXXX)
available: true
credential_binding_count: 1

-- Provider 35: 火山方舟 普通版
provider_model_id: 4227
provider_id: 35
raw_model_name: minimax-m3
outbound_model_name: minimax-m3  ⚠️ 应该是 endpoint ID (ep-XXXXXXXX)
available: true
credential_binding_count: 1
```

### 代码证据

**文件**: `internal/probeutil/endpoint_id.go:12-17`

```go
// 火山方舟 (Volcano Ark) and similar providers expose
// vendor-supplied models (minimax-m3, glm-5.1, …) only through deployment
// endpoint IDs (ep-XXXXXXXX). Calling them by raw model name returns 404
// with error_code "InvalidEndpointOrModel.NotFound".
```

**错误码**: `InvalidEndpointOrModel.NotFound`

---

## 🔧 修复步骤

### Step 1: 获取正确的 Endpoint ID

登录火山方舟控制台获取 minimax-m3 的 endpoint ID：

1. 访问: https://console.volcengine.com/ark
2. 找到 minimax-m3 模型的推理接入点
3. 复制 endpoint ID（格式: `ep-20250115-xxxxx`）

**示例 endpoint ID 格式**:
- `ep-20250115-xxxxxx`
- `ep-20241201-xxxxxx`

### Step 2: 更新数据库配置

```sql
-- 登录数据库
ssh llm-252
docker exec -it pg-252-pg17 psql -U llm_gateway -d llm_gateway

-- 更新 Provider 34 (TokenPlan) 的配置
UPDATE provider_models
SET outbound_model_name = 'ep-替换为实际的endpoint-id'
WHERE provider_id = 34
AND raw_model_name = 'minimax-m3';

-- 更新 Provider 35 (普通版) 的配置
UPDATE provider_models
SET outbound_model_name = 'ep-替换为实际的endpoint-id'
WHERE provider_id = 35
AND raw_model_name = 'minimax-m3';

-- 验证更新
SELECT 
    provider_id,
    raw_model_name,
    outbound_model_name,
    available
FROM provider_models
WHERE provider_id IN (34, 35)
AND raw_model_name = 'minimax-m3';
```

### Step 3: 重启网关或等待配置刷新

```bash
# 如果网关有配置热更新，等待下次 probe
# 否则重启网关服务
ssh 154
sudo systemctl restart llm-gateway
```

### Step 4: 验证修复

```bash
# 测试 minimax-m3 工具调用
curl https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
  "model": "minimax-m3",
  "max_tokens": 1024,
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "parameters": {
        "type": "object",
        "properties": {
          "location": {"type": "string"}
        },
        "required": ["location"]
      }
    }
  }],
  "messages": [{
    "role": "user",
    "content": "What is the weather in Beijing?"
  }]
}' | jq .

# 检查响应是否包含 tool_calls
```

---

## 📋 其他需要检查的模型

以下模型也使用 Volcano Ark，可能需要类似的 endpoint ID 配置：

```
Provider 34/35:
- deepseek-v4-pro: 当前使用模型名，可能需要 endpoint ID
- glm-4-7-251222: 当前 outbound=glm-4.7，可能需要 endpoint ID
- glm-5.1: 当前 outbound=glm-5-2-260617，可能需要 endpoint ID
```

### 批量检查命令

```sql
-- 检查所有 Volcano Ark 模型配置
SELECT 
    provider_id,
    raw_model_name,
    outbound_model_name,
    CASE 
        WHEN outbound_model_name LIKE 'ep-%' THEN '✅ Endpoint ID'
        WHEN outbound_model_name IS NULL THEN '⚠️ NULL'
        ELSE '❌ Model Name'
    END as config_status
FROM provider_models
WHERE provider_id IN (34, 35, 29)
ORDER BY provider_id, raw_model_name;
```

---

## 🎯 预期结果

修复后：

1. **数据库**:
   ```
   outbound_model_name: ep-20250115-xxxxx (正确的 endpoint ID)
   ```

2. **API 请求**:
   - 网关将使用 endpoint ID 调用 Volcano Ark API
   - 不再返回 `InvalidEndpointOrModel` 错误
   - Tools 信息完整返回

3. **日志**:
   ```
   success: true
   failure_detail_code: NULL
   tool_calls: [...]
   ```

---

## 📌 相关信息

### 问题追踪

- **原始 Request ID**: `0becce7ba3e9e5dcce83e9153d48b522` (数据已清理)
- **报告日期**: 2026-07-25
- **确认日期**: 2026-07-26
- **受影响提供商**: Provider 34 (TokenPlan), Provider 35 (普通版)
- **受影响模型**: minimax-m3

### 相关文档

- `internal/probeutil/endpoint_id.go` - Endpoint ID 检测逻辑
- `docs/diagnostics/minimax-m3-tools-missing-diagnostic.md` - 完整诊断指南
- `docs/diagnostics/minimax-m3-tools-missing-summary.md` - 问题摘要

### Volcano Ark 文档

- 控制台: https://console.volcengine.com/ark
- API 文档: https://www.volcengine.com/docs/82379

---

## ✅ 修复清单

- [x] 确认根因：outbound_model_name 配置错误
- [ ] 从火山方舟控制台获取正确的 endpoint ID
- [ ] 更新 provider_models 表
- [ ] 重启网关或等待配置刷新
- [ ] 验证 minimax-m3 工具调用正常工作
- [ ] 检查其他 Volcano Ark 模型是否需要类似修复

---

**创建时间**: 2026-07-26  
**优先级**: P0 - 阻塞生产使用  
**状态**: 等待 endpoint ID 配置
