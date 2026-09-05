# 2026-08-19 — Archive DEPLOYMENT_PLAN.md + 154/245 部署验证 + ZCode Stash 保护

> 承接 `handoff-20260819-audit-completion-and-zcode-coordination.md`（上一会话 audit 完成 push `9ca6fca60` 后接力），本会话按 §3 优先级清单执行：154/245 部署验证 + DEPLOYMENT_PLAN.md 归档 + ZCode stash 完整性保护 + 测试门禁 + 新 handoff 接力。
> Rule refs: rule 04 (preserve ZCode) + rule 09 (FACT) + rule 11 (诚实汇报) + rule 17 (测试门禁) + rule 31 (三层架构) + rule 36 (归档协议) + rule 39 (脱敏) + rule 43 (UTF-8 + 最小补丁) + rule 47 (envs SSOT)

## 1. 做了什么

1. **接手验证**：HEAD 接力时记录 `9ca6fca60`，实际为 `de4b57063`（合并 + 2 个新 commit），又 fetch 后见 `c60504baa`（已 push 的归档 commit）。working tree 始终保持 clean。3 ZCode stash 完整保留。
2. **154 / 245 部署状态验证**（env-injector inject aliyun-gateway-154 + SSH 直连 154/245）：
   - 154 (生产) `git_sha=b3036166 / build_seq=1619` — 落后 HEAD 4 commits (`c776eaa25` / `017f44b37` / `dfb7dc7e2` / `aa5a8a90d` / `8e395456d` / `d3cb79bdf`)
   - 245 (pre-prod) `git_sha=50bf5ba0 / build_seq=1621` — 落后 HEAD 2 commits (`dfb7dc7e2` / `aa5a8a90d` / `8e395456d` / `d3cb79bdf`)
   - **245 build 到 50bf5ba0 解释了 `stash@{0}` 存在的原因**：ZCode 手工 bump version.json → 1621 但未 commit 到 main（git log 无对应 release bump commit）。
3. **修正 handoff §3.1 关于 `stash@{2}` 的 Factuality 错误**：handoff §3.1 断言"已被 HEAD 吸收，可 drop"。本会话 grep `admin/routing.go` 4448-4450 验证：HEAD 中 `markResolveRuntimeUnknown` 函数内仍是空行 + 闭合 `}` 的原版，**stash 内容未吸收**。按 rule 04 红线未 drop，在 commit message + 新 handoff 中明确标注此错误。
4. **DEPLOYMENT_PLAN.md 归档**（handoff §3.3 owner 决策）：
   - `git mv deploy/sql/DEPLOYMENT_PLAN.md → docs/archive/2026-07/specs/deployment-plan-v1-184-pg-citus.md`
   - 归档文件顶部加 YAML frontmatter（`archived_from` / `archived_at` / `archived_reason` / `status: archived`）+ DEPRECATED banner 显指向 154/245 当前拓扑 + 新部署参考链接
   - CHANGELOG.md 加新 `[Unreleased]` 段记录归档动作
   - 从上一会话 "保留的 184 引用" 豁免清单移除 `deploy/sql/DEPLOYMENT_PLAN.md`（已归档）
5. **ZCode WIP 测试文件状态**：grep `git log --all -- 'bg/probe_recovery_authority_test.go' 'executor_dispatch_probe_pin_test.go'` → 全部已在 `c776eaa25 fix(probe): unblock pinned URSM recovery` commit 中。**无 untracked 残留**，handoff §3.7 担忧已解除。
6. **测试门禁**：go vet ./... PASS / go build ./... PASS / go test -short ./admin ./bg ./domains/ursm/v2/... 17 packages 全 PASS (~90s) / pre-commit-check.sh 4 PASS / 0 FAIL / 2 SKIP（web 文件未变）。
7. **Push**：rebase origin/main（含 2 个 ZCode 新 commit `d3cb79bdf` + `8e395456d`）后成功 push `c60504baa`。

## 2. 改动清单（1 commit pushed）

| commit | 说明 |
|---|---|
| `c60504baa` | docs(archive): move deploy/sql/DEPLOYMENT_PLAN.md → docs/archive/2026-07/ — git mv (93% similarity) + CHANGELOG.md `[Unreleased]` 段 + 从 184 豁免清单移除 + commit message 完整记录 ZCode stash 状态 + 154/245 部署对比 + handoff §3.1 错误标注 |

