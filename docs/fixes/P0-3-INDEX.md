# P0-3 计费事务协调器 · 文档索引（2026-11-26 审计轮建）

**为什么有这个索引**：P0-3 产出 8 份文档，其中 4 份「状态类」互相重叠且各钉不同提交号（`48e22e2cd` / `35fe6f36e` / `2552f8890`）。2026-11-26 周审计 r2 判定：不删原文（保留历史与视角），但**读的人必须先看这里**，避免把过期状态当成现状。

## 权威顺序（读哪些、以哪份为准）

| 顺序 | 文档 | 性质 | 权威性 |
|---|---|---|---|
| ① | [p0-3-status.md](./2026-11-26-p0-3-status.md) | **当前状态（20:25，提交 `48e22e2cd`，在 main）** | ★ **唯一现状权威** |
| ② | [p0-3-handoff.md](./2026-11-26-p0-3-handoff.md) | 交接（`35fe6f36e`，在 main） | 交接语境下读 |
| ③ | [p0-3-solution-evaluation.md](./2026-11-26-p0-3-solution-evaluation.md) | 方案评估（方案C 为什么） | 决策依据 |
| ④ | [p0-3-migration-verification.md](./2026-11-26-p0-3-migration-verification.md) | 迁移验证 | 部署前读 |
| ⑤ | [p0-3-deployment-checklist.md](./2026-11-26-p0-3-deployment-checklist.md) | 部署清单 | 部署时读 |
| ⑥ | [p0-3-e2e-test-guide.md](./2026-11-26-p0-3-e2e-test-guide.md) | E2E 指南 | 验收时读 |

## 历史快照（已被 ① 取代，勿当现状）

| 文档 | 过期原因 |
|---|---|
| [p0-3-completion-status.md](./2026-11-26-p0-3-completion-status.md) | 钉 `35fe6f36e`，早于 ① |
| [p0-3-work-completion-report.md](./2026-11-26-p0-3-work-completion-report.md) | 「阶段1-2完成」的时点快照 |
| [p0-3-implementation-summary.md](./2026-11-26-p0-3-implementation-summary.md) | **钉 `2552f8890` —— 该提交不在 main**（断链，审计轮已登记） |

## 审计轮断链警告

`p0-3-implementation-summary.md` 引用的提交 `2552f8890` **不存在于 main**（`git merge-base --is-ancestor` 判否）。该文档描述的「实施内容」与 main 现状存在断链 ⇒ 以代码与 ① 为准。

---
*索引建立：2026-11-26 weekly-audit-r2；判定命令见 `docs/audit/2026-11-26-weekly-audit-r2-goals.md`*
