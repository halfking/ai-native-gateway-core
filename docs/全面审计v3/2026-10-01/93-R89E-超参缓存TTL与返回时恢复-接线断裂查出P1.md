# 93-R89-e：超参缓存 + TTL + 返回时恢复 —— 42 号「未实现」结论已过期，实查查出 **P1 接线断裂**

- 轮次：R89-e
- HEAD 基线：`3a0172c1a`
- 触发：`00-审计覆盖台账.md` 待跟进项「【下轮 A】超参缓存+TTL+返回时恢复——42 号覆盖不充分；实现存在与否尚未定」
- 结论：**功能已实现且质量高**（2026-09-22 建的 `internal/paramledger`，比 42 号当时看到的强得多），
  但**非流式还原点在生产中零可达路径**（P1），另有 3 条可达失效路径 + 2 条 P3
- 改动：**零生产代码、零配置、零门**。变异验证的临时改动已 `cmp` 逐字节还原

---

## 0. 先更正 42 号：这条需求**已经实现了**

42 号 §2.2 的结论是「**代码里没有任何缓存，也因此不存在 TTL**」，并把它列为
「⏸ 需需求方澄清」的需求差距。**该结论已过期**：

| 42 号当时看到的 | 现状（HEAD `3a0172c1a`） |
|---|---|
| `Extensions` 是进程内字段，无跨请求存储 | **`internal/paramledger` 专门实现**（`ledger.go` 10,978 B + `redis_mirror.go` 1,164 B，2026-09-22） |
| 无 TTL | **`entryTTL = 15 * time.Minute`**（`ledger.go:72`），Redis 镜像同 TTL |
| 无回程还原 | **`RestoreResponsesEffort`**（`ledger.go:195-225`）还原 Responses 回显的 `reasoning.effort` |
| 无容量上界 | **`maxEntries = 20000`** FIFO 淘汰（`ledger.go:75`、`:115-119`） |

⇒ objective 的「对于超出原厂的参数，缓存下来（注意 TTL 时间），在返回时恢复放到请求中继续」
**三要素全部落地**。42 号之所以判「未实现」，是因为该包在其报告之后才建。

**为什么 42 号的结论当时是对的**：它查的是 `internal/ir` 的 `Extensions` 字段，
而 paramledger 走的是**完全不同的机制**（`paramguard` 出站改写 → 账本 → 回程字节级还原），
两者不在同一条链上。**「文件名里没有」≠「没审过」在此再次应验。**

---

## 1. 【P1】非流式还原点在生产中**零可达路径**

### 1.1 结论

`domains/streaming/executors/executor_chat.go:1763` 的
`e.restoreClientEcho(params, respBody)`（非流式 Responses 回显还原）
在生产的**任何一条路径上都不会执行**。

### 1.2 证明：两个字段在同一字面量里逻辑互补

`domains/streaming/responses.go` 的 `buildExecParams` 闭包返回**同一个**
`&executors.ExecParams{...}` 字面量，其中：

```go
IsStream:           isStream,          // :725
SuppressSuccessWrite: !isStream,       // :750
```

⇒ **构造上恒有 `SuppressSuccessWrite == !IsStream`。**

而 `executor_chat.go:388-390` 的定义要求：

```go
nativeNonStream := cand.Protocol == providercatalog.ProtocolOpenAIResponses &&
    cand.SupportsNativeResponses && !params.IsStream &&
    len(params.ResponsesBodyBytes) > 0
```

⇒ `nativeNonStream == true` ⇒ `IsStream == false` ⇒ `SuppressSuccessWrite == true`。
而还原点被包在**第二层**守卫里（`executor_chat.go:1744-1745`）：

```go
if nativeNonStream {
    if !params.SuppressSuccessWrite && params.W != nil {   // ← 此处恒为 false
        ...
        respBody = e.redactClientResponse(params, e.restoreClientEcho(params, respBody))  // :1763
```

