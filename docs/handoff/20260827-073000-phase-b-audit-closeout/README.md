# Phase B（webhook / recovery / quota probe）审计收尾 — 交接文档（2026-08-27）

## 1. 任务背景

Phase B 审计覆盖四块：`cmd/gateway/webhooks`（quota_recharged）、
`bg/credential_recovery` + `bg/credential_probe_v2`（恢复与探活）、
`bg/periodic_quota_probe` / `balance_quota_probe`（配额探测）、以及
`internal/fsstore`（请求落盘 / Bleve 索引，被 webhook 与日志检索共同依赖）。

本会话的工作是**审计收尾**：确认历次审计修复已全部进入 `origin/main`、
确认工作区无未提交内容、确认遗留 stash 无丢失风险，并把状态落成文档。

## 2. 当前状态（全部已验证）

| 项目 | 状态 | 证据 |
|---|---|---|
| 工作区 | 干净，与 `origin/main` 同步 | `git status`：无未提交变更 |
| `go build ./...` | PASS | 本会话验证 |
| `go vet ./bg/` | PASS | 曾经的测试代码漂移已被后续合并修复 |
| `go test ./bg/`（全量） | PASS（2.6s） | 含 `credential_recovery_test.go` 347 行 P1-4 测试 |
| `go test ./internal/fsstore/ ./cmd/gateway/webhooks/` | PASS | |

## 3. 历史审计修复存活验证（逐项 grep HEAD）

| 修复 | 落点 | HEAD 证据 |
|---|---|---|
| P1-4 `SetProbeSubmitterImmediate`（恢复后同步探活） | `cmd/gateway/main.go`、`bg/credential_recovery.go` | `grep`：main.go 2 处、recovery 3 处；commit `31d46307b` |
| fsstore `DocIDQuery`（精确按文档 ID 查请求） | `internal/fsstore/fsstore.go:354` | HEAD 已演化为 `DocIDQuery` + `shard_date` keyword 字段方案 |
| `ListRequestsInDay` 日期格式兼容 | `internal/fsstore/fsstore.go` | HEAD 重构为 `requestDateDir()`（同时接受 `YYYY-MM-DD` / `YYYY/MM/DD`，严格校验） |
| `probeTentativeRevertAfter`（smart-fallback 试探恢复回滚窗） | `bg/node_probe.go:976`、`bg/probe_rollback.go:197` | 曾导致测试编译漂移，现已在 HEAD 且 `go vet ./bg/` 通过 |
| 探针辅助函数（`writeBindingUnavailable` 等） | `bg/credential_probe_v2.go` | HEAD 6 处标记 |
| merge 冲突损坏修复（recovery / probe_v2 / 测试文件头） | `bg/credential_recovery.go`、`bg/credential_probe_v2*.go` | 无冲突标记，测试全绿 |

## 4. stash@{0} 处置结论（本会话核心审计项）

`stash@{0}: WIP on main: 6cfab39b3`（10 文件，+583/-55）。逐文件语义级比对
（剥离注释/gofmt 后逐行匹配 HEAD）结论：

| stash 内容 | HEAD 现状 | 结论 |
|---|---|---|
| `credential_recovery.go` +15 行（`probeSubmitterImmediate`）、`credential_recovery_test.go` +256 行、`node_probe.go` +8 行、`main.go`、`executor_dispatch_test.go`、`responses_bridge.go` | 全部逐行在 HEAD | **已吸收**（commit `31d46307b`） |
| `session_overview.go`：`CostStats.TotalRequests` / `AvgLatencyMs`（migration 563） | HEAD 已有同名字段 + Scan 接线 | **已吸收** |
| `credential_probe_v2.go`：`isModelBindingProbeError()` | HEAD 用更严格的 `isPerModelChatFailure()`（显式限定 `chat status 404/410`、`messages status 404/410`，防止 /v1/models 的 404 被误判为单 binding 失败） | **已被更优实现取代** |
| `fsstore.go`：`started_at` 类型 switch（string/[]byte/time.Time） | HEAD 改为索引 `shard_date` keyword 字段（时区稳定，避免 RFC3339 反序列化歧义） | **已被更优实现取代** |
| 测试断言 `fastDelay := 30 * time.Second`（硬编码 30s） | HEAD 默认 5min + 断言放宽为「正数且 ≤5min」（P1-2 落点 C 设计） | **有意设计变更，stash 版本过时** |

