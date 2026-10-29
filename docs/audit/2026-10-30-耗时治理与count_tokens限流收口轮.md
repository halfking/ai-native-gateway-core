# 2026-10-30 · 耗时治理与 count_tokens 限流收口轮

> ★ 本轮承接上一轮（`docs/audit/2026-10-29-Gemini-countTokens诚实实现轮.md`）
> §9「本轮明确没做（留给下一轮）」里登记的三项：
> ① **转发上游那一支**（gemini-generate 上游的 `:countTokens` 精确值）；
> ② **Anthropic `/v1/messages/count_tokens` 的无 RPM 缺口**（仓内 P2）；
> ③ **`domains/streaming` 全包耗时逼近 `make guards` 的 `-timeout=120s` 余量**。
>
> ★ 本轮**完成 ②③**，①**因口径未裁决而未动**（见 §6「本轮明确没做」）。
>
> ★★★ **而 ③ 的真实状态比登记时严重得多**：登记写的是「逼近余量」，
> **实测是已经越界 —— `make test-short` 当前就是红的**。

---

## 1. ★★★ 三项的真实状态（先量，再动手）

★ 本轮的第一件事是把三项**各自实测**一遍，而不是按登记时的描述开工。
⇒ 结果：**两项的登记描述与事实不符**。

| 项 | 登记时的描述 | 实测（本机 macOS arm64 / Go 1.27.2） | 判定 |
|---|---|---|---|
| ③ streaming 耗时 | 「约 150s，逼近 `-timeout=120s` 的余量」 | 全包 **163.561s**；`-short` 口径 **120.249s 超时 panic**；`-race` **260.384s** | ★★★ **不是「逼近」，是已越界** |
| ② count_tokens 无 RPM | 「P2 缺口，待裁决是否补」 | 缺口仍在（登记属实） | ★ 属实 |
| ① 转发上游 | 「只在一��部署形态下成立；需第三条出站动作；须先定『以哪个为准』」 | 见 §5：**反对理由之一被代码推翻** | ★★ **前提变了** |

★ **`make test-short` 是红的** —— 它是
`.github/workflows/sessionforensics-ci.yml:42` 的调用方，
预算正是 `-timeout=120s`（`Makefile:42`）。
⇒ 也就是说：**一个包的全包耗时，把整仓的短模式测试腿拖过了超时线**。

### 1.1 ★★★ 「余量还剩多少」是【会漂的读数】不是事实

★ 218 号（建 `ingressguard` 包那轮）把这句话写进了 `ingressguard` 的**文件头**：

> 实测 `go test ./domains/streaming/ -count=1 -timeout=600s` → ok 106.471s … ⇒ 余量只剩 13.5 / 8.2 秒

★ 本轮实测 **163.561s** ⇒ ★★★ **那个「余量」判断写下时就已经过期了**
（106s → 163s 的涨幅发生在 218 号之后）⇒ ★★★ **真实情况不是「余量只剩 13.5 秒」，
是「已经越界、已经 panic」** ⇒ ★★★

> ★★★ **⇒ 通式：凡是「还剩多少余量」这类读数，必须定期重测。**
> ★★★ 注释负责【理由】，代码负责【事实】—— 注释里的读数**不会自己失效**，
> ★★★ 它会变成一句**看起来很可信、且指向相反结论的假话**。

★ 同一条纪律在 `ingressguard` 文件头的另一句上同样成立：

> ⚠️ 补两道闸**都会改变对外行为**（402 / 429），属产品裁决 ⇒ 本包**不改行为**

★ 而 P2 那条缺口**在同一年就被补掉了**（2026-10-29 的 Gemini countTokens 轮）⇒ ★★
**登记表会过期，注释不会提醒你** ⇒ ★★ 本轮两处都一并订正了。

---

## 2. ★★★★ ③ 的根因一：测试构造了一个**生产不存在的客户端**

### 2.1 现象

★ 耗时分解（`-v` 逐条读数）显示，163.6s 里 **~75s** 集中在 4 个测试：

