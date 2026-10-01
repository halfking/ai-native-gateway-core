# 123 号｜R89-AJ：闭合 122 号的 2 条必核 —— **`Retry-After` 确认无解析；Redis `Index` 是「层级错位」而非「漏接线」**

- 日期：2026-10-01
- 轮次：R89-AJ
- 起点：122 号 §三的 2 条必核，**本轮两条都闭合**
- 零生产代码、零配置、零门

---

## 一、必核 ① 闭合：`Retry-After` 确实无解析（**第二种检索方式复核通过**）

### 1.1 复核用的检索形态

按 122 号自陈的边界（「若某处用 `resp.Header.Get(...)` 读出后**以别的变量名**传递则抓不到」），
本轮改用**另一种形态**搜：

```
grep -rnE 'Header\.Get\("[Rr]etry|Header\["[Rr]etry|X-RateLimit|x-ratelimit|RateLimit-Reset|ratelimit'
```

**⇒ 生产代码（非 vendor、非测试）中 0 命中。**

唯一的 `ratelimit` 命中是**网关自己的入站限流**，与上游无关：

- `cmd/gateway/main_settings.go:40/45/54` — `settings.Global.Spec(ratelimit.RateLimitGateKey)`、
  `ratelimit.SetRateLimitEnabled(v)`
- `cmd/gateway/main.go:1397` — 「`ratelimit` 包内的 `atomic.Bool` 缓存，热路径不再读 settings KV」

**⇒ 这是一条限制**对**自己客户端**的开关，**与「解析供应商返回的限流指示」完全是两件事**。

### 1.2 闭合结论

**122 号的结论成立**：
现役**既不读 `Retry-After` 响应头，也不读 `X-RateLimit-*` / `RateLimit-Reset` 系列头**，
而 `RetryAfter` 字段的 4 个赋值点（122 号已枚举）全部来自网关自己的配置/配额/队列溢出。
**且管道已通**（`coolingDuration` 已实现「优先用 `retryAfter`、带 `maxCoolingDuration` 上限」），
**缺的只是最上游那一步解析**。

**⇒ 这是本次审计中唯一一条经过两种检索方式复核的「能力缺失」类结论。**

---

## 二、必核 ② 闭合：Redis `Index` 是**层级错位**，不是漏接线

### 2.1 三段都不做扫描（逐段读函数签名确认）

| 段 | 位置 | 是否扫描/取数 |
|---|---|---|
| URSM v2 | `manager.go:420 FilterAndScoreReadyWithSource(ctx, seeds []CandidateSeed, ready bool)` → 内部 `filterAndScore` | **否**——候选是**入参** |
| Router | `router.go:220/231/255` 三个导出入口 → `planCandidates` → `:283 candidates = deduplicateCandidates(candidates)` | **否**——只做**去重** |
| 装配层 | `cmd/gateway/main.go:3046`（注释：「resolve 经同一 `Router.PlanCandidatesPinned` 产出运行时…」）、`:1508-1509`（`PlanCandidates` 在 `mode=authoritative` 时按 v2 `FilterAndScore` 过滤） | **候选在此构建** |

### 2.2 顺带修正一处检索失误（如实登记）

我第一次搜 `PlanCandidates(` 时加了排除条件，得出「**零外部调用**」，
**这与「路由确实在跑」直接矛盾**，所以判定是自己搜错了。
重搜（不排除）发现：**真正的活跃入口是 `PlanCandidatesPinned`**
（`cmd/gateway/main.go:3046`、`domains/dispatch/failover.go:169`、
`domains/dispatch/pipeline.go:20` 三处引用），
而 `PlanCandidates` / `PlanCandidatesWithContext` 两个入口**确实只有测试引用**
（`cmd/gateway/main_credentialstate_assembly_test.go:15`）。

⚠️ **这本身是一条待核项**：Router 有 **3 个导出候选规划入口，只有一个在生产被调用**，
另两个只被测试引用 ⇒ **可能是遗留入口，也可能是测试在守护一个已停用的 API**。
**本轮不判定**，列入待跟进。

### 2.3 结论

**`domains/ursm/v2/index` 写的 Redis 索引，若要生效，落点在 `cmd/gateway/main.go` 的候选构建层**——
**既不在 URSM 内部，也不在 router 内部**。

**⇒ 这不是「实现了却忘了接线」，而是「索引写在了管不到数据的层」——
即层级错位。**

**这个重新定性很重要**：
- 若按「漏接线」处理 ⇒ 正确做法是把 `Index` 接到 URSM 自己的入口，
  **但那样接不上**（因为数据不在那一层）；
- 按「层级错位」处理 ⇒ 正确做法是**先确认最上游的候选构建是否真的需要索引**
  （它很可能本来就是从内存里的绑定表来的，每请求成本很低），
  **若不需要，则 `index` 包应当删除而不是接线**。

**⇒ 因此本轮不把它记为「漏接线」，也不建议接线**，
而是登记为「**层级错位，需先确认最上游是否需要索引才能决定去留**」。

---

## 三、本轮收口

| 122 号必核项 | 状态 |
|---|---|
| ① 「无 `Retry-After` 解析」 | ✅ **闭合**：第二种检索形态（`Header.Get` / `X-RateLimit-*` / `RateLimit-Reset`）0 命中；唯一的 `ratelimit` 是**自己的入站限流**，无关 |
| ② v2 候选筛选是全量扫还是索引 | ✅ **闭合（定性）**：URSM 不扫、Router 只去重、候选在 `cmd/gateway/main.go` 构建 ⇒ **`index` 是层级错位，不是漏接线** |

**新增两条登记**（均不下结论）：
1. **`domains/ursm/v2/index` 归「层级错位」** ——
   先确认最上游候选构建是否真的需要索引，才能决定**接线还是删除**。
2. **Router 有 3 个导出候选规划入口，只 1 个在生产被调用** ——
   `PlanCandidatesPinned` 在用；`PlanCandidates` / `PlanCandidatesWithContext`
   **只有测试引用** ⇒ 可能是遗留入口，也可能是测试在守护已停用 API。

---

## 四、编号与去向

- **无新增待裁决条目**。
- 122 号的两条必核**全部闭合**，台账中相应「下轮必核」条目可以划掉。
- 本轮新增的两条登记进台账 §4.2 待跟进。
