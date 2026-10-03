# 195 号 · R89-DE —— 「双侧收口」其实有**三份**枚举：输出合规 lane 被漏掉，模型写进 `tool_search_call` / `mcp_approval_request` 的敏感内容**直过两道输出闸**（已根修 + 钉测）

> **日期**：2026-10-03
> **轮次**：R89-DE（第 95 轮，审计第 195 号）
> **类型**：**扩围 `e0625c6a5` 的「双侧」表述**（同族第三份枚举漏收口）+ 对账方法的族级收口
> **改动生产代码**：`domains/hooks/outputcompliance/protocol_text.go`（2 处 case）
> **改动测试**：新增 `domains/hooks/outputcompliance/edge_tool_carrier_lane_test.go`
> **顺带修**：`stream_compliance.go` import 顺序（gofmt 脏，**HEAD 既有**，非本轮引入）
> **改动数据库**：无
> **上一轮**：194 号（分区边界污染三形态，10 表 38 分区）

---

## 〇、起手：基线与方法

**基线纠错（先 fetch 再核，照 194 号 §5 的纪律做）**：

```
git fetch origin main
git rev-list --left-right --count HEAD...origin/main   →  0  818
```

本地 `main` 落后 origin/main **818 个提交**（并发会话在 2026-09-30 → 10-03 期间高频推送），
工作树干净、无冲突，`git merge origin/main` 为**快进**，收于 `94b1a836f`。
合并后 `go build ./...` **exit 0** ⇒ **818 个提交零功能回归**（与 194 号同结论，但窗口更大）。

**审计范围**：194 号（`20fd86690`）之后的 **335 个提交**。全量逐个读不现实，
按 objective 的热点排序挑了 4 个提交 + 1 条自查线：

| 提交 | 落点 | 审计者 |
|---|---|---|
| `3483152cb` | 脱敏绕过收口 + cache lineage（outputcompliance / threetier / session_cache / cache_v2） | 子代理 A |
| `e0625c6a5` | sanitize 边缘载体「双侧收口」 | 子代理 B |
| `013411cd1` + `f215e22d0` | 调度租约 score + 流式预算 fail-open | 子代理 C |
| （自查线） | 1M handoff 契约、上下文超长重试闭环、请求侧 handoff 落库 | 主代理 |

**方法纪律**：3 个子代理全部**只读**（禁改文件、禁 git 写操作），主代理对每条论断做
**代码 + 可达性双证**再采纳；本轮**证伪 13 条**（含我自己差点发出的 3 条），
详见 §四。**本轮只改 1 处生产代码**——因为另外两个子代理的多数「发现」经复核是设计或已闭合。

---

## 一、🔴 F1：`e0625c6a5` 的「双侧」是**第三份**枚举，当前只收了两份

### 1.1 缺陷坐实

提交标题写「**双侧**收口」，实际改了两个文件：

```
security/sanitize/input_protocols.go   +83   ← 入向 sanitize（洗）
security/sanitize/native_restore.go   +28   ← 出向 restore（还原）
```

两个文件都新增了同一对载体：

- `security/sanitize/input_protocols.go:297`
  `case "tool_search_call", "mcp_approval_request":` → `field = "arguments"`
- `security/sanitize/native_restore.go:188,196`
  分别为 `restoreNativeArgumentField(..., requireJSON=true / false)`

⇒ **提交自身是对的**。问题在于：模型可见的文本载体，在本仓一共有**三份**枚举，
提交只改了其中两份。

**第三份 = 输出合规 lane**（`domains/hooks/outputcompliance/protocol_text.go`），
两处 switch **都没有**这两个 type：

| 枚举 | 位置 | 有 `tool_search_call` / `mcp_approval_request`？ |
|---|---|---|
| ① 入向 sanitize | `security/sanitize/input_protocols.go:290-330` | ✅ `:297` |
| ② 出向 restore | `security/sanitize/native_restore.go:168-203` | ✅ `:188` `:196` |
| ③ **输出合规 lane（非流）** | `outputcompliance/protocol_text.go:230-272` `addOutputItem` | ❌ |
| ③' **输出合规 lane（流式 added 帧）** | `outputcompliance/protocol_text.go:388-438` | ❌ |

复现（无输出即缺口）：

```bash
git grep -n "tool_search_call\|mcp_approval_request" -- "domains/hooks/outputcompliance/*.go" ":!*_test.go"
# 修复前：0 命中
```