⇒ **`nativeNonStream == true` 与 `!params.SuppressSuccessWrite == true` 在构造上互斥**，
还原点无任何可达路径。**这个证明不依赖 `ResponsesBodyBytes` 的来源分析**——
仅凭 `IsStream` 与 `SuppressSuccessWrite` 的互补性即已闭合。

### 1.3 穷举全部 5 个 `ExecParams` 构造点（排除「还有别的路」）

`grep -rn "executors.ExecParams{\|&ExecParams{" --include=*.go . | grep -v _test.go`：

| # | 构造点 | `IsStream` | `SuppressSuccessWrite` | `ResponsesBodyBytes` | `nativeNonStream` 可能 true？ | 还原可达？ |
|---|---|---|---|---|---|---|
| 1 | `handler.go:4368`（chat 主路） | `isStream` | 未设 → `false` | **未设 → `""`** | ❌ 长度门失败 | ❌ |
| 2 | `handler.go:4941`（chat fallback） | `false` | 未设 → `false` | **未设 → `""`** | ❌ | ❌ |
| 3 | `responses.go:719` | `isStream` | **`!isStream`** | 已设 | ✅ | ❌ **互斥** |
| 4 | `messages.go:730` | `isStream` | `!isStream` | **未设 → `""`** | ❌ | ❌ |
| 5 | `durable_runner.go:118` | `false` | **`true`** | 端点门控已设 | ✅ | ❌ |

⇒ **5/5 全部不可达。**

### 1.4 实际后果

`/v1/responses` **非流式** + native responses 上游时，响应写出走
`responses.go:971` 的 `h.writeNonStreamResponse(w, result.ResponseBody, ...)`，
该路径**不做任何账本还原**；`result.ResponseBody` 来自
`executor_chat.go:1776` 的原始上游 body。

⇒ **paramguard 对 `reasoning_effort` 的 clamp/normalize（如 `x-high` → `high`）
在非流式 Responses 车道会原样回显给客户端**，而包头注释
（`ledger.go:10-19`）声明的「回程把被调整的字段还原为客户端原始值」在该车道不成立。

### 1.5 【本轮最值得记】门是绿的，但门没盖住生产路径

`domains/streaming/executors/paramledger_integration_test.go:36` 的
`TestParamLedgerNativeResponsesEffortRoundTrip` **完整断言了这条往返**
（出站被降为 `high`、账本记了 clamp、客户端收到 `x-high`），且**全绿**。

但它直连构造 `&ExecParams{...}`（`:62-67`），**没有设 `SuppressSuccessWrite`**（零值 `false`）
⇒ 测试里 `!SuppressSuccessWrite` 为 true ⇒ 块执行 ⇒ 还原发生。
**生产里这个取值恒为 `true`。**

**变异验证**（conventions §9.4）：

| 变异 | 做法 | 结果 |
|---|---|---|
| **M1** | 给测试的 `ExecParams` 加 `SuppressSuccessWrite: true`（**镜像 `responses.go:750` 的非流式生产取值**） | 🔴 **红**，断言失败于 `paramledger_integration_test.go:94` `client body missing restored effort:`，且 body 为**空**（整块被跳过） |
| **M2（反向对照）** | 改成显式 `SuppressSuccessWrite: false`（语义等价的零值） | 🟢 **绿** —— 证明这些测试**不是「只要动这个函数就会红」** |

按 §9.4 第 1 步确认 M1/M2 均为**断言失败**（`t.Fatalf`）而非编译失败。
还原后 `cmp` 逐字节一致，`go test ./domains/streaming/executors/ -run TestParamLedger` 通过。

> M1 只改**测试**、不改生产代码，就让一个全绿的往返测试报红——这正是
> 「测试断言的是一个人为构造的配置，而生产走的是另一个配置」的教科书形态。
> **如果只跑 CI 看颜色，这个 P1 永远不会浮现。**

---

## 2. 【P2】Anthropic 路径**只写账本、零还原入口**

