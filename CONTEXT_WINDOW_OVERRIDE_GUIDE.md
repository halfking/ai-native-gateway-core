# 凭据级上下文窗口覆盖功能说明

## 概述

当供应商端的实际上下文窗口与标准模型目录中的值不一致时（例如供应商进行了上下文缩减），可以在**凭据×模型**级别设置上下文窗口覆盖值，无需修改全局标准模型配置。

## 功能特性

### 三级上下文窗口继承链

系统使用三级覆盖链来确定最终生效的上下文窗口：

1. **凭据×模型级覆盖** (`credential_model_bindings.context_window_override`)
   - 最高优先级
   - 针对特定供应商的特定模型
   - 通过管理后台手动设置

2. **标准模型级覆盖** (`models_canonical.context_window_override`)
   - 中等优先级
   - 适用于该模型的所有供应商

3. **标准模型基准值** (`models_canonical.context_window`)
   - 兜底默认值
   - 模型目录的基准配置

**SQL 表达式**：
```sql
COALESCE(
    credential_model_bindings.context_window_override,
    models_canonical.context_window_override,
    models_canonical.context_window
) AS effective_context_window
```

## 使用场景

### 场景 1: 供应商缩减上下文窗口

**问题**：某供应商提供的 `glm-5.2` 模型实际上下文窗口为 32K，但标准目录中配置为 128K

**解决方案**：
1. 在管理后台进入 `https://llm.kxpms.cn/providers/{provider_id}`
2. 点击"模型"标签页
3. 找到对应的模型绑定（例如 `glm-5.2`）
4. 点击模型行打开详情抽屉
5. 在"上下文窗口覆盖"字段输入 `32768`
6. 点击"保存"

### 场景 2: 测试环境限制

**问题**：测试环境的某个凭据需要限制上下文窗口以节省成本

**解决方案**：
按照场景 1 的步骤设置较小的上下文窗口值（例如 4096）

### 场景 3: 恢复默认值

**问题**：之前设置的覆盖值不再需要，想恢复为标准目录的值

**解决方案**：
1. 打开模型详情抽屉
2. 在"上下文窗口覆盖"字段输入 `0` 或负数
3. 点击"保存"
4. 系统会清除覆盖值（设置为 NULL），回退到标准目录配置

## API 使用

### 更新上下文窗口

**端点**: `PATCH /api/providers/{provider_id}/offers/{offer_id}`

**请求体**:
```json
{
  "context_window": 32768
}
```

**参数说明**:
- `context_window` (可选):
  - `> 0`: 设置为指定值（例如 32768）
  - `<= 0`: 清除覆盖，回退到标准目录
  - `null`/省略: 不修改当前值

**响应**:
```json
{
  "id": 12345,
  "raw_model_name": "glm-5.2",
  "standardized_name": "glm-5.2",
  "canonical_id": 100,
  "canonical_name": "glm-5.2",
  "outbound_model_name": null,
  "context_window": 32768,
  "context_window_override": 32768
}
```

**字段说明**:
- `context_window`: 当前生效的上下文窗口值（三级链的最终结果）
- `context_window_override`: 本凭据×模型的覆盖值（NULL = 未覆盖）

### 查看模型列表

**端点**: `GET /api/providers/{provider_id}/models`

**响应**:
```json
[
  {
    "id": 12345,
    "credential_id": 42,
    "credential_label": "hzx-prod-1",
    "raw_model_name": "glm-5.2",
    "standardized_name": "glm-5.2",
    "context_window": 32768,
    "context_window_override": 32768,
    ...
  }
]
```

## 前端实现

### ModelOfferDetailDrawer.vue

前端抽屉组件支持：

1. **显示当前值**：
   - 显示生效值 `context_window`
   - 如果有覆盖值，显示 "context 覆盖" 徽章
   - 显示继承来源提示

2. **编辑功能**：
   - 输入框支持数字输入
   - Placeholder: "空=继承标准"
   - 输入 0 或负数可清除覆盖

3. **保存逻辑**：
   ```typescript
   const rawCw = draft.context_window
   if (rawCw !== null && rawCw !== '') {
     body.context_window =
       typeof rawCw === 'number' ? rawCw : parseInt(String(rawCw), 10)
   }
   ```

## 数据库结构

### credential_model_bindings 表

```sql
CREATE TABLE credential_model_bindings (
    id SERIAL PRIMARY KEY,
    credential_id INTEGER NOT NULL,
    provider_model_id INTEGER NOT NULL,
    ...
    context_window_override INTEGER,              -- 覆盖值（NULL = 未覆盖）
    context_window_source VARCHAR(20),            -- 'manual' | 'catalog' | 'discovery'
    context_window_updated_at TIMESTAMP,          -- 最后更新时间
    ...
);
```

