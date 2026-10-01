# 149 号｜R89-BK：**objective 第 25 项收口**（「错误表 + 凭据详情呈现 + 服务质量」）—— 链路是通的，但有**两套并行的错误表**，且「是否已解决」这一维度**存在却从未被使用**

- 日期：2026-10-01
- 轮次：R89-BK
- 起因：覆盖总览里 objective 第 25 项
  （「这些错误需要单独记录到一个 log 错误表中…**这些异常信息需要呈现在凭据的详情下，
  用于评估供应商的服务质量**」）状态仍是**部分**。
  本轮用 143 号立的 **§37 四维度**（路径全等 / 方法匹配 / 鉴权 wrapper / 有没有人会碰到它）
  把这条链路逐环坐实。
- **结论先行**：
  1. **✅ 链路是通的**：`GET /api/vendors/credentials/{id}/error-detail`
     （**注册带 `admin(...)` 鉴权 wrapper**）→ `loadVendorErrorSummary` →
     `web/src/api/vendor-credential-error.ts:78` → `web/src/views/provider-detail/ErrorDetailTab.vue`
     —— **四维度全过**。
  2. **⚠️ 且页面上是有聚合的**（这一点修正了我本轮的初始假设）：
     `loadVendorErrorSummary` 执行
     `SELECT error_type, is_retryable, stage, COUNT(*), MAX(occurred_at), COUNT(DISTINCT http_status)
      FROM supplier_errors_unified … GROUP BY 1,2,3`
     ⇒ **凭据详情页展示的是按「错误类型 × 可重试性 × 阶段」聚合后的分布，不是原始流水。**
  3. **⚠️ 但存在两套并行的错误表 + 两条读路径**：
     | | 表 | 谁写 | 谁读 | 行数 |
     |---|---|---|---|---|
     | **在用** | `supplier_errors_unified` | 错误记录管线 | **凭据详情页** | 有数据 |
     | **在写无读** | `provider_error_details` | `bg/provider_error_aggregator.go`（聚合器在跑） | `error-stats` 端点**零调用方** | **36,401** |
  4. **⚠️ 最实的一条：「错误是否已解决」这个语义存在，但从未被使用。**
     `provider_error_details` 有 `resolved` 列，**真库实测 `resolved=true: 0` / `resolved=false: 36,401`**；
     且 `resolved` 在前端 `vendor-credential-error.ts` 与 `ErrorDetailTab.vue` 里 **0 命中**。
     ⇒ **36,401 行预聚合错误里，没有一行被标记为已解决，这个维度在凭据详情页上完全不可见。**
  5. **⇒ objective 第 25 项可以从「部分」上调为「有（附两条 P2）」。**

---

## 一、四维度逐环取证

### ① 路径全等 ✅

```go
// admin/handler.go:1247
mux.HandleFunc("/api/vendors/credentials/{id}/error-detail",
    admin(h.vceHandlers.getVendorCredentialErrorDetail))
```
```ts
// web/src/api/vendor-credential-error.ts:78
`/api/vendors/credentials/${credentialId}/error-detail?${params.toString()}`
```
**路径与参数完全一致**（143 号里 `session-export` 那种「名字对、路径错」的形态**没有**重演）。

### ② 方法匹配 ✅ ③ 鉴权 wrapper ✅

注册时包了 `admin(...)` ⇒ **不是 39 号那种零鉴权形态**（对比 `/admin/api/v1/health-checks`）。
且函数内 `loadVendorCredentialMeta(ctx, credentialID, EffectiveTenantIDAll(r))`
⇒ **带租户作用域**（对比 43 号那种跨租户读）。

### ④ 有没有人会碰到它 ✅

`web/src/views/provider-detail/ErrorDetailTab.vue` 存在（**非零调用**，
与 143 号那三个「零调用孤儿包装」是相反的形态）。

## 二、页面上展示的是什么（**修正我本轮的初始假设**）

我一开始假设「页面上是原始流水、聚合表才有汇总」—— **这是错的**。
`loadVendorErrorSummary` 的实际 SQL：

```sql
SELECT error_type, is_retryable, COALESCE(NULLIF(stage,''),'unknown') AS stage_bucket,
       COUNT(*)::int, MAX(occurred_at), COUNT(DISTINCT http_status)::int
FROM supplier_errors_unified
WHERE … GROUP BY 1, 2, 3
```

⇒ **凭据详情页给的是按「错误类型 × 可重试性 × 阶段」的三维聚合分布 + 各维计数 + 最近发生时间
+ 涉及多少种 HTTP 状态码。** 这已经能满足 objective 的「用于评估供应商的服务质量」的主要诉求。

## 三、⚠️ 剩下的真实缺口：两套并行 + 一个从未被使用的维度

### 3.1 两套并行的错误表

| | `supplier_errors_unified`（在用） | `provider_error_details`（在写无读） |
|---|---|---|
| 形态 | **原始事件流** | **预聚合**（`occurrences` / `first_seen_at` / `last_seen_at` / `resolved`） |
| 写方 | 错误记录管线 | `bg/provider_error_aggregator.go`（`INSERT INTO provider_error_details`，`bg/provider_error_aggregator.go:381`） |
| 读方 | **凭据详情页** | `admin/provider_credential.go` 的 `error-stats` 端点 —— **零调用方** |
| 行数 | 有数据 | **36,401** |

