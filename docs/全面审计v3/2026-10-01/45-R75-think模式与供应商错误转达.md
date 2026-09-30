# R75 —— 供应商错误处理与 think 模式错误转达（第一部分）

日期：2026-10-01
范围：`domains/streaming`（think 注入与生存流错误转达）、`domains/streaming/survival_wiring.go`、`action_bridge.go`
触发：objective「全面检查连接供应商端的请求的错误处理逻辑……**并且在有备用的情况下，需要以 think 或类似不影响会话的模式返回给客户端，并且不会中断请求流程。**」+ 注意事项「原则上所有的操作不要影响客户端的观感，要通过思考模式将信息进行转达，但不要影响会话，确保会话持续流畅地进行」

本报告 §1–§6 覆盖 R75-B（think 模式）。§7 覆盖 R75-A（错误落库 + 凭据详情展示）——该子代理长时间无输出未返回，§7 全部结论由主代理独立查证，其中最重的一条在本机 PostgreSQL 上实测复现。

---

## 0. 结论

**PARTIAL —— objective 的核心诉求在生产路径已真正达成。**

「上游失败 → think 注释转达 → **真实重试到备用凭据** → 唯一答案帧落地，无「假装成功」」这条链是通的，本轮实测确认。本轮**已修 1 个 P2**（最需要解释的失败类缺 think），另修 1 个被我的改动暴露的门缺陷（判据过宽），**出方案 2 项 P2**。

| # | 缺陷 | 严重度 | 状态 |
|---|---|---|---|
| 1 | 已提交内容后中断（resume_blocked）不发任何 think，客户端只拿到裸 error 信封 | **P2** | ✅ 已修 |
| 2 | 语义帧 Anthropic 缺 `content_block_start`、chat 的 `finish_reason` 为空串 | P2 | ⏸ 生产死代码，接线时升 P1 |
| 3 | `action_bridge.go` 声称「已在 R36-DEBT 登记」，该文档并无此条目 | P2 | ⏸ 出方案 |
| 4 | `handler.go` think 格式注释与实现矛盾（声称 `event: thinking + data:`，实为 `: thinking:` 注释帧） | P2 | ⏸ 出方案 |
| 5 | 凭据详情「最近失败」把非唯一键当唯一键 JOIN：扇出 **且** 上游 body 贴错行 | **P1** | ✅ 已修 + 真库门 |
| 6 | `provider_error_details` 整条聚合管线零 UI 消费方 | P2 | ⏸ 出方案（属产品裁决） |
| 7 | `getProviderErrorStats` 注释声称「凭据详情面板需要」，与实际读端矛盾 | P2 | ✅ 已修（注释更正） |
| — | `TestDurableNonChatEndpointsLeaseLoss` 的内容哨兵用 `"hi"`，被 `thinking` 命中 | — | ✅ 已修（判据过宽，收紧而非改门） |

---

## 1. objective 核心诉求的实测结论

子代理端到端实测（我复核了实现路径）：

```
attempts=3 succeed=true decision=succeed/success
WIRE:
    : keep-alive
    : thinking: "正在等待可用节点并重试（第 1 次，原因=recoverable_candidate）"
    : gw-survival-keepalive
    : thinking: "正在等待可用节点并重试（第 2 次，原因=recoverable_candidate）"
    : gw-survival-keepalive
    data: {"choices":[{"delta":{"content":"REAL ANSWER"}}]}
    data: [DONE]
```

3 次上游调用（2 失败 + 1 备用成功）、2 条 think、**答案帧恰好 1 个**。无备用时 `succeed=false` + 干净 error 终态。

**「不中断」成立，「假装成功」不存在。** 机制在 `survival_coordinator.go:815` 发 think 后，于 retry 分支走 `Refresh()` + `continue` 真正再执行一次 `ExecuteAttempt`（`survival_wiring.go:131-144` 的 `Refresh` 重新解析候选）。

---

## 2. 已修

### 2.1 resume_blocked 不发任何 think 【P2】

**位置**：`domains/streaming/survival_coordinator.go:901`（resume_blocked 分支）

**事实**：这是**唯一不发出任何传输帧**的失败类。客户端手里握着部分答案（已 commit），却只收到一个裸 `event: error` 信封，无法区分「网关有意停止」与「连接被截断」。

