# 2026-08-24 — Model-group node cards sort by manual_priority

## TL;DR

Queue perspective「按模型分组的可用节点」卡片顺序改为与 resolve 一致：按 `manual_priority` 正序（及 priority 标志），不再在 fallback 路径按 `credential_id` 排序。

## Verification

```bash
cd web && pnpm exec vitest run src/utils/queueNodeCards.test.ts src/components/QueuePerspectivePanel.test.ts
```
