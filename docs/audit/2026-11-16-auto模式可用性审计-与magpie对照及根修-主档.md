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

### 1.1 第二轮（部署后 live 验收）追加的三个发现

F1-F3 修完并部署（`2c425d15`）后，第一轮 live 验收**仍然三协议面全 503**。
继续在 211 上取证，挖出下面三个更靠底层的形态：

| # | 现象 | 量级（211 真库 / 日志） | 根因 |
|---|---|---|---|
| **F4** | auto 选中的模型**在进 dispatch 之前**就 503 —— 换模梯子根本没机会跑 | 2026-11-17 00:24 live 验收：chat 选到 `qwen2.5-7b-instruct`，`failure_detail_code=no_candidates`、`chosen_credential_id=99023`（哨兵） | handler 在 `resolveCandidatesForRequest` 拿到**零候选**时直接 503；dispatch 换模在 executor 里面 ⇒ 请求死在 executor 之前 |
| **F5** | auto 的**决策池只有 48h 热门 top-3** ⇒ 退路也只有 2-3 个同类模型 | `autoroute/recommend_v2.go` 默认路径的 `candidatePool` 只装 `getHotTop3Canonicals`；而这三者在同一时刻**全枯竭**（`qwen2.5-7b-instruct` / `glm-4.7` / `glm-4.5-air` 实测全零候选） | 决策池宽度 = 3，与「此刻有 871 个模型可路由」的事实无关 |
| **F6** | 决策面与候选面**判据不一致** ⇒ auto 选中一个候选面**必然滤空**的模型 | `qwen2.5-7b-instruct` / credential 99023：状态闸**全部通过**（ready/ok/healthy/enabled/is_routable=true），但 `models_canonical.released_at = 2024-09-19` ⇒ 被候选面的 **model lifecycle 闸**（migration 966，2026-11-14 上线）丢弃 | 决策侧实时过滤（`filterCurrentlyAvailable`）**缺**候选面的三道闸：canonical disabled、model lifecycle、broken_confirmed 探针对 |

★ **F6 是这一轮最深的根因**：它不是竞态，是**同一行数据两个面对它给出相反答案**
（决策说能用、路由说不能用）。症状与「键错位」一模一样，但修法完全不同。
★ F5 与 F6 互补：**F6 是事前（不再选死模型），F5 是事后（真零候选时有够宽的退路）**。

### 1.2 第二轮部署后的 live 验收逼出 F7（热度榜恒空 ⇒ 决策池退化）

把 F4/F5/F6 部署到 211（版本 `2.6.0-13dd6532`）后，第二轮 live 验收**仍然 503**，
但失败形态变了（`no_candidates` → `gw_no_candidate`），且**重选一条日志都没有** ⇒
「重选没被触发」。继续取证挖到 **F7**：

| # | 现象 | 量级（211 真库） | 根因 |
|---|---|---|---|
| **F7** | **48 小时热门榜恒空** ⇒ 决策池退化成「按 canonical id 升序取第一个」；退路池建在同一张榜上 ⇒ **同样为空** ⇒ 零候选时无链可走 | `session_turns_hot` 近 48h **5338 行，`canonical_id` 5338 行全为 NULL** ⇒ 热度榜 SQL（`WHERE canonical_id IS NOT NULL GROUP BY canonical_id`）返回 **0 行** | ① `getHotTop3Canonicals` 返回空 ⇒ `recommend_v2.go` 的 `len(hotTop3) < 3` 回填分支接管 ⇒ 决策池 = **全部可用候选按 canonical id 升序** ⇒ auto 永远选 **canonical id 最小的那个**（kimi-k3，id 2067363）—— 这才是「auto 总是选那个冷门模型、然后它不可用」的真正机制；② 退路池若也建在这张榜上 ⇒ 空链 ⇒ 重选静默返回 |

