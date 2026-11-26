# 2026-11-27 第三轮周审计（r3）：审核目标与批判修正

**审计会话**: weekly-audit-r3（00:2x 接手，单会话纵深执行）
**审计窗口**: 2026-11-20 ~ 2026-11-27
**与前两轮的关系**: r1（`2026-11-26-weekly-audit-summary.md`）与 r2（`2026-11-26-weekly-audit-r2-goals.md`）的遗留项在本轮全部回访；本文只记录**增量处置、判定证据、以及本轮批判修正**。工作分支 = `feature/routing-v2-user-filter`（22+1 笔在途资产 + 本轮审计增量）。

---

## 一、r2 遗留项回访（逐条判定，附命令）

| r2 遗留 | 本轮处置 | 证据 |
|---|---|---|
| `abc372a371`（installer 离线升级 scope 修复）滞留活跃分支 | **已 cherry-pick 进本分支**（9df1c2465e）并验证：installer 模块 `go build ./...` ✓ / `go test ./internal/upgrader/` ok | `git show abc372a371 --stat`；patch-id 对拍 |
| SAVEPOINT 修复（gw-rm-fix 未提交） | **分支已在远端成形**（`origin/fix/rm-turns-leg-savepoint`）⇒ **3 笔全数 cherry-pick**（229251f119 + 53663d306c + a37cdd8233），`go build ./...` ✓、metricsstore 全绿 | `git branch --contains 901e753f20`；patch-id 三笔全命中 |
| P0-3 文档断链（引用 `2552f8890` 不在 main） | 维持「读文档者以代码为准」；本轮未发现新的断链 | — |
| request-metrics-v2 / 211-statistics-v2-release 活跃分支合并与否 | statistics-v2-release 唯一真实修复已收割 ⇒ 分支失去保留意义；request-metrics-v2（74 笔）仍属 owner 决策 | `git cherry` 判定 |
| 前端 i18n 219 新键缺 6 语种 | 本分支已带全 8 语种（my.ts ×8 + nav/routing 增量）；go-i18n 侧不在本分支范围 | `git diff origin/main...HEAD -- web/src/locales` |

## 二、本轮增量审计与修正（r3 新增）

### 2.1 合并 + 编译测试（目标①）
- FF 合并 origin/main 至 `e030ebe5f8`；`go build ./...` ✓、`go vet ./...` ✓。
- **抓出并修复 8 类红**（每类先在纯 origin/main 复现再修，归因明确）：
  1. 流式测试 4 处断言未跟 `69aec60492` 的 quota 文案澄清（origin/main 自身红）
  2. canonical 读点登记簿含 `admin/my_endpoints.go` 陈旧条目（本分支 SQL 修正的连带）
  3. **迁移 633 投递缺口**（P0）：附件配额表躺在无人消费的 `startup/up/` 目录 ⇒ 搬 canonical ＋ 事务包裹 ＋ 自登记 ＋ 四处投递位全登记
  4. partguard：教学示例文件 3 处父表写登记（先例 233c14a2bc 口径）
  5. modelattribution：`performance.go` 模型链补 `<unknown>` 占位符折叠
  6. dbx jsonb 静态扫：errorplaybook 3 处 `[]byte` 直传改 `string(...) + ::text::jsonb`
  7. rowsguard：billing_retry_queue 循环补 `rows.Err()` 终检
  8. routemount：`registerBillPaymentRoutes` 接线（5 条路由生产 404 → 修复）
- 豁免台账按「只许收窄」纪律删除 11 条已修复豁免。

### 2.2 分支审计（目标②）
- 4 个本地分支全合并（ahead=0）⇒ 删；`feature/users-page-role-verify` 4 文件与 main 逐字节一致（被 `4c03813758` 取代）⇒ 强删。
- 远端 2 个修复分支（rm-turns-leg-savepoint / 211-statistics-v2-release）内容全数收割 ⇒ 可删（属 owner，本轮只登记）。

