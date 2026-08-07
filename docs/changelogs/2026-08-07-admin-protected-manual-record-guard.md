# 2026-08-07 — admin_protected 手工凭据模型记录只允许手工删除

## 背景

用户手工添加的供应商凭据，其下模型记录是用户主动维护的。此前批量处理
（自动刷新 / discovery 重新拉取）、探针（model_probe）、健康检查
（credential health checker）等自动路径在 `credential_model_bindings`
上的 UPDATE 会无差别覆盖这些手工记录（改 available / unavailable_reason /
updated_at），导致用户手工加装的模型被自动流程"洗掉"或状态被覆盖。

需求：**手工添加的凭据模型记录只能由管理员显式操作删除/修改，任何自动
路径（批量处理 / 自动刷新 / 探针 / 健康检查）不得更新**。

## 机制

`credential_model_bindings.admin_protected`（TRUE = 手工添加）作为守卫标记：

- 自动路径 UPDATE 一律追加 `AND COALESCE(cmb.admin_protected, FALSE) = FALSE`
- `modelcatalog.UpsertCredentialModel` 的 ON CONFLICT 分支整体跳过
  admin_protected 记录（连 updated_at 都不动）
- `v_suspicious_probe_targets` 视图排除 admin_protected 记录（自动探针
  根本选不到手工记录）

## 改动清单

### 1. 手工 INSERT 打标（写入口）

| 文件 | 位置 | 改动 |
|---|---|---|
| `admin/free_pool_extra.go` | pool_manager 注册 | INSERT 加 `admin_protected` 列/VALUES TRUE + `pinAdminProtectedOffers` |
| `admin/routing.go` | free_pool_register (L3372+) | 同上 |
| `admin/routing.go` | pool_manager (L3966+) | 同上 |
| `admin/provider_offer_force_recover.go` | 新增 | `pinAdminProtectedOffers(ctx, credentialID, rawModelNames)` 辅助函数（失败仅 slog.Warn） |

### 2. 自动路径守卫（读/改入口）

| 文件 | 位置 | 守卫 |
|---|---|---|
| `modelcatalog/upsert.go` | UpsertCredentialModel ON CONFLICT | `WHERE COALESCE(credential_model_bindings.admin_protected, FALSE) = FALSE` 整体跳过 |
| `db/db.go` | ensureProbeStateFunctionFixes | 4 个 probe 函数（mark_available / mark_unavailable / unified_mark_healthy / unified_mark_failing）cmb UPDATE 加 `AND COALESCE(cmb.admin_protected,FALSE)=FALSE` |
| `db/db.go` | L1934 启动 backfill、L1999 迁移 backfill | 同上守卫 |
| `bg/model_probe.go` | reconcileBrokenConfirmedBindings / broken_confirmed / healthy_confirmed | 同上守卫 |

**已核实带守卫、无需改**：`bg/node_probe.go` (1646/1661)、
`bg/credential_recovery.go` (mnfCoolingRecoverySQL)、
`credentialhealth/checker.go` (markDegraded)、`discovery/discovery.go` (818/859)。

### 3. 视图排除（自动探针选不到手工记录）

- `sql/migrations/startup/468_v_suspicious_probe_targets_admin_protected.sql`
  （+ `.down.sql`）— 视图重定义，EXISTS 绑定子句加
  `AND COALESCE(cmb.admin_protected, false) = false`
- 四副本同步：
  - `sql/schema/01-schema.sql`（视图 L17949 + 4 个 probe 函数定义同步守卫）
  - `deploy/sql/schemas/baseline/01-schema.sql`（视图 L17945 + 函数）
  - `sql/objects/views/v_suspicious_probe_targets.sql`
  - `deploy/sql/objects/views/v_suspicious_probe_targets.sql`

### 4. 测试

| 文件 | 内容 |
|---|---|
| `modelcatalog/upsert_test.go` | 新增 `TestUpsertSQL_AdminProtectedGuard`（SQL 结构契约：ON CONFLICT 含守卫且为末尾子句） |
| `db/db_probe_admin_protected_test.go` | 新增，读 db.go 源断言 4 个 probe 函数体各含守卫 + 全块恰 4 处守卫 |

## 放行的管理员显式端点（不改）

管理员显式操作仍可修改/删除 admin_protected 记录（符合"只能手工删除"）：

- credential_monitor offline/online
- routing 664/789/940/1138/1208
- pricing.go
- diagnostics_routing / diagnostics_credential
- provider_credential:444
- routing_health_checks fixSQL（只落库不自动执行）
- `clearProviderModels`（admin/provider_models.go:91，手工删除路径）

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./modelcatalog/... ./admin/... ./bg/... ./db/...` | exit 0 |
| 新增测试 | `go test ./modelcatalog/... ./db/... -run 'AdminProtected\|ProbeMarkFunctions'` | 3/3 PASS |
| 回归 | `go test ./modelcatalog/... ./db/... ./admin/... ./bg/... ./credentialhealth/... ./discovery/...` | 全绿 |
| 视图一致性 | 4 副本提取 CREATE VIEW 文本 | 均含守卫，语义一致 |

## 遗留与风险

- `sql/objects/` 与 schema 副本存在既有空白缩进差异（语义一致，本轮未触碰）。
- 视图无 ensure 重放机制，部署依赖 startup 迁移 468 重放覆盖线上视图；
  必须在迁移应用后才能让 `v_suspicious_probe_targets` 生效。
- 尚未部署到 154 / 生产实测（纯后端逻辑，无 UI 改动，未走 browser-use）。

## 下一步建议

1. 部署到 154（pre-prod）验证 startup 迁移 468 应用 + 视图重放 + L1-L4。
2. 部署后跑一次自动刷新/探针，确认手工记录 available/updated_at 不被改动。
3. 进入 245→154 晋级门禁后合入生产。
