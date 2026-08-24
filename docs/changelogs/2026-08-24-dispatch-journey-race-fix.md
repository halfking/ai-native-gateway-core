# dispatch 包 journey_test `-race` 数据竞态修复

- 日期：2026-08-24
- 类型：fix（test-only mutex 同步；不动生产语义）
- 关联：`docs/changelogs/2026-08-24-dispatch-race-investigation.md`（read-only 调查，b6306fd9c）
- 上下文：handoff 声明该包存在 `journey_test.go` / `failover.go` 的既有 race，前一会话
  LP4/LP5 不动；本会话按纪要 §"修复建议" 选项 A 立项修复。

## 修复范围

仅 `domains/dispatch/journey_test.go`，未触 `failover.go` / `queued_request.go` / `executor_dispatch.go`。
纪要 §"附加结论" 已明确：race 触发点在测试侧 callback 接线方式
（`var summaries []string` + `qr.OnNodeSwitchSummary = func(...)`），failover.go:111 作为
生产代码本身仅是无条件调用回调，回调契约本身正确；本修复仅在测试侧添加同步。

## 修改点

1. `TestJourneyNodeAndModelSwitches/node_switch`（`journey_test.go:233-267`）
   - 测试局部 `summaries []string` 旁新增 `summariesMu sync.Mutex`。
   - `qr.OnNodeSwitchSummary` 闭包内 `Lock/Unlock` 保护 append。
   - 测试主 goroutine 读取处先 `Lock` 复制 detaching snapshot，再 `Unlock`；断言
     使用 snapshot，避免对共享 slice 同时读 + slice header 可能后续被 append
     重分配的双层 race（纪要 §"竞态清单" 表中两条 race report）。

2. `TestJourneyTerminalQuotaDoesNotEmitSwitchSummary`（`journey_test.go:306-321`）
   - 同模式补丁。纪要判定为"低严重性 / 潜在 race"，该场景下 callback 实际不触发，
     但与上一测试同语义、同补丁模板，统一加锁保证未来回调契约不变时无回归。

## AC

- `go test -race -count=1 -timeout 120s ./domains/dispatch/` 全包 ok。
- `go test -race -count=20 -run '^TestJourneyNodeAndModelSwitches$|^TestJourneyTerminalQuotaDoesNotEmitSwitchSummary$'`
  全过，无 DATA RACE 报告（已验证）。
- `go build ./...` exit 0。
- 生产代码 diff 行数 = 0（仅测试侧改动，符合"不动 failover.go 生产语义"约束）。

## 未修 / 不修

- `TestGovernorMetricsAllowlistAcceptsClosedEnum` `-count>1` 时 Prometheus `MustRegister`
  panic：`prometheus/client_golang` registry 在同进程重复注册同名 counter 时 panic；
  与本次 race 修复无关，是测试基础设施与 `-count>1` 的既有兼容问题（独立问题，
  待运维侧立项）。
- `executor_dispatch.go:320` 生产回调接线点是否 thread-safe：纪要 §"下一步建议"
  描述独立审计范围；不在本次修复内，建议后续并发门禁补齐时立项。

## 提交

- commit：`fix(dispatch): serialize summaries capture in journey_test for -race`
- diff 范围：1 file (`domains/dispatch/journey_test.go`)、本 changelog；不含他人
  worktree 文件（.acc-*/version*/QueuePerspectivePanel/liveStreamPreferences）。
