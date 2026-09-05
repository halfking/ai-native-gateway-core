# 2026-08-18 — routing-v2 resolve 状态与实际不一致 & 配额自检队列无消费者

> 154 生产环境 `/routing-v2?tab=resolve` 用 `glm-5.2` 做凭据路由解析时，
> 智谱AI 供应商下的节点（cred 22 zhipu-roocode-v2）实际可用（上游 5 小时
> 窗口凌晨 5 点已重置，且当晚仍有成功调用），但页面显示不可用；自检也
> 一直没发现恢复。

## 1. 故障摘要

| 项 | 值 |
|---|---|
| 表象 | resolve 页智谱AI 节点常红（`availability_suspended` / `quota_permanent`） |
| DB 现场 | `credentials` id=22: `quota_state='permanently_exhausted'`、`availability_state='suspended'`、`quota_recover_at=NULL`，自 2026-07-08 起未变 |
| 实际状态 | 上游可用：`credential_state_log` 显示 cred 22 glm-4.7 在 2026-08-17 22:14 仍有成功调用 |
| 关键日志 | `credential probe v2: fast probe queue full`（每个探测周期都报，creds 4/22/25/34 全部丢弃）<br>`routing resolve: ursm v2 view out of order`（resolve 每次刷新都在报） |

## 2. 根因（三条独立的链）

### 2.1 配额探测队列没有消费者（自检失察的直接原因）

- 154 运行时 `LLM_GATEWAY_USE_NEW_PROBE_MODE=true`（systemd unit 的
  `Environment=...=false` 被 `EnvironmentFile=/etc/llm-gateway-go/env` 覆盖）。
- 新探测模式下 `cmd/gateway/main.go` 刻意跳过 `credProbeV2.Start()`，
  但 `PeriodicQuotaProbe`（5min）/`BalanceQuotaProbe`（2min）**无条件启动**，
  仍然往 `credProbeV2.fastReprobeQueue`（cap 64）里投递探测。
- `run()` 是该队列唯一的消费者；`Start()` 被跳过后队列永远无人读取，
  64 槽填满后**每一次投递都被静默丢弃**（`SubmitFastProbe` 的 `default`
  分支只打一条 WARN）。
- 结果：所有 `periodic_exhausted` / `permanently_exhausted` 凭据永远
  等不到探活翻回；`permanently_exhausted` 又没有 `quota_recover_at`，
  时间恢复通道也不存在 → 无限挂起。
- 新模式的每日自检（`credential_selfcheck.go pickModels`）只选
  `is_routable=TRUE` 的模型，挂起凭据 0 个可选 → 只写
  `no_routable_models` 占位，同样探测不到。

### 2.2 resolve 页 URSM 运行时覆写是死代码（状态显示层的两个 bug）

`admin/routing.go handleRoutingResolve`：

1. **循环副本 bug**：`for i, c := range candidates` 里 `defaults(&c)` 和
   全部 URSM 覆写都作用在循环副本上，从未写回 `candidates[i]`。运行时
   字段（available / circuit_state / cooling_until / fail_streak…）实际
   永远是零值，URSM 知道的"节点实际可用"无法在页面上体现。
2. **索引配对 bug**：`FilterAndScore` 返回前会 `scoreAndSort` 按分数排序，
   views 与候选不保序。旧代码按索引一一配对，顺序不一致时全部跳过
   （"view out of order" 刷屏），可用节点可能继承别的节点的不可用状态。

### 2.3 5 小时窗口的误分类与恢复时间计算

- 智谱 GLM Coding Plan 的限额是 5 小时固定窗口（北京时间 00/05/10/15/20
  滚动）。报文若无 reset 时间戳且不含 `window_type`，旧 `quotaResetsRe`
  不认得"5 hours/5小时"措辞 → 落 `KindQuotaPermanent` →
  `permanently_exhausted` + `recover_at=NULL`（cred 22 于 2026-07-08
  即如此写入，此后再未恢复）。
- `inferQuotaRecoverAt` / `errorsx.NextQuotaReset` 无 5 小时分支：即使
  分类对了（periodic），恢复时间也会被算成"次日 UTC 零点"（北京 08:00），
  凌晨 5 点就该恢复的窗口被人为多挂 3 小时。

## 3. 修复

| 文件 | 修复 |
|---|---|
| `bg/credential_probe_v2.go` | 新增 `StartFastProbeConsumer(ctx)`：只跑 fast-reprobe 队列消费循环，跳过 legacy 每小时 `cycleAll`；`run()` 增加 `legacyCycle` 参数；补 `fastProbeQueueLen()` 观测 |
| `cmd/gateway/main.go` | 新探测模式跳过 `Start()` 时改为调用 `StartFastProbeConsumer()`，配额探测投递不再被丢弃 |
| `admin/routing.go` | 覆写块提取为纯函数 `applyURSMOverlay`：views 按 `(credential_id, raw_model)` 建映射配对（不再依赖返回顺序）；通过指针写回切片（循环副本 bug）；`candidate` 类型提升为包级 `resolveCandidate`；`resolveRuntimeDefaults` 兜底保持 T4 契约（Redis miss 不显示不可用） |
| `errorsx/classify.go` | `quotaResetsRe` 增加 5 小时窗口关键词（`five hour` / `5 hours` / `hour-5` / `5小时` 等）→ 走 `KindQuotaPeriodic`；`NextQuotaReset` 增加 5h 分支（下一北京时间 5 小时边界） |
| `domains/credential/writer.go` | `inferQuotaRecoverAt` 增加 5h 分支（`nextFiveHourBoundary`，UTC+8 对齐 00/05/10/15/20）；拆出可注入 `now` 的 `inferQuotaRecoverAtNow` |

