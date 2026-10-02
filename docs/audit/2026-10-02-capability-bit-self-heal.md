# 能力位自愈 + 读错误不再被当默认值（2026-10-02，第三轮遗留 #1 收口）

**触发**：第三轮（`2026-10-02-vapeur-protocol-round3.md`）收口后遗留清单第 1 条——
「能力位需要证据才会开……生产上应由探针自然填充……要让全部模型常态走
responses，需要一个按绑定的能力位回填任务（**未做**）」。本轮做掉它，并处理
同一遗留里那句「让 `GetSupportsResponses` 的读错误不再被当默认值」。

基线：`git fetch` 后 HEAD=`0f1cb412d`，origin/main 已前进 33 个文件
（并行会话的 audit 轮 + s4 门），**与本轮所改文件不重叠**。工作区仅 VERSION /
version.json / web/public 两个版本文件被并行会话改动，未触碰。

## 一、动手前先确认的「这张表没人写」

不是照抄第三轮结论，重新核过全树：

```
$ grep -rln credential_model_capabilities --include=*.go .
# 读方：provider/client.go:1664（一条 SELECT）
# 写方：0
```

`sql/migrations/startup/612` 的注释自认 *"until an external probe populates it"*，
613 为流式拆了独立键。**那个 probe 不存在，这是一次全树确认，不是推断。**

## 二、根因（本轮新增，与第三轮不同的一层）

第三轮把「能力位不生效」归到三件事上，其中 Redis 侧（slide_window 解码）已修。
**剩下的那一层是：即使 Redis 侧完全正常，那张表仍然恒空**，于是
`cand.SupportsNativeResponses` 对每个模型都是 false，闸门的正向永远打不开——
第三轮为此加的正向闸门（`DurableVerdict`）当时只能靠**预置** Redis 结论才有效。

所以本轮修的不是「读不出来」，是「**从来没人写**」。

## 三、改了什么

| # | 文件 | 内容 |
|---|---|---|
| 1 | `bg/capability_backfill.go`（新） | 按 binding 的回填任务 |
| 2 | `bg/probe_http.go` | `httpProbeResult.bodySample`：2xx 也保留真实上游帧 |
| 3 | `domains/streaming/executors/responses_durable_verdict.go`（新） | 持久结论解析：读错误 ⇒ 回落 SQL 结论 |
| 4 | `domains/streaming/executors/executor_chat.go` | 两方向闸门合并成一次解析 |
| 5 | `provider/client.go` | 候选投影加 `supports_native_responses_known`（三态） |
| 6 | `cmd/gateway/main.go` | 两个互斥装配点各起一份回填任务 |

### 3.1 回填任务

对每个 `openai-responses` 协议的绑定定期跑**现有**探测器
（`probe_http.go:singleResponsesPing` + `providercap.ResponsesUnsupportedError`），
把结论写回 `credential_model_capabilities`，并镜像进 Redis 能力键。

三条不可让步的约束，代码里逐条对应注释：

1. **不新造探测协议。** 请求体、端点解析、状态码判定全部复用 `singleResponsesPing`。
   任务自己只做「扫哪些绑定」与「把结论写哪」。
2. **无证据不写。** `singleResponsesPing` 只在 2xx 与 `ResponsesUnsupportedError`
   两种情形给出 `supportsResponses`；网络错 / 5xx / 429 / 401 一律 nil，此时
   **一行都不写**。把「没探到」写成 `supported=false` 就等于用默认值关掉一个
   可能完全正常的绑定——那正是这张表最初腐烂的形状。
3. **只作用于非流式。** 能力键只有 `native_responses_nonstream`；流式键在本
   文件的**字符串字面量**层面就不出现（有用例守着，见 §五）。

单次尝试而非 `probeWithRetry` 的重试阶梯：那条阶梯服务的是「这一发失败要不要
再试」的故障判定，而本任务要的判定只有「拿到证据没有」——网络错/5xx/429 都不
产生证据，重试只是拖慢整轮。

### 3.2 读错误不再被当默认值

`resolveDurableResponsesVerdict` 的三条规则：

- 读错误 → SQL 持久结论，`degraded=true`
- Redis 有结论 → Redis 结论
- Redis 无结论 → SQL 持久结论

