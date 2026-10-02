# 121 号｜R89-AH：清掉 120 号的「下轮必核」—— **五层缓存只有 3 层存在**（并更正 120 号的「接线成本低」）、v2 的 Redis `Index` 从未被构造、`smartretry` 的能力高于普通退避

- 日期：2026-10-01
- 轮次：R89-AH
- 起点：120 号 §七列的 4 条「下轮必核」，本轮清掉 3 条
- 零生产代码、零配置、零门

---

## 〇、先更正 120 号的一处判断

120 号写「接线成本低（**5 个缓存层各调一次 `Recorder`**）」。**这个判断是错的**——现役**只有 3 层**。

**实测**：

```
$ ls cache/
prefix                    ← 顶层 cache/ 目录只有这一个子包
$ ls cache/semantic       → 不存在
$ ls cache/kv             → 不存在
```

**⇒ `cachemetrics` 的「五层」是设计期快照，现役只有 3 层真实存在。**

| `CacheLayer` 常量 | 现役实体 | 状态 |
|---|---|---|
| `prefix` | **`cache/prefix`**（`main.go:2815` 有开关日志；`handler.go:4040` **每请求调用** `prefix.Stabilize`） | ✅ 在用 |
| `delta` | `domains/hooks/compression/diff.go` 增量引擎 | ✅ 在用 |
| `session_state` | `domains/hooks/compression/session_cache.go` | ✅ 在用 |
| `semantic` | **无 `cache/semantic` 包** | ❌ **不存在** |
| `kv` | **无 `cache/kv` 包** | ❌ **不存在** |

**⇒ 修正后的接线成本判断**：
- 不是「5 个层各调一次 Recorder」，而是**只有 3 层可接**；
- `semantic` 与 `kv` 两层**要么等它们被实现，要么从 `CacheLayer` 枚举里删掉**——
  否则会写出一批「语义正确但没有对应对象」的指标行。

⚠️ **一个容易被忽略的细节**：`cache_metrics` 表的 CHECK 约束是
`cache_layer = ANY (ARRAY['semantic','prefix','delta','kv','session_state'])`
——**这 5 个值全都被允许写入**。所以约束**不阻止**写入 semantic/kv，
只会在数据里静默多出两层没有对象的行。
**约束是设计期快照，不是现役事实**——**读约束不能替代读代码**（§20 同族）。

**方向与本审计此前的多数案例相反**：通常是「代码比文档多」，
这次是「**约束/枚举比代码多**」。

---

## 一、必核 ②：`domains/ursm/v2/index` 的 Redis 索引**从未被构造**

搜 `index.New` / `index.Query` / `index.Candidate`，**唯一命��**是：

```
./internal/logging/bleve_fanout.go:240:  batch := b.index.NewBatch()
./internal/logging/bleve_fanout.go:255:  batch = b.index.NewBatch()
```

⚠️ **这是一个 §20 级的假阳性**：`b.index` 是 **bleve 的索引**，
与 `domains/ursm/v2/index` 无关——只是**包名前缀恰好都叫 `index`**，
被我的正则 `index\.New` 前缀匹配上了。

**⇒ 结论（干净）**：`domains/ursm/v2/index.Index` **从未被构造**，
零引用得到第二次独立确认。**URSM v2 的热路径（107 号确认的
`manager.go:420 FilterAndScoreReadyWithSource`）不走这个 Redis 索引。**

**判读**：这是一个**已实现但未启用的 Redis 候选索引**。
它**不是死代码**（有真实功能），**也不是已接线**（一条查询都没走）。
⇒ 归 **B 类**（120 号已归 B，此处补上「确证未被构造」这一更强的证据）。

⚠️ **未证实**：v2 当前的候选筛选是**全量扫 + 内存过滤**还是别的索引方式，
**本轮未核**。若当前数据量下全量扫够用，则不启用 Redis 索引是**合理取舍**
而非疏漏——**这一点必须下一轮核完才能定，否则会误判为「漏接线」**。

---

## 二、必核 ③：`smartretry` 的能力**高于**普通退避，与 dispatch 是「部分重叠 + 部分互补」

### 2.1 现役 dispatch 侧有什么

`domains/dispatch` 目录内符号计数：`backoff` × 16、`Backoff` × 7、`MaxRetries` × 4。
**⇒ 现役确实有退避重试。**

