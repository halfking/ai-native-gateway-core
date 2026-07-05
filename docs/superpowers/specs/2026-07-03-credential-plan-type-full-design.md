# Credential Plan Type — 统一标准与路由即时性 (设计 v1)

> **Status:** Approved (2026-07-03)
> **Owner:** llm-gateway-go 凭据+路由团队
> **Supersedes:** [migrations/131_credential_plan_type.sql](../../../migrations/131_credential_plan_type.sql)（旧实现保留为回滚锚点）
> **Related files**:
> - 后端：[`modelcatalog/upsert.go`](../../../modelcatalog/upsert.go), [`admin/provider_credential.go`](../../../admin/provider_credential.go), [`admin/pricing.go`](../../../admin/pricing.go), [`provider/client.go`](../../../provider/client.go)
> - 前端：[`web/src/views/ProvidersView.vue`](../../../web/src/views/ProvidersView.vue), [`web/src/views/PricingManagementView.vue`](../../../web/src/views/PricingManagementView.vue), [`web/src/api/providers.ts`](../../../web/src/api/providers.ts)
> - 数据库：[`migrations/136_credential_plan_type_full.sql`](../../../migrations/136_credential_plan_type_full.sql)

---

## 1. Motivation

### 1.1 现状（4 处计费分类 + 多处语义漂移）

| 存储位置 | 字段 | 枚举 | 写入路径 | 路由读 |
|---|---|---|---|---|
| `credentials` | `plan_type` (131 加的) | `token, token_plan, code_plan, agent_plan, monthly` | 131 自动推断 + 无 UI | ✅ |
| `credential_model_bindings` | `billing_mode` | `per_token`(默认) / `token_plan` / `code_plan` / `agent_plan` / `free` / `monthly` | `UpsertCredentialModel` 不写 → 走列默认 `per_token` | ❌ |
| `model_offers` | `billing_mode` | 同上 | pricing UI 编辑器 + 触发器 | ✅ ← ← **bug 根源** |
| 路由判定 | `v_routable_credential_models` | n/a | `MO.billing_mode ∧ C.plan_type` 交叉检查 | ✅ |

### 1.2 根因（与原 bug 报告一致）

- [discovery/discovery.go:691](../../../discovery/discovery.go:691) → [modelcatalog/upsert.go:43-48](../../../modelcatalog/upsert.go:43) 的 cmb INSERT **未写 billing_mode** → 走列默认 `'per_token'`
- 2026-06-12 cny-fix 脚本 [docs/pricing/2026-06-12-cny-fix-all-credentials.sql](../../../docs/pricing/2026-06-12-cny-fix-all-credentials.sql) 把 cred 6/7/11/12 的 cmb.billing_mode 从 `per_token` 改成 `token`，但**没改成 `token_plan`**（脚本作者当时把它们当作普通 token 套餐）
- 131 把 cred 6 的 `plan_type` 升为 `token_plan`（因为 `active_plan_id IS NOT NULL`），但 cmb 仍是 `token` / `per_token`
- v 视图 [migrations/131_credential_plan_type.sql:74-79](../../../migrations/131_credential_plan_type.sql:74) 检测到 `c.plan_type IN (套餐型) ∧ mo.billing_mode NOT IN (套餐型)` → cred 6 的 11 个模型全部 `is_routable=FALSE, reason='plan_incompatible_model_requires_per_token'`

### 1.3 目标

1. **单一标准类型化**：plan_type 是套餐 SSOT，billing_mode 是派生
2. **计费模式与路由分离**：Pricing 页只管价格，不再决定路由；「免费」开关单独控制
3. **凭据级 UI 控制**：在 `https://__DOMAIN_2__/providers/{id}` 凭据表中直接观察+设置 plan_type
4. **路由即时性**：plan_type / billing_mode / free 切换后，路由 `is_routable` 在毫秒级（同一请求内）正确反映

---

## 2. Decisions (Q1-Q5)

| 维度 | 选择 | 含义 |
|---|---|---|
| **Q1 字段模型** | 双字段并存 | `plan_type` 凭据级 SSOT + `billing_mode` 派生计费 |
| **Q2 billing_mode 存哪** | 只在 cmb | model_offers.billing_mode 退役，路由不再读 |
| **Q3 pricing 页** | 只管价格 | 「免费」另设开关控制 `cmb.billing_mode='free'` |
| **Q4 凭据 UI** | 表加列 | 「套餐」select + 「计价」只读 chip |
| **Q5 discovery 默认** | 拷贝 plan_type | `UpsertCredentialModel` 写入 cmb 时复制 |

---

## 3. Architecture (4 层职责)

