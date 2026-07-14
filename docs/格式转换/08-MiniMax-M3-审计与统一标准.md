# MiniMax-M3 审计与统一转换标准

## 1. 证据范围

本章合并以下材料：

- `docs/fixes/MINIMAX_TOOL_CALL_FIX_AUDIT_2026-07-04.md`
- `docs/fixes/2026-06-23-tool-call-protocol-mismatch.md`
- `docs/会话优化v2/02-多模态能力审计报告.md`
- `docs/会话优化v2/03-多模态技术方案.md`
- `docs/会话优化v2/04-厂商标准与适配矩阵.md`
- `docs/会话优化v2/05-融合实施方案-附件与模型契约.md`
- `docs/格式转换/01-07` 的统一转换标准
- 154 journald：MiniMax `2013`、空 choices、EOF without `[DONE]`
- 252 `request_logs`：`minimax-m3` 约 17K 条历史记录，其中工具 ID 错误和大量 `eof_without_done`

154/252 的请求体已经按生产默认脱敏，无法从历史行恢复完整工具链；因此结构结论以脱敏错误、代码和本地 fixture 三者交叉验证，不把缺失 body 当作“格式正确”的证据。

## 2. MiniMax 两条协议路径

### OpenAI-compatible

```text
客户端 OpenAI Chat
  -> prepareRequestBody
  -> SimplifyTools / capability sanitizer / tool-safe compression
  -> OpenAI Chat body
  -> MiniMax OpenAI-compatible endpoint
```

必须保持：

```json
{
  "role": "assistant",
  "content": "",
  "tool_calls": [{"id":"call_x","type":"function","function":{"name":"lookup","arguments":"{}"}}]
}
```

后续工具结果必须是：

```json
{"role":"tool","tool_call_id":"call_x","content":"..."}
```

### Anthropic-compatible

```text
客户端 OpenAI/Anthropic
  -> ParseOpenAI 或 ParseAnthropic
  -> InternalRequest
  -> SerializeAnthropic(TargetProvider=minimax)
  -> MiniMax Anthropic-compatible endpoint
```

MiniMax 兼容路径的 `tool_result` 使用 `tool_call_id`；标准 Anthropic 使用
`tool_use_id`。不能双字段输出，也不能根据 model name 猜协议。

## 3. 会话优化约束

1. 压缩按完整工具轮次删除：assistant tool call、全部 tool result 和必要的后续确认消息必须作为原子单元。
2. 压缩后必须满足 `assistant call ID -> tool result ID` 一一对应；孤儿结果 fail closed。
3. 合并 consecutive messages 不能跨越 tool round，也不能合并不同 role 的工具消息。
4. 重试复用同一逻辑附件 manifest，不重新改写工具 arguments 或媒体引用。
5. 文本可以摘要，多模态 block、tool call、tool result 和 call ID 不能静默删除。

## 4. 多模态约束

MiniMax 是否支持 image/audio/video/document 取决于具体 model、endpoint 和 provider profile。OpenAI-compatible 不等于全量多模态支持。

- data URI 只有目标 endpoint 明确支持时才原样发送。
- gateway URL 必须可达、鉴权有效、MIME 和 TTL 合法。
- Gemini file URI 不能转成 MiniMax 公网 URL；MiniMax file reference 也不能转给其他原厂。
- 不支持的媒体必须产生明确 `unsupported_modality` 或 loss report，不能转成空文本。

## 5. 当前审计结论

| 项目 | 当前状态 | 证据 |
|---|---|---|
| OpenAI assistant `tool_calls` 保留 | verified | `serialize_openai.go`、tool call tests |
| OpenAI 孤儿 tool result 校验 | verified | `validateToolCallIntegrity` tests |
| Anthropic MiniMax `tool_call_id` | verified | `serialize_anthropic_minimax_test.go` |
| MiniMax tool history compression | verified for covered shapes | `ctx_compress_test.go`、`sanitize_tool_messages_test.go` |
| MiniMax tool schema normalization | verified after this audit | `sanitizer_test.go` |
| Empty tool name rejection | fixed in this audit | `TestSimplifyTools_DropsInvalidEmptyName` |
| 154 MiniMax EOF without DONE | provider behavior observed | journald and `request_logs` |
| EOF with content considered benign | current policy | `classifyStreamInterruption`、executor tests |
| EOF with no content | should fail | empty stream detector and request logs |
| MiniMax audio/video/document | model/endpoint dependent | no universal production fixture |

## 6. 154/252 现象解释

- `tool_call_id_mismatch` 的错误正文来自 gpt-5.6-luna Responses，不是 MiniMax；MiniMax 的对应错误是 `2013 tool id not found`。两者都归类为工具上下文完整性问题，但不能混用协议修复。
- 252 历史 `minimax-m3` 工具错误请求大多只保留脱敏 preview，且 message 数为 0，表明日志策略不能用于证明完整 outbound body；必须依靠 serializer 和 fixture。
- 154 MiniMax 大量 `eof_without_done` 且已经有内容，应按供应商成功但异常终止记录，并向客户端补 `[DONE]`；没有内容的 EOF 必须失败，不能产生空成功。
- 154 中大量 `relay: dropping empty choices block` 是 MiniMax 发送 usage/空 choices 终止块的兼容性现象，网关可丢弃该块，但不能因此把整个响应判定为正常完成。

## 7. 后续验收

每个 MiniMax credential/model/endpoint 必须有脱敏 fixture，记录：`provider_id`、`catalog_code`、`protocol`、`raw_model`、API version、captured_at、redaction version。最低场景：普通文本、单工具、并行工具、孤儿工具、长上下文压缩、data image、file reference、stream DONE、stream EOF with content、stream EOF without content。