### 1.2 后果（为什么这条不是「文档没跟上」）

输出合规 lane 的作用是**输出检测**：模型自己新造敏感内容时由它拦。
`protocol_text.go:240-245` 的注释把这个 switch 的存在理由写得很直白：

> 此前只认 message/function_call/custom_tool_call/reasoning 四型：模型新造敏感内容写进
> 工具/检索载体 lane（restore 无 marker 不动）会直过两道输出闸。

⇒ **这两类载体是 `3483152cb` 刚扩容进去的**（`mcp_call` / `web_search_call` / …），
`e0625c6a5` 又新增了两类却没跟这份枚举 ⇒ **模型把手机号/凭据写进
`tool_search_call.arguments` 或 `mcp_approval_request.arguments` 时，两道输出闸都看不见它。**

### 1.3 负控（判据承重，先跑再改）

新写的钉测在**未修复**代码上先跑：

```
go test ./domains/hooks/outputcompliance/ -run 'EdgeToolCarrier' -count=1
--- FAIL: TestProtocolTextCoversEdgeToolCarrierLanes
    edge tool-carrier lane not inspected at all (observe-only): &{ShouldBlock:false ModifiedBody:[] ...}
--- FAIL: TestStreamCollectCoversEdgeToolCarrierItemFrames/added   issues=0
--- FAIL: TestStreamCollectCoversEdgeToolCarrierItemFrames/done     issues=0
FAIL
```

⚠️ **`ModifiedBody:[]` 是这轮最硬的一句证据**：不是「拦得不够狠」，是
**一个字节都没被检查**（observe-only）⇒ 判据不是恒暗的假红，它真的在测这个性质。
判据取「敏感值确实被改写」而不是「lane 存在」——后者只要加一个空 case 就能过。

### 1.4 根修

两处各加一个 case，用 `addToolInput` 而非 `add`：

```go
case "tool_search_call", "mcp_approval_request":
    addToolInput(item, "arguments", itemLane+".arguments")   // 非流式 addOutputItem
    addToolInput(item, "arguments", initialLane+".arguments") // 流式 output_item.added
```

两个细节是查过才敢这么写的，不是照抄 `mcp_call`：

1. **lane 后缀必须是 `.arguments` 且在 `responses.` 前缀下** ——
   `stream_compliance.go:232-235` 的 `isJSONToolArgumentLane` 靠
   `HasPrefix(lane,"responses.") && HasSuffix(lane,".arguments")` 判定，
   命中才会拿到 `__tool_json` 标签（mandatory 闸的阻断语义）。写成别的后缀会静默降级成普通文本。
2. **`.done` 帧不需要单独 case** —— `protocol_text.go:376-382` 的
   `response.output_item.done` 直接委托 `addOutputItem`，所以两处 case 即覆盖 added+done，
   **不会重复检查**。

`addToolInput` 对字符串形态与 `add` 完全等价（`protocol_text.go:114-122` 先调 `add`），
额外覆盖结构化形态 ⇒ 严格增量的超集，不是行为变更。

### 1.5 复验

```
gofmt -l domains/hooks/outputcompliance/            →  净
go vet ./domains/hooks/outputcompliance/            →  exit 0
go test ./domains/hooks/outputcompliance/ -run EdgeToolCarrier -count=1  →  ok
go test ./domains/hooks/outputcompliance/ ./security/sanitize/ -count=1  →  ok / ok
go build ./domains/...                              →  exit 0
```

---

## 二、族级收口：18 类载体全对账，以及**判别准则**

F1 修完后顺手把整族对了一遍（`193/194 号 §85/§86` 的纪律：查一张表时问「同批还有哪些」）：

| 入向 sanitize 载体（`input_protocols.go:290-330`） | 输出合规 lane |
|---|---|
| `function_call` / `custom_tool_call` / `mcp_call` / `web_search_call` / `file_search_call` / `local_shell_call_output` / `apply_patch_call_output` / `shell_call_output` / `code_interpreter_call` / `apply_patch_call` / `shell_call` / `local_shell_call` / `computer_call` / `message` / `reasoning` | ✅ 全有 |
| `tool_search_call` / `mcp_approval_request` | ❌ **本轮补上** |
| `function_call_output` / `custom_tool_call_output` | ❌ **但这是对的** |

