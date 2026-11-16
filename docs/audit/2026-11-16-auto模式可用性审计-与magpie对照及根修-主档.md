# 2026-11-16 · auto 模式可用性审计 —— 对照 magpie 找差距，211 真库取证，三处根修

> goal 会话。触发：owner 要求「拉取最新代码，比较我们的 auto 模式与 magpie 中的 auto 模式，
> 并且让我们的 auto 模式完善优化，形成真正可用的模式。现在经常中间会断掉，表现为原来的模型不可用。
> 可以到 211 搜索 auto 相关的 log，进行分析验证」。
>
> 目标机：211（`llm.itestu.cn`，蓝绿槽位，DB `acc-pg17` / `llm_gateway`）。
> 对照物：magpie 源码（`/Users/niuer/workspace/ai/magpie`，commit `f0f42c44`，2026-11-11）。
> 本轮：**零迁移、零路由变更**；改动只在「auto 请求失败后怎么办」与「auto 在哪些协议面可用」两处。

---

## 一、结论先行

我们的 auto 模式**不是"选得不准"，而是"选完之后没有任何退路"**。

| # | 现象 | 量级（211 真库） | 根因 |
|---|---|---|---|
| F1 | auto 请求的上游失败**直接把错误抛给客户端**，从不换模型 | 30 天 **87 笔 auto 请求 / 13 笔失败**，`auto_route_selections.fallback_used` **87 笔全为 false** —— 一次都没换过模 | dispatch 流水线的换模梯子被两个开关挡死，其中 `dispatch_v2.allow_model_change` 默认 **false** |
| F2 | auto 模式**只存在于 chat completions 一个协议面** | 同一个 `model="auto"` 请求换到 Anthropic / Responses 协议 ⇒ `auto` 被当成真实模型名 ⇒ 零候选 ⇒ 客户端看到「模型 auto 不可用」 | `AUTO_ON_MESSAGES` / `AUTO_ON_RESPONSES` 默认 **false** |
| F3 | 会话意图缓存**不查目标是否还可路由** | 会话 `gw_455af85c-…` 连续 **55 轮**全落在 `glm-5.2`，最后一轮 latency **203ms** 直接失败 | 缓存命中只有分类一致性判据（role/kind/workType/drift），**没有可用性判据** |

★ **F1 是主因**。F3 在当前生产配置下**不是主因**（V2 决策路径默认不读会话缓存 ⇒ 每轮重判），
但它是「一旦开启会话粘性就会复发」的同型缺陷，一并修掉。

---

## 二、magpie 的 auto 模式到底强在哪（源码取证）

### 2.1 先纠正一个前提

magpie 的 `internal/gateway/automode.go` 里的 "auto mode" 是 **Claude Code 的安全分类器**
（`dangerous-tool-use-2026-09-03` beta / `safeguards` 字段），**与模型路由无关**。

magpie 真正的 auto 分两层：

| 层 | 客户端发什么 | 语义 | 换不换模型 |
|---|---|---|---|
| A. auto 组 | `auto/<model>`（`internal/provider/autoref.go:34-36`） | **同一个模型**在多个 provider 之间自动选供应商 | 不换模型 |
| B. Smart 模板组 | `group/<id>`（`internal/provider/grouptemplate.go:23-47`） | 按任务难度（hard/easy）选**不同模型** | 换模型 |

我们仓的 `model="auto"` 语义上更接近 **B 层**。**本轮不引入 `auto/<model>` 语义**（见 §六 R1）。

### 2.2 magpie 真正让 auto「不断」的四件事

