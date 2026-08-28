# 凭据检测问题分析与解决方案

## 问题 1: 模型智商检测报错 "model-quality worker not configured"

### 问题原因

`h.modelQualityBackend == nil` - 模型质量后端未初始化

### 初始化逻辑

在 `cmd/gateway/main.go` 中：

```go
// 第 3678-3759 行：初始化 ModelQualityWorker
var mqDataDir, mqBaseURL, mqAPIKey string
// ... 从配置读取参数 ...

modelQualityWorker = bg.NewModelQualityWorker(
    mqDataDir, 
    mqAPIKey, 
    mqBaseURL, 
    time.Duration(mqTimeoutSec)*time.Second
)

// 第 4459 行：注入到 admin handler
adminHandler.SetModelQualityBackend(modelQualityWorker)
```

### 配置参数

需要在配置中设置（配置格式待确认）：
- `mqDataDir`: 默认 `./data`
- `mqBaseURL`: 默认 `http://localhost:8787`
- `mqAPIKey`: 默认使用 `selfCheckAPIKey`

### 解决方案

#### 方案 1: 检查配置文件

确认配置文件中是否正确设置了模型质量服务的配置：

```yaml
# config.yaml 或相关配置文件
model_quality:
  data_dir: ./data
  base_url: http://localhost:8787  # 或实际的质量检测服务地址
  api_key: YOUR_API_KEY  # 可选，默认使用 self_check_api_key
  timeout_sec: 30
```

#### 方案 2: 启动模型质量服务

如果 `mqBaseURL` 指向 `http://localhost:8787`，需要确保该服务正在运行。

#### 方案 3: 使用网关自身进行测试（临时方案）

修改配置，让 `mqBaseURL` 指向网关自身：

```yaml
model_quality:
  base_url: http://localhost:8080  # 网关自身地址
  api_key: YOUR_GATEWAY_API_KEY
```

### 验证步骤

1. 检查日志中的模型质量服务初始化信息
2. 确认 `adminHandler.SetModelQualityBackend` 是否被调用
3. 测试 API 端点：
   ```bash
   curl -X POST "https://llm.kxpms.cn/api/admin/model-iq/trigger" \
     -H "Authorization: Bearer ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "credential_id": 42,
       "raw_model_name": "glm-5.2"
     }'
   ```

---

## 问题 2: 凭据检测未轮换到可用凭据

### 问题现象

检查所有凭据时：
- 凭据 A: `quota exhausted` (配额用完)
- 凭据 B: 有大量可用配额
- 但仍然报错，没有自动切换到凭据 B

### 问题分析

从代码 `admin/provider_cred_lifecycle.go:170-262` (`doHealthCheck`) 分析：

**当前逻辑**：
1. 检测时使用**单个凭据**进行探测
2. 如果该凭据配额耗尽，直接返回失败
3. **不会**自动尝试其他凭据

**前端调用逻辑** (`ModelOfferExtrasPanel.vue:117-193`)：
```typescript
async function checkAcross() {
  const credentials = await getProviderCredentials(props.providerId)
  checkResults.value = await Promise.all(credentials.map(async (cred) => {
    // 逐个检查每个凭据
    const result = await checkCredential(props.providerId, cred.id, modelName)
    // ...
  }))
}
```

### 根本原因

**这不是 bug，而是设计行为**：

"检查所有凭据"功能的目的是：
- **诊断**每个凭据的状态
- **显示**哪些凭据可用、哪些不可用
- **不会**自动轮换到其他凭据

### 正确的理解

从错误信息看：
```
augeste - unavailable - 模型列表中无此模型
sp1-2   - error       - quota exhausted (HTTP 403)
```

这个结果是**正确的诊断结果**：
- `augeste` 凭据：没有该模型
- `sp1-2` 凭据：配额用完

### 如何让系统使用可用凭据

系统**自动路由**功能会在实际请求时选择可用凭据：

#### 1. 路由选择逻辑

在 `gateway/routing/` 中，路由器会：
- 过滤掉 `quota_state = 'permanently_exhausted'` 的凭据
- 过滤掉 `quota_state = 'balance_exhausted'` 的凭据
- 优先选择健康的凭据

#### 2. 配额状态更新

配额耗尽后，系统应该自动更新 `credentials.quota_state`：

```sql
UPDATE credentials 
SET quota_state = 'permanently_exhausted',
    quota_recover_at = NULL
WHERE id = {credential_id};
```

#### 3. 验证路由是否正常工作

检查路由视图中的可用凭据：

