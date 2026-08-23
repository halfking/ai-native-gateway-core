# 2026-08-24 — 凭据并发/槽位编辑统一：合并两个修改按钮为一个

> 凭据详情「设置与维护」段的 `NodeDetailConcurrencyPanel` 原先有两个独立按钮：
> - **调整自动并发**（→ `setConcurrencyAuto`，写 `concurrency_limit_auto`）
> - **修改槽位上限**（→ `updateCredential`，写 `fp_slot_limit`）
> 而**手动并发**（`concurrency_limit`）只能在凭据抽屉里另改，三值无法同步更新。

## 1. 手动并发 vs 自动并发

| 字段 | 含义 | 设置方 | 优先级 |
|---|---|---|---|
| `concurrency_limit`（**手动并发**）| 运维手动设的硬上限 | 人工 | 最高 |
| `concurrency_limit_auto`（**自动并发**）| 监控/探测自动发现的值（可被手动覆盖）| 系统 / 「调整自动并发」| 手动未设时生效 |
| `effective_concurrency`（**生效并发**）| `COALESCE(手动, 自动, 5)` | 计算所得 | 路由实际用的值 |

指纹槽位 `fp_slot_limit` 独立列，约束 `fp_slot ≤ 并发`（`credentials_fp_slot_vs_concurrency`）。

## 2. 改动

`web/src/components/NodeDetailConcurrencyPanel.vue`：

- 删除「调整自动并发」「修改槽位上限」两个按钮及各自弹窗。
- 新增单个 **「调整并发与槽位」** 按钮，弹出统一弹窗，同时编辑三个字段：
  - 手动并发（留空 = 使用自动并发）
  - 自动并发（≥ 1）
  - 指纹槽位上限（≥ 0）
- 一个「确认」按钮：
  - 手动并发 + 指纹槽位变 → `updateCredential({concurrency_limit, fp_slot_limit})`
  - 自动并发变 → `setConcurrencyAuto(id, value, reason)`
- 校验：原因必填；自动并发 ≥ 1；指纹槽位 ≥ 0；指纹槽位不超过手动并发。

## 3. 验证

- `vue-tsc --noEmit` 0 错误
- `vite build` 通过
- 新增 `NodeDetailConcurrencyPanel.test.ts`（8 用例）：合并按钮、弹窗 seed、分别/组合调用两个 API、原因/约束校验
- `NodeDetailDrawer.test.ts` 14 用例保持通过
- 部署 245 (seq 1700) / 154 (seq 1701)：产物含「调整并发与槽位」、不含旧按钮文案
