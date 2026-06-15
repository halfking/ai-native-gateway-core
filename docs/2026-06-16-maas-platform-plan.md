# llm-gateway-go MaaS 平台实施方案

> 版本：v1.1
> 日期：2026-06-16
> 状态：**P0 ✅ + P1 ✅ 代码完成；184 部署待执行**

---

## 1. 现状审计摘要

### 1.1 代码事实（非猜测）

- **租户体系已落地**：`tenants` 表、`users.tenant_id`、`api_keys.tenant_id`；`default` 为平台租户。
- **角色**：`super_admin`（整站）vs `tenant_admin`（本租户）；非 default 的 tenant_admin 进入 `isReadOnlyMode()`（定价页只读）。
- **费用现状**：`request_logs.cost_usd` / `cost_display` / `cost_currency`；租户统计 API 返回 `total_cost_usd`（美元）。
- **预算门禁**：`api_keys.budget_usd` + `CheckBudget` → HTTP 402 `budget_exhausted`。
- **定价管理页**（`/pricing`）：管理上游 `model_offers` 的 unit_price，面向运维，不是客户套餐。
- **模型页**（`/models`）：内部 canonical/alias/tag 管理，含发现、编辑；不是客户「支持的模型清单」。
- **积分字段**：当前子模块 `grep credit|积分|points` **零匹配**。用户所述 request_logs 积分字段 **尚未实现**，需新建 `credits_charged`（或 `points_used`）列。

### 1.2 部署

- 生产：`https://llmgo.kxpms.cn`（184 k3s，`k8s/apps/llm-gateway-go.yaml`）
- 技术栈：Go 后端 + Vue3 前端（`web/`）

---

## 2. 目标架构

### 2.1 概念模型

```
平台设置 (maas_settings)
  ├─ cents_per_credit      默认 0.1（1分=10积分）
  ├─ base_credits_per_1m   默认 10000（基准模型 1M tokens）
  └─ currency_display      CNY

套餐 (subscription_plans)          加油包 (topup_packages)
  ├─ tier: basic|pro|max             ├─ tier: small|medium|large
  ├─ monthly_credits_quota           ├─ price_cents / credits_amount
  ├─ price_cents                     └─ 一次性到账
  └─ 周期重置 quota

租户订阅 (tenant_subscriptions)    租户余额 (tenant_credit_wallets)
  ├─ plan_id, period_start/end       ├─ balance_credits
  ├─ quota_remaining                 ├─ monthly_quota_remaining
  └─ status                          └─ locked_credits（预扣）

模型费率 (model_credit_rates)      账本 (credit_ledger)
  ├─ canonical_id                    ├─ type: consume|topup|subscribe|adjust|refund
  ├─ credits_per_1m_in/out           ├─ amount, balance_after
  └─ 覆盖全局基准                    └─ ref: request_id / order_id
```

### 2.2 计费公式（可配置）

```
credits = ceil(
  (prompt_tokens * rate_in + completion_tokens * rate_out) / 1_000_000
)
其中 rate_in/out 来自 model_credit_rates，缺省用 base_credits_per_1m
```

扣减顺序建议：**月包 quota → 钱包余额**；不足则 402。

### 2.3 权限矩阵

| 能力 | super_admin (default) | tenant_admin (非 default) |
|------|----------------------|---------------------------|
| 上游成本/提供商 | ✅ | ❌ |
| 租户列表+金额(USD) | ✅ | ❌ |
| 综合设置/套餐定价 | ✅ | ❌ |
| 模型清单（积分/1M） | ✅ 含成本列 | ✅ 仅积分列 |
| 订阅与价格 | ✅ 管理 | ✅ 购买/查看自己的 |
| 消耗统计 | ✅ 全站+分租户 | ✅ 本租户积分 |
| 请求日志 | ✅ 全站 | ✅ 本租户，显示积分非 USD |
| 手工调账 | ✅ | ❌ |

### 2.4 API 分层

```
/api/admin/maas/settings          GET/PUT  super_admin
/api/admin/maas/plans             CRUD     super_admin
/api/admin/maas/topup-packages    CRUD     super_admin
/api/admin/maas/model-rates       CRUD     super_admin
/api/admin/maas/tenants/:code/wallet        super_admin
/api/admin/maas/tenants/:code/ledger        super_admin
/api/admin/maas/tenants/:code/subscribe     super_admin 代开

/api/maas/models                  GET      租户：公开模型+积分费率
/api/maas/plans                   GET      租户：可购套餐
/api/maas/topup-packages          GET      租户：加油包
/api/maas/wallet                  GET      租户：余额+月包余量
/api/maas/usage/summary         GET      租户：积分消耗汇总（by_model + trend）
/api/maas/ledger                  GET      租户：账本流水
/api/maas/subscribe               POST     租户：订阅（Phase1 记待支付）
/api/maas/topup                   POST     租户：购加油包（Phase1 记待支付）
```

数据面：relay 完成时写 `request_logs.credits_charged` + `credit_ledger` 扣减。

### 2.5 页面与路由

| 路由 | 页面 | 受众 |
|------|------|------|
| `/maas/models` | 模型清单 | 全部登录用户 |
| `/maas/pricing` | 订阅与价格 | 全部；非 default 可购 |
| `/maas/usage` | 消耗统计 | 全部；积分展示 |
| `/maas/account` | 我的账户（余额/流水） | tenant_admin |
| `/tenants/:id` 增 tab | 订阅/加油/账本 | super_admin |
| `/maas/settings` | 综合设置 | super_admin |

导航：非 default 租户隐藏 提供商/路由全景/租户管理等；显示 MaaS 三页。

