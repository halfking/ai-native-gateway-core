# 228 号｜R89-ES：`requestfact` 格式契约为何从未落地 —— 答案是 ADR-0003，而现役那一侧真正的问题在 `response_checksum`

- 日期：2026-10-03
- 轮次：R89-ES
- 起点：执行 115 号（台账 §4.28 L1459）**明文列出的下一轮必做项 ②**
  「`requestfact` 格式契约为何从未落地」
- **本轮新增待裁决 1 条（P2，待裁决 94）** + **守卫新增 3 条测试**
- **零生产代码改动**（含一次负控探针，已回收并复核 `git diff` 为空）

> ⚠️ **并发编号提示**：本轮与另一并发会话**撞了轮次号**——对方也提交了
> 「228 号 / R89-ES」，主题为 `feat(audio)` 的 MCP 端点错误码
> （`2026-10-03/228-R89ES-feat-audio的MCP端点混淆HTTPtransport错误与JSONRPC错误-定义了两个码却从未用上.md`，
> commit `0a9f081dc`）。**双方文件名不同、零冲突合并。**
> 本轮**不重编号**（重编号会与已推送的历史链条制造更多不一致），
> 但**台账编号 4.142 与 README 索引仅本轮占用**，对方的文档未进两处索引。
> ⇒ **台账里「228 号」目前指向两份不同文档**，以文件名与主题区分。

---

## 一、问题的答案在 115/116 号从未打开过的那个文件里

进域后先按 §153 查前轮。`internal/requestfact` 的「零引用」已由 115 号（§4.28）与 116 号（§4.29）登记为 B 类真孤儿 ⇒ **不是本轮的发现**。

但 115 号把它排进队列时写的是「**需确认是否接线**」。这个问题问的是「为什么」，
而答案在一个**这两份报告零次引用**的文件里：

```
docs/adr/ADR-0003-requestfact-production-wiring-and-hash-versioning.md
```

`Status: Accepted`（2026-09-09，Track G）。其 `:15` 明确决策：

> 「本 Track 不在协议 handler 中做半成品生产接线。……当前状态明确标记为『契约就绪 / 生产待接线』，
> 避免伪造完整事实或只覆盖单协议。」

`:24` 列出恢复接线所需的 5 条前置件。

⇒ **「从未落地」不是一个待查的悬案，是一个已被归档的、附前置条件的显式决策。**

---

## 二、⚠️ 台账内部不一致：42 号（8 天前）已经查到了，115/116 号却重新排队

| 轮次 | 日期 | 对 `requestfact` 的定性 | 引用 ADR-0003 |
|---|---|---|---|
| **42 号（R72）** | 2026-10-01 | 「**被 ADR-0003 明确阻塞的需求（非缺陷）**」，并逐条列出 5 条前置件 | **2 次** |
| **115 号（R89-AB）** | 2026-10-01 | 「B（真孤儿）」+ 必做项「**需确认是否接线**」 | **0 次** |
| **116 号（R89-AC）** | 2026-10-01 | 「B」+「两半都零引用，整套设计从未落地」 | **0 次** |

（计数方式：对三份报告正文做字符串计数，非推测。）

42 号 `:§5` 甚至把这一条写成了自己的方法论：

> 「我差点报了个假警。只 grep 到『零调用』就写成『死接线』，是错的——
> **判定能力缺失前，先问『有没有文档记录这是这是有意的』**。」

⇒ **115 号把 42 号已经定性的事当成了未定性重新排队**：它继承了「真孤儿」这个**状态**，
  却丢掉了「被 ADR 阻塞」这个**理由**。
⇒ §153 的一个此前没记过的形态：**前一轮的定性被继承了，理由没有**。
  队列因此出现了一个「其实已经有答案」的待办。

---

## 三、ADR 的「还在等」到今天还成立吗？—— 逐条用代码检验（0/5 满足）

ADR 是决策文档，**不能把它的自述当证据**（§224：注释里的前提会过期）。逐条核查：

