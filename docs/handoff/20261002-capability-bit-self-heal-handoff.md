# handoff — capability bit self-heal（2026-10-02）

> 本文件是接力入口。背景与证据在
> [`docs/audit/2026-10-02-capability-bit-self-heal.md`](../audit/2026-10-02-capability-bit-self-heal.md)，
> 上一轮的问题陈述在
> [`docs/audit/2026-10-02-vapeur-protocol-round3.md`](../audit/2026-10-02-vapeur-protocol-round3.md) §九 遗留 1/2/3。

## 一句话状态

第三轮遗留 #1（`credential_model_capabilities` 从来没人写）**已实现并验证**，
代码已合入 main（`ece68f148`）。**未部署**，所以没有任何生产侧结论。

遗留 #3（热路径那次额外 Redis 读）已实现（审计档 §九），并于 **2026-10-03 做过
两轮批判式复审**：

- **第一轮（§十）**：发现复用快照引入无界陈旧窗口（加 `prefetchMaxAgeSec=5` 护栏）、
  一度为省一次 TIME 往返破坏了「TIME 必须在 state 之后采样」的顺序（既有门当场抓住，已回滚）、
  年龄护栏第一版自己多采了一次 TIME，**往返数从 3→3（净收益归零）而全部门仍绿**
  （已合并采样并补往返预算判据）。
- **第二轮（第三轮续）**：**发现护栏比错了时钟**——比的是 verdict 写入时刻
  （TTL 3600s）而不是快照读取时刻（毫秒级），于是它在几乎每个生产请求上都判
  「太旧」并重读，**热路径 4 次往返 vs 改动前 3 次**。已根修、已加埋点、
  已用 `request_logs_hot` 实测校准 5s。

**同样未部署。**

## 落地了什么

| 文件 | 作用 |
|---|---|
| `bg/capability_backfill.go` | 回填任务本体。扫到期 binding → 跑**现有** `singleResponsesPing` → upsert `credential_model_capabilities`（只 `native_responses_nonstream`）→ 镜像进 Redis 能力键 |
| `bg/probe_http.go` | 新增 `httpProbeResult.bodySample`（2xx 也保留真实上游帧） |
| `domains/streaming/executors/responses_durable_verdict.go` | 读错误 ⇒ 回落 SQL 持久结论（不是默认值） |
| `domains/streaming/executors/executor_chat.go` | 两方向闸门合并成一次解析；降级日志 Debug → Warn |
| `provider/client.go` | 候选投影加 `supports_native_responses_known`（三态） |
| `cmd/gateway/main.go` | 两个互斥装配点各起一份回填任务 |

**kill switch**：`LLM_GATEWAY_CAPABILITY_BACKFILL=0|false|off|no` 可不发版关停。
**为什么必须有**：~101 绑定 × 6h 窗口 ≈ 400 次真实上游调用/天/凭据。

## 已被变异验证钉住的性质（改动这些会让它们红）

- 无证据（5xx/401/参数形状 400/网络错）⇒ **一行都不写**（变异 A）
- 读错误 ⇒ 回落 SQL 结论，不是默认值（变异 B）；降级必须 WARN 级（变异 C）
- 2xx + `finish_reason:length` + 空 output ⇒ **仍然判正**（变异 D）：
  能力位问的是「端点通不通」，判据是状态码，不是有没有出字
- 每条准入闸门（`deleted`/`quarantine`/`quota_expired`/`disabled`/retired/
  manual_disabled/provider_disabled/binding_unavailable/非 responses 协议）⇒
  必须挡住，且挡在**解密与出网之前**（变异 E）

## 2026-10-03 第三轮：年龄护栏比错了时钟（根修）+ 埋点 + 5s 实测校准

**证据**：[`docs/audit/2026-10-03-prefetch-age-calibration.md`](../audit/2026-10-03-prefetch-age-calibration.md)。

上一轮给 `prefetchMaxAgeSec=5` 加的年龄护栏，**比较的是两个不同尺度的量**：

| 量 | 含义 | 量级 |
|---|---|---|
| `CapabilityUpdatedAt` | verdict **写入**时刻（TTL 3600s） | 秒~小时 |
| 快照年龄 | 本进程**读到该 key** 距今 | 毫秒~十秒 |

拿 3600s 尺度的量比 5s 的界 ⇒ 护栏在**几乎每个生产请求**上都判「太旧」并重读。
实测：verdict 写在 30 分钟前、快照只有 6ms 新 ⇒ 闸门内 `GET=1 TIME=2`，
热路径共 **4 次**往返，比改动前的 3 次**还多**。这次优化在生产上是负收益。

- **修法**：`NodeState` 加 `SnapshotReadAt time.Time`（`json:"-"`，带单调时钟读数），
  在 `GetNodeStatesBatch` / `GetNodeState` 打戳，护栏改比它。
  年龄检查**不再采样 Redis 时钟**（它是本地时长，跨时钟比较是本函数明令禁止的）。
  修后 `GET=0 TIME=1`，热路径 2 次。
