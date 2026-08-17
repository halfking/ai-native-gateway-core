---
archived_from: .handoff/2026-07-25-realtime-routing-self-heal.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# Handoff: 2026-07-25 Realtime Routing Self-Heal

**交接时间:** 2026-07-25  
**交接原因:** Plan A 后端任务全部完成，准备交接 Plan B 前端任务  
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`  
**当前分支:** `2026-07-25-realtime-routing-self-heal`  
**最新 commit:** `dcd56f66` (已推送到 remote)

---

## §1 任务背景

**SPEC:** `docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md`

用户发现三个独立问题：
1. 实时流泳道重复（前端 Plan B）
2. 筛选弹窗漂移（前端 Plan B）
3. **node_probe_failed 粘滞问题**（后端 Plan A，已完成）

### Plan A: node-probe 实时恢复（✅ 已完成）

**Plan 文档:** `docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`

**问题:** `v_routable_credential_models` 通过 migration 417 把 `nps.last_direct_ok = FALSE AND nps.next_retry_at > now()` 判为 `node_probe_failed`，导致一次失败的主动探测会让 binding 在下一次直接轮询成功前都不可路由（最长 24h）。

**修复方案:**
- `bg/node_probe.go runOne` 成功分支补齐 `last_direct_ok=TRUE / last_err_code=NULL`
- 成功后调用 `w.invalidateCandidateCache(credID)` + `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')`
- `v_routable` 在 AutoRouteRealtimeListener 5s 防抖内刷新

**已完成的 6 个任务:**

| 任务 | Commit | 文件 | 测试 |
|------|--------|------|------|
| Task 1: runOne success 清理 last_direct_ok + last_err_code | `ac335d07` | `bg/node_probe_test.go` | `TestRunOneSuccessClearsLastDirectOkAndErrCode` |
| Task 2: InvalidateCandidateCache + pg_notify | `bbed3fa8` | `bg/node_probe.go`, `bg/node_probe_test.go` | `TestRunOneSuccessInvokesInvalidateAndNotify` |
| Task 3: Integration test 5s 窗口 | `e3978af5` | `bg/node_probe_recovery_test.go` | `TestRunOneSuccessReflectsToViewWithinDebounceWindow` |
| Task 4: handleNodeProbeStateReset no-probe | `d19a7fed` | `bg/node_probe_test.go` | `TestHandleNodeProbeStateResetIsNoProbe` |
| Task 5: CHANGELOG + docs | `41e489ad` | `CHANGELOG.md`, `docs/changelogs/2026-07-25-node-probe-realtime-recovery.md` | - |
| A.6: TriggerManual → MarkNodeProbeHealthy | `e439565d` | `bg/model_probe.go`, `bg/node_probe_test.go` | `TestTriggerManualSuccessCallsMarkNodeProbeHealthy` |
| SPEC + Plans 文档 | `dcd56f66` | `docs/superpowers/specs/`, `docs/superpowers/plans/` | - |

**测试验证:**
```bash
go test -count=1 ./bg/ -run 'TestRunOne|TestNodeProbe|TestIsMissing|TestHandleNodeProbeStateReset|TestTriggerManual'
```
✅ 所有测试通过，无回归

**代码统计:**
- 6 个文件修改
- +230 行 / -9 行
- 7 个 commits（全部带测试）

---

## §2 当前状态

### Git 状态
- **Branch:** `2026-07-25-realtime-routing-self-heal`
- **Latest commit:** `dcd56f66` - docs: add SPEC and Plans for 2026-07-25 realtime routing self-heal
- **Remote status:** ✅ 已推送到 `origin/2026-07-25-realtime-routing-self-heal`

### PR 状态
❌ **PR 未创建** — 需要在下一步创建 PR 并等待评审

### Plan A 完成度
✅ **100% 完成**（6/6 任务）

所有 AC 满足：
- AC1: node_probe_failed 识别 ✅
- AC2: 5s 内恢复 ✅
- AC3: 失败路径不变 ✅
- AC4: 紧急按钮安全 ✅
- AC5: CHANGELOG 完整 ✅

---

## §3 待办事项（按优先级）

### 🔴 P0: 创建并合并 Plan A 的 PR

