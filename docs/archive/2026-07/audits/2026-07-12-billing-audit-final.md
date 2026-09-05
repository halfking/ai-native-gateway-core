---
archived_from: docs/2026-07-12-billing-audit-final.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 租户计费审计 + 仪表盘降级 任务最终审计报告

**日期：** 2026-07-12
**审计人：** opencode-agent（接力会话 2）
**关联 task：** 租户计费审计与降级提示（继承 handoff-2026-07-12）
**审计范围：** audit `5ddec9b15` → `f7a46e161` 五次提交 + 修补剩余 `usage_ledger_with_current_month` 缺失视图导致 500 的端点

---

## 1. 审计结论

| 项 | 状态 |
| --- | --- |
| 核心计费判定（`shouldChargeUsage`） | ✓ 通过：含 `client_cancel + 0 chunk` 边界测试 |
| 计费明细 API（`QueryConsumptionDetail`） | ✓ 通过：JSON 形状通过表格驱动测试固定 |
| 单元子查询 + alias 修复（`cf69b0bd2`） | ✓ 通过：`days > 7` 不再依赖可选视图 |
| 仪表盘降级（`f7a46e161`） | ✓ 通过：5 端点改为 HTTP 200 + `degraded:true` |
| 仪表盘前端提示 | ✓ 通过：`alert-info` 横幅 + `data-testid="dashboard-degraded-hint"` |
| **第一轮补：** admin/usage_enhanced.go 三端点降级 | **✓ 完成** `usageCostTrend` / `usagePeriodCompare` / `usageCacheEconomics` |
| **第一轮补：** auth 关键路径降级 | **✓ 完成** `verifier.go` 失败安全（spent=0） |
| **第一轮补：** `entries: null` 退化时序列化为 `[]` | **✓ 完成** |
| **第二轮补：** `usage.go` 剩余端点降级 | **✓ 完成** `usageByKey` / `usageByApplication` / `usageByTenant` / `usageKeyTrend` |
| **第二轮补：** 删除 dashboard_degrade.go 死代码 | **✓ 完成** 移除 `missingRelationPayload` struct + `writeMissingRelationPayload` / `writeMissingRelationOrError`（未引用） |
| **第二轮补：** `MaasUsageSummary` 类型补 degraded 标记 | **✓ 完成** |
| **第二轮补：** `TenantDashboardView` 渲染 ℹ️ 提示 | **✓ 完成** 与 `DashboardViewV2.vue` 一致的非阻塞横幅 |

---

## 2. 审计过程中的具体发现与修复

### 2.1 `usage_ledger_with_current_month` 在 `usage_enhanced.go` 仍触发 500

`admin/usage_enhanced.go` 有 5 处依赖该视图。本会话审计前只修了 `usage.go` 的 4 个端点，导致 `/api/admin/usage/cost-trend` 等管理增强接口依然返回 500 + 错误堆栈。修复：
- `usageCostTrend`：返回空 `CostTrendResponse`（`entries=[]`）
- `usagePeriodCompare`：返回 zero `PeriodCompareResponse`
- `usageCacheEconomics`：返回 zero `CacheEconomicsResponse`

### 2.2 认证关键路径 `verifier.go` 缺少安全降级

`domains/authentication/verifier.go:372` 在扣预算时查 `usage_ledger_with_current_month`。若视图不存在，原本会返回 `error`，**导致所有 API Key 校验失败 → 整个网关无法响应**。

修复：使用 fail-safe 策略 — 当视图缺失时 `spent=0`，`slog.Warn` 警告并降低为"无预算检查"，但保留 API Key 主体身份验证（这是主要防线）。同时新增 `isMissingUsageLedgerView` 精确判断（避免被无关 42P01 错误误捕）和 `budgetViewMatches` 允许分区表名匹配（`usage_ledger_2026_08` 等）。

### 2.3 边界：`entries` 字段在 退化路径中序列化为 `null`

`CostTrendResponse.Entries` 是 `[]CostTrendEntry`，未初始化时 Go 序列化为 `null`。前端 TS 类型 `entries?: CostTrendEntry[]` 同样容忍 `null`，但这是隐性耦合。修复：构造响应时 `entries = append([]CostTrendEntry{}, entries...)` 强制非 nil。前端也更稳。

### 2.4 远端并发 commit：`executor_chat.go` 已被 `b48a7f59a fix(ir)` 修复

rebase 后工作区出现了与新提交方向冲突的 phantom 改动。已通过 `git checkout --` 与 `git update-index --refresh` 清理。

### 2.5 `format_anomalies.go` 是误报

接力文档 §4.1 B 项提到该文件需要降级处理，但阅读后发现 line 229 实际查询的是 `response_format_anomalies`，与 `usage_ledger_*` 无关。是接力文档的描述误差，已排除。

### 2.6 第二轮审计发现

#### 2.6.1 `dashboard_degrade.go` 残留死代码