| 测试 | 耗时 | 所在文件 |
|---|---|---|
| `TestAudioCandidateFailureLedger` | **30.01s** | `audio_ledger_test.go` |
| `TestAudioCandidateFailureLedgerNotWiredByDefault` | 15.00s | `audio_ledger_test.go` |
| `TestUpstreamServerErrorStaysBadGateway` | 15.00s | `audio_xiaomi_variants_test.go` |
| `TestEmbeddingsHandlerFailsOverOnServerError` | 15.00s | `embeddings_test.go` |

★ **耗时是 5s 的整数倍** ⇒ 这是**退避**，不是计算。

### 2.2 根因

★ 栈采样（`-timeout=8s` panic 抓栈）指向：

```
upstream.(*Client).doWithClient(...)
	upstream/client.go:312          ← select { case <-time.After(delay) }
upstream.(*Client).Do(...)
domains/streaming.(*AudioService).transcribeViaChatAudio(...)
	domains/streaming/audio_service.go:727
```

★ `upstream/client.go:295-317` 的重试退避：`delay = c.baseDelay * (1 << (attempt-1))`，
★ 而 `baseDelay = retryBaseDelay = 5 * time.Second`（client.go:25）、`maxRetries = 2`（client.go:24）
⇒ ★ 退避序列 **5s + 10s = 15s**；两个候选各等一次 = **30s**。

★ **而生产根本不是这个配置**：

| | 构造器 | maxRetries | 退避 |
|---|---|---|---|
| **生产** | `upstream.NewWithRetries(0)`（`cmd/gateway/main.go:828`） | 0 | 无 |
| **测试** | `upstream.New()`（32 处） | 2 | 5s + 10s |

★ `upstream/client.go:127-132` 的注释把为什么这么设计写得很清楚：

> The chat / anthropic path (via Executor) should pass 0 here so retries are owned by
> the routing layer (which can switch credentials, update health state, and skip
> client-bug kinds)

★ ⇒ ★★★★ **测试构造的是一个【生产不存在的客户端】** ⇒ ★★★★

> ★★★★ **⇒ 这不只是「测试慢」，是【口径漂移】**：
> ★★★★ 生产「重试归路由层所有」（可换凭据、可更新健康状态），
> ★★★★ 测试却在上游客户端内部重试 ⇒ ★★ **测试测的失败语义与生产不同**。
> ★★★★ **⇒ 所以这类改动不是「加速测试」，是「让测试测回生产」。**

★ 佐证：仓内**已有一半**测试改对了（`embeddings_test.go` 里 9 处用
`NewWithRetries(0)`）⇒ ★★ **同一仓里两种写法并存时，
要先确认哪一种是生产口径再统一** ★★。

### 2.3 处置

★ 全仓 `domains/streaming` 的测试里 **`upstream.New()` 32 处 → 全部改成
`upstream.NewWithRetries(0)`**（14 个文件）。

★ **改前先判过「有没有测试依赖内部重试语义」**：

| 断言重试次数的测试 | 位置 | 与 upstream client 内部重试的关系 |
|---|---|---|
| `TestGoalRetryLoop_*`（3 处断言 attempts/retriesPerformed） | `goal_retry_*_test.go` | ★ **无关** —— 那是**网关自己的**重试层 |
| `upstream_bytes_retry_test.go` | 同名 | ★ **无关** —— 测的是 body 可重放 |

★ ⇒ **没有任何测试依赖 upstream client 的内部重试次数** ⇒ ★★ 盲改是安全的。

★ 复测：4 个测试 **30.01s → 0.01s**、15.00s → 0.00s。

---

## 3. ★★★ ③ 的根因二：超时门设成 1s 也救不了「Close 不解除阻塞」

### 3.1 现象

★ 剩余慢测试里有一类**不在** `upstream.New()` 的账上：

| 测试 | 耗时 | 所在文件 |
|---|---|---|
| `TestStreamChat_StreamTimeoutGateAware` | 10.00s | `gate_aware_failure_test.go` |
| `TestStreamAnthropicPassthrough_StreamChunkTimeoutGateAware` | 10.00s | `gate_aware_failure_test.go` |
| `TestStreamChatWithPendingCapture_CommittedTimeoutFrameCarriesDoneAndRootFields` | 10.00s | `r59_audit_regressions_test.go` |