| # | ADR 要求的前置件 | 2026-10-03 现状 | 关键证据 |
|---|---|---|---|
| 1 | 统一 terminal fact builder | **不满足** | `CanonicalRequestFact` 全仓 32 处命中，逐条归类后：**真正的复合字面量构造点全在 `_test.go`**（`golden_roundtrip_corpus_test.go:87`、`codec_test.go:285`、`archive_test.go:37`）；生产侧只有 `requestarchive/archive.go:106` 的**存储字段引用** |
| 2 | ≥1 个真实 handler 端到端 fixture | **不满足** | 两包全部 `_test.go` 中 `httptest|http.Handler|ServeHTTP|domains/streaming` **0 命中** |
| 3 | `CodecVersionV2` / V2 hash projection | **不满足，仅 V1** | `types.go:11-18` 只有 V1；`codec.go:66-69` 对四个版本号 fail-closed；全仓 `CodecVersionV2` 仅命中 ADR 自身 |
| 4 | V1/V2 golden corpus + 迁移/回滚说明 | **部分** | V1 语料**全内联**（`golden_roundtrip_corpus_test.go:32-71`，8 场景），无 testdata；V1/V2 双版本语料与迁移窗口**不存在** |
| 5 | 完整 response/upstream IR | **不满足** | `types.go:150-151` 的 `Upstream` / `Response` 块**生产中从无构造/赋值**；`internal/ir` 无 upstream IR 类型 |

**最强的一条单点证据**：

```
git log --since=2026-09-09 --oneline -- internal/requestfact internal/requestarchive
→ 0 输出（ADR 之后这两个包一行代码都没动过）
```

⇒ **115/116 号的「状态」定性是对的**（确实零引用、确实未接线），缺的只是**理由**。
⇒ 本轮把 ADR-0003 及其 5 条前置件的**当前状态**补进台账，队列项关闭。

---

## 四、但「ADR 说在等」不是本轮的全部产出：现役那一侧要逐项对照

必做项 ② 的完整问法是「requestfact 的**格式契约**为何从未落地」，
而格式契约有四要素 ⇒ 逐项看**现役 `session_turns` 落库路径**有没有：

| 要素 | `requestfact`（孤儿，933 行） | 现役 `session_turns` |
|---|---|---|
| **版本化** | 4 个 envelope 常量（`types.go:9-19`），唯一版本闸 `codec.go:66-67` fail-closed | **有另一套**：`domains/sessiondigest/digest.go:11-18`（`SchemaVersion=1`、`AlgorithmVersion="deterministic-v1"`），载在 `digest jsonb` 列（DDL `636_session_turns_digest.sql:7,9`），读端 `Unmarshal` 版本不符报错（`digest.go:102-104`） |
| **确定性哈希** | 全量 payload canonical hash（`codec.go:251`） | **有，但两列语义不同**（见 §五、§六） |
| **防篡改校验** | 两处 `subtle.ConstantTimeCompare`（`codec.go:59`、`:172`），Encode/Decode 双路径 | **无** |
| **golden 语料** | 8 场景 × 4 协议矩阵，100% 内联 | **无** |

⇒ ⚠️ **115/116 号「那套 `request_logs → session_turns` 迁移设计整体停在设计阶段」这句过头了**：
  现役**已经自建了一套 digest 契约并已在生产落库**（migration 636/707 + 回填器
  `domains/session/v2/session_digest_backfill.go:164,414-455`）。
  不是「没做」，是「做了另一套」。
⇒ 而 `sessiondigest.Envelope`（`digest.go:52-58`）字段为
  `schema_version / algorithm_version / generated_at / source / payload` ——
  **不含任何 hash 字段** ⇒ 现役 digest 契约**没有完整性这一维**。

---

## 五、【P2 · 待裁决 94】`response_checksum`：两个语义不同的生产累加器在写同一列

这一列叫 `checksum`，落在 `request_logs` 与 `session_turns`，并由管理端 API 原样下发
（`admin/logs.go:50-51`、`admin/telemetry.go:87`）。任何读到它的人都会被引导把它当作完整性证据。

