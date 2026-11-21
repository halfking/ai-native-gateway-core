# Migration 996: tenant_budgets enforcement_mode（租户月度预算硬拦截开关）

## 目的

「优化完善方案 2026-11-21（青岛联通交流会输入）」W1-Q1 —— 938 的
tenant_budgets 只有「预警 + 通知」，欠费默认放行（A-15）；本迁移给同一张表加
`enforcement_mode ∈ {warn, hard}` 执行开关（W1-Q1 轮，2026-11-21）：

- `warn`（默认）= 仅告警不拦截 ⇒ 存量行为零变化；
- `hard` = 当月花费（credits × cents_per_credit，与 maas_monthly_bills.amount_cents
  同源 `sumPeriodConsumption`）达 `monthly_budget_cents` ⇒ 网关请求面
  **429 `tenant_budget_exceeded`**（default 租户与内部回环 key 豁免；
  `monthly_budget_cents = 0` 即未设预算不拦截）。

执行面消费方：`maas/tenant_budget_gate.go`（`CheckMonthlyBudget`）+ 
`domains/streaming/handler.go`（MaaS credits pre-check 之后的门分支）。

## 影响范围

- **新列**: `tenant_budgets.enforcement_mode varchar NOT NULL DEFAULT 'warn'`
- **新约束**: `tenant_budgets_enforcement_mode_check`（闭集 warn/hard）
- **台账**: 迁移自携带 `schema_migrations` 自登记（≥952 双账本纪律）

## 依赖

- 前置：938（tenant_budgets 表本体）。RLS 载体不变（应用层 tenant_id
  参数为单一来源，与 938 的 Get/Upsert 同口径）。

## 为什么是新迁移而不是改 938 字节

938 已 applied+verified（2026-11-03 deploy 245 build_seq 2929）⇒ 改字节走
content replay 通道并连锁 break（914/928 教训，同 929/930 的处置）。

## 风险评估

- **风险等级**: 低（纯加列 + 具名 CHECK；PG11+ 元数据级变更，表极小）
- **数据安全**: 默认 warn ⇒ 所有存量租户行为零变化；hard 为逐租户显式 opt-in
- **幂等**: ADD COLUMN IF NOT EXISTS + DO 块具名约束 ⇒ 内容指纹重放零冲突

## 验证步骤

```sql
SELECT column_name, column_default FROM information_schema.columns
 WHERE table_schema='public' AND table_name='tenant_budgets'
   AND column_name='enforcement_mode';
SELECT conname FROM pg_constraint
 WHERE conrelid='public.tenant_budgets'::regclass
   AND conname='tenant_budgets_enforcement_mode_check';
SELECT version FROM schema_migrations WHERE version='1000';
```

## 回滚

见同目录 down.sql（只退本迁移引入的列与约束 + 台账行；表本体与 938
其余列保留 —— 迁移链每一步只回退自己引入的那部分）。

## 登记面

canonical（sql/migrations/startup/）与 installer embeddata、dbinit runner、
tsv、docker 通道序列清单与产物探针、db-changelog 台账等八面见
`internal/sqlguard/migration_1000_delivery_test.go`；本目录为离线升级通道
（installer/internal/upgrader/offline_apply.go）投递副本——与 993/994/995
同族。