```
┌─────────────────────────────────────────────────────────────────────┐
│ ① 后端 SSOT 层（credentials.plan_type）                           │
│    POST/PATCH 凭据 → UPDATE credentials.plan_type                  │
│    admin/provider_credential.go 三处改造                         │
├─────────────────────────────────────────────────────────────────────┤
│ ② 派生层（credential_model_bindings.billing_mode）                 │
│    Discovery → UpsertCredentialModel INSERT cmb 时拷贝            │
│    Pricing setFreeModels 批量写 cmb.billing_mode='free'            │
│    规则: token → per_token | 其余同名                             │
├─────────────────────────────────────────────────────────────────────┤
│ ③ 路由判断层（v_routable_credential_models 视图重构）              │
│    drop LEFT JOIN model_offers                                     │
│    兼容性检查改用 cmb.billing_mode vs credentials.plan_type        │
├─────────────────────────────────────────────────────────────────────┤
│ ④ UI 控制层（凭据表 + Pricing 页）                                 │
│    凭据表: 「套餐」select 写 credentials.plan_type + 「计价」只读   │
│    Pricing 页: 删 billing_mode 编辑器 + 新增「免费」开关           │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 4. Data Model

### 4.1 DB 迁移：`migrations/136_credential_plan_type_full.sql`

```sql
-- 1. credentials.plan_type CHECK 约束收紧 + 枚举扩展
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_plan_type_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_plan_type_check
  CHECK (plan_type IN ('token','token_plan','code_plan','agent_plan','monthly','free'));

-- 2. cmb 添加 plan_type_origin 标签（backfill 不能覆盖手工）
ALTER TABLE credential_model_bindings
  ADD COLUMN IF NOT EXISTS plan_type_origin TEXT DEFAULT 'auto'
  CHECK (plan_type_origin IN ('auto','manual','backfill'));

-- 3. backfill 脚本（修 cred 6 / minimax-prod-1 等历史脏数据）
UPDATE credential_model_bindings cmb
SET billing_mode = CASE c.plan_type
        WHEN 'token' THEN 'per_token'
        ELSE c.plan_type END,
    plan_type_origin = 'backfill',
    updated_at = NOW()
FROM credentials c
WHERE c.id = cmb.credential_id
  AND cmb.plan_type_origin = 'auto'     -- 跳过已手动改过的
  AND cmb.billing_mode != CASE c.plan_type
        WHEN 'token' THEN 'per_token'
        ELSE c.plan_type END;