**真相：同一个 `*StreamCapture` 上有两个累加器在写 `sc.checksum`，而它们哈希的东西不同。**

### 累加器 A —— `ObserveChunk`（`domains/hooks/audit/stream.go:38`），**主流路径 12 个生产调用方**

```go
chunkBytes, _ := json.Marshal(chunk)
sc.checksum = sha256.Sum256(append(sc.checksum[:], chunkBytes...))
```

调用方遍布 **OpenAI 流式、Anthropic 流式与两个协议桥接**：
`stream.go:318/1037/1221/1358/1569`、`anthropic_stream.go:585/644/1167/1181`、
`anthropic_bridge.go:885`、`responses_bridge.go:654/1128`。

它哈希的是 **`ir.StreamChunk` 重新序列化的结果**。而该结构体
（`internal/ir/stream.go:21-38`）含：

| 字段 | tag | 后果 |
|---|---|---|
| `ID` | `json:"id"`（**无 `omitempty`**） | 每个 chunk 唯一 ⇒ **内容相同也必然不同** |
| `Created` | `json:"created"`（无 omitempty） | Unix 时间戳 ⇒ 与内容无关 |
| `Model` | `json:"model"`（无 omitempty） | 可能被网关改写（auto 模型 / 模型名归一化） |

⇒ **它连「相同内容 ⇒ 相同 checksum」这个基本性质都不满足**，更谈不上验证「内容是否被篡改」。

### 累加器 B —— `ObservePayload`（`domains/hooks/audit/audit.go:658`），**responses 系 5 个生产调用方**

```go
sc.checksum = sha256.Sum256(append(sc.checksum[:], []byte(payload)...))
```

这是**链式**哈希 `h_n = SHA256(h_{n-1} || frame_n)` ⇒ **值依赖 SSE 分帧方式**。
调用方：`native_responses_capture.go:38/43/48`、`responses_stream.go:243/410`。

### 合起来的可观测后果

1. **跨协议不可比**：同一段语义内容，OpenAI/Anthropic 流式走 A，Responses 流式走 B ⇒ 值不同。
2. **A 路径上值依赖身份与时间**，不是内容。
3. **B 路径上值依赖分帧**，不是内容。
4. **两者都不能回答「响应体是否被篡改」** —— 而这正是字段名的承诺。
5. **今天不产生错误结论**，因为**没有任何读侧比对**：
   `subtle.ConstantTimeCompare` 全仓命中全部是别的用途
   （webhook 签名 `api/dingtalk_callback.go:248,258`、`feishu_callback.go:269`、
   `quota_recharged.go:144`；鉴权中间件 `middleware/auth_mw.go:101`、`admin_token_mw.go:75`；
   **分页游标签名** `admin/session_turns.go:67-85`；附件 MAC `admin/session_turn_attachments.go:116`；
   outbox 载荷签名 `internal/outbox/signature.go:13-38`），
   `request/response checksum` 的非测试引用**全部是搬运**（struct 字段 / SQL 参数 / API 响应）。
6. **危险形态**：它一旦被当作完整性证据使用——运维排查「这条响应被动过吗」、
   审计取证、按 checksum 对账——**会给出看起来很权威的错误答案**。

**修法方向登记为（不擅自动手）**：该列要么改名成它真实语义
（如 `response_stream_digest_v1` + 记录累加器/协议路径），要么统一到单一确定性算法
并补读侧比对。两者都改**对外可见的数据语义**，属待裁决。

---

## 六、⚠️ 收回对 `request_checksum` 的怀疑：那一侧其实是对的

本轮中途我曾判定「两个候选计算器都未接线、该列很可能恒 NULL」。**这是错的**，已收回：

