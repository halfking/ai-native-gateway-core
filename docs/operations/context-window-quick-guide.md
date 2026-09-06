# 上下文窗口设置 - 快速操作指南

## 场景：供应商缩减了模型的上下文窗口

### 问题示例
供应商通知：我们的 `glm-5.2` 模型实际只支持 32K 上下文，但系统中配置的是 128K。

### 解决步骤

#### 方式 1: 通过管理后台 UI（推荐）

1. **进入供应商页面**
   ```
   https://llm.kxpms.cn/providers/{provider_id}
   ```

2. **切换到"模型"标签页**
   - 查看该供应商下所有凭据的模型列表

3. **找到需要修改的模型**
   - 使用搜索框快速定位（例如输入 "glm-5.2"）
   - 或滚动查找对应的模型行

4. **打开模型详情**
   - 点击模型行，打开右侧详情抽屉

5. **设置上下文窗口**
   - 找到"上下文窗口覆盖"字段
   - 输入新的值（例如 `32768` 表示 32K）
   - 查看当前生效值和继承来源提示

6. **保存并验证**
   - 点击"保存"按钮
   - 等待保存成功提示
   - 确认 `context_window_override` 已更新为新值

#### 方式 2: 通过 API

```bash
# 获取 offer_id
curl -X GET "https://llm.kxpms.cn/api/providers/{provider_id}/models" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  | jq '.[] | select(.raw_model_name == "glm-5.2")'

# 更新上下文窗口
curl -X PATCH "https://llm.kxpms.cn/api/providers/{provider_id}/offers/{offer_id}" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "context_window": 32768
  }'
```

### 清除覆盖值（恢复默认）

如果需要恢复为标准目录的默认值：

**UI 操作**：
- 在"上下文窗口覆盖"字段输入 `0`
- 点击"保存"

**API 操作**：
```bash
curl -X PATCH "https://llm.kxpms.cn/api/providers/{provider_id}/offers/{offer_id}" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "context_window": 0
  }'
```

### 批量设置（同一供应商的多个凭据）

如果同一供应商的多个凭据都需要设置相同的上下文窗口：

```bash
# 获取该供应商的所有 glm-5.2 模型绑定
OFFERS=$(curl -X GET "https://llm.kxpms.cn/api/providers/{provider_id}/models" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  | jq -r '.[] | select(.raw_model_name == "glm-5.2") | .id')

# 批量更新
for OFFER_ID in $OFFERS; do
  curl -X PATCH "https://llm.kxpms.cn/api/providers/{provider_id}/offers/$OFFER_ID" \
    -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"context_window": 32768}'
  echo "Updated offer $OFFER_ID"
done
```

## 验证设置是否生效

### 1. 检查数据库

```sql
-- 查看特定凭据×模型的上下文窗口配置
SELECT 
    c.id AS credential_id,
    c.label AS credential_label,
    pm.raw_model_name,
    cmb.id AS offer_id,
    cmb.context_window_override,
    mc.context_window AS canonical_window,
    COALESCE(cmb.context_window_override, mc.context_window_override, mc.context_window) AS effective_window,
    cmb.context_window_source,
    cmb.context_window_updated_at
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN models_canonical mc ON mc.id = cmb.canonical_id
WHERE c.provider_id = {provider_id}
  AND pm.raw_model_name = 'glm-5.2'
ORDER BY c.id;
```

### 2. 检查 API 响应

```bash
# 查看模型列表，确认 context_window 值
curl -X GET "https://llm.kxpms.cn/api/providers/{provider_id}/models" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  | jq '.[] | select(.raw_model_name == "glm-5.2") | {
      credential_id, 
      credential_label, 
      context_window, 
      context_window_override
    }'
```

### 3. 测试实际请求

发送一个略小于新窗口大小的请求，确认可以正常处理：

```bash
# 测试 30K token 请求（在 32K 窗口内）
curl -X POST "http://qiyovo.com:3000/v1/chat/completions" \
  -H "Authorization: Bearer <REDACTED_ADMIN_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "LONG_CONTENT_30K_TOKENS"}],
    "max_tokens": 100
  }'
```

## 常见问题

### Q1: 设置后多久生效？
**A**: 立即生效。系统会自动：
- 更新数据库
- 清除内存缓存
- 通知其他网关实例
- 通常在 1-2 秒内所有实例都会同步

### Q2: 会影响其他供应商的同名模型吗？
**A**: 不会。设置是**凭据×模型**级别的，只影响当前凭据下的该模型。

### Q3: 如何知道当前生效的是哪个值？
**A**: 查看管理后台模型详情：
- `context_window`: 当前生效值
- `context_window_override`: 本节点的覆盖值（NULL = 未覆盖）
- 如果有覆盖，会显示 "context 覆盖" 徽章

### Q4: 设置会被自动刷新覆盖吗？
**A**: 不会。手动设置的值（`source='manual'`）受到保护，自动发现刷新不会覆盖。

### Q5: 如何批量恢复所有凭据的默认值？
**A**: 使用 SQL 批量清除：
```sql
UPDATE credential_model_bindings cmb
SET 
    context_window_override = NULL,
    context_window_source = 'catalog',
    context_window_updated_at = NOW()
FROM credentials c, provider_models pm
WHERE cmb.credential_id = c.id
  AND cmb.provider_model_id = pm.id
  AND c.provider_id = {provider_id}
  AND pm.raw_model_name = 'glm-5.2'
  AND cmb.context_window_override IS NOT NULL;
```

然后手动触发缓存失效：
```sql
NOTIFY auto_route_refresh, 'credential_model_bindings:bulk_update';
```

## 相关文档

- 详细功能说明: `CONTEXT_WINDOW_OVERRIDE_GUIDE.md`
- 路由配置: `docs/routing/`
- 压缩策略: `docs/compression/`

## 联系支持

如果遇到问题：
1. 查看管理后台的审计日志
2. 检查网关日志中的相关错误
3. 联系技术支持团队
