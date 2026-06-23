# llm-gateway-go 4 主题审计报告 (v2)

**老板，我先用 1 页纸把核心结论说完。**

## TL;DR

| 主题 | 老板原话 | 我的理解 | 1 个最重要的事 |
|------|----------|----------|----------------|
| **T1** | "并发与槽位、自动回收自动注册、一个客户多会话算一个指纹" | slot 分配 + pin 复用 + reclaim goroutine | **reclaim 100% 未启用** + tenant 隔离缺失 |
| **T2** | "Anthropic↔OpenAI tools 转换、claude-opus-4-8 有问题但 sonnet-4 正常" | 流式 + 非流式 tool_use + thinking 混合 | **`signature` 字段被丢弃** → opus-4 多轮 400 |
| **T3** | "凭据+模型状态、自动回测、状态变化即时影响路由" | 3 层状态机 + 6 种时间窗口 | **circuit.Manager 不跨节点** + recent_success_rate 阈值 0.3 是 TEMPORARY |
| **T4** | "决策记录 + 请求/错误管理 + 滑动窗口可否服务" | audit.Trace + 19 Kind + W1-W7 窗口 | **per-attempt latency 缺失** + health_tracker request_id 不一致 |

**老板请先确认：**
1. T2 老板说的"opus-4-8 响应有问题"，**具体是什么症状**？tool_use 缺失 / signature 报错 / thinking 卡住 / max_tokens 截断 / 其他？我下面给的是**全部**可能性的清单。
2. T3"不同时间回测"——transient 30s、rate_limit 15min、auth 永久、quota_periodic 7d、broken_confirmed 1h……**这个粒度够吗？还是老板要更细？**
3. T1"一个客户多会话算一个指纹"——我看到 sticky key 已经做对了（排除 session_id），**但缺测试证明**。老板要的是"代码已经对"还是"我必须补一个 e2e 测试证明"？

---

## 一、T1：并发与槽位管理

### 1.1 当前代码事实

**3 个关键文件**（services/llm-gateway-go/credentialfpslot/, routing/sticky.go）：

```
slot.go       (833 行)  Manager + 3 Lua scripts + memory fallback
reclaim.go    (244 行)  后台回收 goroutine ← **dead code**
sticky.go     (180 行)  StickyCache {tenant}:{app}:{key}:{profile}:{model}
```

**Sticky Key 算 client 指纹的实例**：
```
client X, app A, key K, profile "dev", model "gpt-4"
  + session A1 → key = "X:A:K:dev:gpt-4" → slot #2
  + session A2 → key = "X:A:K:dev:gpt-4" → slot #2  ← 复用！✅
  + model "gpt-4o" → key = "X:A:K:dev:gpt-4o" → slot #3  ← 不同模型新 slot
client Y, app A, key K, profile "dev", model "gpt-4"
  + session Y1 → key = "Y:A:K:dev:gpt-4" → slot #0  ← 不同 client 新 slot
```

**✅ 这部分代码是对的**，缺一个 `TestExecutor_SameClientAcrossSessions_OneSlot` 测试。

### 1.2 改进方向（按"代码层 bug"分类）

| 优先级 | 改动 | 风险 | 回滚 |
|--------|------|------|------|
| **P0** | 启用 reclaim goroutine（main.go 加 1 行） | 极低（可 stop） | `reclaimLoopStop()` |
| **P0** | 修文档注释错位（slot=30min vs pin=24h，注释写反） | 零（注释而已） | git revert |
| **P0** | hasPin memory 模式加过期检查（slot.go:497-500） | 低（多删 stale pin） | git revert |
| **P1** | tenantID 注入 Redis key + executor.go:703 用真 tenant | 中（DB key 格式变化需 migration） | 保留 dual-read 7 天 |
| **P2** | executor.go:631-633 fallback 用 identity hash（非 X-Request-Id） | 低 | git revert |
| **P3** | 合并 acquireRedis 2 次 round-trip | 低（仅性能） | git revert |

**老板请注意**：Redis pub/sub 跨节点同步、IdentityPool 接入 → **暂不做**（探索报告建议的 P1/P3，成本高且非当前痛点）。

---

## 二、T2：Anthropic↔OpenAI tools 转换

### 2.1 老板说的"opus-4-8 有问题"我推测的可能清单

老板没明说症状，**我列全部可能**：

| 症状 | 概率 | 当前代码状态 |
|------|------|---------------|
| 流式 tool_use `input` 字段缺失或解析失败 | 高 | 流式已正确分流 `tool_use` / `input_json_delta` |
| thinking + tool_use 混合序列混乱 | 高 | **fixture 缺失**（12 个流式测试全纯 tool_use） |
| `signature` 字段丢弃 → 多轮 400 | **确定丢** | **IR 层不存 signature，0 修复** |
| max_tokens 截断 input_json → 客户端解析失败 | 中 | flush 时无校验，原样 emit |
| content_block.index 跨多 block 错位 | 中 | relay 用本地 counter 绕开（无 bug 但代码异味） |
| 未知 delta type 误判为 text | 低 | default 分支 Text 优先 |