- 在用的是 `(*EventBuilder).RequestChecksum(body)`（`audit.go:961-963`）= **`SHA256(body)`**，
  **纯请求体的确定性哈希**，语义正确；
  它是**链式 builder 调用**，三个协议 handler 都有：
  `handler.go:3271`、`messages.go:571`、`responses.go:547`（如
  `newAuditEvent(requestID).ClientModel(...).RequestChecksum(bodyBytes)`）。
- 真正零调用的是 `ComputeRequestChecksum(model, body)`（`audit.go:1176`）= `SHA256(model || body)`，
  只有 `audit_test.go:643-646`。
  它还有个**无分隔符拼接**的理论歧义（`model="ab",body="c"` 与 `model="a",body="bc"` 同值），
  但因为无人调用，**不构成现役缺陷** ⇒ 登记为死代码 + 命名陷阱（见 §八、§十一）。

`domains/session/v2/session_writer_v2.go:297` 把 `RequestChecksum` 列为「缺源字段」——
**这条注释与实测不符**（实测有 3 个生产调用方），已在台账登记为注释订正项（代码零逻辑，可直接改，留待下轮一并处理）。

---

## 七、⚠️ 本轮我自己更正了三次，每一次都是同一个失败模式

| # | 我一度写下的结论 | 真相 | 错因 |
|---|---|---|---|
| 1 | 「`RecordChunk`/`ObserveChunk` 生产零调用，`response_checksum` 生产唯一累加器是 `ObservePayload`」 | `ObserveChunk` 有 **12 个**生产调用方，且是**主流路径** | 用了 `Select-Object -First 30` **截断**的 `git grep` 输出，截断线恰好落在 audit 包内 |
| 2 | 「`EventBuilder.RequestChecksum` 生产零调用」 | 有 **3 个**（三个协议 handler） | 用了 `git grep "\.RequestChecksum("` 检索，**方法调用形态与字段引用混在一起**，且输出被我截断 |
| 3 | 「`ComputeRequestChecksum` 的调用点是 `messages.go:571` 等」 | 那些是**另一个函数** `RequestChecksum(bodyBytes)`；`ComputeRequestChecksum` 生产零调用 | **函数名包含关系**：`ComputeRequestChecksum` 包含 `RequestChecksum`，把名字长的那个当成了名字短的那个的调用方 |

⇒ 第 3 条**不是我的原创**：子代理先犯了这个错（它报告「调用点 `messages.go:571`、
`handler.go:3271`、`responses.go:547`」），我复核时未采信。
⇒ **一个符号 A 的名字包含符号 B 的名字时，任何按名字的检索都会把两者合并**。
⇒ 详见 playbook §186。

---

## 八、守卫：`internal/sqlguard` 新增 `response_checksum_semantics_test.go`（集合仍 14 包，零接线成本）

| 测试 | 职责 | 失败条件 |
|---|---|---|
| `TestResponseChecksumSplitStaysRecorded` | 逐个累加器打印生产调用方清单与数量，按**已接线的累加器个数**分类 | **0 个**接线（该列静默停止写入，无编译错误、无测试失败）；**1 个**时打印「语义分叉可能已消除，请人工确认后关闭待裁决 94」而**不转红** |
| `TestRequestChecksumCalculatorIdentity` | 钉住在用的是 `SHA256(body)` 那个算法 | 死掉的 `ComputeRequestChecksum` 获得生产调用方（会静默改变所有历史值的含义）；在用算法失去全部生产调用方 |
| `TestClassifyChecksumProducersCoversEveryState` | 表驱动覆盖 0/1/2/3 四种接线数 | 分类逻辑本身错 |
| `TestChecksumColumnsAreNotUsedAsIntegrityEvidence` | 打印两列的全部生产引用 | **不失败**（§157：0 命中只能当证据） |

**为什么用 `go/ast` 而不是正则**：本轮的门第一版正是被正则/形态问题坑的。
`productionCallSites` 同时覆盖**方法选择器**（`capture.ObservePayload` → `*ast.SelectorExpr`，
比对 **`Sel.Name`**）与**裸函数调用**（`ComputeRequestChecksum` → `*ast.CallExpr` 下的 `*ast.Ident`）——
**只认前者的话，包级函数会被报告成「0 调用方」而从未被真正看过**。