- `executor_anthropic.go:718` 调用 `e.applyParamguardLedger(params, bodyBytes, paramreg.DialectAnthropic)`
  ⇒ `thinking`（对象级 strip）与 `thinking.budget_tokens`（clamp）**会入账**。
- 同一文件 `restoreClientEcho` / `RestoreResponsesEffort` 命中数 = **0**。
- `RestoreResponsesEffort` 的 `switch adj.Field`（`ledger.go:208-209`）只接受
  `"reasoning_effort" / "reasoning.effort" / "reasoning"`，且 `Sent == ""` 的 strip 类
  在 `ledger.go:205` 被 `continue` 掉 ⇒ **`thinking.*` 在设计上就不可能被还原**。

**冲突点**：`ledger.go:17-19` 的包头注释把「chat / anthropic 形态的响应不回显这些请求参数，
天然无需还原」写成**事实陈述**。实际状态是：
**网关对 Anthropic 响应是字节透传、不解析 `thinking` 参数对象**——
这只能证明「网关不消费」，**不能证明「上游不回显」**。该假设**未经任何证据支撑**，
而本条把它写成了已确立的事实。

**定级 P2 而非 P1**：是否真的回显未证实（无真实上游调用验证），且 Anthropic 入口真库仅 52 行。

---

## 3. 【P2】TTL 15 分钟 vs 流式响应**无总时长上限** ⇒ 同一流内前后帧还原结果不一致

- `executor_chat.go:2536-2544` `upstreamContext`：流式分支是
  `context.WithCancel(params.R.Context())`——**只随客户端断开结束，无 deadline**；
  detached 分支才有 2 小时（`detachedStreamMaxLifetime = 2 * time.Hour`，`:2530`）。
- `cmd/gateway/main.go:7258` `WriteTimeout: 0`（Go 语义：写响应无上限）。
- `entryTTL` 只有 15 分钟，且**无任何续期路径**（`Record` 只在出站准备阶段调用，
  `paramledger_integration.go:44`、`:81`；流式逐帧循环里不再调用）。

⇒ 客户端发 native Responses 请求、paramguard 降档 effort、且上游在
`response.created` 之后**超过 15 分钟**才吐 `response.completed` 时：

- `response.created`（`FrameClassAttemptMetadata`，`stream_frame_classifier.go:243`）→ 还原为 `x-high`
- `response.completed`（`FrameClassTerminal`，`:251`）→ `Lookup` 返回 nil → **原样透传 `high`**

**客户端在同一逻辑流里看到自相矛盾的两个值**。失效是**静默**的
（`ledger.go:196-199` 拿 nil 就原样返回，无日志无告警）。

**帧分类本身已核实无缺口**：`response.created`/`in_progress` → `FrameClassAttemptMetadata`，
`response.completed`/`incomplete`/`failed` → `FrameClassTerminal`，
两端都在 `native_responses_stream.go:248` 的还原条件内。
**问题不在分类，在 TTL。**

---

## 4. 【P2】durable 跨进程接管 ⇒ 账本必失

- durable 任务的 SSoT 是 **PostgreSQL**（`durable/store_claim.go`）：
  `ClaimRunnable` 用 `SELECT ... FOR UPDATE SKIP LOCKED`（`:94`）批量领取，
  过滤条件只有 `lease_until < $2`（`:83`、`:91`）——**无任何实例标识过滤**，
  任意进程的 worker 都能领走。
- `durable_runner.go:146` 用**接手进程自己的** `r.exec` 执行，
  `restoreClientEcho` 读的是该进程的 `e.ParamLedger`——里面没有这个 request_id。

⇒ 请求在 A 进程发起 → 客户端断开（survival/durable 接管）→ B 进程领走并重建执行
⇒ **B 必然查不到账本**。

