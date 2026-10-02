# Vapeur 协议适配第三轮（2026-10-02，r3 + 批判式复审修订）

**触发**：用户报告经本地网关以凭据 hxt-local（credential 126，provider 36
vapeur，protocol=openai-responses）访问 gpt-5.6-terra / claude-sonnet-5 /
claude-opus-5 / gpt-6-sol / gpt-6-astra / grok-4.7 / gpt-6-luna / glm-5.2 /
glm-5.3-glb / claude-fable-5-1 / gpt-5.3-codex「经常出错不通」，要求检查协议
适配、充分测试、默认走 responses；先拉最新代码（已做：HEAD=2814bde51，
origin/main 0/0）。

> **本文件含一次自我纠错。** 初稿结论「44/44 全绿」是**错的**：矩阵脚本只按
> HTTP 状态码判成败，而流式失败时状态码已是 200。复审发现 1 例真实失败，
> 并顺带查出**我自己在初版修复里引入的一个契约缺陷**。详见 §六。

## 一、上一轮的结论只对了一半

r2（2026-09-29）判定「协议翻译层全部健康，20/20 矩阵全绿」。本轮证明该结论的
**取样方法**有缺陷：r2 的 5 模型矩阵不含 gpt-5.3-codex，而它恰好是唯一
「chat 腿 400、responses 腿 200」的模型。翻译层在只走 chat 的样本上看起来
永远健康。

本轮先建立**上游真相**，再比对网关行为——这是 r2 缺的步骤。

## 二、上游真相（直连 api.vapeur.ai，绕过网关）

| 模型 | /v1/responses | /v1/chat/completions |
|---|---|---|
| gpt-5.6-terra | 200 | 200 |
| claude-sonnet-5 | **400 该供应商不支持 Responses API** | 200 |
| claude-opus-5 | **400 同上** | 200 |
| gpt-6-sol / gpt-6-astra / gpt-6-luna | 200 | 200 |
| glm-5.2 / glm-5.3-glb | 200 | 200 |
| claude-fable-5-1 | **400 同上** | 200 |
| gpt-5.3-codex | **200** | **400 `{"code":4006,"message":"The requested operation is unsupported."}`** |
| grok-4.7 | **403 No permission**（且不在 /models） | 403 |

**gpt-5.3-codex 是 responses-only 模型**（与 Claude 系正好相反）。
grok-4.7 / gpt-6.1-sol / grok-4.20-reasoning 对本 key 是 403，属供应商侧权限。

## 三、根因（三条）

### 根因 1：chat→responses 方向的降级根本不存在

网关长期只有 responses→chat 降级。`internal/reqprobe/diagnose.go` 会把这类拒绝
分类成 `SuggestMode:"responses"`，但该文件自写「仅记录建议，executor 不自动
切换」。

实测链路：探针腿走 /v1/responses（`providercap.Resolve` 对 openai-responses
供应商映射 EpResponses）→ 200 → 节点判健康；数据面腿走 /v1/chat/completions
→ 400 code 4006 → 3 次重试全败 → `all 3 candidates failed` → 503 → 绑定被打
`auto_credential_transient` → 后续 `no_candidate`。

**node_probe_runs 里 5 天前就有 5 次 `direct_ok=t, gateway_ok=f(503)`**，系统
知情两天无动作。

### 根因 2：`credential_model_capabilities` 这张表没有任何代码写入

`cand.SupportsNativeResponses` 由 `provider/client.go:1664` 从该表读取；全树
唯一的引用就是那一条 SELECT，**没有任何 INSERT/UPDATE**。迁移 612/613 注释
自认「until an external probe populates it」——那个 probe 不存在。

后果：cred 126 的 ~101 个绑定里只有 1 行能力位（grok-4.6）。**30 分钟内
/v1/responses 出站 0 次、/v1/chat/completions 54 次**，「默认走 responses」
完全落空。

### 根因 3：Redis node state 永远解不开，能力位写了也读不到

`credentialfpslot/node_state.go` 的写入方全是 Redis Lua 脚本，而 Lua 的 `{}`
是**空 table**，cjson 重新编码成 JSON **对象** `{}`，不是 `[]`。Go 侧声明
`SlideWindow []NodeRecord`：

