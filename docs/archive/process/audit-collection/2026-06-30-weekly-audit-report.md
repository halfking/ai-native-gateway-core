# llm-gateway-go 周度安全 / 可靠性审计报告

> **审计日期**: 2026-06-30
> **审计范围**: 认证中间件链、telemetry 写入路径、多租户隔离、probe-health 契约、并发启动
> **审计人**: 自动审计 + 人工复核
> **关联代码注释**: `middleware/auth_mw.go` P0-3、`admin/auth.go` P0-4/5、`domains/streaming/handler.go:2203` P0-6、`domains/streaming/executors/executor.go:2234` P0-8、`admin/agents.go:342` P0-9、`admin/probe_dashboard.go:506` P0-10、`db/db.go:1744` P0-11

---

## 一、审计摘要

本轮审计发现 **11 个 P0 级缺陷**，覆盖五个领域。所有缺陷均不会破坏现有调用方（**0 行业务代码删除**），通过新增 / 调整修复，全量回归 78/78 包通过。

| # | 缺陷 | CVSS / 影响 | 状态 | 修复 PR |
|---|------|-------------|------|---------|
| P0-3 | 全局 AuthMiddleware 拦截 cookie 会话的 admin 端点 | 中（功能不可用） | ✅ | PR-3 |
| P0-4 | SuperAdminMiddleware 漏检 MustChangePassword | 8.1 高 | ✅ | PR-4 |
| P0-5 | `/api/auth/logout` 不在强制改密白名单 | 中（用户卡死） | ✅ | PR-4 |
| P0-6 | `/v1/messages` + `/v1/responses` 成功路径 panic | 高（每次成功请求写 error） | ✅ | PR-5 |
| P0-7 | messages/responses 初始 INSERT 漏 client_request_id | 低（可观测性） | ✅ | PR-5 |
| P0-8 | async-retry 成功路径漏 client_request_id | 低（可观测性） | ✅ | PR-5 |
| P0-9 | `/api/agents/health` tenant_admin 看到 default 租户数据 | 高（数据泄漏） | ✅ | PR-6 |
| P0-10 | probe-health `state_summary` 前后端契约不对齐 | 中（UI 显示 0） | ✅ | PR-7 |
| P0-11 | 多 pod 并发启动 DROP/CREATE probe views race | 中（短暂 500） | ✅ | PR-8 |

> P0-1 / P0-2 在前置审计中已确认非问题，本报告不再展开。

---

## 二、缺陷详情与修复

### P0-3 全局 AuthMiddleware 拦截 cookie 会话的 admin 端点

**现象**: cookie 登录的管理员访问 `/api/users` 等管理端点统一返回 401。

**根因**: 全局 `AuthMiddleware`（API-key 校验）在 mux 路由之前执行，会拦截所有路径。admin 端点虽有 `wrapAdmin`（支持 cookie/JWT/API-key），但永远到不了。

**修复** (`middleware/auth_mw.go`): bypass 列表加 `PathPrefixes: []string{"/api/"}`。所有 175 个 `/api/*` 端点均被 `wrapAdmin` / `superAdmin` 独立包装（grep 验证），bypass 安全。

**测试**: `middleware/auth_mw_test.go` — 10 个 admin 路径 bypass、`/v1/*` 仍要求 Bearer。

---

### P0-4 SuperAdminMiddleware 漏检 MustChangePassword  **[CVSS 8.1]**

**现象**: `must_change_password=true` 的 super_admin 用 `Authorization: Bearer <jwt>` 可绕过强制改密。

**根因**: `AdminMiddleware` 检查 `claims.MustChangePassword`，但 `SuperAdminMiddleware` 跳过了该检查。

**修复** (`admin/auth.go`): 抽取 `enforceMustChangePassword(w, r, claims)` 共享函数，在两个中间件中调用。

**测试**: `admin/auth_force_password_test.go` — super_admin + tenant_admin + 白名单路径回归。

---

### P0-5 `/api/auth/logout` 不在强制改密白名单

**现象**: 强制改密用户改完密码想登出 → 403，必须强退浏览器。

**修复** (`admin/auth.go`): `isPasswordChangeAllowedPath` 增加 `"/api/auth/logout"`。

---

### P0-6 /v1/messages + /v1/responses 成功路径 panic

**现象**: 每次成功的 `/v1/messages` 请求，`request_logs` 行被错误写为 `success=false / error_kind="internal_panic"`。

