# Changelog — 审计修正轮:release pass 惰性 plan 下限边界

- 日期:2026-09-13
- 前置:66a9f8e6a(清下限自动回池)+ 上游并行线 probe-recovery closeout(f8322dc04/e352c670d/4ea8ab0af)

## 审计范围

同步上游 probe-recovery closeout 并行线后,复审 balance_floor 所有权不变量与新恢复代码的交互:

1. `bg/credential_recovery.go` availSQL 新增 suspended 证据恢复分支(R1):要求 `quota_state='ok'` 且尾部守卫排除 `balance_exhausted` → 不会误碰 floor 摘除行。✓
2. `bg/balance_quota_probe.go` suspended 复验集 + auto-disabled 复验集:顶层 WHERE 保留 `state_reason_code <> 'balance_floor'` 显式豁免。✓
3. `bg/node_probe_write_through.go` 每请求成功路径豁免健在。✓ 所有权契约测试全绿。

## 修复:部分清除卡死边界(非套餐厂商惰性 plan 下限)

**现象**:非套餐厂商(如 deepseek)的凭据被货币下限摘出后,操作员清掉 `balance_floor_usd` 但残留 `quota_floor_percent`/`quota_floor_tokens`(非套餐厂商无探测数据源,这两列从不参与摘出/恢复):

- release pass 要求三列全 NULL → 不触发;
- 货币 pass C 要求 `balance_floor_usd IS NOT NULL` → 不选;
- 套餐路径显式跳过非 zhipu/minimax 厂商;
- BalanceQuotaProbe 豁免 balance_floor 行。

→ 凭据永久停在 `balance_exhausted/balance_floor`。

**修复**:release WHERE 的 floor 条件改为 `balance_floor_usd IS NULL AND ((两个 plan floor 均 NULL) OR 非套餐厂商)`。zhipu/minimax 且 plan floor 仍配置的行不变(仍归套餐滞回路径所有)。

## 测试命令与结果

```
go build ./...                                   # OK
go vet ./bg/                                     # clean
go test ./bg/ -run 'TestBalanceFloorGuard|TestEvaluatePlanFloor' -count=1  # ok
go test ./bg/ ./domains/credential/ -count=1     # ok 3.95s / 14.79s
```

`TestBalanceFloorGuardClearedFloorRelease` 契约扩展:钉住 `OR NOT EXISTS (... IN ('zhipu','minimax'))` 惰性边界。