⇒ **输出 lane 缺席的只剩 `*_call_output` 两类，而它们在 Responses 协议里只出现在
请求侧**（客户端把工具结果回传给模型），模型不会产出 ⇒ **对称缺席是设计，不是缺口。**

### ⇒ playbook **§87（本轮新增）**

> **枚举收口的判别准则：不对称才是缺口，对称缺席是设计。**
> 收口一个载体时，**先把所有并列的枚举列全**（本例 4 处：入向 sanitize / 出向 restore /
> 输出合规非流 / 输出合规流），再逐个对账。判据：
> - **A 有 B 无**（且 A、B 处理的是同一批载体）⇒ **缺口**，补 B；
> - **A、B 都无** ⇒ 先问「这个载体在协议上会不会出现在这一侧」。
>   只会由客户端回传、模型不产出的载体（`function_call_output` 等）**缺席是正确的**，
>   补上去只会白增检查面。
>
> **「双侧」是个危险的措辞**：它默认了「有两侧」。本例是**四份枚举**，
> 提交写「双侧收口」、评审读「双侧收口」、两侧都真的改了 ⇒ **所有人都合理地漏掉了第三份**。
> ⇒ 收口类提交的标题与正文必须**枚举份数**（「四侧收口」而不是「双侧收口」），
> 或者在正文贴出对账表。**这不是措辞洁癖：措辞决定了评审者检查几处。**

---

## 三、🟡 F2：流式 restore 的事件枚举缺 `output_item.*`（**登记不修**，需外部证据）

`security/sanitize/smart_sani_guard.go:1289-1313` 的 `restoreStreamResponsesDelta`
只认 **12 个文本类事件**（`response.output_text.delta` / `function_call_arguments.delta` / …），
**不认** `response.output_item.added` / `.done` ⇒ 那些载体（`mcp_call`、
`web_search_call`、`code_interpreter_call`、以及本轮补的两类）的 `arguments` / `action`
若带占位符，流式路径**不还原**。

**但方向是 fail-closed，不是泄露**（已亲自复核 `security/sanitize/sse_restore.go:92-101`）：

```go
if !changed {
    if containsReservedJSONToken(payload) {
        return nil, false, true      // ← blocked：整帧拦下
    }
```

⇒ 风险形态是**可用性**（客户端收到阻断），不是敏感信息外泄。
且 `function_call` 走的是专用事件 `response.function_call_arguments.delta/done`（`:1293,:1297`），
**已被覆盖** ⇒ 受影响面是「没有专用参数事件的那几类载体」。

**为什么不修**：要往这个 switch 里加事件名，必须先知道上游 Responses SSE **实际**
为这些载体发什么事件。凭协议文档或推测加名 = 把一个 fail-closed 的可用性缺口
换成一个可能错误的分支。⇒ **登记为待裁决 80（P2），需抓一次真实 SSE 帧序列再定。**

---

## 四、证伪清单（**本轮 13 条**，与发现同等重要）

### 我自己差点发出的（3 条）

| # | 本来以为 | 查了什么 | 结论 |
|---|---|---|---|
| D1 | `trigger_mode=manual` 时 handoff 永不触发（`evaluate()` 在 `:316` 直接 return nil，而 `:313-315` 注释声称「由 InterceptNonStream 的 body parsing 处理」，那里根本没有 body 解析） | 全仓找 manual 路径 → `request_hook.go:93-99` `hasSkillInvocation(req.Body, …)`，生产调用方 `domains/streaming/handler.go:3827` | ❌ **撤回**。功能在**请求侧** `PrepareRequest` 实现，注释只是**指错了文件**。manual 与 hybrid 都活着 |
| D2 | 请求侧 handoff 不落库 ⇒ `max_per_session` / `cooldown_seconds` 两个守卫读到的是永不增长的状态 ⇒ 无限 handoff | `PGStore.Confirm`（`confirmation_pg.go:113-154`）在同一事务里 `SELECT … FOR UPDATE` 读计数 + `INSERT INTO handoff_logs_hot` + `UPDATE session_summaries SET handoff_count=…+1`，而 `ConfirmRequest`（`confirmation_hook.go:81`）走的就是它 | ❌ **撤回**。守卫的状态是原子写的 |
| D3 | objective 要求「>1M 会话默认发 handoff」未实现（出厂阈值看着是 300K 不是 1M） | `handoff_1m_contract_test.go` 四条钉测：1M 必触发 / 抬到 1.5M 必不触发 / 边界含等号 / 0=禁用；`goal_control.go:323` 出厂 300K ⇒ **1M 必然触发** | ❌ **撤回**。300K 比要求更保守，契约已被钉死 |