★ **本轮自己踩的坑**：F5 的第一版退路池取自「同一张 48h 热度榜 LIMIT 32」，
在本地单测里完全正常（可注入），到生产才发现**那张榜是空的** ⇒
**「用同一张榜做更宽的版本」是个陷阱：它把榜的退化原样继承了下来**。
⇒ 现在退路池改为 **索引快照 ∩ 实时可用过滤**（内容来自「此刻可路由」这个事实本身），
并加 30 秒进程内缓存（不为每个 auto 请求多打一次库）。

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

### 4.4 F4：零候选发生在 dispatch 之前，换模梯子根本没机会跑

修完 F1-F3 并部署（`2c425d15`）后，第一轮 live 验收（2026-11-17 00:24）**三协议面仍全 503**：

| 协议面 | auto 解析结果 | HTTP | 失败码 |
|---|---|---|---|
| chat | `qwen2.5-7b-instruct` | 503 | `no_candidates`（`chosen_credential_id=99023` 哨兵） |
| messages | `glm-4.7` | 503 | — |
| responses | `glm-4.5-air` | 503 | — |

源码位置：`domains/streaming/handler.go:4148` —— `len(candidates) == 0` 的分支**当场 503**，
而 dispatch 的换模梯子（`domains/dispatch/dispatcher.go:187-279`）在 **executor 里面**：
请求死在 executor 之前 ⇒ 梯子永远走不到。

⇒ 这是「修了换模闸门却仍然断掉」的原因：**闸门只是让梯子可用，梯子前面还有一道墙**。

### 4.5 F5：决策池只有 48h 热门 top-3 ⇒ 退路也只有 3 个模型

`autoroute/recommend_v2.go` 默认路径（非 `FullCandidateSet`）里：

```
candidatePool = hotTop3 的候选（getHotTop3Canonicals：48h 成功流量 LIMIT 3）
```

⇒ auto 的决策池 = **3 个模型**。选中模型此刻不可路由时，能退的地方也只有这 2-3 个同类模型。
211 实测这三个（`qwen2.5-7b-instruct` / `glm-4.7` / `glm-4.5-air`）**同一时刻全枯竭**，
而同一时刻平台上 `v_routable_credential_models.is_routable = TRUE` 的模型有 **871** 个。

### 4.6 F6：决策面与候选面判据不一致 ⇒ 选中了「候选面必然滤空」的模型

211 真库逐条对拍（credential 99023 / `qwen2.5-7b-instruct`）：

| 闸 | 决策侧 `filterCurrentlyAvailable` | 候选面 `candidateQuerySQL` | 该模型实测 |
|---|---|---|---|
| credential status / lifecycle / manual_disabled | ✅ 有 | ✅ 有 | active / active / false ✅ |
| credential availability_state / quota_state / health_status | ✅ 有 | ✅ 有 | ready / ok / healthy ✅ |
| provider enabled / manual_disabled | ✅ 有 | ✅ 有 | true / false ✅ |
| binding / provider_model available + manual% 原因 | ✅ 有 | ✅ 有 | true ✅ |
| node_probe 退避 | ✅ 有 | — | — |
| **models_canonical.status = 'disabled'** | ❌ **缺** | ✅ 有 | active ✅ |
| **model lifecycle（released_at + stale_threshold）** | ❌ **缺** | ✅ 有（migration 966） | **released_at = 2024-09-19 ⇒ 被丢弃** |
| **broken_confirmed 探针对** | ❌ **缺** | ✅ 有（`brokenPairExcludeSQL`） | — |

⇒ **决策说「能用」，候选面说「不能」** ⇒ auto 把它排到第 1 并选中 ⇒ 必然零候选 ⇒ 503。

★ 这不是竞态，是**判据分歧**：同一行数据，两个面对它给出相反答案。
★ 通用式：**候选面与决策面必须同闸**。同键还不够——键一样、闸不一样，
症状与键错位一模一样，但修法完全不同。
★ 量化（211 真库同款 SQL）：决策池 **1017** 个模型中，**194 个（19%）**「决策面活、候选面死」；
另有 48 个「此刻有货但不在决策池」。⇒ 命中死模型是**高频事件，不是偶发**。

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

