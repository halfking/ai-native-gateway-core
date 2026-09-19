# 两层优先级选路 + 边际成本感知优化(2026-09-19)

## 背景

审计现状(`domains/streaming/executors/router.go` 为生产唯一选凭据链路):

1. **优先级是稳定分区排序,不是硬两层**。`stablePartitionPriority` 把
   `manual_priority>0 AND quota_state='ok'` 的候选排前,但不看该优先节点是否
   已满:全部优先节点饱和时,首跳仍是优先节点,请求在 governor 队列里等待
   (MaxQueueWaitMS),而不是立刻落到非优先层。dispatch 层的
   `ApplySoftPenalty`(QueueFull/GovernorSaturated 软降级到队尾)事后修正,
   但计划时点没有两层语义。
2. **成本评分不区分计费模式**。`calculateCostPenalty`(RT-2)对未知价格返回
   最大惩罚 1.0——订阅/计划凭据(token_plan 等)往往没有 per-1M 单价,被误判
   为"最贵",而其边际成本实际≈0(月费已沉没)。CostWeight 默认 0(关闭),
   "最低成本与效率均衡"没有真正生效。
3. **5h/周窗口用量不参与路由**。`credentials.plan_quota_used_percent`
   (balance_floor_guard 探测写入,zhipu/minimax)没有进入 Candidate,路由无法
   感知"计划额度还剩多少、别把某个 5h 窗打爆、也别让窗口重置时额度白白浪费"。
4. **供应商价格填充缺一级**。候选取价 LATERAL 只区分
   credential 级/全局(`credential_id = c.id OR credential_id IS NULL`),
   `pricing_plans.scope='provider'` 的供应商级价格行与全局行混在同级,仅靠
   effective_from 决胜负。

## 需求(用户原话拆解)

- 优先级多节点平衡与非优先级平衡是**两层**;只有优先节点**都满掉了**才用非优先的。
- 根据供应商信息自动填充成本,用**最低成本与效率均衡**优化选路。
- 周期性 tokenplan **成本固定,尽量用完不浪费**;但注意 **5 小时与周**的用量问题。
- 全局重构简化:简洁而不简单,支撑业务发展。

## 设计

### 1. 两层优先级(计划时点饱和感知)

tier bucket 内 P2C 排序后,按**选路段**分区(`partitionBySelectionLayer`,
替代原 `stablePartitionPriority` 在 planByTier 中的调用):

```
L1 优先层 = isPriorityBucketEligible(c)(priority 标志 + quota ok)且有余量
L2 常规层 = 标准节点(无标志 / quota 失格)
L3 兜底层 = 已满的优先节点(仍可路由,排所有健康标准节点之后)
```

- `priorityNodeSaturated`:容量已知(ConcurrencyLimit/有效容量>0)且实时
  in-flight ≥ 容量 → 满;容量未知 fail-open 视为未满。信号源与
  `calculateConcurrencyScore` 同一套(LiveLoad 优先,回退 Limiter)。
- L1 内部保持 P2C 负载分相对顺序(优先节点之间平衡);首跳加权抽签只在
  **服务段**内进行:L1 非空→L1;L1 空→L2;全空→整个列表。
- 语义:**优先层有节点有余量时,流量只在优先层内平衡;优先节点全部满掉
  才落到常规层;已满的优先节点只作最后的 failover 兜底**(不再让新请求
  在 governor 队列排队等优先槽而闲置标准节点)。
- dispatch 层 `ApplySoftPenalty` 保持不变(派发时点的GovernorSaturated
  兜底),两层语义在计划/派发两个时点各有一份,互为校验。
- 观测:`classifyPrioritySelection` 的标签语义不变(spillover_to_non_priority
  现在真正对应"优先层全满外溢")。

### 2. 边际成本模型(计费感知)

`calculateCostPenalty` 改为按计费轮取边际成本:

- **Round 1**(free/token_plan/code_plan/agent_plan/monthly):成本已沉没,
  边际成本=0 → 惩罚 0。与"周期性计划尽量用完"一致:计划凭据在任何成本维度
  都不被压低。
- **Round 2**(per_token 按量):混合单价(in+out per 1M)/软帽线性归一,
  未知价格=最大惩罚 1.0(unknown ≠ free,RT-2 语义保留)。

`DefaultLoadScoreWeights().CostWeight` 默认 **0.15**(env
`LLM_GATEWAY_ROUTING_W_COST` 可调/置 0 关闭)——效率(延迟 0.3+质量 0.2)
仍主导,成本在同效率候选间倾斜。同步演进 RT-2 钉桩测试:Off 钉桩改为显式
置 0(不再钉"默认关")。

### 3. 计划额度惩罚(5h/周)

- Candidate 新增 `PlanQuotaUsedPercent *float64`(SQL 取
  `c.plan_quota_used_percent`,探测数据,无=NULL)。
- `planQuotaPenalty`:仅 Round 1 且有探测数据时 = used%/100(钳 0..1);
  其余(free 无探测/PAYG)=0。**用法量高的计划凭据惩罚高** → 流量导向剩余
  额度多的计划凭据:窗口重置前把额度用掉(不浪费),同时避免把单一 5h/周窗
  提前打爆(429→冷却→整窗闲置才是最大浪费)。
- 权重 env `LLM_GATEWAY_ROUTING_W_PLANQUOTA` 默认 0.10;与三新惩罚同样折入
  首跳抽签份额(`firstHopLotteryWeights` 扩为四惩罚归一)。

### 4. 供应商价格填充

候选 SQL 的 pricing_plans LATERAL 优先级改为
**credential 级 > provider 级(`pp.provider_id = p.id`) > 全局**,
供应商维护的价格表真正参与自动填充。

### 5. 死代码清理(简化)

- 删除 Router 的 Bandit 死路径:`Router.Bandit`/`Router.BanditFlusher` 字段、
  `banditOrder`、planByTier 分支、main.go 注释块、router_bandit_test.go。
  (`domains/credential` 包内 BanditScorer 保留——reputation worker 在用。)
- 删除 `filterAvailableWithStateManager`(DEPRECATED,零调用)。

## 不做的事

- 不引入新表/迁移(plan_quota 列已存在于 701)。
- 不改 sticky/指纹机制本身(9-19 刚落地,三新惩罚保留)。
- 不动 URSM v2 authoritative 过滤链(冷却/健康仍由它负责)。
- 不改 deploy-local.sh(锁定文件)。

## 验证

- 单测:两层分区语义、饱和感知、边际成本(计费轮)、计划额度惩罚、价格
  LATERAL、权重默认值契约。
- 本地 deploy-local.sh 部署 + admin resolve 冒烟。
- 154 生产部署(deploy 脚本)+ 观测
  `llmgw_routing_priority_candidates_selected_total` 与成本分布。
- 252 确认无 llm-gateway-go 运行实例(docker/系统进程)。
