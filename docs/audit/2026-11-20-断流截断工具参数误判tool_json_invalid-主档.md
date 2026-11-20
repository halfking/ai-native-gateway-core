# 2026-11-20 断流截断工具参数误判 tool_json_invalid —— 根修主档

> **owner 需求**＝「拉取最新代码合并到本地，检查 ZCode 报错 `output_policy_blocked (rule=builtin.guard.tool_json_invalid)` 的原因并修正，提交推送」。
> **范围**：零迁移 / 零 schema 变更 / 零路由变更 / 零前端改动。改动面＝`domains/hooks/outputcompliance/stream_compliance.go`（+56 行）＋ 新增回归测试一个文件。
> **未部署**（owner 未要求；211 现役仍为 `2.6.1-16a9f2a7-dirty-20261120-211`，本修复待下次发版生效）。

## 一、报障与取证

### 1.1 报障原文（ZCode）

```
Response blocked by output policy (rule=builtin.guard.tool_json_invalid):
output policy blocked stream lane (rule builtin.guard.tool_json_invalid) at $
request=d2b7d79accd1800a0bc639f05a73aeac. 上游返回的内容格式有误（非法 JSON 或编码失败），请重试。…
provider=new-provider-2 provider_code=output_policy_blocked model=glm-5.3-flash
request=dc450968-410a-4656-a325-2da4364d28ee reason=unknown retryable=false
TraceID: 20e262a1-83f3-4766-bca3-487f4e587273
```

### 1.2 生产取证（211 docker logs，48h 窗口内唯一一条命中）

按 `client_request_id=dc450968…` 对上内部 request（日志侧 `[REDACTED:2353a3fb]`），时间线逐条：

| 时刻（09:03:06 UTC） | 事件 | 关键字段 |
| --- | --- | --- |
| .576 | `anthropic stream: upstream mid-stream JSON error` | `kind=upstream_down`，"Upstream response stream was interrupted"，`client_visible_chunks=2`，上游 `https://apiclaude.cc`（provider_id 33370，glm-5.3-flash） |
| .577 | `executor: stream interrupted` → `non-resumable stream interrupted` | `chunk_count=2`，attempt 2 |
| .577 | `executor: mid-stream terminal failure notice emitted` | `chunks_sent=0`（合规守卫扣着帧，客户端一个字节都没收到） |
| **.587** | **`output_compliance: blocked stream lane`** | **`lane=anthropic.block.1.partial_json`，`label=__tool_json@Bash`，`lane_bytes=2`，`rule_id=builtin.guard.tool_json_invalid`** |
| .592 | `audit: request completed` | `success=false` |

`POST /v1/messages`（Anthropic 协议，ZCode/3.19.0），duration 166s，response_bytes 1526。

## 二、根因（因果链）

1. **上游断流**：apiclaude.cc 在第 2 个工具调用（Bash，block index 1）的 `input_json_delta` 参数增量发到一半时把流掐断（executor 已正确按 `upstream_down` 分类记账）。
2. **截断前缀被当完整参数**：合规守卫对工具参数 lane 采取「扣帧到终态、合并后整体判定」策略。流死之后 `finish()` 走 `FlushStreamPending` → `releaseCheckedFrames`，把只累积到 **2 字节**的截断前缀（`{"`）当作「上游发来的完整参数」去 `json.Valid`。
3. **修补失败**：`tryRepairToolJSON` 对 `{"` 的闭括号/引号补全组合全部非法（`{""`/`{"}`…）⇒ 判 `tool_json_invalid` fail-closed。
4. **错误归因被顶掉**：`PolicyBlockError` 把写者的 `blocked` 置真 ⇒ finish() 渲染「策略拦截」终帧（`output_policy_blocked`，retryable=false，「报告网关管理员」），**executor 已分类好的 upstream_down 终帧反而被丢弃**（`if w.blocked { _ = pending }`）⇒ 客户端看到的是完全错误的问题报告方向，且审核面多了一张「策略拦截」单，而真因早已有人记账。

### 2.1 判定口径（为什么这是量具/判定错，不是内容违规）

`builtin.guard.tool_json_invalid` 的合同（`builtin_rules_meta.go`）是「**上游发完了**的工具参数本身不是合法 JSON」（rv-2089 形态：完整流 + 非法参数，处置方向＝查上游格式转换）。本案上游**根本没发完**——拼接文本是**截断前缀**，不是「上游发来的完整参数」。传输中断 ≠ 格式违规，两个处置方向完全相反。

## 三、修法（结构性：给守卫补上「流是否正常收尾」的信号）

**信号**：`complianceStreamState` 新增 `terminalSeen`——`processStreamChunk` 见过终帧（`message_stop`/`[DONE]`/`response.completed` 等，`isStreamTerminalFrame`）即置位。没见过终帧就来 release ＝ `FlushStreamPending` 路径 ＝ 流被中途掐断（正常收尾的流在终帧处已 inline release 并 clear，flush 是 no-op）。