### 2.6 租户仪表盘（非 default 租户）

普通租户登录后 `/` 仪表盘**不展示** token/USD 运维指标，改为 MaaS 积分视图：

| 区块 | 数据来源 | 交互 |
|------|----------|------|
| 总积分消耗 / 请求次数 / 可用余额 | `GET /api/maas/usage/summary` + `/api/maas/wallet` | 时间窗 1/7/30 天 |
| 模型请求排行 | `by_model[].requests` | 点击柱子 → 请求明细表 |
| 各模型用量 | `by_model[]` requests + credits | 点击行 → 明细表 |
| 使用趋势 | `trend[]` 双图（积分 + 次数） | 点击日期 → 当日明细 |

**权限裁剪**（`isPlatformOpsView` = super_admin ∧ default 租户）：

- 侧栏隐藏：`/pricing`、`/routing-overview`、`/models`、`/examples` 及已有 `super: true` 项
- 路由守卫：`requiresPlatformOps` 路由重定向 `/`
- default 租户 super_admin 保持原整站仪表盘（token/USD/提供商统计）

---

## 3. 默认套餐草案（可配置，对标 bigmodel 三档）

| 档位 | 月费 | 月积分额度 | 约合 tokens（基准率） |
|------|------|-----------|----------------------|
| 基础 Basic | ¥29 | 100,000 | ~10M |
| 高级 Pro | ¥99 | 500,000 | ~50M |
| 最大 Max | ¥299 | 2,000,000 | ~200M |

加油包：

| 档位 | 价格 | 积分 |
|------|------|------|
| 小 | ¥10 | 10,000 |
| 中 | ¥50 | 55,000（+10%） |
| 大 | ¥100 | 120,000（+20%） |

> 以上为种子数据，入库 `subscription_plans` / `topup_packages`，后台可改。

---

## 4. 任务拆解与优先级

### P0 — 数据层 + 扣费闭环 ✅（commit `39e7f389`）

| ID | 任务 | 验收 | 状态 |
|----|------|------|------|
| P0-1 | SQL 迁移：settings/plans/topup/wallet/ledger/model_rates + request_logs.credits_charged | 迁移可回滚；startup ensure 幂等 | ✅ |
| P0-2 | `maas/` 计费引擎：CalcCredits + Deduct + 402 门禁 | 单元测试 ≥5 场景 | ✅ |
| P0-3 | relay 集成：成功请求写 credits_charged + ledger | request_logs 新字段有值 | ✅ |
| P0-4 | Admin API：settings/plans CRUD | curl 200 | ✅ |
| P0-5 | 租户 API：wallet + models(积分率) | 非 default JWT 403 不能访问 admin 成本 | ✅ |

### P1 — 前端三页 + 租户详情 ✅（commit `0549641b`）

| ID | 任务 | 验收 | 状态 |
|----|------|------|------|
| P1-1 | `MaaSModelsView.vue` | Playwright 截图；列含积分/1M | ✅ 代码；生产待 deploy |
| P1-2 | `MaaSPricingView.vue` | 三档月包+三档加油包展示 | ✅ 代码；生产待 deploy |
| P1-3 | `MaaSUsageView.vue` | 图表用积分；无 USD | ✅ 代码；生产待 deploy |
| P1-4 | TenantDetail 增 订阅/加油/账本 tabs | super_admin 可点进记录 | ✅ |
| P1-5 | 导航/路由按租户裁剪 | tenant_admin 看不到提供商 | ✅ |

### P2 — 运营与支付（后续）

| ID | 任务 |
|----|------|
| P2-1 | 支付网关对接（微信/支付宝） |
| P2-2 | 发票与订单表 |
| P2-3 | 模型差异化费率批量导入 |
| P2-4 | 告警：余额<阈值邮件/webhook |

### 可并行

- P0-1 SQL ∥ P0-4 API 骨架（mock repo）
- P1-1/2/3 前端 mock API 并行，后端就绪后联调

---

## 5. 审计标准（每项任务完成门）

1. **代码**：`go test ./pkg/maas/...` PASS；`go vet` clean
2. **权限**：非 default tenant_admin 调 `/api/admin/providers` → 403
3. **数据**：造一笔 chat 请求 → request_logs.credits_charged > 0；ledger 有 consume 行
4. **UI**：Playwright 登录 tenant 测试账号 → 三页截图与 Figma/方案对比
5. **default 对比**：super_admin 同页可见 USD 成本列（仅 default）
6. **部署**：184 滚动后 `https://llmgo.kxpms.cn/healthz` 200
7. **提交**：子模块 commit → 主仓 bump；不提交 agents/

---

## 6. 实施顺序

```
git pull → pre-deploy-verify → checkpoint pre
→ P0-1 SQL → P0-2 引擎 → P0-3 relay → P0-4/5 API
→ 本地 docker/pg 集成测 → P1 前端
→ browser 验收截图 → checkpoint post → deploy 184
```

---

## 7. 风险

| 风险 | 缓解 |
|------|------|
| 用户以为积分字段已存在 | 明确新建；迁移文档 |
| 与 budget_usd 双轨 | Phase1 保留 budget_usd；非 default 优先积分 |
| 子模块 dirty | 先 commit llm-gateway-go 再 bump |
| 范围膨胀 | 不做支付、不做发票在 P0/P1 |

---

## 8. 技能沉淀（待创建）

- `.github/skills/deploy-llm-gateway-go-184/SKILL.md` 增 MaaS 验收步骤
- `services/llm-gateway-go/.cursor/skills/maas-billing/SKILL.md` 计费规则 SSOT