为此 `provider.Candidate` 增加了 `SupportsNativeResponsesKnown`：原来的
`COALESCE(cmcap.supported, FALSE)` 把「没回填过」和「回填了但为 false」压成同一个
FALSE，无法回答「有没有证据」。新投影 `(cmcap.id IS NOT NULL)` 才回答得了。

**同时把降级日志从 `slog.Debug` 抬到 `slog.Warn`**。这不是润色：第三轮那次
失效的全部症状就是「读错误只写 debug」，所以没人看见。

## 四、一处必须说清的限界（否则这段就是误导）

在今天两个闸门的前置条件下，「回落 SQL」与「无结论」在**路由结果上恰好一致**：

- 闸门 1（降级）只在 `nativeNonStream || nativeStream` 时进入，而
  `nativeNonStream` 由 `cand.SupportsNativeResponses` 推出 ⇒ SQL 必为 true ⇒
  回落 true 与不动，同为「不降级」；
- 闸门 2（开闸）只在两者皆 false 时进入 ⇒ SQL 必为 false ⇒ 同为「不开」。

**所以本轮在路由行为上的真实增量来自回填任务（表不再恒空），不来自这个回落。**
回落是把「读错误 ⇒ 查另一个存储面」写成一条可测判据，而不是依赖两个存储面
碰巧一致这一隐式事实。它承重的场景是 ① Redis 键丢失/重启后闸门不再静默失效，
② 后续任何放宽闸门前置条件的改动自动获得正确的降级来源。

这条限界是**被变异验证测出来的**，不是推演出来的，见 §五。

## 五、测试与变异验证

### 5.1 fixture 全部是实测上游帧

| 用途 | 帧 | 来源 |
|---|---|---|
| 正向 | `resp_0c7d53a4…` codex responses 200 | 直连 api.vapeur.ai 录制 |
| 负向 | `{"error":{"message":"该供应商不支持 Responses API",…}}` 400 | 同上，claude 三兄弟实测拒绝形状 |
| 不写 | 5xx / 401 / 参数形状 400 | 前两类实测；第三类是判据边界样本 |

### 5.2 判据自检（本轮要求的「先自检判据」）

`TestBackfillProbePreconditionSelfCheck` 先喂已知失败样本（真实 400 不支持帧
必须判出 false，不能是 nil），确认能判红；再喂正常样本（真实 200 帧必须判出
true），确认不误报。**理由**：如果 `singleResponsesPing` 对所有输入都返回 nil，
「无证据不写」三条会全绿而毫无意义。

端到端那条同样有自检：先用 `GetSupportsResponses` 确认预置的畸形载荷**确实**
报错，否则测不到降级路径。

### 5.3 变异验证（第一轮四处；复审新增第五处见 §7.5，四处，串行，每轮确认标记恰好 1 处、还原后归零）

| 变异 | 结果 |
|---|---|
| A「无证据」当默认 false 写进去 | `TestBackfillLeavesRowUntouchedWithoutEvidence` 3 条子用例红 @ `capability_backfill_test.go:234`（`written = 1, want 0`） |
| B 读错误回落默认值 | `TestResolveDurableResponsesVerdict` 2 条红 @ `responses_durable_verdict_test.go:86`，值 `(false,false,true)` vs `(true,true,true)` |
| C 降级日志 warn → debug | `TestExecuteOpenAI_DegradedCapabilityReadUsesSQLConclusion` 红 @ `responses_durable_verdict_test.go:162` |
| D 给 2xx 判定加「必须有 `output_text`」内容门控 | `TestBackfillReasoningModelLengthTruncationStillCountsAsSupported` 红 @ `capability_backfill_test.go:344` |

四处的判别签名都干净：变异 D 下**只有那一条**变红（codex 的 200 帧含
`output_text`，不受内容门控影响），说明该用例精确承重在它声称的那条性质上。

四处均已还原（第五处见 §7.5），还原后全仓 `grep -rn MUTATION-PROBE` 无输出，
`bg/probe_http.go` 回到只有 `bodySample` 一处改动（+11 行）。

### 5.4 推理型模型的 token 上限：本轮为何不受影响（并已实测钉住）