| # | 机制 | magpie 出处 | 我们仓现状 |
|---|---|---|---|
| 1 | **同请求内失败即换下一个候选，客户端不感知** | `internal/gateway/gateway.go:4968-4984`（fail → rest → `continue`） | **换模梯子存在但被开关挡死**（F1） |
| 2 | **粘性目标正在休息 ⇒ 不用它，会话让给下一个** | `internal/gateway/affinity.go:599-600`（`a.Why = "resting"`） | 会话缓存命中**不查可用性**（F3） |
| 3 | **rest 时长按失败原因分档 + 递增强度** | `internal/gateway/routing.go:236-296`（credit 阶梯 30m→2h→8h→24h→3d→7d；quota 15m；rate ≤30m；restricted 2h；重复失败退避 1m→2m→…→10m） | **已有等价物**：`errorsx/restclasses.go`（20 类逐条映射）+ `errorsx/penalty.go`（none/binding/credential/provider 四档）+ `domains/credential/writer.go`（绑定级冷却 `unavailable_recover_at`）+ `domains/credential/ratebackoff.go`（升级梯） |
| 4 | **失败分类先判"谁该受罚"再动熔断** | `internal/gateway/routing.go:682-704` `failureOf` | **已有等价物**：`errorsx.PenaltyOf`（请求侧/形状类错误不罚节点） |

★ **结论**：我们的 rest / 归责 / 冷却体系**不弱于 magpie**（甚至更强：本仓用 PG 持久化，magpie 用内存 + `rests.json`）。
**唯独缺的是第 1 条和第 2 条**——也就是"失败之后怎么办"。

---

## 三、211 真库取证（auto 相关 log）

### 3.1 auto 请求的失败分布（30 天）

```sql
SELECT is_auto_request, success, failure_detail_code, error_kind, count(*)
FROM request_logs_with_current_month
WHERE ts > now() - interval '30 days' AND is_auto_request
GROUP BY 1,2,3,4;
```

```
 is_auto_request | success | failure_detail_code | error_kind      | n
-----------------+---------+--------------------+------------------+----
 t               | t       |                    |                  | 74
 t               | f       | transient          | transient        |  9   ← 9 笔
 t               | f       | provider_error     | provider_error   |  3
 t               | f       | request_rejected   | request_rejected |  1
```

**13 笔失败全部在 `upstream` 阶段**，没有一笔是客户端侧问题。

### 3.2 失败高度集中在一条坏凭据上

```sql
SELECT credential_id, success, count(*) FROM request_logs_with_current_month
WHERE credential_id IN (62,3,25,49) AND ts > now() - interval '30 days' GROUP BY 1,2;
```

```
 credential_id | success | n
---------------+---------+------
            62 | f      | 1529      ← zipu-max-account2
            62 | t      | 34147
```

13 笔 auto 失败里 **7 笔**落在 credential 62（`zipu-max-account2`）。
该凭据事后被置 `availability_state='suspended'` / `quota_state='periodic_exhausted'`，
但**它的 20 条绑定至今全是 `available = true`** ⇒ 绑定级 rest 没吃到这次故障，
于是 auto 一次次挑中它。

### 3.3 会话级证据：同一会话连续 55 轮落在同一个模型

```sql
SELECT ts, request_id, task_type, chosen_model, success, latency_ms, fallback_used
FROM auto_route_selections WHERE session_id = 'gw_455af85c-…' ORDER BY ts;
```

- **55 轮全部 `chosen_model = glm-5.2`**（45 分钟内）；
- 最后一轮 `success = f`，`latency_ms = 203`（瞬间被拒，不是超时）；
- **`fallback_used` 70 行全为 `f`** ⇒ **auto 模式从来没换过模型**。

★ 这就是用户说的「**中间会断掉，表现为原来的模型不可用**」的精确形态：
模型一坏，**同一会话的每一轮都继续撞同一堵墙**，直到 TTL 到期。

---

## 四、根因（源码级）

### 4.1 F1：换模梯子存在，但被两个开关挡死

**真正的活路**是 dispatch 流水线里的换模梯子：

```
domains/streaming/executors/executor_dispatch.go:462   qr.AllowModelChange = params.DispatchAllowModelChange
domains/dispatch/dispatcher.go:201                     PlanNoRoute(qr, p.modelChangeEnabled())
domains/dispatch/planner.go:313-318                    需要 modelChangeEnabled && qr.AllowModelChange
domains/dispatch/planner.go:323-330                    PlanModelChange：候选里挑第一个没试过的（**多跳**）
domains/dispatch/dispatcher.go:260-266                 通知客户端「模型 X 无可用节点，切换到 Y 继续执行…」
```

两个开关：

