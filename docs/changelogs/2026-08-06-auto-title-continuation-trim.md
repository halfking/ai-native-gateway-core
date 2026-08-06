# auto-title loopback 误触发 continuation trim → 出站空 messages → Ark 400

- **日期**: 2026-08-06
- **影响**: 标题池 `minimax-m2.7` 间歇性上游 400 `InvalidParameter`，credential 熔断 → 标题请求 503；部分 session 有标题、部分没有
- **优先级**: P1

## 症状

生产 154 生产日志显示 auto-title loopback 请求出站 body 极小（398 / 600 字节），
`finalizeOpenAIUpstreamBody: legacy IR path validated` 记录 `pre_messages=0, post_messages=0`，
随后上游 Ark `https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions`
返回 `400 {"error":{"code":"InvalidParameter",...}}`，cred11 + cred21 熔断。

对照：相同构造的短语料请求 `pre_messages=1` 全部 200。成功/失败只取决于出站 messages 是否被清空。

## 根因

`domains/streaming/executors/executor.go` 的 session-aware continuation/retry 检测（2026-07-22 引入）：

1. 条件 `params.SessionID != "" && e.PendingStore != nil && params.W != nil && len(params.BodyBytes) > 0` 被命中 ——
   auto-title loopback 设置了 `X-Gw-Session-Id: gt:<session>` 使 `params.SessionID` 非空
2. `IsContinuationOrRetry` 取最后一条 user 消息 content，用 `containsFold` 匹配
   `llmgw_continue_keywords`（默认含 "继续"、"continue"、"go"、"come on" 等）
3. auto-title 的 user 消息是**整个会话转录拼接**（corpus），ZCode 场景天然包含 "continue" 等字样
4. 命中 → `trimOneMessageFromBody` 删除最后 user + assistant 消息 → 出站 body 仅剩 system prompt
5. Ark 收到无 user 消息的 body → 400 `InvalidParameter` → 熔断

生产日志铁证（毫秒级相邻）：

```
07:54:26.661 session_cache: db load error session=gw_8c914c95-...
07:54:26.736 executor: continue keyword detected, trimmed one message turn session_id=gw_8c914c95-...
07:54:26.758 finalizeOpenAIUpstreamBody ... request_id=b2ac829a... pre_body_bytes=600 ... pre_messages=0 post_messages=0
```

## 修复

`Executor.Execute` 的 continuation/retry 检测对网关内部 auto 请求整体豁免：
检测 `X-Gw-Is-Auto: true`（auto-title / auto-summary loopback 均已设置），命中则跳过该逻辑。
该逻辑本就只面向真实用户的 "继续"/"重试" 语义，auto 请求是系统代发的语料，不应套用。

## 验证

- `go test ./domains/streaming/executors/ -run TestContinuationTrim -v` 3 用例全绿：
  - `TestContinuationTrim_RemovesUserCorpus` —— 回归：corpus 含 "continue" 时原逻辑会误删 user 消息
  - `TestContinuationTrim_ShortCorpusNotFlagged` —— 对照：短语料不触发（解释为何部分请求成功）
  - `TestContinuationTrim_AutoRequestExempt` —— 修复点 guard 断言
- 全包 `go test ./domains/streaming/executors/` 5.3s 通过
- `go build ./...` / `go vet ./domains/streaming/executors/` 通过

## 回滚

本改动为单文件最小补丁（executor.go 一行条件 + 注释），`git revert` 即可恢复原行为。
