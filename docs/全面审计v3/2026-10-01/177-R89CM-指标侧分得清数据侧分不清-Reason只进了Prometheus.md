# 177 号 · R89-CM —— 指标侧分得清、数据侧分不清：`Reason` 只进了 Prometheus，没进 `request_logs`

> **日期**：2026-10-01
> **轮次**：R89-CM（第 77 轮，审计第 177 号）
> **类型**：**关掉 176 号的下一轮入口** + 一条低风险可修的不对称（新增 **待裁决 71，P3**）
> **改动生产代码**：无　**改动数据库**：无
> **上一轮**：176 号（否定结论：自检 key 与业务 key 不共用限流桶）

---

## 〇、起手

176 号 §三 留下的入口：

> `rate_limit.go:86/98/101/104` 四个 `Blocked` 分支各带一个 `Reason`，
> **而 `Reason` 是否被写进 `request_logs` 未核** ⇒ 若没写，这四种完全不同的阻断原因在数据上无法区分。

**本轮核完了。答案是：没写。**

---

## 一、F1：`Reason` 在两个出口上待遇不同

### 出口 A —— 指标：✅ **完整保留，且有兜底**

`domains/streaming/rate_limit_metrics.go:8-25`：

```go
var gatewayRateLimitRejectionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_rate_limit_rejections_total",
		Help: "Gateway API-key rate-limit rejections by admission reason.",
	},
	[]string{"reason"},
)

func recordGatewayRateLimitRejection(outcome rateLimitOutcome) {
	if !outcome.Blocked { return }
	reason := outcome.Reason
	if reason == "" { reason = "unknown" }      // ← 有兜底
	gatewayRateLimitRejectionsTotal.WithLabelValues(reason).Inc()
}
```

**⇒ 指标侧带 `reason` 标签，四种原因 + `unknown` 全部可分。**

### 出口 B —— 数据：🔴 **只写死一个码**

`domains/streaming/handler.go:2286` 的闭包签名：

```go
captureAndEmitRateLimited := func(errCode, errMsg string, providerID, credentialID *int) {
```

**⇒ 四个参数，**没有 `Reason` 的位置。**

而 `handler.go:3229-3234`：

```go
if rlOutcome.Blocked {
	recordGatewayRateLimitRejection(rlOutcome)                                    // ← 带 Reason
	captureAndEmitRateLimited("rate_limit_exceeded", "rate limit exceeded", nil, nil)  // ← 不带
	...
}
```

**⇒ 同一个 `rlOutcome`，两条出口，一条拿得到 `Reason`，一条拿不到。**

**⇒ `rate_limit.go` 里四个 `Blocked` 分支的 `Reason`
（`queue_budget_exceeded` ×2、`queue_full`、`bucket_timeout`）
全部塌缩成 `error_kind = 'rate_limit_exceeded'`。**

---

## 二、🔴 F2：这就是我 171–173 号误判的确切根源

回看 171 号我怎么解释那 326,126 次：

> 「**不知道** 触发的是**并发**闸门、**速率**闸门，还是**预算**闸门。」

**⇒ 我当时不知道，是因为它们在 `request_logs` 里长得一模一样。**
**⇒ 而我 173 号把 `IsInternal` 豁免查完之后，缺的那个「原因」我始终没有回头找 —— 因为它在数据里不存在。**

**⚠️ 这也解释了 176 号 §三 为什么「更清晰了却仍未定」**：
09-28 那 1,274 次业务限流，到底是配额打满还是队列背压？
**查 `request_logs` 永远查不出来**，必须查 `/metrics` 的 `llm_gateway_rate_limit_rejections_total{reason=...}`。

**⇒ 这是一条「可观测性不对称」缺陷，而不只是「少存了一个字段」。**

---

## 三、影响面（§52 四维）

| 维度 | 答案 |
|---|---|
| 会不会失败 | **不会** —— 客户端仍拿到正确的 429 与 `Retry-After` |
| 会不会波及 | **不会** —— 业务逻辑不依赖这个字段 |
| 当事方知不知道 | **不知道** —— 运维看 `request_logs` 会把四种原因当成一种 |
| 有没有留痕迹 | **部分有** —— Prometheus 侧完整，**数据侧没有** |

⇒ **P3**：不是功能缺陷，是**可观测性缺口**。
⇒ **但它的真实代价已经付过了** —— **审计方（我）因此连续四轮走错方向**（171→176）。

---

## 四、修法（建议，⚠️ 不擅自动手）

| 方案 | 做法 | 代价 | 风险 |
|---|---|---|---|
| **1（推荐）** | 给 `captureAndEmitRateLimited` 闭包**加一个 `reason` 参数**，把 `rlOutcome.Reason` 透传进 `errCode`（如 `queue_full` / `bucket_timeout` / `queue_budget_exceeded` 与 `rate_limit_exceeded` 并列） | 改 1 个闭包签名 + **2 个调用点**（`:3231` 与 `:2376`） | **⚠️ `error_kind` 取值面变大** ⇒ 既有看板/查询若写了 `WHERE error_kind='rate_limit_exceeded'` 会漏掉新值 ⇒ **需产品/运维确认** |
| **2** | 不改 `error_kind`，另加一列 `rate_limit_reason` | 需要迁移 + 视图改动 | 成本更高，但**不破坏既有取值** |
| **3** | 只在文档里写明「数据侧不区分，要区分请查指标」 | 最低 | **治标**；本轮证明文档不足以阻止误判 |

**⇒ 推荐方案 1，但必须先确认没有下游依赖 `error_kind` 的精确匹配。**

**⚠️ 按纪律登记为待裁决 71（P3）** —— 改的是 `request_logs.error_kind` 的取值面，
**属于对外可见的数据契约**，本代理不擅自动手。

---

## 五、playbook §70 新增

> **§70 一个字段在两个出口上待遇不同时，先确认「哪个出口是我一直在查的」—— 查错出口会让你以为字段不存在**

**由来**（R89-CM / 177 号）：`rlOutcome.Reason` 进了 Prometheus（带标签、有 `unknown` 兜底），
**但没进 `request_logs`**。于是：

- 我在 171 号问「触发的是哪种闸门」→ 查 `request_logs` → 四种塌缩成一个 ⇒ **我以为代码没记**
- 176 号问「09-28 那 1,274 次是配额还是背压」→ 同样查 `request_logs` ⇒ **同样查不出**

**⇒ 而真相是：它被记了，只是记在另一个出口。**

**⇒ 落地三条：**
1. **当某个「看起来没被记录的字段」查不到时，先列出它的所有写入出口**（内存指标 / 指标库 / 日志 / 业务表 / 响应头），**逐一确认哪个出口有、哪个没有**；
2. **本仓的可观测性出口至少有 5 个**：`request_logs`（`error_kind`/`request_status`）、
   Prometheus（`llm_gateway_*`）、`slog`、HTTP 响应头（如 `X-LLM-Gateway-RateLimit-Scope`）、
   `gwtrace` 字段。**「查不到」常常是「查错了那一个」**；
3. **审计方要特别警惕**：本仓里 **「数据侧查不到」已经被我误当成「不存在」至少 3 次**
   （171 号的闸门种类、176 号的背压 vs 配额、以及更早的 §67 那条）。

**⇒ 与既有条目的关系**：§46「尺子不对结论必错」讲的是**尺子量错对象**；
**本条讲的是**尺子本身没坏、但**你手里拿的是这一把，而那一把上没有这个刻度**。

**同族**：§46 / §59（尺子错的两种形态）/ §61（单侧分布要配对照）/
§66（守卫存在 ≠ 守卫可达）/ §69（共用桶要读数据结构）。