第三轮遗留 7 记过：探针的 `max_output_tokens=32` 会被 glm-5.2 这类模型吃光，
表现为 **HTTP 200 + `finish_reason:"length"` + 空 output**，让任何用小 token
上限做**内容**断言的探测/回归产生假失败。

本轮问的是另一个问题——「这个供应商的 `/v1/responses` 通不通」，判据是**状态码**
不是有没有出字：

```go
if resp.StatusCode >= 200 && resp.StatusCode < 300 {
    result.supportsResponses = boolEvidence(true)
}
```

所以 length 必须**仍然判正**。这不是推演：`TestBackfillReasoningModelLengthTruncation
StillCountsAsSupported` 用 glm-5.2 的实测形状（`status:"incomplete"`、
`output:[]`、`incomplete_details.reason:"max_output_tokens"`）跑真实探测器，
断言写出 `supported=true` 且真实帧进了 `evidence_json`；变异 D（给判定加内容门控）
让**且只让**这一条变红。

**为什么本轮不把 32 改成 ≥256**：改了会让每个探测的出站成本上去，而能力位判定
根本不看内容；真正需要 ≥256 的是**矩阵/自检脚本的内容断言**，那属于第三轮遗留 7
的其它场景，本轮没碰那些脚本，故未改。若日后有人给能力位判定加内容门控，
上面那条用例会立刻红。

### 5.5 变异验证测出来的东西（两处，都不是原计划里的）

1. **变异 B 下，端到端那条没有红。** 它与解析器的红行不同——这正是 §四 那条
   限界的直接证据：两个存储面在今天的前置条件下结果一致，端到端测不出回落
   语义的变化，只能测出降级**信号**（即变异 C）。若不真跑这一轮变异，我会把
   §四 写成推测。
2. **正向证据的 `body_sample` 曾经恒为空。** `classifyHTTPResponse` 在 2xx 上
   不填 `errMsg`，所以「拿 errMsg 当证据」等于给每条**正向**结论写一个空字符串
   ——证据字段恒空，正是审计里反复批的装饰字段。测试第一次跑就红了。修法是给
   `httpProbeResult` 加 `bodySample`（探测器收到的原始帧，2xx 也有）。

## 六、回归

`bg` / `domains/streaming/...` / `cmd/gateway` / `provider/...` /
`credentialfpslot` / `sql/migrations/startup` 全绿。
`TestCandidateQuerySQL_Shape`、`TestCandidateNativeResponsesCapabilityContract`
等 SQL 形状门未受影响。

### 6.1 一条**不是**本轮引入的红（判定过程留档）

`admin` 包有一条红：`TestNoUnregisteredVPaddedColumnReader` 报
`不可归属豁免 "bg/auto_route_settle_worker.go:id" 已失效`。

判定不是靠「看着不像我改的」，而是靠一次干净的对照：
`git worktree add /tmp/gw-head-probe HEAD` 建了一个不含本轮任何改动的检出，
在**那条检出上跑同一条测试，同样红、同样报同一条失效豁免**。
⇒ 该红在 HEAD 上既存，与本轮无关。

来源已定位：并行会话在本轮进行中推进了 3 个提交（`e154135fa` / `075760768` /
`aa05e630b`，即 §9.42 / §9.43 对 `bg/auto_route_settle_worker.go` 的改动），
那次改动让豁免登记表里的那条形状不再出现，而登记表没有跟着更新。
**这正是「先写登记、后改代码」那条腐烂形态的又一次发生**，但修它不属于本轮授权
范围，故只记录不动手。


## 七、批判式复审（第二轮，同日，交付前自查）

对第一轮自己的产出做了一次对抗性复查。**查出 1 个真缺陷 + 1 个覆盖漏洞 +
1 处未声明的语义改动 + 1 个失控风险**，均已修。

### 7.1 真缺陷：漏抄凭据状态闸门，会去探软删除的凭据

`bg/node_probe.go` 的 `resolveDirectTarget`（全仓唯一的探针目标解析入口）首轮
查询带 `c.status IN ('active','cooling','degraded')`。本轮回填**没抄这条**。

