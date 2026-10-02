# 135 号｜R89-AW：收口 134 号的两处**待核实** —— `recommend_v2` 读裸父表确为**潜在**缺陷，**但本实例两处均不改变结果**

- 日期：2026-10-01
- 轮次：R89-AW
- 起因：134 号留下 `autoroute/recommend_v2.go:399/546` 两处待核实：
  「推荐引擎读裸 `request_logs`，是否**有意**用陈旧数据？」
  本轮查到底。**结论：结构上确为盲区，但本实例两处都不改变输出**——
  属 **latent P3**，**不是现役缺陷**。
- 一句话：**「读裸父表看不见最近 8 小时」是无条件成立的代码事实；
  但「因此推荐结果错了」不成立——本实例两处都未触发。**

---

## 一、两处代码（结构上确为盲区）

`autoroute/recommend_v2.go`，两处都 `FROM request_logs`（**裸父表，不含 hot**）：

### ① `:395` —— 本会话最近一次 auto 请求

```sql
SELECT COALESCE(NULLIF(task_type,''), NULLIF(task_type_chosen,''), 'chat') AS last_task,
       COALESCE(NULLIF(model_chosen,''), NULLIF(client_model,''), '')     AS last_model,
       success, COALESCE(latency_ms,0)
FROM request_logs
WHERE gw_session_id = $1 AND is_auto_request = TRUE
ORDER BY ts DESC LIMIT 1
```
结果喂给 `ComputeCorrectionScore(...)`（自适应纠正打分）。
**失败路径**：`if err != nil { return map[string]float64{}, nil }` ⇒ **静默返回空**。

### ② `:547` —— 48 小时内最常用 canonical 模型 top-3

```sql
SELECT canonical_id, count(*) AS usage_count
FROM request_logs
WHERE ts > NOW() - INTERVAL '48 hours' AND success = TRUE AND canonical_id IS NOT NULL
GROUP BY canonical_id ORDER BY usage_count DESC LIMIT 3
```
**失败路径**：`if err != nil { return []int{} }` ⇒ **静默返回空**。

⇒ **两处都是「静默空返回」，不是报错**（与 133 号 fail-open 同族）。

## 二、量化暴露面（**用含 hot 的视图量，避免自证**）

⚠️ **口径纪律**：下面的统计**必须用含 hot 的视图**（`request_logs_with_current_month`），
否则就是在用 bug 本身制造假象（本轮特意避开）。

### ① 站点一：**0 触发**

```sql
WITH auto AS (
  SELECT gw_session_id, max(ts) AS last_auto_ts
  FROM request_logs_with_current_month
  WHERE gw_session_id IS NOT NULL AND is_auto_request IS TRUE
  GROUP BY gw_session_id)
```

| 指标 | 值 |
|---|---|
| 有 auto 请求的会话数 | 19,736 |
| **最近一次 auto 在 8 小时内** | **0** |
| 最近一次 auto 在 48 小时内 | 108 |
| 最近一次 auto 早于 8 小时 | 19,736 |

⇒ **本实例根本没有近期 auto 流量**（48 小时内只有 108/19,736 个会话）。
⇒ **该处不触发。**

### ② 站点二：**触发但不改结果**

| 指标 | 值 |
|---|---|
| 48h 窗口内成功请求行 | 4,512 |
| 其中**被盲区隐藏**的最近 8h 行 | **290（6.4%）** |
| 48h 内 distinct canonical | 2 |
| **被隐藏的 8h 内 distinct canonical** | **0** |

⇒ **隐藏了 6.4% 的行，但新增的模型数是 0** ⇒ **top-3 的输出完全相同**。
⇒ **该处触发，但不改结果。**

## 三、诚实的定性

| 项 | 结论 |
|---|---|
| 「读裸父表 ⇒ 看不见最近 8 小时」 | ✅ **代码层面无条件成立**（134 号已证 `_hot` 非父表分区） |
| 「因此推荐结果现在是错的」 | ❌ **不成立**：① 不触发，② 触发但输出不变 |
| 定级 | **latent P3**（潜在，非现役） |
| 触发条件 | **某个 canonical 模型在最近 8 小时内首次成为高频** ⇒ 站点二的 top-3 **无法**把它选进来 |
| 失败形态 | 两处都是**静默空/静默陈旧**，不报错、不降级（与 133 号 fail-open 同族） |

⚠️ **实例事实 ≠ 生产结论**：本实例「没有近期 auto 流量」是**实例配置事实**，
**不能据此说生产也不会触发**——生产若 auto 流量正常，站点二就是**常态性偏差**
（新晋热门模型进不了 top-3），而站点一在**任何活跃会话**上都会读到陈旧轮次。

**「是否有意用陈旧数据」这个原问题，答案是：不可能是有意的**——
两处都没有注释声明「只看已归档数据」，而 134 号已证同仓**另外 18 处 `usage_ledger` 读点
全部使用含 hot 的视图**，说明「统一读视图」是本仓的既定约定。

## 四、修法方向（**待裁决，不擅自动手**）

两处均**一词可修**：把 `FROM request_logs` 换成 `FROM request_logs_with_current_month`
（该视图定义含 `FROM request_logs_hot`，134/133 号已实证）。

⚠️ **但先问一句本轮发现的更重要的事**：这两处的查询是**按 `gw_session_id` 取最近一条**
与**取 48h top-3**，**它们在 hot 与父表都读的前提下，语义本来就是「最近」/「最近 48 小时」**——
**盲区是纯粹的实现遗漏，不是设计取舍。**
⇒ **优先级：低**（latent，且本实例不触发），
但**成本也极低（一词两处）**，且**属 objective 明写项「检查 auto 模型的全量实现」**。

## 五、playbook §32（本轮新增）

**量化「盲区影响」时，必须量到「会不会改变输出」，而不是停在「有多少行被藏起来」。**

- 本轮若只报「48h 窗口内 6.4% 的行不可见」，读的人会以为推荐已经偏了；
- **加上「被隐藏行里的 distinct 模型数 = 0」这一维**，结论才变成
  「**触发但不改结果**」——**严重性完全不同**。

⇒ **对任何「数据窗口盲区」类缺陷，报告里必须有这一维**：
**被隐藏的部分，是否引入了原结论里没有的新实体（模型/租户/凭据/键）？**
- 引入新实体 ⇒ 结论可能变，按 P1/P2 报；
- 不引入 ⇒ 盲区是真实存在的，但当前不改结论，按 latent 报。

⚠️ **配套：统计必须用「正确的那一侧」**。
本轮刻意用**含 hot 的视图**而不是裸父表来统计——
**若用裸父表统计，就会用 bug 自己制造假象**（把「没写进去」当成「没发生」）。
**这是 §24 的一个具体变体：连测量工具本身都要先验证它没被待验证的缺陷污染。**

**同族**：§24（证据与前提不同期）、§28（终态缺失的歧义）、§31（先数再定性）、§33（133 号的 fail-open 族）。
**共同点：全都是「**先确认这个测量本身可信，再拿它下结论**」。**