```sql
-- 查看该供应商下哪些凭据×模型可路由
SELECT 
    c.id AS credential_id,
    c.label,
    c.status,
    c.quota_state,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason
FROM v_routable_credential_models vr
JOIN credentials c ON c.id = vr.credential_id
JOIN provider_models pm ON pm.id = vr.provider_model_id
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id AND cmb.provider_model_id = pm.id
WHERE c.provider_id = {provider_id}
  AND pm.raw_model_name = 'glm-5.2'
ORDER BY c.id;
```

### 期望的凭据状态

**配额用完的凭据 (sp1-2)**：
```sql
UPDATE credentials 
SET quota_state = 'permanently_exhausted',
    status = 'disabled',
    lifecycle_status = 'disabled',
    state_reason_code = 'quota_exhausted',
    state_reason_detail = 'call quota exhausted: used=60000 limit=60000',
    state_updated_at = NOW()
WHERE id = {sp1-2_credential_id};
```

**可用的凭据 (有大量配额的那个)**：
```sql
-- 确保状态正常
UPDATE credentials 
SET quota_state = 'ok',
    status = 'active',
    lifecycle_status = 'active',
    availability_state = 'ready'
WHERE id = {available_credential_id};
```

### 解决步骤

#### 1. 检查配额用完凭据的状态

```sql
SELECT 
    id,
    label,
    status,
    lifecycle_status,
    availability_state,
    quota_state,
    state_reason_code,
    state_reason_detail
FROM credentials
WHERE provider_id = {provider_id}
  AND label = 'sp1-2';
```

#### 2. 如果 `quota_state` 不是 'permanently_exhausted'，手动更新

```sql
UPDATE credentials 
SET quota_state = 'permanently_exhausted',
    state_reason_code = 'quota_exhausted',
    state_reason_detail = 'call quota exhausted: used=60000 limit=60000',
    state_updated_at = NOW()
WHERE id = {sp1-2_credential_id};
```

#### 3. 检查可用凭据是否在路由中

```sql
-- 查看可路由的模型绑定
SELECT * FROM v_routable_credential_models
WHERE provider_id = {provider_id}
  AND credential_id = {available_credential_id}
  AND raw_model_name = 'glm-5.2';
```

如果返回空，检查：
- `credentials.status` 是否为 'active'
- `credentials.manual_disabled` 是否为 FALSE
- `credential_model_bindings.available` 是否为 TRUE
- 该模型绑定是否存在

#### 4. 测试实际路由

```bash
curl -X POST "http://qiyovo.com:3000/v1/chat/completions" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "test"}],
    "max_tokens": 10
  }'
```

观察日志，确认使用的是可用凭据而非配额用完的凭据。

### 自动化配额状态更新

系统应该有后台任务自动更新配额状态。检查是否有相关的错误处理器：

```sql
-- 查看是否有配额错误处理记录
SELECT * FROM model_offer_events
WHERE credential_id IN (
    SELECT id FROM credentials WHERE provider_id = {provider_id}
)
AND reason_code LIKE '%quota%'
ORDER BY created_at DESC
LIMIT 10;
```

### 功能增强建议

#### 建议 1: 在健康检查中更新配额状态

修改 `admin/provider_cred_lifecycle.go:doHealthCheck`，当检测到配额错误时：

```go
if strings.Contains(probeError, "quota exhausted") || 
   strings.Contains(probeError, "pre_consume_token_quota_failed") {
    // 更新配额状态
    h.db.Exec(ctx, `
        UPDATE credentials 
        SET quota_state = 'permanently_exhausted',
            state_reason_code = 'quota_exhausted',
            state_reason_detail = $1,
            state_updated_at = NOW()
        WHERE id = $2
    `, probeError, credID)
}
```

#### 建议 2: 前端显示优化

在 `ModelOfferExtrasPanel.vue` 中，对配额错误特殊标记：

```typescript
if (result.probe_error && result.probe_error.includes('quota exhausted')) {
  phase2Message = pm('quotaExhausted') + ': ' + result.probe_error
  // 建议手动禁用该凭据
}
```

### 总结

1. **"model-quality worker not configured"**：需要配置模型质量服务或使用网关自身
2. **凭据未轮换**：这是诊断功能的正常行为，不是 bug
3. **实际路由会自动选择可用凭据**：前提是配额状态已正确更新
4. **手动更新配额用完凭据的状态**：确保路由器能正确过滤

### 相关代码位置

- 健康检查: `admin/provider_cred_lifecycle.go:170-290`
- 路由过滤: `admin/provider_refresh.go:266-303`
- 前端检测: `web/src/components/model/ModelOfferExtrasPanel.vue:117-193`
- 模型质量: `admin/model_iq.go:255-283`
