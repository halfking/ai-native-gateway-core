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

- [x] **T5 远端未合并分支处置** — ✅ 完成（用户确认“按建议全量处置”）。
  先逐条复核当前 `main` 后发现并行流程已吸收/取代绝大多数分支工作，原先的
  12 个 merge conflict 无需盲目解决。已归档并删除 14 条原远端 ref；其中每条均有：
  远端 `archive/20260827/...` 同 hash 副本、本地 `backup/archive-20260827/...` ref、
  以及 Git bundle 三重备份。详情见 §5.3。

- [x] **T6 更新 handoff + 推送** — ✅ 本文件记录 T5 的最终处置结果，随收尾 commit 推送。

### 5.1 完成标准核对

| 项目 | 状态 |
|---|---|
| handoff §5 各项有终态 | ✅（T1–T6 全部完成） |
| 工作区干净 | ✅（`git status` 无未提交改动；见 §5.4 环境干扰说明） |
| `origin/main` 已推送 | ✅（T2 `6d0af88b1`、T3 `8542db7d8`、本次 T5 记录均已推送） |
| `go build ./...` PASS | ✅ |

### 5.2 T2 关键文件路径

| 文件 | 改动 |
|---|---|
| `domains/dispatch/governor_spec.go` | `GovernorSpec` 增加 `MaxQueueDepth`/`MaxQueueWaitMS` |
| `domains/dispatch/policy_publisher.go` | catch-up 查询 `SELECT`/`Scan` 增加两列 |
| `domains/dispatch/pipeline.go` | `specToCredentialRef` 透传深度；`ApplyPolicy` 调 `replaceDepth` |
| `domains/dispatch/forwarder.go` | `queue`→`atomic.Pointer`、`limit`→`atomic.Int64`；新增 `replaceDepth`/`reclaimPendingOld` |
| `domains/dispatch/forwarder_depth_test.go` | 新增 T2 单测 |

### 5.3 T5：远端未合并分支最终处置（2026-08-27）

用户确认“按建议全量处置”后，主代理在执行 merge 前重新以当时最新 `main`
（`6e4a0c081`）审计了每条分支的 patch 等价性和差异计数。结论是：大部分分支工作
已被当前 main 吸收或由后续提交取代；早先尝试合并的 12 条产生真实代码冲突，不应使用
“ours/theirs”盲目自动解决。改按 archive-first 策略收尾。

**归档并删除原 ref（14 条，全部已验证）：**

| 原远端分支 | 原 hash | 处置 | 恢复路径 |
|---|---|---|---|
| `agent/lp9-154-promotion` | `01921e660` | 已集成，原 ref 已删 | `origin/archive/20260827/agent/lp9-154-promotion` |
| `feat/llm-gateway-deploy-245-20260802` | `dd009b39f` | patch 等价，原 ref 已删 | `origin/archive/20260827/feat/llm-gateway-deploy-245-20260802` |
| `fix/classify-eaddrnotavail-network` | `bade62c2f` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/classify-eaddrnotavail-network` |
| `fix/color-token-round2` | `918503272` | 已由后续统一集成取代，原 ref 已删 | `origin/archive/20260827/fix/color-token-round2` |
| `fix/credential-monitor-audit` | `87baeaba5` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/credential-monitor-audit` |
| `fix/redis-queue-lifecycle` | `3b9f4edf2` | 已 ancestry-merged，原 ref 已删 | `origin/archive/20260827/fix/redis-queue-lifecycle` |
| `fix/request-logs-redis-timeout` | `826bcf883` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/request-logs-redis-timeout` |
| `fix/responses-dispatch-failover-audit` | `10f611b38` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/responses-dispatch-failover-audit` |
| `fix/self-check-tool-continuation` | `c7d9642a1` | 已被较新工具结果路径取代，原 ref 已删 | `origin/archive/20260827/fix/self-check-tool-continuation` |
| `fix/session-final-cache-timeout-retry` | `214b9bcdb` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/session-final-cache-timeout-retry` |
| `fix/stats-usage-facts-migration-537` | `82c46a4b3` | patch 等价，原 ref 已删 | `origin/archive/20260827/fix/stats-usage-facts-migration-537` |
| `opencode/glowing-tiger` | `216f2a2e2` | 较新 ops 路由/UI 已覆盖，原 ref 已删 | `origin/archive/20260827/opencode/glowing-tiger` |
| `opencode/hidden-otter` | `3d5075c3c` | patch 等价，原 ref 已删 | `origin/archive/20260827/opencode/hidden-otter` |
| `tmp/local-changes-20260813` | `a17d483cb` | WIP 已由重编号 migration/更强实现取代，原 ref 已删 | `origin/archive/20260827/tmp/local-changes-20260813` |

删除前对每条都执行：本地 `backup/archive-20260827/...` ref、远端 archive ref（与原
hash 逐条比较）、以及 bundle 三重留档。bundle：
`/tmp/llm-gateway-branch-archive-20260827/remote-branches.bundle`（288 MB）；manifest：
`/tmp/llm-gateway-branch-archive-20260827/manifest.tsv`。删除使用
`--force-with-lease=<原 hash>`，避免并发推进时误删。

**由并行流程先行处理（3 条，主代理未重复删除）：**
`fix/anthropic-main-sync`、`fix/sticky-fresh-session-lb`、
`integration/stage-f-policy-hot-reload` 在复核期间已从远端删除。前两者分别已由 main 的
Anthropic conversion 集成和 `8579bdc84` fresh-session sticky 修复取代；Stage F 的
publisher/forwarder e2e 与后续 hot-reload 工作已在 main。因原 ref 在操作前已不存在，
没有伪造 archive ref；如需历史对象，可从 Git 服务日志或现有本地 clone 的 reflog 恢复。

**保留（2 条，需人工 review/rebase）：**

| 分支 | 原因 |
|---|---|
| `origin/agent/ursm-rawmodel-key-lp1` | 与 main 严重发散（`main...branch = 3207 / 2466`）；仍有 7 个未等价提交，包括 raw-model cache fan-out、4xx cooldown reset 与诊断/性能记录，不能自动 merge。 |
| `origin/feat/v5-ui01-menu-sync` | V5 lifecycle/capability/PG state-store（如 `plugin-runtime/lifecycle.go`、`capability.go`、`state_store_pg.go`、migration 352）未证明已被 main 等价吸收，保留供产品/架构 review。 |

**保持原样：** `origin/archive/stash-20260721` 已是 archive 分支，不移动、不删除。

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
未被推送。隔离 stash 仍待另行审计；分支处置结果见 §5.3。

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
