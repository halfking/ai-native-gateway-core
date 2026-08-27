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

按优先级排序，均已排除本会话已收尾的部分：

1. **删除过时 stash**：`git stash drop stash@{0}`（依据见 §4；如不放心可先
   `git stash show -p stash@{0} > /tmp/stash0-backup.patch` 留档再删）。
2. **Stage F 遗留三项**（见 `docs/handoff/20260827-000000-stage-f-audit-fix/README.md`）：
   - queue-depth 热更新消费端缺失（`replaceDepth` vs drain 二选一；触发器
     `bump_credentials_governor_revision` 已存在）；
   - 删除 `governor.go` 中已无构造方的 `unavailableGovernor` 类型（纯清理）；
   - ~~修复 `bg/credential_probe_v2_test.go` 缺 package 头~~ **已被后续合并修复**（本会话验证 `go vet ./bg/` PASS），Stage F 文档该项可划掉。
3. **`bg/auto_route_realtime_listener.lastPending` 死代码清理**（Stage F 文档
   记录其在用，仅为审计遗留观察项）。
4. **远端未合并分支处置**（`git branch -r --no-merged main` 共 10+ 条），
   需逐条判断合并/废弃，建议单独会话处理。

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
