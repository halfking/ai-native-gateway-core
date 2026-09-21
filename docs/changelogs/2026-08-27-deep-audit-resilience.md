# 2026-08-27 Deep-audit resilience fixes

## 背景
承接 `2026-08-27-merge-loss-recovery.md`（坏 merge 修复）之后的第二轮深审计。四路只读审计并行扫了 streaming/executors、provider/credentialstate、admin livestream/bleve、ratelimit/credentialfpslot/fsstore 四个高危域。

## 合入的修复（commit `959698581` / merge `4e381f799`，分支已删）

| 级别 | 模块 | 问题一句话 | 修复 |
|---|---|---|---|
| P0 | `domains/dbdegradation/ring_buffer.go` | 部分 replay 回源错位覆写剩余记录 + 失败丢 WAL | 删错位拷贝环；失败/ctx 取消记录重排回队 |
| P1 | `domains/streaming/stream.go` | `timedLineReader` nil closer 时超时不生效 | nil closer 不等通道直接 timeout |
| P0 | `domains/streaming/connection_registry.go` | 写超时 Unregister 二次查表误删同名新连接 | 指针 identity 比对只删自己 |
| P0 | `admin/live_stream_sse.go` | register/fan-out 裸阻塞，慢客户端卡死全广播 + goroutine 泄漏 | `tryRegister`（stopCh/ctx/3s）+ fan-out 写 deadline 5s |
| P1 | `internal/logging/bleve_fanout.go` | 停机 drain 不死锁；docID 同纳秒覆盖；Open 失败永不重建 | 上限 drain + 批保序；docID 加序号；`.bad-<ts>` 挪走重建 |
| P1 | `provider/client.go` | `SetDB` 裸写被懒配置场景 race；`CalcCost` NaN/±Inf 无 clamp | `c.mu` 锁写；尾部 clamp + warn |
| P1 | `domains/credentialstate/manager.go` | `GetStaleStates` 裸指针交给回调，和 Update 链竞争 | Range 克隆副本 |
| P2 | `domains/streaming/anthropic_bridge.go` | client-disconnect read-error 没 mark interrupted | 补 `MarkInterruptedWithReason` |
| 测试 | `domains/streaming/responses_bridge.go` | 初始 flush 失败 Resumable=true 允许重放 | 改 false + 回归测试 |
| 撤回 | passthrough 中断归因 | 与明确 replay 契约测试冲突 | 不动，代码里写注释说明 |

## 有意未修（后续建议列 issue）
- `attempt_commit_gate.go` gate.mu 内含网络写 + DB hook（大锁序重构）
- audit 包 `QualityFlags` setter 化（跨包）
- ring_buffer Replay 失败记 dead-letter（指标 + 命令一对）

## 验证
- `go build ./...` ✅ / `go vet ./...` ✅ / `go test ./...` ✅ 全绿
- 主分支已推送 origin/main（`4e381f799` + `dc6726c7d` 由其同事顺序又推过）
- pre-commit 全套门禁 PASS