### 2.3 worktree 审计（目标③，8 → 3）
- 处置 6：gw-cause-2 / gw-om3 / gw-r42（干净或内容已在 main）删；gw-turnsalign 删（分支全合并）；wt-datefix 删（HEAD 在 main）；**gw-rolegov 先收割后删**（离线契约门 10/10 绿 + Page.navigate 导航竞态修复 + login.yaml retry 加固合入，commit 17b779bfbb；**丢弃**硬编码口令脚本与违背 CDP 根因的 UI 登录回退）。
- 保留 2（<2h 活跃）：gw-datetimefix（default-time 修复轮在途）、gw-usage-total（合计行轮在途）。

### 2.4 文档收敛（目标④）
- 6 份 P2 渐进快照（BLOCKED → terminal → final → completion → frontend → deployment）合并为一份版本化交接（`2026-11-26-p2-permission-adaptation.md`，v1..v7 时间线），-1566 行。
- 其余重复面（P0-3 八份 / 多模态五份 / 12小时系列 122 份）沿用 r2 的「索引＋不删原文」口径（含 verify.sh §300 的历史 § 引用，移动即断链）。

### 2.5 子项目逐一审计（目标⑨）
| 仓库 | worktree | 未提交变更 | 处置 |
|---|---|---|---|
| llm-gateway-go（生产孪生） | 自身 + gw-base2(prunable) | **活跃**（01:07 仍有文件写入，casdoor 热切换）| **不动**；prunable 空壳 gw-base2 已 prune |
| llm-gateway-client | 仅自身 | 0 | r2 删的 3 分支未复活 ✓ |
| salesdecorate | 仅自身 | 0 | r2 删的 3 分支未复活 ✓ |
| llm-gateway-core / shell | 与父目录同 .git（枚举假象） | — | 跳过（非独立仓） |

## 三、本周审核目标（r3 终版）

| # | 目标 | 状态 |
|---|---|---|
| G1 | schema 投递缺口可判定化：**633 本轮投递收口**；492 与 1000-1002 仍登记待 owner（生产 schema 决策） | 部分完成 |
| G2 | 分支/worktree 卫生：本地 0 残留分支（除活跃 2 个 worktree 的宿主分支）；远端 2 个已收割分支待删 | 完成 |
| G3 | 文档收敛：P2 六合一完成；P0-3 索引沿用 r2 | 完成 |
| G4 | 部署与生产面：本轮零部署、零生产写；线上 211 仍 `f43735ee`（build 3303） | 维持 |
| G5 | 基线红清零：origin/main 自带 6 类红全修（流式断言/partguard/modelattribution/dbx/rowsguard/routemount）| 完成 |
| G6 | 测试资产收口：rolegov 三资产合入 + 契约门进 verify.sh | 完成 |

## 四、遗留风险（诚实边界）

1. **492 与 1000-1002 投递**：生产 schema 决策，owner 未拍板前部署库缺表（known_undelivered 登记在案，门自动 stale 检查）。
2. **2 个活跃 worktree 在途**：gw-datetimefix 的 default-time 修复与 gw-usage-total 后续推进未收口（活跃窗口内不代提）。
3. **远端 2 个已收割分支未删**：属远端操作，待 owner 确认。
4. **`feature/routing-v2-user-filter` 合回 main**：+24 审计增量待推送合并（见 §五）。
5. 245 的 IO 风暴补丁与三台追平：沿 L133 待 owner（本轮未复核机器侧）。

## 五、复核命令存档

- 红归因：`git worktree add /tmp/x origin/main --detach && go test <pkg>`（先复现再修）
- patch 收割验证：`git show <c> | git patch-id --stable` 两侧对拍
- 投递面四点检查：`grep 633 scripts/apply-db-revision-sequence.sh sql/schema/installed_startup_migrations.tsv installer/cmd/llm-gw-installer/embeddata/startup/ installer/migrations/`
- 豁免台账验证：`go test ./scripts/deploygate/`
- 契约门：`bash scripts/checks/maestro-users-role-gate.sh`（10/10）