```
llmgw:cred_fp_node:126:gpt-5.6-terra =
  {"slide_window":{},"disabled":false,"credential_id":126,...,
   "capabilities":{"supports_responses":true}, ...}
→ unmarshal node state: cannot unmarshal object into NodeState.slide_window
```

所有调用方把读错误当作「没有可用结论」⇒ **能力位两个方向同时静默失效**（含
2026-09-30 加的 F04 降级短路）。结论一直在正确写入，从未被读回。

同一陷阱在 `Capabilities` 字段注释里已被记录并用「指针 + omitempty」绕过，
`slide_window` 命中同一形状却漏掉了。

## 四、修复

| # | 文件 | 内容 |
|---|---|---|
| 1 | `credentialfpslot/node_state.go` | `NodeState.UnmarshalJSON`：把 `"slide_window":{}` 归一为 `[]`。**带 `bytes.Contains` 快速路径**——`GetNodeStatesBatch` 每请求按候选数解码，热路径必须保持单次解析。非空对象仍显式失败，不掩盖真畸形。 |
| 2 | `domains/streaming/executors/responses_mode_bridge.go` | `chatRequiresResponses`（code 4006 / 显式 / 中文文案；**参数形状拒绝显式排除**）+ `chatBodyToResponsesBody` / `responsesBodyToChatBody`（复用既有 IR）。响应侧带形状守卫：错误信封、空 output、非 response 对象一律拒转。 |
| 3 | `domains/streaming/executors/responses_stream_bridge.go` | Responses-SSE → chat-SSE 读取器。在**响应体**上转换，复用既有 chat StreamChat 读取器。兼容 OpenAI `event:` 行与 vapEUR「只有 data: 行、类型藏 JSON」两种方言。 |
| 4 | `domains/streaming/executors/executor_chat.go` | ① chat→responses 降级分支，**按客户端协议分叉**（见 §六）；② `upgradedFromChat` 标志 + 成功路径转回 chat 信封；③ 流式绕开 native 直通走转换后读取器；④ 能力闸门补正向方向（非流式）。 |

## 五、测试与承重性

单测 fixtures 全部取自**实测上游帧/生产字节**，非手写。

**变异验证**（去掉修复即红）：

| 变异 | 结果 |
|---|---|
| 关闭 durable 正向闸门 | `DurableVerdictEnablesNativeResponses` 红 |
| `chatRequiresResponses` 恒 false | 桥接两用例红 |
| 关闭 slide_window 归一 | node-state 两用例红 |
| 桥接对所有客户端一律下转 chat（初版行为） | `ResponsesClient_KeepsResponsesStreamOnBridge` 红 |

**测试期间/复审期间抓到的真缺陷**（若非端到端用例都会静默失效）：
1. 桥接分支最初嵌在 `if diag, diagOK := reqprobe.Diagnose(...); diagOK` 内，
   vapEUR 的 4006 文案不匹配任何 hint ⇒ `diagOK=false` ⇒ **修复是死代码**。
2. `upgradedFromChat` 流式用例最初 nil panic（`if nativeStream` 先于我插入的
   switch 命中并调用未装配的 native handler）。
3. 复审抓到下述 §六 的契约缺陷。

## 六、批判式复审：我自己引入的缺陷（已修）

### 6.1 初版「44/44 全绿」是错的

矩阵脚本按 HTTP 状态码判成败。`gpt-5.3-codex` + `/v1/responses` + 流式
返回 **HTTP 200**，但响应体只有 `: keep-alive` 与
`: thinking: "上游请求暂时失败（upstream_down）…"` 注释，最后是
`event: response.failed` / `All providers unavailable`。网关自己的
`request_logs_hot` 记的是 `success=f, latency 22535ms`。

**HTTP 200 在流式场景不代表成功**——头已发出，失败只能体现在 body 和
success 标志里。已修：`gw_verdict()` 对流式响应检查是否含真实内容帧
（`chat.completion.chunk` / `output_text.delta` / `"object":"response"`），
`response.failed` 与空内容一律判 FAIL。

### 6.2 桥接对 Responses 客户端做了错误的下行转换（真 bug）