继承 commit（rebase 后接续，无 squash）：
- `8e395456d` test(audit): prove migration 538 legacy upgrade (ZCode 接力期间)
- `d3cb79bdf` fix(audit): close startup migration and tenant routing gaps (ZCode 接力期间)
- `de4b57063` Merge branch 'main' of https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go (ZCode 接力期间)
- `aa5a8a90d` fix(session): assert authentication.InvalidKeyError (cross-package)
- `dfb7dc7e2` fix(ursm): gate cleanup with durable deadline and CAS
- `9ca6fca60` docs(session): record 8/19 audit completion + ZCode coordination (上一会话)
- `500e646fe` docs(sweep-audit): complete 184 server redaction across 9 remaining active docs (上一会话)

## 3. 为什么这样做

- **rule 04 §1 红线**：3 ZCode stash 完整保留，未 apply / pop / drop 任何一个。即使发现 `stash@{2}` 未被 HEAD 吸收（handoff §3.1 误判），仍按红线不擅自 drop — 标入新 handoff 由 ZCode 决策。
- **rule 09 §2.1 Factuality**：handoff §3.1 断言"可 drop"是事实错误（stash 内容 HEAD 未吸收），本会话用 `grep` 命令实际验证后在 commit message + 新 handoff 中明确标注，不掩盖 handoff 错误（对齐 rule 11 §5 诚实汇报）。
- **rule 31 §2.3 三层架构**：154 是当前 PROD、245 是 pre-prod、184 已下线。DEPLOYMENT_PLAN.md 整篇关于 184 + PG/Citus，与现状脱节，rule 36 归档协议落地。
- **rule 36 §归档协议**：归档动作含 git mv + frontmatter（YAML 结构化）+ DEPRECATED banner + CHANGELOG 段 + 从豁免清单移除 — 完整 5 件套。
- **rule 43 §UTF-8 + 最小补丁**：归档 banner 用 targeted edit（仅 26 行新增），未触碰原 676 行文档内文（保留 184 / 172.31.0.3 / 172.31.0.4 历史引用，标注为失效）。
- **rule 47 envs SSOT**：本会话无新增 KEY，沿用 env-injector inject aliyun-gateway-154（plain 模式，~57 个 KEY 已 export）。无明文入仓 / 无明文 git diff。

## 4. 验证结果

- **pre-commit-check.sh**: 4 PASS / 0 FAIL / 0 WARN / 2 SKIP
- **go vet ./...**: PASS
- **go build ./...**: PASS
- **go test -short ./admin ./bg ./domains/ursm/v2/...** (17 packages): ALL PASS (~90s)
  - admin 7.4s / bg 5.7s / domains/ursm/v2 + 15 子包 总 ~80s
- **curl http://154:8781/api/system/version**: `{"build_seq":1619,"git_sha":"b3036166",...}` ✓
- **curl http://245:8781/api/system/version**: `{"build_seq":1621,"git_sha":"50bf5ba0",...}` ✓
- **ssh 154 'systemctl is-active llm-gateway-go'**: `active`
- **ssh 245 'systemctl is-active llm-gateway-go'**: `active`
- **grep `git log --all -- 'bg/probe_recovery_authority_test.go' 'executor_dispatch_probe_pin_test.go'`**: 均在 `c776eaa25` 中 ✓
- **HEAD == origin/main == `c60504baa`**, 0 ahead/behind ✓
- **ZCode stash 完整性**: 3/3 保留 ✓ (`stash@{0}` / `stash@{1}` / `stash@{2}`)

## 5. 遗留与风险

1. **ZCode stash 处置未决**（rule 04 红线下不可擅动）：
   - `stash@{0}` version.json bump 1619→1621 + menu-config.json export timestamp — 245 已 build 到 1621，所以这是 ZCode 手工 bump 后的产物；建议下个 session 在与 ZCode 确认后 apply + commit（chore(release): bump version v1621）让 main branch 与 245 服务状态对齐
   - `stash@{1}` 6 文件 URSM 改动（175 行 diff，84 行新增）— ZCode WIP，rule 04 红线下等 ZCode 决策
   - `stash@{2}` admin/routing.go format-only（空行挪动）— handoff §3.1 误判"已吸收"，实际 HEAD 未吸收；建议下个 session 在与 ZCode 确认后 `git stash pop` + commit "chore(format): admin/routing.go gofmt-style 空行调整"