★ ★★★ **反常点**：这些测试**已经把超时门设成 1 秒**了：

```go
t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")
```

★ ⇒ **超时参数是 1s，测试却耗了 10s** ⇒ ★★★ **⇒ 超时门没有按预期生效**。

### 3.2 根因

★ `internal/sse/line_reader.go:98-135` 的 `ReadLineWithContext`：

```go
select {
case got := <-results:
	return got.line, got.err
case <-readCtx.Done():
	if closer != nil {
		_ = closer.Close()
		<-results          // ★★ 等读协程退出
	}
	...
}
```

★ ⇒ ★★★ **超时后要等读协程退出，而读协程卡在 `Read` 里** ⇒
★ ★★★ 测��里的 `hangingBody.Read` 是：

```go
time.Sleep(10 * time.Second) // exceed chunk timeout in tests
```

★ ⇒ ★★★ **超时设成 1s 也救不了：那个读协程要睡满 10s 才退出** ⇒
★ ★★★ **⇒ `<-results` 把这 10s 等了出来** ⇒ ★★★

> ★★★ **⇒ 通式：「超时参数」≠「超时后返回」** ——
> ★★★ **只要取消路径上还有一处不可中断的阻塞，超时门就只是「开始计时的信号」**。

★ 仓内**已有正确形态**：`internal/sse/line_reader_test.go` 的
`blockingReader`（`<-r.unblock`）+ `closeSignal`（Close 关 channel）⇒ ★★
**同族包里往往已经有正确实现，先去找** ★★。

### 3.3 处置

★ `hangingBody` 改成 channel 形态：

```go
func (h *hangingBody) Read(p []byte) (int, error) {
	if !h.done {
		h.done = true
		return copy(p, h.data), nil
	}
	if h.unblock != nil {
		<-h.unblock // released by Close, so the timeout path is not blocked
	}
	return 0, errors.New("upstream hang")
}
```

★ 复测：两个 gate-aware 测试 **10.00s → 1.00s**（= 被测的超时门时长）。

### 3.4 ★★★ 第一次改动**真的炸了**（自愈记录）

★ 改完第一版跑全包 ⇒ **`panic: close of nil channel`**：

```
--- FAIL: TestStreamChatWithPendingCapture_CommittedTimeoutFrameCarriesDoneAndRootFields
panic: close of nil channel
	domains/streaming.(*hangingBody).Close(...)
		domains/streaming/gate_aware_failure_test.go:71
```

★ 真因：`hangingBody` 被**另一个测试文件**（`r59_audit_regressions_test.go`）复用，
★ 而那里的构造点**只填 `data`、不填新增的 `unblock` 字段** ⇒ `close(nil)` panic。

★ ⇒ ★★★ **通式：给共享测试类型加字段时，必须把【所有复用点】找齐** ——
★ ★★★ 仓内共 **3 个构造点**（`gate_aware_failure_test.go` ×2 + `r59_...` ×1）⇒ 全部补齐。
★ ⇒ ★★★ 且字段做成 **nil-safe**（nil ⇒ 不阻塞；close 幂等）⇒ 即使漏改也不会 panic。

★ ⇒ ★★★ **⇒ 这一条与本轮第 6 条的「变异臂」互为镜像**：
★ ★★★ 上一轮（§3.2）说「**只加 `testing.Short()` 门会让 8 个测试假绿**」，
★ ★★★ 本轮（§3.4）说「**加字段会让复用点 panic**」⇒
★ ★★★ **两边都是「改共享测试设施」的外溢成本** ⇒ ★★★
★ ★★★ **⇒ 改共享测试设施前，先数清它的复用面** ★★★。

---

## 4. ★★★ ② 的处置：给 `/v1/messages/count_tokens` 补 RPM 闸

### 4.1 缺口原文