域值（`sql/migrations/startup/631_provider_credential_soft_delete.sql` 的
`credentials_status_check`）是
`active|cooling|degraded|quarantine|quota_expired|disabled|deleted`。
所以缺这一条意味着：回填会对 **`deleted`（软删除）**、`quarantine`、
`quota_expired`、`disabled` 的凭据发真实请求。

**为什么测试没抓住**：本轮判据全在 DB-free 的注入接缝后面，而这些闸门**只写在
SQL 里**——一条都测不到，删了也不会红。这比缺陷本身更值得记：我在注释里写了
「SQL 预筛是性能优化，正确性来源在 Go 侧」，却只对协议做到了，其余闸门留在
SQL 里。**注释写了原则而代码没兑现，和没写是同一种腐烂。**

修法：闸门整体提升为纯函数 `capabilityBackfillAdmit`（Go 侧），SQL 里保留同
条件的预筛只作少取行用。`dueBinding` 增 6 个闸门字段随行取回。
`TestCapabilityBackfillAdmissionGates` 15 条子用例覆盖每一条，
`TestBackfillDoesNotProbeSoftDeletedCredential` 端到端钉住 `deleted` 必须在
**解密与出网之前**被挡。

### 7.2 失控风险：一条持续花上游 token、却没人批准过它的后台任务

回填会对**每一个** openai-responses 绑定周期发真实请求。实测 vapEUR / cred 126
有 ~101 个绑定；按默认 `staleAfter=6h` 折算 ≈ **该凭据 400 次/天**。绑定数 ×
凭据数一多，这笔账会变成真金白银，而第三轮明确记录过「探针只在失败/触发时跑」，
翻转成「全量主动探」是没人批准过的行为变化。

修法：加 `LLM_GATEWAY_CAPABILITY_BACKFILL` kill switch（`0/false/off/no`），
启动与每个 tick 都重读，运营方不发版即可关停。`TestCapabilityBackfillKillSwitch`
钉住全部 10 个取值。

### 7.3 未声明的语义改动：闸门 2 的 body 判据被我换了

旧：`params.ResponsesBodyBytes != nil`；本轮：`len(...) > 0`。
二者在「非 nil 但为空切片」时不同：旧写法会让开闸条件成立，于是
`nativeNonStream=true` 并把一个**空 body** 发往 `/v1/responses`。
`nativeNonStream` 自己的定义一直用 `len(...) > 0`，所以旧写法与它自相矛盾。

新写法更安全，但它是**我没被要求就改的语义**。已在 `executor_chat.go` 就地留痕，
并在此登记：**本条没有专门用例**，只由既有 `TestExecuteOpenAI_DurableVerdict*`
间接覆盖。属已知未覆盖项。

### 7.4 「SQL 没跑过」这个洞，本轮补上了

第一轮如实说明里写「SQL 写入路径没有活库覆盖」。本轮用**真 PostgreSQL 17 +
真 pgx 客户端**在一次性 scratch 库里验掉了四项（验证后已 DROP 该库）：

| 验证项 | 结果 |
|---|---|
| 扫描语句可解析，且 `make_interval(secs => $2)` 在**客户端送 Go int** 时被引擎接受 | `SCAN_OK`（这是第一轮明确标注「未验证」的那一处） |
| upsert 的 `ON CONFLICT (binding_id, capability)` 命中真实唯一约束并**确实覆盖** | `UPSERT_OK supported=false http_status=400`（先插 true/200，再 upsert false/400） |
| 能力行刚写入时扫描**不再**返回该绑定 | `STALENESS_OK` |
| 回拨到 7h（> 6h 窗口）后重新到期 | `STALENESS_OK` |

第三项是最要紧的：staleness 过滤若失效，行为会从「每 6h 探一次」变成
「每 30min 探一次」，即 ~4848 次/天，且完全没有报错。

**这次实跑本身也出了一次红**——第一次跑报
`evidence_json is of type jsonb but expression is of type timestamp`。
核对后错在**验证脚本**（`$4` 放到了 `last_tested_at` 位），交付代码是
`$4 → evidence_json`，正确。记这一笔是因为：一个头一次就红的验证工具，
比一个头一次就绿的验证工具可信。

### 7.5 变异验证扩到 5 处

