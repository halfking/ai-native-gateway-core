# Streaming P0 第二轮审计与修复

## 基线

- 审计基线：`origin/main`（本轮隔离 worktree 创建时为 `cf9c966e8`）
- 上一轮修复：`103144b82`，已由合并提交进入主线
- 审计范围：Anthropic 4xx body 生命周期、AttemptCommitGate 成功结算、SerializedStreamWriter capture 完整性、durable checkpoint 幂等性
- 当前工作树中的 deployment、Redis、URSM、provider 及 staged deletion 均属于并发工作，本轮未触碰

## 发现与修复

### P1：读取错误后未继续 drain

`readAndDrainErrorBody` 在受限读取返回错误时仍可能存在可读尾部；旧实现只在无错误时调用 `io.Copy`，会破坏 HTTP 连接复用。

修复：无论读取是否返回错误都执行 best-effort drain；保留首个读取错误作为返回值。新增 `TestReadAndDrainErrorBody_ReadErrorStillDrainsTail` 覆盖 `(n>0, err)` 后仍有尾部的 reader。

### P1：成功 buffered attempt 丢失 trailing partial

`GateWriter.Finish` 对未完成 SSE 分隔符的尾部数据会放入 gate buffer；成功协调器此前只调用 `Finish` 就报告成功，导致尾部数据不发送。

修复：成功路径在 `gw.Finish()` 成功后调用 `gate.Commit()`，任何提交/flush 错误都转为 fail-closed，不报告成功。新增 `TestSurvivalCoordinatorFlushesSuccessfulTrailingPartial`。

结算矩阵：

| Attempt 结果 | Finish | Commit | 对客户端 | 任务状态 |
|---|---|---|---|---|
| 成功、无 partial | 成功 | 幂等成功 | 已提交内容保留 | success |
| 成功、有 partial | 成功、缓存 partial | 成功提交 | metadata + partial 按序发送 | success |
| 成功但 Finish/Commit 失败 | 失败 | 不继续 | 不宣告成功 | fail-closed |
| 可重试失败 | 由失败分支处理 | 不提交 | discard buffer | retry |

### P2：capture 在底层写之前记录完整 payload

旧实现先更新 capture 再调用底层 writer，导致 detached no-op、写错误和短写也进入 replay capture。

修复：仅在底层完整写成功后追加 capture；失败写、短写和 detached no-op 不会伪装成已发送字节。已有 capture cap/overflow 语义保持不变。新增失败写和 detached write 测试。

### P2：checkpoint hook 重复调用

`Commit` 对同一 state 重复调用 checkpoint hook，违背“状态成功 checkpoint 后不重复执行”的契约。

修复：新增受 gate mutex 保护的 `checkpointedState` 单调字段；只有当前 state 高于已成功 checkpoint state 时才调用 hook。失败不推进该字段并继续 fail-closed。新增 `TestAttemptCommitGateCommitCheckpointRunsOncePerState` 与 `TestAttemptCommitGateCheckpointAdvancesOnce`。

## 验证

```text
go test ./domains/streaming/... ./domains/streaming/executors/...
PASS

go vet ./domains/streaming/... ./domains/streaming/executors/...
PASS（vendor/go-m1cpu 仅有 Clang VLA 扩展警告）

go build ./...
PASS

git diff --check
PASS
```

新增回归覆盖：

- error body short read、cap、nil/empty、read-error-with-tail
- successful trailing partial flush
- capture excludes failed and detached writes
- checkpoint exactly-once per state and monotonic state advancement

## 未决风险

- `go test ./...` 仍需在最终合并基线上执行；外部 Redis/PG 依赖测试失败必须与本轮回归区分。
- Gate checkpoint 与 Discard 的更复杂跨 goroutine 语义仍需后续 barrier-based 测试；本轮只修复已证实的重复 hook、partial loss、capture integrity 和 body drain 问题。
- 当前并发工作树的 SafeHGetAll、provider resolver、部署脚本和 durable context 方案不属于本轮范围，需由各自 owner 后续 handoff。

## 结论

本轮修复关闭了四个已复现的 streaming 生命周期/状态一致性缺陷，未改变协议转换、重试分类或 schema。修复和测试应随同本报告及 CHANGELOG 一起合并，作为可追溯的审计闭环。
