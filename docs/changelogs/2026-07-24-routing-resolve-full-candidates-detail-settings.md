# routing-v2 resolve 页 — 完整路由可能性 + 全明细状态标志 + 管理员设置

**日期**：2026-07-24  
**触发问题**：原 `/routing-v2?tab=resolve` 只展示可路由候选，需要勾选"不可用（N）"才能看到阻断候选；并且每行只有 7 个汇总字段（得分/供应商/凭据/上游/Tier/计费/状态），无法看到完整的"为什么不可用"明细，也无法在不跳转凭据管理页的前提下调整排序相关字段。

## 设计要点

### 1. 默认展示全部候选

把 `filteredResolveCandidates`（= 仅 routable）改为 `resolveCandidates`（= 后端 `?include_blocked=true` 已经返回的全部候选）。可用性以 badge 区分；不可用行半透明 + 额外显示 `runtime_block_reason`。这与"一眼看清路由状态"的需求一致。

### 2. 行级明细抽屉（`CandidateDetailDrawer.vue`）

按"为什么这个候选是这个状态"的诊断心智分组，8 组 30+ 标志位：

| 分组 | 字段 | 状态管理模块中的来源 |
|---|---|---|
| 可用性 | available / runtime_routable / block_reason | `v_routable_credential_models.is_routable` + `unavailable_reason` |
| 凭据 / Provider | credential_status / lifecycle_status / availability_state / availability_recover_at / provider_enabled | `credentials` 表 + `providers.enabled` |
| 配额 | quota_state / quota_recover_at / balance_usd / quota_cap_usd / quota_used_usd | `credentials` + `model_offers` |
| 熔断 | circuit_state / cooling_until | `credentials.circuit_state` 状态机（closed → open → half_open） |
| 并发 / 会话 | concurrency_limit / effective_concurrency / active_sessions | `credentials` + `credential_model_bindings.active_sessions` |
| 时效 | effective_at / expires_at / credential_in_effect | `credentials` 派生 `now() BETWEEN ...` |
| 计费 | billing_mode / billing_round / unit_price_in / unit_price_out / currency | `credential_model_bindings.billing_mode` + `model_offers` |
| 评分 / 优先级 | composite_score / manual_priority / tier / weight / success_rate / p95_latency_ms / consecutive_failures | `executors.CalculateCompositeScore` + `cmb` |

每行固定结构：标签 / 当前值（带 `--kx-success/danger/warning` 颜色）/ 说明（人类可读）/ 来源（具体列名或视图名）。这样排查"为什么这个候选状态是这样"时不再需要在 admin 多个页面之间跳转。

### 3. 行级管理员设置对话框（`CandidateSettingsDialog.vue`，仅 super_admin）

不绕过状态管理模块的硬规则，只暴露**排序相关**的可写字段：

- `manual_priority`（人工优先级，0–99，越小越优先）
- `routing_tier`（tier，0–9，free tier 固定 9）
- `weight`（同 tier 内权重，0–10000）
- `manual_disabled`（人工停用，置凭据进入 routing 排除名单）
- `lifecycle_status`（active / deprecated / test）

写入后端点：
- `PATCH /api/routing/candidate-binding/{credential_id}?raw_model=...`（**新**）→ 后端新增 `admin/handleRoutingCandidateBindingUpdate`，super_admin middleware，写 `routing_audit_log`
- `PATCH /api/providers/{id}/credentials/{cid}/manual-disabled`（**复用已有**）
- `PATCH /api/providers/{id}/credentials/{cid}/lifecycle`（**复用已有**）

交互语义：**乐观更新 + 失败回滚**——表单字段先在前端反映期望值，写入失败时立即复原并提示错误，避免出现"已经改了 UI 但后端没接受"的误导窗口。

### 4. kx-design token 双主题

所有颜色直接读 `var(--kx-success)` / `var(--kx-danger)` / `var(--kx-warning)` / `var(--kx-bg-elevated)` / `var(--kx-border-light)` 等 token，自动适配 daylight / night / 任何后续主题。fallback 链 `var(--kx-*, var(--success, #16a34a))` 保证即使 token 未挂载也不会硬编码紫蓝色系。