**处置：stash@{0} 可安全删除**（`git stash drop stash@{0}`）。本会话未删除，
留给用户在确认本文档后自行执行——因为删除不可恢复。

## 5. 剩余任务（新会话执行清单）

按优先级排序，均已排除本会话已收尾的部分。状态更新于 2026-08-27 主代理执行。

- [x] **T1 删除过时 stash** — ✅ 完成。
  `git stash show -p stash@{0} > /tmp/stash0-backup.patch`（留档 855 行）后
  `git stash drop stash@{0}`。备份位于 `/tmp/stash0-backup.patch`，工作区已干净。
  （注意：本会话中段出现环境干扰，见 §5.4；原 `stash@{0}` 删除动作发生在干扰之前，已确认生效。）

- [x] **T2 Stage F queue-depth 热更新（replaceDepth）** — ✅ 完成，已推送。
  选定 `replaceDepth`（改动面小、锁内 resize）。commit `6d0af88b1`（rebase 后落在
  `origin/main`）。实现要点：
  - `GovernorSpec` 新增 `MaxQueueDepth` / `MaxQueueWaitMS`；
  - `policy_publisher.go` 的 catch-up 查询 `SELECT` 增加这两列并 `Scan`；
  - `specToCredentialRef` 把深度写入 `CredentialRef`；
  - `ApplyPolicy` 对每个 live forwarder 调用 `cf.replaceDepth(want)`；
  - `credForwarder` 的 `queue` 改为 `atomic.Pointer[chan]`、`limit` 改为
    `atomic.Int64`（热路径无锁、race-free）；`replaceDepth`：shrink 仅移动准入上限
    （缓冲 oversized 无害），grow 才 under-atomic 换更大 channel 并 drain 在途请求，
    配合 `wakeCh` + `pendingOld` 回收竞态落到旧 channel 的请求。
  - 新增测试 `domains/dispatch/forwarder_depth_test.go`（`TestCredForwarderReplaceDepth`
    + `TestApplyPolicyHotReloadsQueueDepth`），`go test -race ./domains/dispatch/` 全绿。

- [x] **T3 清理 unavailableGovernor** — ✅ 完成，已推送。commit `8542db7d8`。
  `domains/dispatch/governor.go` 中该类型无构造方（grep 全仓仅类型定义+3 方法+文档
  提及），已删除 13 行。`go build ./...` / `go test ./domains/dispatch/` 均 PASS。

- [x] **T4 `bg/auto_route_realtime_listener.lastPending`** — ✅ 完成（无代码改动）。
  **该字段在 HEAD 已不存在**：经核实 `grep -rniI lastpending` 全仓仅在 Markdown
  文档命中，`.go` 文件零命中。git 考古显示它于 `b996bb0c7`（Stage E）移除、在
  `d2cbaf88b` 合并回退、再于 `3ba4a5ae2` 重新移除。本 handoff §5 原描述（“Stage F
  文档记录其在用，line 124”）取自合并回退前的旧文件版本，属**过时描述**（且旧版本中
  它也是「赋值但从不读取」的死代码）。故无需删除，仅在此记录差异。建议同时划掉
  `docs/handoff/20260827-000000-stage-f-audit-fix/README.md` 第 106-109 行那条
  已被事实推翻的「lastPending 在用」记录。

- [ ] **T5 远端未合并分支处置** — ⏳ **待用户决策**（主代理不擅自删分支）。
  审计见 §5.3 处置建议表；merge/archive/delete 三栏建议已给出，等用户拍板。

- [x] **T6 更新 handoff + 推送** — ✅ 本文件即本次收尾更新，随各任务 commit 一并推送。

### 5.1 完成标准核对

| 项目 | 状态 |
|---|---|
| handoff §5 各项有终态 | ✅（T1–T4 完成+hash；T5 待用户决策） |
| 工作区干净 | ✅（`git status` 无未提交改动；见 §5.4 环境干扰说明） |
| `origin/main` 已推送 | ✅（`6d0af88b1` 含 T2；T3 `8542db7d8` 紧随推送） |
| `go build ./...` PASS | ✅ |

### 5.2 T2 关键文件路径