**最大嫌疑（按概率）**：`thinking + tool_use 混合` fixture 缺失 + `signature` 字段丢弃。

### 2.2 改进方向

| 优先级 | 改动 | 老板请确认 |
|--------|------|-----------|
| **P0** | IR `ThinkingBlock` 加 `Signature` 字段 + 流式 `signature_delta` 分支 | 必要（多轮 round-trip 必然 400） |
| **P0** | 流式 `content_block_start` 处理 `thinking` 类型 | 必要 |
| **P1** | 新增 `anthropic_to_openai_stream_thinking_tool_test.go`（opus-4-8 fixture） | **请告诉我具体症状，我才能写准确的 fixture** |
| **P1** | max_tokens 截断 fixture + emit warning | 请确认症状 |
| **P2** | 去重 SSE data 3 次 unmarshal | 重构 |
| **P3** | IR Index 语义化文档 | 文档 |

---

## 三、T3：凭据+模型状态管理

### 3.1 当前代码事实（精简版）

**3 层状态机**：
```
Layer A: credentials.availability_state   (PG 共享, 60s recover tick)
Layer B: credentials.circuit_state         (in-memory, **每节点独立**)
Layer C: cmb.available                     (PG 共享, 路由 source of truth)
```

**回测时间表**（核心，老板要求的"不同时间"）：

| 失败 Kind | 回测时间 | 回测方式 | 触发器 |
|-----------|----------|----------|--------|
| transient | 60s | 自然过期 | circuit.Manager |
| timeout | 60s | 自然过期 | circuit + cmb |
| network | 2 min | cmb.unavailable_at | credential_recovery |
| rate_limit | 15 min | exp backoff (900s × N) | circuit |
| upstream_down | 30s → 30min exp | 自然过期 | circuit |
| concurrent | 5 min | 固定 | circuit |
| auth_failed | **永久**（等 5 min fast-reprobe 或 1 h v2） | 真实 /v1/models + mini chat | credential_probe_v2 |
| auth_revoked | **永久**（admin 介入） | — | — |
| quota (balance) | **永久**（admin 充值） | — | — |
| quota (periodic) | weekly/monthly（quota_recover_at） | 自然过期 | credential_recovery |
| quota (permanent) | **永久** | — | — |
| mnf_streak (5/2min) | 2 min | 自然过期 | credential_recovery.mnfCooling |

**"状态变化即时影响路由"的执行时序**：
```
T+0:    executor.Execute() 收到 execErr
T+0:    同步触发 e.Circuit.RecordFailure()        ← in-memory, < 1μs (本节点立即)
T+0:    异步触发 e.writeCredentialStateOnError()  ← DB 写, ~10ms
T+0+ε:  provider.InvalidateAllCandidateCache()    ← in-process, < 1μs (本节点立即)
T+0+:   下一次 PlanCandidates() 走 filterAvailable → 看到新状态

跨节点 (71↔184) 视角:
T+0~30s: 对方节点的 candCache TTL 才过期
```

**3 个独立延迟带**（按 user 关注度排序）：

```
                     同节点              跨节点
circuit.Manager      < 1 μs             **不可见**（每节点独立，30s 内不感知）
cmb.available        < 1 μs             0~30s（candCache TTL）
availability_state   < 1 μs             0~30s（同上）
```

### 3.2 改进方向

| 优先级 | 改动 | 代价 | 老板请确认 |
|--------|------|------|-----------|
| **P0** | 删 dead `writeModelLevelFailure`（writer.go:296-379） | 低（grep 调用点 + 替换） | 必要 |
| **P0** | credential_recovery 也清 cmb 原因（防"cmb=TRUE 但 availability=cooling"假阴） | 低（加 1 个 SQL） | 必要 |
| **P0** | recent_success_rate 阈值从 0.3 恢复 0.5 | 低（改 1 个常量） | 必要（注释为 TEMPORARY） |
| **P1** | Redis pub/sub 跨节点同步 candidate cache | 中（新增依赖） | **暂不做**（成本高，非当前痛点） |
| **P1** | KindNetwork 进 checker 失败率（当前排除） | 低 | 请确认 |
| **P2** | circuit.Manager 同步到 Redis | 高 | **暂不做** |
| **P2** | model_probe_state + candidate_failure_logs 加 tenant_id | 中 | R40-R43 风险 |
| **P3** | pool size metric（防 minimax-m3 事件重演） | 低 | nice-to-have |

---

## 四、T4：路由决策记录 + 请求/错误管理 + 滑动窗口

### 4.1 当前代码事实（精简版）

**4 层 trace**（从内存到 DB）：
```
L1 audit.Event (in-memory, 17 字段, 含 DecisionTrace any)
  ↓
L2 Trace (PlannedCandidates / BlockedCandidates / Chosen / FailureReason)
  ↓
L3 routing_decision_log 表 (PG, JSONB decision_trace, **V06 验证通过**)
  ↓
L4 request_logs 行 (failure_detail_code / failure_stage / error_kind)
```

