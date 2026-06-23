# credential_manual_disabled 参数管理需求（2026-06-23）

## 需求描述

用户要求：
> "我们需要将 credential_manual_disabled 这个参数的状态显示出来，并且可以进行手工设置。"

## 当前状态

`credentials` 表已有 `manual_disabled` 列（BOOLEAN），但：
- ❌ Admin UI 中没有显示
- ❌ 没有提供手动设置的 API/UI

## 实现方案

### 1. 后端 API（admin/provider_credential.go）

已有的端点需要扩展：

#### GET /api/admin/credentials（列表）
**当前**：返回 credential 列表，但不包含 `manual_disabled`
**需要**：SELECT 子句添加 `c.manual_disabled`

#### PUT /api/admin/credentials/:id（更新）
**当前**：支持更新 enabled/priority/concurrency_limit 等
**需要**：添加 `manual_disabled` 字段支持

#### 新增：POST /api/admin/credentials/:id/manual-disable
快捷端点，专门用于切换 manual_disabled 状态：
```go
func (h *Handler) toggleManualDisable(w http.ResponseWriter, r *http.Request) {
	credID := extractCredentialID(r)
	var req struct {
		ManualDisabled bool `json:"manual_disabled"`
		Reason string `json:"reason,omitempty"` // 操作原因（审计）
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET manual_disabled = $1, updated_at = now()
		WHERE id = $2
	`, req.ManualDisabled, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 记录审计日志
	h.logAudit(ctx, "credential_manual_disable", map[string]interface{}{
		"credential_id": credID,
		"disabled": req.ManualDisabled,
		"reason": req.Reason,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credential_id": credID,
		"manual_disabled": req.ManualDisabled,
	})
}
```

### 2. 前端 UI（web/src/views/CredentialsView.vue）

#### 凭据列表表格
在 `<el-table>` 中添加新列：
```vue
<el-table-column label="手动禁用" width="100">
  <template #default="{ row }">
    <el-switch
      v-model="row.manual_disabled"
      @change="handleManualDisableToggle(row)"
      :loading="row.manualDisableLoading"
    />
  </template>
</el-table-column>
```

#### 切换处理函数
```typescript
const handleManualDisableToggle = async (row: Credential) => {
  row.manualDisableLoading = true
  try {
    const reason = await ElMessageBox.prompt(
      '请输入禁用/启用原因（可选）',
      row.manual_disabled ? '手动禁用凭据' : '恢复凭据',
      { inputType: 'textarea' }
    )
    
    await api.toggleCredentialManualDisable(row.id, {
      manual_disabled: row.manual_disabled,
      reason: reason.value
    })
    
    ElMessage.success(row.manual_disabled ? '已禁用' : '已启用')
  } catch (err) {
    // 回滚开关状态
    row.manual_disabled = !row.manual_disabled
    ElMessage.error('操作失败: ' + err.message)
  } finally {
    row.manualDisableLoading = false
  }
}
```

#### API 类型定义（web/src/api.ts）
```typescript
export interface Credential {
  id: number
  provider_id: number
  api_key: string
  enabled: boolean
  manual_disabled: boolean  // 新增
  priority: number
  concurrency_limit: number
  fp_slot_limit: number
  // ... 其他字段
}

export async function toggleCredentialManualDisable(
  credentialId: number,
  data: { manual_disabled: boolean; reason?: string }
) {
  return request.post(`/api/admin/credentials/${credentialId}/manual-disable`, data)
}
```

### 3. 路由注册（admin/handler.go）

```go
mux.HandleFunc("/api/admin/credentials/{id}/manual-disable",
	h.superAdmin(h.toggleManualDisable))
```

### 4. 业务逻辑集成

#### routing/executor.go 中过滤被手动禁用的凭据

**当前**：`PlanCandidates` 只检查 `enabled` 字段
**需要**：同时检查 `manual_disabled`

```go
// provider/client.go
func (c *Client) ListCandidatesForRouting(...) ([]*Candidate, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT ...
		FROM credentials c
		JOIN credential_model_bindings b ON ...
		WHERE c.enabled = true
		  AND c.manual_disabled = false  -- 新增
		  AND ...
	`)
	// ...
}
```

### 5. 审计日志

所有 manual_disabled 变更应写入审计表：

```sql
CREATE TABLE IF NOT EXISTS credential_manual_disable_audit (
	id BIGSERIAL PRIMARY KEY,
	credential_id INT NOT NULL REFERENCES credentials(id),
	disabled BOOLEAN NOT NULL,
	reason TEXT,
	operator_id INT, -- 操作员 ID（从 JWT 提取）
	created_at TIMESTAMPTZ DEFAULT now()
);
```

### 6. UI 设计建议

#### 状态显示
- ✅ **启用且未禁用**：绿色指示灯
- ⚠️ **启用但手动禁用**：黄色指示灯 + "已手动禁用"标签
- ❌ **系统禁用（enabled=false）**：灰色指示灯

#### 操作权限
- `manual_disabled` 切换：需要 `super_admin` 角色
- 普通 admin 只能查看，不能修改

#### 批量操作
添加批量禁用功能：
```vue
<el-button
  :disabled="!selectedCredentials.length"
  @click="handleBatchManualDisable"
>
  批量禁用选中凭据
</el-button>
```

## 实施步骤

1. **后端 API**（30min）
   - [ ] 扩展 listCredentials SELECT
   - [ ] 扩展 updateCredential 支持 manual_disabled
   - [ ] 新增 toggleManualDisable 端点
   - [ ] 注册路由

2. **前端 UI**（40min）
   - [ ] CredentialsView 添加 manual_disabled 列
   - [ ] 实现切换开关 + 原因输入
   - [ ] API 类型定义

3. **业务逻辑集成**（20min）
   - [ ] provider/client.go 过滤 manual_disabled=true
   - [ ] 测试路由是否正确排除被禁用的凭据

4. **审计日志**（可选，15min）
   - [ ] 创建审计表
   - [ ] logAudit 函数实现

5. **测试**（30min）
   - [ ] 切换 manual_disabled 开关
   - [ ] 验证凭据不再被路由
   - [ ] 恢复后验证正常

**总计**：~2.5 小时

## 测试用例

### TC1: 手动禁用凭据
1. 在凭据列表中点击某个凭据的"手动禁用"开关
2. 输入禁用原因："测试环境维护"
3. 确认后，开关变为禁用状态
4. 发送请求，验证该凭据不再被选中

### TC2: 恢复凭据
1. 点击已禁用凭据的开关
2. 输入恢复原因："维护完成"
3. 确认后，开关变为启用状态
4. 发送请求，验证该凭据恢复正常路由

### TC3: 优先级检查
- `enabled=false` > `manual_disabled=true`
- 即使 manual_disabled=false，但 enabled=false 时凭据仍不可用

### TC4: 审计追踪
1. 查询 `credential_manual_disable_audit` 表
2. 验证所有操作都有记录（操作员、原因、时间戳）

## 相关文档

- `admin/provider_credential.go` — 凭据管理 API
- `provider/client.go` — 候选凭据查询
- `web/src/views/CredentialsView.vue` — 凭据管理 UI