**⚠️ 门的第一版自己红了，而红的是尺子**：`productionCallSites` 第一版写成
`sel.X.(*ast.Ident).Name != name`，拿**接收者名**（`capture`）去比**方法名**（`ObservePayload`），
永远不成立 ⇒ 三个方法全被报成「未接线」。
用一个临时诊断测试（打印扫描文件数与命中）证明尺子错在接收者/方法名混淆后，才修对。
**修好之后门立刻报出新事实**（`ObserveChunk` 有 12 个生产调用方），进一步推翻了 §七 的第 1 条。

### 负控（全部实测）

| 负控 | 手法 | 结果 |
|---|---|---|
| **NC-L** | 在 `domains/hooks/audit/audit.go` 追加一个生产函数调用 `ComputeRequestChecksum` | ✅ 转红，精确点名 `audit.go:1337`，并打印「两个算法不可互换」；探针已回收，`git diff --stat` 复核为空，复跑复绿 |
| **NC-M（用表驱动替代改生产代码）** | 「1 个累加器 ⇒ 提示关闭待裁决 94」这条自失效分支若靠改 17 个生产调用点去验证，爆炸半径不值；若靠改门内记录的常量去验证，则什么都没验（§183） | ✅ 抽出 `classifyChecksumProducers` 纯函数，用 0/1/2/3 表驱动覆盖；输入是被测性质本身而非尺子输出（§171） |

### ⚠️ 门变慢也是尺子错的信号：本包 29.2s → 75.6s，当场修

14 包全量复跑时 `internal/sqlguard` 从 227 号的 **29.2s 涨到 75.6s**（预算 `-timeout=120s`，
余量从 91s 降到 44s）。真因是**我的门自己的问题**：`productionCallSites` 按名字逐个全仓 `Walk`，
每条测试调若干次 ⇒ **9836 个 `.go` 文件被解析了 8 遍**。

修法：一次 `Walk` 建全仓索引（`buildRepoIndex` + `sync.Once`），对 `trackedNames` 里的
6 个符号同时收集声明与引用。

| | 修复前 | 修复后 |
|---|---|---|
| `TestResponseChecksumSplitStaysRecorded` | 25.82s | **4.49s** |
| 其余 3 条测试 | 各 5–20s | **0.00s**（索引复用） |
| 三个累加器的生产调用方数 | 12 / 5 / 0 | **12 / 5 / 0（完全一致）** |

⇒ ⚠️ **门变快同样是尺子错的信号（§174）**，所以必须做对照：
**修复前后的数字逐个相同**，才说明这是纯性能修复而不是「判据变松了」（§181）。

**Why this guard pins, and why each condition is currently satisfiable**（判据设计说明见守卫文件头注释）

---

## 九、诚实边界

- **未起真进程、未连 PG/Redis/Docker、未执行 SQL** ⇒
  `request_checksum` / `response_checksum` 在**真实 live turn 上的填充率未测**
  （子代理亦明确标注为无法确认，需真库 fill-rate 查询）。
- **未跑通一条真实流式响应** ⇒ 「跨协议 checksum 不可比」是由**代码路径**推出的，
  未用两条真实响应对照验证。
- `ObserveChunk:37` 的 `chunkBytes, _ := json.Marshal(chunk)` **丢弃 error**；
  若 marshal 失败则 `chunkCount++` 已执行（`stream.go:30`）而 checksum **零贡献**。
  **未确认该 error 分支是否可达**（未核 `ir.StreamDelta/StreamUsage/StreamError` 内部是否含
  `json.RawMessage` 等可导致 marshal 失败的字段）⇒ **登记为 P3 可达性待查，本轮不下结论**。
- **未核** `durable_projection.go:14` 的第 5 个版本常量 `DurableProjectionInputVersionV1`
  为何**无任何 gate**（子代理 B 发现，本轮未展开）。
