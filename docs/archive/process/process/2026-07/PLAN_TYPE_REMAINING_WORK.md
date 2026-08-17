# Plan Type 标准化 - 剩余工作 (Steps 3-8)

## 已完成 (commit 历史)

✅ **Step 1**: mig_136 - DB schema + 视图 (`3bd3c29f`)
✅ **Step 2**: modelcatalog/upsert.go - discovery 写入 billing_mode (`f013ba2f`)
✅ **路由修复**: provider 过滤 + quota 切换 (`fb203beb`, `3e8b80dd`)

## 剩余工作

### Step 3: admin/provider_credential.go CRUD 扩展 (~150行)

**addCredential** (line 36):
- 请求体加 `PlanType *string`
- INSERT 加 `plan_type` 列，默认 'token'
- 验证 `isValidPlanType()` (token/token_plan/code_plan/agent_plan/monthly/free)

**listCredentials** (line 102):
- SELECT 加 `c.plan_type, cmb.billing_mode`
- 响应体加字段

**updateCredential** (line 294):
- 请求体加 `PlanType *string`
- 如果 `req.PlanType != nil`:
  - 验证 `isValidPlanType()`
  - 开事务
  - UPDATE `credentials SET plan_type=$1`
  - CASCADE UPDATE `credential_model_bindings SET billing_mode = CASE WHEN $1='token' THEN 'per_token' ELSE $1 END, plan_type_origin='auto' WHERE credential_id=$2 AND plan_type_origin='auto'`
  - Commit
  - `provider.InvalidateAllCandidateCache()`

**Helper函数** (新增):
```go
var validPlanTypes = map[string]bool{
    "token": true, "token_plan": true, "code_plan": true,
    "agent_plan": true, "monthly": true, "free": true,
}
func isValidPlanType(s string) bool { return validPlanTypes[s] }
```

### Step 4: admin/pricing.go - setFreeModels 端点 (~80行)

**新端点** `POST /api/providers/{id}/set-free-models`:
```go
func (h *Handler) setFreeModels(w http.ResponseWriter, r *http.Request, providerID int) {
    var req struct {
        RawModelNames []string `json:"raw_model_names"`
        Free          bool     `json:"free"`
    }
    // parse + validate
    // BEGIN TX
    // UPDATE credential_model_bindings cmb
    // SET billing_mode = CASE WHEN $free THEN 'free' ELSE c.plan_type END,
    //     plan_type_origin = CASE WHEN $free THEN 'manual' ELSE 'auto' END
    // FROM credentials c, provider_models pm
    // WHERE cmb.credential_id = c.id
    //   AND cmb.provider_model_id = pm.id
    //   AND pm.provider_id = $pid
    //   AND pm.raw_model_name = ANY($models)
    // COMMIT
    // provider.InvalidateAllCandidateCache()
    // 200 OK {updated: count}
}
```

注册路由 (admin/handler.go):
```go
mux.HandleFunc("/api/providers/{id}/set-free-models", withProviderID(h.setFreeModels))
```

### Step 5: 路由层读 cmb.billing_mode (~30行)

**已完成！** mig_136 的视图已经暴露 `cmb.billing_mode`，
`provider/client.go` 和 `admin/routing.go` 通过 `v_routable_credential_models` 读取。

### Step 6-8: 前端 (web/src/) (~200行)

**Step 6**: `web/src/api/providers.ts`
- 定义 `export type PlanType = 'token' | 'token_plan' | ... | 'free'`
- `ProviderCredential` 接口加 `plan_type?: PlanType, cmb_billing_mode?: string`
- `addCredential` / `updateCredential` 签名加 `plan_type`
- 新文件 `web/src/api/pricing.ts`:
  ```ts
  export function setFreeModels(providerId: number, rawModelNames: string[], free: boolean)
  ```

**Step 7**: `web/src/views/ProvidersView.vue`
- 凭据表加两列：
  - "套餐": `<select v-model="c.plan_type">` (6 options)
  - "计价": 只读 chip 显示 `c.cmb_billing_mode`
- `saveCredential` 加 `plan_type: c.plan_type`

**Step 8**: `web/src/views/PricingManagementView.vue`
- 移除 `editForm.billing_mode` 字段（不再手动编辑单行）
- 加免费开关列：`<input type="checkbox" :checked="offer.billing_mode==='free'" @change="toggleFree">`
- 实现 `async function toggleFree(offer, event)` 调用 `setFreeModels`

## 实施建议

**优先级 A** (核心功能已完成):
- 当前已完成的 Step 1-2 + 路由修复已经**解决 cred-6 bug**
- 视图正确检查 plan_type 兼容性
- Discovery 自动写入正确 billing_mode
- 禁用 provider 过滤 + quota 切换已生效

**优先级 B** (运维增强):
- Steps 3-5: 允许运维手动编辑 plan_type（API 层）
- Steps 6-8: UI 支持（前端页面）

建议：
1. 先部署 mig 136+137 到生产，验证核心修复
2. 后续迭代完成 Steps 3-8（运维工具）
