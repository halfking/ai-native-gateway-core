# 数据权限审计 P2（前端适配 + E2E 验证）— 合并版交接文档

> ★ 本文件是 6 份过程报告的**合并版**（2026-11-27 一周审计轮去重合并）。
> 原 6 份碎片（BLOCKED-p2-completion-requires-owner-action / 2026-11-26-p2-terminal-state-report /
> -p2-final-status-report / -p2-completion-report / -p2-frontend-complete-report /
> -p2-deployment-verification-report）记录的是**同一个任务的 6 个渐进快照**，
> 信息高度重叠 ⇒ 合并为一份版本化文档（git 历史保留原碎片，可回溯）。
> 最终态证据 = docs/testing/2026-11-26-e2e-execution-report.md（8/8 用例）。

## 版本时间线（v1→v7）

| 版本 | 原碎片 | 时点 | 状态 |
|------|--------|------|------|
| v1 | BLOCKED-p2-completion-requires-owner-action | 103min | ⚠️ BLOCKED：缺 E2E 凭据 + 前端 UI 未实现 |
| v2 | p2-terminal-state-report | 105min | Phase 1 完成：后端可用 / 前端基建就绪 |
| v3 | p2-final-status-report | 92min | 目标 54%：明确 owner 决策项 |
| v4 | p2-completion-report | — | 前端适配 + 测试文档完成 |
| v5 | p2-frontend-complete-report | — | MySessionsView + MyUsageView 落地（8d53965c51） |
| v6 | p2-deployment-verification-report | 部署日 | 211 部署验证（障碍与回滚路径） |
| v7 | （见 docs/testing/2026-11-26-e2e-execution-report.md） | 最终 | **8/8 用例全跑，3 个 bug 当场修** |

## 最终态（以 v7 为准）

- **部署**：211 双槽 @ 8783，`v2.6.2-f43735ee`（build 3303）
- **后端**：Routing-v2 用户级过滤 + 凭据脱敏（`balance_usd` 对 tenant_admin 脱敏，
  f43735eead）＋ `my_endpoints` SQL 对齐生产真列名（cb90618477）
- **前端**：MySessionsView / MyUsageView ＋ 路由/导航/i18n×8（a7b5c50f0d，
  element-plus 显式 import 8d53965c51）
- **E2E**：8/8 用例（super_admin / tenant_admin / user 三层 × 读写面），
  权限越界用例全部按预期拒绝；EXPLAIN ANALYZE 验证无全表扫
- **已知遗留**：`admin/my_endpoints.go` 教学示例文件（permission_fix_examples.go）
  零路由接线（2026-11-27 审计轮已在 partguard 白名单登记并写明理由）

## 历史遗留决策项（沿 v3 登记，现状见「遗留风险」）

1. ~~E2E 测试凭据~~ → **已解除**（v7 用临时 key 完成，用后吊销）
2. ~~前端 UI 实现~~ → **已完成**（v5）
3. 245/154 环境部署 → 仍待 owner（生产追平前置 = 投递面闭合，见 AGENTS.md L133 ⑥）
