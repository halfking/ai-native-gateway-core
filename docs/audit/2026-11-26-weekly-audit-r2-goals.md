# 2026-11-26 第二轮周审计：审核目标与批判修正

**审计会话**: weekly-audit-r2（22:32 接手，与前一轮周报会话并行）
**审计窗口**: 2026-11-20 ~ 2026-11-26
**与前一份周报的关系**: `docs/audit/2026-11-26-weekly-audit-summary.md`（22:00 会话产出）。本文不重复其内容，只记录**增量事实、其结论的复核结果、以及本轮批判修正**。

---

## 一、前一份周报结论的复核（批判审计）

| 前周报声称 | 复核结果 | 判定 |
|---|---|---|
| 拉取 104 提交、编译成功 | 本轮 FF 合并 **110** 笔（期间上游继续推进），`go build ./...` ✓、`go test ./admin/` ok **164.551s** | ✓ 成立（数字随时间漂移属正常） |
| `codex/211-statistics-v2-release` 测试修复已 cherry-pick 到 main | `git cherry`：`40586d42cf` 在 main 有等价 patch（`-`），**`abc372a371`（installer 离线升级 scope 修复）不在 main（`+`）** | ⚠️ 部分成立：该分支仍有 1 笔真实修复未进 main |
| `s5-gateway-pricing` 未合并（待评估） | **本轮复核推翻**：内容已通过 merge `8b9ff1540b` 进 main，main 有逐字等价 patch `40af488cc6` ⇒ 分支无用，已删 | ✗ 已过时，已处置 |
| `users-page-role-verify` 未合并（待评估） | 本轮审计：1 笔独有提交（maestro flow21 CDP 预置会话，444 行测试资产，bash/py 语法验证通过，`mint-jwt-token.py` 已在 main 逐字存在）⇒ **有价值，已合并进 main（`4c03813758`）并推双远端后删除分支** | ✗ 已过时，已处置 |
| `feature/routing-v2-user-filter` 已合并 | `git rev-list --count origin/main..b` = 0 且 cherry 全空 ⇒ 已删 | ✓ 成立，已删 |

**批判结论**：前周报的「待评估」两项在 22:00~22:42 之间已有确定答案，本轮完成闭环。**教训：周报里的「待评估」条目若不附判定命令，下一轮还得重推一遍 —— 本文每条判定都附命令。**

---

## 二、本周提交总结（2026-11-20 起，no-merges = 981 笔）

类型分布：docs 433 / fix 327 / feat 115 / test 61 / chore 15 / refactor 6 / perf 3。

五大工作方向（与代码面交叉验证）：
1. **计费 P0 批次**（billing retry / bill payment / balance precheck；迁移 1000-1002）
2. **角色治理**（非 default 租户禁 super_admin、变更角色入口、maestro 真机三层验证）
3. **V2 统计面**（request metrics / dashboard board / session analysis / reconciliation V2）
4. **附件安全**（租户隔离 + 配额基础设施）
5. **schema 投递治理**（known_undelivered 登记 + 三本台账 + 路由快照重生成）

---

## 三、本周审核目标（更新版）

### G1 schema 投递缺口全数「可判定化」
- 492（channels/customers/channel_users）与 1000-1002（billing）不在投递通道 —— **门全绿（`go test ./internal/schemadelivery/` ok 0.527s、`./internal/sqlguard/ -run 'TestMigration055|Test996'` ok 0.022s）**，缺口以 known_undelivered 登记形态存在（42 个条目）。
- 判据：登记条目与磁盘真值一致（本轮回访 ✓）；owner 决策前**不再新增**同类缺口。
- 遗留：492 与 1000-1002 的投递属生产 schema 决策，维持 owner 待裁决。

### G2 共享工作树与分支卫生
- 1 小时不活跃且无独有价值 ⇒ 删；有独有价值 ⇒ 合并后删（本轮 3 删 1 并 2 留，见 §一）。
- worktree：近 2 小时活跃 ⇒ 不动只登记。本轮终态：
  - `gw-rm-fix`（22:46 活跃）：**含一条 main 上没有的真缺陷修**（`admin/dashboardapi/session_projected.go` turns 对拍腿无 SAVEPOINT ⇒ PG aborted 事务使 soft-fail 名存实亡 ⇒ 整面板 500）。归并行车道，**本轮不代提**。
  - `.codex/worktrees/211-v2-reaudit`（22:38 活跃）：reconciliation V2 工具 3 文件 staged，归并行车道。