| 文件 | 改动 |
|---|---|
| `domains/dispatch/governor_spec.go` | `GovernorSpec` 增加 `MaxQueueDepth`/`MaxQueueWaitMS` |
| `domains/dispatch/policy_publisher.go` | catch-up 查询 `SELECT`/`Scan` 增加两列 |
| `domains/dispatch/pipeline.go` | `specToCredentialRef` 透传深度；`ApplyPolicy` 调 `replaceDepth` |
| `domains/dispatch/forwarder.go` | `queue`→`atomic.Pointer`、`limit`→`atomic.Int64`；新增 `replaceDepth`/`reclaimPendingOld` |
| `domains/dispatch/forwarder_depth_test.go` | 新增 T2 单测 |

### 5.3 T5：远端未合并分支处置建议表

`git branch -r --no-merged main` 共 **20** 条（审计于 2026-08-27，`git fetch` 后）。
分级建议如下；**删除类操作须经用户确认，主代理未做任何分支删除/合并**。

| 分支 | 最后提交 | 距今天数 | 建议 | 理由 |
|---|---|---|---|---|
| `origin/agent/lp9-154-promotion` | 2026-08-25 | 2d | **merge** | 推广类，1 commit，待 review 后合入 |
| `origin/agent/ursm-rawmodel-key-lp1` | 2026-08-21 | 6d | **review/rebase** | 虽 6d 前但领先 2466 commits（自旧 main 分叉），需 rebase 或确认是否已被取代 |
| `origin/archive/stash-20260721` | 2026-07-21 | 37d | **archive（保持）** | 命名已带 `archive/`，超 30d 陈旧，保留即可 |
| `origin/feat/llm-gateway-deploy-245-20260802` | 2026-08-02 | 25d | **merge** | 1 commit，含 migration ledger 校验，可合入 |
| `origin/feat/v5-ui01-menu-sync` | 2026-08-18 | 9d | **merge/review** | 3 commit，UI 菜单同步 |
| `origin/fix/anthropic-main-sync` | 2026-08-27 | 0d | **merge** | 今日同步 main，1 commit |
| `origin/fix/classify-eaddrnotavail-network` | 2026-08-15 | 12d | **merge** | 网络错误分类，1 commit |
| `origin/fix/color-token-round2` | 2026-08-25 | 2d | **merge/review** | 5 commit（halfking），含请求内容捕获 |
| `origin/fix/credential-monitor-audit` | 2026-08-21 | 6d | **merge** | 凭据监控加固，1 commit |
| `origin/fix/redis-queue-lifecycle` | 2026-08-27 | 0d | **merge** | 今日 Redis 队列生命周期，4 commit |
| `origin/fix/request-logs-redis-timeout` | 2026-08-25 | 1d | **merge** | 请求日志查询预算，4 commit |
| `origin/fix/responses-dispatch-failover-audit` | 2026-08-20 | 6d | **merge** | Responses failover，1 commit |
| `origin/fix/self-check-tool-continuation` | 2026-07-18 | 39d | **archive/delete** | 陈旧 self-check 修复，疑似被取代 |
| `origin/fix/session-final-cache-timeout-retry` | 2026-08-11 | 15d | **merge** | 会话缓存超时重试，1 commit |
| `origin/fix/stats-usage-facts-migration-537` | 2026-08-23 | 3d | **merge** | migration 537，2 commit |
| `origin/fix/sticky-fresh-session-lb` | 2026-08-27 | 0d | **merge** | 今日 sticky 会话负载均衡，1 commit |
| `origin/integration/stage-f-policy-hot-reload` | 2026-08-26 | 0d | **merge 残留或 archive** | Stage F 源分支（halfking）；其修复多数已合入 main，确认无残留后归档 |
| `origin/opencode/glowing-tiger` | 2026-07-13 | 44d | **archive/delete** | 陈旧，44d 无更新 |
| `origin/opencode/hidden-otter` | 2026-07-16 | 41d | **archive/delete** | 陈旧，41d 无更新 |
| `origin/tmp/local-changes-20260813` | 2026-08-13 | 13d | **extract 后 delete** | 命名 `tmp/` + “wip 本地未提交修改”，提取有用内容后删除 |

