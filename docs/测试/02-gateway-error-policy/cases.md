# 02 网关错误策略 — 用例

| ID | 场景 | 预期 |
|---|---|---|
| C01 | `empty_response` 投影 | `IsRetryable=false` 且 `EffectiveRetryable=true`（候选转移） |
| C02 | `candidate_failure_logs.retryable` | empty_response / no_available_channel 记 true |
| C03 | 502 overloaded 正文 | `KindUpstreamOverloaded`，EnqueueProbe=true，Scope=model |
| C04 | KindNetwork / KindConcurrent | `isTransientFailoverKind=true`，打出 trying-next 日志 |
| C05 | 写端 `broken pipe` / EPIPE | `KindCanceled`，不转移节点 |
| C06 | 读端 `broken pipe` | `KindNetwork`，可转移节点 |
| C07 | survival `committed_output` | 客户端帧含 `reason=committed_output` 且 `retryable=false` |
| C08 | MiniMax token wrap | `minimax[>[<tool_call>...]<]` unwrap 后可解析 |
| C09 | 宽松 tool_call | `<tool_call>ssh ...</tool_call>` → structured tool_calls |
| C10 | `minimax-m3-high` / `GLM-5.2` | 命中 10s/50 holdback |
| C11 | request_flow 属性 | 含 event / request_id / kind / action / retryable |
| C12 | survival 无节点等待 think | 每次重试的 `: thinking:` 帧含底层失败 kind（`原因=wait_recovery_window:no_available_channel`）；断开后执行器调用增量 ≤1。E2E：`survival_no_nodes_e2e_test.go` |