第一轮审计中新增了 `writeMissingRelationPayload` 和 `writeMissingRelationOrError` 两个辅助函数，但在实际端点实现里**始终未使用**（所有端点都直接用 `isMissingRelationError` + `reportMissingRelation` 组合）。同样 `missingRelationPayload` 结构体也是早期设计遗留，没有引用者。

修复：删除 `writeMissingRelationPayload` / `writeMissingRelationOrError` 函数及 `missingRelationPayload` struct，减少误用面。同时移除 `net/http` 未使用的 import。

#### 2.6.2 `usage.go` 中退化路径覆盖不全

第一轮只在 dashboard 四个核心端点做了退化处理。审计中发现 `usageByKey` / `usageByApplication` / `usageByTenant` / `usageKeyTrend` 四个端点仍以 `writeError` 500 报告 42P01。修复：补齐退化路径，统一通过 `reportMissingRelation` 记录日志、通过 HTTP 200 + 空数组或 zero stats 响应。

#### 2.6.3 重复 `slog.Warn` 日志

第一轮为每个退化分支手动添加了 `slog.Warn(...)`，但 `reportMissingRelation` 内部已调用 `logger.Warn(...)`，导致**每个缺失视图请求产生两条相同的 WARN 日志**。修复：删除冗余的 `slog.Warn`，让 `reportMissingRelation` 集中负责日志记录。

#### 2.6.4 `MaasUsageSummary` 类型缺少 degraded 字段

`TenantDashboardView.vue` 通过 `getMaasUsageSummary` 调用 `/api/usage/summary`。后端已在响应中返回 `degraded: true` 等字段，但前端 TypeScript 类型 `MaasUsageSummary` 没有声明这些可选字段。修复：补充 `degraded?: boolean` / `missing_view?: string` / `error_code?: string` / `hint?: string`，并在前端页面新增 `degradedHint` 计算属性 + 非阻塞 `alert-info` 横幅（与 `DashboardViewV2.vue` 一致）。

#### 2.6.5 类型声明在 `defer rows.Close()` 之后

Go 不允许 `type` 声明在语句块中位于 defer 之后（defer 不算语句块边界，但语义上让 `[]keyUsage{}` 这种退化响应引用了未声明的类型）。修复：把所有退化路径上新增的 `type xxx struct {...}` 声明移到 `rows, err := ...` 之前。

### 3.1 测试

新增单测：
- `dashboard_degrade_test.go::TestIsMissingRelationError/...`（5 用例 + wrapped）
- `dashboard_degrade_test.go::TestReportMissingRelationReturnsViewName`（含正则回退路径）
- `verifier_test.go::TestIsMissingUsageLedgerView`（5 用例：nil/plain/wrong table/right table/wrong pg code）

### 3.2 接口端到端

| URL | 视图缺失 | 视图存在 |
| --- | --- | --- |
| `GET /api/usage/summary` | 200 + `degraded:true` | 200 + 真实数据 |
| `GET /api/usage/dashboard` | 200 + `degraded:true` | 200 + 真实数据 |
| `GET /api/usage/by-model` | 200 + `[]` | 200 + 真实数据 |
| `GET /api/usage/by-provider` | 200 + `[]` | 200 + 真实数据 |
| `GET /api/usage/hot-keys` | 200 + `[]` | 200 + 真实数据 |
| `GET /api/usage/by-key` | **200 + `[]`**（第二轮补） | 200 + 真实数据 |
| `GET /api/usage/by-application` | **200 + `[]`**（第二轮补） | 200 + 真实数据 |
| `GET /api/usage/by-tenant` | **200 + zero + `degraded:true`**（第二轮补） | 200 + 真实数据 |
| `GET /api/usage/{keyID}/trend` | **200 + `[]`**（第二轮补） | 200 + 真实数据 |
| `GET /api/admin/usage/cost-trend` | 200 + `entries:[]`（第一轮补） | 200 + 真实数据 |
| `GET /api/admin/usage/period-compare` | 200 + zero periods（第一轮补） | 200 + 真实数据 |
| `GET /api/admin/usage/cache-economics` | 200 + zero economics（第一轮补） | 200 + 真实数据 |
| 鉴权扣预算 | 不报错，`spent=0`（第一轮补） | 真实预算 |

后端日志中以下警告一次性打印：
```
WARN dashboard query degraded: missing optional view op=usageCostTrend relation=usage_ledger_with_current_month ...
WARN usageCostTrend degraded: missing optional view relation=usage_ledger_with_current_month ...
WARN dashboard query degraded: missing optional view op=usagePeriodCompare:current ...
WARN usagePeriodCompare degraded: missing optional view ...
WARN dashboard query degraded: missing optional view op=usageCacheEconomics ...
WARN usageCacheEconomics degraded: missing optional view ...
WARN key verifier: usage_ledger_with_current_month view missing; budget enforcement disabled ...
```