**讽刺处**：`durable_runner.go:140` 用的 `snapshot.NormalizedBody` **就是已改写过的出站体**，
B 手里信息是够的，只是账本查不到。Redis 镜像明明写了（`llmgw:paramledger:{request_id}`，同 TTL），
但**读路径永不走 Redis**（`ledger.go:22-24` 自陈）。

> 定级依赖一个**未证实的前提**：多实例部署的实际生产拓扑无清单证据。
> 本条按**代码路径可达性**定级，不是「现网一定开了多副本」。同一条路的非流式部分
> 另受 §1 的互斥保护（`durable_runner.go:122` 恒 `SuppressSuccessWrite: true`）。

---

## 5. 【P3】零可观测性 + Redis 镜像**零消费面**

### 5.1 零指标

`grep -rni "paramledger|param_ledger|param_adjust" --include=*.go metrics/` → **0 命中**。
`Record` / `Lookup` / `RestoreResponsesEffort` 全部无计数、无耗时、无失败信号。
§1/§3/§4 三条失效路径**全部静默**。
Redis 镜像写失败甚至只 `slog.Debug`（`redis_mirror.go:40`，默认日志级别下不可见）。

### 5.2 Redis 镜像无任何读取方

用**三种检索方式**复核：

| 方式 | 检索 | 结果 |
|---|---|---|
| A | `grep -rn "llmgw:paramledger" .`（全仓、全文件类型） | 10 命中，除 5 个编译产物二进制外，**全部是写入侧**（`main.go:1575` 构造、`redis_mirror.go:19/:25` 定义）或注释 |
| B | `grep -rn "\.Lookup(\|HasAdjustments()"`（包外） | 生产命中 **0**；仅 `paramledger_integration_test.go:78/:139` 与包内 `ledger.go:196` |
| C | `grep -rln "paramledger" --include=*.go admin/ domains/` | 7 个文件全部是**写入侧/接线/测试**，无观测面 |

⇒ `ledger.go:23-24` 声称的「供跨进程审计/观测」**没有消费方**。
写入是有成本的（每次变更一次 `SET` + goroutine），收益为零。

**与已知 P3 同族**：`domains/credential/state_sync.go:25` 注释所述的
`routing_health_checks.circuit_open` 同样是不存在的消费面。
**共同形态：注释里的「消费者」比代码里的「生产者」跑得更快。**

---

## 6. 【P3】注释与代码不符（42 号那条「需求差距」的成因之一）

`executor_chat.go:386-387` 注释：

> The mode-fallback path populates ResponsesBodyBytes from the chat body before calling
> executeOpenAI, so reqprobe-driven Responses→Chat fallback still works.

**代码不如此**。读了该回退的完整包围函数（`tryUnsupportedResponsesFallback`，`:563-611`），
它做的是 `sourceBody = clientSourceBody; bodyBytes = fallbackBody; nativeNonStream, nativeStream = false, false`
——**恢复 chat 体并关闭 native 标志，从未写入 `params.ResponsesBodyBytes`**。

全仓 `ResponsesBodyBytes` 的生产赋值只有 4 处
（`responses.go:723` / `executor_dispatch.go:957` 自身派生 / `executor.go:2182` 自拷贝 /
`durable_runner.go:140` 端点门控）——**没有一处来自 chat 车道**。

⇒ **chat 客户端 + native responses 上游这条组合在生产中不存在**（`nativeStream`/`nativeNonStream`
恒 false），`supervisor` 注释描述的是一条未接线的路径。
按「不回改历史」只登记，**不建议改注释**（改注释不改变行为，但会掩盖该路径的待接线意图）。

---

## 7. 已核实为**不是**问题的项（避免下轮重复劳动）