- `go test ./...` 全量未跑；**但已抽查 `domains/streaming` 的 `Audio|MCP` 子集，发现并发会话
  新推的 `TestMCPEarlyErrorsUseJsonRPCEnvelope` 3 个子用例红**（详见 §十二）；
  `core.hooksPath` 未设 ⇒ 未经 pre-push 门；**CI 从未运行**。
- 本轮**未改任何生产代码**。

---

## 十、编号与去向

- **新增待裁决第 94 条（P2）**：`response_checksum` 由两个语义不同的生产累加器共同写入
  （A：哈希含 `ID`/`Created`/`Model` 的 `ir.StreamChunk` 序列化，12 处；
  B：哈希原始 SSE 帧的链式值，5 处）⇒ 值依赖协议路径，跨协议不可比，
  且**不能用作完整性证据**。**修法方向登记为「改名到真实语义」或「统一到单一确定性算法 + 补读侧比对」**，
  两者都改对外可见数据语义 ⇒ 待裁决。
- **115 号必做项 ② 关闭**：`requestfact` 未落地的原因 = ADR-0003 显式决策，5 条前置件 **0/5 满足**，
  ADR 后两个包零提交。台账补记 ADR 与前置件现状。
- **订正 115/116 号一句过头结论**：「整套迁移设计停在设计阶段」⇒ 现役已自建 digest 契约并生产落库。
- **订正注释一处**：`session_writer_v2.go:297`「缺源字段 RequestChecksum」与实测（3 个生产调用方）不符。
- **守卫**：`internal/sqlguard` 新增 4 条测试，集合仍 14 包。
- **playbook**：§186–§187。

---

## 十一、playbook §186–§187

**§186 「名字包含」是两个符号之间最便宜的伪连接；而截断的检索输出是伪证据的第二个来源。**

本轮四次否定结论被推翻，三次源于同一个族：

1. 子代理把 `RequestChecksum(bodyBytes)`（`handler.go:3271`）读成
   `ComputeRequestChecksum(...)` 的调用方 —— 因为**后者的名字包含前者**。
2. 我用 `git grep "ObserveChunk" | Select-Object -First 30` 下结论「生产零调用」，
   而 30 行的截断线**恰好落在 audit 包内**（该包的定义与测试就占了前 20 多行），
   真正的 12 个生产调用方全在 `domains/streaming/` 里、**排在截断线之后**。
3. 我用 `git grep "\.RequestChecksum("` 判定「生产零调用」，
   而该词形**同时匹配方法调用与结构体字段**（`turn_writer.go:438` 是后者），
   两种形态在输出里长得一样。

⇒ **判别式**：
  - **符号名 A 包含 B 时，禁止用名字检索区分它们**。必须用 AST 取
    `CallExpr.Fun` 的**具体节点形态**（`*ast.SelectorExpr` vs 裸 `*ast.Ident`），
    并比对**被选中的名字**（`Sel.Name`）而不是**接收者的名字**。
    本轮门的第一版正是把接收者名当方法名比，于是三个方法全部「零引用」——
    **一个永不成立的判据，报告出的 0 命中与真相一模一样**。
  - **任何带 `-First` / `head` / 管道截断的检索输出，不得作为否定结论的依据**。
    截断只对「已找到至少 N 个」的正向结论是安全的。
    判别式：**看结论的方向**——正向结论可以基于样本，否定结论必须基于穷尽。
  - **词的形态数决定检索的可靠性**：`\.RequestChecksum\(` 覆盖 2 种形态，
    `RequestChecksum` 覆盖更多但也覆盖更多无关。**写下你搜的词，并列出它覆盖的形态数**。

⇒ 与 §185（否定结论必须附「命中数 + 逐条归类」）同源，
  本条补的是它的**上游**：「逐条归类」的前提是**你真的看到了那 N 条**。

**§187 自失效守卫的分支必须能被验证——把判定抽成纯函数，而不是去改生产代码制造条件。**

