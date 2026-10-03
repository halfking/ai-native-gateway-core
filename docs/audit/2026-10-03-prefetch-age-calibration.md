# 2026-10-03 — prefetchMaxAgeSec 实测校准 + 年龄护栏错时钟根修

> 承接 [`2026-10-02-capability-bit-self-heal.md`](./2026-10-02-capability-bit-self-heal.md)
> §十一 第 1 条（"给年龄护栏加埋点"）与 §10.6（"`prefetchMaxAgeSec=5` 是工程判断，
> 不是实测分布"）。
>
> **本轮最重要的结论不是埋点，也不是那个 5 秒——是护栏在比一个错的量。**
> 埋点只是把它照出来了。

## 一句话状态

遗留 #3 的年龄护栏**比错了时钟**，导致它在几乎每个生产请求上都判定「快照太旧」
并重读，把这次优化要省的往返原样还了回去，**热路径比改动前还多一次往返**。
已根修（改比快照读取时刻）、已加埋点、已用 `request_logs_hot` 实测校准 5s。
**仍未部署，无生产侧结论。**

## 一、根因：护栏拿「verdict 写入时刻」当「快照年龄」

护栏的表达式是：

```go
now - state.CapabilityUpdatedAt > prefetchMaxAgeSec   // prefetchMaxAgeSec = 5
```

但这两个量不在同一个尺度上：

| 量 | 含义 | 量级 |
|---|---|---|
| `CapabilityUpdatedAt` | **verdict 被写入**的时刻（`setNodeCapabilityScript` 写，TTL 3600s） | 秒~小时 |
| 快照年龄 | **本进程读到该 key** 的时刻距今（路由 MGET → 闸门） | 毫秒~十秒 |

拿 3600s 尺度的量去比一个 5s 的界，结论必然是「几乎永远超期」。

### 实测证据（RED）

`TestProbe_AgeGuardComparesTheWrongClock`：verdict 写在 30 分钟前（TTL 3600s 内，
完全有效），快照本身只有 **6ms** 新（审计 2026-10-02 实测的路由→闸门常见间隔）。

```
--- FAIL: TestProbe_AgeGuardComparesTheWrongClock
    verdict 写入于 30m0s 前；快照本身只有 6ms 新
    (supported=false, known=false), want (true, true)
```

`TestProbe_StaleSnapshotMustNotReReadOnTheHotPath` 把它换算成钱：

```
30 分钟前写的 verdict + 6ms 前的快照：GET=1 TIME=2
```

| 路径 | 闸门内 GET | 闸门内 TIME | 热路径合计（含路由 MGET） |
|---|---|---|---|
| 改动前（无透传） | 1 | 1 | **3** |
| 透传命中（应有） | 0 | 1 | **2** |
| **护栏错时钟（实际）** | 1 | 2 | **4** |

⇒ 这次「优化」在生产上比它替换掉的代码**多一次往返**。

### 为什么上一轮全绿

上一轮的三条年龄判据，**全部**用「写完 verdict 立刻读快照」的夹具，
或用 `mr.SetTime()` 推进 miniredis 时钟。在那些夹具里
「verdict 写入时刻」与「快照读取时刻」落在同一秒，两个量恰好相等，
于是比错了时钟也看不出来。

⚠️ 这与上一轮 §10.4 记的坑同源：**判据的场景形状必须落在被测表达式的边界上**。
上一轮补判据时只检查了「场景是否够新鲜」，没检查「两个被比较的量是否真的不同」。

### 为什么 M6（护栏收紧到 0）当时也漏了

M6 收紧到 0 之后旧判据仍全绿，上一轮补了
`TestAudit_AgeGuardMustNotDisableTheOptimisation`。**本轮把它的制造手段也改了**
（见 §三），修完之后 M6 会判红 **6 条**而不是 0 条。

## 二、修法：给快照打上「本进程读取时刻」

`NodeState` 新增一个**不进 Redis** 的字段：

```go
SnapshotReadAt time.Time `json:"-"`
```