-- 4. 视图重构：v_routable_credential_models
DROP VIEW IF EXISTS v_routable_credential_models CASCADE;
CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
    cmb.billing_mode,                  -- SSOT: 单一来源
    c.plan_type,
    CASE
        WHEN c.status NOT IN ('active','cooling','degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN cmb.available IS NOT true THEN false
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted'
             AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false
        WHEN c.availability_state = 'unavailable'
             AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false

        WHEN c.plan_type IN ('token_plan','code_plan','agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan') THEN false
        WHEN cmb.billing_mode IN ('token_plan','code_plan','agent_plan')
             AND c.plan_type NOT IN ('token_plan','code_plan','agent_plan') THEN false

        ELSE true
    END AS is_routable,
    CASE
        WHEN c.status NOT IN ('active','cooling','degraded') THEN 'credential_status_'||c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_'||c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'
        WHEN c.plan_type IN ('token_plan','code_plan','agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan','code_plan','agent_plan')
             THEN 'plan_incompatible_cmb_requires_' || COALESCE(cmb.billing_mode,'per_token')
        WHEN cmb.billing_mode IN ('token_plan','code_plan','agent_plan')
             AND c.plan_type NOT IN ('token_plan','code_plan','agent_plan')
             THEN 'plan_incompatible_credential_not_' || cmb.billing_mode
        ELSE NULL
    END AS unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;
```

### 4.2 Go 代码变更清单

| 文件 | 改动 | 备注 |
|---|---|---|
| `modelcatalog/upsert.go:43-48` | INSERT 加 `billing_mode, plan_type_origin`，值从 JOIN `credentials` 取 | 方案 A 执行点 |
| `admin/provider_credential.go:36-100 addCredential` | 加 `plan_type` 入参，写入 `credentials.plan_type` | 默认 `token` |
| `admin/provider_credential.go:294-407 updateCredential` | 加 `plan_type` UPDATE 分支 | 同事务再 UPDATE cmb.billing_mode |
| `admin/provider_credential.go:106-144 listCredentials` | SELECT 加 `c.plan_type`, `cmb.billing_mode` | JSON 显式曝光 |
| `admin/pricing.go` 新增 `setFreeModels` | POST `/api/providers/{id}/set-free-models` body=`{raw_model_names, free}` | 批量 UPDATE cmb |
| `provider/client.go:693` | `mo.billing_mode` 改读 `cmb.billing_mode` (join) | 不依赖 mo |
| `provider.InvalidateAllCandidateCache()` | updateCredential + setFreeModels 调用点都已包含 | 由 provider 包导出 |
| `admin/routing.go:217` | 保留 `v.billing_mode`（视图已导出） | 不变 |
| `admin/routing.go:1833` | `mo.billing_mode` 改读 `cmb.billing_mode` (join) | 同上 |

### 4.3 前端代码变更清单

| 文件 | 改动 |
|---|---|
| `web/src/api/providers.ts` | `ProviderCredential` 加 `plan_type?: string`、`cmb_billing_mode?: string \| null`；`addCredential` 加 `plan_type?`；`updateCredential` 加 `plan_type?` |
| `web/src/api/pricing.ts`（新建） | `setFreeModels(providerId, rawModelNames, free: boolean)` |
| `web/src/views/ProvidersView.vue` | 凭据表新增「套餐」select + 「计价」chip；saveCredential 同步 plan_type 入 PATCH body |
| `web/src/views/PricingManagementView.vue` | 删除 `offer.billing_mode` 编辑器；新增 checkbox 列「免费」→ 调 setFreeModels |

### 4.4 Sync 即时性契约（关键 — 见 4.1）

**任何改 plan_type / cmb.billing_mode 的 handler 必须三步同栈：**

```go
// Step 1: DB 写（事务内） — 视图下一 SELECT 即反映
tx, _ := h.db.Begin(ctx); defer tx.Rollback(ctx)
tx.Exec(ctx, `UPDATE credentials SET plan_type=$1 WHERE id=$2`, plan, credID)
if bundleSync {
    tx.Exec(ctx, `
        UPDATE credential_model_bindings cmb
        SET billing_mode = CASE WHEN c.plan_type='token' THEN 'per_token' ELSE c.plan_type END,
            plan_type_origin = 'auto', updated_at = NOW()
        FROM credentials c WHERE c.id = cmb.credential_id AND cmb.credential_id = $1
    `, credID)
}
tx.Commit(ctx)

// Step 2: 路由缓存立即失效
provider.InvalidateAllCandidateCache()

// Step 3: 后台自检 + 日志（不阻塞响应）
go func() { /* slog.Info("post-update routing check", ...) */ }()
```

---

## 5. Data Flow

### 场景 A：管理员在凭据页把 plan_type 从 `token` 改为 `token_plan`
- 浏览器 PATCH `credentials.plan_type` → handler 事务内级联 UPDATE `cmb.billing_mode` → 提交 → cache invalidate → 路由 `is_routable=true`

### 场景 B：Discovery 发现新模型
- `UpsertCredentialModel` INSERT cmb → billing_mode = credentials.plan_type（新建 cmb 同步）

### 场景 C：Pricing 页把某模型标记为「免费」
- POST setFreeModels → UPDATE cmb.billing_mode='free' AND plan_type_origin='manual' → cache invalidate → 路由下次 SELECT 看到 free 模型进入免费池

---

## 6. Error Handling

| 失败点 | 表现 | 处理 |
|---|---|---|
| 不合法 plan_type | DB CHECK 拒绝 | handler 白名单 `isValidPlanType()` 校验，400 + 枚举提示 |
| Cmb 行与 plan_type 不一致 | 视图返回 `is_routable=false` + reason | admin UI 加 banner 「N 行 cmb 不一致」 + 「同步 cmb 按钮」 |
| Backfill 失败 | 事务回滚 | 单一事务；失败记录 runtask_errors |
| Front-end PATCH 后 cmb 未刷新 | 路由短暂误判 | handler 同步模式 (A) 已规避；前端 `await loadCredentials()` 后再 toast |
| 「免费」切换 | 立即生效 | UI 加二次确认 dialog：「将立即影响路由」 |

---

## 7. Testing

### 7.1 单测（Go）

| 测试 | 文件 | 断言 |
|---|---|---|
| `TestUpsertCredentialModelWritesBillingMode` | `modelcatalog/upsert_test.go` (新建) | INSERT 后 cmb.billing_mode = credentials.plan_type |
| `TestUpsertCredentialModelManualOriginUntouched` | 同上 | `plan_type_origin='manual'` 不被 backfill 覆盖 |
| `TestViewPlanIncompatibleFalse` | `db_test.go` | cred plan_type='token_plan' + cmb billing_mode='per_token' → is_routable=false |
| `TestViewPlanCompatibleTrue` | 同上 | 两者均为 'token_plan' → is_routable=true |
| `TestUpdateCredentialPlanTypeValid` | `provider_credential_test.go` | PATCH 合法 plan_type 后 SELECT 读到新值 |
| `TestUpdateCredentialPlanTypeInvalid` | 同上 | PATCH `plan_type='garbage'` → 400 |
| `TestBackfillIdempotent` | 136 migration test | 重复跑 0 行 affected |
| `TestSetFreeModels` | `pricing_test.go` (新建) | 调 POST 后 cmb.billing_mode='free' 且 plan_type_origin='manual' |
| `TestRoutingImmediatelyCorrect` | `routing_test.go` | PATCH plan_type 后立即 SELECT v 视图验证 |

### 7.2 单测（前端）
- `ProvidersView`: 套餐 select 改变 → PATCH body 含 `plan_type`
- `PricingManagementView`: 免费 checkbox → setFreeModels body 正确
- `providers.ts`: 类型 `plan_type` 字面量联合 + API 类型校验

### 7.3 E2E（184 SSH）
1. T1 → T4 顺序部署 → 重启
2. 凭据表 #6 把 plan_type 从 `token_plan` 改成 `token` → 11 行 cmb routing 立即变 false
3. 改回 `token_plan` → 立即变 true（验路由即时性）
4. Pricing 页勾「免费」某个模型 → admin/routing 立即见 free 模型进池
5. 压测 1k 请求，确认无 plan_incompatible_* 错误日志

---

## 8. Migration Order

```
T0  pg_dump 备份
T1  apply migrations/136 (DB-only, view 重建)
T2  立即 SELECT count(*) FILTER (WHERE is_routable) FROM v WHERE credential_id = 6; → 预期 11
T3  set DISCOVERY_ENABLED=false (防新发现模型 cmb 走老默认值)
T4  apply Go 代码（modelcatalog + admin + provider）
T5  apply 前端代码（独立可单独回滚）
T6  unset DISCOVERY_ENABLED
T7  5 步烟测
T8  unset DISCOVERY_ENABLED=true（默认 ON）
```

**T3-T6 窗口期**：旧 Go 代码发现新模型会写错 cmb.billing_mode，故禁 discovery。

---

## 9. Rollback Plan

**阶段 A（仅 Go 代码回滚）**：git checkout previous-tag → deploy app only；不回退 DB（DB 写是无损的 DDL）

**阶段 B（仅回滚视图 4.1 节 ④）**：psql 应用 131 + 326 综合视图版本（不含 plan_type check）

**阶段 C（完全回滚）**：psql 应用 136.down.sql
```sql
DROP VIEW IF EXISTS v_routable_credential_models CASCADE;
CREATE OR REPLACE VIEW v_routable_credential_models AS ... (131+326 综合版);
ALTER TABLE credential_model_bindings DROP COLUMN IF EXISTS plan_type_origin;
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_plan_type_check;
```
+ git checkout previous-tag + deploy

---

## 10. Acceptance Gates (上线必须 5/5)

| # | 命令 / 操作 | 预期 |
|---|---|---|
| 1 | `SELECT count(*) FILTER (WHERE is_routable) FROM v_routable_credential_models WHERE credential_id = 6;` | = 11 (cmb 总数) |
| 2 | `SELECT billing_mode, count(*) FROM cmb WHERE credential_id = 6 GROUP BY 1;` | 仅 1 行 = `token_plan` |
| 3 | admin UI 凭据表 #6 行 套餐 select 选 `token_plan` → 保存 → 截图验 cmb 列显式一致 | UI 显式一致 |
| 4 | Pricing 页模型 X 勾「免费」→ admin/routing 选该模型 → 候选列表最低成本排序第一 | free 立即生效 |
| 5 | `curl :__PORT_3__/api/routing/minimax-m3` v view | 该 cred cmb `is_routable=true` 且 `unavailable_reason=NULL` |

---

## 11. Deliverables

| # | 文件 | 类型 |
|---|---|---|
| 1 | `migrations/136_credential_plan_type_full.sql` + `.down.sql` | DB |
| 2 | `modelcatalog/upsert.go` (line 43-48) 改 | Go |
| 3 | `admin/provider_credential.go` addCredential/updateCredential/listCredentials 改 | Go |
| 4 | `admin/pricing.go` setFreeModels 端点 | Go |
| 5 | `admin/routing.go` mo→cmb.billing_mode | Go |
| 6 | `provider/client.go:693` mo→cmb | Go |
| 7 | `web/src/api/providers.ts` 加 plan_type/cmb_billing_mode | FE |
| 8 | `web/src/api/pricing.ts` 新增 setFreeModels | FE |
| 9 | `web/src/views/ProvidersView.vue` 表加两列 | FE |
| 10 | `web/src/views/PricingManagementView.vue` 删编辑器+加开关 | FE |
| 11 | 单测：`modelcatalog/upsert_test.go` (新建), `pricing_test.go` (新建) | Go test |
| 12 | 部署报告：`deployment-report-20260703-plan-type-full.md` | Doc |

---

**下一步**：调用 `writing-plans` skill 把这份设计稿分解为可执行计划（按步骤、依赖、TDD、verification gates 排列）。
