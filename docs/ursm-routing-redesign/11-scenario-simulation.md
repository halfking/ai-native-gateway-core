# 全场景业务模拟与处理逻辑

> 基于 `10-final-architecture.md`、`08-unified-resource-pipeline.md`、现有
> `errorsx` 错误分类、Executor 重试及 Session/Hook 体系进行纸面推演。

## 1. 不变量

| 编号 | 不变量 |
|---|---|
| I1 | 同一 `(session_id, std_model)` 首次成功后优先保持同一 `credential + provider + raw_model`。 |
| I2 | 未发生上游实际错误，不得因价格、P2C、协议排序、资源竞争或配置刷新切换。 |
| I3 | 上游错误切换只能发生在客户端尚未收到不可回放响应内容时；客户端 cancel/deadline 不属于上游错误。 |
| I4 | 客户端 RPM/TPM/并发限制在进入供应商候选循环前执行，释放必须幂等。 |
| I5 | 供应商 FP slot 限制身份数量；Concurrency slot 限制活跃请求数量，两者不可混用。 |
| I6 | 状态、路由绑定、资源获取、上游结果和成本均通过 Hook 暴露，但 Hook 不得静默改变核心路由。 |
| I7 | 所有自动切换必须记录具体 `route_change_reason`（如 `upstream_timeout`、`upstream_5xx`、`credential_fatal`）及原/新节点。 |

## 2. 统一决策状态

```text
RequestStarted
  -> Authenticated
  -> ClientRateAllowed
  -> BindingResolved
  -> CandidateSelected
  -> ProviderGatePassed
  -> FPSlotAcquired
  -> ConcurrencyAcquired
  -> CircuitAllowed
  -> UpstreamStarted
  -> UpstreamSucceeded | UpstreamFailed | ClientCanceled | DeadlineExceeded
  -> ResourcesReleased
  -> StateRecorded
  -> BindingCreated/Kept/Invalidated
```

每个状态转换携带 `request_id`, `session_id`, `std_model`, `credential_id`,
`provider_id`, `raw_model`, `binding_level`, `attempt_index`, `reason`。

## 3. 正常稳定请求

### 场景 S1: 新会话首次成功

```text
输入: stdModel=gpt-4o, 无 route binding, 两个可用候选
决策: CostScorer 综合价格/时延/稳定性/压力选 node A
执行: FP -> Conc -> CB -> upstream A(rawModel-A)
结果: 200
动作:
  - 创建 session route binding
  - cached_tokens>0 或 prompt_tokens>阈值 -> 强绑定
  - 否则 -> 软绑定
  - StateRecorder.Record(success)
  - 更新 node 指标、成本和 binding last_success_at
切换: 不切换
Hook: binding_miss, cost_scored, route_selected, upstream_success, binding_created
```

### 场景 S2: 已绑定会话连续成功

```text
输入: 已存在 binding(A, provider-A, raw-A)
决策: 只返回 A，不执行 P2C/价格重排/协议亲和重排
资源: 只申请 A 的 FP/Concurrency
结果: 200
动作: 保持 binding，刷新 last_success_at 和资源时间
切换: 不切换，即使 B 更便宜或更快
Hook: binding_hit, provider_gate_check, resource_released
```

### 场景 S3: 同一会话的短请求

```text
输入: 无缓存、prompt 小于阈值
策略: 软绑定，但在 reevaluate_interval 内仍优先当前 binding
只有达到请求次数/时间窗口且满足劣化阈值，才进入重选
约束: 重选不是每请求执行，避免厂商缓存和连接状态抖动
```

## 4. 客户端限制场景

### 场景 S4: API Key RPM 超限

```text
顺序: Auth -> RPM/TPM check -> 返回 429
不执行: 路由、FP、Concurrency、上游调用、状态失败记录
释放: 若此前已 acquire key concurrency，必须幂等 release
前端: 429 + X-RateLimit-* + Retry-After
Hook: auth_completed, rate_limit_checked, request_rejected
```

### 场景 S5: API Key 并发超限

```text
Redis Lua 原子检查并递增 key counter
超限: 返回 429，不占用供应商资源
成功: 将 release token 放入请求 cleanup，不能只按 keyID 无条件 DECR
原因: 同一 key 的并发请求必须各自持有 token，避免重复释放
```

### 场景 S6: 客户端主动取消

```text
分类: KindCanceled / client cancellation
动作: 取消当前 upstream context，释放 Conc/FP/Key token
状态: 不降低供应商健康度，不触发 binding failover
响应已开始: 直接结束流，不尝试拼接其他供应商响应
Hook: upstream_cancelled, resource_released, state_record_skipped
```

## 5. 供应商稳定性与自动切换

### 场景 S7: 连接前供应商资源暂时不足

