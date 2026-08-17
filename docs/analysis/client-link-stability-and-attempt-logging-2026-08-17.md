# 客户端链路稳定性与切换过程落库 — 架构检查报告（2026-08-17）

## 需求与结论总览

| # | 需求 | 现状 | 结论 |
|---|------|------|------|
| 1 | 上游不稳定不影响客户端 TCP；请求进来就握住连接，定时发保活信息 | preStreamKeepalive（默认开启，全协议）+ StreamSession 心跳 + AttemptCommitGate 缓冲 | **已满足**；本轮补齐 goal 重试退避窗口的 thinking 提示 |
| 2 | 非指令错误须试完所有匹配模型的可用节点才回错 | 执行器候选循环遍历全部候选；模型级切换仅 auto 请求 + 双开关（默认关） | 同模型维度**已满足**；跨模型维度是策略边界（见 §3） |
| 3 | 操作过程以 thinking 模式回送，不影响结果 | OnNodeJump“正在切换节点 (N/M)” SSE 注释；本轮补 goal 重试提示 | **已满足**（全部为 SSE 注释，对任何解析器零风险） |
| 4 | 错误如实入 log；切换过程多行记录、同一 session_id、仅一条成功 | request_logs 单行 + candidate_failure_logs 每失败尝试一行 | **本轮修复两处缺口**：session_id 直接落行（V358）+ 中途断流路径此前不落行 |

## 1. 客户端 TCP 稳定性（上游不稳定不影响客户端）

### 已有机制（逐层）

**请求进入即握连接 + 定时保活**：
- `handler.go:3435` `startPreStreamKeepalive` 对**所有流式协议**（chat / anthropic / responses）立即 `WriteHeader(200)` 并启动 `StreamSession` 心跳，默认 **15s** 间隔（`LLM_GATEWAY_KEEPALIVE_INTERVAL`），默认**开启**（`LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE` 默认 true，`stream_runtime.go:36`）。
- 心跳是纯 SSE 注释（`: keep-alive\n\n`），任何解析器都忽略——包括 opencode 这类对 `data:` 行做 Zod 校验的客户端（`handler.go:212-231` 有专门的兼容性调研注释）。
- `handler.go:3447` `w = psk.Writer()`：后续所有写入（桥接、错误信封、survival 协调器）走同一个序列化通道，心跳与数据帧不会交错撕裂。

**上游执行期间**：
- 桥接读上游阻塞时 `OnStreamHeartbeat`（`handler.go:3586`）持续心跳。
- 节点切换时 `OnNodeJump`（`handler.go:3629` → executor.go:3350/3396）发“正在切换节点 (N/M)...” thinking 注释。
- **AttemptCommitGate**（`attempt_commit_gate.go`）：buffered 模式下，未提交的尝试帧全部留在缓冲区——上游失败重试时客户端零感知；keepalive 帧直通，不推进提交状态。

**重试/恢复窗口**：
- SurvivalCoordinator（SR-W2，`survival_coordinator.go:283`）恢复等待期 `waitWithKeepalive` 持续心跳；按租户开关（`LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED`）。
- streamretry 包装器（HTTP 层首字节前重试 + `: thinking:` 保活）：`LLM_GATEWAY_STREAM_RETRY_ENABLED`，默认关；与 survival 互斥，survival 优先（`main.go:227-234` 启动期强制收敛，避免双层重试叠加）。
- **本轮补齐**：goal 重试退避窗口（`handler.go` goal_retry_scheduled 之后）现在也发“上游请求失败，正在重试 (第 N/M 次，等待 X)...” thinking 注释。此前该窗口只有底层心跳、没有人类可读的过程提示。

### 结论
需求 1 的架构已经存在且默认生效：客户端连接由网关独立维持，上游断连/切换/重试期间客户端只会看到心跳注释与 thinking 过程信息。上一轮修复（docs/fixes/2026-08-17-passthrough-error-interception.md）已消除“上游错误原样打到客户端流里”的污染源，并保证切换重试不会重放已提交内容。

## 2. 错误回送策略（全部节点耗尽才回错）

### 已有机制
- **同模型全候选**：executor 候选循环（`executor.go:2584+`）遍历路由给出的全部候选节点，任何一个失败都 `continue` 到下一个并通知客户端切换，直到 `Exhausted`。此时才走 `handler.go:4394+` 的 `model_not_found` 503 路径，消息如实写明 "All %d candidates failed"。
- **客户端断连不计入**：`isClientStreamInterruption`（executor.go:5272）把客户端主动断开从上游失败里剔除，不触发探测/熔断。
- **指令类错误快速失败**：context_length、content_filter、auth 等客户端侧错误短路，不浪费候选（`handler.go:4200+` 分类渲染）。