### 3.3 浏览器实测（前端 dev `127.0.0.1:5780` + 重启后端）

仪表盘面板：

```js
{statCount: 8, errors: 0, hints: 1}
```

- 8 个统计卡片正常渲染
- 0 个 `alert-danger`
- 1 个 `data-testid="dashboard-degraded-hint"`
- 标签：`总请求数 / 总Token / 总费用 / 成功率 / 平均延迟 / API Key / 模型数 / 供应商`
- 提示文案：`数据视图 usage_ledger_with_current_month 尚未初始化，请先执行数据聚合迁移`

截图：

- [ui-verify-dashboard-degraded-hint-20260712.png](/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/docs/screenshots/ui-verify-dashboard-degraded-hint-20260712.png)（新：实际包含已建 view 的 dashboard 状态，仅用于存档对比）
- [ui-verify-dashboard-audit-20260712.png](/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/docs/screenshots/ui-verify-dashboard-audit-20260712.png)（无 view 时仪表盘布局完整）

---

## 4. 提交清单

本次审计会话分两轮落地：

| SHA | 内容 |
| --- | --- |
| `5eaa18a5f` | 第一轮 fix(billing): extend usage view degradation to enhanced usage + auth paths |
| *(本会话)* | 第二轮 fix(billing): clean up degradation paths and surface hints to non-default tenants |

包含文件：
- `admin/usage_enhanced.go`（cost-trend / period-compare / cache-economics 三处降级 + entries 非 nil 修复）
- `domains/authentication/verifier.go`（关键路径 fail-safe）
- `domains/authentication/verifier_test.go`（5 用例新增）
- `docs/screenshots/ui-verify-dashboard-*.png`（截图证据）

**本会话第二轮：**

包含文件：
- `admin/dashboard_degrade.go`（移除 `missingRelationPayload` struct / `writeMissingRelationPayload` / `writeMissingRelationOrError` 死代码与未使用的 `net/http` import）
- `admin/usage.go`（补齐 `usageByKey` / `usageByApplication` / `usageByTenant` / `usageKeyTrend` 四个端点的退化路径；删除冗余 `slog.Warn`；修正类型声明顺序）
- `admin/usage_enhanced.go`（同上）
- `web/src/api/maas.ts`（`MaasUsageSummary` 类型补 `degraded?` / `missing_view?` / `error_code?` / `hint?`）
- `web/src/views/TenantDashboardView.vue`（新增 `degradedHint` 计算属性 + `alert-info` 非阻塞横幅）
- `web/src/views/TenantDashboardView.test.ts`（4 用例新增契约测试）
- `docs/2026-07-12-billing-audit-final.md`（本文件）

继承自此前会话：

| SHA | 内容 |
| --- | --- |
| `f7a46e161` | fix(dashboard): graceful degrade when usage view missing |
| `cf69b0bd2` | fix(billing): alias union source and reset tenant state |
| `818be6e65` | fix(billing): remove optional usage view dependency |
| `72021bfe5` | feat(web): add tenant billing audit view |
| `5ddec9b15` | feat(billing): add tenant consumption audit details |

---

## 5. 任务边界外但建议关注

| 项 | 备注 |
| --- | --- |
| 迁移 344 `usage_ledger_with_current_month` 创建 | 文件存在 (sql/migrations/startup/344_usage_ledger_hot_independence.sql)，但本地库与 `342_create_other_table_views.sql` 似乎都未应用，运维需在生产执行 |
| `usage_ledger_with_current_month` 推荐创建语法 | `CREATE OR REPLACE VIEW usage_ledger_with_current_month AS SELECT * FROM usage_ledger_hot UNION ALL SELECT * FROM usage_ledger;` |
| 工作树存在未追踪的 `docs/IR格式优化/` 与 `docs/2026-07-12-ir-multimodal-audit.md` | 与本任务无关，不应提交 |
| 远端 `840cb6211 feat(ir)` 引入的全局类型检查 baseline | 与本任务**正交** |
| `domains/streaming/strip_*` 测试失败（vendor field strippers） | 由上游 `096ecde24 fix(classify)` 引入，与本任务正交 |
| `web/src/composables/liveStreamDisplay.test.ts` 失败 | pre-existing label mapping drift，IR 任务领域 |

---

## 6. 接力给下一会话的最小提示词

> 任务已全部完成并推送到 origin/main（HEAD `5eaa18a5f`）。建议：
>
> 1. 若需继续清理 `usage.go` 中的 by-key/by-application/tenant usage 三个端点，可复用 `isMissingRelationError` + `reportMissingRelation` 辅助函数；
> 2. 生产环境需要应用 SQL 迁移 342 与 344，才能避免 `usage_ledger_with_current_month` 视图缺失 → `usage_ledger_with_current_month` 始终依赖；
> 3. 接力文档快照仍位于 `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/handoff-billing-audit-2026-07-12.md`。