objective 要求的「以 think 转达」在最需要解释的一类失败上缺失——**客户端观感最差的一类恰好是唯一无 think 的一类**。

**修法**：在该分支的 `renderTerminal` 之前发一条 think。

**刻意不复用 `notifyRetry`**：它渲染「正在等待可用节点并重试（…，等待 0s）」，而 `ADR-Disp-003` 明确禁止已提交后透明重试（否则客户端看到重复答案）。**先承诺再停止比沉默更糟。** 因此在 wiring 层为该 reason 单独出文案：

> 本次回答已输出部分内容后中断，网关不会透明重试以避免重复；请开启新会话继续。

reason 走内部闭集常量 `partial_answer_delivered_stop`，不引入新词表。

**变异验证**：去掉 think 后 `TestR74_ResumeBlockedEmitsThinkNotice` 红（`notices=[]`）。

> 变异脚本第一次没切中代码块导致「仍绿」，重做（用 `\n\t\t\t}\n` 锚定而非首个 `}`）后确认有判别力。**变异没红的第一解释应是「变异没生效」，不是「门没覆盖」。**

### 2.2 被我的改动暴露的门缺陷（判据过宽）【非缺陷，是门的问题】

**位置**：`domains/streaming/durable_nonchat_endpoint_test.go:268`

上一提交补 think 后，`TestDurableNonChatEndpointsLeaseLossRendersOneNativeTerminal` 转红。按纪律先假设门是对的，查下来**门确实过宽**：

```go
if strings.Contains(out, "hi") || ...   // 内容哨兵
```

传输注释帧是 `: thinking: …`，而 **"thinking" 里含有 "hi"**——门被 SSE 协议关键字本身触发，与内容是否重发无关。

**判据意图是对的**（「中断的尝试的内容没有被重发」），所以**收紧判据而不是放弃 think**：改为检查执行器写入的那一整帧 `tc.frame` 而非两字母片段。`tc.frame` 随用例不同（messages 是 `content_block_delta`，responses 是 `response.output_text.delta`），一条断言覆盖两个协议。

**判别力实证**（直接对比两个判据）：

| 场景 | 旧判据 `"hi"` | 新判据 `tc.frame` |
|---|---|---|
| 仅 think 帧（无内容重发） | **误报 = true** | 正确 = false |
| 含内容重发（真缺陷） | true | **true** |

旧判据在真缺陷时能报是**巧合**（它对一切含 "hi" 的串都报），在只有 think 帧时也报（假警报）。新判据只在真缺陷时报。

---

## 3. 出方案

| # | 缺陷 | 为什么不本轮修 |
|---|---|---|
| 2 | 语义帧（`action_bridge.go:337-368`）：Anthropic 只发 `content_block_delta` 无前置 `content_block_start`、index 硬编码 0；chat 的 `finish_reason` 为空串（OpenAI 规范要求 mid-stream 为 `null`） | **生产死代码**：`gatewayActionBridge` 在 `cmd/gateway/main_dispatch.go:40` 仅 `var` 声明、全仓无赋值点；`NewActionBridge` 的调用方仅 `action_bridge_test.go`。已双重 grep 确认。改它等于改一段跑不到的代码；若 owner 接线则升 P1 |
| 3 | `action_bridge.go:1-4` 声称「Registered as cleanup candidate R36-DEBT in `docs/audit/2026-09-17-r36-24h-audit-round.md` §四」，该文档 §四 的 6 条遗留登记中无此条目（已亲读全文） | 需先确认该登记是否记在别处；改文档前要问 owner 这个债务项是否真实存在 |
| 4 | `handler.go:4436-4438` 注释写 `event: thinking + data: {...}` 且「Zod skips unknown event names」，实现是 `: thinking: <json>` 注释帧；同文件 `:405-414` 已明确记录 `data:` 方案**是错的**（Zod `invalid_union`） | 与 2 同属死代码路径的注释；且 `:402-403`、`:406` 也有同类失真，宜一并处理 |

---

## 4. 已核对确认没问题的部分