## 4. 测试

- `admin/routing_resolve_overlay_test.go`：乱序 views 按键配对、写回生效
  （Redis miss 保持默认可用）、cool_until 熔断、SQL 否决原因保留。
- `bg/credential_probe_v2_consumer_test.go`：无消费者时队列 64 封顶 →
  `StartFastProbeConsumer` 排空；consumer-only 模式不跑 `cycleAll`；
  生命周期幂等（含 -race）。
- `domains/credential/writer_test.go`：5h 报文恢复时间 = 下一 5 小时边界
  （03:30 北京 → 05:00，而非次日 UTC 零点）；边界对齐性质测试。
- `errorsx/classify_test.go`：5 小时窗口报文（英文/中文/window_type/
  hour-5）→ `KindQuotaPeriodic`；`NextQuotaReset` 5h 边界 + 月度语义不回归。

## 5. 上线后自愈路径

1. 部署后 `StartFastProbeConsumer` 开始消费队列：`BalanceQuotaProbe`
   2 分钟内即会探活 cred 22（`default_probe_model='glm-5.1'），成功后
   `writeHealth` 写 `quota_state='ok'` + `availability_state='ready'`
   （2026-08-07 P0 修复已保证硬配额守卫放行探活成功路径）。
2. resolve 页 `applyURSMOverlay` 生效后，运行时列（熔断/冷却/失败计数）
   反映 URSM 真实状态；"view out of order" 日志消失。
3. 此后智谱 5h 限额耗尽会走 periodic 通道：恢复时间对齐下一 5 小时
   边界，到期 `credential_recovery` 自动翻回，探活兜底。

## 6. 追加（同日第二起）：minimax-m3 "没有可用节点"（no_candidates）

用户复报 `minimax-m3` 解析无可用节点。生产证据：

- `request_logs`：2026-08-18 13:41:53 与 13:45:41 两条
  `error_kind=no_candidates`；
- 执行器日志：`executor: no candidates after router,
  input_candidates=2, reasons={availability_check_failed:2}`；
- URSM 快照（tenant=default）：(36, minimax-m3)
  `available=f, fail_streak=3, cool_until=13:35:17` —— cool 已过期
  6 分钟仍不可用；(32, minimax-m3) 无 Redis key（从未观测）→ 请求面
  T4 保护性拒绝；(21, MiniMax-M3) 可用但不在该请求的输入候选里；
- 13:50 服务重启后恢复（内存态重建 + 后续成功事件清除禁用位）。

### 根因：URSM 熔断"冷却过期不半开"的读写不对称死锁

- 写侧 `record_request.lua`：fail_streak 达限 → `disabled=1,
  available=0, cool_until_ms=now+cool`；其恢复分支（lua:232）只在
  **新事件到达**时清禁用位（半开语义）。
- 读侧 `store/pipeline.go`：`available = hash["available"]=="1"` 直接
  信任粘滞位；`cool_until` 只会在"未来"强制不可用，**过期不恢复**。
- 路由不派流量 → 没有新事件 → 写侧恢复永不触发；fast-probe 队列
  又是死的（本日修复 1）→ 没有探活事件 → 节点不可用直到进程重启。

### 修复

`domains/ursm/v2/store/pipeline.go`：`cool_until_ms` 存在且已过期时，
将节点读为 available（半开重试探）。与写侧 lua:232 语义对齐：若下一
请求再失败，写侧按 `cool × 2^disable_count`（封顶 30min）指数退避重新
禁用。`disabled=1` 且无 cool 窗口（管理持有类）不自动恢复。

测试：`pipeline_test.go` 新增过期 cool 半开 / 未来 cool 仍拦 / 无 cool
的 disabled 仍拦 三个用例。

## 7. 提交前审计修正

审计并快进同步了 6 个他人提交（v1603 release、credential 17 follow-up、
URSM k2 key primitives、质量页面等），本修复文件与其没有重叠，未丢弃任何
远程改动。额外修正：

1. **手工禁用优先级**：`apply_admin.lua` 的 force-disable 会写
   `manual_hold=1` 与 `available=0`，但可能保留此前自动熔断的
   `cool_until_ms`。半开逻辑现在显式跳过 `manual_hold=1`，防止手工禁用
   在旧 cool 到期后被意外放回路由；补回归测试。
2. **fast-probe 生命周期闭环**：开启 queue consumer 后，周期性 quota
   worker 可重复提交同一 credential，而每项会等待 5 分钟。现按
   credential 去重，任务结束/队列满/关停时均释放 pending 标记；延迟
   goroutine 纳入 `probeWG`，`Stop()` 等待其退出。gateway shutdown 先停
   periodic/balance producer 再停 consumer，避免末尾 tick 向已停止消费者
   入队；补 -race 测试。
3. **5h 文本精度**：英文 5h 正则加词边界，避免把 `25 hours` / `15 hours`
   尾部的 `5 hours` 误判为 5 小时窗口；保留中文无词边界场景的保守偏早
   恢复策略（探活会纠偏）；补正反例测试。
4. **测试质量**：移除 `ursmViewKey` collision 测试中的恒真条件。

