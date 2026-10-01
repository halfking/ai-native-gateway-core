# 122 号｜R89-AI：清掉 121 号的 2 条必核 —— **「尊重 `Retry-After`」的管道已通、水源未接**（用赋值点枚举坐实，非关键词缺失）

- 日期：2026-10-01
- 轮次：R89-AI
- 起点：121 号 §四列的 2 条「下轮必核」，本轮**两条都做了**
- 零生产代码、零配置、零门

---

## 一、必核 ①：现役**不解析上游的 `Retry-After`** —— 但**管道已经通了**

### 1.1 先纠正我自己的取证方式

我第一遍用的是**关键词检索**（`grep "Retry-After|RetryAfter"`），
得到「只有 `Set("Retry-After")` 在给**自己的客户端**加响应头」的印象。
**这个取证方式不合格**（§20 同族：**关键词缺失不等于能力缺失**），
因为它没回答「值从哪来」。

**改用赋值点枚举**（`grep "RetryAfter:"`）后，结论不仅成立，而且**精确得多**。

### 1.2 全仓 `RetryAfter:` 的**全部**赋值点

| 赋值点 | 值来源 | 是上游响应吗 |
|---|---|---|
| `cmd/gateway/main.go:6419` `DispatchRetryAfter: …cfg.HostedTasks.DispatchRetryAfterSeconds…` | **网关自己的配置** | ❌ |
| `domains/tenant/quota.go:140/143/146/189` | **网关自己的租户配额**（QPS / tokens / 并发溢出） | ❌ |
| `domains/dispatch/dispatcher.go:285` | `DefaultOverflowRetryAfter`（**网关自己的模型队列满**） | ❌ |
| `domains/dispatch/failover.go:399` | `DefaultOverflowRetryAfter`（**网关自己的凭据队列满**） | ❌ |

**⇒ 没有任何一个赋值点来自「解析供应商返回的 `Retry-After` 响应头」。**

### 1.3 而管道**已经存在**（这是本条最有价值的部分）

```
domains/credential/writer.go:167  recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))
domains/credential/writer.go:357  recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))

domains/credential/writer.go:596  func coolingDuration(kind errorsx.ErrorKind, retryAfter time.Duration) time.Duration {
                                    if retryAfter > 0 {
                                        if retryAfter > maxCoolingDuration { … }   // 有上限保护
                                        return retryAfter                          // ← 优先用上游给的值
                                    }
                                    …                                            // 否则按 kind 取默认
```

并且 `domains/dispatch/planner.go:221` 有 `RetryAfter: out.RetryAfter` —— **值一路被搬运**。

**⇒ 一句话结论：「尊重供应商 `Retry-After`」的完整管道
（响应 → failure 结构体 → planner → 凭据冷却时长，且带 `maxCoolingDuration` 上限保护）
**已经建好并接好；唯一缺的是最上游那一步——从响应里把它读出来填进去。**

**⇒ 判读**：当供应商返回 `429` + `Retry-After: 30` 时，
网关**看不到这个 30 秒**，凭据冷却完全按自己的 kind 默认值走。
后果是两种错误之一：**冷却太短 ⇒ 提前再撞限流（429 放大）**；
**冷却太长 ⇒ 白白闲置一个可用凭据**（在有多个凭据时直接降低可用容量）。

**⇒ 这是本审计目前遇到的性价比最高的一条**：
**不是「缺一套机制」，而是「机制建好了、只差一个解析」**。
且它与 120/121 号的 `smartretry` 呼应：
`smartretry` 的 `parseRetryAfter` 恰好就是缺的那一步。

**⚠️ 诚实边界**：本轮**只枚举了 `RetryAfter:` 的字面赋值**。
若某处用 `resp.Header.Get("Retry-After")` 读出后**以别的变量名**传递，
本轮抓不到。**⇒ 已列入下轮复核**，不宣称「绝对没有」。

---

## 二、必核 ②：v2 候选**不是 URSM 自己扫的** ⇒ Redis `Index` 的位置被收窄

- `manager.go:420 FilterAndScoreReadyWithSource(ctx, seeds []CandidateSeed, ready bool)`
  → 内部 `filterAndScore(ctx, seeds, ready)`：**候选由调用方传入**，
  **URSM v2 自己不做任何扫描/取数**。
- 唯一调用点 `domains/streaming/executors/router.go:383`：
  `r.URSMv2.FilterAndScoreReadyWithSource(ctx, seeds, *readySnapshot)`，
  而 `seeds` 是在同一函数里由 `for i, c := range candidates` 构造的
  （配合 `seedLookupKey(seeds[i].ProviderID, c.CredentialID, c.BindingRawModel())` 的 allow 过滤）。

**⇒ 收窄结论**：**Redis `Index` 若要启用，落点在 `router.go` 的候选构建处，不在 URSM 内。**
（这排除了「URSM 内部漏用自己写的索引」这一解读。）

⚠️ **仍未证实**：`candidates` 本身是全量扫表得到、还是已有别的索引/缓存。
**本轮未追到 `candidates` 的构建点**（上下文预算已到上限）。
**⇒ 决定「Redis `Index` 未启用是合理取舍还是漏接线」的那个问题，
仍然开放；下轮只需追 `candidates` 的来源即可闭合。**

---

## 三、本轮收口

| 121 号必核项 | 状态 |
|---|---|
| ① 现役是否尊重上游 `Retry-After` | ✅ **已核：不解析；但管道已通只差解析**（并**更正了自己的取证方式**） |
| ② v2 候选筛选是全量扫还是索引 | 🟡 **部分已核**：URSM 自己不扫，候选来自 `router.go` 的 `candidates`；`candidates` 来源**仍未追** |

**新增下轮必核 2 条**（都很小）：
1. 复核「无 `Retry-After` 解析」——用 `resp.Header.Get("Retry-After")` 形态再搜一次，
   排除「换个变量名传递」的可能。
2. 追 `domains/streaming/executors/router.go` 里 `candidates` 的构建点，
   闭合「Redis `Index` 未启用是取舍还是漏接线」。

---

## 四、编号与去向

- **无新增待裁决条目**（本条属「修法明确、收益明确」，但**仍不擅自动手**——
  它要改的是凭据冷却这一敏感路径，且需要产品确认是否要遵循上游指示）。
- **登记为高性价比待跟进**：`Retry-After` 解析缺口
  ——**管道已通，只差一个 `resp.Header.Get` + 填字段**。
- **取证方式更正**已写入 §1.1：**关键词缺失不等于能力缺失，
  判定「某值从哪来」必须枚举赋值点，不能只搜关键词。**
