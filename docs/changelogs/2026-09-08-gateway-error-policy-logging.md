# 2026-09-08 网关错误策略与请求流日志

供应商错误按策略区分重试、转移节点、向客户端报错；补齐 `request_flow` 日志便于按 `request_id` 现场还原。

- empty_response 保持 `IsRetryable=false`，但 `EffectiveRetryable=true`（候选转移）
- overload 入队探测；network/concurrent 进入显式 failover 日志路径
- 写端 EPIPE → canceled；读端 EPIPE → network
- MiniMax token wrap unwrap + 宽松 tool_call 识别
- survival 终端帧携带 reason / retryable