★ `internal/ingressguard/ingress_surface_test.go` 的登记册：

```go
RateLimit:    gateNone,
RateLimitWhy: "无 RPM 限流；单请求可读至 128MB（handler.go:69 的 maxBodySize）且无频率上限 ⇒ P2 缺口，待裁决是否补",
```

★ 218 号的判词（文件头）：

> 218 号又查出 `/v1/messages/count_tokens` **既没有配额闸、也没有 RPM 限流**
> … 而同级 `/v1/messages` 在 `messages.go:429` 是有的。

### 4.2 ★★★ 补它的理由（为什么这不是产品裁决）

★ 2026-10-29 的 Gemini countTokens 轮**已经把同一类问题的口径定了**，
★ 且它**明确指向本条**：

> ★ 而仓内已把 Anthropic 的 `/v1/messages/count_tokens` 登记成 P2 缺口
> （「无 RPM 限流」）⇒ **新端点不应再制造同一个缺口的第二个实例**。

★ ⇒ 那个 P2 缺口**就是本条** ⇒ ★★ 本轮补它，是**兑现上一轮自己写下的承诺**，
★ ★★ 而不是新开一个产品决策 ⇒ ★★

★ 三条理由（与 Gemini 轮同源）：

1. ★ **本面消耗网关自身算力**：要读整个 body（最多 **128MB**，`handler.go:72` 的
   `maxBodySize`）再做一次 Anthropic 形状解析与 token 估算 ⇒ 网关的 CPU/内存
   开销由调用方触发 ⇒ **有频率上限才是对的**。
2. ★ **不存在「已计一次」的那一跳**：与 `summarize/transform` 的「刻意不消耗 RPM」
   不同 —— 那三个面的 LLM 步骤经**环回 chat 面**携带同一把 key，那一跳已计一次
   ⇒ 再计即双计；★ 而本面**不产生任何上游调用** ⇒ 没有那一跳 ⇒ 按同族动作
   计一次是唯一自洽的选择。
3. ★ **同级对照**：`/v1/messages`（`messages.go:444`）本来就有 RPM 闸 ⇒
   「同级有、自己没有」要消除。

### 4.3 ★★★ 闸位 = 这道闸的**全部意义**

★ 处置时最要紧的一条：

```go
// ★ 闸位放在【鉴权之后、读 body 之前】——这是它真正的意义所在：
//   被限流的请求**不会**再去读那最多 128MB 的 body ⇒ 限流 thereby 限制了
//   网关自己的算力与内存开销 ⇒ 若把闸放在读 body 之后，被拒的请求已经
//   付掉了全部读取成本，限流就只剩了个「拒绝」语义、挡不住消耗。
```

★ ⇒ ★★★ **⇒ 必须有一条【位置断言】来钉它** ⇒ ★★★
★ ★★★ **只断言状态码是不够的**（★ 闸挪到读 body 之后，状态码仍是 529、
★ ★★★ **只断言状态码的测试照样绿** ★★★）⇒ ★★★

★ 本轮的位置断言（用一个会记录「自己有没有被读过」的 body）：

```go
if body.read {
    t.Fatal("rate-limited request must NOT read the request body — " +
        "the RPM gate is what stops the gateway from paying that read cost")
}
```

### 4.4 状态码口径：529，不是 429

★ 同路由族的 `/v1/messages` 被限流时回 **529**（`messages.go:462`）⇒
★ 本端点沿用同族口径 ⇒ ★★ **同一把 key 在 `/v1/messages` 与
`/v1/messages/count_tokens` 上不会收到两种限流状态码** ★★。

★ 另附（与 `/v1/messages` 同款）：写限流响应头（`writeRateLimitHeaders`）
⇒ 客户端能读到 `Retry-After` / 剩余额度，而不是只看到一个错误信封。

### 4.5 ★★★ 登记册翻转 + 双向咬合验证

★ 登记册那一条：`RateLimit` 由 `gateNone` 翻成 `gateOwn` ⇒ 该门的判据是
**锚点计数**（`checkGatewayRateLimit(` 恰好 1 次）。