⚠️ **这不是新发现** —— `admin/provider_credential.go:1240-1243` 的注释**今天（R75，2026-10-01）
已经把它钉准了**：
> 「panel needs（"this credential's errors"）——**这句是错的**，读端曾据此被认定已闭环。
> 实际前端凭据详情页调的是 `error-detail`，**从不调用本端点**。…因此 `provider_error_details`
> 的唯一生产读端就是这里，而它当前没有 UI 消费方——**聚合管线本身在跑、数据在写、但没有面板读**。
> 是否下线聚合器、或把本端点接进凭据详情页，**属产品裁决（见 45 号报告 §8）**。」

**⇒ 本轮的价值是把这个已登记项的边界量化清楚了**（下节），
**而不是重新发现它**。

### 3.2 ⚠️ 量化出来的精确边界（本轮新增）

**「在写、无面板读」这个说法容易让人以为「面板上没有聚合」——那是错的**（见 §二）。
真正的缺口窄得多，具体是**两个维度不可见**：

| 维度 | 在 `provider_error_details` | 在凭据详情页 | 状态 |
|---|---|---|---|
| 按错误类型聚合 | `occurrences` | ✅ 有（`COUNT(*)`） | 页面已覆盖 |
| 可重试性 / 阶段 | — | ✅ 有 | 页面已覆盖 |
| HTTP 状态码多样性 | — | ✅ 有（`COUNT(DISTINCT http_status)`） | 页面已覆盖 |
| **`resolved`（是否已解决）** | ✅ 有列 | ❌ 无 | **36,401 行全部 `false`；前端 0 命中** |
| **`first_seen_at`（首次发生）** | ✅ 有列 | ❌ 无（只有 `MAX(occurred_at)`） | 不可见 |

⚠️ **`resolved` 的状态最值得单独说**：真库实测 **`resolved=true: 0` / `resolved=false: 36,401`**
⇒ **这个列存在、有一列默认值，却从未被任何代码置真过** ⇒
**「错误是否已解决」是一个被设计了但从未启用的语义**：
聚合器只写不裁决，没有任何流程会去把某条错误标记为已解决。

**这与本会话反复出现的家族同源**：
**「未定义」被编码进了一个具体的默认值（0/false），而不是「未裁决」状态**
（与 47 号 `COALESCE(...,0)`、131 号「未定义不可编码进 0/NULL」同一族，**但方向相反**：
那次是**该区分的没区分**，这次是**预留了区分位却永远不填**）。

## 四、建议（待裁决 61，不擅自动手）

**不建议直接下线聚合器，也不建议直接接线** —— 先由产品答一句：

> **「凭据详情页要不要展示『这个错误是否已经解决』以及『首次发生时间』？」**
>
> - **要** ⇒ 把 `error-stats` 端点接进 `ErrorDetailTab`（端点已存在、鉴权已包、SQL 已写好，
>   **只剩前端消费**），并**给 `resolved` 一个真正的写入方**
>   （否则接上去也是 36,401 行全 `false` 的假信息）。
> - **不要** ⇒ **下线聚合器**（`bg/provider_error_aggregator.go` + 表），
>   **但要先确认 `provider_error_details` 不是被别的东西依赖**（本轮实测唯一读端零调用方）。
>
> **⚠️ 中间态（最坏）**：两边都留着、都不裁决 ⇒
> **每一条错误被存了两次（36,401 行预聚合 + 原始流水），且没有任何一处知道它是否已解决。**
> **这就是当前的真实状态**，它是**可接受的但需要被显式决策**的状态，不该靠「忘了」维持。

**诚实边界**：本轮**未**逐行读完 36,401 行对应的聚合逻辑；
`resolved` 「从未被置真」的结论来自**全表聚合**（`resolved IS TRUE` 为 0 行）
与**前端 0 命中**两项，**未检查是否存在其他写入路径**（如手工 UPDATE 脚本）。

---

## 五、playbook §43 新增

> **「某维度在 A 表有、在 B 页面没有」有两种完全不同的成因，处置相反：**
> ① **B 页面根本没做这个聚合** ⇒ 补 B；
> ② **B 页面做了另一种聚合，而 A 表的那个维度是「预留但从未启用」** ⇒ **补了也白补**。
>
> **证据（149 号）**：`provider_error_details.resolved` 有列、36,401 行全 `false`、前端 0 命中。
> **若只按「页面没展示 → 把表接上去」处置，接上去的是 36,401 行 `false` 的假信息。**
>
> **落地：把一个不可见维度接进 UI 之前，先查它在源表里**有没有真实的取值分布**：
> - **分布是常量**（全 true / 全 false / 全 0）⇒ **它其实没有被启用**，
>   **先补写入方，再谈 UI**；
> - **分布有变化** ⇒ 它是真的在用，接 UI 即可。
>
> **本会话已有一个同族但方向相反的案例**（131 号）：
> **「未定义」不可编码进 0/NULL** —— 那是**该区分的没区分**；
> **本条是「预留了区分位却永远不填」**。
> **两者都要在「值域是否恒定」这一步先查一眼。**

**同族**：§35（阈值来自对象自身配置）/ §36 / §37 / §39 / §40 / §41 / §42。