## API 契约

### 新增端点（super_admin only）

```http
PATCH /api/routing/candidate-binding/{credential_id}?raw_model={model_name}
Content-Type: application/json
Authorization: Bearer <jwt>

{
  "manual_priority": 5,    // 可选
  "routing_tier": 2,       // 可选
  "weight": 120            // 可选
}
```

**响应**：`200 {"message":"updated","binding_id":N,"actor":"..."}`

**失败码**：
- 400：缺少 raw_model / 没有任何字段 / 字段越界 / credential_id 非整数
- 404：找不到 (credential_id, raw_model_name) 对应的 binding
- 403：非 super_admin
- 500：DB 异常

### 复用端点

- `PATCH /api/providers/{pid}/credentials/{cid}/manual-disabled`（已有，写入 manual_disabled + 写入 model_offer_events）
- `PATCH /api/providers/{pid}/credentials/{cid}/lifecycle`（已有，写入 lifecycle_status）

## 修改文件清单

### 后端
- `admin/routing.go` — 新增 `handleRoutingCandidateBindingUpdate`（输入校验 → binding_id 查表 → COALESCE UPDATE → audit log）
- `admin/handler.go` — 在 `RegisterRoutes` 中注册 `mux.HandleFunc("/api/routing/candidate-binding/", h.superAdmin(h.handleRoutingCandidateBindingUpdate))`
- `admin/routing_candidate_binding_test.go` — 新增 7 个表驱动输入校验单测（method / credential_id / raw_model / body / 三字段越界）

### 前端
- `web/src/components/routing/CandidateDetailDrawer.vue` — 新增，8 组 30+ 标志位的诊断抽屉
- `web/src/components/routing/CandidateSettingsDialog.vue` — 新增，仅 super_admin 可见，乐观更新 + 失败回滚
- `web/src/api/routing.ts` — 新增 `patchCandidateBinding()` 客户端
- `web/src/views/RoutingDashboardView.vue` — resolve 块表格重写：默认展示全部候选 + 5 列精简 + "明细/设置"入口 + drawer/dialog 挂载

## 验证

### 结构验证
- `go build ./...` ✅
- `go vet ./admin/...` ✅
- `go test -count=1 -run TestRoutingCandidateBindingUpdate_InputValidation ./admin/...` ✅（7/7 输入校验单测）
- `npx vue-tsc --noEmit` ✅
- `npx vite build` ✅（942 modules，dist 生成成功）

### 浏览器实测（限制）
- 用 `vite preview` 起 dist（端口 4174），`browser-use open http://localhost:4174/routing-v2?tab=resolve`
- 命中路由守卫 `/login` → `LoginModal` 自动弹出，admin 预填账号（截图 `docs/screenshots/ui-verify-routing-resolve-login-modal-20260724.png`）
- 由于 vite preview 不附带后端 `/api/auth/token`，无法完成完整登录 → resolve 渲染 → 双主题截图链路
- **生产级双主题实测**：需要在部署到 245/dev/154 之后，用真实 super_admin JWT 走完登录 + resolve 查询 + 明细抽屉 + 设置对话框的全链路，并截图保留证据

### 缺失风险与下一步
- **必须**在 245/dev/154 部署后用真实凭据走 E2E：登录 → resolve 查询 → 明细抽屉 → 设置对话框 + 切 daylight/night 主题
- 长期监控：`routing_audit_log.action = 'routing_candidate_binding_update'` 的写入频率
- 设置页面的 `manual_disabled` 写入流走原 900-series endpoint，复用既有 audit，不需要额外 schema

## i18n 备注

UI 文案目前用中文硬编码（与同文件其它 resolve 区文案风格一致），未走 `web/src/locales/<lang>/routing.ts` —— 同 resolve 区其它标题/列头也都是中文。后续如需全 i18n 翻译，按 2026-07-23 i18n parity 任务的同流程补齐。