### 子代理 A 撤回的（5 条，摘要）

- **守卫不可达** → `smart_sani_guard.go:280` 是真实生产写入方，且在 `if generation != ""` **之外**（无脱敏的请求也装守卫）。撤回。
- **`tool_calls` / `tool_use` 只洗不还原** → `native_restore.go` 有 `block["input"] → restoreNativeNestedValue` 覆盖对象形态。撤回。
- **`ParsePlaceholder` 收紧为 `m[0] != s` 会打断还原** → 8 个非测试调用点全部传精确 token，无一依赖子串语义。撤回。
- **非流式响应绕过拦截器链（则新装的 mandatory 输出守卫是死代码）** → `handler.go:5684` 与 `native_response_intercept.go:40` 都调 `InterceptNonStream`。撤回。
- **`chain.go:208` 提前 return 丢帧/重帧** → 逐层 trace 两级 holder，无重复无丢失。撤回。

### 子代理 C 撤回的（4 条，摘要）

- **renew 引入第二份会漂移的 score 副本** → score 唯一存放处是 Redis ZSET，Go 侧 map 只存 token。撤回。
- **`ttl=0` 让剪除 cutoff 退化为「全量放行」** → `redis_backend.go:279-282` 兜底 30s。撤回。
- **预算 fail-closed 会中断在途客户端请求** → 两处都是**路由前预检**（`handler.go:2384` 在 body/session 解析之前），503 是对尚未开始的请求的拒绝。**符合 objective 的「不中断」要求**。撤回。
- **两提交重复扣减配额** → 预算是只读 SUM 预检（无扣减），dispatch 是 ZSET 租约，作用于不同资源。撤回。

---

## 五、登记不修（含定级理由）

| 编号 | 级别 | 内容 | 为什么不修 |
|---|---|---|---|
| **待裁决 79** | P2 | **预算闸门只覆盖 2 个面**：全仓非测试 `CheckBudget(` 仅 `handler.go:2386`（chat）与 `embeddings.go:399`；而 `messages.go:72` / `responses.go:105` / `handler_gemini.go:281` 各自 `ServeHTTP`，**不进 `serveWithExecutor`** ⇒ 这三个面**根本没有闸门**（不是 fail-open，是无闸门）。子代理 C F1，已坐实 | 既存缺陷、非 48h 引入；补闸门会**改变这三个面的现行行为**（现在超预算也能过），属产品决策不是审计能定的 |
| **待裁决 80** | P2 | 流式 restore 事件枚举缺 `output_item.*`（本文 §三） | 需真实 SSE 帧序列；盲加事件名会把 fail-closed 换成可能错误的分支 |
| 待裁决 81 | P2 | `3483152cb` 把 `server_ip` / `Bearer` / 连接串加进默认规则集，而 `session_compressor.go:361 cachedBodyPassesGuard` 在**任一字符串**判敏时把整个 L1/L2 状态清空 ⇒ **旧策略下写入的缓存体在新二进制上必然大面积失配**，会话退化为每轮全量重算。子代理 A F2，代码路径已坐实 | 线上发生量需真库取数（本机无 PG）。**这条的修法（放宽 guard 或加策略版本号）会改缓存语义**，需先有量级证据 |
| P3 | P3 | `redis_backend.go:75` 同一 ZSET 混两种 score 粒度（acquire 秒截断 / renew 全毫秒）。方向安全（`cutoff ≤ now_ms - ttl`），未续约租约寿命 `[TTL, TTL+1s)`。子代理 C F4 | 生产 TTL=30s 时误差 <3.3%；统一粒度是行为变更，留待与 F5 滚动升级窗口一并处理 |
| P3 | P3 | 子代理 C F3：`redis_backend_integration_test.go:238-241` 的 3s 前置守卫在 sleep+Renew 内被打断 >3s 时**先** `t.Fatalf`，而此时秒边界其实已跨过（循环条件尚未求值）⇒ 重负载 CI 可假红。**判别力本身成立** | 只改判定顺序，不改被测逻辑；本轮预算留给 F1 |
| P3（死代码，**勿接回**） | — | `domains/hooks/handoff/request_hook.go:204 CommitRequest` **零生产调用方**（`git grep "CommitRequest(" -- "*.go" ":!*_test.go"` 只命中定义），已被 `Confirm` 流程取代。⚠️ **它的语义与 `Confirm` 相反**：`CommitRequest` 无条件 `RecordHandoff`，而 `Confirm` 是「`SELECT … FOR UPDATE` 校验 max_per_session/cooldown → 记账」的 fail-closed 事务 ⇒ **谁把它接回去，就等于给 `max_per_session` / `cooldown` 开一个绕过侧门** | 按 objective「冗余实现标注整理」登记；接回是行为变更，须与 §四 D2 一起裁决 |
| 需求缺口 | P3 | objective 写「使用 `/new` 自动进行新会话切换」，实现是注入 `/handoff`（`trigger_hook.go:568`），全仓**无 `/new` 命令实现**。功能等价物存在（`new_session_id` + `resume_packet` + 202 + 确认令牌） | 是**需求措辞与实现的差异**，不是缺陷；改注入文本会影响客户端 ⇒ 需产品确认 |