- **三协议 think 一致**：`handler.go:4439`、`messages.go:742`、`responses.go:732` 三处 `OnNodeJump` 闭包**逐字同构**。`responses_bridge.go` 经 grep 确认**不含任何 think 注入**（仅 2 处注释提及），转达集中于 handler，无重复通道。
- **SSE 帧合法性**：`: keep-alive\n\n: thinking: "…"\n\n` — 无 `data:` 行、无裸换行（`json.Marshal` 转义）、`\n\n` 正确分帧。
- **不污染答案**：答案帧恒为 1；`action_bridge_test.go` 的 `TestActionBridgeNeverPollutesAnswerStream` 已固化。
- **中文字段污染**：think 正文全部是受控中文模板 + 枚举，**无英文技术词当正文**。上游 err 中的 `sk-` 密钥、内部 URL、用户 prompt **均未**进入 wire。
- **pause 不吞失败通知**：`pause()` 只 gate 心跳 ticker（`stream_session.go:80`），`writeThinking` 走 `WriteTransportFrame` 无 paused 检查。
- **并发无撕裂**：8 goroutine × 100 think + 100 答案帧，`-race` 干净，800 think 帧零 torn。`serializedResponseWriter` 单一 `SerializedStreamWriter` 串行化成立。
- **生存协调器三协议均已接线**：`handler.go:4622`、`messages.go:831`、`responses.go:832`。
- **已提交后不重复内容**：committed resume_blocked 实测 `partial answer` 出现 1 次。
- **非流式无 think 通道**：改走 `FailoverNoticeHeader` 响应头，drop 计入 `DispatchNoticeDroppedTotal`，注释与实现一致。

---

## 5. 子代理主动撤回的一条误判（记录在案）

R75-B 一度把「Anthropic / Responses 终态帧没有 `message_stop` / `[DONE]`」报为协议缺陷，随后**自行撤回**：该形态是全项目钉死的约定（`native_response_hook_integration_test.go:60-62` 显式 pin 三协议终态帧），**非 survival 特有**，非生存路径用同样形态。撤回理由充分，本轮未追查。

**我没有独立复核这一条**（涉及真实 Anthropic SDK 行为，本地无法验证），如实登记为「未验证」。

---

## 6. 验证

```
go build ./...                                    exit 0
gofmt -l                                          空
go test ./domains/streaming/ -run 'TestR74_|TestSurvival|TestDurableNonChat'   ok
```

**变异验证 2 处**：
- 去掉 resume_blocked 的 think ⇒ 红（`notices=[]`）
- 判据退回 `"hi"` 哨兵 ⇒ 红（证明收紧是必要的）

---

## 7. R75-A：供应商错误落库与凭据详情展示（主代理独立复核）

R75-A 子代理长时间无输出未返回，本节结论**全部由我自己独立查证**，其中最重的一条在本机 PostgreSQL 上实测复现（见 §7.1）。

| # | 缺陷 | 严重度 | 状态 |
|---|---|---|---|
| 5 | 凭据详情页「最近失败」把非唯一键当唯一键 JOIN：行数扇出 **且** 上游 body **贴错行** | **P1** | ✅ 已修 + 真库门 |
| 6 | `provider_error_details` 整条聚合管线零 UI 消费方（唯一读端端点无调用方） | **P2** | ⏸ 出方案（属产品裁决） |
| 7 | `getProviderErrorStats` 注释声称「凭据详情面板需要」，与实际读端矛盾 | **P2** | ✅ 已修（注释更正） |

### 7.1 【P1】JOIN 键非唯一 → 扇出 + 上游响应体贴错行

**位置**：`admin/vendor_credential_error_handlers.go` 的 `loadVendorRecentFailures`

**根因链**（每一环都单独核实过）：

1. `candidate_failure_logs` **没有任何唯一索引**（schema + 全部 migration 检索确认）。文件头注释「One row per (request_id, credential_id, raw_model_name, attempt_index)」是**设计声明，不是约束**。
2. 同一次 dispatch 内，**准入降级行与随后的上游失败行共用 `(request_id, credential_id, attempt_index)`**：
   - `logDispatchPreflightRejection`（`executor_dispatch.go:1356`）写第一行，但它是**独立函数**，写不到 `forwardForDispatch` 闭包里的 `failureLogged`（`executor_dispatch.go:711`）；
   - fp 槽饱和是 `degraded_continue`（`executor_dispatch.go:772-792`）：记完继续跑上游，上游再失败，`executor_dispatch.go:1085` 那个 `!failureLogged` 分支就写第二行；
   - 既有测试 `TestForwardForDispatch_LogsFpSlotSaturation` 正好构造了这个场景，却只等**第一条** insert 就断言 kind，**对第二条完全沉默**。
