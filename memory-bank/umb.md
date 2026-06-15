# UMB — llm-gateway-go MaaS 积分计费平台

> **最后更新**：2026-06-16
> **模式**：实施 / P0 收尾
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

## 📋 P1 待做（不在本次范围）

| ID | 任务 |
|----|------|
| P1-1 | `MaaSModelsView.vue` — 客户向模型清单（积分/1M） |
| P1-2 | `MaaSPricingView.vue` — 三档月包 + 三档加油包 |
| P1-3 | `MaaSUsageView.vue` — 积分消耗图表 |
| P1-4 | TenantDetail 增 订阅/加油/账本 tabs |
| P1-5 | 导航/路由按租户裁剪 |

## 🔗 参考
- 方案：`docs/2026-06-16-maas-platform-plan.md`
- 部署：`llmgo.kxpms.cn` / 184 k3s