### 2.2 `smartretry` 提供什么（读 `retry.go` 的真实导出）

| 能力 | 符号 | 说明 |
|---|---|---|
| **按供应商统计成功/失败与延迟** | `RecordSuccess(provider, latency)` / `RecordFailure(provider, err, latency)` / `Stats(provider)` | 每供应商独立 |
| **滑动窗口失败率** | `rollingRate(old, n, event)` | 自适应的统计量，不是固定退避 |
| **重试决策** | `ShouldRetry(ctx, provider, attempt, err) (bool, time.Duration)` | **「该不该重试」与「退多久」分离** |
| **错误分类** | `isTimeout` / `is5xx` / `classifiableRetriable` | import 了 `net` / `net/http` / `syscall` / `io` / `errors` |
| **尊重上游 `Retry-After`** | `retryAfterOrBackoff` / `parseRetryAfter` | import `strconv`，**解析上游返回的 Retry-After 头** |
| 可注入时钟 | `SetClock(now func() time.Time)` | 便于测试 |

**⇒ 判读**：
- **重叠部分**：两者都算退避间隔。
- **不重叠部分（smartretry 独有）**：
  ① **按供应商的自适应失败率**（现役是固定退避参数）；
  ② **错误分类**（超时 / 5xx / 可分类可重试）；
  ③ **尊重上游 `Retry-After`**——
     **这一条最有价值**：被限流的供应商会明确告诉你什么时候该重试，
     忽略它意味着**要么白等要么提前撞限流**。
- ⇒ **本轮不下「应当用 smartretry 替代 dispatch」的结论**（缺对比证据），
  但**「现役是否尊重 `Retry-After`」是一条可直接验证、且有明确收益的差距**，
  已列入下轮必核。

---

## 三、必核 ④：`domains/hooks` 目录其余零导入子包 —— **本轮未做，如实登记**

`domains/hooks` 下除已定性的 `intentanalysis`（119 号）、`memoraauto`（116 号）、
`promptoptimization`（115 号）外，还有 `audit` / `cache` / `compression` /
`goal` / `handoff` / `observability` / `outputcompliance` / `promptinjection` /
`response` / `security` / `session-inspector` / `sessionanalysis` / `sessionaudit` /
`toolexecution` / `tools` 等目录。

⚠️ **其中 `compression` / `observability` / `promptinjection` / `sessionaudit`
在前几轮已被确认「在用」**（119 号的 D 类判定正是基于这一点）。
**⇒ 120 号 §六.1 说「余下 3 个在 `domains/hooks` 目录」这句话本身不准确**：
真正需要逐个核的是**那些既没被 119 号确认在用、也没被列为零导入的目录**
（如 `goal` / `handoff` / `cache` / `security` / `response` / `audit` /
`outputcompliance` / `session-inspector` / `sessionanalysis` / `toolexecution` / `tools`）。

**⇒ 120 号该句已在本号一并更正**，并在台账登记为下一轮的独立任务。

---

## 四、本轮收口

| 必核项 | 状态 |
|---|---|
| ① `cachemetrics` 五层是否还在 + 口径 | ✅ **已核：只有 3 层**，并**更正 120 号的「接线成本低」** |
| ② v2 热路径是否走 Redis `Index` | ✅ **已核：从未被构造**（并记录 bleve 假阳性） |
| ③ `dispatch` 重试与 `smartretry` 是否重叠 | 🟡 **部分已核**：重叠在退避，smartretry 独有自适应/分类/`Retry-After`；**是否替代不下结论** |
| ④ `domains/hooks` 目录其余子包 | ❌ **未做**，并**更正 120 号对该句的不准确表述** |

**新增下轮必核 2 条**：
1. 现役重试**是否尊重上游 `Retry-After`**（有明确收益、可直接验证）；
2. URSM v2 当前的候选筛选是**全量扫还是索引**——决定 Redis `Index` 未启用
   是「合理取舍」还是「漏接线」。

---

## 五、编号与去向

- **无新增待裁决条目**。
- **已回灌 120 号两处更正**（「5 个层各调一次 Recorder」/「余下 3 个在 `domains/hooks`」）。
- `cachemetrics` 的接线建议随之修正：**只接 3 层**，
  并决定 `semantic` / `kv` 是「等实现」还是「从枚举删掉」。
