# D14 安全场景横切 子代理报告（窗口：b9d9a8ba5..HEAD，核心提交 72d921819 + 2f3151a27）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **X-Gw-Source-Actor / X-Gw-Parent-Request-Id 为客户端可直接注入的受信头，本轮把 goal-% 对账压在该无鉴权头上**。handler 入口无条件采信入站头（无 loopback 校验、无来源 allowlist、全仓无任何中间件剥离 X-Gw-Source-Actor/Parent-Request-Id/X-Gw-Is-Auto——middleware/ 目录只处理 X-Request-Id）。触发路径：认证客户端在普通 `/v1/chat/completions` 请求带 `X-Gw-Source-Actor: goal-continue`（或任意值）→ handler.go:1729-1731 写 logCtx.OriginActor → request_log_pipeline.go:479-481 落库 request_logs_hot.origin_actor → 会话对账 SQL `WHERE gw_session_id=$1 AND origin_actor LIKE 'goal-%'`（设计文档 18-Goal §3/§152 行）把该客户端轮计成影子轮，污染影子轮占比与预算对账；伪造 `auto-title-generator` 还会经 admin/session_turns_tree.go:362-367 把客户端轮在前端标成 title 子轮，经 telemetry/client.go:495-501 伪造 request_type。头信任机制是 2026-08-06 旧缝，但本轮 72d921819 新增 followUpSourceActor `goal-*` 命名空间并使其成为对账关键维度，属"旧缝承重" | `domains/streaming/handler.go:1726-1731`、`domains/streaming/request_log_pipeline.go:472-482`、`docs/03-design/02-feature-design/会话优化v4/18-Goal影子指令与续跑优化方案.md:69,152` | 入口侧仅对进程内合成请求采信这三个头（或客户端入口统一剥离 X-Gw-Source-Actor/X-Gw-Parent-Request-Id/X-Gw-Is-Auto，保留 loopback 直连 h.ServeHTTP 不经剥离层的路径）；配钉桩测试 |
| 2 | P3（相邻缝，预存在） | **客户端伪造 `X-Gw-Is-Auto: true` 可把自己的一轮从 session_turns 镜像剔除（审计逃逸）**。isInternalAutoEntry 对 IsAutoRequest=true 且无 TaskType 的普通请求返回 true（走 line 927 兜底 `TaskType==nil ⇒ true`），mirror hook 直接 return。本轮相关性：response_interceptor_helpers.go:183-186 的设计决策（shadow turn 刻意不打 X-Gw-Is-Auto）依赖"该头 ⇒ 内部请求"语义，而该语义本身客户端可伪造 | `internal/sessionv2mirror/hook.go:92-94`、`internal/sessionv2mirror/hook.go:911-927`、入口读取 `domains/streaming/handler.go:1718-1720` | 与 #1 同根同修：入口剥离/来源校验 |
| 3 | P3（现状）/ 觐 P1（若引入运行时轮换） | **R33 遗留⑤核实：keyring 无锁缝隙为潜在隐患而非活竞态**。SetKeyring 无锁写字段、解密路径无锁读：`discovery/discovery.go:68-70`（写）与 `:85`（读，decryptCredential 在 discovery goroutine 内跑）；`bg/credential_probe_v2.go:125-127`（写）与 `:476`（读，decryptCiphertext 在 probe goroutine 内跑）。并发触发条件：当前 wiring 全部为 boot 期 Start 前一次性 SetKeyring（cmd/gateway/main.go:2843-2846、3871-3900、服务器 6982 才 ListenAndServe），goroutine 创建建立 happens-before，**当前无数据竞态**；且 secret.Keyring 构造后不可变（secret/aes_gcm.go:34-52，无 setter、全仓无二次 SetKeyring/rotation 调用点）。后果等级（若未来热轮换在 Start 后再调 SetKeyring）：probe/discovery 循环可能读到 nil keyring → 解密失败 → 凭据被误标 unreachable 5 分钟（credential_probe_v2.go:477-496），且 `-race` 必报 | `discovery/discovery.go:68-70,85-86`、`bg/credential_probe_v2.go:125-127,476`、`secret/aes_gcm.go:34-52`、wiring `cmd/gateway/main.go:2844,2873→3900` | 保持遗留登记；给 SetKeyring 注释加"必须在 Start 前调用"契约（或改 atomic.Pointer）防未来回归 |
| 4 | P3 | 日志双截断不一致：injectFollowUpRequest 对 defaultDispatchFollowUp 已 rune-safe 截断（bodySnippetPrefix，带 "..." 可到 259 字节）的结果再做字节级 `[:256]` 截断，可能截半个 rune。仅 slog 输出，无功能影响 | `domains/streaming/response_interceptor_helpers.go:155-158` vs `:255-263`（bodySnippetPrefix） | 删外层截断或复用 bodySnippetPrefix |