1. **创建 PR:**
   ```bash
   cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
   gh pr create --title "fix(node-probe): realtime routing self-heal for node_probe_failed" \
     --body "Implements SPEC docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md §3

## 修复摘要
- runOne success 分支清理 last_direct_ok / last_err_code
- 成功后 invalidate URSM candCache + pg_notify('auto_route_refresh')
- v_routable 在 5s 内自动恢复已修复的 binding
- TriggerManual 成功后调用 MarkNodeProbeHealthy

## 测试
- 新增 5 个单元测试 (source-grep + integration)
- 回归测试: go test ./bg/ -run 'TestRunOne|TestNodeProbe' ✅

## 文档
- docs/changelogs/2026-07-25-node-probe-realtime-recovery.md
- CHANGELOG.md 更新

## Commits
- ac335d07 fix(node-probe): clear last_direct_ok + last_err_code on success
- bbed3fa8 fix(node-probe): invalidate URSM candCache + pg_notify
- e3978af5 test(node-probe): add recovery integration test
- d19a7fed test(admin): pin handleNodeProbeStateReset no-probe contract
- 41e489ad docs: add changelog for node_probe realtime recovery
- e439565d fix(model-probe): TriggerManual success calls MarkNodeProbeHealthy
- dcd56f66 docs: add SPEC and Plans

## 验收标准
全部 AC 满足 (SPEC §3.3)" \
     --base main
   ```

2. **等待评审并合并**

3. **生产验证**（合并后）:
   - 监控 `auto_route_refresh` pg_notify 计数（Grafana dashboard）
   - 人工触发 5xx → 200 恢复流程，确认 ≤5s 内 `is_routable` 翻转

### 🟡 P1: Plan B 前端任务（独立分支）

**Plan 文档:** `docs/superpowers/plans/2026-07-25-live-stream-frontend-fixes.md`

**5 个任务:**
1. **Task 1:** `standardModelName` 集中为 trim pass-through 助手
2. **Task 2:** `LiveStreamFilterDialog` 快照 options + 打开冻结
3. **Task 3:** `liveStreamViewSettings` 持久化与恢复
4. **Task 4:** cypress live-stream e2e + shim
5. **Task 5:** CHANGELOG + i18n 文案

**独立分支:** 从 `main` 创建新分支 `2026-07-25-live-stream-frontend-fixes`（与 Plan A 无依赖）

---

## §4 关键决策记录

1. **Worktree 隔离:** 使用 `.worktrees/2026-07-25-realtime-routing-self-heal` 避免主工作区污染
2. **TDD 严格遵守:** 每个任务都先写失败测试（RED），再实现（GREEN），最后提交
3. **增量任务 A.6:** 原 Plan 标记为 read-only，实际发现需要在 `TriggerManual` 加 `MarkNodeProbeHealthy` 调用，已补充实现
4. **Source-grep 测试策略:** Task 1/2/3/4 均用 source-grep 而非真实 DB，快速验证合约不被破坏
5. **pg_notify 格式:** 复用 `credentials:UPDATE:<id>` 格式（与 admin endpoints 一致）

---

## §5 陷阱与注意事项

1. **Worktree 路径:** 所有操作必须在 `.worktrees/2026-07-25-realtime-routing-self-heal` 内，推送需回到主仓
2. **测试 tab 匹配:** source-grep 测试的 snippet 必须精确匹配源码的 tab 缩进（4 个 tab vs 3 个 tab）
3. **函数体提取:** `TestHandleNodeProbeStateResetIsNoProbe` 需要提取特定函数体，避免误报其他函数的调用
4. **reports/ 目录被忽略:** `.gitignore` 包含 `reports/`，completion report 无法提交到 git
5. **Plan B 独立性:** 前端 Plan B 与后端 Plan A 零依赖，可并行或稍后处理

---

## §6 关键文件路径

**SPEC & Plans:**
- `docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md`
- `docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md` (Plan A)
- `docs/superpowers/plans/2026-07-25-live-stream-frontend-fixes.md` (Plan B)

**修改的代码文件:**
- `bg/node_probe.go` (runOne success 分支)
- `bg/model_probe.go` (TriggerManual success 分支)
- `bg/node_probe_test.go` (4 个新测试)
- `bg/node_probe_recovery_test.go` (1 个新测试)
- `CHANGELOG.md`
- `docs/changelogs/2026-07-25-node-probe-realtime-recovery.md`

**测试命令:**
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/.worktrees/2026-07-25-realtime-routing-self-heal
go test -count=1 ./bg/ -run 'TestRunOne|TestNodeProbe'
```

---

## §7 环境信息

- **项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
- **Worktree:** `.worktrees/2026-07-25-realtime-routing-self-heal`
- **操作系统:** macOS (darwin 25.5.0 arm64)
- **Go 版本:** (项目默认)
- **Git user:** halfking

---

## §8 Next Agent 提示

下一个 agent 应该：
1. **先创建 Plan A 的 PR** 并等待合并（P0）
2. **创建新 worktree** 从 `main` 拉 `2026-07-25-live-stream-frontend-fixes` 分支
3. **实施 Plan B 任务 1-5**（前端修复）
4. **合并后验证** Plan A 的 AC2（生产环境 5s 恢复）

Plan B 可选择在 Plan A 合并前并行开发（两者无依赖），但 PR 应该顺序合并（Plan A 优先，因为是 P0 bug 修复）。
