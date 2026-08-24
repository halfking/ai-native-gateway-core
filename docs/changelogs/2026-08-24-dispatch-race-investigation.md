# dispatch 包 `-race` 数据竞态调查（read-only 诊断，未改动源码）

- 日期：2026-08-24
- 类型：investigation（仅诊断；按 handoff 要求本会话不修他人代码）
- 上下文：本会话（LP4/LP5）提交 `10cafca46` + `fcf80e9dd` 后，老板授权对
  `domains/dispatch` 跑 `-race`；交接文档预先声明该包存在 `journey_test.go`
  / `failover.go` 的既有竞态，需先确认归属再决定是否修复。

## 复现命令

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go test -race -count=1 -timeout 120s ./domains/dispatch/ 2>&1 | tee /tmp/race-dispatch.txt
```

完整输出：`/tmp/race-dispatch.txt`、`/tmp/race-journey.txt`、`/tmp/race-failover.txt`、
`/tmp/race-terminal.txt`、`/tmp/race-dispatch-skip-journey3.txt`（跳 TestJourney* 后全包干净）。

关键现象：跑 8.0 秒失败，`TestJourneyNodeAndModelSwitches/node_switch` 子测试在第一次
`WARNING: DATA RACE` 后被中断；只跑 `^TestJourney` 同样失败且复现两份 race report；
只跑 `^TestFailover` 干净通过；跳掉所有 `TestJourney*` 后 `go test -race ./domains/dispatch`
**6.6 秒全包 ok** —— 确认 dispatch 包除 journey 系列外没有其它 `-race` 失败。

## 竞态清单

| 文件:行 | 变量 / 字段 | 触发测试 | 严重性 |
|---|---|---|---|
| `domains/dispatch/journey_test.go:249`（写）+ `:264`（读） | 测试局部 `var summaries []string`，`OnNodeSwitchSummary` 回调 append vs 主测试 goroutine `len(summaries)` 读 | `TestJourneyNodeAndModelSwitches/node_switch` | 中（仅测试侧；不影响生产）|
| `domains/dispatch/journey_test.go:249`（写）+ `:264`（读） | 同上，但地址为 slice backing array（同一 race 的两条报告：slice header + backing array）| 同上 | 同上 |
| `domains/dispatch/journey_test.go:314`（写）+ `:318`（读） | 同模式（`TestJourneyTerminalQuotaDoesNotEmitSwitchSummary`）| 同上 | 低（潜在 race，单凭据场景下 callback 实际不触发，本会话跑 `-run` 隔离未失败；改动语义同 node_switch）|

写栈固定为：`Pipeline.Start.gowrap3 (pipeline.go:665)` → `runFailover (failover.go:27)` →
`move (failover.go:111)` → `qr.OnNodeSwitchSummary(...)`；读栈固定为：
`TestJourneyNodeAndModelSwitches` 主测试 goroutine。

## 溯源

| 文件:行 | 引入 commit | 作者 | 是否本会话 LP4/LP5 引入 |
|---|---|---|---|
| `journey_test.go:248-249,264-266` | `2471c4b82` "fix(stream): fail over quota-exhausted nodes transparently" | zcode | **否**（15:45 早于 LP4 19:47）|
| `journey_test.go:314,318` | `2471c4b82`（同 commit）| zcode | **否** |
| `failover.go:110-112`（OnNodeSwitchSummary 调用点）| `2471c4b82` | zcode | **否** |
| `queued_request.go:117-120`（OnNodeSwitchSummary 字段定义）| `2471c4b82` | zcode | **否** |
| `executor_dispatch.go:320`（生产接线点）| `2471c4b82` | zcode | **否** |
| LP4 `10cafca46` touched `domains/dispatch/journey.go`、`queued_request.go`、`streaming/executors/*`、minute_stats_pipeline_test.go | — | zcode | 未触及 `journey_test.go` / `failover.go` race 路径 |
| LP5 `fcf80e9dd` touched `queue_projection.go`、`queue_projection_test.go` | — | zcode | 未触及 `journey_test.go` / `failover.go` race 路径 |

`2471c4b82` 经 merge commit `5c235c5f8`（zcode, 2026-08-24 16:52）合入 main；本会话开工时
已在 main 上，比 LP4/LP5 早 3-4 小时。

## 归属结论

1. **本会话引入**：无。LP4/LP5 的全部 diff（`git show 10cafca46 -- journey_test.go failover.go`、`git show fcf80e9dd -- journey_test.go failover.go`）均输出 0 行 —— 不存在本会话引入的新 race。
2. **既有遗留**：`journey_test.go:249/264/314` + `failover.go:111` 的 race 由
   `2471c4b82` 引入并随 `5c235c5f8` 合入 main，归属明确为同作者 zcode 的前一会话
   （quota-exhausted failover 透明度专项）。建议下会话立项修复（不动本会话 LP4/LP5）。
3. **来自他人未提交 WIP**：N/A（该 race 已 commit 到 main）。

附加结论：dispatch 包除 journey 系列外没有其它 `-race` 失败（`go test -race -skip TestJourney
./domains/dispatch/` 6.6s ok）。handoff 文档提及的"failover.go race"实际触发点是测试侧 callback
接线方式（`var summaries []string` + `qr.OnNodeSwitchSummary = func(...)`），failover.go:111
作为生产代码本身仅是无条件调用回调，回调契约本身正确。

## 修复建议（建议下会话立项；本会话不修）

最小补丁草案（**仅供下一会话参考**，不提交）：

- 选项 A（最小改动）：在测试局部引入 `sync.Mutex`，回调与主 goroutine 都加锁。
  ```go
  var (
      summariesMu sync.Mutex
      summaries   []string
  )
  qr.OnNodeSwitchSummary = func(message string) {
      summariesMu.Lock()
      defer summariesMu.Unlock()
      summaries = append(summaries, message)
  }
  // 读处
  summariesMu.Lock()
  got := append([]string(nil), summaries...)
  summariesMu.Unlock()
  if len(got) != 1 || got[0] != "..." { ... }
  ```
  改动 ~10 行，可直接复用 `journeyRecorder` 现有的 mutex 模式（journey_test.go:13-17）。
- 选项 B（取消回调语义）：去掉 `OnNodeSwitchSummary`，把期望的 summary 文本写进
  `Observation{Type: ObservationNodeSwitched, Summary: ...}`，由 `journeyRecorder` 收齐后
  同步断言。改动更大但根治回调竞态家族。
- 选项 C（保护 callback 契约）：在 `QueuedRequest` 文档显式写"回调可从任何 worker
  goroutine 触发，调用方必须自行同步"，并在 `executor_dispatch.go:320` 的生产接线点
  `params.OnNodeJump` 审计当前实现是否已 sync（本次未审计，仅记录建议）。

修复 AC：`go test -race -count=1 ./domains/dispatch` 全包 ok，无新增 retry/deadline。

## 下一步建议

1. **不阻塞本会话收口**：本会话 LP4/LP5 已在原测试集下通过（不带 `-race`），把此 race
   留作下一会话的"dispatch 测试稳定性"独立 LP。
2. **下会话动作建议**：选 §"修复建议" 选项 A 作为最小 patch（≤ 30 行，单 commit 可
   revert），owner 仍为 zcode；改完后跑 `go test -race -count=1 ./domains/dispatch`
   + `bash verify.sh` 全过再 commit。
3. **跨会话提醒**：handoff §6"failover.go race"在概念上覆盖了生产调用契约，审计
   `executor_dispatch.go:320` `params.OnNodeJump` 是否自身 thread-safe（生产接线点，
   SSE 写入端同步状况不在本次范围）。

## 参考

- Handoff：`/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/llm-gateway-memory-optimization-phase2-handoff.md` §6
- 本会话 commit：`10cafca46`（LP4）、`fcf80e9dd`（LP5）
- 引入 commit：`2471c4b82`（zcode, 2026-08-24 15:45）
- 合入 main 的 merge：`5c235c5f8`（zcode, 2026-08-24 16:52）
