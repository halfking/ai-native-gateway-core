# UMB — llm-gateway-go MaaS 积分计费平台

> **最后更新**：2026-06-16
> **模式**：P0+P1 已部署 184 / **镜像 gitsha-399f63c0 seq-202**
> **任务**：对外 MaaS 平台 — 积分体系 + 套餐 + 加油包 + 租户隔离

## 🎯 当前任务

将 llm-gateway-go 从中转运维台升级为可对外销售的 MaaS 平台：
- 非 default 租户：积分计价、包月套餐、加油包、模型清单（积分/1M tokens）
- default 租户（super_admin）：成本 USD + 租户收入/积分双视图
- 可配置换算率（1分=10积分、1M tokens=10000积分 等为默认值）

## ✅ P0 已完成（2026-06-16）

| ID | 任务 | 状态 |
|----|------|------|
| P0-1 | SQL 迁移 `007_maas_billing.sql` + `db/maas_schema.go` EnsureMaasSchema | ✅ |
| P0-2 | `maas/` 计费引擎：CalcCredits + PreCheck + ChargeRequest + 单元测试 | ✅ |
| P0-3 | relay 集成：PreCheckCredits 402 + emitTelemetry ChargeRequest + credits_charged 落库 | ✅ |
| P0-4 | Admin API：`admin/maas_handlers.go` settings/plans/wallet/ledger | ✅ |
| P0-5 | 数据面贯通：telemetry insert/update credits_charged、admin/logs 查询、web 请求日志展示 | ✅ |
| P0-6 | `cmd/gateway/main.go` 注入 `maas.NewService` + `SetMaas` | ✅ |

### P0 关键文件
- `db/migrations/007_maas_billing.sql`
- `maas/credits.go`, `maas/service.go`
- `relay/handler.go` — PreCheckCredits / ChargeRequest / classifyFailureStage(insufficient_credits)
- `telemetry/client.go` — RequestLogEntry.CreditsCharged
- `admin/maas_handlers.go`, `admin/logs.go`
- `web/src/api.ts`, `web/src/views/RequestLogsView.vue`

## ✅ P1 已完成（2026-06-16）

| ID | 任务 | 状态 |
|----|------|------|
| P1-1 | `MaaSModelsView.vue` — 客户向模型清单（积分/1M） | ✅ |
| P1-2 | `MaaSPricingView.vue` — 三档月包 + 三档加油包 + 钱包余额 | ✅ |
| P1-3 | `MaaSUsageView.vue` — consume 汇总 + 流水表 | ✅ |
| P1-4 | TenantDetail 增 钱包/账本 tabs + adjust 表单 | ✅ |
| P1-5 | 导航/路由：/maas/models、/maas/pricing、/maas/usage 全租户可见 | ✅ |

### P1 关键文件
- `web/src/api.ts` — MaaS 类型与 API 函数
- `web/src/views/maas/MaaSModelsView.vue`
- `web/src/views/maas/MaaSPricingView.vue`
- `web/src/views/maas/MaaSUsageView.vue`
- `web/src/router.ts` — 三路由
- `web/src/App.vue` — 侧栏 MaaS 三页
- `web/src/views/TenantDetailView.vue` — super_admin 钱包/账本

## ✅ 184 部署（2026-06-16 00:39 UTC+8）

| 项 | 值 |
|----|-----|
| 子模块 SHA | `399f63c0` |
| 镜像 | `kx-llm-gateway-go:gitsha-399f63c0` / seq-202 |
| 主仓 bump | `28ed2e95`（子模块指针） |
| 部署 tag | `deploy/prod-184-20260616-003929-5c8edaac0706` |
| healthz | `https://llmgo.kxpms.cn/healthz` → 200 |
| MaaS API | `/api/maas/{settings,models,plans,wallet,ledger}` 全 200 |
| 前端 bundle | `/maas/models|pricing|usage` 路由 +「积分」文案已打入 dist |

### 验收备注
- 登录页副标题已显示「开轩 MaaS 控制面」
- 未登录访问 `/maas/models` 正确重定向 `/login`（SPA 路由生效）
- 三页积分 UI（模型单价/钱包余额/消耗流水）需 JWT 登录后目视确认

## 🔜 P2 待办
- 套餐购买 / 加油包下单流程（支付对接）
- 非 default 租户隔离端到端验收
- Dockerfile FROM 迁移 daocloud mirror（pre-deploy Step 4 当前 FAIL）

## 🔗 参考
- 方案：`docs/2026-06-16-maas-platform-plan.md`
- 部署：`llmgo.kxpms.cn` / 184 k3s