### 策略边界（跨模型切换）
“所有匹配的模型”的跨模型部分由两条路径承担，**均默认关闭**：
- D5 模型级回退（`handler.go:3995+`）：仅 auto 请求 + `AUTO_ROUTE_FALLBACK_ENABLED`，且**仅非流式**（流式已提交响应无法换模型重放——这是正确的安全边界）。
- dispatch_v2 `AllowModelChange`（`dispatch/gate.go:46` 默认 false）：auto 请求的 `ModelAlternatives` 链。

固定模型请求（客户端显式指定 model）按定义只匹配该模型的所有节点——已满足。若要把跨模型切换放开给普通流式请求，需要先解决“首字节前切换”的提交门控（目前只有 auto 链路具备），属于后续演进项，本轮不改默认值。

## 3. 日志与落库（多尝试、同会话、仅一成功）

### 落库模型
- **request_logs：一个 request_id 一行**，带 `session_id`（有索引）。请求全生命周期通过 WAL 阶段更新（StageExecuteFail 等），最终状态唯一——**天然“只有一条成功记录”**。`routing_attempts` JSONB 记录全部路由轮次（含每轮的候选、结果、错误）。
- **candidate_failure_logs：每个失败尝试一行**（request_id, credential, model, attempt_index, error_kind/message, 上游状态码与响应体预览, retryable, context）——即“切换的过程生成多条记录”。

### 本轮修复的两处缺口

**缺口 A — session_id 不在行上**：此前会话归集只能 `candidate_failure_logs.request_id → request_logs.session_id` 间接关联。V358 迁移给 `candidate_failure_logs` 直接加 `session_id` 列 + `(session_id, ts DESC)` 索引 + 历史回填；写入链路（`candidate_failure_logger.go` → `executor.go:3436`）同步传 `params.SessionID`。

**缺口 B — 中途断流不落行**：`streamInterruptedError` 分支（executor.go:3222+）在“可恢复切换 continue”和“终态 return”两条路径上都**绕过了** 3436 的 `LogFailure`——也就是说用户实际遭遇的故障形态（"other side closed"、上游终态错误事件导致的断流切换）**从未在 candidate_failure_logs 留痕**。本轮在该分支补 `LogFailureWithKind` 调用：带上执行器已分类的精确 kind（消息兜底会把 `stream_interrupted: network_error` 拍平成 transient）、`stream_reason`、`stream_resumable` 上下文。两条路径各留一行，同一 session_id 归集。

slog 结构化日志（`candidate_failed_trying_next` / `executor: stream interrupted` / `anthropic passthrough: upstream terminal error event` 等）此前已如实记录错误，未改动。

## 4. 变更清单

| 文件 | 变更 |
|------|------|
| `deploy/sql/migrations/V358__candidate_failure_logs_session_id.sql` | 新增：session_id 列 + 会话索引 + 回填 |
| `deploy/sql/objects/tables/candidate_failure_logs.sql` | 同步 DDL |
| `domains/streaming/executors/candidate_failure_logger.go` | LogFailure 增加 sessionID；新增 LogFailureWithKind（显式 kind 覆盖）；INSERT 增加 session_id（NULLIF 空串） |
| `domains/streaming/executors/executor.go` | 通用失败路径传 session_id；sie（中途断流）分支补 LogFailureWithKind |
| `domains/streaming/handler.go` | goal 重试退避窗口发 thinking 保活提示 |
| `domains/streaming/executors/candidate_failure_logger_test.go` | buildRow 的 session_id / 显式 kind / 兜底分类测试 |

## 5. 运维提示

- 查一个会话的全部切换尝试：`SELECT * FROM candidate_failure_logs WHERE session_id = $1 ORDER BY ts;`（V358 上线并回填后）。
- 保活节奏调优：`LLM_GATEWAY_KEEPALIVE_INTERVAL`（默认 15s）需小于客户端/中间代理的空闲超时；前置 nginx 需配 `proxy_read_timeout` 大于该值并保留 `X-Accel-Buffering: no`（网关已发）。
- survival 与 streamretry 互斥：开 `LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED` 时不要同时开 `LLM_GATEWAY_STREAM_RETRY_ENABLED`（启动期会强制关掉后者并打 error 日志）。