**19 ErrorKind → 路由决策的 AND/OR 组合规则**：
```
最终路由通过 = 
   cmb.available=TRUE                            [OR 关系，先淘汰]
   AND availability_state IN (ready, cooling→recover_at>now)
   AND model_probe_state NOT IN (broken_confirmed)
   AND v_routable_credential_models.is_routable=TRUE
   AND circuit.Allow()=true                      [本节点 in-memory, 不同步]
   AND limiter.Acquire()=ok                      [本节点 in-memory]
   AND fpSlots.RoutingEligible()=true            [Redis 同步]
   AND recent_success_rate IS NULL OR >= 0.3    [TEMPORARY, 应 0.5]
```

**老板请注意**：**8 个 AND 条件，缺一不可**，但**淘汰顺序没有优先级**（任一为 false 就丢，可能错杀）。

### 4.2 改进方向

| 优先级 | 改动 | 代价 |
|--------|------|------|
| **P0** | candidate_failure_logs 加 per-attempt latency 列 + migration | 低 |
| **P0** | health_tracker Redis callhist entry 用真 request_id（executor.go:804, 1049） | 低 |
| **P1** | vendor response body 脱敏（cap 200 + redact PII） | 低 |
| **P1** | recent_success_rate window 24h（覆盖更长历史） | 中（291 migration + 调用点） |
| **P2** | tenant filter 加入 dashboard | 低 |
| **P2** | (provider, model, error_kind, time_bucket) material view | 中 |
| **P3** | request_logs.request_body 强制脱敏 | 中 |

---

## 五、跨主题关联 + 优先级汇总

### 5.1 4 个主题的 P0 总数 = 11 项

| 主题 | P0 改动数 | 主要方向 |
|------|-----------|----------|
| T1 | 3 | 启用 reclaim、修注释、hasPin 过期 |
| T2 | 2 | Signature 字段、thinking 流式 |
| T3 | 3 | 删 dead code、假阴、阈值恢复 |
| T4 | 3 | per-attempt latency、真 request_id、body 脱敏 |
| **总计** | **11** | 可在 4 个独立 PR 中提交 |

### 5.2 实施顺序建议

```
第 1 周（最关键 PR）：
  PR-1 (T1): fix(credentialfpslot): enable reclaim + fix TTL comments + hasPin expiry
  PR-2 (T2): feat(ir): add thinking signature field + signature_delta stream handling
  PR-3 (T3): refactor(credentialstate): drop writeModelLevelFailure + restore 0.5 threshold
  PR-4 (T4): feat(telemetry): per-attempt latency + real request_id in health tracker

第 2 周（按主题逐一）：
  PR-5 (T1 P1): feat(credentialfpslot): tenantID in Redis key
  PR-6 (T2 P1): test(relay): thinking + tool_use mixed fixture (opus-4-8)
  PR-7 (T3 P1): feat(credentialhealth): KindNetwork in checker
  PR-8 (T4 P1): feat(telemetry): vendor body sanitization

第 3+ 周 (P2/P3，按需）：
  跨节点同步、tenant_id 补全、pool size metric 等
```

### 5.3 跨主题风险

| 风险 | 主题 | 影响 |
|------|------|------|
| circuit.Manager 不跨节点 | T1+T3 | 71 熔断开 → 184 还选 → 失败累积 → candidate_failure_monitor auto-cool → **60s 内自我修复** |
| recent_success_rate 0.3 TEMPORARY | T3+T4 | 资源泄漏修复后**必须**恢复 0.5，否则 degraded 永不复原 |
| signature 丢失 | T2 | 仅 Anthropic 客户端 + multi-turn round-trip 触发，OpenAI 客户端无影响 |
| fp_slot key 不含 tenantID | T1+T3+R40 | 多租户 credential 共享时撞 pin（当前所有 tenant='default'，暂未触发） |

---

## 六、暂不做（Out-of-Scope）

为避免范围蔓延，**明确以下暂不实施**：

1. ❌ Redis pub/sub 跨节点同步 candidate cache — 成本高，非当前痛点
2. ❌ circuit.Manager 同步到 Redis — 同上
3. ❌ IdentityPool 接入或删除 — 探索报告半成品，不在审计范围
4. ❌ 多租户 tenant_id 全表回填 — R40-R43 长期项目，单独规划
5. ❌ 重构 SSE data 3 次 unmarshal — 重构风险 > 收益，P2 待评估
6. ❌ IR Index 语义重构 — 同上

---

## 七、Plan 模式等待指令

**我现在停在 Plan 模式**，等待老板确认：

1. **T2 症状**：opus-4-8 到底是什么问题？tool_use 缺失 / signature 400 / thinking 卡住 / 其他？
2. **T3 回测时间粒度**：我列的 12 种 kind × 回测时间是否够细？要不要拆出"连续失败 3 次" vs "首次失败" 的不同行为？
3. **T1 客户指纹**：老板只关心"代码正确"还是"我要看测试证明"？
4. **批量授权**：4 个 P0 PR 是分开合还是 1 个 PR 一次合？

**收到老板指令后，我会**：
- 用 `/handoff` 把当前所有产出打包成 markdown
- 在新会话里转到 agent/code 模式逐步实施
- 每个 PR 完成后做 post-audit