- **埋点**（§十一 第 1 条，已做）：`llmgw_node_state_prefetch_dropped_total{reason}`
  + `llmgw_node_state_prefetch_age_seconds`（后者不能省：只有计数器时
  「从不触发」与「0.1%」在面板上分不开，而后者才是调参信号）。**零新增往返**。
- **5s 已实测校准**：本机 `request_logs_hot` 168 行完整瀑布，T2→T5 区间
  p50 0.005s / p90 0.040s / p95 5.41s / p99 17.88s / max 26.04s，**>5s 占 6.5%**。
  分布是**双峰**的，5s 切在尾巴中间。**不升到 30s**：那要信任 26s 前的快照，
  正是护栏要封的窗口；**不降到 p90**：尾巴正是护栏的用武之地。

### 这一轮最贵的两条教训

- **上一轮三条年龄判据全部用「写完立刻读」的夹具**，在那些夹具里
  两个被比较的量恰好相等，于是比错了时钟也全绿。
  ⚠️ **判据的场景形状要对上被测表达式**，且要检查**被比较的两个量是否真的不同**——
  上一轮 §10.4 只检查了「场景够不够新鲜」。
- **判据的「造场景手段」必须跟着被测语义一起改。** 年龄从「比 Redis 时钟」
  改成「比本地时长」后，三条判据里的 `mr.SetTime()` 全部失效——
  **用例还在跑，但已经不测它声称测的东西**。其中
  `TestAudit_AgeGuardMustNotDisableTheOptimisation` 退化成「年龄≈0」，
  而 ≈0 恰好是 M6（护栏=0）杀不掉的形状 ⇒ **假绿**。修正后 M6 判红 **6 条**（修前 0 条）。

## 下一轮该做什么（按优先级）

1. ~~**遗留 #3：热路径那次额外 Redis 读。**~~ **已做**（同日续轮）。
   待决策的语义问题已答：**期限判定继续用 Redis `TIME`**。
   依据是写侧 `setNodeCapabilityScript` 用 `redis.call('TIME')` 算
   `capability_expires_at`，存下来的就是 Redis 时钟的绝对时间戳；读侧改本地
   时间＝跨 epoch 比较。且 `TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch`
   已把该性质钉死。
   ⚠️ **代价**：省掉 1 次 GET，仍花 1 次 TIME ⇒ 热路径 2 次往返 → 1 次，**不是 0**。
   下一条是把它真正降到 0 的做法。

0. ~~**给 `prefetchMaxAgeSec` 加埋点**~~ **已做**（2026-10-03 第三轮，见上）。
   ⚠️ 埋点过程中发现护栏本身比错了时钟，已一并根修——**埋点的价值正在于此**。

2. **`TIME` 搭车（#3 的续）。** 已实测可行：miniredis 支持一个 Lua 脚本同时返回
   `TIME` + states（本机单节点 `redis.NewClient`，无 cluster 跨槽问题）。做完可把
   热路径的最后一次往返也省掉（2 → 0）。改动面：`GetNodeStatesBatch` 返回契约、
   `cmd/gateway/main_livestream.go:594`、router interface（`router.go:135`）+ 4 处
   stub。**须单独一轮。**
   ⚠️ 上一轮已实测：把它提前到 state 之前会撞坏「TIME 在 state 之后采样」的承重性质。
   搭车方案必须**同时**满足两条，不能靠推理。

3. **遗留 #2：流式能力位仍无人写。** 探针不发 SSE，零证据。需要先有流式探针。

4. ~~**回填的每日探测预算闸门。**~~ **已做**（2026-10-03 第三轮）。
   `LLM_GATEWAY_CAPABILITY_BACKFILL_DAILY_BUDGET`，默认 2400 次/24h 滚动窗口，
   0 或负数＝不设预算（显式逃逸阀）。**默认 2400 = 旧配置自己会花掉的数**
   （batchLimit 50 × 48 轮/天），所以开启预算**零行为变化**——它把一条
   此前无人显式批准的账单变成显式、可调小、且立刻生效的。
   ⚠️ 记账点必须在**出网点**（`probeAndPersist` 里 `chargeProbe`），
   不能只在 `BackfillOnce` 循环里预检——见下面那条变异教训。
   ⚠️ 进程内台账 ⇒ **重启即清空**，连续重启可把日花费放大到
   「重启次数 × 预算」。这是保守方向（只会多花，不会把超支算成安全），
   跨重启封顶需要持久化，是另一轮的事。

5. **多实例并发 upsert 未实测**（ON CONFLICT 最后写者胜，结论不撕裂，但没测）。

## 2026-10-03 复审新增的坑

- **「复用更早的快照」不是行为等价的优化，除非你证明陈旧度有上界。**
  上一轮把它当等价优化描述；实测发现路由 MGET 与闸门之间常见 ~6ms，
  但请求随后进 `dispatchPipeline.Submit` **同步阻塞等 `qr.ResultCh`**，
  队列等待**无上界**。⇒ 必须给快照加年龄护栏，否则 TTL 内结论被改写时
  会读到陈旧值（方向保守，但语义已变）。
