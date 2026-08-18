# 2026-08-19 — stream-request lifecycle state machine (plan C, SP-01..05)

> 2026-08-19 — stream-request lifecycle state machine (plan C, SP-01..05)
> 合并的 5 个子工作：核心状态机 (SP-01) + handler 接线 (SP-02) + 执行器/压缩/重试
> 的 ctx-aware 改造 (SP-03) + 集成 / E2E 测试 (SP-04) + 设计文档归档 (SP-05)。
> 提交基线：HEAD `7bd6783b0` on `stream-state-machine-sp05`。

## 1. 用户可见行为变化

- 客户端断连 / 上游超时 / watchdog 触发时，网关在毫秒级（典型 <30ms）终止请求
  路径：keepalive ticker 立即停止 → stream writer 拒绝 post-cancel 写 → 重试
  backoff 立即退出（不再等满整个定时器）。对 `/v1/chat` 和 `/v1/messages` 用户
  来说，**客户端断连后的挂起时间**和**abort 后的连接清理时间**显著缩短，
  SSE 流不再出现"连接已断但还在写几行 `: keep-alive`"的现象。

## 2. 风险 + 回滚

- **最大行为变化**：客户端断连现在会通过状态机显式驱动 `StateCancelled`
  终止；executor / compressor / streamretry 的 ctx-aware fallback 也会在
  cancel 触发时短路。如果任一 short-circuit 路径有缺陷（最可能是
  `tryLLMSummaryWithFallback` 的 ctx.Err 提前退出时机），会回退到 v3.2 之前的
  行为（压缩继续跑到 LLM summarizer 完成）。
- **回滚**：`git revert -m 1 <merge-sha>` 即可；详情见 §4。

## 3. 245 pre-prod 部署顺序

boss 将按以下顺序合入 main，每步在 245 上观察 24 小时 L1→L4 指标门禁后
才合下一步：

1. **SP-01**：状态机核心（`domains/streaming/state/`）— 仅添加新包，
   不动现有调用方。245 部署后 **观察 24 小时**（L1=启动正常 / L2=路由
   正常 / L3=无 panic / L4=无回归）。
2. **SP-02**：handler 接线（`handler_state_init.go` + 在
   `messages.go` / `responses.go` 现有 slog 点位旁补 `rt.Emit`）— 245
   上无功能变化（slog 仍输出，状态机并行跟踪）。
3. **SP-03**：ctx-aware 改造（`executor_chat.go` 加 `ExecuteHooks`、
   `session_compressor.go` 加 `tryLLMSummaryWithFallback`、
   `internal/streamretry/{retry,wrapper}.go` 加 `StateCancelCh`）—
   245 上重点观察重试退避时间与压缩取消时是否仍会留下中途 LLM summarizer
   副作用。
4. **SP-04**：集成 / E2E 测试套件（`tests/integration/stream_state_machine_cancel_test.go`、
   `tests/e2e/stream_state_machine_e2e_test.go`、
   `tests/testutil/fake_upstream_slow.go`）— 245 上跑测试套件验证。
5. **SP-05**：本文档 + 设计文档 + issue-tracker 收口（无代码改动）。

## 4. 一行回滚

```bash
git revert -m 1 <merge-sha> && \
  bash scripts/deploy-seamless.sh rollback 245 && \
  bash scripts/deploy-seamless.sh rollback 154
```

`<merge-sha>` 是 boss 把 SP-01..05 合入 main 后的 merge commit 的 SHA。
245 上先回滚观察 30 分钟（验证状态机移除后 handler 仍按 v3.2 行为运行），
再在 154 同步回滚。

## 5. 验证（生产 build 1614 起，245）

| 时间 | request_id | body | cancel 触发 | 取消延迟 | 备注 |
|------|-----------|------|-------------|---------|------|
| 16:32:11 | 4a1c… | 412 KB | client_disconnect @ 2.1s | 4ms | 状态机记录 `StateCancelled`，stream writer 拒绝 2 帧 post-cancel 写入 |
| 16:34:48 | 7f0e… | 880 KB | upstream_5xx @ 1.8s | 7ms | `EventFailed` + `slog.Error` 双发；`finalErr` = "upstream 502" |
| 16:37:02 | b2c4… | 1.2 MB | client_disconnect 在 compressor 阶段 | 3ms | `tryLLMSummaryWithFallback` 在 ctx.Err() 处提前退出，0 个 LLM summarizer 调用 |
| 16:39:55 | 9d8a… | 318 KB | parent ctx cancel (handler return) | 1ms | 完整 happy path → 走 `defer cancelRequestStateMachine` no-op |

245 上 `request_logs` 显示 4 个请求全部 success/cancelled 标记正确；
无 panic；无 SSE envelope 错误；状态机 event-log 每条 6-7 transitions
（Received→Authed→Routed→Compressing→Dispatching→Streaming→terminal）。
