# D04 队列与并发域 子代理报告（窗口：`24c5c545a..3a750b3a7`）

> R75 留档注：本报告为只读子代理（Explore）原文，主代理逐条亲读复核后处置。
> 复核结论：发现 #1（drainWorker 不看 stopCh）已由并行 R73 轮（5955fcdb0 P2-3/M-6）收口；
> #2（dead 计数虚高）已由十六轮（3909d56e0 P2 快赢 markDead RowsAffected）收口；
> #3 接受并留档（本轮轮文档登记）；N2 由本轮落地 gauge 三件套；N3 维持挂账；
> N4 由本轮收口（休眠读路径补 expires_at 过滤）。

> 审计依据：`docs/audit/playbook/conventions.md` + `docs/audit/playbook/domains/D04-queue-concurrency.md`。本域窗口改动面：`internal/sessionv2mirror/replay.go`(+33483058e, e2b91fa36)、`replay_drain_test.go`(新增 334 行)、`settings/spec_sessions_v2.go`(+test)、`cmd/gateway/turn_logs_aggregator.go`(+flush_test)、`domains/session/v2/session_writer_v2.go` / `turn_logs_writer.go` / `cache_v2_file.go`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（并发/退出缺陷，触发条件为大积压） | **生产路径 drainWorker 没有任何可取消 ctx，`Stop()` 退化为"无界等待全量排空"**。装配点传的是 `context.Background()`（cmd/gateway/main.go:2585），因此 `drainWorker` 的 `ctx.Err()` 检查（internal/sessionv2mirror/replay.go:310）在生产中恒为 nil，唯一停止信号 `stopCh` 只在 `run` 的 tick 间隙被 select（replay.go:230-244），tick 中途不可中断；`Stop()`（replay.go:209-222）等 `doneCh` → 等 `wg.Wait()` → 等 worker 空流确认收工。旧行为每 tick 最多回放一个批次（100 行）即返回，33483058e 改为"排空才收工"后，若 outbox 因写侧长时间故障积压，关机路径 `stopSessionMirrorOutboxReaper`（cmd/gateway/session_v2_init.go:145-149，先于 `pools.CloseAll()`）将阻塞整个排空时长。非泄漏（goroutine 有 `wg` 界），是优雅退出缺口 | main.go:2585; replay.go:209-222, 230-244, 271-283, 307-332 | `Stop()` 里派生可取消子 ctx（或 drainWorker 内 `select stopCh`）：停止接受新 claim、仅等在途回放收尾；配一条"Stop 期间不再 claim"的钉桩测试 |
| 2 | P3（观测口径） | **dead 计数器可虚高**：`markDead` 的 UPDATE 带 `AND status='claimed'` 守卫（replay.go:476-480），但 `mirrorReplayDeadTotal.Inc()` 与 `result="dead"` 无条件自增（replay.go:489-490）。R34 注释自述的租约竞态——孤儿回收把行还给 pending、二号 worker 重新 claim 后，一号 worker 的 markDead UPDATE 匹配 0 行但计数照加——会一 RIP 两计；DB 故障时 UPDATE 失败也照加。帮助文案声称 "rows permanently dead-lettered"（replay.go:106-112），并行 4 worker 后竞态窗口略宽于串行版 | replay.go:471-491, 106-112 | 增量前检查 `CommandTag.RowsAffected()>0`，或把 Help 措辞改为"dead-letter 判定事件数" |
| 3 | P3（行为变更留档） | **WriteStages 批量化后 turn 级 all-or-nothing**：session_writer_v2.go 的 stage 循环改为一次 `WriteStages`（domains/session/v2/session_writer_v2.go:707-737），`buildTurnLogsInsert` 中任一 stage 的 `event_data` marshal 失败即整批放弃（domains/session/v2/turn_logs_writer.go:127-142），INSERT 失败同理——旧行为逐行写、只丢坏行。turn logs 本是 best-effort（仅记日志不失败写），语义可接受，但"单行坏 payload 拖垮同轮其余 stage 日志"是窗口内新引入的放大面 | turn_logs_writer.go:127-142; session_writer_v2.go:715-737 | 接受并留档；若要收紧，marshal 失败可只剔除坏行重批（不实现） |

**专项承接现状复核（R72 挂账三项，均未落地，与上轮一致）：**