★ ⇒ ★★★ **正向变异臂**：把闸挪到读 body 之后 ⇒
★ `TestCountTokens_RateLimitedDoesNotReadBody` **立刻变红**（rc=1，报
★ 「rate-limited request must NOT read the request body」）⇒ 还原后 md5 逐字一致。

★ ⇒ ★★★ **反向变异臂**：把整道闸删掉 ⇒ `ingressguard` 的门**立刻变红**
★ （报「登记为 own，但 "checkGatewayRateLimit(" 出现 0 次，期望恰好 1 次」）
★ ⇒ 还原后 md5 逐字一致。

> ★★★ **⇒ 通式：登记表与代码是【双向咬合】的一对。**
> ★★★ 只做「代码改了就更新表」是**单向**的 ⇒ ★★★
> ★★★ **必须验证「表改了、代码没改」也会红** ★★★ ⇒ 否则表可以撒谎 ★★★。

### 4.6 ★★ 一张测试表要容纳语义不同的成员时，【显式区分】优于【放宽】

★ 本轮把 count_tokens 加进既有的限流测试表
★ （`rate_limit_test.go` 的 `TestNonChatHandlers_RecordQueueBudgetRejection`）⇒
★ 那张表原先断言「被限流时**恰好写 1 条**请求日志」⇒
★ 而 count_tokens **不是推理面**（不产生上游调用、不计费）⇒ 不写日志 ⇒ 该用例红。

★ ⇒ ★★★ 正解是给表加一列**显式声明**「期望写 / 不期望写」，
★ ★★★ **而不是把断言整体放宽** ⇒ ★★★ **放宽 ⇒ 既有三面的断言就被架空了** ★★★。

★ ★★ 附带的测试判据坑：`checkGatewayRateLimit` **只在 ctx 带 deadline 时**
★ 才走 `AdmitRPMWithBudget`（`rate_limit.go:88`）⇒ ★★ 用只实现了该方法的桩去测
★ ★★ **不带 deadline ⇒ 走的是 `AdmitRPMWithWait` ⇒ 桩不被触发 ⇒ 测试假绿**
★ ★★ （★ 本轮实测：状态码 200，而测试以为已限流 ★★）★★。

---

## 5. ★★ ① 的口径前提被代码推翻（但仍不动手）

### 5.1 登记原文

★ `2026-10-29-Gemini-countTokens诚实实现轮.md` §9：

> ★★ **gemini-generate 协议上游的 `:countTokens` 精确值**（转发上游那一支）——
> ★ **只在这一种部署形态下成立**，且它要求**第三条出站动作**
> （响应形状 `{"totalTokens":N}` 与 `GenerateContentResponse` 不同 ⇒ 不走
> `ProtocolHandler` 的通用路径）⇒ 独立一轮。
> …
> ★★ **注意它与本轮的关系**：转发的那一支**只在上游协议是 gemini-generate 时**
> ★★ 才有意义 ⇒ ⇒ **它的答案与本轮的估算值口径不同**（一个算客户端原文、一个算
> ★★ 归一体）⇒ ⇒ ★★ 若两者同时存在，**同一个客户端在两种部署下会拿到两个口径的
> ★★ 数字** ⇒ ⇒ **做那一支之前必须先定「以哪个为准」**

### 5.2 ★★★ 本轮读代码发现的**反对理由之一不成立**

★ 那一轮反对「转发上游」的第一条理由是：

> ★ **转发算的是另一份请求**：厂商的 `:countTokens` 会对**客户端原文**
> （Gemini 形状）计数，而网关发出去的是**归一后**的那份 ⇒ 转发得到的
> 数字描述的是**一个不会发生的请求** ⇒ 它不是更诚实，是**另一种不诚实**。