### 5.3 第二轮（F4/F5/F6）改动

| 文件 | 改动 | 为什么 |
|---|---|---|
| `domains/streaming/auto_route_reselect.go`（新） | **零候选重选器**：沿退路链逐个试，改写请求体后按新体重新解析候选，第一个有节点的即采用 | F4：换模梯子前面那道墙 |
| `domains/streaming/handler.go` | `len(candidates)==0` 分支前插入 auto 重选块（命中则改写 model/body/candidates/policy/modality 并继续） | F4 接线点 |
| `autoroute/availability_filter_sql.go`（新） | 决策面实时过滤**补齐候选侧三道闸**：canonical disabled / model lifecycle / broken_confirmed 探针对 | F6（治本：不再选死模型） |
| `autoroute/recommend_v2.go` | 过滤 SQL 抽出为独立常量 + 占位符渲染探针闸 | F6 |
| `autoroute/reselect.go`（新） | **宽退路池**：索引快照 ∩ **实时可用过滤** → canonical 名列表（**不依赖任何统计表**；30s 进程内缓存） | F5 + F7（第一版建在空榜上 ⇒ 空链） |
| `autoroute/decision.go` / `decision_v2.go` | `Decision.ReselectCandidates` 新字段（V1/V2 同语义） | F5 载体 |
| `autoroute/index.go` | 退路池的进程内缓存字段 | 避免每次 auto 请求多打一次库 |
| `domains/streaming/auto_route.go` | wire 增加进程内字段 `reselectModels`（**不进响应头 / 不进审计 JSON**） | F5 传输 |
| `domains/streaming/request_log_pipeline.go` | `RequestLogContext.AutoReselectModels` + `SetAutoDecision` 填充 | F5 传输 |
| `domains/streaming/auto_route_reselect.go` | 重选链**优先宽池**、回退窄链；**两条链分开消费** | F5 |

### 5.4 第二轮的三条作用域声明

1. **零候选重选只对 auto 请求生效**（`logCtx.IsAutoRequest`）：显式点名模型的请求，
   零候选的语义就是「这个模型确实不可用」，应如实 503 让客户端自己换；
2. **宽退路池不进审计视图**：`Decision.ReselectCandidates` 与 wire 的 `reselectModels`
   都是进程内字段 ⇒ `auto_decision` JSONB 与响应头**字节级不变**；
3. **决策侧补闸是 fail-safe 方向的收紧**：只会让池子里的模型**更少**（都是本来就路由不到的）；
   过滤查询出错时走既有的 `fallbackSnapshotAvailability`（放行），不会把 auto 打成空池。

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

### 6.3 第二轮（F4/F5/F6）钉测