**汇总**：建议 merge ≈ 14 条、archive/保持 1 条、archive 或 delete（陈旧）≈ 4 条、
review/rebase 1 条。无一条需立即删除，全部待用户确认。

### 5.4 环境干扰说明（rule 3：发现与 handoff 不符现状，先核实并记录）

执行期间观测到**并行自动化审计修复进程**在共享工作树中活动，与本会话竞争：
- 曾自动 `git checkout` 到 `audit/followup-20260827` / `audit/followup-20260827-v2`
  分支，并把本会话的 dispatch 改动 stash（`stash@{0}` 标题 “preserve preexisting
  dispatch queue-depth work before audit fixes”，即 T2 内容，已安全恢复）；
- 自动改动 `admin/free_pool_extra.go`、`bg/credential_probe_v2.go`、
  `domains/approval/llm_client.go`、`pool/pool.go` 等（与本任务无关）。

处置：上述无关改动均已 `git stash push` 隔离（可恢复，未删除），并备份至
`/tmp/automated-auditfix-wip.patch`。本会话交付物（T2 `6d0af88b1`、T3 `8542db7d8`
及本 handoff 更新）均已落在 `main` 并推送；并行进程的改动未进入本会话任何 commit、
未被推送。建议后续单独会话处置这些隔离 stash 与未合并分支（§5.3）。

## 6. 主代理 + 子代理总提示词

新会话开始时，将 §7 的提示词整段复制作为系统级/首条指令。该提示词已内嵌
本 handoff 的关键路径，无需附加其他上下文。

## 7. 复制用总提示词

```text
你是 llm-gateway-go 仓库的主代理（工作目录：
/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4）。
先读 docs/handoff/20260827-073000-phase-b-audit-closeout/README.md，
按其 §5「剩余任务」推进。规则：

【全局规则】
1. main 分支直接小步提交；每次 commit 前 go build ./... 必须 PASS，
   涉及的包 go test ./<pkg>/ 必须 PASS；push 前先 fetch 再 rebase。
2. 每完成一项：更新 handoff 文档中该项状态（打勾+commit hash），
   并 git commit + push。
3. 发现与 handoff 描述不符的现状（代码已被别人改），先核实、在文档中
   记录差异，不要盲目执行。
4. 删除类操作（stash drop、分支删除）必须先留备份再执行。

【并行模式】
- 使用 Agent 工具并行派发子代理，子代理类型只用：
  a) Explore（只读侦察，产出结论不产出代码）
  b) general-purpose（可写，负责独立编码/测试任务）
- 派发原则：互不触碰同一文件的子任务才并行；凡是要改
  cmd/gateway/main.go 或 bg/ 包的任务一律串行（合并冲突高发区）。
- 主代理职责：读 handoff → 拆任务 → 并行派发只读侦察 → 汇总结论 →
  串行执行写操作 → 验证 build/test → 提交推送 → 更新 handoff。

【剩余任务（按序）】
T1 删除过时 stash：git stash list 确认 stash@{0} 为
   "WIP on main: 6cfab39b3" → git stash show -p stash@{0} >
   /tmp/stash0-backup.patch 留档 → git stash drop stash@{0}。
T2 Stage F queue-depth 热更新：先派 Explore 子代理侦察
   internal/governor/（或 grep -rn bump_credentials_governor_revision
   定位）现状与 forwarder 结构，主代理汇总后在 replaceDepth 与
   drain-rebuild 两个方案中选定一个（推荐 replaceDepth：改动面小，
   锁内 resize channel 需处理 in-flight），实现 + 单测 + 提交。
T3 清理 unavailableGovernor：确认 governor.go 中该类型无构造方后删除，
   go build 验证。
T4 处理 bg/auto_route_realtime_listener.go 的 lastPending 字段：
   核实是否死代码，是则删，不是则在 handoff 记录原因。
T5 远端未合并分支审计：派 Explore 子代理逐条列
   git branch -r --no-merged main 的最后提交时间/内容摘要，主代理
   汇总成处置建议表（merge / archive / delete）写进 handoff，
   等用户决策，不擅自删除分支。
T6 每项完成后更新 handoff 文档 §5 状态并推送。

【完成标准】
handoff §5 所有项有终态（完成+hash / 待用户决策），工作区干净，
origin/main 已推送，go build ./... PASS。
```