★ ★★★ **但这条理由的前提是「转发客户端原文」** —— ★★★ 而转发**不必**转发原文：
★ ★★★ 网关出站体就是 `ir.SerializeGemini(IR)`
★ （`domains/streaming/executors/executor_gemini.go:1454`，`finalizeGeminiUpstreamBody` 的最后一步）
★ ★★★ ⇒ **转发归一后的体，拿到的就是「网关真正会发出去的那份」的厂商精确值**
★ ★★★ ⇒ ⇒ ★★★ **「两个口径」这个前提可以被消解**（前提变了，但**裁决还没做**）。

★ ★★ 而**裁决仍未做的原因是**：转发**必然**是「gemini-generate 上游用精确值、
★ ★★ 其他上游用本地估算」⇒ ★★ **同一个客户端在两种部署下会拿到两个数字**
★ ★★ ⇒ ★★ 这个「以哪个为准」仍是**产品口径问题**，不是工程问题。

### 5.3 ★★ 本轮的动作

★ ⇒ **不实现** ⇒ ★★ 已就此向 Owner 发出**两问裁决**（转发口径 + Anthropic
★ ★★ count_tokens 的 RPM 口径）⇒ ★★ 在裁决到达前**不猜口径、不动手**
★ ★★ （★ §9.5 的纪律：「口径要写在【代码】里，不能只写在【注释】里」⇒
★ ★★ 反过来也成立：**口径没定就不能落代码** ★★）。

---

## 6. 读数与验证

### 6.1 ★★★ 耗时三个口径（前后对照）

| 口径 | 修复前 | 修复后 | 预算 | 判定 |
|---|---|---|---|---|
| 全包 `-timeout=600s` | **163.561s** | **59.223s** | — | ✅ |
| `-short -timeout=120s`（`make test-short`） | **120.249s 超时 panic** | **55.823s** | 120s | ✅ **从红变绿** |
| `-race -timeout=600s`（`make test-race-core`） | **260.384s** | **123.662s** | 600s | ✅ |

★ ⇒ **余量从「已越界」变成 ~60s** ⇒ ★★ 216 号实测 `sql/schema` 在 25s~83s
★ ★★ 之间波动 ⇒ ★★ 现在这个余量**足以吸收环境波动** ★★。

### 6.2 ★★ 本轮改动的读数

| 项 | 读数 |
|---|---|
| `domains/streaming` 全包 | **ok 59.223s**（rc=0） |
| `domains/streaming` `-short` | **ok 55.823s**（rc=0） |
| `domains/streaming` `-race` | **ok 123.662s**（rc=0） |
| `domains/streaming` + `internal/ingressguard` + `upstream` 同跑 | 全绿（64.190s / 0.037s / 17.423s） |
| `internal/ingressguard` | ok 0.025s（rc=0） |
| `internal/sqlguard` / `internal/routeguard` | ok 45.990s / ok 0.845s（rc=0） |
| `go build ./...` | ✅ |
| `go vet ./domains/streaming/ ./internal/ingressguard/` | ✅（0 错） |
| 变异臂（闸位） | rc=1 命中 ⇒ 还原 md5 一致 |
| 变异臂（删闸） | rc=1 命中 ⇒ 还原 md5 一致 |

### 6.3 ★★ 改动的文件清单

| 文件 | 改动 |
|---|---|
| `domains/streaming/messages_count_tokens.go` | 补 RPM 闸（闸位在鉴权后、读 body 前）；`Verify` 的返回值不再丢弃 |
| `domains/streaming/messages_count_tokens_test.go` | 新增 2 条测试（位置断言 + 反向面） |
| `domains/streaming/rate_limit_test.go` | 限流测试表加 count_tokens 一行 + 「期望写请求日志」列 |
| `internal/ingressguard/ingress_surface_test.go` | 登记册 `gateNone` → `gateOwn`；文件头两份过期读数订正 |
| `domains/streaming/gate_aware_failure_test.go` | `hangingBody` 改 channel 形态（nil-safe + 幂等） |
| `domains/streaming/r59_audit_regressions_test.go` | 补 `unblock` 字段 |
| 14 个 `domains/streaming/*_test.go` | `upstream.New()` → `upstream.NewWithRetries(0)`（32 处） |
| `LESSONS.md` | §576（6 条通用式） |

---

