# 凭据检测问题 - 快速解决指南

## 问题 1: "model-quality worker not configured"

### 快速解决方案

这个错误表示模型智商检测服务未配置。有两个选择：

#### 选项 A: 跳过模型智商检测（推荐）

模型智商检测是**可选功能**，不影响正常使用。如果不需要该功能，可以忽略此错误。

#### 选项 B: 配置模型质量服务

如果需要模型智商检测，需要：

1. 确认配置文件中有以下配置：
   ```yaml
   model_quality:
     base_url: http://localhost:8787  # 或实际服务地址
     api_key: YOUR_API_KEY
     data_dir: ./data
   ```

2. 启动对应的模型质量检测服务（在 port 8787）

3. 重启网关

---

## 问题 2: 配额用完的凭据没有自动切换

### 问题说明

**这不是 bug！** "检查所有凭据"功能的目的是**诊断**每个凭据的状态，而不是自动切换。

从你的错误信息：
```
augeste: unavailable - 模型列表中无此模型
sp1-2:   error - quota exhausted (HTTP 403)
```

这个结果是**正确的诊断**：
- `augeste` 凭据：确实没有这个模型
- `sp1-2` 凭据：确实配额用完了

### 实际请求会自动路由到可用凭据

当你通过 API 发送真实请求时，网关的路由器**会自动**选择可用的凭据。

### 立即解决步骤

#### 步骤 1: 手动禁用配额用完的凭据（推荐）

进入管理后台：

1. 访问 `https://llm.kxpms.cn/providers/{provider_id}`
2. 切换到"凭据"标签
3. 找到 `sp1-2` 凭据
4. 点击禁用按钮（或设置 `manual_disabled = TRUE`）

这样可以确保该凭据不会被路由选择。

#### 步骤 2: 使用 SQL 更新配额状态

```sql
-- 查找配额用完的凭据
SELECT id, label, quota_state, status 
FROM credentials 
WHERE provider_id = {provider_id}
  AND label = 'sp1-2';

-- 标记为配额耗尽
UPDATE credentials 
SET quota_state = 'permanently_exhausted',
    state_reason_code = 'quota_exhausted',
    state_reason_detail = 'call quota exhausted: used=60000 limit=60000',
    state_updated_at = NOW()
WHERE id = {credential_id};
```

#### 步骤 3: 验证可用凭据的状态

```sql
-- 查看所有凭据的状态
SELECT 
    id,
    label,
    status,
    quota_state,
    availability_state,
    manual_disabled,
    balance_usd
FROM credentials
WHERE provider_id = {provider_id}
ORDER BY id;
```

确保至少有一个凭据满足：
- `status = 'active'`
- `quota_state = 'ok'` 或 `NULL`
- `availability_state = 'ready'`
- `manual_disabled = FALSE`

#### 步骤 4: 测试实际路由

发送一个真实请求，看是否使用了可用凭据：

```bash
curl -X POST "http://qiyovo.com:3000/v1/chat/completions" \
  -H "Authorization: Bearer sk-4JLfwKsx1m9IaSSYJZo7uzf09CfgyP6wSjQ8hcthSFabwNbW" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 10
  }'
```

如果返回成功，说明路由正常工作。如果仍然返回配额错误，继续下一步。

#### 步骤 5: 检查模型绑定

```sql
-- 查看该模型在哪些凭据上可用
SELECT 
    c.id AS credential_id,
    c.label,
    c.status,
    c.quota_state,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE c.provider_id = {provider_id}
  AND pm.raw_model_name = 'glm-5.2'
ORDER BY c.id;
```

确保可用凭据上：
- `cmb.available = TRUE`
- `cmb.unavailable_reason IS NULL`

#### 步骤 6: 如果模型绑定不存在，手动添加

```sql
-- 获取 provider_model_id
SELECT id FROM provider_models 
WHERE provider_id = {provider_id} 
  AND raw_model_name = 'glm-5.2';

-- 为可用凭据添加模型绑定
INSERT INTO credential_model_bindings (
    credential_id, 
    provider_model_id, 
    available, 
    routing_tier, 
    weight
)
VALUES (
    {available_credential_id},
    {provider_model_id},
    TRUE,
    2,
    100
)
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
    available = TRUE,
    unavailable_reason = NULL,
    updated_at = NOW();
```

### 验证路由是否正常

```sql
-- 查看可路由的凭据×模型
SELECT * FROM v_routable_credential_models
WHERE provider_id = {provider_id}
  AND raw_model_name = 'glm-5.2';
```

应该至少返回一行（可用凭据的绑定）。

### 常见问题

#### Q1: 为什么"检查所有凭据"不自动切换？

**A**: 这是**诊断工具**，用于显示每个凭据的状态。实际路由时会自动选择可用凭据。

#### Q2: 如何让系统永久不使用配额用完的凭据？

**A**: 两种方式：
1. 手动禁用：设置 `manual_disabled = TRUE`
2. 自动标记：确保 `quota_state = 'permanently_exhausted'`

#### Q3: 配额恢复后如何重新启用？

**A**: 
```sql
UPDATE credentials 
SET quota_state = 'ok',
    quota_recover_at = NULL,
    state_reason_code = NULL,
    state_reason_detail = NULL
WHERE id = {credential_id};
```

或在管理后台点击"强制恢复"。

#### Q4: 如何查看当前请求使用了哪个凭据？

**A**: 查看网关日志或请求日志：
```sql
SELECT 
    credential_id,
    request_id,
    model,
    status_code,
    created_at
FROM request_logs
WHERE client_model = 'glm-5.2'
ORDER BY created_at DESC
LIMIT 10;
```

### 自动化脚本

如果经常需要处理配额问题，可以使用以下脚本：

```bash
#!/bin/bash
# disable_exhausted_credential.sh

PROVIDER_ID=$1
CREDENTIAL_LABEL=$2

psql -d llm_gateway << EOF
-- 禁用配额用完的凭据
UPDATE credentials 
SET quota_state = 'permanently_exhausted',
    manual_disabled = TRUE,
    state_reason_code = 'quota_exhausted',
    state_reason_detail = 'Manually disabled due to quota exhaustion',
    state_updated_at = NOW()
WHERE provider_id = ${PROVIDER_ID}
  AND label = '${CREDENTIAL_LABEL}';

-- 显示结果
SELECT 
    id, label, status, quota_state, manual_disabled
FROM credentials
WHERE provider_id = ${PROVIDER_ID}
ORDER BY id;
EOF
```

使用方式：
```bash
./disable_exhausted_credential.sh 12763 sp1-2
```

---

## 总结

1. **模型智商错误**：可以忽略，或配置模型质量服务
2. **凭据检测**：正常工作，显示每个凭据的真实状态
3. **实际路由**：会自动选择可用凭据
4. **立即行动**：手动禁用配额用完的凭据，或更新其 `quota_state`

## 下一步

1. 手动禁用 `sp1-2` 凭据
2. 确认其他凭据状态正常
3. 测试实际请求是否路由到可用凭据
4. 如有问题，检查模型绑定和路由视图

## 相关文档

- 详细分析: `CREDENTIAL_CHECK_ISSUES.md`
- 凭据管理: 管理后台 → 供应商详情 → 凭据标签
- 路由诊断: 管理后台 → 供应商详情 → 模型标签 → 路由阻塞诊断