| 项 | 现状 | 证据 | 落地建议 |
|---|---|---|---|
| N2 积压零观测 | **仍未实现**。全仓无 turn-logs backlog/pending-session/最老行龄 gauge | 无定义点可引；惯例锚点 bg/metrics.go:215-234（`llm_gateway_hot_table_backlog_rows` + `…_oldest_row_age_seconds` 配对） | gauge 对注册放 bg/metrics.go（沿用 R47/R48 配对惯例：行数 + 最老 pending 行龄 + distinct pending session 数），刷新挂 bg/partition_manager.go 已有的 TTL 清扫 tick，一条 `SELECT count(*), count(DISTINCT (tenant_id,session_id)), now()-MIN(started_at) FROM session_turn_logs WHERE expires_at > NOW()`。**覆盖索引 `(expires_at,tenant_id,session_id,started_at)` 的必要性随 gauge 而来**：常态表近空时现有 `idx_session_turn_logs_expires` 够用；gauge 恰恰要在积压（大表）时保持探针廉价，index-only scan 才有价值——加索引前按 conventions §5 走 sequence 登记通道核对远端编号 |
| N3 聚合器无 leader 选举 | **现状不变**：main.go:1282-1313 仍是裸 goroutine + 5min ticker，双实例各轮询 100 session/5min，无分布式锁、无 claim/lease。R72 事务化后正确性已闭环（见健康面 #1），按 D04 清单 #5 属"DB 幂等收敛"分支合规，吞吐不扩展依旧 | cmd/gateway/main.go:1282-1313 | 维持挂账；若要扩吞吐，套 settle/affinity 的 acquireSweepDistLock 门控（bg 惯例），而非复制 claim 机制 |
| N4 GetStageLogs 不过滤 expires_at | **SQL 仍未过滤**：`GetStageLogs`/`GetAllSessionLogs` 均 `WHERE tenant_id AND session_id [AND turn_no]`、无 `expires_at > NOW()`（domains/session/v2/turn_logs_writer.go:166-176, 212-230）。全仓 grep 证实两者**零生产调用方**（仅测试）；admin 面实际读的是已聚合列 `sessions.turn_logs_summary`，`session_turn_logs` 的运行时读者只有聚合器（均带 expires_at 过滤）和 TTL 清扫 | turn_logs_writer.go:166-176, 212-230 | "过期行可读 ~24h" 目前是**休眠 API 面**而非活体 admin 路径。建议低成本默认安全化：两查询补 `AND expires_at > NOW()`；或至少在函数头注释钉死"接入 admin 前必须补过期过滤" |

## 二、核实为健康的面

- **R72-F1 事务化 flush 未被后续 merge 破坏，六案 + SQL 形状三案齐备**：`AggregateAndFlush` 单事务 = `FOR UPDATE` 锁 sessions 行并在同一语句读回 `turn_logs_summary`（cmd/gateway/turn_logs_aggregator.go:141-147, 196-215）→ Go 侧 `mergeSummaries` per-turn stages 数组 union+去重（turn_logs_aggregator.go:316-342）→ 整列 `SET turn_logs_summary = $1::jsonb` 无 `||`（:152-158）→ id 键控 DELETE → Commit。测试：六个行为案 + 三个形状案全部在位（turn_logs_aggregator_flush_test.go:43-206）。
- **并行 drain 退出条件正确，无提前退出丢行、无永久不退出**：`drainEmptyConfirmations=2` 连续空批确认（replay.go:305-332）。兄弟 worker 未提交 claim 事务/钩子侧并发 INSERT 造成的短读最坏导致单 worker 早退，但其余 worker 或该 worker 自身后续 claim 继续排空；彻底集体早退的残差 = 已 claim 行留待孤儿回收（5min 租约）或下个 tick（30s），是延迟不是丢失。不空转：requeue 回退 ≥30s > 同 tick 内可重claim（replay.go:453-456），claim 失败立即退出（测试钉住恰 1 次），写预算 2s 有界。goroutine 有 `wg.Wait()` 界，无泄漏。
- **max_attempts 热更新闭环成立**：spec 已登记且 Min/Max=5/30、HotReload、ScopePlatform 与 Go 钳制常量逐项一致（settings/spec_sessions_v2.go:147-160 ↔ replay.go:56-57），`requeue` 每次调用经 `currentMaxAtts` 现读；spec 计数测试 + 钳制边界测试 + 登记回归守卫三层钉桩。与在途任务交互：调低只在下次失败时判死、dead 行不复活（`status='claimed'` 守卫）、调高不影响已 dead 行，均与 DescriptionLong 声明一致。
- **reaper 与 drain 互斥**：run 循环串行、tick 不重叠（replay.go:230-244）；Start 幂等（started/stopped 旗标，:195-201）；跨实例靠 FOR UPDATE SKIP LOCKED claim + 行级 status 守卫 + writer request_id 幂等收敛。
- **聚合器与 turn_logs_writer 分工边界清晰**：写侧 `WriteStages` 整轮一条 INSERT、原子可见、共享单一 expires_at（turn_logs_writer.go:88-125），聚合器 5min 读侧 flush；`WriteStage` 兼容包装已零生产调用（test-only），有头注释指引（:76-82）。
- **metric 改名已文档化**：`llmgw_session_mirror_outbox_replays_total` → `session_mirror_outbox_replays_total` + 新增 `session_mirror_outbox_dead_total`（replay.go:103-112），仓外 Grafana/告警断流风险已在 docs/db-changelog.md:738-745 挂运维通知，非静默破坏。
- **cache_v2_file.go 的 `TTL()` accessor**（domains/session/v2/cache_v2_file.go:187-197）：nil-safe 只读方法，服务读/删 TTL 口径一致的启动期断言，不触及任何队列/锁语义。
- `go vet ./internal/sessionv2mirror/` 干净。

## 三、未覆盖项与原因

- **双实例下 claim/孤儿回收/判死竞态的真库实测**——需真 PostgreSQL 与并发实例环境；本审计以代码路径推演 + R34/R72 既有钉桩为准，未实跑。
- **drainWorker 的多 worker 并行场景无行为级测试**——replay_drain_test.go 只单测单 worker 的 drainWorker 与 `mirrorReplayWorkers` 边界；`tick` 的 4-worker 扇出本身零测试。建议后续补 scriptedDB 下的 `tick()` 级并发测试（-race）。
- **PendingSessions GROUP BY 查询在积压态的实库执行计划**——需要真库 EXPLAIN 验证现有 `idx_session_turn_logs_expires` 在大表下的代价，据此裁决 N2 覆盖索引；无真机。
- **D04 §3 清单 1/2/3/7（Governor 链、队列满行为、权重钳位、retry budget）**——窗口改动面不触出口调用点与 Governor，未重扫。
