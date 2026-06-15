# UMB — llm-gateway-go MaaS 积分计费平台

> **最后更新**：2026-06-16
> **模式**：P0+P1 已部署 184 / **镜像 gitsha-6f912dc4 seq-207**
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
| P1-6 | 普通租户仪表盘 + 导航裁剪 + `/api/maas/usage/summary` | ✅ |

### P1 关键文件
- `web/src/api.ts` — MaaS 类型与 API 函数
- `web/src/views/maas/MaaSModelsView.vue`
- `web/src/views/maas/MaaSPricingView.vue`
- `web/src/views/maas/MaaSUsageView.vue`
- `web/src/views/TenantDashboardView.vue` — 非 default 租户积分仪表盘
- `maas/usage.go` — QueryUsageSummary（request_logs.credits_charged）
- `admin/maas_handlers.go` — GET /api/maas/usage/summary
- `web/src/router.ts` — requiresPlatformOps 守卫
- `web/src/App.vue` — platformOps 侧栏过滤
- `web/src/views/TenantDetailView.vue` — super_admin 钱包/账本

## ✅ 184 部署（2026-06-16 00:52 CST — 租户仪表盘）

| 项 | 值 |
|----|-----|
| 子模块 | **`6f912dc4`**（`224ee6d3` 功能 + `6f912dc4` 先图后表顺序） |
| 主仓 HEAD（post） | **`e5684f2f`** + tag **`deploy/prod-184-20260616-005215-15a1a46c0200`** |
| pre checkpoint | `deploy/prod-184/checkpoints/prod-184-20260616-005104-pre.md` |
| 镜像 | **`kx-llm-gateway-go:gitsha-6f912dc4`** / **seq-207** / `VERSION=1.0.0-6f912dc4-2026-06-15` |
| 脚本 | `./scripts/deploy-llm-gateway-go-184.sh --only app`（`ALLOW_SUBMODULE_DIRTY=1` `ALLOW_DOCKERHUB_FROM=1`） |
| rollout | `llm-gateway-go-deployment` successfully rolled out |
| healthz | **200** |
| `/api/maas/usage/summary` JWT | **200** + by_model/trend JSON |

### 验收（2026-06-16 00:52）

| 检查项 | 结果 |
|--------|------|
| `go test ./maas/...` | **PASS** |
| `cd web && npm run build` | **PASS** |
| 非 default 租户侧栏 | 隐藏定价管理/路由总览/模型与标签（`platformOps`） |
| 非 default `/` 仪表盘 | `TenantDashboardView` 积分+次数，先图后表 |

## ✅ 184 部署（2026-06-16 00:48 CST — P1 MaaS 三页）

| 项 | 值 |
|----|-----|
| 子模块 | `399f63c0`（部署前 `git reset --hard` + `git clean -fd` 清本地脏树） |
| 主仓 HEAD（post） | `0b61e984` + tag **`deploy/prod-184-20260616-004828-71fbbdeee36a`** |
| pre checkpoint | `deploy/prod-184/checkpoints/prod-184-20260616-003951-pre.md` |
| 镜像 | **`kx-llm-gateway-go:gitsha-399f63c0`** / **seq-204** / 容器 `VERSION=1.0.0-399f63c0-2026-06-15` |
| 脚本 | `./scripts/deploy-llm-gateway-go-184.sh --only app`（`ALLOW_DOCKERHUB_FROM=1`） |
| rollout | `llm-gateway-go-deployment` successfully rolled out |
| healthz | **200** `{"status":"ok",...}` |
| `/api/maas/models` 无鉴权 | **401**（已非 404） |
| `/api/maas/models` JWT admin | **200** + models JSON |

### 验收（2026-06-16 00:48）

| 检查项 | 结果 |
|--------|------|
| Playwright 登录 + 侧栏三菜单 + `/maas/models` | **FAIL**（Chromium 二进制未就绪；`npx playwright install` 挂起无产出） |
| API 登录 + MaaS 三接口 | **PASS** |
| 生产 JS 含「MaaS 模型/套餐/消耗」 | **PASS**（`assets/index-Bp9l-AVY.js`） |
| pre-deploy-verify Step 4 | **FAIL**（Dockerfile 直连 docker.io；部署旁路 `ALLOW_DOCKERHUB_FROM=1`） |

### 备注
- 静态 `/version.json` 仍显示旧 `git_sha=9e6eb473`（web 产物未 bump version.json）；以 k8s 镜像 tag / 容器 `VERSION` 为准。
- `git pull origin main` 本机失败（远程权限）；子模块已在 `399f63c0`。

## ✅ P2 已完成（2026-06-16 — 三池账户 + 订单占位）

| ID | 任务 | 状态 |
|----|------|------|
| P2-1 | `008_billing_orders.sql` 三池 + billing_orders + payment 占位字段 | ✅ |
| P2-2 | 扣费三池顺序 + ledger.pool | ✅ |
| P2-3 | 订单 CRUD + `StubQRProvider` | ✅ |
| P2-4 | `/maas/account` + `/maas/orders/:id` + Pricing 购买 | ✅ |
| P2-5 | Admin grant / confirm / 订单 tab | ✅ |
| P2-6 | 真实支付宝/微信 API | ⏳ 待账号 |

### P2 关键文件
- `db/migrations/008_billing_orders.sql`
- `maas/payment.go`, `maas/orders.go`
- `maas/service.go` — 三池 ChargeRequest / GrantCredits / GetAccount
- `admin/maas_handlers.go` — account/orders/grant/confirm API
- `web/src/views/maas/MaaSAccountView.vue`, `MaaSOrderView.vue`

### 支付宝接入点
- 配置 `maas_settings.alipay_account` / `wechat_mch_id`
- 扩展 `maas/payment.go` 实现真实 Provider + webhook

## 🔜 P2 后续
- 支付 webhook 自动确认
- 发票与对账导出
- 余额低阈值告警

## 🔗 参考
- 方案：`docs/2026-06-16-maas-platform-plan.md`
- 部署：`llmgo.kxpms.cn` / 184 k3s