### G3 文档收敛（本周最大重复源）
- P0-3 计费事务协调器 **8 份文档**（status / completion-status / work-completion-report / implementation-summary / handoff / migration-verification / deployment-checklist / solution-evaluation），四份「状态类」互相重叠且各钉不同提交号（48e22e2cd / 35fe6f36e / 2552f8890——最后一个不在 main）。⇒ **建索引 README 明确唯一权威版**，不删原文（保留历史）。
- 多模态 5 份 handoff 属审计链（executive → extended → protocol → verification → followup），是流水不是重复 ⇒ 不合并，索引注明阅读顺序。

### G4 部署与生产面
- 现役 211 @8783 build_seq 3261 `8092699bd`（来自裁决清单，本轮未做部署）。
- 前端 i18n 219 新键缺 6 语种翻译：属并行会话引入，部署闸门（go test）不受影响 ⇒ 维持 owner 裁决清单 ③.1。

---

## 四、本轮遗留风险（诚实边界）

1. **`abc372a371`（installer 离线升级 scope 修复）仍在活跃分支**上未进 main —— 1 小时规则内不动；若该分支后续丢弃，此修复丢失。
2. **SAVEPOINT 修复**（gw-rm-fix 未提交）是 main 缺失的真缺陷修 —— 已登记 §G2，等活跃会话自行提交。
3. P0-3 文档引用的提交 `2552f8890` 不在 main —— 说明该文档描述的「实施」与 main 现状存在断链，读文档者须以代码为准。
4. request-metrics-v2（74 笔）与 211-statistics-v2-release（2 笔）为活跃功能分支，是否合并属 owner 决策。

## 五、子项目逐一审计（§G2 补遗，23:1x 执行）

首轮循环只覆盖了 4 个仓，漏了 `salesdecorate`；client / cursor 只跑了 worktree list 未核 status 与陈旧分支。本轮补齐全量 5 仓（同一父目录下全部 GIT_REPO）：

| 仓库 | worktree | 未提交变更 | 分支审计 | 处置 |
|---|---|---|---|---|
| **salesdecorate** | 仅自身（无额外） | **0**（干净） | 3 个陈旧本地分支 `audit/docs-round84/85/86`（最晚 11-20）：全部 ahead=0 且 `git cherry main` 为空 ⇒ 内容已在 main | **删 3 分支** |
| **llm-gateway-client** | 仅自身 | **0**（干净） | 3 个陈旧 `audit/docs-round83/85/86`（最晚 11-19）：全部 ahead=0 且 cherry 空 | **删 3 分支** |
| **llm-gateway-go-cursor** | 仅自身 | **0**（干净） | 本地 main 落后 origin/main **116 笔**、领先 0 | **FF 同步至 `9ef6dbd99a`**（与本轮推送尖端一致） |
| llm-gateway-go-3 | +2（gw-rm-fix、codex-reaudit） | gw-rm-fix 1 文件（SAVEPOINT 修复，22:46 活跃）；reaudit 3 文件 staged（22:38 活跃） | 首轮已审 | 保留（活跃窗口内，§G2） |
| llm-gateway-go-5 | 仅自身 | 0 | 首轮已审并处置 | 已收口（§一） |

**补审计判据**：`git rev-list --count main..<branch>` = 0 且 `git cherry main <branch>` 输出为空 ⇒ 内容已在 main，分支可安全删（6 个删除全部满足两条判据）。
**一轮补遗教训**：`ls` 枚举出了 5 个 GIT_REPO，审计循环只写了 4 个 ⇒ 「逐一检查」要以枚举清单为对拍表，循环结束必须核对清单覆盖率。

---

**复核命令存档**：
- 分支等价性：`git cherry main <branch>` / `git rev-list --count main..<branch>`
- patch 逐字对比：`diff <(git show A) <(git show B)`
- 投递门：`go test ./internal/schemadelivery/ ./internal/sqlguard/`
- 快照重生成：`bash scripts/audit/extract-admin-routes.sh --update-snapshot`（go-5 checkout，790 行 + 口径头）
