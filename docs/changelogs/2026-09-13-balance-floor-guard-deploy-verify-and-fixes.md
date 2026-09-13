# Changelog — balance-floor guard 部署级验证 + 两个修复

- 日期:2026-09-13
- 相关提交:1a89c32fe / a6b535dab(guard),本日两个修复见下

## 部署级验证(本地 8782,真实凭据)

按 handoff「下一轮提示词」完成部署级验证:

1. **migration 701 生效确认**:本地库 credentials 表 `balance_floor_usd/quota_floor_tokens/quota_floor_percent/plan_quota_*` 列全部就位(随 ensure 生效)。
2. **zhipu 套餐摘出/恢复闭环**(`quota_floor_percent:95`):
   - 摘出:`balance_floor guard: pulled credentials below plan floor (count=1)`,凭据转 `balance_exhausted/balance_floor`,出池。
   - 恢复:窗口重置(5h 窗翻新)后 `recovered above hysteresis band (count=1)`,1 个 sweep 内回池。
3. **货币下限 pass A/B/C 闭环**(`balance_floor_usd`,mock 平衡接口):refresh(15min 新鲜度)→ pull → 充值回滞回带 → restore,日志与 admin 展示一致。
4. **web 表单控件补齐**:CredsTab 三个下限字段(货币/token/百分比)+ 套餐额度只读展示已可用(vue-tsc + vite build 通过)。
5. minimax `/v1/token_plan/remains` 仍无订阅 key 可测,维持 fail-open。

## 修复 1:清下限不自动回池(P1)

**现象**:给凭据配 floor 摘出后,把下限清掉(admin PATCH NULL),凭据永远停在 `balance_exhausted/balance_floor`。

**根因**:三条恢复路径全部失效——货币 pass C 要求 `balance_floor_usd IS NOT NULL`;套餐路径两 floor 均 NULL 时 floorNone 直接 no-op;BalanceQuotaProbe 对 balance_floor 行按设计豁免(防 ping-pong)。

**修复**:`bg/balance_floor_guard.go` 新增 `releaseClearedFloorCredentials` pass,cycle 内套餐 sweep 之前执行:三个 floor 列全 NULL 且 `quota_state='balance_exhausted' AND state_reason_code='balance_floor'` 的行,按 pass C 同款可路由守卫(status/lifecycle/manual_disabled/provider enabled+manual_disabled)释放回 `ok/ready`。任一 floor 仍配置的行不动(仍归滞回恢复路径所有)。约一个 sweep 周期(≤5min)内回池。

**测试**:TestBalanceFloorGuardClearedFloorRelease 契约钉死;两个 pinned-count 测试更新(restore 2→3,守卫 marker 2→3)。

## 修复 2:planTypes 下拉无效值(小)

zhipu/minimax 实测套餐 kind 实际值域:`free/token_plan/code_plan/agent_plan/monthly`。下拉删掉 `request/seat/compute_time/flat_quota`(后端不认),新增 `monthly`(8 locale)。

## 测试命令与结果

```
go build ./...                          # OK
go test ./bg/ -count=1                  # ok 27.4s
go test ./admin/ -count=1               # ok 136.4s
cd web && npx vue-tsc --noEmit          # clean
cd web && npx vite build                # ✓ built in 10.50s
```
