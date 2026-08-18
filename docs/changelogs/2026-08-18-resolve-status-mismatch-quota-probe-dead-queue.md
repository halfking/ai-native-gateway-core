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