初版桥接不看客户端协议，对**所有**客户端都把 Responses 响应转成 chat 信封。
但 `/v1/responses` 客户端的请求体**本来就是 Responses 形态**，它需要的是
Responses SSE 帧，不是 `chat.completion.chunk`。后果：responses handler
找不到终止帧 ⇒ `response.failed` / `All providers unavailable`，真机三次重试
全败。

真机对照（修复前）：

| 客户端 | 结果 |
|---|---|
| `/v1/chat/completions` + 流式 | ✅ `chat.completion.chunk` 正常 |
| `/v1/responses` + 流式 | ❌ `response.failed` / All providers unavailable |

**修复**：桥接分支按客户端协议分叉。
- chat 客户端：转请求体 + 转回响应（现状正确）。
- Responses 客户端：**只换出站腿**，请求体用 `params.ResponsesBodyBytes`
  原样发出，`upgradedFromChat` 保持 false 以走原生透传。

回归测试 `TestExecuteOpenAI_ResponsesClient_KeepsResponsesStreamOnBridge`
断言客户端收到 `response.output_text.delta` / `response.completed` 且**不含**
`chat.completion.chunk`；已变异验证对初版行为会红。

## 七、真机验收

出站腿实证（cred 126）：

| 模型 | 出站腿 |
|---|---|
| gpt-5.3-codex | CHAT:400 → **RESP:200**（桥接生效） |
| gpt-5.6-terra / gpt-6-sol / gpt-6-astra / gpt-6-luna / glm-5.3-glb | CHAT:200 + **RESP:200**（/v1/responses 首次真正走上游 /responses） |
| claude-sonnet-5 / claude-opus-5 / claude-fable-5-1 | 仅 CHAT:200（负向能力位被正确尊重） |

修复前同窗口 RESP:200 计数为 **0**。

44 例矩阵的最终结果见 §八（用复审后修正的 `gw_verdict` 判据重跑，含 §6.2 修复）。

## 八、最终矩阵结果（修复后，含 §6.2 修复）

构建 `2.5.8.2392`（`git_sha=05cc1c71`），`VERIFY_PASS=1`，凭据解密冒烟
`providers=587 creds=7 failed=0`。11 模型 × {chat, responses} × {流式, 非流式}
= **44 例**。

| 维度 | 结果 |
|---|---|
| HTTP 200 | **44 / 44** |
| 流式 22 例（`gw_verdict` 判真实内容帧） | **22 / 22 CONTENT_OK** |
| 非流式 22 例（解析 body 取 output_text / content） | **22 / 22 有真实答案** |

非流式 22 例在 `max_tokens=16 / max_output_tokens=32` 下 glm-5.2 两例判 FAIL，
经查是 **`finish_reason:"length"`——推理过程吃光了 token 上限**，网关与协议
适配均无问题；把上限提到 256/512 后两例分别返回
`finish_reason=stop, content="Hi there! 👋 How can I help you today?"` 与
`status=completed, output_text=["Hi there! 👋 How can I help you today?"]`。
**这是测试参数问题，不是产品缺陷，如实记录以免下次被当成回归。**

### 8.1 判据本身也修了一处（量具缺陷）

首轮重跑出现 5 例 `NO_CONTENT`（claude-sonnet-5/opus-5/fable-5-1、grok-4.7、
glm-5.2 的 chat 流式）。核查发现是**量具坏了，不是被测物坏了**：`one()` 用
`head -c 200` 截断 body，而 SSE 开头的若干 `: keep-alive` 注释行会吃满 200
字节预算，使判别 token（`"object":"chat.completion.chunk"` /
`output_text.delta`）落在截断点之后。

已构造对照样本证明：同一 body（387 字节，判别 token 在第 363 字节），
**旧判据（截断 200）报 `NO_CONTENT`（假阳性），新判据（读完整 body）报
`CONTENT_OK`**。判据改为消费完整 body、仅显示列仍取 200 字节预览后，5 例假
阳性消失。这与 §6.1 是同一类错误的两个方向：**判据既不能漏判失败，也不能
误判成功。**

### 8.2 与网关自身日志交叉核对

