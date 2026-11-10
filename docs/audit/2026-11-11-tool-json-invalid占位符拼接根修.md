# 2026-11-11 builtin.guard.tool_json_invalid 大量拦断 —— anthropic→responses 桥 `{}` 占位符拼接根修

> 用户报障：「211 中大量 builtin.guard.tool_json_invalid 错误，请检查并分析，修正发现的问题」。
> 结论先行：**这是网关自己制造的非法 JSON**——anthropic→responses 桥把标准 Anthropic 流里
> `content_block_start.tool_use.input` 的 `{}` **占位符**当成首个参数片段累计，终态工具参数
> 拼成 `{}{...}` ⇒ 输出合规守卫按设计 fail-closed 整流拦断。已根修 + 门钉死。

## 1. 现象与实证（211）

- 容器日志（llm-gateway-211-b，2026-11-10T16:10–16:11Z 一簇，24h 内 12 条；部署频繁换容器，
  聚合量级更大——用户侧看到的就是客户端收到的 `code=output_policy_blocked,
  rule=builtin.guard.tool_json_invalid` 错误帧）：
  - `output_compliance: blocked stream event`，label=`__tool_json@read`（93/94 字节）、
    `__tool_json@todowrite`（669）、`__tool_json@bash`（230）——**field_bytes 全部 = 2 + 合法参数长度**。
  - 同请求上下文：路由 `/v1/responses`、客户端 `MiniMaxCode`、模型 `claude-sonnet-5`
    （anthropic 协议上游；同窗先有 nvidia glm-5.3-flash chat 上游的失败尝试后 failover）。
  - 拦断发生在终态帧：`attempt_commit_gate: committing on first semantic frame` 之后 222ms，
    紧跟 `failed to write SSE chunk to client: gateway protocol terminal already rendered on
    the wire`——内容片段其实已经流出去了，守卫在 done 帧上补刀。
- 持久化面：`output_compliance_audit`/`sanitize_rule_events` 均不收 `builtin.guard.*`（合成守卫
  规则非内容规则）；`session_turns.error_kind='output_policy_blocked'` 14 天 123 次。

## 2. 机制链（为什么是网关的错）

1. 标准 Anthropic 流：`content_block_start` 带
   `{"type":"tool_use","id":…,"name":"read","input":{}}`——**`input` 恒为 `{}` 占位符**，
   真实参数经 `input_json_delta.partial_json` 增量到达。
2. IR 解析器（`internal/ir/stream.go` ParseAnthropicStreamEvent）把
   `Arguments: string(input)` 原样放进 `ToolCalls[0].Arguments`——即 `"{}"`。
3. anthropic→responses 桥（`responses_bridge.go` StreamAnthropicSSEToResponses）：
   - `SerializeResponses` 把 `"{}"` 当**首个参数增量**发出（`function_call_arguments.delta`）；
   - 终态累计器 `state.Arguments` 也把 `"{}"` 拼进去；
   - `writeFinalEvents` 发 `response.function_call_arguments.done` /
     `response.output_item.done`，arguments = `{}{"file_path":"…"}` —— **非法 JSON**。
4. 输出合规拦截器（`stream_compliance.go`）：done 帧**不是增量帧**（isIncrementalStreamText
   不含 `.done`），带名标签 `__tool_json@read` 走 CheckField → `json.Valid` 失败 →
   `builtin.guard.tool_json_invalid` fail-closed 整流拦断。**守卫行为本身是对的**——它如实
   报告了「参数不是合法 JSON」；错在参数是网关拼出来的。

对照：anthropic→**chat** 桥（`anthropic_bridge.go:1271`）**历来就排除这个占位符**
（`string(InputRaw) != "{}"` 才当初始参数），并有 content_block_stop 上的续拼校验
（ValidateStreamingToolArgs）。同一 wire 语义在 responses 桥缺失——「同一缺陷只修一半」的
跨协议形态。

## 3. 修正

`internal/ir/stream.go` ParseAnthropicStreamEvent content_block_start(tool_use) 分支：
`arguments == "{}"` 时置空（占位符不进入参数流）。选**解析器层**而非桥层的理由：
这是 Anthropic wire 级语义（占位符 vs 参数），修在源头使全部消费方一致
（responses 桥修复；chat 桥本就自行排除、不受影响；audit-ir-e2e 脚本仅解析 delta）。

非标准中继把**真实半截参数**放进 `input`（非 `{}`）的形状不受影响：保留、由后续增量续拼
（与 chat 桥 initialArgsSent 路径同语义）。

## 4. 门（有牙）

- 新增 `TestStreamAnthropicSSEToResponses_ToolUse_EmptyInputPlaceholderNotArgs`
  （domains/streaming/responses_bridge_test.go）四道断言：
  ① done 帧 arguments 必须恰好等于 input_json_delta 拼接值；② 必须是合法 JSON
  （正是守卫的判定输入）；③ output_item.done 的 function_call.arguments 同判；
  ④ `{}` 不得作为参数 delta 发出（客户端同样会拼坏）。
- **变异验证**：临时禁用占位符排除（`if false && …`）⇒ 四道断言全红；恢复后全绿。
- 同步两处把旧行为钉死的断言：
  - `internal/ir/thinking_signature_test.go`：旧断言明确期望 `{}{"city":"Tokyo"}`，
    其注释假设「下游 relay 层会滤掉它」——211 事件证明该假设对 responses 桥不成立，
    改钉正确语义；
  - golden `tool_use_stream.sse` 再生成：差异恰好是删掉一条**上游从未发过**的
    `input_json_delta partial_json:"{}"` 事件（旧解析器往返时插进去的伪事件）——往返更忠实；
  - `TestStreamAnthropicSSEToResponses_ToolUse_MultipleArgChunks` 的 delta 计数 3→2
    （第三个 chunk 的 partial_json 本为空串；旧的"3"含 `{}` 伪 delta）。

## 5. 验证

- `go test ./internal/ir/ ./domains/streaming/ ./domains/streaming/executors/
  ./domains/hooks/outputcompliance/ ./security/sanitize/ -count=1` 全绿
  （streaming 全包 103.9s）；
- `go build ./...` rc=0；`go vet`（internal/ir、domains/streaming）rc=0；gofmt 零漂移。

## 6. 边界与登记（本轮明确没做）

- **守卫语义未改**：模型**原生**产出的非法 JSON 参数（如未转义换行、单引号）在 done 帧上
  仍会被 fail-closed 拦断——这是既有设计（无法结构化脱敏 ⇒ 不放行），本轮不动。
- **登记观察项**：非 hold 模式下，参数片段（`__tool_fragment`）已流出后才在 done 帧拦断，
  「拦断」并不能收回已发字节，只是把「客户端可自行恢复的工具参数解析错误」升级成
  「整流中断」。若后续仍有 model-native 非法 JSON 的大量误伤，可考虑对该路径
  「披露 + 放行 done 帧」的豁免面——需 owner 裁决，本轮未做。
- 非标准中继「`input` 带真实半截参数 + 增量续拼」在 responses 桥没有 chat 桥那套
  ValidateStreamingToolArgs 终态校验（无 211 证据表明该形状出现过），登记为已知差异。
- 不动 chat→responses / native-responses / anthropic-passthrough 路径（无此缺陷）。
