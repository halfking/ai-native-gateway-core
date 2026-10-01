# 176 号 · R89-CL —— 否定结论：自检 key 与业务 key **不共用限流桶**；09-28 的业务限流另有原因


> ## ✅ 177 号已关掉本条（2026-10-01，R89-CM）—— **`Reason` 进了 Prometheus，没进 `request_logs`**
>
> 176 号 §三 问：`rate_limit.go` 四个 `Blocked` 分支各带一个 `Reason`，
> **而 `Reason` 是否被写进 `request_logs` 未核**。**答案：没写。**
>
> **① 出口 A（指标）✅ 完整保留**：`rate_limit_metrics.go:8-25` 的
> `llm_gateway_rate_limit_rejections_total` **带 `reason` 标签**，且 `Reason==""` 时
> 兜底为 `"unknown"` ⇒ **四种原因 + unknown 全部可分**。
> **② 出口 B（数据）🔴 只写死一个码**：`handler.go:2286` 的闭包签名是
> `func(errCode, errMsg string, providerID, credentialID *int)` —— **四个参数，没有 `Reason` 的位置**；
> 而 `:3229-3234` 里 `recordGatewayRateLimitRejection(rlOutcome)` **带** `rlOutcome`、
> `captureAndEmitRateLimited("rate_limit_exceeded", "rate limit exceeded", nil, nil)` **不带**
> ⇒ **同一个 `rlOutcome`，两条出口待遇不同**
> ⇒ **`queue_budget_exceeded` ×2 / `queue_full` / `bucket_timeout` 全部塌缩成
> `error_kind='rate_limit_exceeded'`。**
>
> **③ 🔴 这就是我 171–173 号误判的确切根源**：171 号写「不知道触发的是并发/速率/预算哪种闸门」
> —— **不是代码没记，是它记在另一个出口**；
> **176 号「09-28 那 1,274 次是配额还是背压」同样永远查不出**，
> **必须查 `/metrics` 的 `llm_gateway_rate_limit_rejections_total{reason=...}`**。
> **⇒ 属「可观测性不对称」缺陷（新增待裁决 71，P3）**，但**代价已经付过** ——
> **审计方因此连续四轮走错方向（171→176）。**
>
> **④ 修法（不擅自动手）**：给闭包加 `reason` 参数并透传进 `errCode`（改 1 个签名 + 2 个调用点）
> —— ⚠️ **`error_kind` 取值面变大，既有看板若写死 `WHERE error_kind='rate_limit_exceeded'` 会漏值**
> ⇒ **属对外可见的数据契约，需产品/运维确认**。
>
> 详见 [177 号](177-R89CM-指标侧分得清数据侧分不清-Reason只进了Prometheus.md)（playbook §70）。

> **日期**：2026-10-01
> **轮次**：R89-CL（第 76 轮，审计第 176 号）
> **类型**：**纯否定结论**（175 号 §五 那个疑问的答案）+ 一条仍未定的观察
> **改动生产代码**：无　**改动数据库**：无
> **上一轮**：175 号（业务面健康、探测面崩塌，两者���分离的系统面）

---

## 〇、起手：关掉 175 号留下的那一个疑问

175 号 §五 登记：

> **09-28 业务成功率掉到 76.33%、`rate_limited` 涨到 1,274**（09-27 是 247）
> ⇒ **业务面自己也开始撞限流了**
> ⇒ **待查：自检 key 与业务 key 是否落进同一个限流桶**
> （`CheckRPMCtx(keyID, limit)` 按 keyID 分桶，但 `AdmitRPMWithBudget`
> 是否有跨 key 共享计数**本轮未核**）。

**本轮把那个未核关掉。**

---

## 一、F1（否定结论）：`MinuteBucketAdmission` 完全按 `keyID` 分桶

`ratelimit/minute_bucket.go` 里**每一处**状态访问都以 `keyID` 为键：