227 号引入的形态（门在缺口被补上时打印提示而不转红）有一个弱点：
**它的「提示分支」在本轮无法被验证**——要让门走进那个分支，
得把 17 个生产调用点全改掉（爆炸半径与风险都不成比例）。

⇒ **修法**：把判定逻辑抽成**纯函数**，输入是**被测性质本身**（接线的累加器个数），
  用表驱动单测覆盖它的每一个状态（0/1/2/3），**不碰生产代码**。
⇒ ⚠️ **两种错误的替代做法都要避免**：
  - ❌ 「改门里记录的常量」制造条件 ⇒ 什么都没验（§183：判据不得复用尺子的返回值）。
  - ❌ 「因为难验证就不验证」⇒ 自失效分支会**静默腐烂**：判定逻辑改坏了没人知道。
⇒ **判别式**：写下任何「将来会走另一条分支」的守卫时，
  问「**那条分支的判定逻辑，单独拿出来能不能测**」。
  能 ⇒ 抽出来测；不能 ⇒ 重新设计分支的触发条件，让它可测。
⇒ 与 §171（验证尺子的断言不得复用尺子的返回值）配套：
  §171 禁止「用尺子的输出验证尺子」；
  这条禁止「用**改尺子**的方式验证尺子的另一条分支」。

---

## 十二、⚠️ 并发会话合并记录：轮次号撞车 + main 上有一颗红的测试

### 12.1 轮次号撞车

提交前 `git fetch` 发现 origin/main 前进一个提交，**对方也自称「228 号 / R89-ES」**，
主题是 `feat(audio)` 的 MCP 端点错误码（commit `0a9f081dc`）。

- **文件名不冲突**（`228-R89ES-feat-audio…md` vs 本轮 `228-R89ES-requestfact…md`），
  `git merge origin/main` **零冲突**合并（未强推）。
- **台账编号不冲突**：`4.140`（226）、`4.141`（227）、`4.142`（本轮）连续；
  **对方的文档没有进台账，也没有进 README 索引**。
- ⚠️ **本轮不重编号**：重编号会与已推送的历史链条制造更多不一致。
  但**台账里「228 号」目前指向两份不同文档**，已在报告头部与台账登记提示，
  引用时**必须带主题/文件名，不能只写「228 号」**。

### 12.2 main 上当前有一颗红的测试（来自并发会话）

`domains/streaming/audio_endpoints_test.go`（对方新增 82 行）的
`TestMCPEarlyErrorsUseJsonRPCEnvelope` **3 个子用例红**：

```
audio_endpoints_test.go:336  status = 400, want 200
audio_endpoints_test.go:351  response not valid JSON-RPC envelope:
      json: cannot unmarshal string into Go struct field .error.code of type int
```

即该测试断言「MCP 早期错误应是 transport 200 + JSON-RPC envelope（`-32700` / `-32600`）」，
而生产实测返回 **400 + `{"error":{"code":"invalid_json",…}}`**（HTTP 风格错误信封）。

**已排除是我的合并所致**：`git diff 6750caa01..HEAD --stat -- domains/streaming/`
**只有该测试文件新增 82 行，非测试文件改动为空**。

⚠️ **本代理不擅自动手**：该文件属并发会话的在制品（其文档标题即「定义了两个码却从未用上」，
像是刚登记的待裁决项），改它会与对方会话的进行中工作撞车。

⚠️ **影响面已量化**：该包**不在 `GUARD_PACKAGES` 内**
⇒ **`make guards`（14 包）仍全绿**；但 `go test ./...` 会红。

### 12.3 合并后复验（零冲突 ≠ 结论不变）

| | 结果 |
|---|---|
| 14 包守卫（`-timeout=300s`） | **全绿**，`sqlguard` 21.6s、`sql/schema` 43.8s |
| 本轮守卫的三个数字 | **12 / 5 / 0**，与合并前完全一致 |
| `domains/streaming` `Audio\|MCP` 子集 | ❌ 红（见 §12.2，非本轮引入） |