| 开关 | 来源 | 原值 | 作用域 |
|---|---|---|---|
| `qr.AllowModelChange` | `params.DispatchAllowModelChange` ← `logCtx.IsAutoRequest && dispatchAllowModelChangeEnabled()` | 仅 auto 请求 | ✔ 本来就只对 auto 生效 |
| `p.modelChangeEnabled()` | `dispatch.IsModelChangeEnabled()`（`domains/dispatch/gate.go`） | **默认 false** | 全局 |

⇒ **只要 auto 请求拿到了换模许可，梯子就活着**；而许可的另一半被全局开关按在关闭位。

### 4.2 附带发现：handler 层的 D5 旧路是**死代码**

`domains/streaming/handler.go:5546` 的 D5 块注释写着「feature-flag OFF by default」，
但它的进入条件里同时要求 `!dispatchModelAlternativesConsumed` 与 `dispatchAllowModelChangeEnabled()`，
而 `dispatchModelAlternativesConsumed` 在上面被初始化为 `dispatchAllowModelChange`
（= `IsAutoRequest && dispatchAllowModelChangeEnabled()`）—— **两个条件互斥 ⇒ 该块从未执行过**。

⇒ **D5 从来不是 auto 模式的退路**；它是注释在骗人。本轮已订正注释并指明活路。

### 4.3 F3：会话缓存不查可用性

`autoroute/decision.go`（V1）与 `autoroute/decision_v2.go`（V2）的缓存命中分支只有分类一致性判据。
magpie 的对应规则是 `affinity.go:599-600`：**粘性目标正在休息 ⇒ 不用它**。

★ **另一处关键细节**：V2 早已有 `ValidateCachedChoice`，但它**只看绑定级/模型级**的 `available`，
**不看凭据级**的 `availability_state` / `quota_state` / `health_status`。
211 上 credential 62 正是「20 条绑定全 `available=true`、凭据本身已 `suspended`」的形态
⇒ 旧校验会误判「还可用」。本轮把判据**改成与 autoroute 索引刷新 SQL 逐条对齐**。

---

## 五、改动（本轮）

| 文件 | 改动 | 为什么 |
|---|---|---|
| `domains/dispatch/gate.go` | `modelChangeEnabled` 默认 **true** | 换模梯子对 auto 请求生效（F1） |
| `settings/spec_dispatch.go` | `dispatch_v2.allow_model_change` 默认 **true**，描述改写 | 同上；运维仍可热加载关闭（杀闸） |
| `autoroute/decision.go` | V1 缓存命中增加**可用性重校验**：目标不可路由 ⇒ 作废缓存 + 重新决策 | F3 |
| `autoroute/cache_revalidate.go`（新） | 重校验helper + `Decider.CachedChoiceValidator` 缝 | F3，且让该分支可测 |
| `autoroute/recommend_v2.go` | `ValidateCachedChoice` 的判据**改成与索引刷新 SQL 逐条对齐**（含凭据级状态、provider 禁用、探针退避） | 旧判据会漏掉 credential 62 那种形态 |
| `autoroute/internal/legacyflags/legacyflags.go` | `AUTO_ON_MESSAGES` / `AUTO_ON_RESPONSES` 默认 **true** | F2 |
| `autoroute/feature_flags.go` | 同步默认值与注释 | 同上 |
| `domains/streaming/handler.go` | 订正 D5 块注释（死代码 + 指向活路） | 事实订正 |

### 5.1 三条安全边界（重校验）

1. **无 pool ⇒ 放行**（测试/降级环境不因缺 DB 把会话打散）；
2. **重校验自身出错 ⇒ 放行 + 留痕**（一次 DB 抖动不该变成全站 auto 失败；
   "数据库说不知道" ≠ "模型死了"）；
3. **明确不可路由 ⇒ 不放行**（唯一会作废缓存的分支）。

### 5.2 作用域声明：非 auto 请求字节级不变

`dispatch.IsModelChangeEnabled()` 的**每一处**消费点都额外要求
`DispatchAllowModelChange` / `qr.AllowModelChange`，而这两个字段的唯一来源是
`logCtx.IsAutoRequest`：

- `domains/streaming/handler.go:4915`（chat 面内联实现）
- `domains/streaming/responses.go:1184` → `requestDispatchOptions`（messages/responses 共用）
- `domains/streaming/executors/executor_dispatch.go:462`