| 测试 | 守什么 | 变异（改坏）后 |
|---|---|---|
| `autoroute/availability_filter_sql_test.go::TestAvailabilityFilterSQL_HasCandidateSideGates` | 决策面过滤 SQL **带齐**候选侧三道闸（canonical disabled / lifecycle / broken_confirmed） | 摘掉任一道闸 ⇒ **红** |
| `autoroute/availability_filter_sql_test.go::TestAvailabilityFilterSQL_ProbeTableIsProbemodeGuarded` | 探针闸读的是 `probemode.GuardStateTable()`（legacy/新栈同源，不与候选面分叉） | 换成别的表 ⇒ **红** |
| `autoroute/availability_filter_sql_test.go::TestAvailabilityFilterSQL_PlaceholderRendered` | 占位符真的被渲染（否则闸会静默消失） | 占位符残留 ⇒ **红** |
| `autoroute/reselect_test.go::TestReselectCandidateNames_SkipsChosenAndDuplicates` | 退路池去重 + 不含已选模型 | — |
| `autoroute/reselect_test.go::TestReselectCandidateNames_EmptyWhenNoLadder` | 无榜 ⇒ 空池（行为退化为旧窄链，不丢退路） | — |
| `autoroute/reselect_test.go::TestReselectCandidateNames_HonoursLimit` | 退路池宽度有上限（不会变成无界串行扫描） | 宽度退回 3 ⇒ **红** |
| `autoroute/reselect_test.go::TestDecideV2_FillsReselectCandidates` | V2 决策**真的带上**宽退路池；审计视图 `CandidatesTopN` 不受影响 | 决策不填 ⇒ **红** |
| `autoroute/reselect_test.go::TestDecideV1_FillsReselectCandidates` | V1 同语义（字段契约不分叉） | — |
| `domains/streaming/auto_route_reselect_test.go::TestAutoRequestReselect_PrefersWideReselectLadder` | 宽池**优先**于窄链 | 宽池不参与 ⇒ **红** |
| `domains/streaming/auto_route_reselect_test.go::TestAutoRequestReselect_WideLadderExhaustedFallsBackToNarrow` | 宽池全灭 ⇒ 仍走窄链 | 宽池不参与 ⇒ **红** |
| `domains/streaming/auto_route_reselect_test.go::TestAutoRequestReselect_WideLadderDoesNotConsumeDispatchLadder` | 宽池命中时**不掏空** dispatch 换模梯子 | — |
| `domains/streaming/auto_route_reselect_test.go::TestAutoRequestReselect_NotAutoRequest` | 非 auto 请求**不被**改路由 | 去掉 `IsAutoRequest` 边界 ⇒ **红** |
| `domains/streaming/auto_route_reselect_test.go::TestApplyAutoReselect_RewritesRequestContext` | 接线**整体**改写（model/body/candidates/policy/modality 一起改） | 半写 ⇒ **红** |
| `domains/streaming/auto_route_reselect_wiring_test.go::TestHandlerWiresAutoReselect` | handler 真的调用了 `applyAutoReselect`（接线形状门） | 去掉调用 ⇒ **红** |

### 6.4 变异汇总（第二轮）

`/tmp/auto-mutation-round2.sh`：**9/9 检出**（M1 lifecycle 闸摘掉｜M2 broken_confirmed 闸改名｜
M3 canonical disabled 闸摘掉｜M4 宽池不参与重选｜M5 退路池宽度退回 3｜M6 决策不再填退路池｜
M7 handler 不再应用重选结果｜M8 非 auto 请求也被重选｜M9 退路池不查实时可用）。

★ **M5 两次漏检，两次都是「守卫没覆盖到」，而不是「守卫失效」**——这正是本轮最值钱的两条负控：

1. **第一次**：`TestReselectCandidateNames_HonoursLimit` 传的是**显式 limit 参数**，
   所以它看不见「生产调用点实际使用的那个常量」被改窄 ⇒ 补 `TestReselectPoolLimitIsWide`
   （直接钉住常量 ≥ 16）。
2. **第二次**：脚本的 `-run` 正则是 `TestReselectCandidateNames`，
   **根本没匹配到**新加的 `TestReselectPoolLimitIsWide` ⇒ 那条测试**一次都没跑**，
   而 `go test` 对「无测试可跑」**照样输出 ok** ⇒ 脚本误判成「守卫失效」。
   ⇒ 修法：脚本改用 `-v` 并**显式断言 `--- PASS/FAIL:` 出现过**，否则报
   `NO TEST RAN`（与「守卫失效」分开记）。

★ 通用式：**「测试全绿」不等于「改动被守住了」**；而**「守卫没生效」与「守卫压根没跑」
是两种不同的失败**——后者尤其阴险，因为它同时伪装成「代码有 bug」。
⇒ 负控脚本自己要能被审计：锚点未命中、`-run` 未匹配、测试未执行，三种情况必须分开报。

★ M9 是本轮新增的变异，专门钉「退路池必须查实时可用」——它是 F7 那版
「退路池建在恒空的热度榜上」的守卫。

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