3. 旧 SQL 把这个三元组当唯一键 `LEFT JOIN`。

**真库实测（本机 PG 17，`llm_gateway` 库，事务内播种后 ROLLBACK）**：

```
2 次 dispatch 尝试 = 4 条不同失败
旧 SQL 实际返回 4 行（0 次尝试时为 2 条 → 4 行）：
    fp_slot_saturated | upstream body preview     ← 错配！
    fp_slot_saturated | (空)
    network           | upstream body preview
    network           | (空)
```

后果两条同时成立：

- **扇出**：12 次尝试 = 24 条不同失败，外层 `LIMIT 10` 只给出 **5 条**不同失败。
- **错配（更严重）**：`fp_slot_saturated` 是网关侧准入事件、本就没有上游 body，却被贴上了 `network` 失败的上游响应体。运维按错误类型排查时，**看到的是另一条错误的响应体**——这不是显示瑕疵，是会误导排障方向的错误信息。

**修法**（两处同施，缺一不可）：
- 外层先子查询取 10 条**不同**失败，再回补 ⇒ `LIMIT` 语义回到「10 条失败」；
- 回补改 `LEFT JOIN LATERAL (...) ON true ... LIMIT 1`，定位键补 `cf.error_kind = u.error_type`（两侧来自同一个 `buildRow`，天然同值），并要求 `preview IS NOT NULL`（准入行的 preview 恒为 NULL，它因此不会再从兄弟行借 body）。

**真库当场否掉的第一版修法**（记录在案，因为它是「预防性提醒不如真跑一次」的实例）：第一版子查询用了 `SELECT *`，真库直接报

```
ERROR:  cache lookup failed for attribute source of relation 1906997
```

这与 `bg/provider_error_aggregator.go:298-309` 记载的是**同一个 citus-columnar planner 缺陷**（unified 视图跨 columnar 分区时 `SELECT *` 触发 XX000）。若不做真库验证，这个修法会把凭据详情页打成 500——**比原缺陷更严重**。已把该约束写进常量注释与离线门。

### 7.2 验证

```
go build ./...                                        exit 0
gofmt -l <本轮改动 5 个文件>                            空
go test ./admin/ ./deploy/sql/verify/ ./bg/            ok（67.1s / 0.7s / 25.9s）

真库门（LLM_GATEWAY_SUPPLIER_PG_DSN 指向本机 PG 17）：
  go test ./deploy/sql/verify/ -run TestVendorRecentFailuresSQL -v   PASS
```

**新增两道门**：
- `deploy/sql/verify/vendor_recent_failures_pg_test.go` —— **真库门**。跑的是导出常量 `admin.VendorRecentFailuresSQL` **本身**（不是副本），在单事务内播种后 ROLLBACK，对目标库零残留、不建任何对象。这是本轮唯一能证伪「不扇出 / 不错配 / LIMIT 限失败条数」这类**纯 SQL 连接语义**的形态。
- `admin/vendor_recent_failures_sql_test.go` —— **离线契约门**。真库门以 DSN 门控、无 PG 即跳过，本门补上离线那一段。**边界已在文件头写明**：它只能证明「四要素都在」，证明不了「因此语义正确」，把它当充分条件读是错的。

**变异验证 5 处，全部红→绿**：

| 变异 | 真库门 | 离线门 |
|---|---|---|
| 退回旧 SQL（普通 LEFT JOIN） | 🔴 8 行 ≠ 4 行 | — |
| 抽掉 `cf.error_kind = u.error_type` | 🔴 预览跨尝试错配 | 🔴 |
| 抽掉 `preview IS NOT NULL` | — | 🔴 |
| `LEFT JOIN LATERAL` → 普通 `LEFT JOIN` | — | 🔴 |
| 抽掉 LATERAL 内 `LIMIT 1` | — | 🔴 |