---

## 六、验证命令与结果（可重放）

```bash
git fetch origin main
git rev-list --left-right --count HEAD...origin/main     # 0  818
git merge origin main                                     # 快进 → 94b1a836f
go build ./...                                            # exit 0   ← 818 提交零回归

# F1 负控（未修复代码）
go test ./domains/hooks/outputcompliance/ -run 'EdgeToolCarrier' -count=1   # FAIL ×3
# F1 根修后
gofmt -l domains/hooks/outputcompliance/                                  # 净
go vet ./domains/hooks/outputcompliance/                                  # exit 0
go test ./domains/hooks/outputcompliance/ -run EdgeToolCarrier -count=1   # ok
go test ./domains/hooks/outputcompliance/ ./security/sanitize/ -count=1   # ok / ok
go build ./domains/...                                                     # exit 0

# F1 缺口复现（修复前 0 命中）
git grep -n "tool_search_call\|mcp_approval_request" -- \
  "domains/hooks/outputcompliance/*.go" ":!*_test.go"
```

**未在本机执行**（诚实登记，不假装覆盖）：
- 无 Redis / 无 PG / 无 Docker ⇒ `domains/dispatch` 的 Redis 集成钉测、子代理 C 的
  F3/F5 属静态推演；`security/sanitize` 的 testcontainers 迁移测试未跑。
- `go test ./...` 全量未跑（本机 Windows，依赖外部服务的套件必红，与本轮零关联）。
- 未起真进程 / 未重建镜像 ⇒ **本轮根修未做部署级验证**（`protocol_text.go` 的改动是
  纯枚举扩容，风险面窄，但仍是**未验证**而非**已验证**）。

---

## 七、下一轮顺位

1. **`SanitizedMessageRefs` ↔ `AlignmentMap` 在压缩后的一一对应**（子代理 A 明确未查完，
   本轮我也只看了快照基线那一半）：`session_compressor.go` 里生成 `AlignmentMap` /
   `MsgHashes` 的函数 + `threetier/align.go` 全文。**objective 点名的「三层 provenance 的
   完整 identity/occurrence 映射」正落在这里**，已挂三轮。
2. **待裁决 81 的量级取数**：真库上比一下 `cachedBodyPassesGuard` 失配率（缓存体被整段丢弃
   的比例），有量级再谈修法。
3. **待裁决 79 的产品裁决**：messages / responses / gemini 三个面要不要补预算闸。
4. **待裁决 80**：抓一次上游 Responses SSE 的 `output_item.*` 帧序列。
5. **提交态测试**：子代理 A 在 HEAD 而非 `3483152cb` 提交态跑的测试（只读约束无法 checkout），
   若要严格结论需在独立 worktree 里 checkout 该提交复跑。

---

## 八、playbook 新增

- **§87 枚举收口的判别准则：不对称才是缺口，对称缺席是设计**（详见 §二）。
  附带一条关于**提交标题措辞决定评审覆盖面**的教训。
- **§88 负控必须先在未修复代码上跑**（本轮 F1 严格执行）：
  新写的判据**先在缺陷还在的代码上红一次**，才允许改生产代码。
  判据取「可观测后果」（敏感值被改写 / `ModifiedBody` 非空 / `issues>0`），
  **不取「结构存在」**（加个空 case 就能过）。本轮若先改代码再补测试，
  就无法区分「新判据抓到了真缺陷」与「新判据迎合了新代码」。