新增 **变异 E**：把 `capabilityBackfillAdmit` 短路为恒 true ⇒
`TestCapabilityBackfillAdmissionGates` 13 条子用例红 @`capability_backfill_test.go:199`、
`TestBackfillDoesNotProbeSoftDeletedCredential` 红 @`:234`。
后者的红形态是「`persist` 被调用」而不是「返回了错误」——闸门失效就长那样。

### 7.6 复审**没有**查出的东西（如实记账）

- 没有对 `bg/probe_http.go` 的 `bodySample` 做全仓消费面审计的自动化门；本轮是
  用 `grep` 人工确认它只在证据构造处被消费、从不整体序列化或进日志。
- 没有验证并发：多实例同时跑回填时对同一 binding 的 upsert 竞争未做测试
  （ON CONFLICT 下最后写者胜，结论不会撕裂，但没有实测）。

## 八、如实说明

1. **SQL 路径已对着真引擎验过四项**（见 §7.4），但**没有对着真实网关数据跑过
   端到端回填**：本地两个库都没有网关 schema，真实数据的端到端仍未验证。
   刻意**没有**为它建一次性网关库再播种样本——制造数据来证明数据门，播种口径
   本身就成新的未经审计事实源。
2. **未部署。** 因此**没有**任何 `request_logs_hot` 交叉核对可做——本轮不声称
   任何生产「全绿」，只声称单测与变异验证层面的结论。
