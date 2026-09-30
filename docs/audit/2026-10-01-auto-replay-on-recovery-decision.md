# 「恢复即自动回放」行为变更决策材料(owner 决策,不动代码)

- 日期:2026-10-01(local)
- 上游:docs/audit/2026-09-30-hotzone-o5o1r9-closure.md §1.4(「如需恢复即自动回放,属行为变更……留 owner 决策」)
- 性质:**只出材料。** 代码事实按 origin/main 935be9493 实勘,锚点随文。

## 一、现状(变更的起点)

PG 恢复监听器(`cmd/gateway/main.go:5694` `DBStatusAvailable` 分支)只做三件事:
`sessionMgr.SetDegradedMode(false)`、`telemetryClient.SetDegraded(false)`、`ttlManager.ExitDegradedMode`
——**不触发任何回放**。回放全部手动,三个入口:

| 入口 | 路径 | 语义 |
| --- | --- | --- |
| ring buffer | `POST /internal/telemetry/fallback-buffer/replay` | **破坏性弹出**(失败 requeue),注释明确「Replay is only ever triggered manually」(ring_buffer.go:247) |
| 文件(generic) | admin → `GenericRecovery.Recover`(单文件,后台任务,`Status(id)` 查询) | 逐条 `ReplayFallback`,仅回放 `request_log`/`request_wal`,session 记录跳过并提示走会话恢复;`HasLoss` 护栏阻止带损归档(generic_recovery.go:124-127) |
| 文件(session) | admin → `Recovery` | 会话快照/轮换记录恢复 |

降级进入条件:monitor 10s×3 连败(~30s 潜伏);恢复条件:10s×3 连续成功(monitor.go:150-171)。
fallback 双层:FileWriter(磁盘,持久)+ RingBuffer(内存 cap=10000,随进程死亡清零)。

## 二、变更提案与三个必须回答的风险维度

提案:恢复监听器在 `DBStatusAvailable` 后自动对积压的 fallback 内容执行回放
(ring buffer replay + 当日/近期文件 GenericRecovery)。

### 1. 回放风暴(replay storm)

- **时机最脆弱**:Available 翻转时 PG 刚恢复,生产流量同时重连,回放写放大正好叠加在
  连接池爬坡期。硬停 1h 的窗口,ring buffer 最多 10000 条 + 文件侧无上限(245 日 WAL 量级 GB)。
- 必备约束(若做):①**稳定窗**——Available 后延迟 N 秒(建议 60-120s)且期间未翻回 Degraded
  才触发,天然回避 flap;②**分批限速**——按固定速率分批,不一次性灌入;③**单飞**——
  自动回放全局互斥(与手动回放也互斥),`Sync.Map` 任务表已有基础(generic_recovery.go tasks)。

### 2. 顺序(order)

- 文件内:单遍顺序读,文件序=写入序,`GenericRecovery` 天然保序。
- 跨文件:同日 base → `-01` → `-02`;`ListBackupFiles` 按日期字符串排序已满足
  (`sessions-YYYY-MM-DD` < `sessions-YYYY-MM-DD-NN`,file_reader.go:157-159)。
  跨日按日期升序。
- **双源重叠**:fallback 写入同时进 FileWriter 与 RingBuffer(multi_writer.go),
  自动回放若两源都扫,同一条目会重放两次——幂等必须先成立(见下),且建议
  文件优先、ring buffer 殿后(文件是持久真相,ring buffer 只是补进程未落盘的尾部)。

### 3. 幂等边界(idempotency)

- `request_log`/`request_wal` 按 `record_key`(`:insert`/`:update` 独立 key)UPSERT,
  重放同一 key 无害(closure §1.3);`request_log` 回放走 telemetryClient 带 R44
  exactly-once hook,重放不产生重复侧效。
- **归档联动是幂等的实际载体**:现在 clean+全部成功才归档,`HasLoss`/失败保留原文件
  (generic_recovery.go:117-139)。自动回放沿用该语义即自带「重跑安全」——已归档文件
  不再被扫到,未归档文件重放是 UPSERT no-op。**不引入 watermark/已回放标记,复杂度
  不值得**:归档即水位。
- 重试上界:FailureCount>0 不归档 → 下轮自动重放会重试;需要**尝试次数上限或退避**
  (如文件级 attempts 计数),否则毒记录(永远回放失败)导致每轮恢复都全量重扫。
- 边界明确不自动:session 记录(GenericRecovery 本就跳过);镜像 tenant 目录分裂的
  对账(O2)不属于回放职责。

### 4. 次生行为面(若做,须一并定)

- degraded 抖动窗:触发后 PG 再挂 → 回放写失败再进 fallback;失败条目不丢(文件保留),
  但需要「回放中 PG 翻 Degraded → 立即中止本轮」的取消路径。
- 观测:回放任务已有 Status API;自动触发需补「自动回放触发/完成/失败」日志与指标,
  否则运维分不清「数据是回放来的还是实时写的」。
- 多实例:本地单容器无此问题;多副本部署(245/154)恢复时刻不同步,自动回放会跨副本
  重复触发——单飞必须带分布式语义(Redis 锁),多实例下风险显著放大,**这是分阶段的最大理由**。

## 三、分级建议

| 阶段 | 内容 | 行为变更 |
| --- | --- | --- |
| 0(推荐先做) | 恢复时自动**探测+告警**:Available 后查 ring buffer stats 与未归档文件列表,积压>0 即告警+日志附 runbook 命令,仍由人按键 | 无(只加观测) |
| 1 | bounded 自动回放:稳定窗 120s+单飞(单实例内存锁)+分批限速+仅 generic 类型+失败保留文件+attempts 上限 | 中 |
| 2 | 多实例分布式锁+session 恢复自动化 | 大,不建议近期 |

**决策点**:① 是否接受阶段 0(纯观测,无回放行为);② 阶段 1 的稳定窗/限速参数与
attempts 上限取值;③ 单实例先行、多实例挂起是否可接受(245/154 是单活还是多活决定此项)。
