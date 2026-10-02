# 179 号 · R89-CO —— 类化 177 号：16 个诊断 label 里只有 2 个进了 `request_logs`

> **日期**：2026-10-01
> **轮次**：R89-CO（第 79 轮，审计第 179 号）
> **类型**：**把 177 号从一个实例提升为一个类**（类化，不新增缺陷）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：178 号（七轮收敛总结 + 下一轮三条入口）

---

## 〇、起手

177 号发现 `rlOutcome.Reason` 进了 Prometheus 却没进 `request_logs`，并写下 playbook §70。

**但那只是第一个实例。** 问题是：**这是孤例还是一类？**
**若是一类，那么「查数据查不到原因」就���绝大多数细粒度诊断问题的常态，而不是我运气不好。**

---

## 一、F1：全仓指标 label 清单

对 `domains/` `bg/` `metrics/` 下全部 `*_metrics.go` 提取 `[]string{...}`：

| 出现次数 | label 组合 |
|---|---|
| **9** | `reason` ★ 全仓最常见 |
| 6 | `result` / `outcome`（各 6） |
| 5 | `tenant` |
| 4 | `store` / `backend,mode,result` |
| 3 | `status` / `source` / `protocol` / `mode` |
| 2 | `trigger` / `stage` / `script` / `endpoint` / `backend,mode` / `backend,mode,state` |
| 1 | `window_type,success` / `tenant,reason` / `task_type` |

**⇒ `reason` 出现 9 次，是全仓最常用的诊断维度。**
**⇒ 而 177 号那个「`reason` 只进指标」的具体实例，正是这个最常见维度的一个样本。**

---

## 二、F2：交叉核对 —— **16 个 label 里只有 2 个在 `request_logs` 里有列**

```sql
-- 把全部 label 名与 information_schema 求差集
```

| 指标 label | `request_logs` 里有对应列？ |
|---|---|
| `success` | ✅ HAS COLUMN |
| `task_type` | ✅ HAS COLUMN |
| `reason` | 🔴 **NO COLUMN** |
| `result` | 🔴 **NO COLUMN** |
| `outcome` | 🔴 **NO COLUMN** |
| `store` | 🔴 **NO COLUMN** |
| `backend` | 🔴 **NO COLUMN** |
| `mode` | 🔴 **NO COLUMN** |
| `protocol` | 🔴 **NO COLUMN** |
| `stage` | 🔴 **NO COLUMN** |
| `status` | 🔴 **NO COLUMN** |
| `source` | 🔴 **NO COLUMN** |
| `endpoint` | 🔴 **NO COLUMN** |
| `trigger` | 🔴 **NO COLUMN** |
| `window_type` | 🔴 **NO COLUMN** |
| `script` | 🔴 **NO COLUMN** |

**⇒ 16 个诊断维度里，14 个在 `request_logs` 上「根本没有刻度」。**

**⚠️ 注意 `status` 也没有** —— 业务表上叫 **`request_status`**，而且它是 **view 里从 `status_code` 推导出来的**
（`db/request_logs_view_schema.go:405`），**不是一张真实列**。
⇒ **连「状态」这个看似最基础的维度，都是派生值而非存储值。**

---

## 三、结论定性：这不是缺陷，但它是**审计方最容易踩的坑**

**⚠️ 必须说清楚：这本身不是设计缺陷。**

| 出口 | 用途 | 保留期 | 合理形态 |
|---|---|---|---|
| **Prometheus** | 实时告警、短窗口聚合、**高基数诊断维度** | 通常短期 | label 随便加 |
| **`request_logs`** | 长期分析、对账、**业务维度** | 月/年 | 列要稳定、要可 join |

**14 个诊断维度不进业务表，在架构上是正确的取舍**（否则 `request_logs` 会变成一张宽到没法维护的表）。

**⇒ 真正的问题在审计方（我）身上：我默认了「指标与业务表同构」。**
**⇒ 177 号那一次的代价是实打实的：我为此连续四轮走错方向（171→176）。**

**⇒ 更正 §70 的表述强度**（这是本轮对 playbook 的一处实质修订）：

> §70 原文：「一个字段在两个出口上待遇不同时，先确认哪个出口是我一直在查的。」
> **本轮修订为**：
> **「诊断维度体系与业务表基本正交 —— 查『为什么』之前先问『这个维度在不在我要查的那张表里』。」**
> 这不是**一个**字段待遇不同，而是**一整类**形态；**本仓 16 个 label 里 14 个只活在指标侧。**

---

## 四、可直接复用的「出口分工表」（下一轮审计照着用）

| 想回答的问题 | 该去哪个出口 | ❌ 不要去 |
|---|---|---|
| 「这次为什么被拒/被限流/被降级？」 | `/metrics` 的 `llm_gateway_*{reason,outcome,result}` | `request_logs` |
| 「这个 key/provider 的**配额**打满了吗？」 | `/metrics` + `api_keys` 配置表 | `request_logs` |
| 「**租户到 apikey** 的用量与对账」 | `request_logs` / `usage_ledger` | `/metrics` |
| 「会话压缩/脱敏是否生效」 | `request_logs.compression_strategy` + `session_turns` | `/metrics` |
| 「流量结构（模型分布、协议、端点）」 | `request_logs.client_model` 等**真实列** | `protocol`/`endpoint` label |

**⇒ 一句话**：**「为什么」走指标，「是多少 / 归谁」走业务表。**

---

## 五、playbook §71 新增

> **§71 查「为什么」之前先问「这个维度在不在我要查的那张表里」—— 本仓的诊断维度体系与 `request_logs` 基本正交**

**由来**（R89-CO / 179 号）：对全仓 `*_metrics.go` 的 label 求差集 —— **16 个里 14 个在
`request_logs` 上没有对应列**，且 `status` 这个看似基础的维度也是 view 派生值。

**⇒ 落地三条：**
1. **遇到「查数据查不到原因」时，先做一次 label↔列 的差集核对**，而不是先怀疑「代码没记」；
2. **本仓的可观测性出口有 6 个**（§70 列了 5 个，本轮补第 6 个）：
   `request_logs`（业务列）/ Prometheus（高基数 label）/ `slog` /
   HTTP 响应头（`X-LLM-Gateway-RateLimit-Scope` 等）/ `gwtrace` 字段 /
   **view 派生列**（`request_status` 由 `status_code` 推导，**不是存储值**）；
3. **view 派生列要单独标出来** —— 查它等于查一个计算表达式，不是查数据。
   本仓 `request_status='rate_limited'` 就是这样来的（`request_logs_view_schema.go:405`），
   **我在 172 号就是靠这一条才确认那批 429 是网关自己发的**（正例）。

**⇒ 与既有条目的关系**：
§70 说「查错出口会让你以为字段不存在」；
**本条说「这不只是个别字段 —— 整类诊断维度都这样，所以要在开工前就问，而不是查不出来才回头换表」。**

**同族**：§46（尺子不对结论必错）/ §59（尺子错的两种形态）/ §70（查错出口）/
§16（否定结论分轮登记）/ §41（注释/代码都不是契约，架构取舍也不是）。
