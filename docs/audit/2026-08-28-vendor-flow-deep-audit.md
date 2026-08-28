# 多厂商协议对齐与流式闭环深度审计

**日期**: 2026-08-28
**范围**: P0/P1/P2 vendor 修复及其当前 `cmd/gateway` 生产调用链
**结论**: 已修复 5 项线上闭环问题；核心协议回归通过；保留 legacy Responses 路径、SSE 单行上限和 vendor strip 重构为后续 handoff。

## 1. 模块与业务流程

### 1.1 模块职责

- `cmd/gateway`: composition root，注册 HTTP/SSE handler、候选执行器、vendor sanitizer 和持久化/诊断依赖。
- `domains/streaming`: SSE 读取、首帧处理、empty-stream gate、vendor 字段过滤、错误分类、客户端写入和流式中断。
- `domains/streaming/executors`: 候选执行、超时、重试/failover、非流式响应读取及 OpenAI→Anthropic IR 转换。
- `internal/ir`: OpenAI/Anthropic/Responses/Gemini 响应解析、内容归一化和目标协议序列化。
- `domains/transformation/anthropic`: Anthropic 兼容流转换和独立 timeout/事件读取支撑。
- `admin`、`bg`、`domains/dispatch`: 控制面、状态/健康探测、资源门禁、路由和审计事实持久化。

### 1.2 当前闭环

```text
client request
  -> auth / tenant / policy / model resolution
  -> candidate routing + concurrency/RPM/FP gates
  -> upstream HTTP/SSE request
  -> first-byte read and non-SSE error detection
  -> vendor-aware sanitizer (before strip removes error envelope)
  -> empty-stream gate (same sanitizer for start/buffered frames)
  -> IR parse / normalization / protocol conversion
  -> serialized client SSE or JSON response
  -> pending capture / audit / usage / journey telemetry
  -> terminal outcome controls retry/failover and credential state
```

## 2. 关键数据来源与去处

| 数据 | 来源 | 中间处理 | 最终去处 |
|---|---|---|---|
| `base_resp.status_code` | MiniMax 上游 JSON/SSE | sanitizer 前预检、映射 `ErrorKind` | executor retry/failover、客户端错误事件、audit reason |
| vendor 私有字段 | MiniMax/Zhipu/DeepSeek/Doubao payload | 对应 stripper；Ernie 不 strip | 客户端不接收私有字段；标准 usage/推理字段保留 |
| Qwen structured content | `content`/`delta.content` 数组 | IR text block / StreamDelta 文本归一化 | OpenAI/Anthropic/Responses 序列化 |
| reasoning | OpenAI `reasoning_content` 或 Anthropic signed thinking | IR `ReasoningContent` 或带 signature 的 block | OpenAI round-trip；Anthropic 仅回传可验证 signed thinking |
| 流式终态 | `[DONE]`、EOF、timeout、client cancel、错误 envelope | `StreamOutcome` + capture finalization | retry/failover、pending replay、日志/指标 |
| tenant/request/session | HTTP headers、路由候选、Redis/PG state | request context、审计捕获、journey | request logs、usage/ledger、session/pending store |

审计确认：不存在将无 signature vendor reasoning 伪造成 Anthropic `thinking` 的路径；Ernie `search_info.search_results[]` 当前没有 strip 接线，直接透传。

## 3. 本轮修复

### 3.1 流式 vendor 接线和 gate 闭环

- `cmd/gateway/main.go` 对 catalog 统一 trim/lower。
- 流式接入 MiniMax、Doubao、Zhipu/GLM、DeepSeek sanitizer。
- 新增 vendor-aware 内部入口，保留旧函数签名兼容已有调用方。
- 首帧、gate starting line、gate 后续缓冲帧、gate disabled 直写前以及主循环均走同一 sanitizer。
- MiniMax 错误只在 vendor 已确认或空 catalog 能通过顶层 `base_resp` 识别时分类；Doubao 不再误触发 MiniMax parser。

### 3.2 非流式 MiniMax 错误

- `executor_chat.go` 统一 catalog code。
- 显式 catalog 大小写/空白值可以正确检测。
- 空 catalog 仅对真正可解析的顶层 `base_resp` 触发预检，避免 strip 先删除错误信号。

### 3.3 JSON 错误判定

- `isJSONErrorBody` 现在要求 `error` envelope 或显式顶层 `type`/`code`。
- 单独的 `{"message":"normal metadata"}` 不会被当作错误。
- 同步修复 `domains/transformation/anthropic` 中的复制实现。