```text
未绑定会话: 当前候选 FP/Conc 获取失败，可尝试下一候选
已绑定会话: 只等待绑定节点至 resource_wait_deadline
超时: 429/503，不伪造 upstream_error，不改 binding
```

### 场景 S8: 绑定供应商连接超时

```text
条件: 已真正发起 upstream，未向客户端输出响应
动作:
  1. 记录 KindTimeout/KindNetwork，并记录 `error_origin=upstream`；客户端 context deadline/cancel 不得触发 failover
  2. StateRecorder.Record(failure)
  3. 使当前 binding 进入 failover candidate 状态
  4. 释放旧资源
  5. 选择下一候选并重试
  6. 成功后原子更新 binding，并记录 route_change_reason
前端: 只收到最终成功响应
```

### 场景 S9: 上游 5xx / 503 overload

```text
分类: KindConcurrent 或 KindTransient，按错误主体精确分类
动作: 当前尝试失败 -> 更新节点错误率/熔断计数 -> 允许 failover
绑定级别: 强绑定也允许切换，因为已发生实际上游错误
切换上限: 遵守 max_attempts、总 deadline 和客户端重试预算
```

### 场景 S10: 上游 429 rate limit

```text
普通配额 429: KindRateLimit，记录 retry_after，当前节点冷却
下一候选: 只有当前请求尚未输出内容时才切换
供应商级 429: provider gate 降低可用权重，避免同供应商兄弟凭据同时冲击
不能把 429 一律当成凭据永久故障
```

### 场景 S11: 认证失败/凭据撤销

```text
分类: KindAuth/KindAuthRevoked，属于 credential fatal
动作:
  - 立即禁用当前 credential/model binding
  - 清理 FP pin 和 session binding
  - 记录 state_change + route_change_reason=credential_fatal
  - 当前请求在未输出响应时切换候选
后续请求: 永不继续尝试该凭据，直到探测或管理员恢复
```

### 场景 S12: 配额耗尽

```text
永久配额: KindQuotaPermanent/KindQuotaBalance -> credential fatal，允许切换
周期配额: KindQuotaPeriodic -> 按 recover_at 冷却，允许切换
不应只修改 node Redis；必须同步持久化 credential/model availability
```

## 6. 客户端可见性边界

### 场景 S13: 非流式响应，上游失败后切换

```text
上游 A 未返回有效 body -> B 重试 -> 返回 B 的 200
客户端无感，响应只写一次
request_logs 保留 attempt A/B，最终 route 与切换原因可审计
```

### 场景 S14: 流式响应尚未输出 token

```text
连接/首 token 阶段失败 -> 可切换 B
必须由 response writer/stream tracker 确认尚未写出首个不可回放 SSE 事件
否则不能透明拼接，需返回明确错误或可恢复协议结果
```

### 场景 S15: 流式响应已输出部分 token

```text
A 已输出部分内容后断流 -> 不允许静默切 B 并拼接
动作: 记录 KindStreamTimeout/stream_interrupted，结束当前响应
若协议支持客户端续传，可由显式 retry 请求触发新路由
原因: 自动拼接会重复/丢失 token，破坏语义和计费
```

### 场景 S16: 内容过滤/客户端参数错误

```text
KindContentFilter: 短路，不换供应商；相同输入通常会被同类策略拒绝
KindUnsupportedFeature/ToolCallIdMismatch: 客户端或协议问题，不惩罚凭据，不切换
KindContextLength: 先执行一次专用 trim/retry；不能把相同 oversized body 发给下一节点
```

## 7. 成本与路由评分

### 场景 S17: 首次选路

候选必须先经过硬过滤：provider/credential/model 可用、客户端策略、资源上限、
协议能力和上下文窗口。只有硬过滤后的候选进入评分。

```text
score = price_weight * price_score
      + latency_weight * latency_score
      + stability_weight * stability_score
      + error_weight * (1 - error_rate)
      + pressure_weight * (1 - resource_pressure)
      + cache_affinity_bonus
```

约束:
- 价格必须按预计 input/output tokens 计算，而非只比较单价。
- 稳定性使用有样本门槛的 EWMA/p95，样本不足使用 provider 先验。
- 错误率按错误类别分层，客户端错误不计入供应商错误率。
- 成本评分只能影响未绑定会话或允许重评估的软绑定会话。
- 评分输入必须来自同一统计窗口；RouteNode 提供价格/历史成功率/p95，NodeState 提供实时错误与资源压力。
- 缺失价格、成功率或延迟数据时使用保守先验，不得把缺失值当作零成本或零延迟。

### 场景 S18: 成本更低候选出现

```text
强绑定: 不切换，优先缓存收益
软绑定: 只有在重评估窗口且预期节省超过切换成本时切换
切换成本包括: prompt cache 丢失、预热、失败风险、资源获取、额外延迟
```

### 场景 S19: 成本与稳定性冲突