2. **154 生产落后 HEAD 6 commits**（rule 03 §6 部署前置）：
   - 落后 commit: `c776eaa25` (fix probe) / `017f44b37` (feat dashboard) / `dfb7dc7e2` (fix ursm) / `aa5a8a90d` (fix session) / `d3cb79bdf` (fix audit) / `8e395456d` (test audit)
   - 当前 154 build_seq=1619 / git_sha=b3036166
   - 建议下个 session 与 ZCode 确认 deploy-245 后再 deploy-154
3. **245 pre-prod 落后 HEAD 4 commits**（同上）：
   - 落后 commit: `dfb7dc7e2` / `aa5a8a90d` / `d3cb79bdf` / `8e395456d`
   - 当前 245 build_seq=1621 / git_sha=50bf5ba0
   - 建议优先验证 245 集成 smoke（applyURSMOverlay + credential_recovery.go URSM 改动），通过后再 deploy-154
4. **ZCode author 异常**（handoff §3.2 移交）：
   - `f54ae6de8` commit author=`zcode <zcode@local>` 但 message 是上一会话 AI 维护
   - ZCode 团队决定是否需要 force-push 改 author 或接受现状
5. **doc-sweep + doc-sweep-audit 两条相邻 [Unreleased] 段**（handoff §3.6）：
   - 当前 CHANGELOG.md 有 3 个相邻的 [Unreleased] 段（8/19 audit + 8/19 archive + 8/19 sweep + 8/19 sweep-audit）
   - 按 Keep a Changelog 规范，建议下次 release 前手动合并为单条

## 6. 下一步建议

- **新会话第一动作**：
  ```bash
  git fetch origin main
  git status  # 确认 ZCode 没又留下新 WIP
  git stash list  # 确认 3 stash 仍在
  env-injector inject aliyun-gateway-154  # 准备凭据
  ```
- **owner 决策点**（按优先级）：
  1. **ZCode 决定 stash 处置**：apply / pop / drop 哪一个或全部？建议下个 session 在 ZCode 同事在场时逐个 `git stash show -p stash@{N}` 共同决策
  2. **245 deploy 流程**：priority queue = `stash@{1}` (URSM 大改动) → 245 集成 smoke → 通过后 deploy-154
  3. **是否需要为 `git_sha=50bf5ba0` 补 release bump commit**（让 main branch 与 245 服务状态对齐）— ZCode 决策
- **handoff 文档**：见 `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/handoff-20260819-archive-deployment-plan-and-zcode-stash-handoff.md`（按 `handoff` skill 输出）。
- **必选 skills**（新会话启动后立即加载）：
  - session-init（接力本 handoff）
  - env-injector（任何 deploy / connect 操作前）
  - deploy-245 / deploy-154（按 owner 决策触发）
  - code-review（任何新 commit 前 review）
  - session-audit-gate（仅当 .acc-session-policy 存在时强制；当前无）

## 7. Rule 引用清单

| Rule | 用途 |
|---|---|
| rule 04 §1 | preserve ZCode WIP（3 stash 不擅自动）|
| rule 09 §2.1 | Factuality（handoff §3.1 stash@{2} 误判的修正）|
| rule 11 §6 | 诚实汇报（不掩盖 handoff 错误）|
| rule 17 | 测试门禁（go vet + build + 17 packages test + pre-commit 4/4 PASS）|
| rule 31 §2.3 | 三层架构（154 / 245 / 184 状态确认）|
| rule 36 | 归档协议（DEPLOYMENT_PLAN.md 5 件套）|
| rule 39 §脱敏铁律 1 | 184 引用合规处理（归档保留 + 标注失效）|
| rule 43 §UTF-8 + 最小补丁 | 归档动作仅 26 行新增，原文 676 行不动 |
| rule 47 | envs SSOT 占位符（无新增 KEY，沿用 inject）|
| rule 50 | 审计门禁（仓库无 .acc-session-policy，skill 不强制）|

## 8. 关键文件路径

| 路径 | 用途 |
|---|---|
| `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go` | 项目根 |
| `docs/changelogs/2026-08-19-doc-sweep-audit-completion.md` | 上一会话 audit 报告 |
| `docs/session-logs/2026/08/2026-08-19-audit-and-zcode-preserve.md` | 上一会话 session log |
| `docs/archive/2026-07/specs/deployment-plan-v1-184-pg-citus.md` | 本会话归档的 DEPLOYMENT_PLAN.md |
| `/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/handoff-20260819-archive-deployment-plan-and-zcode-stash-handoff.md` | 新 handoff（接力下个 session）|

最后更新: 2026-08-19 02:15 +0800
本会话 commit: `c60504baa`（已 push）
工作树状态: clean
ZCode stash: 3/3 完整保留