### 更新触发器

当 `context_window_override` 更新时：

1. **设置来源标记**：
   ```sql
   context_window_source = 'manual'
   context_window_updated_at = NOW()
   ```

2. **失效路由缓存**：
   - 调用 `invalidateRoutingCaches()` 清除内存缓存
   - 发送 PG NOTIFY 通知其他网关实例
   - 清除 `InvalidateAvailableModelsCache()`

## 影响范围

设置上下文窗口覆盖会影响：

1. **路由选择**：
   - 网关根据生效的上下文窗口值选择合适的凭据
   - 较小的窗口会排除超长请求

2. **请求验证**：
   - 拦截超过上下文窗口的请求
   - 返回 400 错误或自动截断

3. **压缩触发**：
   - 上下文压缩器根据窗口大小计算触发阈值
   - 智能窗口恢复使用窗口值作为切分点

4. **计费统计**：
   - 某些计费策略可能参考上下文窗口

## 最佳实践

### 1. 验证供应商实际限制

在设置覆盖值前，通过以下方式验证供应商的实际限制：

```bash
# 测试长上下文请求
curl -X POST https://provider.example.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "LONG_CONTEXT_HERE"}],
    "max_tokens": 100
  }'
```

观察是否返回上下文长度错误。

### 2. 记录修改原因

在操作时记录：
- 修改时间
- 操作人员
- 原因说明（供应商通知、测试发现等）

可以在管理后台的审计日志中查看历史记录。

### 3. 监控影响

设置后观察：
- 该凭据的路由分配是否正常
- 是否有请求被错误拒绝
- 压缩器行为是否符合预期

### 4. 分阶段生效

对于生产环境：
1. 先在测试凭据上设置并验证
2. 确认无误后再应用到生产凭据
3. 逐步推广到同一供应商的其他凭据

## 故障排查

### 问题 1: 设置后未生效

**检查项**:
1. 缓存是否已失效：
   ```sql
   SELECT last_invalidated_at FROM routing_cache_status;
   ```

2. 其他网关实例是否收到通知：
   - 检查 PG LISTEN/NOTIFY 是否正常
   - 重启其他网关实例

3. 确认数据库值已更新：
   ```sql
   SELECT context_window_override, context_window_source, context_window_updated_at
   FROM credential_model_bindings
   WHERE id = {offer_id};
   ```

### 问题 2: 请求被意外拒绝

**检查项**:
1. 查看生效的上下文窗口：
   ```sql
   SELECT 
       cmb.id,
       COALESCE(cmb.context_window_override, mc.context_window_override, mc.context_window) AS effective_window,
       cmb.context_window_override,
       mc.context_window_override,
       mc.context_window
   FROM credential_model_bindings cmb
   LEFT JOIN models_canonical mc ON mc.id = cmb.canonical_id
   WHERE cmb.id = {offer_id};
   ```

2. 对比请求的实际 token 数：
   - 检查请求日志中的 `prompt_tokens` 字段
   - 确认是否超过设置的窗口大小

### 问题 3: 覆盖值被刷新覆盖

**原因**: 
早期版本的发现刷新可能会覆盖手动设置的值。

**解决方案**:
当前版本已修复（见 `modelcatalog/upsert.go:220-227`）：
```sql
ON CONFLICT ... DO UPDATE SET
    context_window_override = COALESCE(
        EXCLUDED.context_window_override,
        credential_model_bindings.context_window_override
    ),
    context_window_source = CASE
        WHEN EXCLUDED.context_window_override IS NOT NULL THEN 'manual'
        ELSE credential_model_bindings.context_window_source
    END
```

手动设置的值 (`source='manual'`) 不会被自动发现覆盖。

## 相关代码位置

- **后端处理**: `admin/provider_offer_force_recover.go:71-269` (`updateModelOffer`)
- **前端组件**: `web/src/components/model/ModelOfferDetailDrawer.vue`
- **数据库表**: `credential_model_bindings`
- **路由逻辑**: `gateway/routing/candidate_query.go` (使用 effective_context_window)
- **压缩触发**: `domains/hooks/compression/` (使用窗口值计算阈值)

## 版本历史

- **522 (2026-06-XX)**: 初始实现上下文窗口三级覆盖链
- **523 (2026-06-XX)**: 添加路由缓存失效机制
- **524 (2026-06-XX)**: 扩展数据库触发器监听 context_window_override 变更

## 总结

凭据级上下文窗口覆盖功能提供了灵活的配置能力，让运维人员可以针对特定供应商的实际情况进行精确调整，而无需修改全局标准模型配置。通过三级继承链，系统在保持灵活性的同时，也确保了配置的清晰性和可维护性。