3. **遗留 #3（热路径那次额外 Redis 读）已在本文件同日的续篇中实施**（见
   [§九](#九遗留-3--热路径那次额外-redis-读2026-10-02-续)）。原先记的
   「需要单独决策的语义问题」已决策：**期限判定继续用 Redis `TIME`**，理由与证据
   见 §九.1，不是「保守起见」，而是写侧已把语义钉死。
4. **流式能力位仍无人写**（第三轮遗留 #2）。本轮刻意不碰：探针不发 SSE 请求，
   对流式腿没有任何证据。
5. **多实例并发未测。** 两个实例同时回填同一 binding 时，upsert 在 ON CONFLICT
   下是最后写者胜，结论不会撕裂（同一探测器、同一判定），但**没有实测**。
6. **`bodySample` 的消费面只有人工 `grep` 确认**，没有自动化门盯住
   「它不会被整体序列化或打进日志」。字段是新增的，日后有人给 `httpProbeResult`
   加一条 `%+v` 日志就会连带泄出上游响应原文。
7. **闸门 2 的 body 判据改动无专门用例**（见 §7.3），只由既有 DurableVerdict 用例
   间接覆盖。
8. **回填的出网成本没有上限闸门。** 现在只有 `batchLimit=50`/轮 与
   `staleAfter=6h` 两个隐式约束，没有「每日最多 N 次探测」这类硬预算。
   多凭据 × 多绑定时真实账单仍不可预测。kill switch 是止血阀，不是预算。
9. 未新建任何业务 API key；测试凭据是包内生成的 AES-GCM 信封，仓库无密钥材料。

---

## 九、遗留 #3 — 热路径那次额外 Redis 读（2026-10-02 续）

**未部署。** 本节所有结论只到单测与变异验证层面，**没有任何生产侧结论**，
`request_logs_hot` 交叉核对为 N/A（窗口须用 `t0_arrived_at`，本轮无部署可核）。

### 9.1 先答的语义问题：期限判定**继续**用 Redis TIME

上一轮把这条挂起，要求先决策。**决策：继续用 Redis `TIME`，不并到本地时间。**

理由不是「保守起见」，而是**写侧已把语义钉死**：

`setNodeCapabilityScript`（`credentialfpslot/node_state.go:503`）用
`local now = tonumber(redis.call('TIME')[1])` 算出
`state.capability_expires_at = now + ttl`（ttl=3600s，`node_state.go:543`）。
也就是说**存下来的到期时间戳本身是 Redis 时钟的读数**。读侧若改用本地时间，
就是拿本机时钟去比一个 Redis 时钟写下的绝对时间戳——两者不是同一个 epoch，
任何进程与 Redis 之间的时钟偏移都会**直接变成期限判定的误差**：结论可能
超过 TTL 仍然生效，或提前失效。

本仓已有用例把这个性质钉死：`TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch`
（`credentialfpslot/node_state_capability_test.go:140`）用 go-redis hook 在 GET
之后把 Redis 时间前拨 2 秒，断言到期必须被识别。若改本地时间，这条用例的
**前提本身失效**——不是用例坏了，是「到期时间戳属于 Redis 时钟」这个事实变了。

⚠️ 代价如实记账：**透传后省掉 1 次 node key GET，但仍要 1 次 TIME 往返。
热路径从 2 次 Redis 往返降到 1 次，不是降到 0。** 不假装省掉了 TIME。
「TIME 搭车」的做法（把 `TIME` 塞进路由已付的那次批量读，一个 Lua 脚本同时
返回 `TIME` + states）已实测可行（miniredis 支持该脚本形态；本机是单节点
`redis.NewClient`，无 cluster 跨槽问题），但它要改 `GetNodeStatesBatch` 的返回
契约，牵动 `cmd/gateway/main_livestream.go:594`、router interface
（`router.go:135`）与 4 处 stub —— 属独立一轮的改动面，**本轮刻意不做**，
留作 §9.5 遗留项。

### 9.2 落地了什么

| 文件 | 作用 |
|---|---|
| `provider/client.go` | `Candidate` 加 `RoutedNodeState *credentialfpslot.NodeState`（`json:"-"`）。已验**无 import 环**：`credentialfpslot` 不 import `provider`（`go list -deps` 命中数 0） |
| `router.go` `filterHealthyNodes` | 把已读到的 state 挂到 candidate 上（健康分支） |
| `router.go` `chooseLeastCooledCandidate` | 同上，挂在**实际被选中**的索引上 |
| `credentialfpslot/node_state.go` | `GetSupportsResponses` 增加可选 `prefetched`；nil 则自行读 |
| `executor_chat.go` | 闸门改用 `cand.RoutedNodeState` 透传 |

`nil` 的含义钉死为「**没有快照**」（authoritative URSM v2 路径 / FpSlots 关闭 /
批量读失败开放），**绝不是「无结论」**。这条区分是本轮最危险的失效形态：
把 nil 当成无结论 ⇒ 每请求重打一发注定失败的 `/responses`，而**没有任何东西报错**。

### 9.3 判据钉在「调用点是否还存在」

按要求，判据用 **go-redis hook 数 node key 的 `GET` 次数**，
**不是**钉函数里第一个 return。区别是实质的：`GetSupportsResponses` 的第一条
return 是 `state == nil || !SupportsResponsesKnown()`，任何「state 形状不对」的
改动都能让它提前返回，而真正的成本在它**之后**那次 GET。钉第一个 return 的用例
在本轮改动前后**都是绿的**——它测不到本轮做的事。

新增用例（`capability_state_passthrough_test.go` / `router_state_passthrough_test.go`）：

- 带透传 state ⇒ node key `GET` 次数 **= 0**，且 Redis `TIME` 次数 **≥ 1**
- 无透传 state（nil）⇒ `GET` 次数 **≥ 1**，且结论仍被遵守
- 透传但结论为负 ⇒ 仍然短路，`/responses` 命中 **= 0**
- 透传且已过期 ⇒ `TIME` 仍被调用（期限仍在 Redis 时钟上判定）
- 逐候选索引对齐、nil 槽保持 nil、批量失败不挂载、过滤后不错位
- `chooseLeastCooledCandidate` 的 early-exit 分支挂的是 `states[pickedIdx]`
  而非 `states[bestIdx]`（这两个索引在 early-exit 时通常不同）
- 不得就地改写调用方的 candidate 切片

### 9.4 变异验证（4 处，全部红→绿）

| 变异 | 判红的用例 |
|---|---|
| M1 忽略透传，永远自己读 | `PrefetchedStateRemovesTheNodeKeyGet`：`GET count = 1, want 0` |
| M2 把 nil 当「无结论」直接返回 | `NoPrefetchedStateStillReads`：`GET count = 0, want >=1` |
| M3 Redis TIME → 本地 `time.Now()` | `PrefetchedStateRemovesTheNodeKeyGet` + `ExpiredPrefetchedStateStillReadsAsUnknown`：`TIME count = 0, want >=1`（2 条红） |
| M4 生产侧完全不挂 state | 挂载侧 5 条红，**而消费侧 3 条仍全绿** |

**M4 是本轮最值得记的一条**：它实证了接缝纪律要防的东西——消费侧（executor）
对生产侧（router）挂载点**零覆盖且绿着**。若只写消费侧判据，M4 会完全漏网，
本轮的改动等于没被测到。挂载侧判据是补出来的，不是顺手写的。

### 9.5 遗留（下一轮）

1. **`TIME` 搭车**：Lua 脚本一次返回 `TIME` + states，把热路径的最后一次往返也
   省掉（2 → 0）。已实测脚本形态可行，改动面见 §9.1。**须单独一轮。**
2. **流式能力位仍无人写**（第三轮遗留 #2）。探针不发 SSE，零证据。
3. **回填的每日探测预算闸门**（原 (a) 选项）：现在只有 `batchLimit=50`/轮 与
   `staleAfter=6h` 两个隐式约束，真实账单不可预测。kill switch 是止血阀不是预算。
4. **多实例并发 upsert 未实测**（沿用上轮记账）。
5. `admin` 的既有红灯 `TestNoUnregisteredVPaddedColumnReader`（原 (b) 选项）
   **本轮未修**：#3 判定为值得做，故不触发替代项。该红灯来源仍是并行会话
   §9.42/§9.43 改了 `bg/auto_route_settle_worker.go` 但豁免登记表未同步，
   修的时候必须同步更新登记表。

---

## 十、遗留 #3 的批判式复审（2026-10-03）

上一轮（§九）判定 #3 完成并提交。本轮按「别吹牛、仔细核查」的要求做**对抗性复审**，
不假设上一轮是对的。复审发现 **2 个真实问题**，均已修。

### 10.1 根因：复用快照 = 引入一个无界的陈旧窗口（已修）

**上一轮没讲清的事**：透传让判定基于**更早**的快照。上一轮只测了「快照过期仍读成
unknown」（那本来就由 Redis TIME 保证，是安全的），**没有测「TTL 之内结论被改写」**
——而那才是复用快照真正引入的新行为面。

实测（`TestAudit_SnapshotStalenessWindow_MeasuresTheGap`）：路由 MGET 与闸门之间
常见间隔 **~6ms**。但这只是常见情况，**不是上界**：

```
Execute → PlanCandidatesPinned（filterHealthyNodes 在此 MGET 并挂快照）
       → executeViaDispatch → dispatchPipeline.Submit
```

`Submit`（`domains/dispatch/pipeline.go:1078`）**同步阻塞在 `<-qr.ResultCh`**，
请求在 `totalQueue` 里排队等待 worker 空闲。**该等待在代码里没有上界** ——
取决于队列深度与上游延迟。所以快照的实际陈旧度是**无界的**。

**为什么过期仍然安全、但改写不安全**：
- **过期安全**：`capability_expires_at` 由 Redis TIME 判定，快照不会让过期结论复活。
- **改写不安全**：`SetSupportsResponses` 有三个写入方
  （`executor_chat` 实时证据 / `bg/credential_probe_v2` / `bg/responses_capability`），
  正向重探可把 `false → true`。队列里等完的请求会**继续按陈旧的负向结论走 Chat**。
  方向是保守的（不会打出注定失败的请求），但它意味着**这个优化不是行为等价的**，
  而上一轮把它当成等价优化来描述。

**修法**：加 `prefetchMaxAgeSec = 5`（`credentialfpslot/node_state.go`）。
超过该年龄的快照被丢弃并重新读取；`CapabilityUpdatedAt <= 0`（pre-migration
payload，年龄不可知）同样重读。年龄用 **Redis TIME** 判定，与期限同一时钟源。

**5s 的依据**：常见路径 ~6ms，远小于 5s ⇒ 稳态下护栏**从不触发**，优化照常生效；
它只针对「请求在队列里等了几秒」这个尾部。**它不是期限旋钮**（期限是 3600s，
独立判定）。

### 10.2 我一度引入的退化：TIME 采样顺序（已修，且被既有门抓住）

第一版修法把 Redis TIME 提到函数最前面，想让「年龄检查」与「期限检查」共用一次
往返。**这破坏了既有门守护的性质**：`TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch`
当场变红——它要求 TIME 必须在 state 读**之后**采样，才能发现「GET 与 TIME 之间
跨过的到期」。

**这是一次真实的自证**：上一轮我把这个用例当作「钉住 Redis TIME 决策」的证据，
本轮它又反过来抓住了我在同一决策上的一个错误实现。**顺序是承重的，不是风格问题。**
已回滚为「先读 state，后采样 TIME」，并把理由写进函数注释（含「不要优化这个顺序」的警告）。

### 10.3 审计的判据自身也有一个洞（M6 逼出来的）

新护栏写完后做双向变异自检：

| 变异 | 期望 | 实际 |
|---|---|---|
| M5 删掉年龄护栏（永远信任快照） | 红 | ✅ 红 2 条 |
| M6 护栏收紧到 0（永远重读＝优化整个关闭） | 红 | ❌ **全绿** |

M6 漏网暴露的是**判据形态问题**：只钉「该拒的拒了」的门，在「全都拒了」时照样绿。
补 `TestAudit_AgeGuardMustNotDisableTheOptimisation` 后**仍绿**——第二次排查发现
根因更细：护栏写成 `now - updatedAt > 0`，而我的测试里 `now == updatedAt`（同一秒），
`0 > 0` 为假 ⇒ 快照仍被采信。必须让快照**明确 1 秒以上但仍在上限内**才能区分
「采信」与「全拒」。**判据的场景形状没对上被测表达式的边界。**

修正后 M5/M6 双向均判红。⇒ 这条补出来的门与 M5 那批**互补**，缺一不可。

### 10.4 审计确认没有问题的部分（如实记账，避免下轮重复怀疑）

- **优化在生产主路径上确实生效**：`dispatchForward` 先把 candidate 压成
  `CredentialRef`（该结构只带标量，**会丢掉快照**），随后**按 CredentialID 从
  `d.candidates` 取回完整 candidate**（`executor_dispatch.go:210-217`），
  快照随之回来。`TestAudit_DispatchRefRoundTripKeepsSnapshot` 钉住这一环。
  ⇒ 若将来有人把那次「按 ID 取回」改成只用 ref，**本轮优化会静默失效且功能仍正确**。
- **闸门在重试循环外**：`executor_chat.go` 闸门在 448 行，重试循环在 690 行，
  一次请求只判定一次。⇒ 快照窗口不含退避重试时长。
- **零值 state 不是 nil**：`GetNodeStatesBatch` 对不存在的键返回**零值 NodeState**。
  零值快照与 nil 回退读出结果一致（都是 unknown），`TestAudit_ZeroValuedSnapshotIsNotNil` 钉住。
- **无结论的快照仍是 unknown**：`capabilities` 为空的快照不得被当成「有结论取默认值」。
- **过期结论不得被快照续命**：`TestAudit_ExpiredVerdictInSnapshotIsNotHonoured`。

### 10.5 本轮未做 / 仍未验

- **仍未部署**，无任何生产侧结论；`request_logs_hot` 交叉核对 N/A。
- `prefetchMaxAgeSec=5` 是**工程判断，不是实测分布**。上线后若 dispatch 排队
  P99 超过 5s，这个优化会在尾部失效（退化为每次重读，功能正确、收益减少）。
  **建议观测**：统计「快照被年龄护栏丢弃」的比例，目前**没有埋点**（见 §十一）。
- 其余遗留见 §9.5（TIME 搭车 / 每日探测预算 / 流式能力位 / 多实例并发）。

## 十一、下一轮建议（本轮新增）

1. **给年龄护栏加埋点**：`node_state_prefetch_dropped_total{reason=stale|unstamped}`。
   没有它就无从判断 5s 这个数字是否合适——而 5s 是本轮唯一**未经实测校准**的参数。
2. **TIME 搭车**（§9.5 第 1 条）：Lua 脚本一次返回 `TIME` + states。
   ⚠️ 但要先想清楚：它会与 §10.2 的「TIME 必须在 state 之后采样」冲突吗？
   同脚本同快照时**不冲突**（无跨命令窗口），但这需要一条判据证明，而不是推理。