```text
最低价格候选 stability/error 不达硬门槛 -> 不可选
可靠性约束优先于价格排序
可用候选不足时进入受控降级，并记录 cost_guardrail_blocked
```

## 8. 探测、恢复和配置变化

### 场景 S20: 主动探测失败

```text
探测失败只更新 node/model health，不直接改变 session binding
已绑定会话只有实际请求失败才切换
但若管理员/安全策略明确禁用 credential，binding 立即失效并返回可见错误
```

### 场景 S21: 探测恢复

```text
恢复节点进入候选池，影响新会话和软绑定重评估
不抢占强绑定会话，不强制迁移已有会话
```

### 场景 S22: rawModel/价格/供应商开关变更

```text
新会话: 使用最新配置
存量强绑定: 保持原 rawModel，除非显式 route reset 或安全失效
存量软绑定: 下一个合法重评估点检查配置版本；切换记录原因
```

### 场景 S23: Redis/DB 故障

```text
Redis 故障:
  已绑定: 本地短 TTL binding 快照可继续使用；无法确认资源所有权时宁可 503，不随机换路由
  未绑定: 使用 provider cache/DB；两者均不可用则返回 503
DB 故障:
  已绑定: Redis session binding + node state 继续服务
  未绑定: 使用 model index，但必须检查 binding/credential safety cache
恢复后: 异步对账，不在请求中强制改路由
```

## 9. Hook 与插件在场景中的约束

| Hook | 必须记录 | 可做 | 不可做 |
|---|---|---|---|
| `route_selected` | 候选、评分、成本估计、绑定级别 | 观测/建议 | 静默改选节点 |
| `provider_gate_check` | 开关、限额、当前压力 | 调整等待预算 | 绕过硬限制 |
| `upstream_failure` | 错误分类、状态码、是否已输出 | 触发 failover 建议 | 将客户端错误标为供应商错误 |
| `binding_invalidated` | 原节点、原因、时间 | 审计/通知 | 无错误静默解除 |
| `resource_released` | token、持有时长、是否超时 | 统计/修复 | 重复释放其他请求 |
| `cost_scored` | 每候选成本和权重 | 调整策略参数 | 覆盖稳定性硬门槛 |

插件失败默认 fail-open 还是 fail-closed 必须按 Hook 分类配置；治理/审计插件不应阻塞数据面。

## 10.1 纸面审计修正项

| 严重性 | 发现 | 修正 |
|---|---|---|
| P0 | `KindTimeout` 可能来自客户端 context，不能直接触发供应商切换 | 增加 `error_origin=client/upstream/gateway`；只有 upstream 才允许 failover |
| P0 | 流式“已开始”不能只用 HTTP 状态判断 | 维护 `response_started` 和 `first_replayable_event_sent`；任一为真则禁止透明切换 |
| P0 | API Key 并发释放若只按 keyID DECR，重复释放会污染计数 | 使用 request-scoped lease token，Release 必须幂等并校验 token |
| P1 | 强绑定错误切换后旧 binding 可能被并发请求覆盖 | 使用版本号/CAS 更新 binding；旧版本更新失败必须重新读取 |
| P1 | 成本、时延、稳定性统计窗口不一致会导致错误排序 | 统一统计窗口、样本门槛和先验值，并将统计版本写入决策审计 |
| P1 | Hook/插件可能修改核心路由造成隐式漂移 | Hook 默认只读建议；改变路由必须返回显式 decision override 并通过策略校验 |
| P2 | 仅写 node state 不足以支持模型候选发现 | 首次成功必须原子维护 node HASH、model index、session binding 三者 |

## 11. 场景验收矩阵

| 类别 | 必测场景 | 核心断言 |
|---|---|---|
| 稳定性 | S1/S2/S8/S9/S11 | 稳定不切，真实上游错误可透明切换 |
| 缓存 | S2/S14/S15/S22 | 未错误不改 provider/rawModel，部分流不可拼接 |
| 限流 | S4/S5/S7 | 客户端限流先执行，供应商资源独立计数且幂等释放 |
| 成本 | S17/S18/S19 | 硬门槛优先，绑定收益计入切换成本 |
| 数据故障 | S20/S21/S23 | 已绑定优先恢复原路由，不随机漂移 |
| Hook | 全部 | 每次决策可追踪，插件不能破坏核心不变量 |

## 12. 结论

系统采用 **稳定优先、错误切换、成本约束** 的三层决策：

1. 稳定会话保持绑定，保护厂商缓存和上下文成本。
2. 只有可归因的上游错误允许自动 failover，且必须在响应可回放时完成。
3. 首次选路和合法重评估使用价格、预计 token 成本、时延、稳定性、错误率、资源压力综合评分。
4. 客户端限流、供应商并发、FP 身份限制分别治理，任何插件通过 Hook 参与但不能破坏核心不变量。