- **既有的 TIME 用例会反过来抓住你对 TIME 的错误实现。** 上一轮拿
  `TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch` 当「钉住 TIME 决策」的
  证据；本轮为省一次往返把 TIME 采样提前，它**当场变红**。
  ⇒ 顺序（TIME 在 state 之后）是承重性质，不是风格。
- **「昂贵的命令是否消失」抓不住「新增一条便宜命令」。** 年龄护栏第一版为了
  判断快照是否可信，**自己先采一次 TIME**，期限检查再采一次 ⇒ 往返数从 3 → 3，
  快照被拒时 3 → 4，**§九 宣称的「2→1」不再成立，净收益归零**，
  而**所有既有判据全绿**（「GET 没了」成立、「TIME 仍被调用」也成立）。
  ⇒ 优化类改动必须钉**往返预算**（`GET==0 && TIME<=1`），不是钉「某命令没了」。
  这条是本轮最贵的教训：省下的往返可以被自己悄悄还回去。
- **只钉「该拒的拒了」的门，在「全都拒了」时是绿的。** 年龄护栏的 M6 变异
  （护栏收紧到 0 ＝ 优化整个关闭）最初全绿；补判据后**仍绿**——根因是
  `now - updatedAt > 0` 在同一秒时为假。⇒ 判据的场景形状必须落在被测
  表达式的边界上，否则「补了判据」也只是补了个恒绿的东西。

## 每日预算闸门这一轮的坑

- **闸门必须长在花钱的地方，且判据也必须落在那一层。**
  第一版把「本轮还允许探几次」做成 `BackfillOnce` 循环里的**预检**，
  出网点只记账不判定。后果：变异 B1（把记账挪到出网**之后**，
  即先发包后扣费）与 B2（`chargeProbe` 永不拦截）在 `BackfillOnce`
  层的**全部判据仍然全绿**——因为循环的预检独立地挡住了超发。
  ⇒ 那不是变异无害，是**那一层没有能区分它的判据**。
  修法两条：① 判定权收到出网点，循环里只做「提前收尾」；
  ② 补 `TestBudget_EgressPointIsTheGate`——**绕开循环**直接连按两次
  `probeAndPersist`，第二次必须一包都不发。补完 B1/B2 立刻判红。
- **「测试全绿」和「这一层有判据」是两件事。** B1 漏网时，
  `TestBudget_StopsAtTheDailyCeiling` 之类的用例是绿的、且**测的是对的东西**
  （确实探了 5 次），只是它们测在循环层，而真正的契约在出网点。
  ⇒ 判据要问「这条性质在**哪一层**被兑现」，并对每一层各钉一条。
- **一次写预算用例时踩了 attempt 退避的坑**：三轮复用同一批 BindingID，
  第二轮起全部 `backedOff`，计数卡在 2不动。看起来像「预算没生效」，
  实际是退避在生效。**每轮换新绑定**才测得到预算的跨轮累加。
- **两个台账不能合并**：`attempts`（BindingID→时刻，退避用）与
  `probes`（窗口内出网次数，预算用）问的是不同问题。合并会把
  「同一条探 4 次」算成 1 次（少算 ⇒ 闸门失效）。已由
  `TestBudget_IsSeparateFromAttemptLedger` 钉住。

## 踩过的坑（别再踩）

- **接缝的「消费侧判据」不等于「生产侧被测到」。** 遗留 #3 实测：把 router 的两处
  state 挂载**全部删掉**（变异 M4），挂载侧 5 条判红，而 executor 消费侧
  **3 条全绿**——因为消费侧拿到 nil 就安静地回落自己读。⇒ 只写消费侧判据，
  生产侧挂载点可以完全坏掉而没人发现。**挂载点必须单独钉。**

- **判据写在 SQL 里 = 一条都测不到。** 本轮第一版把全部闸门放 SQL，测试靠注入
  `scan` 接缝绕开，于是「漏抄 `c.status`」这种缺陷没有任何用例会红。第二版把
  闸门提成纯函数 `capabilityBackfillAdmit` 才有覆盖。**注释里写「正确性来源在
  Go 侧」而代码没做到，和没写是同一种腐烂。**
- **证据字段可能只在错误路径上有值。** `classifyHTTPResponse` 在 2xx 上不填
  `errMsg`，拿它当「上游原文」会让**每条正向结论**写下空证据。
- **子串门会被文件自己的约束注释喂饱**（「本文件不得出现 X」那句注释里就有 X）。
  用 AST 查字符串字面量。
- **A 绿不等于 A 承重。** 变异 B 下端到端那条不变红——那不是用例坏了，是关于
  前置条件的事实（两个存储面在今天条件下同解）。

## 尚未处理

- **未部署 ⇒ `request_logs_hot` 交叉核对 N/A。** 下轮若部署，窗口必须用
  `t0_arrived_at`（不是 `created_at` / `error_message`）。
- `admin` 有一条既有红灯 `TestNoUnregisteredVPaddedColumnReader`（豁免
  `bg/auto_route_settle_worker.go:id` 失效）。已用干净 worktree 在 HEAD 对照
  证明与能力位无关，来源是并行会话 §9.42/§9.43 改了那个文件但豁免登记表没跟着
  更新。**谁修谁负责更新登记表**——否则下一轮还会再撞一次。
