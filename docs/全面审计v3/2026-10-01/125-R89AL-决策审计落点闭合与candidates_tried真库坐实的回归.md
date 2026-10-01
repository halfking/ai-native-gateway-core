# 125 号｜R89-AL：决策审计落点闭合 —— 落点存在，但「为什么」记不下来；并真库坐实 `candidates_tried` 回归

- 日期：2026-10-01
- 轮次：R89-AL
- 起因：124 号留下的最重要开放问题——**现役的决策审计（谁在什么时候、基于哪个策略版本、为什么做了这个路由/凭据选择）记录在哪里？**
- 结论先行：
  1. **落点存在且在用**：`routing_decision_log`（hot + 月分区，TTL 30 天，键 `lifecycle.routing_decision_log_ttl_days`），3 个生产写方，读端 `admin/analytics.go` 的决策回放与漏斗。**不是能力空白。**
  2. **但它不记「为什么」**：候选池、每候选 `reason`、被拦原因、策略版本，**在当前 build 里对所有成功请求均不可得**。数据结构存在（`TraceCandidate.Reason`），但只在「零尝试即耗尽」这一条窄路径上才到达 SQL。
  3. **并真库坐实一处回归**（本轮新发现，P1 级）：`candidates_tried` 在当前 build 对**每一条成功请求恒为 1**，而旧 build 携带真实规划候选数。配套的 admin 漏斗端点的「planned vs blocked」四段聚合因此结构性归零——**端点照常返回 200 和一组自信的 0**。

---

## 一、落点定位：25 张候选表里只有 3 张是现役决策审计

`information_schema` 列出约 25 张 routing/decision 族表。逐个核实写入方后：

| 表 | 现役写入方 | 性质 |
|---|---|---|
| **`routing_decision_log`（+`_hot`/`_default`/月分区/`_archive`）** | `admin/telemetry.go:313`、`admin/telemetry.go:712`、`domains/hooks/observability/telemetry/client.go:1054`（另 `admin/routing_resolve_probe.go:58` 为管理端探测） | **真正的决策审计落点** |
| `routing_audit_log` | `admin/users.go:97/106`、`admin/routing.go:123`、`admin/routing_overrides.go:504`、`admin/credential_monitor.go:1721/1826`、`domains/routeincident/action_infra.go:390`、`evidence.go:150` | **配置变更审计**（谁改了 routing 配置），不是「为什么选这个凭据」 |
| `routing_feedback_log` / `routing_optimization_metrics` / `routing_optimization_state` | `routingopt/dao.go:267/438`、`feedback_batch.go:31`、`bg/routing_metrics_aggregator.go:287` | 反馈/优化闭环，非决策留痕 |
| `candidate_failure_logs`（族） | `domains/streaming/executors/candidate_failure_logger.go:66` | 失败原因集合（凭据详情用） |
| `request_journey_observation_outbox` | `domains/requestjourney/observation_outbox.go:150` | 观测 outbox |
| `route_decisions` / `routing_decision_log`（旧名形态） | — | **表名与代码写法不一致，第一次 INSERT 检索 0 命中**（见 §四） |

> §23 补充：第一次用 `INSERT INTO route_decisions|routing_decisions|candidate_selection|dispatch_decisions|requestjourney|journey` 检索**全部 0 命中**。表在库里、名字却对不上——**0 命中的原因不是「没人写」，是「名字不同」**。改用库内真实表名直接搜才拿到上表。

### 决策回放端点确实在读它
`admin/analytics.go:734` `handleDecisionReplay`（L1 `request_logs` + L2 `routing_decision_log` 双腿）与 `:960-1010` 漏斗聚合，读的都是这张表。**不是死读端。**

---

## 二、核心发现：完整决策 trace 只在一条窄路径上落库

### 2.1 三处代码的实况

`domains/streaming/handler.go:5891-5901`（成功腿 `emitTelemetry`）的三级取值：

```go
if result.Trace != nil {                    // ← 恒假，见 2.2
    dl.DecisionTrace = traceJSON
} else if evt.DecisionTrace != nil {        // ← audit builder，见 2.3
    dl.DecisionTrace = traceJSON
} else if logCtx != nil && len(logCtx.AutoDecision) > 0 {
    dl.DecisionTrace = autoDecisionTrace(logCtx.AutoDecision)   // 7 字段裁剪投影
}
```