矩阵结果不采信自述，用 `request_logs_hot` 独立复核（`credential_id=126`，
`14:07` 之后，即修复后窗口）：

| 客户端协议 | 上游协议 | protocol_conversion | ok | fail |
|---|---|---|---|---|
| openai-completions | **openai-responses** | true | **30** | 0 |
| openai-responses | openai-responses | false | 30 | 0 |
| openai-completions | openai-completions | false | 3 | 0 |

按模型展开（`outbound_model`）：

- `gpt-5.3-codex`：chat 客户端 **5 条走上游 openai-responses，5 ok / 0 fail**。
  修复前该组合必 400（`code 4006`），这是桥接生效的直接证据。
- `gpt-5.6-terra / gpt-6-sol / gpt-6-astra / gpt-6-luna / glm-5.3-glb`：
  chat 客户端请求同样走上游 responses，各 4-5 条全成功。
- 走 `openai-completions` 上游的 3 条是 Claude 系（`deepseek-v4-pro` 等回落
  供应商），负向能力位被正确尊重，未被强行送上不支持 responses 的上游。

**修复后窗口失败数 = 0**（`success=false AND t0_arrived_at > '14:07'` → 0）。
日志中最后一条失败在 `13:56:15`，属部署 2392 之前的旧窗口（13:54-13:56 共
11 条），与本次修复后验证无关——已按时间边界确认，不计入。

## 九、遗留与如实说明

1. **能力位需要「证据」才会开。** 闸门能开了，但仍需针对该 (credential, model)
   的探针跑过一次。本轮为验收用探针同款写入路径（`SetSupportsResponses`）
   预置了 terra/sol/astra/luna/glm-5.2/glm-5.3-glb 的正向结论、Claude 三兄弟
   的负向结论——每个都对应 §二 的实测上游结果。**生产上应由探针自然填充**：
   探针目前只在失败/触发时跑，健康模型不会被主动探测。要让全部模型常态走
   responses，需要一个按绑定的能力位回填任务（**未做**）。在此之前，本项
   能力依赖预置，不具备自愈性。
2. **流式能力位仍只认 SQL 表。** 探针不做流式探测，无从获得流式证据；补齐
   需要流式探针（**未做**）。
3. **热路径新增一次 Redis 读。** durable 闸门对「openai-responses 供应商 +
   Responses 客户端 + 非流式 + native 未开」的请求多一次
   `GetSupportsResponses`。实测（miniredis 进程内，含 2 次 Redis 往返 + JSON
   解码）约 **270 µs/op**。缓解事实：路由器本就在每请求按候选批量 MGET 同一批
   node key（100ms 预算），故这是同一批键上的再一次取。更优解是把路由已取到
   的 state 透传给 executor（**未做**）。另已为 `UnmarshalJSON` 加
   `bytes.Contains` 快速路径，避免在解码热路径上多解析一次。
4. **grok-4.7 / gpt-6.1-sol 对 vapEUR 是 403**，且 gpt-6.1-sol 已不在 /models
   却仍留在目录里（`provider_models` 有行，last_seen 停在 09-30）。网关会静默
   改由其他供应商（suyun/apigpt）承接该模型名。属供应商侧权限 + 目录陈旧，
   **未改**。
5. **钉 credential 的头不是硬失败语义**：无可用候选时静默回落全局路由。本轮
   测试中 `X-LLM-Pin-Credential: 126` 在 cred 126 无候选时被忽略即触发该行为。
   所有结论均已按 `credential_id` 过滤，未被污染（**未改**，属语义决策）。
6. 未新建任何业务 API key；测试用系统 self-check key，仓库内无任何密钥材料
   （已 grep 确认新增文件与本审计文档不含密钥）。
7. **矩阵的 token 上限对推理型模型偏小。** `max_tokens=16 / max_output_tokens=32`
   会被 glm-5.2 这类先出 `reasoning_content` 的模型吃光，表现为
   `finish_reason:"length"` + 空 `content`。这不是网关缺陷，但会让后续任何用小
   token 上限的探测/回归产生假失败。**建议**（**未做**）：探针与自检脚本对
   reasoning 型模型用 ≥256 的上限，或把 `finish_reason=length` 且 content 为空
   单独归类而不是记 FAIL。