### 3.4 Anthropic timeout 和 MiniMax 数据完整性

- Anthropic OpenAI→Anthropic 首字节读取使用带 body closer 的 helper，超时关闭并 drain 读取 goroutine。
- MiniMax leak cleaner 只在发现闭合 `</tool_call>` 时清理；未闭合 marker 默认保留，避免截断合法 XML/代码或用户文本。

## 4. 并发、锁、资源和异常审计

- `stripVendorFields`/IR sanitizer 每次调用使用局部 JSON map，无共享可变状态；竞态测试通过。
- pending capture、audit capture、writer/gate 各自使用 mutex/有界 buffer；现有容量门禁避免先 append 后溢出。
- 核心 streaming 和 executor 路径 `resp.Body.Close`、retry context cancel、timeout close/drain 均有 defer/失败路径覆盖。
- Anthropic transformation 首字节路径此前可能在 nil closer 下等待阻塞 reader；现已传递 response body closer。兼容 helper 在无 closer 时不等待不可中断 reader，但调用方应优先使用带 closer 版本。
- 错误不再被错误 strip：MiniMax base_resp 在删除前检测，客户端已有语义输出时不透明重试，符合流一致性边界。
- JSON 类型断言均有失败降级；数组遍历使用 range；本轮未发现参数或状态码存在确定性溢出路径。
- 仍需单独评估 SSE 单行的上限：reader 的 64 KiB buffer 不是最大行长；本轮未改变既有 128 MiB 完整非流响应兼容策略。

## 5. 网络、TCP、密钥和数据 API

- HTTP response body 生命周期、超时、TCP 连接复用和错误 body drain 已按现有实现与测试检查；核心路径未发现确定性句柄泄漏。
- 流式连接在 client cancel、upstream EOF、timeout、解析错误和 vendor error 时都有终态；retry 受首字节/客户端可见性约束。
- 未执行真实厂商调用、未读取或记录任何 API key/密码/token；因此外部密钥可用性和真实 provider API 可用性标记为 `UNKNOWN`，不能宣称通过。
- 现有自动化测试使用本地 `httptest`/fixture，验证协议和资源边界，不等价于真实外部网络可用性。

## 6. 代码完整性与他人修改保护

审计期间发现工作树存在与本任务无关的修改（admin、多处 Go/前端测试、workflow、locale、历史 handoff）。这些文件没有被 reset、checkout、stash、格式化或加入本任务提交。最终提交只应包含本报告列出的 vendor/stream/transformation 文件、测试和架构文档。

远端曾领先于本地；同步时必须使用 `git pull --rebase`，不得 force push。提交前后需核对 cached name-only、working tree 和 `origin/main` 差异，确认没有覆盖他人改动。

## 7. 测试证据

本轮目标测试：

- `go test ./domains/streaming ./domains/streaming/executors ./domains/transformation/anthropic -run ...`
- `go test ./internal/ir ./domains/transformation ./domains/streaming ./domains/streaming/executors -count=1`
- `go test -race ./domains/streaming ./domains/streaming/executors ./domains/transformation/anthropic -count=1`
- `go vet ./domains/streaming ./domains/streaming/executors ./domains/transformation/anthropic`
- `go build ./...`

结果应绑定最终提交 SHA 记录；任何未执行的真实 provider、密钥、TCP 长连接、Redis/PG 故障演练均标记为 `UNKNOWN` 或 `SKIPPED-CONFIG`。

## 8. 剩余任务

以下事项不在本轮最小安全修复内，已写入最新 handoff：

1. legacy `StreamResponsesSSE` 是否下线或补齐 reasoning/tool/audio IR 数据。
2. SSE 单行最大长度和完整非流 body 超限后的 fail-closed 语义。
3. vendor strip/错误分类统一抽取到 `internal/vendor/strip`，消除 streaming/executors 重复实现。
4. Doubao multimodal embedding 路由、能力注册和计费的独立项目。
5. 使用真实 provider/API key 的外部可用性与 TCP 长连接演练（需受控环境和脱敏凭据）。

## 9. 总结

本轮修复关闭了流式首帧/gate 脱敏绕过、MiniMax 错误信号吞失、跨 vendor 误分类、正常 JSON 错误误判、Anthropic timeout 生命周期和 MiniMax 未闭合内容截断等问题。核心代码、竞态、构建和协议回归验证通过；未验证的外部 API/密钥/生产 TCP 可用性保持明确的未知状态。