**一个「变异后仍绿」的诚实记录**：LIMIT 子测试第一版断言的是「返回 10 行」，变异后**仍然绿**。第一解释本应是「变异没生效」，但另一子测试已证明变异生效——所以第二解释成立：**是我的断言在测错东西**（旧 SQL 扇出后 LIMIT 同样返回 10 行，只是那 10 行只含 5 条不同失败）。改为统计**不同** `(attempt, kind)` 后立刻变红。教训同族：断言要钉住**语义量**，不是**行数**。

### 7.3 【P2】`provider_error_details` 整条管线零 UI 消费方

**事实**（全仓检索，「0 命中问两次」已执行）：

```
非测试代码中 "error-stats" 仅 2 处命中：
  admin/providers.go:494          路由 case
  admin/provider_credential.go:1196  文档注释
provider_error_details 的生产 SELECT 仅 1 处：
  admin/provider_credential.go:1292  （即上述端点）
```

前端凭据详情页（`web/src/views/provider-detail/ErrorDetailTab.vue` → `api/vendor-credential-error.ts`）调的是 `GET /api/vendors/credentials/{id}/error-detail`，其 SQL 读 `supplier_errors_unified` + `candidate_failure_logs_unified`（`vendor_credential_error_handlers.go:234/312`），**从不调用 `error-stats`**。

即：`bg.ProviderErrorAggregator`（约 490 行 SQL：advisory lock + 单调水位 + 两趟 staging + replace-upsert，配 7 个契约测试与 1 个真库集成测试）**在正常跑、数据在写，但没有面板读**。同时系统里存在**两条并行的供应商错误聚合链路**，只有 `supplier_errors_*` 那条真正闭环。

**为什么只注释不删**：下线聚合器、或把 `error-stats` 接进凭据详情页，两条路都改变现状且属产品裁决（前者删一条已上生产的聚合管线，后者新增 UI 数据源）。已在两处最显眼位置把事实钉准：
- `bg/provider_error_aggregator.go` 文件头——明写「唯一生产读端无调用方，不要因为注释详尽、测试齐全就默认产出有人在消费」；
- `admin/provider_credential.go` 的 `credential_id` 过滤注释——**原文声称「这正是凭据详情面板需要的」，该说法是错的**，已更正并附核实路径。

---

## 8. 一次操作失误的记录

为验证那个测试是否本轮就红，我执行了 `git stash`——**它抓到了并发会话的 WIP**（taskprofile 前后端 6 个文件）。`stash pop` 因 `menu-config.json` 的 `exported_at` 时间戳冲突而中止恢复，留下 `UU` 冲突状态。

处置：核对 stash 内容完整后，用 `git checkout stash@{0} -- <5 个文件>` 把并发会话的改动原样取回工作区（`menu-config.json` 保留 upstream 的新时间戳，`exported_at` 是自动生成元数据，取新值正确），确认工作区与 stash 逐文件无差异后才 drop。

**结论：并发会话的 WIP 未丢失，已完整恢复。**

**教训**：`git stash`（不带 pathspec）在有并发会话的 worktree 里是**破坏性操作**——它会连别人的未提交改动一起抓走。本会话此前每次 stash 都带了明确的 pathspec 或在确认工作区干净后才用；这次为图省事省掉了前提检查。今后在本仓库：`git stash` 之前必须先 `git status --porcelain` 确认为空，否则一律带 pathspec。

---

## 9. 能力边界

- **无运行时/线上验证**：全部结论来自静态审读 + 注入式单测。未在 154/252 生产验证真实客户端观感。
- **「Zod 会拒收 `finish_reason:""`」是基于 `handler.go:405-414` 记录的既有研究推断**，未实跑 opencode/zcode SDK。
- **D2 的严重度依赖「生产未接线」这一前提**：已双重 grep 确认源码侧，但未核对 154/252 实际二进制是否含旧版 ActionBridge 接线。若线上曾接线过，该项应重判 P1。
- **未追进 `internal/liveactions` 的 Detail 字段**验证「只含 id/标签/计数」的承诺（该链路当前亦为死代码）。
- **R71–R74 域未触碰**，不评价其结论。
