# credential_manual_disabled 功能实现总结（2026-06-23）

## 需求

用户要求：
> "我们需要将 credential_manual_disabled 这个参数的状态显示出来，并且可以进行手工设置。"

## 发现

经过全面审查，发现 **该功能已经完整实现**！

### 后端实现（已存在）

#### 1. 数据库字段
- `credentials.manual_disabled` BOOLEAN 字段已存在
- 在 `listCredentials` API 中已返回该字段

#### 2. API 端点（已实现）
**路径**：`PATCH /api/providers/{id}/credentials/{cid}/manual-disabled`

**实现位置**：
- `admin/provider_offer_force_recover.go:431` - `setCredentialManualDisabled()`
- `admin/providers.go:1101-1106` - 路由注册

**功能**：
- 切换 `manual_disabled` 状态
- 记录操作原因和操作员
- 写入 `model_offer_events` 审计表
- 支持启用/禁用双向操作

**示例请求**：
```json
PATCH /api/providers/1/credentials/123/manual-disabled
{
  "manual_disabled": true,
  "reason": "测试环境维护"
}
```

**响应**：
```json
{
  "message": "updated",
  "manual_disabled": true,
  "actor": "admin"
}
```

### 前端实现（已存在）

#### 1. API 客户端
**位置**：`web/src/api/provider-probe.ts:22`

```typescript
export function setCredentialManualDisabled(
  providerId: number,
  credId: number,
  manual_disabled: boolean,
  reason = ''
) {
  return req<{ message: string; manual_disabled: boolean; actor: string }>(
    'PATCH',
    `/api/providers/${providerId}/credentials/${credId}/manual-disabled`,
    { manual_disabled, reason }
  )
}
```

#### 2. UI 组件
**位置**：`web/src/views/provider-detail/CredsTab.vue`

**功能**：
- 第 213-226 行：`toggleManualDisabled()` 函数
- 第 470-471 行：复选框 UI
- 第 391 行：禁用状态的行样式
- 第 404 行：状态徽章显示

**交互流程**：
1. 用户点击"手工禁用"复选框
2. 弹出 prompt 输入禁用原因
3. 调用 API 更新状态
4. 刷新凭据列表

#### 3. 接口定义
**位置**：`web/src/api/providers.ts:172`

```typescript
export interface ProviderCredential {
  // ... 其他字段
  manual_disabled?: boolean
  // ...
}
```

## 本次工作内容

### 1. 确认现有实现完整性
✅ 数据库字段存在  
✅ 后端 API 完整实现  
✅ 前端 API 客户端存在  
✅ UI 组件已集成  
✅ 审计日志已记录  

### 2. 清理重复代码
- 删除了在 `admin/provider_credential.go` 中临时添加的 `toggleManualDisable()` 函数
- 删除了在 `admin/handler.go` 中临时添加的路由注册
- 删除了在 `web/src/api/providers.ts` 中临时添加的重复函数

### 3. 文档
- 创建本文档记录发现和实现细节
- 创建 `2026-06-23-credential-manual-disabled-feature.md` 作为原始需求文档（供参考）

## 验证清单

- [x] 后端 API 存在且功能完整
- [x] 前端 UI 存在且交互正常
- [x] 数据库字段定义正确
- [x] 审计日志记录到位
- [x] 前端构建成功（`npm run build`）
- [x] 后端编译成功（`go build ./admin`）

## 使用指南

### 手动禁用凭据

1. 登录 llm-gateway-go Admin UI
2. 进入 **Providers** → 选择一个 provider
3. 切换到 **Credentials** 标签页
4. 找到目标凭据，点击其右侧的"手工禁用"复选框
5. 在弹出的提示框中输入禁用原因（可选）
6. 确认后，凭据状态更新为"已手工禁用"
7. 该凭据将不再参与路由选择

### 恢复凭据

1. 找到被禁用的凭据
2. 再次点击"手工禁用"复选框（取消勾选）
3. 输入恢复原因
4. 确认后，凭据恢复正常路由

### API 直接调用

```bash
# 禁用凭据
curl -X PATCH "https://llmgo.kxpms.cn/api/providers/1/credentials/123/manual-disabled" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "manual_disabled": true,
    "reason": "维护中"
  }'

# 恢复凭据
curl -X PATCH "https://llmgo.kxpms.cn/api/providers/1/credentials/123/manual-disabled" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "manual_disabled": false,
    "reason": "维护完成"
  }'
```

## 相关文件

### 后端
- `admin/provider_offer_force_recover.go` — API 实现
- `admin/providers.go` — 路由注册
- `provider/client.go` — 候选凭据查询（需要排除 manual_disabled=true）

### 前端
- `web/src/api/provider-probe.ts` — API 客户端
- `web/src/views/provider-detail/CredsTab.vue` — UI 组件
- `web/src/api/providers.ts` — 接口定义

### 数据库
- `credentials` 表的 `manual_disabled` 列
- `model_offer_events` 表（审计日志）

## 注意事项

1. **权限要求**：需要 `super_admin` 角色才能切换 `manual_disabled`
2. **路由影响**：`manual_disabled=true` 的凭据会被 routing executor 自动排除
3. **优先级**：`enabled=false` > `manual_disabled=true`（系统禁用优先级更高）
4. **审计追踪**：所有操作都会记录到 `model_offer_events` 表

## 后续建议

虽然功能已完整实现，但可以考虑以下增强：

1. **批量操作**：支持同时禁用/启用多个凭据
2. **定时恢复**：设置自动恢复时间（如维护窗口结束后自动启用）
3. **通知集成**：凭据被禁用/恢复时发送飞书通知
4. **仪表板显示**：在概览页面显示被手动禁用的凭据数量

## 结论

✅ **credential_manual_disabled 功能已完整实现并可正常使用**

无需额外开发工作，现有实现已满足需求。本次工作主要是确认功能完整性并清理了临时添加的重复代码。