## 7. ★★ 通用式（详见 `LESSONS.md` §576）

1. ★★★★ **「余量还剩多少」是【会漂的读数】不是事实** —— 注释里的读数不会自己失效。
2. ★★★★ **测试构造的依赖版本必须与生产同款** —— 测试慢有时是「它测的不是生产那条路」。
3. ★★★ **超时门设成 1s 也救不了「Close 不解除阻塞」** —— 「超时参数」≠「超时后返回」。
4. ★★★ **限流闸的位置就是它的意义** —— 只断言状态码的门挡不住「闸被挪位」。
5. ★★★ **登记册的门与代码必须双向咬合** —— 要验证「表改了、代码没改」也会红。
6. ★★ **「同一句话不许两份」在测试表里也要成立** —— 显式区分优于放宽断言。

---

## 8. ★★ 本轮明确没做（留给下一轮，别重复排查）

- ★★ **① 转发上游那一支**（gemini-generate 上游的 `:countTokens` 精确值）——
  ★★ **已向 Owner 发出「以哪个为准」裁决**，在裁决到达前不实现。
  ★★ **★ 落地时的起手式已探明**：
  ★★ ① 在 `GeminiExecutor` 里加一个「只计数不生成」的动作 ⇒ 复用候选选择/凭据/
  ★★    header profile/重试，**只换上游 URL**（`:countTokens`）与响应解析
  ★★    （★ `upstreamurl.GenerateContentURL` 需要一个同款的动作参数化版本）；
  ★★ ② **转发体用 `ir.SerializeGemini(IR)`**（★ 即 `finalizeGeminiUpstreamBody`
  ★★    的最后一步，`executor_gemini.go:1454`）⇒ ★★ **不是客户端原文**
  ★★    ⇒ ★★ 这条让「两个口径」的前提被消解（§5.2）；
  ★★ ③ 第三条出站动作的响应形状 `{"totalTokens":N}` 与 `GenerateContentResponse`
  ★★    不同 ⇒ **不走 `ProtocolHandler` 的通用路径** ⇒ 要单独一条写出/解析路径。
- ★★ **「本地估算面」的 RPM 口径统一** —— `summarize/transform`（刻意不计）与
  ★★ `count_tokens`（计一次）的差异**理由已写全**，但**两套口径并存**这件事
  ★★ 本身值得一次统一（★ 归口：`domains/streaming/rate_limit.go`）。
- ★★ **`/v1/messages/count_tokens` 的「原文 vs 归一体」口径** —— 估**原文**。
  ★★ 改它会改变既有端点的返回值 ⇒ 属行为变更 ⇒ 需单独评估（本轮只补了 RPM 闸，
  ★★ **没动估算口径** ⇒ ★★ 本轮**没有**让 Anthropic 面与 Gemini 面的估算口径一致）。
- ★★ **剩余 9 个慢测试**（`goal_retry` 的 `sustained_load` 5s 负载窗、
  ★★ `summarize` 的 429 环回等一个 RPM 窗口、`survival` 的 keepalive 等）——
  ★★ 这些是**「测什么就要等多久」的合理成本**，不是缺陷 ⇒ ★★ 不动。
- ★★ **`domains/streaming` 纳入 `GUARD_PACKAGES`** —— 治理后余量够了（~60s），
  ★★ 但 `ingressguard` 刻意**不导入**它（静态判据足够、导入会让这道本该
  ★★ 毫秒级的守卫变成 CI 最慢的一步）⇒ ★★ **治理的目的是消除红灯，
  ★★ 不是把它搬进 GUARD_PACKAGES** ★★。
- ★★ **`make test-short` 在 CI 上是否真的红** —— 本仓远端是 codeup.aliyun.com
  ★★ ⇒ `.github/workflows/` 不在跑的流水线里（★ `main-verify.yml` 才是真载体，
  ★★ 它跑 `make guards-fast`）⇒ ★★ 但 `make test-short` 是**仓内开发者的
  ★★ 常用入口** ⇒ ★★ 本地红也是红 ⇒ ★★ 本轮已修。
