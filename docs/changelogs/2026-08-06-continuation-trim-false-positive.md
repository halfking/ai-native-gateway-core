# continuation 关键词子串误判 → 静默丢弃最近一轮对话

- **日期**: 2026-08-06
- **影响**: agent / 工具调用会话在网关侧被静默截断最近一轮 user+assistant，模型据残缺历史作答并凭空编造状态；生产实测 **187 次裁剪 / 2 小时**
- **优先级**: P0（静默数据丢失，无错误码、无告警、审计记为成功）

## 症状

用户报告："通过网关操作后，会话中的信息有丢失，返回的信息不是期望的信息。"

同一会话内可复现的错乱：

- 声称某文件未提交，实际上一轮已 commit 成功（`571c4ba3`）
- 声称"测试全部通过"，实际当轮未运行任何测试
- 把远程命令当本地执行（查 `/opt/llm-gateway-go/releases/`、`/var/log/nginx/`，本地均不存在）
- 声称"看到 443 返回 404"，实际从未发起该请求

共同特征：**上一轮的 user+assistant 从上游请求体中消失**，模型把空缺补成了幻觉。HTTP 200、审计 `success=true`、无任何错误信号。

## 根因

`domains/streaming/executors/node_failover.go` 的 `IsContinuationOrRetry` 对**最后一条 user 消息**做**大小写无关的子串包含**匹配：

```go
defaultContinue := []string{"继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说"}
...
if containsFold(lastUserContent, kw) { return true, false }
```

命中后 `trimOneMessageFromBody`（continuation.go:28-35）删掉最后一轮 user + assistant，裁剪后的 body 发往上游。

关键词 `"go"` 与 `"continue"` 在子串语义下的爆炸半径：

| 内容 | 命中词 |
|---|---|
| `golang` / `go test ./...` / `django` / `algorithm` / `good` / `ongoing` | `go` |
| 代码中的 `continue` 语句、日志行 `continue keyword detected` | `continue` |
| 任何含"继续/接着"的叙述性中文 | `继续` / `接着` |

在 agent 会话里讨论 Go 代码、跑 `go test`、贴日志 —— 几乎每轮必然命中。

2026-08-06 早前的修复（executor.go:1775）只豁免了 `X-Gw-Is-Auto: true` 的自动标题/摘要请求，**真实用户会话完全没有豁免**。

叠加的其它裁剪源（同批日志）：

- `sanitize_tool_messages: removed orphaned tool messages` — 单请求 `removed: 131, original: 221, sanitized: 90`
- `validate_and_fix_request: applied automatic fixes` — `221 → 67`，删 154 条

## 修复

### 第一层：分类器改为精确匹配（node_failover.go）

1. **长度门限** `continuationMaxRuneLen = 32`：超过 32 runes 的尾部 user 消息一律不视为 nudge。
2. **精确匹配**替代子串包含：`normaliseNudge` 归一化（小写 + 去首尾空白 + 去首尾标点）后与关键词**全等**比较。

只有"整条消息就是一个纯 nudge"才触发裁剪。这是必要的严格度 —— 裁剪会同时丢掉 nudge 和上一轮回答，而 `继续修复这个 bug`、`请继续下一步`、`continue the refactor` 都携带真实指令，丢掉即信息损失。

注：word-boundary 匹配不够。`go test ./...` 中的 `go` 是独立 token，仍会被裁剪。

### 第二层：工具会话整体豁免（executor.go:1777）

```go
if !isAutoReq && !params.ToolsRequested && params.SessionID != "" && ... {
```

`ToolsRequested` 表示入站 body 带非空 `tools` 数组，即 agent 循环。删除最后一轮会破坏 `tool_call` / `tool_result` 配对，即使用户在 agent 循环中真的输入"继续"也不应裁剪。

## 行为变化

| 输入（尾部 user 消息） | 修复前 | 修复后 |
|---|---|---|
| `继续` / `continue` / `继续。` / `  继续  ` | 裁剪 | 裁剪（不变） |
| `重试` / `retry` / `try again` | 重放缓存 | 重放缓存（不变） |
| `go test ./...` | **裁剪** | 不裁剪 |
| `golang` / `django` / `good` / `ongoing` | **裁剪** | 不裁剪 |
| `继续修复这个 bug` / `请继续下一步` | **裁剪** | 不裁剪 |
| 含 `continue` 的长语料（transcript / 代码） | **裁剪** | 不裁剪 |
| 任意 nudge + `tools` 非空（agent 会话） | **裁剪** | 不裁剪（第二层） |

纯聊天场景的原有体验保留；agent 场景的静默数据丢失消除。

## 测试

`domains/streaming/executors/continuation_test.go`，7 个用例全 PASS：

| 用例 | 覆盖 |
|---|---|
| `TestContinuationTrim_LongCorpusNotFlagged` | 长 transcript 含 `continue` 不再误判 |
| `TestContinuationTrim_SubstringFalsePositives` | 13 个误伤输入（`golang`/`go test ./...`/`django`/`good`/`continues`/`discontinued`/`ongoing work`/`继续修复这个 bug`/`请继续下一步`/…） |
| `TestContinuationTrim_GenuineNudgesStillFlagged` | 9 个真 nudge 仍生效（含标点与空白变体）+ 5 个 retry 关键词 |
| `TestContinuationTrim_TrimStillWorks` | 真 nudge 触发后裁剪逻辑本身正确 |
| `TestContinuationTrim_ShortCorpusNotFlagged` | 原有控制用例 |
| `TestContinuationTrim_ToolSessionExemptionMarker` | 第二层豁免标记 |
| `TestContinuationTrim_AutoRequestExempt` | 第一层 header 豁免（改为纯 nudge 语料，不再 skip） |

被改写的旧测试：`TestContinuationTrim_RemovesUserCorpus` 原先断言"长语料含 continue **必须**被 flag"，即把 bug 行为固化为契约，已替换为 `TestContinuationTrim_LongCorpusNotFlagged`。

验证命令：

```
go build ./...                                          # OK
go vet ./domains/streaming/executors/                   # OK
go test -race -count=1 ./domains/streaming/... ./metrics/...   # 全部 ok，无 race
```

## 即时缓解（无需发版）

热配置摘掉高危词：

```
llmgw_continue_keywords = ["继续","请继续","接着说","keep going"]
llmgw_retry_keywords    = ["重试","请重试","再试一次","try again"]
```

移除 `go`、`continue`、`come on`、`接着` 可立刻消除绝大部分误伤。

## 关联

与 `2026-08-06-auto-title-continuation-trim.md`（同一 continuation 逻辑，auto 请求侧）为同源缺陷的两个面：该次只修了 auto 请求路径，本次修分类器本体 + agent 会话。

诊断触发链：用户报告 minimax 会话中断 → 排查 `eof_without_done`（`0818a07c` 观测补丁）→ 发现真实信息丢失来自 continuation 裁剪。
