# handoff — capability bit self-heal（2026-10-02）

> 本文件是接力入口。背景与证据在
> [`docs/audit/2026-10-02-capability-bit-self-heal.md`](../audit/2026-10-02-capability-bit-self-heal.md)，
> 上一轮的问题陈述在
> [`docs/audit/2026-10-02-vapeur-protocol-round3.md`](../audit/2026-10-02-vapeur-protocol-round3.md) §九 遗留 1/2/3。

## 一句话状态

第三轮遗留 #1（`credential_model_capabilities` 从来没人写）**已实现并验证**，
代码已合入 main（`ece68f148`）。**未部署**，所以没有任何生产侧结论。

遗留 #3（热路径那次额外 Redis 读）**已在同日续轮实现并验证**（见
[审计档 §九](../audit/2026-10-02-capability-bit-self-heal.md#九遗留-3--热路径那次额外-redis-读2026-10-02-续)），
**同样未部署**。

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

## 下一轮该做什么（按优先级）

1. ~~**遗留 #3：热路径那次额外 Redis 读。**~~ **已做**（同日续轮）。
   待决策的语义问题已答：**期限判定继续用 Redis `TIME`**。
   依据是写侧 `setNodeCapabilityScript` 用 `redis.call('TIME')` 算
   `capability_expires_at`，存下来的就是 Redis 时钟的绝对时间戳；读侧改本地
   时间＝跨 epoch 比较。且 `TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch`
   已把该性质钉死。
   ⚠️ **代价**：省掉 1 次 GET，仍花 1 次 TIME ⇒ 热路径 2 次往返 → 1 次，**不是 0**。
   下一条是把它真正降到 0 的做法。

2. **`TIME` 搭车（#3 的续）。** 已实测可行：miniredis 支持一个 Lua 脚本同时返回
   `TIME` + states（本机单节点 `redis.NewClient`，无 cluster 跨槽问题）。做完可把
   热路径的最后一次往返也省掉（2 → 0）。改动面：`GetNodeStatesBatch` 返回契约、
   `cmd/gateway/main_livestream.go:594`、router interface（`router.go:135`）+ 4 处
   stub。**须单独一轮。**

3. **遗留 #2：流式能力位仍无人写。** 探针不发 SSE，零证据。需要先有流式探针。

4. **回填的每日探测预算闸门。** 现在只有 `batchLimit` 与 `staleAfter` 两个隐式
   约束，真实账单不可预测。kill switch 是止血阀，不是预算。

5. **多实例并发 upsert 未实测**（ON CONFLICT 最后写者胜，结论不撕裂，但没测）。

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