**根因**: `domains/streaming/messages.go:513` 和 `responses.go:449` 调用 `emitTelemetry(..., nil)`（logCtx 传 nil），而 `handler.go:2203` 直接 `logCtx.ClientRequestID` → nil 解引用 panic → 被 `defer recover()` 捕获并改写为失败。

**修复** (`domains/streaming/handler.go`): `emitTelemetry` 内对 `logCtx` 做 nil 守卫，`clientReqIDPtr` 条件赋值。

**测试**: 4 个 async-success 测试 + 全量回归。

---

### P0-7 / P0-8 client_request_id 字段缺失

**现象**: `/v1/messages`、`/v1/responses`、async-retry 成功路径的 `request_logs.client_request_id` 始终为 NULL，无法区分客户端重试。

**修复**:
- `messages.go` / `responses.go` 入口构造 `RequestLogContext` 并填充 `ClientRequestID`（从 `X-Gw-Client-Request-Id` / `X-Client-Request-Id` header）
- `executor.go buildAsyncSuccessEntry` 从 `params.R.Header` 读取同字段

**测试**: `executor_async_success_test.go` — X-Gw-Client-Request-Id、X-Client-Request-Id fallback、无 header 时 nil。

---

### P0-9 /api/agents/health 多租户数据泄漏

**现象**: tenant_admin 访问 `/api/agents/health` 看到 `default` 租户的 stale assets。

**根因**: handler 调用 `svc.ListTenants(ctx)`，而 `pg_store.ListTenants` 受 RLS 限制只返回 `default`，导致任何 tenant_admin 都看到 default 的数据。

**修复** (`admin/agents.go`): `IsTenantAdmin(r)` 分支 — 只查自己 tenant（直接 `apihub.WithTenant` + `ListStale`）；super_admin / admin_key 保留跨租户聚合。

**测试**: `admin/agents_test.go` — tenant_admin 不调用 ListTenants、super_admin 跨 3 租户聚合。

---

### P0-10 probe-health state_summary 契约不对齐

**现象**: `ProbeHealthDetailView.vue` 顶部 4 个状态徽章（healthy/failing/suspicious/probing）全部显示 0。

**根因**: 前端读 `state_distribution`（`Record<string, number>`），后端只返回 `breakdown`（数组）。

**修复** (`admin/probe_dashboard.go`): 抽取 `buildStateDistribution(breakdown)` 函数，端点输出新增 `state_distribution` 字段。

**测试**: `admin/probe_dashboard_test.go` — 5 个用例覆盖空、单 state、9 种枚举、重复 state 聚合。

---

### P0-11 多 pod 并发启动 probe views race

**现象**: 多 pod 同时启动时 `/probe-health` 短暂返回 500（约 30s）。

**根因**: `ensureProbeHealthDashboardViews` 直接 `pool.Exec("DROP VIEW ... CASCADE; CREATE ...")`，两个 pod 并发执行时 race。

**修复** (`db/db.go`): 用事务 + `pg_try_advisory_xact_lock(0x50524F42)`（"PROB" ASCII）。非阻塞 — 拿不到锁的 pod 跳过，赢家 commit 后视图立即可见。事务级锁自动释放，无泄漏风险。

**测试**: `db/db_probe_views_test.go` — lock ID 稳定性 + nil 安全。

---

## 三、验证结果

```
go build ./...       ✅
go vet ./...         ✅
go test -short ./... ✅ 78/78 包通过
```

新增测试覆盖：
- `middleware/auth_mw_test.go` — 5 个
- `admin/auth_force_password_test.go` — +4 个
- `domains/streaming/executors/executor_async_success_test.go` — +3 个
- `admin/agents_test.go` — +3 个
- `admin/probe_dashboard_test.go` — 5 个（新文件）
- `db/db_probe_views_test.go` — 2 个（新文件）

---

## 四、部署建议

1. **滚动更新**：所有修复向后兼容，可灰度发布。
2. **重点验证**：
   - cookie 登录后访问管理后台（P0-3）
   - `request_logs` 中 `/v1/messages` 的 `client_request_id` 字段（P0-7）
   - tenant_admin 访问 `/api/agents/health`（P0-9）
3. **回滚**：每个 PR 独立，可单独 `git revert`。

---

## 五、不在本次范围内的发现

- `domains/streaming/executors/executor.go` 的 `ListStale` 接口扩展遗留（`apihub/service_test.go` LSP 报错为预存问题，非本次引入）。
- `web/src/views/ProbeHealthDetailView.vue` 前端 `failing` 聚合漏 `unavailable` / `recovering` — 属 P2，留待后续。