## 二、核实为健康的面

- **advisory 信号面无持久层注入（本轮特别核查③）**：ClientSignalPayload 全部消费面仅两条出口——流式 SSE 帧（handler.go:5172 → stream_recovery.go:919-924，事件名 allowlist + `json.Valid` 双校验）与非流式 base64 响应头（handler.go:5204-5205）；无任何 DB/Redis 写入。payload 字段来源：`TokensUsed=extractTotalTokens(上游响应)`（handler.go:5120）、`ContextWindow=candidates[0]` 路由快照（handler.go:5121-5126）、`FinishReason=extractFinishReason(响应体)`、request_id/session_id 服务端生成——均非客户端直接可控；json.Marshal 产物无裸换行，无 SSE 帧注入。advisory 路径确认未调用 ClaimContinueAttempt、无 hint 字段（mode_hook.go:536-585）。
- **shadow turn 会话镜像不变量**：goal 影子请求不带 X-Gw-Is-Auto，isInternalAutoEntry 的 OriginActor 枚举只认 auto-title/auto-summary/session-summary（sessionv2mirror/hook.go:921-926），goal-* 影子轮会正常进镜像——与 GLOBAL_G2 对账意图一致（response_interceptor_helpers.go:179-186 注释成立，针对网关自身请求）。
- **异步与资源安全**：defaultDispatchFollowUp 有 panic recover 记账（response_interceptor_helpers.go:210-214）；injectFollowUpRequest 有深度上限 + 每会话次数上限（:119-138）；合成请求体 bytes.NewReader 无句柄泄漏；`go h.injectFollowUpRequest(...)` 的 followUpCtx 基于 context.Background() 无取消但受上述双重上限约束。
- **溢出/转换**：bodySnippetPrefix rune 边界回退循环有界（≤256 次）；窗口无新增数值转换/长度来自外部的切片操作。
- **锁与竞态**：窗口内无新增 mutex/atomic/map 共享写；ModeHook loadBool/Int/Float/String 经 SettingsGetter 只读（mode_hook.go:790-816），Path 1.5 只读 sess 快照。
- **密钥可用性**：goal_control.go 新增启动告警为纯只读检查（llmCallerConfigured，goal_control.go:598 定义、:255-266 调用），无锁/IO 风险；新配置 spec goal_specs.go 纯声明。
- **注入面**：窗口无新增 SQL；followUpSourceActor 为固定枚举（response_interceptor_helpers.go:251-265）；落库走 telemetry 既有参数化管道。

## 三、未覆盖项与原因

- `go build / go vet / go test -race` 三门未运行——本子代理只读纪律（运行会写 build cache）；新代码已有测试覆盖（response_interceptor_helpers_test.go:315-349 覆盖 parentRequestID/actor 映射/头契约，goal_test.go:510 覆盖 Path 1.5），三门验证留主代理。
- 发现 #1/#2 的实际可利用性取决于生产入口（外层 LB/nginx）是否剥离 X-Gw-* 客户端头——需真机/部署配置确认，代码侧无剥离。
- ClientSignalAllowed（客户端能力协商头 X-Gw-Gateway-Capabilities）属自我 opt-in，无越权面，未深挖解析器（durable_contract.go:167-172 已核边界）。
