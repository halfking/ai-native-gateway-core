# D11 — auto 模型

> 域知识库：[docs/audit/playbook/domains/D11-auto-model.md](../../../docs/audit/playbook/domains/D11-auto-model.md)  
> 48h 改动面（截至 R70）：D11 auto-model · classification / over-provision / decision-chain 三层闭环  
> 状态：✅ **R70 已关闭**（本文件为收口摘要，权威报告见下方 latest.md）

## R70 收口摘要

| 维度 | 状态 |
|---|---|
| 改动面 | D11 auto-model · classification layer accuracy/F1/GRRQ + over-provision 0/30 + decision chain 14×11×4 |
| 任务定位 | 关闭 D11 域 R70 轮 auto 路由专项测试 + 三门验收 |
| 验收门 | `go build ./...` · `go vet ./autoroute/...` · `go test -race -timeout 120s ./autoroute/...` · 全部 PASS |
| 回归五件套 | autoroute 单测五组（classification matrix / e2e / fallback / recommend_v2 / channel-quality）— 全部 PASS（业务/数据/压力/安全/一致性专项 .go 测试文件当前 0 个，见 latest.md §7 后续路径） |
| 决策链 | 14 个 (prompt×profile) × 11 任务类型 × 4 profile 档位 — 端到端可复现 |
| 过配率 | 0/30（分类层/路由层/计费层全部命中目标档位，无掉档/越级） |
| 提交 | 本地 2 commits ahead of `origin/main`（`59107132e` + `ae344d51e`）；HEAD `1c9c753c4` |

**权威收口报告**：[reports/latest.md](reports/latest.md)（148 行，覆盖 §1 改动面 / §2 任务定位 / §3 验收门 / §4 回归五件套 / §5 决策链 / §6 过配率 / §7 收口结论）

## 后续路径

- **推送代码到 `origin/main`**：触发条件为用户确认 R70 关账后希望远端同步；工作流为启动 `session-audit-gate`（会话日志 + 双轴审查 + 规则自检 → GO 后 `git push`）。已完成（R70 收口 merge `5e6707e7c` 已推 origin/main）。
- **补充业务/数据/压力/安全测试文件**：留待后续 sprint，不阻塞 R70 关账。