- `json:"-"` 是承重的：Lua 写入方会用 cjson 解码再重编码整个 state，多一个键
  要么被下次写丢掉，要么被当成存储字段读回来。
- 用 `time.Time` 而不是 `int64`：年龄是**本进程两次读之间的时长**，
  带着 Go 的**单调时钟**读数，NTP 跳变不会让一张新鲜快照看起来很老
  （那会静悄悄地又把优化关掉）。
- 打戳位置：`GetNodeStatesBatch`（整批一次，在 MGET **之前**取，与往返耗时无关）
  与 `GetNodeState`。
- 护栏改为 `time.Since(prefetched.SnapshotReadAt).Seconds() > prefetchMaxAgeSec`。
- **年龄检查不再采样 Redis 时钟**。它是本地时长；拿 Redis 时钟去比本地时刻
  正是该函数上一条注释警告的跨时钟比较。Redis TIME 仍只服务期限判定。

修后实测：

```
30 分钟前写的 verdict + 6ms 前的快照：GET=0 TIME=1（热路径共 2 次）
```

## 三、判据：连同「造场景的手段」一起改

三条旧判据用 `mr.SetTime()` 推进 miniredis 时钟来制造陈旧。年龄改成**本地时长**后，
推进 miniredis 时钟不再让快照变陈旧——**用例还在跑，但已经不测它声称测的东西**。
必须改拨的是快照自己的读取时刻：

```go
snapshot.SnapshotReadAt = snapshot.SnapshotReadAt.Add(-60 * time.Second)
```

⚠️ `TestAudit_AgeGuardMustNotDisableTheOptimisation` 是这里最危险的一条：
它原本拨的是 miniredis 时钟，改完语义后它的「快照年龄」实际退化成 ≈0，
而 ≈0 恰好是 **M6（护栏=0）杀不掉的形状** ⇒ 判据假绿。已一并修正，
现在它拨的是快照自己的时刻，M6 判红。

### 变异结果

| 变异 | 判红 |
|---|---|
| M5 删掉年龄护栏（永远信任快照） | 2 条 |
| M6 护栏收紧到 0（永远重读） | **6 条**（修前是 0 条） |
| M8 退回错时钟（比 `CapabilityUpdatedAt`） | 3 条 |

## 四、埋点

`metrics/node_state_prefetch_metrics.go`：

| 指标 | 用途 |
|---|---|
| `llmgw_node_state_prefetch_dropped_total{reason=stale\|unstamped}` | 护栏丢弃了多少快照 |
| `llmgw_node_state_prefetch_age_seconds` | **被采信**快照的实际年龄分布 |

**为什么两个都要**：只有计数器时，「从不触发」与「触发率 0.1%」在面板上难以区分，
而后者恰恰是要调参的信号。直方图直接给出分布。

**不引入新的热路径往返**——全部是进程内 Prometheus 采样，年龄由已存在的
快照时间戳算出。

桶布局是被判据逼出来的，不是预防性写法：`TestNodeStatePrefetch_BucketsCoverTheGuard`
判红后发现 `ExponentialBuckets(0.001, 2, 13)` 顶档只到 **4.096s**，
**低于** 5s 护栏——顶档必须超过护栏，否则看不到「样本正在逼近护栏」。
改成 14 档（顶档 8.192s）。第一次改时我按 `0.001×2^13=8.192` 心算，
忘了公式是 `start×factor^(count-1)`，档数少了一档；是判据第二次把它抓出来的。

## 五、5s 的实测校准

### 量哪个区间

快照 MGET 发生在**路由**里（`filterHealthyNodes` / `chooseLeastCooledCandidate`），
它落在瀑布的 **T2 total-dequeued → T5 cred-enqueued** 之间
（`pipeline.go:1648` 打 T2，`pipeline.go:2093` 打 T5，中间是模型解析与凭据选择）。

⚠️ **不是**总排队时长。实测同一批行上：

| 区间 | p50 | max | 含义 |
|---|---|---|---|
| T1→T2 准入队列 | 0.004s | 0.082s | 准入，毫秒级 |
| T3→T4 模型队列 | 0.000s | 0.000s | **空** |
| T5→T6 凭据队列 | 0.004s | 0.024s | 毫秒级 |
| **T2→T5 路由/选择** | 0.005s | **26.044s** | **秒都在这里** |