| 项 | 结论 | 证据 |
|---|---|---|
| 账本共享切片的数据竞争 | **不存在**。`Record` 捕获的 `entry.Adjustments` len = 追加后的 len，后续 `append` 写入位置 `>= len`，与镜像 goroutine 只读的 `[0, len)` **不重叠** | `ledger.go:140-146` |
| FIFO 容量 20000 条 | **本地实测不可达**。峰值 18.25 QPS（1095/分钟）⇒ 驻留 ≈ 18.3 分钟；且 TTL 15 分钟**先于**淘汰生效。另：账本只装**被调整过**的请求（`len(reports)==0` 即 return，`paramledger_integration.go:31-33`），插入量远小于总 QPS | 真库 `request_logs_2026_09`（215 万行，聚合 2.0s） |
| 流式帧分类漏还原 | **不存在**。`response.created`/`completed`/`incomplete`/`failed` 均落在还原条件内 | `stream_frame_classifier.go:233-252` |
| 账本注入脑裂（单例 vs executor 字段） | **不存在**。`main.go:1581` 与 `:1619` 注入**同一实例** | `cmd/gateway/main.go:1570-1581`、`:1619` |
| 还原改写 body 长度与 Content-Length 不一致 | **已修**且顺序正确（R51 修复），还原在 `copyNonStreamResponseHeaders` 之前 | `executor_chat.go:1763-1767` |

---

## 8. 登记为待核实项（本轮**未**下结论，不猜测填空）

1. **path F 双重改写**：`domains/transformation/serialize_openai.go:278-321` 的
   `applyThinkingToOpenAIChat` 会经 `reasonnorm.Render` 写出 `reasoning_effort`，
   **绕过 paramguard 且不产生 Report**。若它先于 paramguard 执行，
   账本里的 `Original` 记的是 **IR 渲染后的值**而非客户端原值 ⇒ 还原链语义错位。
   **本轮未能确认它与 paramguard 的实际执行先后**（取决于走
   `finalizeOpenAIUpstreamBody` 还是 native responses 分支），故不定级。
2. **`reasoning` 对象的兄弟键**：`internal/paramreg/registry.go:209` 登记
   OpenAI Responses 的 `reasoning` 含 `{effort,summary,context,mode}`。
   `summary` 是否会被网关改写、是否被回显——未查。
3. **`fixOpenAIOTokens`（`guard.go:300-319`）故意不记账**：o-series 下
   `max_tokens` → `max_completion_tokens` 键名改写，两键并存时还 `delete(obj,"max_tokens")`
   —— 实质是 strip 语义却不记账。是否产生客户端可见偏差未查。

---

## 9. 对 objective 的直接回答

| objective 原句 | 现状 |
|---|---|
| 「对于超出原厂的参数，**缓存**下来」 | ✅ `internal/paramledger`，进程内存 20,000 条 + Redis 镜像 |
| 「（注意 **TTL** 时间）」 | ✅ 15 分钟，内存与 Redis 同 TTL；但**流式长于 15 分钟时会中途失效**（§3） |
| 「在**返回时恢复**放到请求中继续」 | ⚠️ **已实现但接线断裂**：流式可达；**非流式零可达**（§1）；Anthropic 零入口（§2）；durable 跨进程必失（§4） |

**42 号「⏸ 需需求方澄清」应关闭**：这不是「是否需要跨请求多轮恢复」的需求歧义，
而是一个**已实现、已接线、但非流式那条线从未真正跑过**的缺陷。

---

## 10. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/93-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |
| `docs/全面审计v3/00-审计覆盖台账.md` | 待裁决第 38 条 + 关闭 42 号的「需求澄清」项 |
| `docs/全面审计v3/2026-10-01/42-R72-...md` | **加警示框**指向本报告（不改写原文） |

变异验证仅改 `paramledger_integration_test.go` 且已 `cmp` 逐字节还原。
**零生产代码、零配置、零门、零 CI 行为变化。**

## 11. 交叉引用

- 42 号：其 §2.2「无缓存无 TTL」结论**已过期**，本报告关闭
- conventions **§9.4**（变异验证）/ **§10.1**（量化结论自己数一遍）/ **§10.2**（先定位函数体）
- playbook 既有条目「**门全绿 ≠ 门覆盖我**」——§1.5 是该条目在本仓的新实例