`autoDecisionTrace`（`auto_route.go:843-862`）只投影 `source/task_type/fallback_used/confidence/classifier/chosen_model/chosen_raw_model/chosen_credential_id` ——**不含候选池、不含分数、不含 `reason`**。而决策 wire 本身是有 `CandidatesTop3` 的（`auto_route.go:807-820` 还在为响应头做体积裁剪），**裁剪后的完整候选列表从未进 SQL**。

### 2.2 `ExecuteResult.Trace` 从未被赋值 —— 两个分支是死代码

`executors.ExecuteResult` 有 `Trace *Trace` 字段，但**全包非测试代码里唯一的 `Trace:` 字面量在 `executor.go:2577`，赋给 `ExecuteError` 而非 `ExecuteResult`**：

```
$ grep -rn "Trace:\s|\.Trace = " domains/streaming/executors/*.go | grep -v _test.go
domains/streaming/executors/executor.go:2577:  return nil, &ExecuteError{Tried: 0, Exhausted: true, Trace: trace}
```

三种检索形态（`Trace:` / `.Trace =` / 全仓 `Trace = |.Trace:`）互证一致。因此：

- `handler.go:5891` `result.Trace != nil` → **恒假**（成功腿）
- `handler.go:6671-6673` `if result.Trace != nil && len(...) > 0 { dl.CandidatesTried = len(...) }` → **恒假，且带副作用**（见 §三）

### 2.3 audit builder 也只有 1 个调用点

```
$ grep -rn "AuditBuilder.DecisionTrace\(trace\)"
domains/streaming/executors/executor.go:2575
```

位于 `Tried: 0, Exhausted: true` 分支（候选耗尽且**一次上游都没打**）。`ExecuteError` 的另外两个返回点（`:2676` 凭据指纹槽全饱和、`:2685`）**都不带 `Trace`**。

**⇒ 完整 trace（含 `PlannedCandidates` / `BlockedCandidates` / `FailureReason`）只在「路由给出零候选、零次尝试」这一种请求上落库。**

### 2.4 两个注释互相矛盾，其中一句是错的

- `routing_tracker.go:36`：*"the planned pool is independently persisted as routing_decision_log.decision_trace (Trace.PlannedCandidates)"* —— **只在 2.3 那条窄路径上成立**，不是一般事实。
- `auto_route.go:839-841`：*"the executor Trace is only built on routing failures, which left the column an empty object in production"* —— 这句 2026-09-14 O2 修复了 auto-route 成功腿，但**没有修成功腿的候选池**。

### 2.5 两个死字段：`Trace.Chosen` 与 `Trace.FallbackFromModel`

`Trace` 声明了 `Chosen *TraceCandidate` 与 `FallbackFromModel string`，**生产代码零赋值**（只有 `executor.go:1866-1869` 的声明，和 `executor_fallback_test.go:186` 手工构造后断言清零）。`executor_fallback_test.go:175` `TestFallbackLogic_FailureReasonCleared` **在合成对象上断言一个生产永不写入的字段** —— 测试绿，字段空。P3。

---

## 三、真库坐实的回归：`candidates_tried` 对成功请求恒为 1

### 3.1 观测

```sql
-- 决策 trace 形状
rows_nonnull_trace            932,546 / 932,546（100%，非 NULL）
rows_trace_ne_empty_obj        27,579（3.0%）
rows_with_planned_cands        27,229（2.9%）
rows_with_blocked_cands        11,972（1.3%）
rows_with_chosen_key                0
rows_with_fallback_from             0
```

按月 × 成功/失败拆开，**出现一个干净的断档**：

| 月份 | 总行 | 成功行 | 带 `planned_candidates` |
|---|---|---|---|
| 2026-09（旧 build） | 932,228 | 310,559 | 27,208 |
| 2026-10（当前 build，数据截至 10-01 02:57） | 318 | 109 | 21 |

**2026-09 有 9,736 条成功行带 `planned_candidates`；2026-10 成功行带候选池的是 0 条**（9,736 全部落在 9 月）。这与 §2.2/§2.3 的代码分析**互相独立地一致**。

### 3.2 `candidates_tried` 的同一个断档

```sql
-- 2026-10，成功请求
candidates_tried | count
                1 |   109      ← 全部是 1
```

对照 2026-09 同列的分布：`8(989) / 16(915) / 14(866) / 9(849) / 6(653) / 15(71) / 4(54) / 13(476) / 28(8) …`