⇒ **把全局默认值翻成 true，只改变 auto 请求的行为**。

---

## 六、验证

### 6.1 钉测

| 测试 | 守什么 | 变异（改坏）后 |
|---|---|---|
| `domains/streaming/executors/auto_model_failover_test.go::TestExecuteDispatchAutoRequestSwitchesModelWhenAllCredentialsFail` | auto 请求的模型 A 全部凭据失败 ⇒ **换模型 B 并把请求做完** | 退回 `modelChangeEnabled.Store(false)` ⇒ **红**（"model-level failover must be enabled by default"）；把 `qr.AllowModelChange` 钉成 false ⇒ **红**（"all 6 candidates failed: [upstream_down] upstream 502" —— **与用户报障同形**） |
| `autoroute/cache_revalidate_test.go::TestDecide_CachedChoiceDead_ReDecides` | 缓存里的死目标 ⇒ 不复用 + 重新决策 | 去掉重校验分支 ⇒ **红**（"dead cached choice must not be reused"） |
| `autoroute/cache_revalidate_test.go::TestDecide_CachedChoiceAlive_Reuses` | 活目标 ⇒ 继续复用（会话一致性不牺牲） | — |
| `autoroute/cache_revalidate_test.go::TestDecide_CachedChoiceRevalidationUnavailable_FailsOpen` | 重校验不可用 ⇒ 放行 | — |
| `domains/dispatch/gate_test.go::TestModelChangeEnabled_DefaultOn` | 闸门默认开 | — |
| `autoroute/internal/legacyflags/legacyflags_test.go::TestLoadProtocolAutoOptOut` | 协议面 auto 仍可单独关 | — |

### 6.2 命令

```
go build ./...                                  # 绿
go vet ./autoroute/... ./domains/dispatch/... ./domains/streaming/... ./settings/... ./admin/...   # 绿
go test ./autoroute/... ./domains/dispatch/... ./settings/...        # 全绿
go test ./domains/streaming/... ./cmd/gateway/...                    # 全绿
gofmt：经手文件零漂移
```

---

## 七、风险与不照抄 magpie 的地方

| # | 风险 | 处置 |
|---|---|---|
| R1 | magpie 的 `auto/<model>` 与我们的 `model="auto"` **语义不同**（前者同模型跨供应商，后者换模型） | **不引入**。直接对齐会破坏现有客户端契约；「同模型跨供应商」若要做，另起 id 并保留 `model="auto"` 现状 |
| R2 | magpie 的 rest 是内存 + JSON 文件，跨实例不一致 | 保留本仓 PG 持久化（更强） |
| R3 | magpie 的 rest 默认账号级，会连带摘掉同账号其它模型 | 本仓的绑定级口径是**对的**（本仓限流确实按模型下发），不为了"对齐"改回去 |
| R4 | magpie 的 patience 轮次最长 240s/组、总 540s | 本轮**未引入** patience 轮次（超预算风险）；换模梯子受既有 attempt budget 约束 |
| R5 | 换模默认开启意味着 auto 请求会**多付一次失败尝试**的代价 | 可用平台设置热加载关闭；但代价远小于"请求直接失败" |
| R6 | `fallback_used` 列语义 ≠ "发生过错模" | 该列记的是"决策来自 48h 热门兜底"，**不是**换模。验换模请看 dispatch 的 `metricFailover{kind="model_change"}` 与请求日志里的换模通知 |

## 八、遗留登记

1. **换模通知进请求日志的字段位**：`routing_decision_log.resolution_path` / `decision_trace`
   是否完整承载"本次换到了哪个模型"，本轮未逐字段核验；当前可观测面是
   Prometheus `metricFailover{kind="model_change"}` + 客户端收到的换模通知。
2. **`errorsx/restclasses.go` 只映射 magpie 旧副本的 20 类**，新版 magpie 新增
   `sight` / `offline` / `cut` 三类未覆盖（词表版本差，不影响本轮结论）。
3. **credential 62 形态暴露的老问题**：绑定级 rest 没吃到"凭据已 suspended 但绑定仍 available"的故障
   （本轮靠换模兜住，不靠 rest 摘除）。绑定与凭据两级状态的同步是独立议题。
