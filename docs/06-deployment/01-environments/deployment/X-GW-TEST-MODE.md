# X-Gw-Test-Mode — 不消耗上游 token 的 auto 路由回归

> 状态：2026-09-29 实施 + 批判式复审修正
> 代码：`domains/streaming/auto_route.go`（模式与 mock 生成器）、
> `handler.go` / `messages.go` / `responses.go`（三协议接线）、
> `cmd/autoroute-e2e-audit`（回归工具）

## 用途

系统化验证 `model=auto` 的匹配流程，**不产生真实上游调用**。
对 252 / 245 / 本地网关高频跑 auto 回归时，成本从"每轮真金白银"降为 0。

## 四种模式

请求头 `X-Gw-Test-Mode`：

| 值 | auto 路由 | 上游调用 | 用途 |
|---|---|---|---|
| `mock` | 执行 | **无** | 验证 auto 决策层，零成本 |
| `auto-only` | 执行 | **无** | 同 `mock`（保留独立名以便将来分化） |
| `other-only` | **跳过** | 有 | 隔离非 auto 回归 |
| `full` / `live` / 不带 | 执行 | 有 | 生产默认，行为不变 |

未识别的取值一律降级为 `live`，并在日志中不记录原始串。

## 鉴权（务必先读）

非 `live` 模式**必须**同时携带共享密钥：

```bash
# 请求头
X-Gw-Test-Mode: mock
X-Gw-Test-Mode-Token: <LLM_GATEWAY_TEST_MODE_TOKEN 的值>
```

服务端配置：

```bash
export LLM_GW_TEST_MODE_TOKEN='<运维生成的长随机串>'
```

**fail-closed 语义**：该环境变量未设置时，**任何**调用方都无法开启非 live 模式，
而不是"默认放行"。这是刻意的——未配置的测试开关绝不能变成生产降级开关。

### 为什么不用 X-Gw-Source-Actor

初版实现用 `X-Gw-Source-Actor` 白名单做鉴权，**这是错的**（2026-09-29 复审修正）。
该头在 `internal/loopback/token.go` 的 `CorrelationHeaders` 清单里，
`middleware.StripUntrustedCorrelationHeaders` 会把它从**所有**不带本进程
loopback token 的请求上删除。也就是说：外部审计工具、人工 curl 发送的
actor 头在到达 handler 之前**已经被剥掉了**，闸门对"它本来要服务的那些
调用方"永远返回 false，请求被静默降级为 live——也就是重新发生了这个功能
要避免的真实上游调用。

初版单测之所以全绿，是因为它直接对手工构造的 `http.Request` 调
`testModeAllowed`，**从未经过中间件**。现已有端到端测试
（`TestTestModeAllowed_EndToEndThroughLoopbackMiddleware`）跑真实中间件链。

`X-Gw-Test-Mode` / `X-Gw-Test-Mode-Token` **不在**剥离清单内，可正常透传。

## 响应特征（三协议一致）

非流式：body 内含 `mock_marker`，`usage.mock = true`（OpenAI 侧
`usage` 内），响应头含：

- `X-Gw-Mock-Marker: <mode>` — 生效的模式
- `X-Gw-Test-Mode: <mode>` — 回显
- `X-Gw-Auto-Decision: {...}` — 与 live 路径同一份决策 wire

流式（`"stream": true`）：按各协议 SSE 语法输出并带终止事件——
OpenAI `data: [DONE]`、Anthropic `message_stop`、Responses `response.completed`。
**流式请求不会被塞回一个 JSON 对象**（那会让 SSE 客户端挂到超时）。

## 日志

mock 请求**会写一行真实的 `request_logs`**，而不是"标记已写但实际没写"。
这是修正的第二个缺陷：初版只调 `markLogged()`，而该分支在
`recordInitialRequestLog` 之前返回，安全网又只在 `ErrCode != ""` 时补行，
结果 mock 流量在看板和审计里**完全消失**。

行的特征：`request_status = success`，`error_kind = '<mode>_mock'`
（`mock_mock` / `auto-only_mock`），token 用量为 0。
过滤内部测试流量：

```sql
SELECT * FROM request_logs_hot WHERE error_kind NOT LIKE '%_mock';
```

## 回归工具

```bash
export AUTO_AUDIT_API_KEY='sk-xxx'
export AUTO_AUDIT_TEST_MODE_TOKEN='<与服务端一致>'

go run ./cmd/autoroute-e2e-audit \
  -gateway http://252.llm-gateway:8782 \
  -test-mode mock \
  -suite autoroute/testdata/auto_matching_suite.jsonl \
  -out /tmp/auto_e2e_mock_results.jsonl
```

工具的两道防护：

1. 带了 `-test-mode` 却没带 token → **发第一个请求前就退出**并说明原因。
   否则网关会静默走 live，这轮"mock 回归"会烧真钱。
2. 响应里没有 `X-Gw-Mock-Marker` → 该用例记为 `error` 并说明"模式被拒、
   本次实际走了 LIVE"。判定依据始终取自**线上实际返回的响应头**，
   而不是本地变量。

JSONL 每行含 `test_mode` 字段（`omitempty`，空 = live），
可据此区分历史数据里的 live 行与 mock 行。

## 已知边界

- `other-only` 下若 body 里仍写 `model=auto`，auto 会被跳过，
  后续按显式模型名 `"auto"` 走找不到候选的路径。调用方需自己保证
  `other-only` 时给出真实模型名。
- `mock` 只在 `model=auto` 时短路；显式模型 + mock 会照常走真实上游。
  这是刻意的（mock 的定义就是"验证 auto 决策"），
  但**调用方若以为 mock 能拦下所有请求会误判成本**。
- 三个协议的 mock 都只覆盖非流式与流式两种形态；
  工具调用、多模态等请求体内容不会被校验或改写。
- `RequestLogContext.TestMode` 目前只在进程内流转，**未落库**（无对应列）。
  库的过滤口径请用上面的 `error_kind`。