**当前 build 把这一列压成了常量 1，量级正好等于 `handler.go:5869` 的硬编码 `CandidatesTried: 1`，而 `6672` 的真实覆盖是死代码。**

### 3.3 连带后果：admin 漏斗端点的四段聚合结构性归零

`admin/analytics.go:988-996` 的漏斗聚合完全建立在 `decision_trace->'planned_candidates'` 与 `->'blocked_candidates'` 上：

```sql
COUNT(*) FILTER (WHERE decision_trace <> '{}' AND jsonb_array_length(COALESCE(decision_trace->'planned_candidates','[]'))>0)  -- traceRows
COALESCE(SUM(jsonb_array_length(COALESCE(decision_trace->'planned_candidates','[]'))),0)                            -- totalPlanned
COALESCE(SUM(jsonb_array_length(COALESCE(decision_trace->'blocked_candidates','[]'))),0)                           -- totalBlocked
COALESCE(SUM(GREATEST(planned - blocked, 0)),0)                                                                  -- routable
```

当前 build 下**所有成功请求**对这四个量都贡献 0。端点**照常返回 200 和一组自洽的数字**，没有任何报错或告警——正是本会话反复命中的那一类失效形态：**输出干净、读起来完全正常、不会自己报警**。

---

## 四、方法论：这一轮我自己的代码推断被真库证伪了一次

**第一遍只读代码的结论**是：「完整 trace 只在零尝试耗尽时落库，成功请求恒无候选池」。听起来已经足够硬，可以直接写报告。

**真库一跑**：`rows_with_planned_cands = 27,229`，其中成功行 9,736 条 —— **与我的推断矛盾**。若就此发报告，就是一条**方向相反**的结论。

按 §20/§21/§22 的纪律，做了三件事才敢定性：

1. **不急着解释差异**，先假设「我漏了写方」，用第二、第三种检索形态（`Trace:` / `.Trace =` / 全仓 `Trace = |.Trace:`）复核写方全集；
2. 补查 `wrappedExec`（`handler.go:5103-5110`）来源，确认它也是 `*executors.ExecuteError`，不引入新的 Trace 产出方；
3. **加一个能区分两种解释的维度**：按月拆分。若是「我漏了写方」，新旧月份都该有；若是 build 更替，断档会出现在某个日期边界。**断档确实出现了，且 `candidates_tried` 在同一处同步断裂**——两个独立信号同点断裂，才敢定「回归」。

⇒ **新增 playbook 纪律候选 §24：「代码推断与真库观测矛盾时，不要先解释观测，先假设自己漏了写方；用一个能区分两种解释的额外维度（时间/版本/租户）去分辨，而不是找一个说得通的巧合」。** 本轮若跳过这一步，发出去的就是一条**方向相反且看起来同样严谨**的结论。

---

## 五、定性与登记

| 编号 | 定性 | 说明 |
|---|---|---|
| **待裁决 48** | **P1（新）** | `candidates_tried` 在当前 build 对每条成功请求恒为 1（旧 build 携带真实候选数），连带 `admin/analytics.go` 漏斗端点的 planned/blocked/routable 三段聚合对全部成功流量结构性归零。**回归**（数据变坏），不是「没接线」。 |
| **待裁决 49** | **P2（新）** | 决策审计不记「为什么」：`TraceCandidate.Reason` / `BlockedCandidates` / 策略版本在成功请求上不可得。修法方向：在 `ExecuteResult` 上补 `Trace` 赋值（一处），或让 `autoDecisionTrace` 带候选摘要；策略版本可 join 现成的 `candidate_binding_scope_revision.scope_version`（`bump_candidate_binding_scope_revision()` 已在维护，`admin/routing.go:1108/1186` 在写）——**版本概念已在库里，决策行只是不引用它**。 |
| **P3** | `Trace.Chosen` / `Trace.FallbackFromModel` 生产零赋值；`executor_fallback_test.go:175` 在合成对象上断言生产死字段。 |

**未改动任何生产代码**（待裁决项主代理不得擅自动手）。

## 六、本轮清掉的下轮必核

- ~~「决策审计落点在哪」~~ → 闭合：`routing_decision_log`，在用，3 写方 + 2 读端点。
- ~~「是否存在决策审计的别的落库路径」~~ → 闭合：`routing_audit_log` 是**配置变更**审计（who changed what），不是决策原因；`routingopt` 三张是反馈闭环；其余为失败/outbox。与 124 号 `sessionaudit` 不是同一条线，已分别定性。