| 行 | 代码 | 状态 |
|---|---|---|
| `:50` | `current: make(map[int]*minuteBucket)` | 每 key 一个计数器 |
| `:51` | `queues:  make(map[int][]*queuedRequest)` | 每 key 一条等待队列 |
| `:93/96` | `bucket := a.current[keyID]` / `a.current[keyID] = bucket` | 取/建该 key 的桶 |
| `:97` | `a.releaseWaitersLocked(keyID, limit)` | 只唤醒该 key 的等待者 |
| `:99/126` | `queue := a.queues[keyID]` / `a.queues[keyID] = append(queue, waiter)` | 入队按 key |
| `:157/160` | `a.current[keyID]` | 重入等待时仍按 key |
| `:173-175` | `releaseWaitersLocked(keyID, limit)` 取 `a.current[keyID]` + `a.queues[keyID]` | — |
| `:184-186` | `delete(a.queues, keyID)` / `a.queues[keyID] = queue` | 只清该 key |
| `:190/196` | `cancel(keyID, waiter)` | 取消按 key |

**⇒ 零个跨 key 的共享计数器、零个全局桶。**
**⇒ 自检 worker key（727/736）与业务 key 落在不同的桶里，互不影响。**

**⇒ 175 号那个「业务面 09-28 也开始撞限流」的现象，**不能用「自检挤占了业务配额」解释。**

---

## 二、⚠️ F2：顺带确认的一条一致行为（不是缺陷）

`minute_bucket.go:84-86`：

```go
if limit <= 0 {
	return AdmissionResult{Admitted: true}, nil
}
```

⇒ `limit <= 0` 在**这一层**也是直接放行。

**⇒ 与 `rate_limit.go:70-73`（172 号那条）行为一致。** 两层守卫语义相同、无矛盾 ⇒ **不构成新问题**，登记为「已核对一致」。

---

## 三、⚠️ F3：09-28 业务限流为何涨到 1,274 —— 仍未定（诚实登记）

**否定结论反而让这个问题更清晰了**：既然不是「自检挤占」，那它要么是

| 可能 | 需要什么才能定 |
|---|---|
| 某个业务 key 自己的 `rate_limit_rpm` 打满 | 该 key 的 `rate_limit_rpm` + 当日请求速率 |
| 队列背压（`queue_full` / `bucket_timeout`）而非配额 | `rate_limit.go:98-104` 的 `Reason` 是否落库（**本轮未核**） |

**⚠️ 本轮不追**（上下文预算已到上限）。
⇒ **关键线索留在这里**：`rate_limit.go:86/98/101/104` 四个 `Blocked` 分支
分别带 `Reason = "queue_budget_exceeded" / "queue_full" / "bucket_timeout"`，
**而 `Reason` 是否被写进 `request_logs` 本轮未核** ——
**如果没写，那这四种完全不同的阻断原因在数据上无法区分**，
这本身就是一条待登记的观测缺口（登记为下一轮入口）。

**⇒ 这与 174 号那条教训同族**：我 171–173 号之所以把 32.6 万次 429 一律当成「超配额」，
**正是因为我没有先确认「超配额」与「队列背压」在数据上是否可区分。**

---

## 四、playbook §69 新增

> **§69 怀疑「两个主体共用一个桶」时，去读那个数据结构里桶的 key 是什么 —— 不要靠机制推测**

**由来**（R89-CL / 176 号）：175 号留下的疑问是「自检 key 与业务 key 是否落进同一个限流桶」，
并明确写了「`CheckRPMCtx(keyID, limit)` 按 keyID 分桶，但 `AdmitRPMWithBudget`
**是否有跨 key 共享计数未核**」。

**答案：读 `minute_bucket.go` 就有了** —— 9 处状态访问全部以 `keyID` 为键，
`current`/`queues` 都是 `map[int]…`，**零个全局桶**。

**⇒ 落地：**
1. 问「A 和 B 共用桶吗」时，**去读那个保存桶状态的数据结构的 key 类型**；
2. **数一数有几处状态访问**（本例 9 处）—— 只有 1 处就可能漏看，**数全了才敢下结论**；
3. `map[int]X` 里的 `int` 是什么，是这类问题唯一需要确认的东西。

**⇒ 与既有条目的关系**：§66（守卫存在 ≠ 守卫可达）问的是「输入有没有写入方」；
**本条问的是「两个行为主体会不会共享状态」，答案是去数据结构里看键。**
两者都是「读机制不如读数据结构」。

**同族**：§66 / §67（共变 ≠ 因果）/ §68（共变要另找判别维度）/ §46（尺子不对结论必错）/ §16（否定结论分轮登记）。