**豁免（`releaseCheckedFrames` lane 循环内）**：`result.Blocked && !state.terminalSeen && issuesAllToolJSONInvalid(result.Issues)` ⇒ 不产策略判决，`continue` 放行（扣着的帧原样释放，客户端随后收到 executor 的上游错误终帧），同时：
- WARN 日志 `released truncated tool lane on interrupted stream`（带 request/lane/label/lane_bytes，可审计）；
- 指标 `OutputGateDecisionsTotal{output_compliance, allow, stream_lane}` +1（与既有 allow 口径同族，可区分）。

**豁免面刻意收窄（三条边界）**：
1. 只豁免「唯一判决就是 `tool_json_invalid`」的 lane（`issuesAllToolJSONInvalid` 逐条判定 Subtype）——真实内容规则命中（凭据/外传目的地，含对截断但已可判定的内容）在断流路径**照拦**；
2. 终帧路径（`terminalSeen=true`）**不豁免**——完整收尾的流发非法参数仍是上游格式违规（rv-2089 合同不动）；
3. 脱敏改写流不受影响（Blocked 结果无 RedactedOutput，不走该分支）。

**协议无关**：豁免在 lane 合并层，chat（`chat.tool.N.arguments`）/ anthropic（`.partial_json`）/ responses（`.arguments`）三种 lane 形态一并覆盖。

## 四、验证

### 4.1 回归钉测（`truncated_tool_lane_interruption_test.go`，3 条契约）

| 用例 | 形态 | 期望 | 结果 |
| --- | --- | --- | --- |
| `TestInterruptedStreamTruncatedToolLaneReleasesNotBlocks` | 生产事件复现：`content_block_start(tool_use Bash, index 1)` + 一帧 `partial_json: {"`，**无终帧** → `FlushStreamPending` | 无 PolicyBlockError，帧原样释放（日志 lane/label/lane_bytes 与生产逐字同形） | ✅ |
| `TestCompletedStreamMalformedToolJSONStillBlocks` | 同上截断前缀 + `message_stop` 终帧 | `ShouldBlock` + `policy_rule=builtin.guard.tool_json_invalid`（rv-2089 合同不松动） | ✅ |
| `TestInterruptedStreamRealRuleHitStillBlocks` | 断流 + **合法 JSON** 参数含真实规则命中 | `FlushStreamPending` 返回 PolicyBlockError（rule=test.hard_block）——豁免不扩大到内容规则 | ✅ |

（stub checker 逐款复刻 sanitize 侧 `CheckField` 合同：`__tool_fragment` 立即合规、`__tool_json@*` 非法 ⇒ 单条 `tool_json_invalid` issue——生产判决的唯一来源就是那份代码；builtin Checker 无 CheckField，不产该判决。）

### 4.2 变异验证（3/3 红 → 还原绿）

| 变异 | 摘掉什么 | 应红 | 结果 |
| --- | --- | --- | --- |
| M1 | 整个豁免分支（条件置 false） | 用例 1 | ✅ 红 |
| M2 | `!state.terminalSeen` 条件（豁免扩大到正常收尾） | 用例 2 | ✅ 红 |
| M3 | `issuesAllToolJSONInvalid` 限定（豁免扩大到所有阻断） | 用例 3 | ✅ 红 |

还原后全绿。

### 4.3 门禁

- `go build ./...` ✅（含 admin/gateway 全面）
- `go vet ./domains/hooks/outputcompliance/ ./errorsx/` ✅
- `go test ./domains/hooks/outputcompliance/`（全包）✅
- `go test ./domains/streaming/`（写者 finish 路径消费方，106s）✅ / `./security/sanitize/` ✅ / `./errorsx/` ✅
- `gofmt -l` 经手两文件干净（包内 `guard_failure_attribution_test.go` 的 gofmt 欠账为 origin/main 既有，非本轮引入，不动）

## 五、登记

- **L1**：`response.failed` / `response.incomplete`（Responses 协议的**失败终帧**）按现判据属「终帧」⇒ 终态 release 不走截断豁免——若上游以 `response.failed` 收尾且参数被截，仍会判 `tool_json_invalid`。本轮无生产证据，未扩豁免（要动需区分「成功终帧/失败终帧」，属 Responses 列车）。
- **L2**：断流路径上「修补后再判」（`tryRepairToolJSON` 成功的截断前缀会按**修补猜测的内容**走结构检查）——理论上可能对「上游没发完的内容」产真实规则拦截。本轮保留（拦的是真实到达的字节，方向安全）；若未来出现该形态误报再收。
- **L3（上游侧欠账，非本仓）**：apiclaude.cc 对 glm-5.3-flash 的流式转发在工具参数中途断流（本事件真因）。网关侧已正确分类 `upstream_down`；修复后客户端将看到真实错误分类与 retryable 语义。

## 六、通用式

1. **「拼接文本非法」要先问「拼接是否完整」**——截断前缀和非法完整体在 `json.Valid` 那里同形，但处置方向相反；判「格式违规」前必须有「传输完整」的信号。
2. **fail-closed 守卫的判决会顶掉真实错误的客户端可见面**——拦截终帧一旦渲染，executor 已分类好的错误就被丢弃；给守卫加判决前先问「这个判决呈给用户时，用户会被引向哪条排查线」。
3. **扣帧型守卫在异常收尾时的语义要单独定义**——「没等到终帧」本身就是信息（流被掐断），不是「终态内容到了但内容坏」。