⇒ 那 5–26s 的尾巴**不是队列深度**。谁要是照「dispatch 排队 P99」去校准，
量到的就是错的分布。

### 分布（168 行完整瀑布，窗口按 `t0_arrived_at`）

```
p50 0.005s   p90 0.040s   p95 5.41s   p99 17.88s   max 26.04s
> 5s: 11/168 = 6.5%
```

| 桶 | n | 占比 |
|---|---|---|
| <50ms | 144 | 85.7% |
| 50–200ms | 12 | 7.1% |
| 0.2–1s | 1 | 0.6% |
| 1–5s | 0 | 0% |
| **5–15s** | 5 | **3.0%** |
| **15–30s** | 6 | **3.6%** |

### 结论：5s 保留，但理由要换

分布是**双峰**的：93.5% 的请求在几十毫秒内完成路由，剩下 6.5% 落在 5–26s。

5s **切在尾巴中间而不是尾巴之上**——保住 93.5% 流量上的优化，
并对那 6.5% 真排了队的请求主动重读。这正是护栏该做的事。

- **不升到 30s**（观测到的 max）：那意味着信任最多 26s 前的快照，
  恰好是护栏要封的那个无界陈旧窗口。护栏的职责是**给「与新鲜读的偏离」封上界**，
  不是把命中率拉到最大——**丢掉的快照永远安全（会重读），错信的才不安全**。
- **不降到 p90（0.04s）**：尾巴正是护栏的用武之地。把 40ms 以上的全拒掉，
  会把「真排过队的请求」全部变成重读，指标也就失去了它本来的用途。

### 可复现的查询

```sql
WITH d AS (
  SELECT extract(epoch FROM (t5_cred_enqueued_at - t2_total_dequeued_at)) AS r
  FROM request_logs_hot
  WHERE t0_arrived_at IS NOT NULL                 -- 窗口必须用 t0_arrived_at
    AND t2_total_dequeued_at IS NOT NULL
    AND t5_cred_enqueued_at IS NOT NULL
    AND t5_cred_enqueued_at >= t2_total_dequeued_at
)
SELECT count(*),
       round(percentile_cont(0.50) WITHIN GROUP (ORDER BY r)::numeric,3),
       round(percentile_cont(0.90) WITHIN GROUP (ORDER BY r)::numeric,3),
       round(percentile_cont(0.95) WITHIN GROUP (ORDER BY r)::numeric,3),
       round(percentile_cont(0.99) WITHIN GROUP (ORDER BY r)::numeric,3),
       round(max(r)::numeric,3),
       count(*) FILTER (WHERE r > 5)
FROM d;
```

样本：本地 `request_logs_hot`，751 行带 `t0_arrived_at`，其中 168 行有完整瀑布，
时间窗 2026-10-02 21:19 → 2026-10-03 05:27。

⚠️ 这是**本机/开发环境**的流量，绝对值不能直接当生产分位数用。
它的作用是**证明 5s 落在分布的哪个位置**（双峰、6.5% 越过），
以及**钉住「秒在 T2→T5 而不是队列里」这个结构性事实**。
生产真实分位数等部署后从 `llmgw_node_state_prefetch_age_seconds` 读。

## 六、仍然未做 / 仍未验

- **未部署** ⇒ 无生产侧结论；本节的绝对分位数是本机数据。
- `llmgw_node_state_prefetch_dropped_total` 上线后需盯：
  若 `reason="stale"` 持续非零且 `age_seconds` 的 P95 顶到桶沿，
  说明生产排队比本机更重，应按分布上调而不是拍脑袋。
- `reason="unstamped"` 持续非零 ⇒ 有构造点绕过了读路径（当前为 0 是预期的）。
- 其余遗留见 handoff：TIME 搭车（须单独一轮）、流式能力位无人写、
  每日探测预算闸门、多实例并发 upsert 未实测。
